// Package session 定义追加式 transcript 的记录格式与存储接口。
// 本文件只冻结跨包契约；文件实现与重放算法由后续文件补齐。
package session

import (
	"context"
	"errors"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
)

// RecordType 是 JSONL 行类型。
type RecordType string

// 记录类型常量。Type 决定哪些字段有效：
//   - session_meta：Meta 中的 title、workDir、model
//   - message：Message
//   - compact：Compact
//   - usage：Usage
//   - mode：Meta["mode"]
//   - title：Meta["title"]
//   - turn_end：TurnEnd
const (
	RecordSessionMeta RecordType = "session_meta" // RecordSessionMeta 是首行元信息。
	RecordMessage     RecordType = "message"      // RecordMessage 是一条对话消息。
	RecordCompact     RecordType = "compact"      // RecordCompact 是压缩边界。
	RecordUsage       RecordType = "usage"        // RecordUsage 是一次模型调用的用量。
	RecordMode        RecordType = "mode"         // RecordMode 是权限模式变更。
	RecordTitle       RecordType = "title"        // RecordTitle 是会话标题更新。
	RecordTurnEnd     RecordType = "turn_end"     // RecordTurnEnd 是一轮结束。
)

// Meta 是会话元信息，由 CreateSession 写入首行。
// 落盘时转换成 Record 的字段：ID 写入 SessionID，CreatedAt 写入 Time，
// Title、WorkDir、Model 写入 Record.Meta。
type Meta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	WorkDir   string    `json:"workDir"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Compact 是压缩边界记录。它之前的消息在恢复时不再载入。
type Compact struct {
	Trigger    string `json:"trigger"` // auto | manual
	PreTokens  int    `json:"preTokens"`
	PostTokens int    `json:"postTokens"`
	Summary    string `json:"summary"`
	Model      string `json:"model"`
}

// TurnEnd 是一轮结束记录。
type TurnEnd struct {
	TurnID     string `json:"turnId"`
	StopReason string `json:"stopReason"`
	Error      string `json:"error,omitempty"`
	Turns      int    `json:"turns"`
}

// Record 是 JSONL 的一行。所有写入由 agent 负责，Store 只负责落盘。
type Record struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parentId,omitempty"`
	Type      RecordType        `json:"type"`
	SessionID string            `json:"sessionId"`
	Time      time.Time         `json:"time"`
	Message   *llm.Message      `json:"message,omitempty"`
	Compact   *Compact          `json:"compact,omitempty"`
	Usage     *llm.Usage        `json:"usage,omitempty"`
	TurnEnd   *TurnEnd          `json:"turnEnd,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Info 是会话列表项。
type Info struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	WorkDir   string    `json:"workDir"`
	Messages  int       `json:"messages"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store 是会话持久化接口。
// Append 是同步写入：返回 nil 表示数据已交给内核（写入 OS 页缓存），Flush 额外做 fsync。
// 实现不得做「只进内存队列」的假写入，也不得因调用方 ctx 已取消而拒绝写入。
type Store interface {
	// CreateSession 创建会话并写入 session_meta 首行；已存在返回 ErrSessionExists。
	CreateSession(ctx context.Context, meta Meta) error
	// Append 追加一条记录；写入失败必须返回错误。
	Append(ctx context.Context, rec Record) error
	// Flush 把数据 fsync 到磁盘。
	Flush(ctx context.Context) error
	// Load 读取一个会话的全部记录（上限 50MB）。会话不存在返回 ErrSessionNotFound；
	// 首行不是 session_meta 返回 ErrInvalidTranscript。
	Load(ctx context.Context, sessionID string) ([]Record, error)
	// List 返回全部会话摘要，按 UpdatedAt 倒序。
	List(ctx context.Context) ([]Info, error)
	// Delete 删除一个会话及其文件。
	Delete(ctx context.Context, sessionID string) error
	// Close 关闭底层文件句柄；可重复调用。
	Close() error
}

// Loaded 是重放结果。
type Loaded struct {
	Messages        []llm.Message
	Records         int
	Meta            Meta      // 由首行 session_meta 重建
	Boundary        *Compact  // 最后一条压缩边界；nil 表示从未压缩
	Usage           llm.Usage // 全部 usage 记录的 Sum
	LastUsage       llm.Usage // 最后一条 usage 记录；零值表示无。用于判断当前上下文规模
	Mode            string    // 最后一条 mode 记录；空表示未记录
	LastAssistantAt time.Time // 最后一条 assistant 消息的时间，供 microCompact 判断
	Interrupted     bool      // 最后一条 assistant 的 tool_use 没有对应结果
}

// 会话层错误哨兵。
var (
	// ErrSessionExists 表示会话已存在。
	ErrSessionExists = errors.New("session: already exists")
	// ErrSessionNotFound 表示会话不存在。
	ErrSessionNotFound = errors.New("session: not found")
	// ErrInvalidID 表示会话 ID 不合法（含路径分隔符或超长）。
	ErrInvalidID = errors.New("session: invalid id")
	// ErrInvalidTranscript 表示 transcript 结构损坏，例如首行不是 session_meta。
	ErrInvalidTranscript = errors.New("session: invalid transcript")
)
