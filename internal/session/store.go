// 本文件实现记录编解码、会话 ID 校验、FileStore 与 MemStore。
// 记录类型与 Store 接口的契约在 types.go；跨包签名以 docs/agent-architecture.md §2.4、§8.1 为准。
package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"anc-dsk/internal/llm"
)

const (
	// maxTranscriptBytes 是单文件读取上限，超出时只读尾部（§8.1）。
	maxTranscriptBytes int64 = 50 << 20
	// sessionsSubdir 是会话文件相对存储根目录的位置（§8.1）。
	sessionsSubdir = "sessions"
	// sessionFileMode 与 sessionDirMode 是 transcript 文件与目录的权限。
	sessionFileMode os.FileMode = 0o600
	sessionDirMode  os.FileMode = 0o700
	// headLineLimit 限制超限文件「另读首行」的最大字节数。
	headLineLimit int64 = 1 << 20
	// readChunkSize 是逐行读取的缓冲区大小。
	readChunkSize = 64 << 10
)

// ErrCorruptLines 表示 transcript 中有行被跳过。此时 Load 仍返回可用的记录，
// 调用方可用 errors.Is(err, ErrCorruptLines) 判断并按需继续使用 []Record。
var ErrCorruptLines = errors.New("session: corrupt transcript lines")

var (
	errStoreClosed   = errors.New("session: store is closed")
	errTruncatedLine = errors.New("truncated line without newline terminator")
	errEmptyLine     = errors.New("empty line")
	errMetaOnAppend  = errors.New("session: session_meta can only be written by CreateSession")
)

// CorruptLine 描述一行被跳过的损坏记录。Number 是读取窗口内的 1 基行号。
type CorruptLine struct {
	Number int
	Err    error
}

// CorruptError 汇总一次读取中被跳过的损坏行。它同时实现 ErrCorruptLines 哨兵匹配。
type CorruptError struct {
	SessionID string
	Path      string
	Lines     []CorruptLine
}

// Error 实现 error 接口，最多展示前三条损坏行的原因。
func (e *CorruptError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "session: %q has %d damaged line(s)", e.SessionID, len(e.Lines))
	shown := len(e.Lines)
	if shown > 3 {
		shown = 3
	}
	for _, l := range e.Lines[:shown] {
		fmt.Fprintf(&b, "; line %d: %v", l.Number, l.Err)
	}
	if len(e.Lines) > shown {
		fmt.Fprintf(&b, "; and %d more", len(e.Lines)-shown)
	}
	return b.String()
}

// Is 报告 target 是否为 ErrCorruptLines。
func (e *CorruptError) Is(target error) bool { return target == ErrCorruptLines }

// Skipped 返回被跳过的行数。
func (e *CorruptError) Skipped() int { return len(e.Lines) }

// Unwrap 返回全部损坏行的原因，便于 errors.Is/As 逐条检查。
func (e *CorruptError) Unwrap() []error {
	errs := make([]error, 0, len(e.Lines))
	for _, l := range e.Lines {
		errs = append(errs, l.Err)
	}
	return errs
}

// ValidateID 校验会话 ID：只允许字母、数字、下划线与连字符，长度 1–64，
// 且首字符必须是字母或数字。用于阻断 `../` 之类的路径穿越（§8.1）。
func ValidateID(id string) error {
	if len(id) == 0 || len(id) > 64 {
		return fmt.Errorf("%w: length %d", ErrInvalidID, len(id))
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return fmt.Errorf("%w: %q", ErrInvalidID, id)
		}
	}
	return nil
}

// Encode 把记录编码成单行 JSON（不含换行）。未知类型返回错误。
func Encode(r Record) ([]byte, error) {
	if !isKnownType(r.Type) {
		return nil, fmt.Errorf("session: unknown record type %q", r.Type)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("session: encode record %q: %w", r.ID, err)
	}
	return b, nil
}

// Decode 解析一行 JSON。空行、未知类型与缺少必需载荷的记录返回错误。
func Decode(line []byte) (Record, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return Record{}, errEmptyLine
	}
	var r Record
	if err := json.Unmarshal(line, &r); err != nil {
		return Record{}, fmt.Errorf("session: decode record: %w", err)
	}
	if !isKnownType(r.Type) {
		return Record{}, fmt.Errorf("session: unknown record type %q", r.Type)
	}
	if err := validatePayload(r); err != nil {
		return Record{}, err
	}
	return r, nil
}

