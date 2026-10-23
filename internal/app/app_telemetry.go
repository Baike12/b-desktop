package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Baike12/b-desktop/internal/telemetry"
)

// 用量追踪绑定层的固定值。
const (
	// maxUsageSummaryDays 是 GetUsageSummary 允许回溯的最大天数（10 年）。
	maxUsageSummaryDays = 3650
	// maxRequestTraceLimit 是 ListRequestTraces 单次返回的最大条数。
	maxRequestTraceLimit = 1000
)

// 用量追踪绑定层错误哨兵。底层错误一律用 %w 包装。
var (
	// ErrInvalidDays 表示 days 不在 [1, maxUsageSummaryDays] 内。
	ErrInvalidDays = errors.New("app: invalid usage summary days")
	// ErrInvalidTraceLimit 表示 limit 不在 [1, maxRequestTraceLimit] 内。
	ErrInvalidTraceLimit = errors.New("app: invalid request trace limit")
	// ErrTelemetryUnavailable 表示用量组件类型异常，无法提供统计。
	ErrTelemetryUnavailable = errors.New("app: telemetry store unavailable")
)

// UsageTotalsDTO 是一组模型请求的用量合计。
type UsageTotalsDTO struct {
	RequestCount             int   `json:"requestCount"`
	ErrorCount               int   `json:"errorCount"`
	InputTokens              int   `json:"inputTokens"`
	OutputTokens             int   `json:"outputTokens"`
	CacheReadInputTokens     int   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int   `json:"cacheCreationInputTokens"`
	TotalTokens              int   `json:"totalTokens"`
	DurationMS               int64 `json:"durationMs"`
}

// ModelUsageDTO 是单个模型在统计区间内的用量。
type ModelUsageDTO struct {
	Model                    string `json:"model"`
	RequestCount             int    `json:"requestCount"`
	ErrorCount               int    `json:"errorCount"`
	InputTokens              int    `json:"inputTokens"`
	OutputTokens             int    `json:"outputTokens"`
	CacheReadInputTokens     int    `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int    `json:"cacheCreationInputTokens"`
	TotalTokens              int    `json:"totalTokens"`
	DurationMS               int64  `json:"durationMs"`
}

// DailyUsageDTO 是某一天的用量，Models 是该天各模型的拆分。
type DailyUsageDTO struct {
	Date                     string          `json:"date"`
	RequestCount             int             `json:"requestCount"`
	ErrorCount               int             `json:"errorCount"`
	InputTokens              int             `json:"inputTokens"`
	OutputTokens             int             `json:"outputTokens"`
	CacheReadInputTokens     int             `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int             `json:"cacheCreationInputTokens"`
	TotalTokens              int             `json:"totalTokens"`
	DurationMS               int64           `json:"durationMs"`
	Models                   []ModelUsageDTO `json:"models"`
}

// UsageSummaryDTO 是一段日期区间的用量汇总。From/To 是本地日期，两端含当天。
type UsageSummaryDTO struct {
	From                     string          `json:"from"`
	To                       string          `json:"to"`
	RequestCount             int             `json:"requestCount"`
	ErrorCount               int             `json:"errorCount"`
	InputTokens              int             `json:"inputTokens"`
	OutputTokens             int             `json:"outputTokens"`
	CacheReadInputTokens     int             `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int             `json:"cacheCreationInputTokens"`
	TotalTokens              int             `json:"totalTokens"`
	DurationMS               int64           `json:"durationMs"`
	Days                     []DailyUsageDTO `json:"days"`
	Models                   []ModelUsageDTO `json:"models"`
}

// RequestTraceDTO 是一次模型调用的前端视图，AtMS 是 Unix 毫秒。
type RequestTraceDTO struct {
	AtMS                     int64  `json:"atMs"`
	SessionID                string `json:"sessionId"`
	Model                    string `json:"model"`
	InputTokens              int    `json:"inputTokens"`
	OutputTokens             int    `json:"outputTokens"`
	CacheReadInputTokens     int    `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int    `json:"cacheCreationInputTokens"`
	TotalTokens              int    `json:"totalTokens"`
	DurationMS               int64  `json:"durationMs"`
	StopReason               string `json:"stopReason"`
	Err                      string `json:"err,omitempty"`
}

// GetUsageSummary 返回最近 days 天（含今天）的用量汇总，按日期与模型聚合。
// days 必须在 [1, maxUsageSummaryDays] 内，否则返回 ErrInvalidDays。
func (a *App) GetUsageSummary(days int) (UsageSummaryDTO, error) {
	if days < 1 || days > maxUsageSummaryDays {
		return UsageSummaryDTO{}, fmt.Errorf("app: get usage summary: %w", ErrInvalidDays)
	}
	store, err := a.telemetryComponent()
	if err != nil {
		return UsageSummaryDTO{}, fmt.Errorf("app: get usage summary: %w", err)
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -(days - 1))
	summary, err := store.Summary(a.lifeCtx(), start, now)
	if err != nil {
		return UsageSummaryDTO{}, fmt.Errorf("app: get usage summary: %w", err)
	}
	return toUsageSummaryDTO(summary), nil
}

