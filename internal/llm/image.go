package llm

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// 图片输入约束。base64 上限与参考实现一致：Anthropic/OpenAI 都按编码后
// 字符串校验 5MB，超限直接拒绝而不是发出去等 API 报含糊错误。
const (
	// MaxImageBase64Bytes 是单个图片 base64 编码后的字节上限。
	MaxImageBase64Bytes = 5 << 20 // 5MB
	// MaxImagesPerMessage 是单条用户消息允许携带的图片数量上限。
	MaxImagesPerMessage = 5
	// imageSourceB64 前缀预留：Anthropic source.type 的取值。
	// 图片始终以 base64 进入消息历史，URL 引用不落盘（与 codex/zed 一致）。
)

// Image 是一张 base64 编码的图片。它作为 tool_result 的附属内容出现；
// 用户消息里的图片直接用 Type 为 BlockImage 的 ContentBlock 表达。
type Image struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// ImageBlock 构造一个 base64 图片内容块。
func ImageBlock(mediaType, base64Data string) ContentBlock {
	return ContentBlock{Type: BlockImage, MediaType: mediaType, Data: base64Data}
}

// 图片格式白名单。其余格式（bmp、svg、tiff 等）在提交时拒绝，
// 而不是让 API 在请求时返回难排查的 400。
var imageMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// SupportedImageMediaType 报告 mediaType 是否受支持。
func SupportedImageMediaType(mediaType string) bool {
	return imageMediaTypes[strings.ToLower(strings.TrimSpace(mediaType))]
}

// SniffImageMediaType 按文件头识别图片格式，无法识别返回空串。
// 前端可能丢失或谎报 MIME，magic bytes 是唯一可信来源。
func SniffImageMediaType(data []byte) string {
	switch {
	case len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}

// NormalizeImageInput 校验并归一化一张图片输入，返回 (mediaType, base64)。
//   - data 允许是裸 base64 或 data URL（data:<mime>;base64,<payload>）；
//   - mediaType 以解码后的 magic bytes 为准，调用方声明只作参考；
//   - base64 必须可解码，且编码后长度不超过 MaxImageBase64Bytes。
func NormalizeImageInput(mediaType, data string) (string, string, error) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return "", "", fmt.Errorf("image data is empty")
	}
	// data:[<mediatype>];base64,<payload>
	if strings.HasPrefix(trimmed, "data:") {
		idx := strings.Index(trimmed, ",")
		if idx < 0 {
			return "", "", fmt.Errorf("image data URL has no payload")
		}
		header := trimmed[len("data:"):idx]
		if strings.HasSuffix(header, ";base64") {
			if m := strings.TrimSuffix(header, ";base64"); SupportedImageMediaType(m) {
				mediaType = m
			}
		}
		trimmed = trimmed[idx+1:]
	}
	trimmed = strings.Join(strings.Fields(trimmed), "")
	if len(trimmed) > MaxImageBase64Bytes {
		return "", "", fmt.Errorf("image is %d bytes after base64 encoding, exceeding the %d byte limit", len(trimmed), MaxImageBase64Bytes)
	}
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("image data is not valid base64: %w", err)
	}
	sniffed := SniffImageMediaType(raw)
	if sniffed == "" {
		return "", "", fmt.Errorf("unsupported image format: only png, jpeg, gif and webp are accepted")
	}
	// magic bytes 是唯一可信来源：声明与实际不符时以实际为准。
	return sniffed, trimmed, nil
}