func isKnownType(t RecordType) bool {
	switch t {
	case RecordSessionMeta, RecordMessage, RecordCompact, RecordUsage, RecordMode, RecordTurnEnd:
		return true
	}
	return false
}

func validatePayload(r Record) error {
	switch r.Type {
	case RecordMessage:
		if r.Message == nil {
			return errors.New("session: message record without message")
		}
	case RecordCompact:
		if r.Compact == nil {
			return errors.New("session: compact record without compact")
		}
	case RecordUsage:
		if r.Usage == nil {
			return errors.New("session: usage record without usage")
		}
	case RecordTurnEnd:
		if r.TurnEnd == nil {
			return errors.New("session: turn_end record without turnEnd")
		}
	}
	return nil
}

// needsSync 报告该类型写入后是否必须 fsync。mode 等低价值记录只写页缓存。
func needsSync(t RecordType) bool {
	switch t {
	case RecordSessionMeta, RecordMessage, RecordCompact, RecordUsage, RecordTurnEnd:
		return true
	}
	return false
}

func newRecordID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("rec_%d", time.Now().UnixNano())
	}
	return "rec_" + hex.EncodeToString(b[:])
}

func defaultNow() time.Time { return time.Now() }

func cloneRecord(r Record) Record {
	out := r
	if r.Message != nil {
		m := cloneMessage(*r.Message)
		out.Message = &m
	}
	if r.Compact != nil {
		c := *r.Compact
		out.Compact = &c
	}
	if r.Usage != nil {
		u := *r.Usage
		out.Usage = &u
	}
	if r.TurnEnd != nil {
		t := *r.TurnEnd
		out.TurnEnd = &t
	}
	if r.Meta != nil {
		m := make(map[string]string, len(r.Meta))
		for k, v := range r.Meta {
			m[k] = v
		}
		out.Meta = m
	}
	return out
}

func cloneMessage(m llm.Message) llm.Message {
	out := m
	out.Blocks = append([]llm.ContentBlock(nil), m.Blocks...)
	for i := range out.Blocks {
		if out.Blocks[i].Input != nil {
			out.Blocks[i].Input = append(json.RawMessage(nil), out.Blocks[i].Input...)
		}
	}
	if m.Usage != nil {
		u := *m.Usage
		out.Usage = &u
	}
	return out
}

// normalizeAppend 为 Append 补齐记录 ID 与时间，并校验类型与载荷。
func normalizeAppend(ctx context.Context, r Record, now func() time.Time, newID func() string) (Record, error) {
	// Store 契约要求写入不得因调用方 ctx 已取消而拒绝（types.go:85）。
	_ = ctx
	if r.SessionID == "" {
		return r, errors.New("session: record without session id")
	}
	if err := ValidateID(r.SessionID); err != nil {
		return r, err
	}
	if !isKnownType(r.Type) {
		return r, fmt.Errorf("session: unknown record type %q", r.Type)
	}
	if r.Type == RecordSessionMeta {
		return r, errMetaOnAppend
	}
	if err := validatePayload(r); err != nil {
		return r, err
	}
	if r.ID == "" {
		if newID != nil {
			r.ID = newID()
		} else {
			r.ID = newRecordID()
		}
	}
	if r.Time.IsZero() {
		if now != nil {
			r.Time = now()
		} else {
			r.Time = time.Now()
		}
	}
	return r, nil
}

// FileStore 是每会话一个 JSONL 文件的实现（§8.1）。
// 进程内只应存在一个实例（由 App 持有）；同一会话的追加与读取由会话级互斥锁串行化。
type FileStore struct {
	mu       sync.Mutex
	dir      string // 会话文件目录（<存储根>/sessions）
	files    map[string]*fileWriter
	locks    map[string]*sync.Mutex
	closed   bool
	syncFile func(*os.File) error // fsync 注入缝；nil 时使用 (*os.File).Sync
	now      func() time.Time
	newID    func() string
	maxBytes int64
}

type fileWriter struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	closed bool
}

var _ Store = (*FileStore)(nil)

