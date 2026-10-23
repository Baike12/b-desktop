// Package telemetry 持久化模型调用观测，并提供用量汇总与最近请求查询。
//
// 每次模型调用写成一行 JSON（JSON Lines），追加到用户级用量日志
// ~/.b-desktop/usage.jsonl（权限 0600）。Store 实现 agent.MetricsSink，
// 可直接注入 agent.Extensions.Metrics；聚合边界与参考实现一致：
// 按本地日期与模型分组，统计请求数、输入/输出/缓存 token 与总时长。
package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Baike12/b-desktop/internal/agent"
)

// DefaultFileName 是用量日志的默认文件名，放在用户级设置目录下。
const DefaultFileName = "usage.jsonl"

const (
	// dateLayout 是日志中日期字段的格式；按 ISO 日期字符串可直接做范围比较。
	dateLayout = "2006-01-02"
	// unknownModel 是模型名缺失时的占位值，保证聚合键不为空。
	unknownModel = "unknown"
	// maxLineBytes 是单行 JSON 的读取上限；错误信息可能较长，留足余量。
	maxLineBytes = 1 << 20
)

// 存储层错误哨兵。调用方可用 errors.Is 判定。
var (
	// ErrEmptyPath 表示创建 Store 时路径为空。
	ErrEmptyPath = errors.New("telemetry: empty usage log path")
	// ErrInvalidLimit 表示 Recent 的 limit 不是正数。
	ErrInvalidLimit = errors.New("telemetry: invalid limit")
	// ErrInvalidRange 表示统计区间的结束日期早于开始日期。
	ErrInvalidRange = errors.New("telemetry: invalid range")
)