// ListRequestTraces 返回最近 limit 次模型调用，按时间倒序。
// limit 必须在 [1, maxRequestTraceLimit] 内，否则返回 ErrInvalidTraceLimit。
func (a *App) ListRequestTraces(limit int) ([]RequestTraceDTO, error) {
	if limit < 1 || limit > maxRequestTraceLimit {
		return nil, fmt.Errorf("app: list request traces: %w", ErrInvalidTraceLimit)
	}
	store, err := a.telemetryComponent()
	if err != nil {
		return nil, fmt.Errorf("app: list request traces: %w", err)
	}
	traces, err := store.Recent(a.lifeCtx(), limit)
	if err != nil {
		return nil, fmt.Errorf("app: list request traces: %w", err)
	}
	out := make([]RequestTraceDTO, 0, len(traces))
	for _, trace := range traces {
		out = append(out, toRequestTraceDTO(trace))
	}
	return out, nil
}

// telemetryComponent 返回用量存储单例，日志位于 ~/.b-desktop/usage.jsonl。
// 构造只做本地文件 IO；Store 实现 agent.MetricsSink，可直接注入 Agent 扩展。
func (a *App) telemetryComponent() (*telemetry.Store, error) {
	value, err := a.extensionStore("telemetry", func() (any, error) {
		dir, err := a.settingsDir()
		if err != nil {
			return nil, err
		}
		return telemetry.New(filepath.Join(dir, telemetry.DefaultFileName))
	})
	if err != nil {
		return nil, fmt.Errorf("app: create telemetry store: %w", err)
	}
	store, ok := value.(*telemetry.Store)
	if !ok {
		return nil, fmt.Errorf("app: telemetry store type %T: %w", value, ErrTelemetryUnavailable)
	}
	return store, nil
}

// toUsageSummaryDTO 把领域汇总转换成前端 DTO。
func toUsageSummaryDTO(summary telemetry.Summary) UsageSummaryDTO {
	out := UsageSummaryDTO{
		From:                     summary.From,
		To:                       summary.To,
		RequestCount:             summary.RequestCount,
		ErrorCount:               summary.ErrorCount,
		InputTokens:              summary.InputTokens,
		OutputTokens:             summary.OutputTokens,
		CacheReadInputTokens:     summary.CacheReadInputTokens,
		CacheCreationInputTokens: summary.CacheCreationInputTokens,
		TotalTokens:              summary.TotalTokens,
		DurationMS:               summary.DurationMS,
		Days:                     make([]DailyUsageDTO, 0, len(summary.Days)),
		Models:                   make([]ModelUsageDTO, 0, len(summary.Models)),
	}
	for _, day := range summary.Days {
		out.Days = append(out.Days, toDailyUsageDTO(day))
	}
	for _, model := range summary.Models {
		out.Models = append(out.Models, toModelUsageDTO(model))
	}
	return out
}

// toDailyUsageDTO 把某天的领域聚合转换成前端 DTO。
func toDailyUsageDTO(day telemetry.DailyUsage) DailyUsageDTO {
	out := DailyUsageDTO{
		Date:                     day.Date,
		RequestCount:             day.RequestCount,
		ErrorCount:               day.ErrorCount,
		InputTokens:              day.InputTokens,
		OutputTokens:             day.OutputTokens,
		CacheReadInputTokens:     day.CacheReadInputTokens,
		CacheCreationInputTokens: day.CacheCreationInputTokens,
		TotalTokens:              day.TotalTokens,
		DurationMS:               day.DurationMS,
		Models:                   make([]ModelUsageDTO, 0, len(day.Models)),
	}
	for _, model := range day.Models {
		out.Models = append(out.Models, toModelUsageDTO(model))
	}
	return out
}

// toModelUsageDTO 把模型聚合转换成前端 DTO。
func toModelUsageDTO(model telemetry.ModelUsage) ModelUsageDTO {
	return ModelUsageDTO{
		Model:                    model.Model,
		RequestCount:             model.RequestCount,
		ErrorCount:               model.ErrorCount,
		InputTokens:              model.InputTokens,
		OutputTokens:             model.OutputTokens,
		CacheReadInputTokens:     model.CacheReadInputTokens,
		CacheCreationInputTokens: model.CacheCreationInputTokens,
		TotalTokens:              model.TotalTokens,
		DurationMS:               model.DurationMS,
	}
}

// toRequestTraceDTO 把一次调用记录转换成前端 DTO。
func toRequestTraceDTO(trace telemetry.Trace) RequestTraceDTO {
	return RequestTraceDTO{
		AtMS:                     trace.At.UnixMilli(),
		SessionID:                trace.SessionID,
		Model:                    trace.Model,
		InputTokens:              trace.InputTokens,
		OutputTokens:             trace.OutputTokens,
		CacheReadInputTokens:     trace.CacheReadInputTokens,
		CacheCreationInputTokens: trace.CacheCreationInputTokens,
		TotalTokens:              trace.TotalTokens,
		DurationMS:               trace.DurationMS,
		StopReason:               trace.StopReason,
		Err:                      trace.Err,
	}
}