// NewFileStore 创建存储。dir 是存储根目录，会话文件写入 dir/sessions；
// 目录不存在时以 0700 创建。
func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session: empty storage directory")
	}
	root := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(root, sessionDirMode); err != nil {
		return nil, fmt.Errorf("session: create storage directory: %w", err)
	}
	return &FileStore{
		dir:      root,
		files:    make(map[string]*fileWriter),
		locks:    make(map[string]*sync.Mutex),
		now:      defaultNow,
		newID:    newRecordID,
		maxBytes: maxTranscriptBytes,
	}, nil
}

// pathOf 校验 ID 并返回会话文件路径，二次确认结果未越出会话目录。
func (s *FileStore) pathOf(sessionID string) (string, error) {
	if err := ValidateID(sessionID); err != nil {
		return "", err
	}
	p := filepath.Join(s.dir, sessionID+".jsonl")
	rel, err := filepath.Rel(s.dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, sessionID)
	}
	return p, nil
}

func (s *FileStore) lockFor(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[sessionID]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[sessionID] = l
	}
	return l
}

func (s *FileStore) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *FileStore) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *FileStore) newRecordID() string {
	if s.newID != nil {
		return s.newID()
	}
	return newRecordID()
}

func (s *FileStore) sync(f *os.File) error {
	if s.syncFile != nil {
		return s.syncFile(f)
	}
	return f.Sync()
}

// CreateSession 创建会话并写入 session_meta 首行；已存在返回 ErrSessionExists。
func (s *FileStore) CreateSession(ctx context.Context, meta Meta) error {
	_ = ctx // 写入不受调用方 ctx 取消影响，见 types.go Store 契约。
	if err := ValidateID(meta.ID); err != nil {
		return err
	}
	path, err := s.pathOf(meta.ID)
	if err != nil {
		return err
	}
	createdAt := meta.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.nowTime()
	}
	rec := Record{
		ID:        s.newRecordID(),
		Type:      RecordSessionMeta,
		SessionID: meta.ID,
		Time:      createdAt,
		Meta:      map[string]string{"workDir": meta.WorkDir},
	}
	if meta.Title != "" {
		rec.Meta["title"] = meta.Title
	}
	if meta.Model != "" {
		rec.Meta["model"] = meta.Model
	}
	line, err := Encode(rec)
	if err != nil {
		return err
	}

	lock := s.lockFor(meta.ID)
	lock.Lock()
	defer lock.Unlock()

	if s.isClosed() {
		return errStoreClosed
	}
	s.mu.Lock()
	_, open := s.files[meta.ID]
	s.mu.Unlock()
	if open {
		return fmt.Errorf("%w: %s", ErrSessionExists, meta.ID)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, sessionFileMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrSessionExists, meta.ID)
		}
		return fmt.Errorf("session: create %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("session: write %s: %w", path, err)
	}
	if err := s.sync(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("session: sync %s: %w", path, err)
	}

	fw := &fileWriter{f: f, path: path}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = f.Close()
		return errStoreClosed
	}
	if _, ok := s.files[meta.ID]; ok {
		s.mu.Unlock()
		_ = f.Close()
		return fmt.Errorf("%w: %s", ErrSessionExists, meta.ID)
	}
	s.files[meta.ID] = fw
	s.mu.Unlock()
	return nil
}

// Append 追加一条记录。写入使用 O_APPEND，一次性写出「JSON + 换行」；
// 关键记录（meta/message/compact/usage/turn_end）在返回前 fsync。
func (s *FileStore) Append(ctx context.Context, rec Record) error {
	if rec.SessionID == "" {
		return errors.New("session: record without session id")
	}
	sessionID := rec.SessionID
	if err := ValidateID(sessionID); err != nil {
		return err
	}
	rec, err := normalizeAppend(ctx, rec, s.now, s.newID)
	if err != nil {
		return err
	}
	line, err := Encode(rec)
	if err != nil {
		return err
	}

	lock := s.lockFor(sessionID)
	lock.Lock()
	defer lock.Unlock()

	fw, err := s.writer(sessionID)
	if err != nil {
		return err
	}
	buf := append(line, '\n')

	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.closed {
		return errStoreClosed
	}
	if _, err := fw.f.Write(buf); err != nil {
		return fmt.Errorf("session: append %s: %w", fw.path, err)
	}
	if needsSync(rec.Type) {
		if err := s.sync(fw.f); err != nil {
			return fmt.Errorf("session: sync %s: %w", fw.path, err)
		}
	}
	return nil
}

