package app

import (
	"strings"

	"github.com/Baike12/b-desktop/internal/llm"
)

// ModelPresetDTO 是当前模型配置的前端视图。
type ModelPresetDTO struct {
	// ID 是预设的稳定标识。
	ID string `json:"id"`
	// Name 是展示名称。
	Name string `json:"name"`
	// Provider 是协议名：anthropic 或 openai。
	Provider string `json:"provider"`
	// BaseURL 是请求根地址。
	BaseURL string `json:"baseUrl"`
	// DefaultModel 是建议填入的模型名。
	DefaultModel string `json:"defaultModel"`
	// NeedsKey 表示必须配置 API Key。
	NeedsKey bool `json:"needsKey"`
	// Local 表示供应商运行在本机。
	Local bool `json:"local"`
}

// ListModelPresets 返回当前生效的模型配置。项目不内置模型清单，
// 设置页只展示这一条，避免出现用户并未配置的选项。
func (a *App) ListModelPresets() []ModelPresetDTO {
	cfg, _ := a.configSnapshot()
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = defaultProvider
	}
	preset := llm.CurrentPreset(provider, cfg.BaseURL, cfg.Model)
	return []ModelPresetDTO{toModelPresetDTO(preset)}
}

// toModelPresetDTO 把领域预设转换成前端 DTO。
func toModelPresetDTO(p llm.Preset) ModelPresetDTO {
	return ModelPresetDTO{
		ID:           p.ID,
		Name:         p.Name,
		Provider:     p.Provider,
		BaseURL:      p.BaseURL,
		DefaultModel: p.DefaultModel,
		NeedsKey:     p.NeedsKey,
		Local:        p.Local,
	}
}
