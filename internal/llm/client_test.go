package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------- 测试辅助 ----------

// sseFrameText 把一条 data 组装成 SSE 帧。
func sseFrameText(data string) string {
	return "data: " + data + "\n\n"
}

// sseEventText 把 event 名与 data 组装成 SSE 帧。
func sseEventText(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// writeRaw 逐个写入原始帧并立即 flush。
func writeRaw(w http.ResponseWriter, frames ...string) {
	flusher, _ := w.(http.Flusher)
	for _, f := range frames {
		if _, err := io.WriteString(w, f); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// recvAll 读取到流结束，返回事件与终止错误。
func recvAll(t *testing.T, st Stream) ([]StreamEvent, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []StreamEvent
	for {
		ev, err := st.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
}

// eventTypes 返回事件类型序列，便于整体断言。
func eventTypes(events []StreamEvent) []EventType {
	out := make([]EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

// findEvent 返回第一个匹配类型的事件。
func findEvent(events []StreamEvent, typ EventType) (StreamEvent, bool) {
	for _, ev := range events {
		if ev.Type == typ {
			return ev, true
		}
	}
	return StreamEvent{}, false
}

// countEvents 统计某类事件数量。
func countEvents(events []StreamEvent, typ EventType) int {
	n := 0
	for _, ev := range events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// newAnthropicTestServer 返回固定 SSE 流的服务器，并记录请求体与请求头。
func newAnthropicTestServer(t *testing.T, frames ...string) (*httptest.Server, <-chan []byte) {
	t.Helper()
	bodyCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case bodyCh <- b:
		default:
		}
		if r.Header.Get("x-api-key") == "" {
			t.Errorf("缺少 x-api-key 头")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("缺少 anthropic-version 头")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeRaw(w, frames...)
	}))
	t.Cleanup(srv.Close)
	return srv, bodyCh
}

// newOpenAITestServer 返回固定 SSE 流的服务器，并记录请求体。
func newOpenAITestServer(t *testing.T, frames ...string) (*httptest.Server, <-chan []byte) {
	t.Helper()
	bodyCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case bodyCh <- b:
		default:
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("缺少 Authorization 头")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeRaw(w, frames...)
	}))
	t.Cleanup(srv.Close)
	return srv, bodyCh
}

// fastOptions 返回把退避压缩到毫秒级的选项，避免测试变慢。
func fastOptions(baseURL, apiKey string) HTTPOptions {
	return HTTPOptions{
		BaseURL:           baseURL,
		APIKey:            apiKey,
		MaxRetries:        3,
		BaseBackoff:       time.Millisecond,
		MaxBackoff:        5 * time.Millisecond,
		FirstTokenTimeout: 2 * time.Second,
		IdleTimeout:       2 * time.Second,
	}
}

// ---------- 工厂与错误类型 ----------

func TestNewProvider(t *testing.T) {
	for _, name := range []string{"anthropic", "Anthropic", " openai ", "OpenAI"} {
		p, err := NewProvider(name, HTTPOptions{APIKey: "k"})
		if err != nil {
			t.Fatalf("NewProvider(%q): %v", name, err)
		}
		if p == nil {
			t.Fatalf("NewProvider(%q) 返回 nil", name)
		}
	}
	if _, err := NewProvider("gemini", HTTPOptions{}); err == nil {
		t.Fatal("未知供应商应返回错误")
	}
}

func TestMissingAPIKey(t *testing.T) {
	for _, p := range []Provider{NewAnthropic(HTTPOptions{}), NewOpenAI(HTTPOptions{})} {
		_, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
		if !errors.Is(err, ErrMissingAPIKey) {
			t.Fatalf("%s: 期望 ErrMissingAPIKey，得到 %v", p.Name(), err)
		}
	}
}

func TestStreamErrorFormatAndIs(t *testing.T) {
	se := &StreamError{Kind: ErrKindRateLimit, Message: "slow down", Status: 429, Provider: "anthropic"}
	msg := se.Error()
	for _, part := range []string{"anthropic", "429", "rate_limit", "slow down"} {
		if !strings.Contains(msg, part) {
			t.Fatalf("Error() = %q，缺少 %q", msg, part)
		}
	}
	if errors.Is(se, ErrInvalidRequest) {
		t.Fatal("rate_limit 不应被视为 ErrInvalidRequest")
	}
	invalid := &StreamError{Kind: ErrKindInvalidRequest, Message: "bad"}
	if !errors.Is(invalid, ErrInvalidRequest) {
		t.Fatal("invalid_request 应命中 ErrInvalidRequest")
	}
	if errors.Unwrap(invalid) != nil {
		t.Fatal("StreamError.Unwrap 应返回 nil")
	}
}

// ---------- 重试、错误映射与取消 ----------

func TestHTTPRetryOn429ThenSuccess(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeRaw(w, sseEventText("message_start", `{"type":"message_start","message":{"id":"m1"}}`),
			sseEventText("message_stop", `{"type":"message_stop"}`))
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropic(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	events, err := recvAll(t, st)
	if err != nil {
		t.Fatalf("recvAll: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("期望 2 次尝试，实际 %d", attempts)
	}
	retry, ok := findEvent(events, EventRetry)
	if !ok {
		t.Fatal("缺少 retry 事件")
	}
	if retry.Retry == nil || retry.Retry.Attempt != 1 || retry.Retry.Max != 3 {
		t.Fatalf("retry 信息不正确: %+v", retry.Retry)
	}
	if retry.Retry.DelayMS > 5 {
		t.Fatalf("Retry-After 应被 MaxBackoff 钳制，实际 %dms", retry.Retry.DelayMS)
	}
	if !strings.Contains(retry.Retry.Reason, "slow down") {
		t.Fatalf("retry 原因不正确: %q", retry.Retry.Reason)
	}
	if _, ok := findEvent(events, EventMessageStart); !ok {
		t.Fatal("重试后应收到 message_start")
	}
}

func TestHTTPErrorNoRetryOn401(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	t.Cleanup(srv.Close)

	p := NewOpenAI(fastOptions(srv.URL, "bad"))
	_, err := p.Stream(context.Background(), Request{Model: "m"})
	if err == nil {
		t.Fatal("期望错误")
	}
	if attempts != 1 {
		t.Fatalf("401 不应重试，实际尝试 %d 次", attempts)
	}
	var se *StreamError
	if !errors.As(err, &se) {
		t.Fatalf("期望 *StreamError，得到 %T", err)
	}
	if se.Kind != ErrKindAuth || se.Status != 401 || se.Retryable {
		t.Fatalf("错误分类不正确: %+v", se)
	}
	if !strings.Contains(se.Message, "bad key") {
		t.Fatalf("错误信息未提取: %q", se.Message)
	}
}

func TestHTTPErrorContextOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"prompt is too long: 300000 tokens > 200000 maximum context length"}}`)
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropic(fastOptions(srv.URL, "k"))
	_, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	var se *StreamError
	if !errors.As(err, &se) {
		t.Fatalf("期望 *StreamError，得到 %T", err)
	}
	if se.Kind != ErrKindContextOverflow {
		t.Fatalf("期望 context_overflow，得到 %s", se.Kind)
	}
}

func TestServerErrorRetriesExhausted(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"overloaded"}}`)
	}))
	t.Cleanup(srv.Close)

	opts := fastOptions(srv.URL, "k")
	opts.MaxRetries = 2
	p := NewAnthropic(opts)
	_, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	var se *StreamError
	if !errors.As(err, &se) {
		t.Fatalf("期望 *StreamError，得到 %T", err)
	}
	if se.Kind != ErrKindOverloaded || !se.Retryable {
		t.Fatalf("错误分类不正确: %+v", se)
	}
	if attempts != 3 {
		t.Fatalf("期望 3 次尝试（1 次 + 2 次重试），实际 %d", attempts)
	}
}