// writer 返回会话的写入句柄；调用方必须持有会话锁。
func (s *FileStore) writer(sessionID string) (*fileWriter, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errStoreClosed
	}
	if fw := s.files[sessionID]; fw != nil {
		s.mu.Unlock()
		return fw, nil
	}
	s.mu.Unlock()

	path, err := s.pathOf(sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
		}
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	fw := &fileWriter{f: f, path: path}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = f.Close()
		return nil, errStoreClosed
	}
	if existing := s.files[sessionID]; existing != nil {
		s.mu.Unlock()
		_ = f.Close()
		return existing, nil
	}
	s.files[sessionID] = fw
	s.mu.Unlock()
	return fw, nil
}

// Flush 对全部打开的文件做 fsync。
func (s *FileStore) Flush(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errStoreClosed
	}
	writers := make([]*fileWriter, 0, len(s.files))
	for _, fw := range s.files {
		writers = append(writers, fw)
	}
	s.mu.Unlock()

	var errs []error
	for _, fw := range writers {
		fw.mu.Lock()
		if !fw.closed {
			if err := s.sync(fw.f); err != nil {
				errs = append(errs, fmt.Errorf("session: sync %s: %w", fw.path, err))
			}
		}
		fw.mu.Unlock()
	}
	return errors.Join(errs...)
}

// Load 读取一个会话的全部记录。首行必须是 session_meta，否则返回 ErrInvalidTranscript。
// 损坏行会被跳过，此时返回的记录仍然可用，同时返回 *CorruptError
// （errors.Is(err, ErrCorruptLines) 为 true）。
func (s *FileStore) Load(ctx context.Context, sessionID string) ([]Record, error) {
	path, err := s.pathOf(sessionID)
	if err != nil {
		return nil, err
	}
	if s.isClosed() {
		return nil, errStoreClosed
	}

	lock := s.lockFor(sessionID)
	lock.Lock()
	defer lock.Unlock()

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
		}
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("session: stat %s: %w", path, err)
	}
	return s.loadRecords(ctx, sessionID, path, f, info.Size())
}

func (s *FileStore) loadRecords(ctx context.Context, sessionID, path string, f *os.File, size int64) ([]Record, error) {
	limit := s.maxBytes
	if limit <= 0 {
		limit = maxTranscriptBytes
	}
	start := int64(0)
	if size > limit {
		start = size - limit
	}

	var head Record
	if start > 0 {
		// 超限时只读尾部；首行单独读取，保证 Load 的首行契约与 Replay 的 Meta 重建仍然成立。
		headLen := min(size, headLineLimit)
		br := bufio.NewReader(io.NewSectionReader(f, 0, headLen))
		line, _ := br.ReadBytes('\n')
		body := bytes.TrimSuffix(line, []byte("\n"))
		body = bytes.TrimSuffix(body, []byte("\r"))
		rec, derr := Decode(body)
		if derr != nil || rec.Type != RecordSessionMeta {
			return nil, fmt.Errorf("%w: %s: first line is not %s", ErrInvalidTranscript, path, RecordSessionMeta)
		}
		head = rec
	}

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("session: seek %s: %w", path, err)
	}
	br := bufio.NewReaderSize(f, readChunkSize)
	if start > 0 {
		// 丢弃窗口首行：它可能从记录中间开始。
		if _, err := br.ReadBytes('\n'); err != nil {
			return nil, fmt.Errorf("%w: %s: no line boundary after offset %d", ErrInvalidTranscript, path, start)
		}
	}

	recs := make([]Record, 0, 16)
	if start > 0 {
		recs = append(recs, head)
	}
	var corrupt []CorruptLine
	lineNo := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("session: load %s: %w", path, err)
		}
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			complete := line[len(line)-1] == '\n'
			body := bytes.TrimSuffix(line, []byte("\n"))
			body = bytes.TrimSuffix(body, []byte("\r"))

			if start == 0 && lineNo == 1 {
				if !complete || len(bytes.TrimSpace(body)) == 0 {
					return nil, fmt.Errorf("%w: %s: line 1 is empty or truncated", ErrInvalidTranscript, path)
				}
				rec, derr := Decode(body)
				if derr != nil {
					return nil, fmt.Errorf("%w: %s: line 1: %w", ErrInvalidTranscript, path, derr)
				}
				if rec.Type != RecordSessionMeta {
					return nil, fmt.Errorf("%w: %s: line 1 is %q, want %q", ErrInvalidTranscript, path, rec.Type, RecordSessionMeta)
				}
				recs = append(recs, rec)
			} else {
				switch {
				case !complete:
					corrupt = append(corrupt, CorruptLine{Number: lineNo, Err: errTruncatedLine})
				case len(bytes.TrimSpace(body)) == 0:
					corrupt = append(corrupt, CorruptLine{Number: lineNo, Err: errEmptyLine})
				default:
					rec, derr := Decode(body)
					if derr != nil {
						corrupt = append(corrupt, CorruptLine{Number: lineNo, Err: derr})
					} else {
						recs = append(recs, rec)
					}
				}
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("session: read %s: %w", path, rerr)
		}
	}
	if start == 0 && len(recs) == 0 {
		return nil, fmt.Errorf("%w: %s: empty transcript", ErrInvalidTranscript, path)
	}
	if len(corrupt) > 0 {
		return recs, &CorruptError{SessionID: sessionID, Path: path, Lines: corrupt}
	}
	return recs, nil
}

