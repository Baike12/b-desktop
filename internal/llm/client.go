package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 默认 HTTP 与重试参数。零值字段在构造 Provider 时被替换成这些默认值。
const (
	defaultMaxRetries        = 5
	defaultBaseBackoff       = 500 * time.Millisecond
	defaultMaxBackoff        = 30 * time.Second
	defaultFirstTokenTimeout = 60 * time.Second
	defaultIdleTimeout       = 120 * time.Second

	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultOpenAIBaseURL    = "https://api.openai.com"
	defaultUserAgent        = "anc-dsk-llm/1.0"

	// maxErrorBody 是读取非 2xx 响应体的上限，避免恶意端点返回超大数据。
	maxErrorBody = 64 << 10
)

// HTTPOptions 是两种 HTTP Provider 的公共配置。
type HTTPOptions struct {
	BaseURL           string        // 为空时使用供应商默认地址
	APIKey            string        // 调用所需的密钥
	HTTPClient        *http.Client  // nil 时使用内部默认客户端（Timeout 为 0，超时由 ctx 与 idle 控制）
	MaxRetries        int           // 0 时取默认 5；负数表示关闭重试
	BaseBackoff       time.Duration // 默认 500ms
	MaxBackoff        time.Duration // 默认 30s
	FirstTokenTimeout time.Duration // 默认 60s
	IdleTimeout       time.Duration // 默认 120s
	Headers           map[string]string
	UserAgent         string
}

// NewProvider 按供应商名创建 Provider。
// name 支持 "anthropic" 与 "openai"（大小写不敏感）；其它名称返回错误。
func NewProvider(name string, opts HTTPOptions) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "anthropic":
		return NewAnthropic(opts), nil
	case "openai":
		return NewOpenAI(opts), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", name)
	}
}

// Error 实现 error 接口，输出形如
// "llm: anthropic (status 429) [rate_limit]: rate limited"。
func (e *StreamError) Error() string {
	if e == nil {
		return "llm: <nil>"
	}
	var b strings.Builder
	b.WriteString("llm")
	if e.Provider != "" {
		b.WriteString(": ")
		b.WriteString(e.Provider)
	}
	if e.Status != 0 {
		fmt.Fprintf(&b, " (status %d)", e.Status)
	}
	if e.Kind != "" {
		b.WriteString(" [")
		b.WriteString(string(e.Kind))
		b.WriteString("]")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Unwrap 返回 nil。StreamError 不携带底层错误链；由 ctx 取消产生的错误使用
// wrappedStreamError 包装 context.Canceled / context.DeadlineExceeded。
func (e *StreamError) Unwrap() error { return nil }

// Is 让 errors.Is(err, ErrInvalidRequest) 对 invalid_request 分类成立，
// 使调用方既能用错误分类判断，也能用哨兵判断。
func (e *StreamError) Is(target error) bool {
	return target == ErrInvalidRequest && e.Kind == ErrKindInvalidRequest
}

// wrappedStreamError 把 *StreamError 与底层 cause（ctx 取消/超时）同时暴露：
// errors.Is 通过 Unwrap 命中 context.Canceled，errors.As 通过 As 命中 *StreamError。
type wrappedStreamError struct {
	se    *StreamError
	cause error
}

func (e *wrappedStreamError) Error() string {
	if e.se == nil {
		return e.cause.Error()
	}
	return e.se.Error()
}

// Unwrap 返回底层原因。
func (e *wrappedStreamError) Unwrap() error { return e.cause }

// As 让 errors.As 能从包装错误中取出 *StreamError。
func (e *wrappedStreamError) As(target any) bool {
	if p, ok := target.(**StreamError); ok {
		*p = e.se
		return true
	}
	return false
}

// newCanceledError 构造携带 ctx 原因的错误。
func newCanceledError(provider string, cause error) error {
	return &wrappedStreamError{
		se: &StreamError{
			Kind:     ErrKindCanceled,
			Message:  cause.Error(),
			Provider: provider,
		},
		cause: cause,
	}
}

// invalidRequest 构造参数非法错误，errors.Is(err, ErrInvalidRequest) 为真。
func invalidRequest(provider, message string) error {
	return &StreamError{
		Kind:     ErrKindInvalidRequest,
		Message:  message,
		Provider: provider,
	}
}

// providerConfig 是两个 HTTP Provider 共享的不可变运行时配置。
type providerConfig struct {
	name              string
	url               string
	apiKey            string
	headers           map[string]string
	client            *http.Client
	maxRetries        int
	baseBackoff       time.Duration
	maxBackoff        time.Duration
	firstTokenTimeout time.Duration
	idleTimeout       time.Duration
	newDecoder        func() (sseDecoder, error)
	httpError         func(status int, header http.Header, body []byte) *StreamError
}

// newProviderConfig 归一化公共选项并拼出请求地址。
// path 以 "/v1/" 开头；BaseURL 已以 "/v1" 结尾时不会重复拼接。
func newProviderConfig(name, defaultBase, path string, opts HTTPOptions, auth map[string]string, dec func() (sseDecoder, error), httpErr func(int, http.Header, []byte) *StreamError) providerConfig {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = defaultBase
	}
	endpoint := path
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		endpoint = strings.TrimPrefix(path, "/v1")
	}

	retries := opts.MaxRetries
	switch {
	case retries < 0:
		retries = 0 // 负数显式关闭重试。
	case retries == 0:
		retries = defaultMaxRetries
	}
	baseBackoff := opts.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultBaseBackoff
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxBackoff
	}
	if maxBackoff < baseBackoff {
		maxBackoff = baseBackoff
	}
	firstToken := opts.FirstTokenTimeout
	if firstToken <= 0 {
		firstToken = defaultFirstTokenTimeout
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}

	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "text/event-stream",
		"User-Agent":   ua,
	}
	for k, v := range auth {
		headers[k] = v
	}
	for k, v := range opts.Headers {
		headers[k] = v
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

	return providerConfig{
		name:              name,
		url:               base + endpoint,
		apiKey:            opts.APIKey,
		headers:           headers,
		client:            client,
		maxRetries:        retries,
		baseBackoff:       baseBackoff,
		maxBackoff:        maxBackoff,
		firstTokenTimeout: firstToken,
		idleTimeout:       idle,
		newDecoder:        dec,
		httpError:         httpErr,
	}
}

