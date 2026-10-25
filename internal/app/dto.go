package app

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Baike12/b-desktop/internal/llm"
	"github.com/Baike12/b-desktop/internal/project"
	"github.com/Baike12/b-desktop/internal/session"
)

// CreateSessionInput 是 CreateSession 的入参。
// SessionID 与 WorkDir 可省略：分别自动生成与取应用默认工作目录。
type CreateSessionInput struct {
	SessionID string `json:"sessionId,omitempty"`
	Title     string `json:"title,omitempty"`
	WorkDir   string `json:"workDir,omitempty"`
}

// SendMessageResult 是 SendMessage 的返回值。输出不在这里返回：
// 文本增量、工具进度与结束原因都通过 "agent:event" 事件推送。
type SendMessageResult struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	Running   bool   `json:"running"`
}

// ImageInput 是随用户消息提交的一张图片。Data 是裸 base64（也兼容 data URL）；
// MediaType 可省略，后端按 magic bytes 重新判定。
type ImageInput struct {
	MediaType string `json:"mediaType,omitempty"`
	Data      string `json:"data"`
}

// ImageDTO 是一张 base64 图片的前端视图，出现在 BlockDTO.Data（用户消息图片）
// 或 BlockDTO.Images（工具结果图片）里。
type ImageDTO struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// SessionInfoDTO 是会话列表项。UpdatedAtMS 是 Unix 毫秒。
type SessionInfoDTO struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	WorkDir string `json:"workDir"`
	// ProjectPath 是会话归属的项目路径。工作树会话的 WorkDir 与它不同，
	// 前端据此把工作树会话归到主项目下。
	ProjectPath string `json:"projectPath,omitempty"`
	Messages    int    `json:"messages"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
	Running     bool   `json:"running"`
	// Questioning 表示该会话正在等待 AskUserQuestion 的回答。
	Questioning bool `json:"questioning"`
}

// ProjectDTO 是侧边栏项目列表项。Path 是解析后的绝对路径，
// 普通会话按 WorkDir 精确匹配归属项目，工作树会话按 ProjectPath 归属。
type ProjectDTO struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	AddedAtMS int64  `json:"addedAtMs"`
}

// SessionViewDTO 是会话详情（重放后的历史）。
type SessionViewDTO struct {
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	WorkDir        string       `json:"workDir"`
	Model          string       `json:"model"`
	PermissionMode string       `json:"permissionMode"`
	CreatedAtMS    int64        `json:"createdAtMs"`
	Messages       []MessageDTO `json:"messages"`
	Usage          UsageDTO     `json:"usage"`
	// ContextTokens 是最近一次模型调用占用的上下文规模；0 表示尚无调用记录。
	ContextTokens int  `json:"contextTokens"`
	Running       bool `json:"running"`
}

// MessageDTO 是一条消息的前端视图。TimeMS 来自 llm.Message.Time（Unix 毫秒）。
type MessageDTO struct {
	ID         string     `json:"id"`
	Role       string     `json:"role"`
	Blocks     []BlockDTO `json:"blocks"`
	StopReason string     `json:"stopReason,omitempty"`
	TimeMS     int64      `json:"timeMs"`
}

// BlockDTO 是一个内容块的前端视图。
//
// Input 使用 any 而不是 json.RawMessage：Wails 的 TypeScript 生成器把 []byte
// 映射成 number[]，而工具参数在线格式上是 JSON 对象。any 在运行期就是对象。
type BlockDTO struct {
	Type      string     `json:"type"`
	Text      string     `json:"text,omitempty"`
	Thinking  string     `json:"thinking,omitempty"`
	ToolUseID string     `json:"toolUseId,omitempty"`
	ToolName  string     `json:"toolName,omitempty"`
	Input     any        `json:"input,omitempty"`
	IsError   bool       `json:"isError,omitempty"`
	MediaType string     `json:"mediaType,omitempty"`
	Data      string     `json:"data,omitempty"`
	Images    []ImageDTO `json:"images,omitempty"`
}

// UsageDTO 是 token 统计。
type UsageDTO struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

// ProviderConfigDTO 是 Provider 配置的只读视图。
// 它不含 API Key 明文，只有 HasAPIKey 与末四位掩码。
type ProviderConfigDTO struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"baseUrl"`
	Model           string `json:"model"`
	HasAPIKey       bool   `json:"hasApiKey"`
	APIKeyMasked    string `json:"apiKeyMasked,omitempty"`
	ContextWindow   int    `json:"contextWindow"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	// ThinkingEnabled 表示是否请求扩展思考；ThinkingBudget 是思考预算（token）。
	ThinkingEnabled bool   `json:"thinkingEnabled"`
	ThinkingBudget  int    `json:"thinkingBudget"`
	PermissionMode  string `json:"permissionMode"`
	WorkDir         string `json:"workDir"`
	InitError       string `json:"initError,omitempty"`
	// SupportsImages 报告当前模型是否接受图像输入（llm.ModelInfoOf 判定），
	// 前端据此禁用图片附件入口。
	SupportsImages bool `json:"supportsImages"`
}

// ProviderConfigInput 是 SetProviderConfig 的入参。
// APIKey 为空表示保留现有密钥：前端拿不到明文，不能回传。
type ProviderConfigInput struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"baseUrl,omitempty"`
	Model           string `json:"model,omitempty"`
	APIKey          string `json:"apiKey,omitempty"`
	ContextWindow   int    `json:"contextWindow,omitempty"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
	// ThinkingEnabled 打开扩展思考；ThinkingBudget 是思考预算（token），0 表示用默认值。
	ThinkingEnabled bool `json:"thinkingEnabled"`
	ThinkingBudget  int  `json:"thinkingBudget,omitempty"`
}

// toProjectDTO 转换项目列表项。
func toProjectDTO(item project.Project) ProjectDTO {
	return ProjectDTO{
		Path:      item.Path,
		Name:      item.Name,
		AddedAtMS: unixMilli(item.AddedAt),
	}
}

// toSessionInfoDTO 转换会话列表项。
func toSessionInfoDTO(info session.Info, running bool) SessionInfoDTO {
	return SessionInfoDTO{
		ID:          info.ID,
		Title:       info.Title,
		WorkDir:     info.WorkDir,
		Messages:    info.Messages,
		UpdatedAtMS: unixMilli(info.UpdatedAt),
		Running:     running,
	}
}

// toSessionViewDTO 把重放结果转换成会话详情。
func toSessionViewDTO(loaded session.Loaded, model, permissionMode string, running bool) SessionViewDTO {
	return SessionViewDTO{
		ID:             loaded.Meta.ID,
		Title:          loaded.Meta.Title,
		WorkDir:        loaded.Meta.WorkDir,
		Model:          model,
		PermissionMode: permissionMode,
		CreatedAtMS:    unixMilli(loaded.Meta.CreatedAt),
		Messages:       toMessagesDTO(loaded.Messages),
		Usage:          toUsageDTO(loaded.Usage),
		ContextTokens:  loaded.LastUsage.ContextTokens(),
		Running:        running,
	}
}

// toMessagesDTO 批量转换消息；空输入返回空切片而不是 nil，保证 JSON 输出 []。
func toMessagesDTO(msgs []llm.Message) []MessageDTO {
	out := make([]MessageDTO, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, toMessageDTO(msg))
	}
	return out
}

// toMessageDTO 转换一条消息。
func toMessageDTO(msg llm.Message) MessageDTO {
	return MessageDTO{
		ID:         msg.ID,
		Role:       string(msg.Role),
		Blocks:     toBlocksDTO(msg.Blocks),
		StopReason: string(msg.StopReason),
		TimeMS:     unixMilli(msg.Time),
	}
}

// toBlocksDTO 批量转换内容块；空输入返回空切片。
func toBlocksDTO(blocks []llm.ContentBlock) []BlockDTO {
	out := make([]BlockDTO, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, toBlockDTO(block))
	}
	return out
}

// toBlockDTO 转换一个内容块。非法工具参数按 nil 下发，不影响会话加载。
func toBlockDTO(block llm.ContentBlock) BlockDTO {
	out := BlockDTO{
		Type:      string(block.Type),
		Text:      block.Text,
		Thinking:  block.Thinking,
		ToolUseID: block.ToolUseID,
		ToolName:  block.ToolName,
		Input:     decodeJSONValue(block.Input),
		IsError:   block.IsError,
		MediaType: block.MediaType,
		Data:      block.Data,
	}
	for _, img := range block.Images {
		out.Images = append(out.Images, ImageDTO{MediaType: img.MediaType, Data: img.Data})
	}
	return out
}

// toUsageDTO 转换 token 统计。
func toUsageDTO(u llm.Usage) UsageDTO {
	return UsageDTO{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	}
}

// decodeJSONValue 把工具参数的原始 JSON 解码成可被 Wails 序列化的值。
// 空输入或非法 JSON 返回 nil（历史里可能出现被中断的调用）。
func decodeJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// toProviderConfigDTO 构造脱敏的 Provider 配置视图。
func toProviderConfigDTO(cfg Config, initErr error) ProviderConfigDTO {
	out := ProviderConfigDTO{
		Provider:        cfg.Provider,
		BaseURL:         cfg.BaseURL,
		Model:           cfg.Model,
		HasAPIKey:       cfg.APIKey != "",
		APIKeyMasked:    maskAPIKey(cfg.APIKey),
		ContextWindow:   cfg.ContextWindow,
		MaxOutputTokens: cfg.MaxOutputTokens,
		ThinkingEnabled: cfg.ThinkingEnabled,
		ThinkingBudget:  cfg.ThinkingBudget,
		PermissionMode:  cfg.PermissionMode,
		WorkDir:         cfg.WorkDir,
	}
	if initErr != nil {
		out.InitError = initErr.Error()
	}
	out.SupportsImages = llm.ModelInfoOf(cfg.Model).SupportsImages
	return out
}

// maskAPIKey 只保留末四位，其余用星号遮蔽；空密钥返回空串。
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	runes := []rune(key)
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}

// unixMilli 把时间转成 Unix 毫秒；零值返回 0。
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// marshalDTO 供测试与调试确认 DTO 可被 JSON 序列化。
func marshalDTO(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("app: marshal dto: %w", err)
	}
	return b, nil
}
