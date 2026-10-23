// Package workspace 实现跨会话的全局搜索与 git worktree 管理。
//
// 搜索直接扫描会话存档目录下的 JSONL transcript，不依赖索引：每次查询按行流式读取，
// 命中用户/assistant 的文本块与工具名，并把命中窗口拼成单行片段。
// Worktree 管理全部通过 git 命令（exec.CommandContext）完成，工作树统一放在
// <repo>/.b-desktop/worktrees/<branch>，分支已存在时直接挂载该分支。
package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/session"
)

// 搜索参数默认值与上限。
const (
	// DefaultSearchLimit 是未指定 limit 时返回的会话数。
	DefaultSearchLimit = 50
	// MaxSearchLimit 是单次查询返回的会话数上限。
	MaxSearchLimit = 200
	// maxMatchesPerSession 是每个会话最多返回的命中条数。
	maxMatchesPerSession = 5
	// snippetWindow 是片段在命中词两侧保留的字符数。
	snippetWindow = 120
	// searchReadChunk 是逐行读取的缓冲区大小。
	searchReadChunk = 64 << 10
	// sessionsSubdir 与会话存储布局一致：<dataDir>/sessions/<id>.jsonl。
	sessionsSubdir = "sessions"
)

// 搜索阶段错误哨兵。
var (
	// ErrEmptyDataDir 表示未提供会话数据目录。
	ErrEmptyDataDir = errors.New("workspace: empty data directory")
	// ErrEmptyQuery 表示查询串为空。
	ErrEmptyQuery = errors.New("workspace: empty query")
)

// SessionSearchHit 是会话中的一条命中。
type SessionSearchHit struct {
	Role     string    // user、assistant 或 tool
	ToolName string    // 仅 Role 为 tool 时有值
	Line     int       // 命中的 JSONL 行号（1 基）
	Snippet  string    // 压缩空白后的单行片段，命中点两侧各保留 snippetWindow 个字符
	Time     time.Time // 该记录的时间
}

// SessionSearchResult 是一个会话的搜索命中汇总。
type SessionSearchResult struct {
	SessionID  string             // 会话 ID（文件名去掉 .jsonl）
	Title      string             // session_meta 中的标题；缺失时回退为会话 ID
	WorkDir    string             // session_meta 中的工作目录
	UpdatedAt  time.Time          // 全部记录中最晚的时间
	MatchCount int                // 会话内命中总数，可能大于 len(Matches)
	Matches    []SessionSearchHit // 最多 maxMatchesPerSession 条
}

// SearchSessions 扫描会话 JSONL，返回命中 query 的会话，按 UpdatedAt 倒序。
//
// 匹配范围是用户与 assistant 的文本块，以及 assistant 发起的工具名；
// 匹配不区分大小写。limit <= 0 取 DefaultSearchLimit，超过 MaxSearchLimit 时截断。
// 目录不存在时返回空结果而不是错误；损坏的单行被跳过，首行损坏的文件整体跳过。
func SearchSessions(ctx context.Context, dataDir, query string, limit int) ([]SessionSearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("workspace: search sessions: %w", err)
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, ErrEmptyQuery
	}
	needle := strings.ToLower(trimmed)
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	root, err := sessionsRoot(dataDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []SessionSearchResult{}, nil
		}
		return nil, fmt.Errorf("workspace: list sessions in %s: %w", root, err)
	}

	results := make([]SessionSearchResult, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("workspace: search sessions: %w", err)
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		if session.ValidateID(id) != nil {
			continue
		}
		result, ok, err := scanSession(ctx, filepath.Join(root, entry.Name()), id, trimmed, needle)
		if err != nil {
			return nil, err
		}
		if ok {
			results = append(results, result)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].UpdatedAt.Equal(results[j].UpdatedAt) {
			return results[i].SessionID < results[j].SessionID
		}
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// sessionsRoot 返回会话文件目录：优先 <dataDir>/sessions，不存在时退回 dataDir 本身。
func sessionsRoot(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", ErrEmptyDataDir
	}
	root := filepath.Join(dataDir, sessionsSubdir)
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		return root, nil
	}
	return dataDir, nil
}

