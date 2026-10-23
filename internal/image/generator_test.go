package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pngBytes 是一段带 PNG 文件头的假图片内容，足够触发格式嗅探。
func pngBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0xde, 0xad, 0xbe, 0xef}
}

// jpegBytes 是一段带 JPEG 文件头的假图片内容。
func jpegBytes() []byte {
	return []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46}
}

// generationRequest 是测试服务器收到的请求快照。
type generationRequest struct {
	path string
	auth string
	body imagesRequest
}

// newGenerationServer 启动一个返回固定图片数据的假端点，并通过通道回传请求。
func newGenerationServer(t *testing.T, data []map[string]any) (*httptest.Server, <-chan generationRequest) {
	t.Helper()
	ch := make(chan generationRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		var body imagesRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body %q: %v", raw, err)
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		ch <- generationRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

// TestGenerateSavesBase64PNG 覆盖主路径：请求体字段、鉴权头、0600 落盘与绝对路径。
func TestGenerateSavesBase64PNG(t *testing.T) {
	img := pngBytes()
	srv, requests := newGenerationServer(t, []map[string]any{{
		"b64_json":       base64.StdEncoding.EncodeToString(img),
		"revised_prompt": "a red bicycle",
	}})
	dir := t.TempDir()

	g, err := New(Options{BaseURL: srv.URL, APIKey: "sk-test", Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	results, err := g.Generate(context.Background(), Request{Prompt: "  a bicycle  ", Size: "1024x1024", N: 1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("结果数 = %d，期望 1", len(results))
	}

	got := <-requests
	if got.path != "/v1/images/generations" {
		t.Errorf("请求路径 = %q", got.path)
	}
	if got.auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got.auth)
	}
	if got.body.Model != DefaultModel || got.body.Prompt != "a bicycle" || got.body.Size != "1024x1024" || got.body.N != 1 {
		t.Errorf("请求体 = %+v", got.body)
	}

	res := results[0]
	if !filepath.IsAbs(res.Path) {
		t.Errorf("路径不是绝对路径: %q", res.Path)
	}
	if !strings.HasSuffix(res.Path, ".png") {
		t.Errorf("PNG 结果应以 .png 结尾: %q", res.Path)
	}
	if res.MimeType != "image/png" {
		t.Errorf("MimeType = %q", res.MimeType)
	}
	if res.RevisedPrompt != "a red bicycle" {
		t.Errorf("RevisedPrompt = %q", res.RevisedPrompt)
	}

	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("读取落盘文件: %v", err)
	}
	if string(data) != string(img) {
		t.Fatalf("落盘内容与响应不一致")
	}
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("文件权限 = %o，期望 600", perm)
	}
}

// TestGenerateMultipleImages 覆盖 n 与多张图片落盘，并保留上游真实格式后缀。
func TestGenerateMultipleImages(t *testing.T) {
	srv, requests := newGenerationServer(t, []map[string]any{
		{"b64_json": base64.StdEncoding.EncodeToString(pngBytes())},
		{"b64_json": base64.StdEncoding.EncodeToString(jpegBytes())},
	})
	g, err := New(Options{BaseURL: srv.URL, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	results, err := g.Generate(context.Background(), Request{Prompt: "two cats", N: 2, Model: "gpt-image-x"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}
	if !strings.HasSuffix(results[0].Path, ".png") || results[0].MimeType != "image/png" {
		t.Errorf("第一张 = %+v", results[0])
	}
	if !strings.HasSuffix(results[1].Path, ".jpg") || results[1].MimeType != "image/jpeg" {
		t.Errorf("第二张 = %+v", results[1])
	}

	got := <-requests
	if got.body.N != 2 || got.body.Model != "gpt-image-x" {
		t.Errorf("请求体 = %+v", got.body)
	}
	if got.auth != "" {
		t.Errorf("未配置密钥时不应发送 Authorization，得到 %q", got.auth)
	}
}

// TestGenerateDownloadsURLImage 覆盖 data[0].url 分支：需要下载后再落盘。
func TestGenerateDownloadsURLImage(t *testing.T) {
	img := pngBytes()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"url": srv.URL + "/files/out.png"}},
		})
	})
	mux.HandleFunc("/files/out.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(img)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	g, err := New(Options{BaseURL: srv.URL, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	results, err := g.Generate(context.Background(), Request{Prompt: "download me"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(results) != 1 || results[0].MimeType != "image/png" {
		t.Fatalf("结果 = %+v", results)
	}
	data, err := os.ReadFile(results[0].Path)
	if err != nil {
		t.Fatalf("读取落盘文件: %v", err)
	}
	if string(data) != string(img) {
		t.Fatal("下载的图片内容不一致")
	}
}

