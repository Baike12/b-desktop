// Package imagegen 调用 OpenAI 兼容的图片生成接口（POST /v1/images/generations），
// 并把返回的图片以 0600 权限落盘到用户目录。
//
// 目录名是 internal/image，包名取 imagegen 以避免与标准库 image 混淆。
package imagegen

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// 默认值。Generate 中未显式指定的字段会回落到这些常量。
const (
	// DefaultBaseURL 是未指定 baseURL 时使用的 OpenAI 官方地址。
	DefaultBaseURL = "https://api.openai.com"
	// DefaultModel 是未指定 model 时使用的图片模型。
	DefaultModel = "gpt-image-2"
	// DefaultSize 是未指定 size 时使用的图片尺寸。
	DefaultSize = "1024x1024"
)

const (
	// defaultTimeout 是单次请求（含 URL 下载）的超时上限。
	defaultTimeout = 10 * time.Minute
	// maxImageBytes 是单张图片的字节上限，与参考实现一致。
	maxImageBytes = 30 << 20
	// maxResponseBytes 是 JSON 响应体上限：base64 编码后约为原图的 4/3。
	maxResponseBytes = 64 << 20
	// maxErrorBody 是读取非 2xx 响应体的上限。
	maxErrorBody = 8 << 10
	// maxImagesPerRequest 是单次请求允许的图片数量上限。
	maxImagesPerRequest = 10
	// defaultDirName 与 imagesDirName 拼出 ~/.b-desktop/images。
	defaultDirName = ".b-desktop"
	imagesDirName  = "images"
)

// 图片生成的错误哨兵，调用方可用 errors.Is 判断。
var (
	// ErrEmptyPrompt 表示 prompt 为空。
	ErrEmptyPrompt = errors.New("imagegen: empty prompt")
	// ErrInvalidBaseURL 表示 baseURL 不是合法的 http/https 地址。
	ErrInvalidBaseURL = errors.New("imagegen: invalid base url")
	// ErrInvalidRequest 表示请求参数超出允许范围。
	ErrInvalidRequest = errors.New("imagegen: invalid request")
	// ErrUpstream 表示上游返回非 2xx 或错误体。
	ErrUpstream = errors.New("imagegen: upstream error")
	// ErrNoImage 表示响应中没有可用图片。
	ErrNoImage = errors.New("imagegen: response contains no image")
	// ErrImageTooLarge 表示单张图片超过 30MB 上限。
	ErrImageTooLarge = errors.New("imagegen: image too large")
	// ErrUnsupportedImage 表示返回内容不是 PNG、JPEG 或 WebP。
	ErrUnsupportedImage = errors.New("imagegen: unsupported image format")
)

// Options 构造 Generator 的配置。BaseURL 与 APIKey 由调用方传入。
type Options struct {
	// BaseURL 是 OpenAI 兼容端点根地址；空时使用 DefaultBaseURL。
	// 已带 /v1 或 /v1/images/generations 后缀时不会重复拼接。
	BaseURL string
	// APIKey 是 Bearer 密钥；为空时不发送 Authorization 头（适用于本地端点）。
	APIKey string
	// HTTPClient 是可选的自定义客户端；nil 时使用带 defaultTimeout 的默认客户端。
	HTTPClient *http.Client
	// Dir 是图片输出目录；为空时使用 ~/.b-desktop/images。测试可指向临时目录。
	Dir string
}

// Request 是一次图片生成请求。
type Request struct {
	// Prompt 是图片描述，不能为空。
	Prompt string
	// Model 是图片模型名；空时使用 DefaultModel。
	Model string
	// Size 是图片尺寸，如 "1024x1024"；空时使用 DefaultSize。
	Size string
	// N 是生成张数；0 或负数按 1 处理，最大 maxImagesPerRequest。
	N int
}

// Result 是一张已落盘的图片。
type Result struct {
	// Path 是图片的绝对路径。
	Path string
	// MimeType 是按文件内容嗅探出的 MIME 类型。
	MimeType string
	// RevisedPrompt 是上游改写后的提示词；上游未返回时为空。
	RevisedPrompt string
}

