package builtin

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestPNG 生成一张 w×h 的纯色 PNG。
func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestReadImageReturnsImages(t *testing.T) {
	root := newTestRoot(t)
	writeFixture(t, root, "shot.png", makeTestPNG(t, 64, 32))

	read := NewRead(root)
	res, err := read.Run(context.Background(), []byte(`{"file_path":"shot.png"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if len(res.Images) != 1 || res.Images[0].MediaType != "image/png" {
		t.Fatalf("Images = %+v", res.Images)
	}
	if !strings.Contains(res.Content, "[Image:") || !strings.Contains(res.Content, "64x32") {
		t.Fatalf("Content 应包含路径与尺寸: %q", res.Content)
	}
	if _, ok := res.Meta["file_path"]; !ok {
		t.Fatalf("Meta 应包含 file_path: %+v", res.Meta)
	}
}

func TestReadImageDownscalesOversize(t *testing.T) {
	root := newTestRoot(t)
	writeFixture(t, root, "big.png", makeTestPNG(t, 3000, 1000))

	read := NewRead(root)
	res, err := read.Run(context.Background(), []byte(`{"file_path":"big.png"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	// 最长边应被缩到 1568 以内（整除截断得 1567）。
	if !strings.Contains(res.Content, "1567x522") {
		t.Fatalf("缩放后最长边应为 1567: %q", res.Content)
	}
}

func TestReadRejectsOversizeNonImage(t *testing.T) {
	root := newTestRoot(t)
	big := filepath.Join(root, "huge.bin")
	if err := os.WriteFile(big, bytes.Repeat([]byte{0x01}, 9<<20), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	read := NewRead(root)
	res, err := read.Run(context.Background(), []byte(`{"file_path":"huge.bin"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "byte limit") {
		t.Fatalf("超大文件应报错: %+v", res)
	}
}

func TestReadTextLimitUnchanged(t *testing.T) {
	root := newTestRoot(t)
	writeFixture(t, root, "big.txt", bytes.Repeat([]byte("a"), 300*1024))

	read := NewRead(root)
	res, err := read.Run(context.Background(), []byte(`{"file_path":"big.txt"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "exceeding the 262144 byte limit") {
		t.Fatalf("文本超限应维持原上限文案: %s", res.Content)
	}
}