// stream 发起请求并在「请求未建立」阶段做重试。一旦 2xx 响应建立就返回 Stream，
// 此后的失败只通过 Stream.Recv 返回，不再重试。
func (c *providerConfig) stream(ctx context.Context, payload []byte) (Stream, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("llm: %s: %w", c.name, ErrMissingAPIKey)
	}

	var prefix []StreamEvent
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
		if err != nil {
			return nil, invalidRequest(c.name, err.Error())
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, newCanceledError(c.name, cerr)
			}
			se := &StreamError{
				Kind:      ErrKindNetwork,
				Message:   err.Error(),
				Retryable: true,
				Provider:  c.name,
			}
			if attempt >= c.maxRetries {
				return nil, se
			}
			delay := c.backoff(attempt+1, 0)
			prefix = append(prefix, retryEvent(attempt+1, c.maxRetries, delay, se))
			if werr := waitBackoff(ctx, delay); werr != nil {
				return nil, newCanceledError(c.name, werr)
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			_ = resp.Body.Close()
			se := c.httpError(resp.StatusCode, resp.Header, body)
			if se.Retryable && attempt < c.maxRetries {
				retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
				delay := c.backoff(attempt+1, retryAfter)
				prefix = append(prefix, retryEvent(attempt+1, c.maxRetries, delay, se))
				if werr := waitBackoff(ctx, delay); werr != nil {
					return nil, newCanceledError(c.name, werr)
				}
				continue
			}
			return nil, se
		}

		dec, err := c.newDecoder()
		if err != nil {
			_ = resp.Body.Close()
			return nil, err
		}
		st := newSSEStream(c.name, resp.Body, dec, c.firstTokenTimeout, c.idleTimeout)
		if len(prefix) == 0 {
			return st, nil
		}
		return &retryStream{prefix: prefix, inner: st}, nil
	}
}

// backoff 返回第 retry 次重试前的等待时间。
// retry 从 1 开始：min(BaseBackoff*2^(retry-1), MaxBackoff) + 0–25% 抖动。
// retryAfter 非零时优先使用，并钳制到 MaxBackoff。
func (c *providerConfig) backoff(retry int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > c.maxBackoff {
			return c.maxBackoff
		}
		return retryAfter
	}
	d := c.baseBackoff
	for i := 1; i < retry && d < c.maxBackoff; i++ {
		if d > c.maxBackoff/2 {
			d = c.maxBackoff
			break
		}
		d *= 2
	}
	if d > c.maxBackoff {
		d = c.maxBackoff
	}
	if d <= 0 {
		return 0
	}
	// 抖动范围 [0, 25%]。
	jitter := time.Duration(rand.Int64N(int64(d/4) + 1))
	return d + jitter
}