// TestGenerateDefaults 覆盖空 model/size/N 的默认值。
func TestGenerateDefaults(t *testing.T) {
	srv, requests := newGenerationServer(t, []map[string]any{{
		"b64_json": base64.StdEncoding.EncodeToString(pngBytes()),
	}})
	g, err := New(Options{BaseURL: srv.URL, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := g.Generate(context.Background(), Request{Prompt: "defaults"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := <-requests
	if got.body.Model != DefaultModel {
		t.Errorf("model = %q，期望 %q", got.body.Model, DefaultModel)
	}
	if got.body.Size != DefaultSize {
		t.Errorf("size = %q，期望 %q", got.body.Size, DefaultSize)
	}
	if got.body.N != 1 {
		t.Errorf("n = %d，期望 1", got.body.N)
	}
}

// TestGenerateErrors 覆盖参数校验、上游错误与无图片响应。
func TestGenerateErrors(t *testing.T) {
	okServer, _ := newGenerationServer(t, []map[string]any{{
		"b64_json": base64.StdEncoding.EncodeToString(pngBytes()),
	}})

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	}))
	defer errorServer.Close()

	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer emptyServer.Close()

	badBase64Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"not-base64!!"}]}`))
	}))
	defer badBase64Server.Close()

	unsupportedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{
			"b64_json": base64.StdEncoding.EncodeToString([]byte("plain text")),
		}}})
	}))
	defer unsupportedServer.Close()

	cases := []struct {
		name     string
		url      string
		req      Request
		wantErr  error
		contains string
	}{
		{name: "空提示词", url: okServer.URL, req: Request{Prompt: "   "}, wantErr: ErrEmptyPrompt},
		{name: "张数过多", url: okServer.URL, req: Request{Prompt: "x", N: 11}, wantErr: ErrInvalidRequest},
		{name: "上游错误", url: errorServer.URL, req: Request{Prompt: "x"}, wantErr: ErrUpstream, contains: "rate limited"},
		{name: "无图片", url: emptyServer.URL, req: Request{Prompt: "x"}, wantErr: ErrNoImage},
		{name: "非法 base64", url: badBase64Server.URL, req: Request{Prompt: "x"}, wantErr: nil, contains: "decode base64"},
		{name: "不支持的格式", url: unsupportedServer.URL, req: Request{Prompt: "x"}, wantErr: ErrUnsupportedImage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := New(Options{BaseURL: tc.url, Dir: t.TempDir()})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = g.Generate(context.Background(), tc.req)
			if err == nil {
				t.Fatal("期望错误，得到 nil")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("错误 = %v，期望 %v", err, tc.wantErr)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("错误 = %v，期望包含 %q", err, tc.contains)
			}
		})
	}
}

// TestGenerateCleansUpPartialFiles 覆盖第二张失败时删除第一张已写入的文件。
func TestGenerateCleansUpPartialFiles(t *testing.T) {
	srv, _ := newGenerationServer(t, []map[string]any{
		{"b64_json": base64.StdEncoding.EncodeToString(pngBytes())},
		{"b64_json": "not-base64!!"},
	})
	dir := t.TempDir()
	g, err := New(Options{BaseURL: srv.URL, Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := g.Generate(context.Background(), Request{Prompt: "partial", N: 2}); err == nil {
		t.Fatal("期望错误")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("失败后目录应清空，仍有 %d 个文件", len(entries))
	}
}

// TestNewRejectsInvalidBaseURL 覆盖 baseURL 校验。
func TestNewRejectsInvalidBaseURL(t *testing.T) {
	cases := []string{"ftp://example.com", "example.com", "://bad", "https://"}
	for _, in := range cases {
		if _, err := New(Options{BaseURL: in}); !errors.Is(err, ErrInvalidBaseURL) {
			t.Errorf("New(%q) 错误 = %v，期望 ErrInvalidBaseURL", in, err)
		}
	}
}

// TestImagesEndpoint 覆盖 baseURL 拼接规则。
func TestImagesEndpoint(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", DefaultBaseURL + "/v1/images/generations"},
		{"https://api.openai.com", "https://api.openai.com/v1/images/generations"},
		{"https://api.openai.com/", "https://api.openai.com/v1/images/generations"},
		{"https://relay.test/v1", "https://relay.test/v1/images/generations"},
		{"https://relay.test/v1/", "https://relay.test/v1/images/generations"},
		{"https://relay.test/api/v1", "https://relay.test/api/v1/images/generations"},
		{"https://relay.test/v1/images/generations", "https://relay.test/v1/images/generations"},
		{"http://127.0.0.1:11434/v1", "http://127.0.0.1:11434/v1/images/generations"},
	}
	for _, tc := range cases {
		got, err := imagesEndpoint(tc.in)
		if err != nil {
			t.Errorf("imagesEndpoint(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("imagesEndpoint(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestOutputDirDefault 校验未指定 Dir 时落到 ~/.b-desktop/images。
func TestOutputDirDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("UserHomeDir 在 Windows 上使用 USERPROFILE")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	g, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir, err := g.outputDir()
	if err != nil {
		t.Fatalf("outputDir: %v", err)
	}
	want := filepath.Join(home, ".b-desktop", "images")
	if dir != want {
		t.Fatalf("outputDir = %q，期望 %q", dir, want)
	}

	// 生成时自动创建目录，权限 0700。
	srv, _ := newGenerationServer(t, []map[string]any{{
		"b64_json": base64.StdEncoding.EncodeToString(pngBytes()),
	}})
	g, err = New(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := g.Generate(context.Background(), Request{Prompt: "home"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("目录权限 = %o，期望 700", perm)
	}
}

// TestDetectImage 覆盖格式嗅探。
func TestDetectImage(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		mime string
		ok   bool
	}{
		{"png", pngBytes(), "image/png", true},
		{"jpeg", jpegBytes(), "image/jpeg", true},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp", true},
		{"text", []byte("hello"), "", false},
	}
	for _, tc := range cases {
		mime, ok := detectImage(tc.data)
		if ok != tc.ok || mime != tc.mime {
			t.Errorf("%s: detectImage = (%q, %v)，期望 (%q, %v)", tc.name, mime, ok, tc.mime, tc.ok)
		}
	}
}