// recordHead 是 List 扫描时使用的轻量记录视图，避免反序列化整条消息。
type recordHead struct {
	Type RecordType        `json:"type"`
	Time time.Time         `json:"time"`
	Meta map[string]string `json:"meta"`
}

// List 返回全部会话摘要，按 UpdatedAt 倒序。首行不是 session_meta 的文件被跳过。
func (s *FileStore) List(ctx context.Context) ([]Info, error) {
	if s.isClosed() {
		return nil, errStoreClosed
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("session: list %s: %w", s.dir, err)
	}
	infos := make([]Info, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("session: list: %w", err)
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		if ValidateID(id) != nil {
			continue
		}
		info, ok, err := s.scanInfo(ctx, id, filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if ok {
			infos = append(infos, info)
		}
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].UpdatedAt.Equal(infos[j].UpdatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
	return infos, nil
}

func (s *FileStore) scanInfo(ctx context.Context, id, path string) (Info, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Info{}, false, nil
		}
		return Info{}, false, fmt.Errorf("session: open %s: %w", path, err)
	}
	defer f.Close()

	info := Info{ID: id}
	br := bufio.NewReaderSize(f, readChunkSize)
	lineNo := 0
	for {
		if err := ctx.Err(); err != nil {
			return Info{}, false, fmt.Errorf("session: list: %w", err)
		}
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			complete := line[len(line)-1] == '\n'
			body := bytes.TrimSuffix(line, []byte("\n"))
			body = bytes.TrimSuffix(body, []byte("\r"))
			var h recordHead
			valid := complete && len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &h) == nil && isKnownType(h.Type)
			if !valid {
				if lineNo == 1 {
					return Info{}, false, nil // 首行损坏：整个文件视为无效 transcript。
				}
			} else if lineNo == 1 {
				if h.Type != RecordSessionMeta {
					return Info{}, false, nil
				}
				info.Title = h.Meta["title"]
				info.WorkDir = h.Meta["workDir"]
				if !h.Time.IsZero() {
					info.UpdatedAt = h.Time
				}
			} else {
				if h.Type == RecordMessage {
					info.Messages++
				}
				if h.Time.After(info.UpdatedAt) {
					info.UpdatedAt = h.Time
				}
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return Info{}, false, fmt.Errorf("session: read %s: %w", path, rerr)
		}
	}
	if lineNo == 0 {
		return Info{}, false, nil
	}
	if info.UpdatedAt.IsZero() {
		if st, err := f.Stat(); err == nil {
			info.UpdatedAt = st.ModTime()
		}
	}
	return info, true, nil
}

// Delete 删除一个会话文件并关闭其写入句柄。
func (s *FileStore) Delete(ctx context.Context, sessionID string) error {
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
	s.mu.Lock()
	fw := s.files[sessionID]
	delete(s.files, sessionID)
	s.mu.Unlock()

	if fw != nil {
		fw.mu.Lock()
		fw.closed = true
		cerr := fw.f.Close()
		fw.mu.Unlock()
		if cerr != nil && !errors.Is(cerr, fs.ErrClosed) {
			return fmt.Errorf("session: close %s: %w", path, cerr)
		}
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
		}
		return fmt.Errorf("session: delete %s: %w", path, err)
	}
	return nil
}

