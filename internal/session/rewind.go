package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Baike12/b-desktop/internal/llm"
)

// ErrMessageNotFound 表示 transcript 中不存在指定 ID 的用户消息。
var ErrMessageNotFound = errors.New("session: user message not found")

// RewindCut 返回截断位置：messageID 对应的用户消息记录的下标。
// 只匹配 user 消息（回溯以用户输入为锚点）；找不到返回 ErrMessageNotFound。
func RewindCut(recs []Record, messageID string) (int, error) {
	if messageID == "" {
		return 0, fmt.Errorf("%w: empty message id", ErrMessageNotFound)
	}
	for i, rec := range recs {
		if rec.Type != RecordMessage || rec.Message == nil {
			continue
		}
		if rec.Message.Role != llm.RoleUser || rec.Message.ID != messageID {
			continue
		}
		return i, nil
	}
	return 0, fmt.Errorf("%w: %q", ErrMessageNotFound, messageID)
}

// RewindBefore 把 transcript 截断到 messageID 对应的用户消息之前：
// 该消息及其后的全部记录被丢弃，session_meta 首行保留。
// 截断后文件原子替换；FileStore 上已打开的写入句柄作废，下次 Append 重新打开。
func (s *FileStore) RewindBefore(ctx context.Context, sessionID, messageID string) error {
	_ = ctx
	path, err := s.pathOf(sessionID)
	if err != nil {
		return err
	}
	lock := s.lockFor(sessionID)
	lock.Lock()
	defer lock.Unlock()
	if s.isClosed() {
		return errStoreClosed
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
		}
		return fmt.Errorf("session: open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("session: stat %s: %w", path, err)
	}
	recs, err := s.loadRecords(ctx, sessionID, path, f, info.Size())
	if err != nil && !errors.Is(err, ErrCorruptLines) {
		_ = f.Close()
		return err
	}
	_ = f.Close()

	cut, err := RewindCut(recs, messageID)
	if err != nil {
		return err
	}
	kept := recs[:cut]

	var buf bytes.Buffer
	for _, rec := range kept {
		line, err := Encode(rec)
		if err != nil {
			return fmt.Errorf("session: rewind encode %s: %w", rec.Type, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(s.dir, ".rewind-*.tmp")
	if err != nil {
		return fmt.Errorf("session: create rewind temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(sessionFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: chmod rewind temp: %w", err)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: write rewind temp: %w", err)
	}
	if err := s.sync(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: sync rewind temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: close rewind temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("session: replace %s: %w", path, err)
	}

	// 打开中的写入句柄指向被替换的旧文件：标记关闭并移出登记，
	// 下次 Append 经 writer() 以 O_APPEND 重新打开新文件。
	s.mu.Lock()
	fw := s.files[sessionID]
	delete(s.files, sessionID)
	s.mu.Unlock()
	if fw != nil {
		fw.mu.Lock()
		if !fw.closed {
			fw.closed = true
			_ = fw.f.Close()
		}
		fw.mu.Unlock()
	}
	return nil
}

// RewindBefore 截断内存会话到 messageID 对应的用户消息之前。
func (m *MemStore) RewindBefore(ctx context.Context, sessionID, messageID string) error {
	_ = ctx
	if err := ValidateID(sessionID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errStoreClosed
	}
	recs, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	cut, err := RewindCut(recs, messageID)
	if err != nil {
		return err
	}
	kept := make([]Record, cut)
	for i, rec := range recs[:cut] {
		kept[i] = cloneRecord(rec)
	}
	m.sessions[sessionID] = kept
	return nil
}
