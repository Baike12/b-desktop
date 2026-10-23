package llm

import "strings"

// Preset 是设置页展示的模型配置项。项目不内置任何模型清单：
// 设置页只展示当前生效的这一条配置，其余字段由用户手动填写。
type Preset struct {
	// ID 是稳定标识；当前配置固定为 "current"。
	ID string `json:"id"`
	// Name 是展示给用户的名称，取当前模型名。
	Name string `json:"name"`
	// Provider 是 llm.NewProvider 接受的协议名："anthropic" 或 "openai"。
	Provider string `json:"provider"`
	// BaseURL 是请求根地址。
	BaseURL string `json:"baseUrl"`
	// DefaultModel 是当前配置的模型名。
	DefaultModel string `json:"defaultModel"`
	// NeedsKey 表示调用该供应商必须配置 API Key。
	NeedsKey bool `json:"needsKey"`
	// Local 表示供应商运行在本机。
	Local bool `json:"local"`
}

// CurrentPreset 用当前生效的 Provider 配置生成唯一一条预设。
// 没有预置模型目录，避免设置页出现用户并未配置的选项。
func CurrentPreset(provider, baseURL, model string) Preset {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	name := model
	if name == "" {
		name = "当前模型（未配置）"
	}
	return Preset{
		ID:           "current",
		Name:         name,
		Provider:     provider,
		BaseURL:      strings.TrimSpace(baseURL),
		DefaultModel: model,
		NeedsKey:     true,
		Local:        false,
	}
}