// Close 关闭全部文件句柄；可重复调用。
func (s *FileStore) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	writers := make([]*fileWriter, 0, len(s.files))
	for id, fw := range s.files {
		writers = append(writers, fw)
		delete(s.files, id)
	}
	s.mu.Unlock()

	var errs []error
	for _, fw := range writers {
		fw.mu.Lock()
		if !fw.closed {
			fw.closed = true
			if err := fw.f.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
				errs = append(errs, fmt.Errorf("session: close %s: %w", fw.path, err))
			}
		}
		fw.mu.Unlock()
	}
	return errors.Join(errs...)
}

// MemStore 是内存实现，供测试与无盘场景使用。
type MemStore struct {
	mu       sync.RWMutex
	sessions map[string][]Record
	closed   bool
	now      func() time.Time
	newID    func() string
}

var _ Store = (*MemStore)(nil)

// NewMemStore 创建内存存储。
func NewMemStore() *MemStore {
	return &MemStore{
		sessions: make(map[string][]Record),
		now:      defaultNow,
		newID:    newRecordID,
	}
}

// CreateSession 创建会话并写入 session_meta 首行；已存在返回 ErrSessionExists。
func (m *MemStore) CreateSession(ctx context.Context, meta Meta) error {
	_ = ctx
	if err := ValidateID(meta.ID); err != nil {
		return err
	}
	createdAt := meta.CreatedAt
	if createdAt.IsZero() {
		createdAt = m.now()
	}
	rec := Record{
		ID:        m.newID(),
		Type:      RecordSessionMeta,
		SessionID: meta.ID,
		Time:      createdAt,
		Meta:      map[string]string{"workDir": meta.WorkDir},
	}
	if meta.Title != "" {
		rec.Meta["title"] = meta.Title
	}
	if meta.Model != "" {
		rec.Meta["model"] = meta.Model
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errStoreClosed
	}
	if _, ok := m.sessions[meta.ID]; ok {
		return fmt.Errorf("%w: %s", ErrSessionExists, meta.ID)
	}
	m.sessions[meta.ID] = []Record{rec}
	return nil
}

// Append 追加一条记录。
func (m *MemStore) Append(ctx context.Context, rec Record) error {
	rec, err := normalizeAppend(ctx, rec, m.now, m.newID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errStoreClosed
	}
	recs, ok := m.sessions[rec.SessionID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, rec.SessionID)
	}
	m.sessions[rec.SessionID] = append(recs, cloneRecord(rec))
	return nil
}

// Flush 对内存实现是空操作。
func (m *MemStore) Flush(ctx context.Context) error {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return errStoreClosed
	}
	return nil
}

// Load 返回会话的全部记录副本。
func (m *MemStore) Load(ctx context.Context, sessionID string) ([]Record, error) {
	_ = ctx
	if err := ValidateID(sessionID); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, errStoreClosed
	}
	recs, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	out := make([]Record, len(recs))
	for i, r := range recs {
		out[i] = cloneRecord(r)
	}
	return out, nil
}

// List 返回全部会话摘要，按 UpdatedAt 倒序。
func (m *MemStore) List(ctx context.Context) ([]Info, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, errStoreClosed
	}
	infos := make([]Info, 0, len(m.sessions))
	for id, recs := range m.sessions {
		if len(recs) == 0 {
			continue
		}
		info := Info{ID: id}
		head := recs[0]
		info.Title = head.Meta["title"]
		info.WorkDir = head.Meta["workDir"]
		info.UpdatedAt = head.Time
		for _, r := range recs {
			if r.Type == RecordMessage {
				info.Messages++
			}
			if r.Time.After(info.UpdatedAt) {
				info.UpdatedAt = r.Time
			}
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].UpdatedAt.Equal(infos[j].UpdatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
	return infos, nil
}

// Delete 删除一个会话。
func (m *MemStore) Delete(ctx context.Context, sessionID string) error {
	_ = ctx
	if err := ValidateID(sessionID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errStoreClosed
	}
	if _, ok := m.sessions[sessionID]; !ok {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	delete(m.sessions, sessionID)
	return nil
}

// Close 关闭存储；可重复调用。
func (m *MemStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.sessions = make(map[string][]Record)
	return nil
}
