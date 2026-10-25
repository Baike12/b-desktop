package llm

import "strings"

// 已知模型的静态能力。表外的模型一律按「全能力可用」处理：
// 错误地把视觉模型当成纯文本会直接挡掉用户输入，而把纯文本模型当成
// 视觉模型只会在发送图片时收到供应商报错（经错误回灌进入对话）。
var knownModels = []ModelInfo{
	// Anthropic：Claude 3 起全系支持视觉与工具。
	{ID: "claude-opus-4", ContextWindow: 200000, MaxOutput: 32000, SupportsTools: true, SupportsImages: true},
	{ID: "claude-sonnet-4", ContextWindow: 200000, MaxOutput: 64000, SupportsTools: true, SupportsImages: true},
	{ID: "claude-haiku-4", ContextWindow: 200000, MaxOutput: 32000, SupportsTools: true, SupportsImages: true},
	{ID: "claude-3-7-sonnet", ContextWindow: 200000, MaxOutput: 64000, SupportsTools: true, SupportsImages: true},
	{ID: "claude-3-5-sonnet", ContextWindow: 200000, MaxOutput: 8192, SupportsTools: true, SupportsImages: true},
	{ID: "claude-3-5-haiku", ContextWindow: 200000, MaxOutput: 8192, SupportsTools: true, SupportsImages: true},
	{ID: "claude-3-opus", ContextWindow: 200000, MaxOutput: 4096, SupportsTools: true, SupportsImages: true},
	{ID: "claude-3-haiku", ContextWindow: 200000, MaxOutput: 4096, SupportsTools: true, SupportsImages: true},
	// OpenAI：gpt-4o 系与更新支持视觉与工具；o 系推理模型支持图像输入。
	{ID: "gpt-5", ContextWindow: 400000, MaxOutput: 128000, SupportsTools: true, SupportsImages: true},
	{ID: "gpt-4.1", ContextWindow: 1047576, MaxOutput: 32768, SupportsTools: true, SupportsImages: true},
	{ID: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, SupportsImages: true},
	{ID: "gpt-4-turbo", ContextWindow: 128000, MaxOutput: 4096, SupportsTools: true, SupportsImages: true},
	{ID: "o4-mini", ContextWindow: 200000, MaxOutput: 100000, SupportsTools: true, SupportsImages: true},
	{ID: "o3", ContextWindow: 200000, MaxOutput: 100000, SupportsTools: true, SupportsImages: true},
	// DeepSeek：V3/R1 系 API 不接受图像输入。
	{ID: "deepseek-chat", ContextWindow: 128000, MaxOutput: 8192, SupportsTools: true},
	{ID: "deepseek-reasoner", ContextWindow: 128000, MaxOutput: 8192, SupportsTools: true},
	// 阿里通义：qwen3 文本系（视觉能力在 qwen-vl 系，由通配覆盖）。
	{ID: "qwen3", ContextWindow: 128000, MaxOutput: 8192, SupportsTools: true},
	{ID: "qwen-max", ContextWindow: 32768, MaxOutput: 8192, SupportsTools: true},
	{ID: "qwen-plus", ContextWindow: 131072, MaxOutput: 8192, SupportsTools: true},
	{ID: "qwen-turbo", ContextWindow: 1000000, MaxOutput: 8192, SupportsTools: true},
}

// textOnlyPatterns 是名字匹配即判为「不支持图像」的后缀/关键词表，
// 覆盖表外已知纯文本系（例如 deepseek-v3、qwen3-xxx-text、glm-4-air 等变体）。
var textOnlyPatterns = []string{
	"deepseek", "qwen3", "qwen2", "qwq", "glm-4", "glm-3", "kimi-k2",
	"minimax-text", "moonshot-v1", "ernie", "hunyuan", "spark", "yi-",
}

// visionPatterns 是名字匹配即判为「支持图像」的表外视觉系
// （覆盖 qwen-vl、glm-4v、llama-3.2-vision 等多模态命名）。
var visionPatterns = []string{"-vl", "vision", "omni", "4o", "4v", "4.1", "gpt-5", "o3", "o4-"}

// ModelInfoOf 返回模型的静态能力与容量。匹配规则按优先级：
//  1. 表内前缀匹配（"claude-sonnet-4-20250514" 命中 "claude-sonnet-4"）；
//  2. 视觉关键词命中 → 支持图像，容量取默认值；
//  3. 纯文本关键词命中 → 不支持图像，容量取默认值；
//  4. 完全未知 → 全能力可用 + 默认容量。
func ModelInfoOf(model string) ModelInfo {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, info := range knownModels {
		if strings.HasPrefix(model, info.ID) {
			return info
		}
	}
	for _, pattern := range visionPatterns {
		if strings.Contains(model, pattern) {
			return ModelInfo{ContextWindow: defaultContextWindowTokens, MaxOutput: defaultMaxOutputTokens, SupportsTools: true, SupportsImages: true}
		}
	}
	for _, pattern := range textOnlyPatterns {
		if strings.Contains(model, pattern) {
			return ModelInfo{ContextWindow: defaultContextWindowTokens, MaxOutput: defaultMaxOutputTokens, SupportsTools: true}
		}
	}
	return ModelInfo{ContextWindow: defaultContextWindowTokens, MaxOutput: defaultMaxOutputTokens, SupportsTools: true, SupportsImages: true}
}

// llm 包内的容量默认值，与 app 层的 defaultContextWindow/defaultMaxOutputTokens 保持一致。
const (
	defaultContextWindowTokens = 200000
	defaultMaxOutputTokens     = 8192
)