func TestCanceledRecv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropic(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = st.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望包装 context.Canceled，得到 %v", err)
	}
	var se *StreamError
	if !errors.As(err, &se) {
		t.Fatalf("期望可通过 errors.As 取出 *StreamError，得到 %T", err)
	}
	if se.Kind != ErrKindCanceled {
		t.Fatalf("期望 canceled，得到 %s", se.Kind)
	}
}

func TestCloseWithFullEventChannel(t *testing.T) {
	frames := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		frames = append(frames, sseEventText("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"m%d"}}`, i)))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeRaw(w, frames...)
	}))
	t.Cleanup(srv.Close)

	p := NewAnthropic(fastOptions(srv.URL, "k"))
	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// 不消费事件，让内部 32 容量通道被填满，Close 仍必须立即返回。
	done := make(chan struct{})
	go func() {
		_ = st.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 在事件通道写满时阻塞")
	}
	_ = st.Close() // 幂等
}

func TestNoGoroutineLeakAfterClose(t *testing.T) {
	frames := []string{sseEventText("message_start", `{"type":"message_start","message":{"id":"m"}}`)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeRaw(w, frames...)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	before := runtime.NumGoroutine()
	p := NewAnthropic(fastOptions(srv.URL, "k"))
	for i := 0; i < 10; i++ {
		st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if _, err := st.Recv(context.Background()); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Recv: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		after := runtime.NumGoroutine()
		if after <= before+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("关闭后 goroutine 未回落：before=%d after=%d", before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFirstTokenTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	opts := fastOptions(srv.URL, "k")
	opts.FirstTokenTimeout = 50 * time.Millisecond
	p := NewAnthropic(opts)
	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	start := time.Now()
	_, err = st.Recv(context.Background())
	if err == nil {
		t.Fatal("期望首字节超时")
	}
	if time.Since(start) > time.Second {
		t.Fatal("首字节超时未及时生效")
	}
	var se *StreamError
	if !errors.As(err, &se) || !strings.Contains(se.Message, "first token timeout") {
		t.Fatalf("错误不正确: %v", err)
	}
}

func TestIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeRaw(w, sseEventText("message_start", `{"type":"message_start","message":{"id":"m"}}`))
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	opts := fastOptions(srv.URL, "k")
	opts.IdleTimeout = 50 * time.Millisecond
	p := NewAnthropic(opts)
	st, err := p.Stream(context.Background(), Request{Model: "m", MaxTokens: 16})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	if _, err := st.Recv(context.Background()); err != nil {
		t.Fatalf("第一个事件: %v", err)
	}
	_, err = st.Recv(context.Background())
	var se *StreamError
	if !errors.As(err, &se) || !strings.Contains(se.Message, "idle timeout") {
		t.Fatalf("期望空闲超时，得到 %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("3", now); d != 3*time.Second {
		t.Fatalf("秒数解析错误: %v", d)
	}
	if d := parseRetryAfter("", now); d != 0 {
		t.Fatalf("空值应为 0: %v", d)
	}
	if d := parseRetryAfter("-1", now); d != 0 {
		t.Fatalf("负数应为 0: %v", d)
	}
	future := now.Add(2 * time.Second).Format(http.TimeFormat)
	if d := parseRetryAfter(future, now); d != 2*time.Second {
		t.Fatalf("HTTP 日期解析错误: %v", d)
	}
}

func TestHTTPOptionsDefaultsDoNotMutate(t *testing.T) {
	p := NewAnthropic(HTTPOptions{BaseURL: "http://example.com/v1/", APIKey: "k"})
	if p.cfg.url != "http://example.com/v1/messages" {
		t.Fatalf("BaseURL 已含 /v1 时不应重复拼接: %s", p.cfg.url)
	}
	if p.cfg.maxRetries != defaultMaxRetries || p.cfg.firstTokenTimeout != defaultFirstTokenTimeout || p.cfg.idleTimeout != defaultIdleTimeout {
		t.Fatalf("默认值填充不正确: %+v", p.cfg)
	}
	o := NewOpenAI(HTTPOptions{BaseURL: "http://example.com/v1", APIKey: "k"})
	if o.cfg.url != "http://example.com/v1/chat/completions" {
		t.Fatalf("OpenAI 地址不正确: %s", o.cfg.url)
	}
}