// retryEvent 构造一次重试通知事件。
func retryEvent(attempt, max int, delay time.Duration, se *StreamError) StreamEvent {
	reason := "request failed"
	if se != nil && se.Message != "" {
		reason = se.Message
	}
	return StreamEvent{
		Type: EventRetry,
		Retry: &RetryInfo{
			Attempt: attempt,
			Max:     max,
			DelayMS: delay.Milliseconds(),
			Reason:  reason,
		},
	}
}

// waitBackoff 等待退避时间，ctx 取消时立即返回。
func waitBackoff(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseRetryAfter 解析 Retry-After：秒数或 HTTP 日期。非法或非正数返回 0。
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		return d
	}
	return 0
}

// isRetryableStatus 按设计文档 §4.4 的可重试列表判定。
func isRetryableStatus(status int) bool {
	switch status {
	case 408, 409, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// classifyHTTPError 把非 2xx 响应转换成结构化错误。
func classifyHTTPError(provider string, status int, body []byte) *StreamError {
	msg := apiErrorMessage(body)
	kind := ErrKindInvalidRequest
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = ErrKindAuth
	case status == http.StatusTooManyRequests:
		kind = ErrKindRateLimit
	case status == 529 || status == http.StatusServiceUnavailable:
		kind = ErrKindOverloaded
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		if looksLikeContextOverflow(msg) {
			kind = ErrKindContextOverflow
		}
	case status == http.StatusRequestTimeout || status == http.StatusConflict:
		kind = ErrKindServer
	case status >= 500:
		kind = ErrKindServer
	}
	return &StreamError{
		Kind:      kind,
		Message:   msg,
		Status:    status,
		Retryable: isRetryableStatus(status),
		Provider:  provider,
	}
}

// apiErrorMessage 提取常见错误体中的 message。
func apiErrorMessage(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 {
		var env struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if err := json.Unmarshal(trimmed, &env); err == nil {
			switch {
			case env.Error.Message != "":
				return env.Error.Message
			case env.Message != "":
				return env.Message
			case env.Error.Code != "":
				return env.Error.Code
			case env.Code != "":
				return env.Code
			}
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

// looksLikeContextOverflow 用常见文案识别上下文超窗。
func looksLikeContextOverflow(msg string) bool {
	m := strings.ToLower(msg)
	for _, needle := range []string{
		"context_length_exceeded",
		"context length",
		"maximum context",
		"context window",
		"prompt is too long",
		"too many tokens",
		"input is too long",
	} {
		if strings.Contains(m, needle) {
			return true
		}
	}
	return false
}

// sseFrame 是一帧 SSE：event 名（可为空）与拼接后的 data。
type sseFrame struct {
	Event string
	Data  []byte
}

// sseDecoder 把一帧解码成零到多个 StreamEvent。
// done 为 true 表示协议层已结束（message_stop / [DONE]）。
type sseDecoder interface {
	Decode(frame sseFrame) (events []StreamEvent, done bool, err error)
}

// sseFinisher 由需要在连接 EOF 时补发收尾事件的解码器实现。
type sseFinisher interface {
	Finish() ([]StreamEvent, error)
}

// readSSEFrame 读取下一帧。注释行忽略；多行 data 用换行拼接。
func readSSEFrame(r *bufio.Reader) (string, []byte, error) {
	var event string
	var data strings.Builder
	have := false
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if have {
					return event, []byte(data.String()), nil
				}
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
				have = true
			case strings.HasPrefix(line, "data:"):
				v := line[len("data:"):]
				v = strings.TrimPrefix(v, " ")
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(v)
				have = true
			case strings.HasPrefix(line, ":"):
				// SSE 注释（含心跳），忽略。
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && have {
				return event, []byte(data.String()), nil
			}
			return "", nil, err
		}
	}
}

// sseStream 是一次进行中的 SSE 流。
// 读取 goroutine 只在通道可写或 done 关闭时投递，因此 Close 不会因通道满而阻塞。
type sseStream struct {
	provider string
	body     io.ReadCloser
	events   chan StreamEvent
	errCh    chan error
	done     chan struct{}
	wg       sync.WaitGroup

	firstToken time.Duration
	idle       time.Duration

	closeOnce     sync.Once
	bodyCloseOnce sync.Once

	// startedAt 用于首字节超时：从流建立（响应头到达）开始计时。
	startedAt time.Time
	// gotFirst 只由 Recv 的单消费者读写，不需要同步。
	gotFirst bool
}

// newSSEStream 创建流并启动唯一的读取 goroutine。
func newSSEStream(provider string, body io.ReadCloser, dec sseDecoder, firstToken, idle time.Duration) *sseStream {
	s := &sseStream{
		provider:   provider,
		body:       body,
		events:     make(chan StreamEvent, 32),
		errCh:      make(chan error, 1),
		done:       make(chan struct{}),
		firstToken: firstToken,
		idle:       idle,
		startedAt:  time.Now(),
	}
	s.wg.Add(1)
	go s.run(dec)
	return s
}

// run 是唯一的读取 goroutine。
func (s *sseStream) run(dec sseDecoder) {
	defer s.wg.Done()
	defer close(s.events)

	br := bufio.NewReaderSize(s.body, 32*1024)
	for {
		event, data, err := readSSEFrame(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// 连接在协议收尾前断开时，解码器仍要补齐 tool_use_stop / message_stop。
				if f, ok := dec.(sseFinisher); ok {
					evs, ferr := f.Finish()
					if ferr != nil {
						s.fail(ferr)
						return
					}
					for _, ev := range evs {
						if !s.emit(ev) {
							return
						}
					}
				}
				return
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				s.fail(newCanceledError(s.provider, err))
				return
			}
			s.fail(err)
			return
		}
		if event == "" && len(data) == 0 {
			continue
		}

		evs, done, derr := dec.Decode(sseFrame{Event: event, Data: data})
		if derr != nil {
			s.fail(derr)
			return
		}
		for _, ev := range evs {
			if !s.emit(ev) {
				return
			}
		}
		if done {
			s.closeBody()
			return
		}
	}
}

// emit 投递一个事件；调用方已 Close 时返回 false 并结束读取。
func (s *sseStream) emit(ev StreamEvent) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.done:
		return false
	}
}