// entry 是 usage.jsonl 中的一行，也是一次模型调用的完整观测。
type entry struct {
	At                       time.Time `json:"at"`
	Date                     string    `json:"date"`
	SessionID                string    `json:"sessionId,omitempty"`
	Model                    string    `json:"model"`
	InputTokens              int       `json:"inputTokens"`
	OutputTokens             int       `json:"outputTokens"`
	CacheReadInputTokens     int       `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens int       `json:"cacheCreationInputTokens,omitempty"`
	DurationMS               int64     `json:"durationMs"`
	StopReason               string    `json:"stopReason"`
	Err                      string    `json:"error,omitempty"`
}

// UsageTotals 是一组请求的用量合计。
type UsageTotals struct {
	// RequestCount 是模型请求次数。
	RequestCount int `json:"requestCount"`
	// ErrorCount 是停止原因为 error 的请求次数。
	ErrorCount int `json:"errorCount"`
	// InputTokens 是未命中缓存的输入 token 数。
	InputTokens int `json:"inputTokens"`
	// OutputTokens 是输出 token 数。
	OutputTokens int `json:"outputTokens"`
	// CacheReadInputTokens 是缓存读取的 token 数。
	CacheReadInputTokens int `json:"cacheReadInputTokens"`
	// CacheCreationInputTokens 是缓存写入的 token 数。
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
	// TotalTokens 是输入、输出与缓存 token 之和。
	TotalTokens int `json:"totalTokens"`
	// DurationMS 是请求耗时合计（毫秒）。
	DurationMS int64 `json:"durationMs"`
}

// add 把一行观测累加进合计。
func (t *UsageTotals) add(e entry) {
	t.RequestCount++
	if e.StopReason == string(agent.StopError) {
		t.ErrorCount++
	}
	t.InputTokens += e.InputTokens
	t.OutputTokens += e.OutputTokens
	t.CacheReadInputTokens += e.CacheReadInputTokens
	t.CacheCreationInputTokens += e.CacheCreationInputTokens
	t.TotalTokens += e.TotalTokens()
	if e.DurationMS > 0 {
		t.DurationMS += e.DurationMS
	}
}

// merge 把另一份合计累加进来。
func (t *UsageTotals) merge(other UsageTotals) {
	t.RequestCount += other.RequestCount
	t.ErrorCount += other.ErrorCount
	t.InputTokens += other.InputTokens
	t.OutputTokens += other.OutputTokens
	t.CacheReadInputTokens += other.CacheReadInputTokens
	t.CacheCreationInputTokens += other.CacheCreationInputTokens
	t.TotalTokens += other.TotalTokens
	t.DurationMS += other.DurationMS
}

// TotalTokens 返回这一行观测的 token 总量：输入、输出与两类缓存之和。
func (e entry) TotalTokens() int {
	return e.InputTokens + e.OutputTokens + e.CacheReadInputTokens + e.CacheCreationInputTokens
}

// ModelUsage 是某个模型在统计区间内的用量合计。
type ModelUsage struct {
	// Model 是模型标识；缺失时为 "unknown"。
	Model string `json:"model"`
	// UsageTotals 是该模型的用量合计。
	UsageTotals
}

// DailyUsage 是某一天的用量合计，Models 是该天各模型的拆分。
type DailyUsage struct {
	// Date 是本地日期，格式 2006-01-02。
	Date string `json:"date"`
	// UsageTotals 是该天的用量合计。
	UsageTotals
	// Models 是该天按模型拆分的用量，按 token 总量降序。
	Models []ModelUsage `json:"models"`
}

// Summary 是一段日期区间的用量汇总。
type Summary struct {
	// From 是区间起始本地日期；空表示不设下界。
	From string `json:"from"`
	// To 是区间结束本地日期（含当天）；空表示不设上界。
	To string `json:"to"`
	// UsageTotals 是区间内的用量合计。
	UsageTotals
	// Days 是按日期升序的每日明细。
	Days []DailyUsage `json:"days"`
	// Models 是区间内按模型拆分的用量，按 token 总量降序。
	Models []ModelUsage `json:"models"`
}

// Trace 是一次模型调用的精简记录，用于最近请求列表。
type Trace struct {
	// At 是这次请求的完成时间。
	At time.Time `json:"at"`
	// SessionID 是发起请求的会话。
	SessionID string `json:"sessionId,omitempty"`
	// Model 是模型标识；缺失时为 "unknown"。
	Model string `json:"model"`
	// InputTokens 是未命中缓存的输入 token 数。
	InputTokens int `json:"inputTokens"`
	// OutputTokens 是输出 token 数。
	OutputTokens int `json:"outputTokens"`
	// CacheReadInputTokens 是缓存读取的 token 数。
	CacheReadInputTokens int `json:"cacheReadInputTokens"`
	// CacheCreationInputTokens 是缓存写入的 token 数。
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
	// TotalTokens 是输入、输出与缓存 token 之和。
	TotalTokens int `json:"totalTokens"`
	// DurationMS 是请求耗时（毫秒）。
	DurationMS int64 `json:"durationMs"`
	// StopReason 是请求结果：completed、error 或 canceled。
	StopReason string `json:"stopReason"`
	// Err 是失败时的错误文本；成功时为空。
	Err string `json:"error,omitempty"`
}

// Store 是用量日志的追加写入器与查询入口。零值不可用，必须用 New 构造。
//
// Record 与查询方法并发安全；Close 之后 Record 变为空操作，查询仍可读取已落盘内容。
type Store struct {
	path   string
	mu     sync.Mutex
	file   *os.File
	closed bool
}

// Store 实现 agent.MetricsSink，可直接注入 agent.Extensions.Metrics。
var _ agent.MetricsSink = (*Store)(nil)

// New 创建用量存储：父目录不存在时以 0700 创建，日志文件以 0600 追加打开。
// 文件已存在时会把权限校正为 0600。
func New(path string) (*Store, error) {
	if path == "" {
		return nil, ErrEmptyPath
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("telemetry: create dir %s: %w", dir, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("telemetry: open usage log %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("telemetry: chmod usage log %s: %w", path, err)
	}
	return &Store{path: path, file: file}, nil
}

// Record 记录一次模型调用，实现 agent.MetricsSink。
//
// 每次请求只写一小行 JSON，在互斥锁内同步追加并立即对读方可见；不启动后台
// goroutine，也不等待任何用户交互。写入失败被忽略：MetricsSink 没有返回值，
// 且观测失败不得影响 Agent 轮次。At 为零值时取当前时间，Model 为空时记为 "unknown"。
func (s *Store) Record(m agent.RequestMetric) {
	if s == nil {
		return
	}
	at := m.At
	if at.IsZero() {
		at = time.Now()
	}
	model := m.Model
	if model == "" {
		model = unknownModel
	}
	duration := m.DurationMS
	if duration < 0 {
		duration = 0
	}
	e := entry{
		At:                       at,
		Date:                     at.In(time.Local).Format(dateLayout),
		SessionID:                m.SessionID,
		Model:                    model,
		InputTokens:              m.Usage.InputTokens,
		OutputTokens:             m.Usage.OutputTokens,
		CacheReadInputTokens:     m.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: m.Usage.CacheCreationInputTokens,
		DurationMS:               duration,
		StopReason:               string(m.StopReason),
		Err:                      m.Err,
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.file == nil {
		return
	}
	_, _ = s.file.Write(line)
}

// Close 关闭日志文件。可重复调用；关闭后 Record 不再写入，查询方法仍可工作。
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	if err != nil {
		return fmt.Errorf("telemetry: close usage log: %w", err)
	}
	return nil
}

// Summary 汇总 from 到 to（按本地日期，两端含当天）的用量。
// 零值 from 或 to 表示该侧不设边界；to 的日期早于 from 时返回 ErrInvalidRange。
func (s *Store) Summary(ctx context.Context, from, to time.Time) (Summary, error) {
	fromDate, toDate, err := rangeDates(from, to)
	if err != nil {
		return Summary{}, err
	}
	out := Summary{From: fromDate, To: toDate}
	models := make(map[string]*UsageTotals)
	days := make(map[string]map[string]*UsageTotals)
	err = s.scan(ctx, func(e entry) {
		if fromDate != "" && e.Date < fromDate {
			return
		}
		if toDate != "" && e.Date > toDate {
			return
		}
		out.add(e)
		total := models[e.Model]
		if total == nil {
			total = &UsageTotals{}
			models[e.Model] = total
		}
		total.add(e)
		byModel := days[e.Date]
		if byModel == nil {
			byModel = make(map[string]*UsageTotals)
			days[e.Date] = byModel
		}
		dayTotal := byModel[e.Model]
		if dayTotal == nil {
			dayTotal = &UsageTotals{}
			byModel[e.Model] = dayTotal
		}
		dayTotal.add(e)
	})
	if err != nil {
		return Summary{}, err
	}

	out.Models = sortedModelUsage(models)
	dates := make([]string, 0, len(days))
	for date := range days {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	out.Days = make([]DailyUsage, 0, len(dates))
	for _, date := range dates {
		day := DailyUsage{Date: date, Models: sortedModelUsage(days[date])}
		for _, model := range day.Models {
			day.merge(model.UsageTotals)
		}
		out.Days = append(out.Days, day)
	}
	return out, nil
}

// Recent 返回最近 limit 次模型调用，按时间倒序；limit 必须为正数，否则返回 ErrInvalidLimit。
func (s *Store) Recent(ctx context.Context, limit int) ([]Trace, error) {
	if limit <= 0 {
		return nil, ErrInvalidLimit
	}
	// 只保留最近 limit 条，避免日志很长时把全部记录留在内存。
	buf := make([]Trace, limit)
	count := 0
	err := s.scan(ctx, func(e entry) {
		buf[count%limit] = e.trace()
		count++
	})
	if err != nil {
		return nil, err
	}
	n := count
	if n > limit {
		n = limit
	}
	out := make([]Trace, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, buf[(count-1-i)%limit])
	}
	return out, nil
}

// trace 把一行观测转换成对外展示的记录。
func (e entry) trace() Trace {
	return Trace{
		At:                       e.At,
		SessionID:                e.SessionID,
		Model:                    e.Model,
		InputTokens:              e.InputTokens,
		OutputTokens:             e.OutputTokens,
		CacheReadInputTokens:     e.CacheReadInputTokens,
		CacheCreationInputTokens: e.CacheCreationInputTokens,
		TotalTokens:              e.TotalTokens(),
		DurationMS:               e.DurationMS,
		StopReason:               e.StopReason,
		Err:                      e.Err,
	}
}

// scan 逐行读取日志；文件不存在视为空日志，损坏行被跳过。
func (s *Store) scan(ctx context.Context, fn func(entry)) error {
	if s == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("telemetry: open usage log %s: %w", s.path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Date == "" {
			e.Date = e.At.In(time.Local).Format(dateLayout)
		}
		if e.Model == "" {
			e.Model = unknownModel
		}
		fn(e)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("telemetry: read usage log %s: %w", s.path, err)
	}
	return nil
}

// sortedModelUsage 把模型合计按 token 总量降序、模型名升序排列。
func sortedModelUsage(models map[string]*UsageTotals) []ModelUsage {
	out := make([]ModelUsage, 0, len(models))
	for model, totals := range models {
		out = append(out, ModelUsage{Model: model, UsageTotals: *totals})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// rangeDates 把时间区间规整成本地日期字符串，并校验区间方向。
func rangeDates(from, to time.Time) (string, string, error) {
	var fromDate, toDate string
	if !from.IsZero() {
		fromDate = from.In(time.Local).Format(dateLayout)
	}
	if !to.IsZero() {
		toDate = to.In(time.Local).Format(dateLayout)
	}
	if fromDate != "" && toDate != "" && toDate < fromDate {
		return "", "", ErrInvalidRange
	}
	return fromDate, toDate, nil
}