// scanSession 读取一个会话文件；ok 为 false 表示该文件不是有效 transcript 或没有命中。
func scanSession(ctx context.Context, path, id, query, needle string) (SessionSearchResult, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SessionSearchResult{}, false, nil
		}
		return SessionSearchResult{}, false, fmt.Errorf("workspace: open %s: %w", path, err)
	}
	defer f.Close()

	result := SessionSearchResult{SessionID: id, Title: id}
	headerOK := false
	lineNo := 0
	reader := bufio.NewReaderSize(f, searchReadChunk)
	for {
		if err := ctx.Err(); err != nil {
			return SessionSearchResult{}, false, fmt.Errorf("workspace: search %s: %w", path, err)
		}
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			complete := line[len(line)-1] == '\n'
			body := bytes.TrimSuffix(line, []byte("\n"))
			body = bytes.TrimSuffix(body, []byte("\r"))
			rec, decodeErr := session.Decode(body)
			if decodeErr != nil {
				if lineNo == 1 || !complete {
					// 首行不是合法记录时整个文件不是 transcript；末行可能只写了一半。
					return SessionSearchResult{}, false, nil
				}
			} else {
				if lineNo == 1 {
					if rec.Type != session.RecordSessionMeta {
						return SessionSearchResult{}, false, nil
					}
					headerOK = true
					if title := strings.TrimSpace(rec.Meta["title"]); title != "" {
						result.Title = title
					}
					result.WorkDir = rec.Meta["workDir"]
				}
				at := recordTime(rec)
				if at.After(result.UpdatedAt) {
					result.UpdatedAt = at
				}
				if rec.Type == session.RecordMessage && rec.Message != nil {
					collectMatches(&result, rec, lineNo, query, needle, at)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return SessionSearchResult{}, false, fmt.Errorf("workspace: read %s: %w", path, readErr)
		}
	}
	if !headerOK {
		return SessionSearchResult{}, false, nil
	}
	if result.UpdatedAt.IsZero() {
		if info, err := f.Stat(); err == nil {
			result.UpdatedAt = info.ModTime()
		}
	}
	return result, result.MatchCount > 0, nil
}

// recordTime 返回记录时间；记录未带时间时退回消息时间。
func recordTime(rec session.Record) time.Time {
	if !rec.Time.IsZero() {
		return rec.Time
	}
	if rec.Message != nil {
		return rec.Message.Time
	}
	return time.Time{}
}

// collectMatches 把一条消息中命中的文本块与工具名追加到结果，命中总数不受片段上限影响。
func collectMatches(result *SessionSearchResult, rec session.Record, lineNo int, query, needle string, at time.Time) {
	role := string(rec.Message.Role)
	for _, block := range rec.Message.Blocks {
		switch block.Type {
		case llm.BlockText:
			if !containsFold(block.Text, needle) {
				continue
			}
			result.MatchCount++
			if len(result.Matches) < maxMatchesPerSession {
				result.Matches = append(result.Matches, SessionSearchHit{
					Role:    role,
					Line:    lineNo,
					Snippet: buildSnippet(block.Text, query),
					Time:    at,
				})
			}
		case llm.BlockToolUse:
			if block.ToolName == "" || !containsFold(block.ToolName, needle) {
				continue
			}
			result.MatchCount++
			if len(result.Matches) < maxMatchesPerSession {
				result.Matches = append(result.Matches, SessionSearchHit{
					Role:     "tool",
					ToolName: block.ToolName,
					Line:     lineNo,
					Snippet:  buildSnippet(block.ToolName, query),
					Time:     at,
				})
			}
		}
	}
}

// containsFold 报告 text 是否包含已小写的 needle（大小写不敏感）。
func containsFold(text, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), needle)
}

// buildSnippet 把文本压缩成单行，并在命中点两侧各保留 snippetWindow 个字符。
func buildSnippet(text, query string) string {
	normalized := strings.Join(strings.Fields(text), " ")
	if normalized == "" {
		return ""
	}
	runes := []rune(normalized)
	lower := []rune(strings.ToLower(normalized))
	needle := []rune(strings.ToLower(query))
	idx := -1
	if len(lower) == len(runes) {
		idx = indexRunes(lower, needle)
	}
	if idx < 0 {
		head := runes
		if len(head) > snippetWindow*2 {
			head = head[:snippetWindow*2]
		}
		out := string(head)
		if len(head) < len(runes) {
			out += "…"
		}
		return out
	}

	start := idx - snippetWindow
	if start < 0 {
		start = 0
	}
	end := idx + len(needle) + snippetWindow
	if end > len(runes) {
		end = len(runes)
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	b.WriteString(string(runes[start:end]))
	if end < len(runes) {
		b.WriteString("…")
	}
	return b.String()
}

// indexRunes 返回 needle 在 haystack 中首次出现的下标；不存在返回 -1。
func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