// Generator 是一个 OpenAI 兼容图片生成客户端，可并发使用。
type Generator struct {
	endpoint string
	apiKey   string
	client   *http.Client
	dir      string
}

// New 创建 Generator。BaseURL 非法时返回包装 ErrInvalidBaseURL 的错误。
func New(opts Options) (*Generator, error) {
	endpoint, err := imagesEndpoint(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Generator{
		endpoint: endpoint,
		apiKey:   strings.TrimSpace(opts.APIKey),
		client:   client,
		dir:      strings.TrimSpace(opts.Dir),
	}, nil
}

// Generate 生成图片并把每一张保存为 0600 权限的文件，返回绝对路径。
// 任一图片失败时删除本次已写入的文件并返回错误。
func (g *Generator) Generate(ctx context.Context, req Request) ([]Result, error) {
	if g == nil {
		return nil, errors.New("imagegen: nil generator")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, ErrEmptyPrompt
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = DefaultModel
	}
	size := strings.TrimSpace(req.Size)
	if size == "" {
		size = DefaultSize
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	if n > maxImagesPerRequest {
		return nil, fmt.Errorf("%w: n must be <= %d", ErrInvalidRequest, maxImagesPerRequest)
	}

	payload, err := json.Marshal(imagesRequest{Model: model, Prompt: prompt, Size: size, N: n})
	if err != nil {
		return nil, fmt.Errorf("imagegen: encode request: %w", err)
	}

	resp, err := g.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out imagesResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("%w: %s", ErrUpstream, out.Error.Message)
	}
	if len(out.Data) == 0 {
		return nil, ErrNoImage
	}

	dir, err := g.outputDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("imagegen: create dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("imagegen: chmod dir %s: %w", dir, err)
	}

	results := make([]Result, 0, len(out.Data))
	for i, item := range out.Data {
		data, mime, err := g.resolveImage(ctx, item)
		if err != nil {
			removeAll(results)
			return nil, fmt.Errorf("imagegen: image %d: %w", i, err)
		}
		path, err := saveImage(dir, data, extensionFor(mime))
		if err != nil {
			removeAll(results)
			return nil, err
		}
		results = append(results, Result{
			Path:          path,
			MimeType:      mime,
			RevisedPrompt: item.RevisedPrompt,
		})
	}
	return results, nil
}

// imagesRequest 是 /v1/images/generations 的请求体。
type imagesRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Size   string `json:"size"`
	N      int    `json:"n"`
}

// imageItem 是响应中的一张图片：base64 与 url 至少有一个非空。
type imageItem struct {
	B64JSON       string `json:"b64_json"`
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt"`
}