// fail 记录终止错误。先写 errCh 再关闭 events 的顺序由 run 的 defer 保证。
func (s *sseStream) fail(err error) {
	var se *StreamError
	if !errors.As(err, &se) {
		err = &StreamError{Kind: ErrKindUnknown, Message: err.Error(), Provider: s.provider}
	}
	select {
	case s.errCh <- err:
	default:
	}
}

// closeBody 关闭响应体，可重复调用。
func (s *sseStream) closeBody() {
	s.bodyCloseOnce.Do(func() {
		_ = s.body.Close()
	})
}

// Recv 实现 Stream。
func (s *sseStream) Recv(ctx context.Context) (StreamEvent, error) {
	var timer *time.Timer
	if !s.gotFirst {
		// 首字节超时从响应头到达（流建立）开始计时，而不是从第一次 Recv 开始。
		remaining := s.firstToken - time.Since(s.startedAt)
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		timer = time.NewTimer(remaining)
	} else {
		timer = time.NewTimer(s.idle)
	}
	defer timer.Stop()

	select {
	case ev, ok := <-s.events:
		if !ok {
			// events 已关闭：先做一次非阻塞的 errCh 检查，避免 select 随机选中
			// 已关闭通道而吞掉真实错误。
			select {
			case err := <-s.errCh:
				return StreamEvent{}, err
			default:
			}
			return StreamEvent{}, io.EOF
		}
		s.gotFirst = true
		return ev, nil
	case err := <-s.errCh:
		return StreamEvent{}, err
	case <-ctx.Done():
		return StreamEvent{}, newCanceledError(s.provider, ctx.Err())
	case <-timer.C:
		s.closeBody()
		what := "stream idle timeout"
		limit := s.idle
		if !s.gotFirst {
			what = "first token timeout"
			limit = s.firstToken
		}
		return StreamEvent{}, &StreamError{
			Kind:      ErrKindNetwork,
			Message:   fmt.Sprintf("%s after %s", what, limit),
			Retryable: false,
			Provider:  s.provider,
		}
	}
}

// Close 实现 Stream：幂等，不等待未被消费的事件。
func (s *sseStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.closeBody()
		s.wg.Wait()
	})
	return nil
}

// retryStream 在真实流之前先吐出重试通知事件。
type retryStream struct {
	prefix []StreamEvent
	inner  Stream
}

// Recv 先返回重试通知，再转发真实流事件。
func (s *retryStream) Recv(ctx context.Context) (StreamEvent, error) {
	if len(s.prefix) > 0 {
		ev := s.prefix[0]
		s.prefix = s.prefix[1:]
		return ev, nil
	}
	return s.inner.Recv(ctx)
}

// Close 关闭底层流。
func (s *retryStream) Close() error { return s.inner.Close() }
