package app

import (
	"errors"
	"fmt"
	"strings"

	imagegen "github.com/Baike12/b-desktop/internal/image"
	"github.com/Baike12/b-desktop/internal/llm"
)

// ErrEmptyImagePrompt 表示图片生成的提示词为空。
var ErrEmptyImagePrompt = errors.New("app: empty image prompt")

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

// ImageResultDTO 是一次图片生成的结果，只含落盘文件的绝对路径。
// 不含任何密钥或上游原始响应。
type ImageResultDTO struct {
	// Paths 是每张图片的绝对路径，顺序与上游返回一致。
	Paths []string `json:"paths"`
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

// GenerateImage 使用 OpenAI 兼容的 /v1/images/generations 生成图片，
// 保存到 ~/.b-desktop/images 并返回绝对路径。
//
// size、model、baseURL、apiKey 为空时依次回落到当前 Provider 配置；
// apiKey 只用于请求头，绝不写入返回值。返回的结构不含密钥。
func (a *App) GenerateImage(prompt, size, model, baseURL, apiKey string) (ImageResultDTO, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ImageResultDTO{}, ErrEmptyImagePrompt
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ImageResultDTO{}, ErrClosed
	}
	cfg := a.cfg
	a.mu.Unlock()

	if strings.TrimSpace(baseURL) == "" {
		baseURL = cfg.BaseURL
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = cfg.APIKey
	}
	if strings.TrimSpace(model) == "" {
		model = cfg.Model
	}

	gen, err := imagegen.New(imagegen.Options{BaseURL: baseURL, APIKey: apiKey})
	if err != nil {
		return ImageResultDTO{}, fmt.Errorf("app: create image generator: %w", err)
	}
	results, err := gen.Generate(a.lifeCtx(), imagegen.Request{
		Prompt: prompt,
		Model:  model,
		Size:   size,
	})
	if err != nil {
		return ImageResultDTO{}, fmt.Errorf("app: generate image: %w", err)
	}

	paths := make([]string, 0, len(results))
	for _, r := range results {
		paths = append(paths, r.Path)
	}
	return ImageResultDTO{Paths: paths}, nil
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