// imagesResponse 是兼容端点的响应体；Data 按顺序对应每张图片。
type imagesResponse struct {
	Data  []imageItem `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// post 发起生成请求；非 2xx 时把错误体转成包装 ErrUpstream 的错误。
func (g *Generator) post(ctx context.Context, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("imagegen: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if g.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.apiKey)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: status %d: %s", ErrUpstream, resp.StatusCode, upstreamMessage(body))
	}
	return resp, nil
}

// resolveImage 取出一张图片的字节并嗅探格式：优先 b64_json，其次下载 url。
func (g *Generator) resolveImage(ctx context.Context, item imageItem) ([]byte, string, error) {
	if b64 := item.B64JSON; strings.TrimSpace(b64) != "" {
		return decodeBase64Image(b64)
	}
	if rawURL := strings.TrimSpace(item.URL); rawURL != "" {
		return g.downloadImage(ctx, rawURL)
	}
	return nil, "", ErrNoImage
}

// decodeBase64Image 去掉空白后解码 base64，并校验大小与格式。
func decodeBase64Image(encoded string) ([]byte, string, error) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, encoded)
	if cleaned == "" {
		return nil, "", ErrNoImage
	}
	if len(cleaned) > (maxImageBytes+2)/3*4+8 {
		return nil, "", ErrImageTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		// 部分端点省略填充，再按无填充编码重试一次。
		data, err = base64.RawStdEncoding.DecodeString(cleaned)
		if err != nil {
			return nil, "", fmt.Errorf("imagegen: decode base64: %w", err)
		}
	}
	if len(data) == 0 {
		return nil, "", ErrNoImage
	}
	if len(data) > maxImageBytes {
		return nil, "", ErrImageTooLarge
	}
	mime, ok := detectImage(data)
	if !ok {
		return nil, "", ErrUnsupportedImage
	}
	return data, mime, nil
}

// downloadImage 下载 url 指向的图片；只接受 http/https 地址。
func (g *Generator) downloadImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("%w: image url: %v", ErrInvalidRequest, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", fmt.Errorf("%w: image url scheme %q", ErrInvalidRequest, u.Scheme)
	}
	if u.Host == "" {
		return nil, "", fmt.Errorf("%w: image url missing host", ErrInvalidRequest)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("imagegen: build download request: %w", err)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("imagegen: download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, "", fmt.Errorf("%w: download status %d: %s", ErrUpstream, resp.StatusCode, upstreamMessage(body))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("imagegen: read image: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, "", ErrImageTooLarge
	}
	if len(data) == 0 {
		return nil, "", ErrNoImage
	}
	mime, ok := detectImage(data)
	if !ok {
		return nil, "", ErrUnsupportedImage
	}
	return data, mime, nil
}

// outputDir 返回图片输出目录：显式 Dir 优先，否则 ~/.b-desktop/images。
func (g *Generator) outputDir() (string, error) {
	if g.dir != "" {
		return g.dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("imagegen: resolve home dir: %w", err)
	}
	return filepath.Join(home, defaultDirName, imagesDirName), nil
}

// imagesEndpoint 由 baseURL 拼出 /images/generations 地址。
// baseURL 已以 /v1 结尾时只补 /images/generations，避免出现 /v1/v1。
func imagesEndpoint(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidBaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: scheme must be http or https", ErrInvalidBaseURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidBaseURL)
	}

	path := strings.TrimRight(u.Path, "/")
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, "/images/generations"):
		// 已经是完整端点，原样保留。
	case strings.HasSuffix(lower, "/v1"):
		path += "/images/generations"
	default:
		path += "/v1/images/generations"
	}
	u.Path = path
	return u.String(), nil
}

// upstreamMessage 提取错误体中的 message，失败时返回截断后的原文。
func upstreamMessage(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 {
		var env imagesResponse
		if err := json.Unmarshal(trimmed, &env); err == nil && env.Error != nil {
			for _, candidate := range []string{env.Error.Message, env.Error.Code, env.Error.Type} {
				if candidate != "" {
					return candidate
				}
			}
		}
		var generic struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(trimmed, &generic); err == nil && generic.Message != "" {
			return generic.Message
		}
	}
	s := strings.TrimSpace(string(trimmed))
	if len(s) > 2000 {
		s = s[:2000]
	}
	if s == "" {
		s = "empty error response"
	}
	return s
}

// detectImage 按文件头嗅探 PNG、JPEG、WebP；其它格式返回 false。
func detectImage(data []byte) (string, bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", true
	default:
		return "", false
	}
}

// extensionFor 返回 MIME 对应的文件扩展名。
func extensionFor(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

// saveImage 以 0600 权限写入 <随机ID><ext> 并返回绝对路径。
func saveImage(dir string, data []byte, ext string) (string, error) {
	id, err := newImageID()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+ext)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("imagegen: create image file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("imagegen: write image file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("imagegen: close image file: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("imagegen: resolve image path: %w", err)
	}
	return abs, nil
}

// newImageID 返回 16 字节随机数的十六进制串。
func newImageID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("imagegen: generate image id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// removeAll 删除本次已写入的文件，用于失败时清理。
func removeAll(results []Result) {
	for _, r := range results {
		_ = os.Remove(r.Path)
	}
}
