package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 客户端默认值：最多重试 3 次（429/5xx/网络错误，指数退避），单次尝试默认超时 120s。
const (
	defaultMaxRetries = 3
	defaultTimeout    = 120 * time.Second
	retryBaseDelay    = 500 * time.Millisecond
	retryMaxDelay     = 8 * time.Second
)

// Client OpenAI 兼容 HTTP 客户端，只负责收发 /chat/completions，不感知事件。
type Client struct {
	BaseURL    string        // API 基础地址，如 https://api.deepseek.com
	Secret     string        // API 密钥
	HTTPClient *http.Client  // 底层客户端，nil 时用 http.DefaultClient
	Timeout    time.Duration // 单次尝试超时，0 表示不额外加超时（仅依赖 ctx），负数用默认 120s
	MaxRetries int           // 429/5xx/网络错误最大重试次数，负数用默认 3
}

// Chat 发起 /chat/completions 对话；req.Stream 决定流式与否，两种路径都聚合为 Result。
// 429/5xx/网络错误按指数退避重试，ctx 取消立即中止。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*Result, error) {
	return c.chat(ctx, req, nil)
}

// ChatStream 与 Chat 相同，但每收到一帧流式增量就调用 onDelta（可为 nil）。
// onDelta 返回 error 会立即中止流并把该错误返回给调用方。
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onDelta func(StreamDelta) error) (*Result, error) {
	return c.chat(ctx, req, onDelta)
}

// chat 是 Chat/ChatStream 的公共实现。
func (c *Client) chat(ctx context.Context, req ChatRequest, onDelta func(StreamDelta) error) (*Result, error) {
	start := time.Now()
	attempts := c.maxRetries() + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, retryAfter, err := c.doChat(ctx, req, onDelta)
		if err == nil {
			res.Duration = time.Since(start)
			return res, nil
		}
		lastErr = err
		var pe *Error
		if !errors.As(err, &pe) || !pe.Retryable || attempt == attempts {
			break
		}
		if sleepErr := sleepRetry(ctx, backoffDelay(attempt, retryAfter)); sleepErr != nil {
			return nil, sleepErr
		}
	}
	if lastErr == nil {
		lastErr = &Error{Kind: KindUnknown, Message: "请求失败"}
	}
	return nil, lastErr
}

// doChat 单次尝试；返回的 time.Duration 是服务端 Retry-After 提示（无则 0）。
func (c *Client) doChat(ctx context.Context, req ChatRequest, onDelta func(StreamDelta) error) (*Result, time.Duration, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, &Error{Kind: KindRequest, Message: fmt.Sprintf("请求体编码失败: %v", err)}
	}
	if c.Timeout != 0 {
		var cancel context.CancelFunc
		timeout := c.Timeout
		if timeout < 0 {
			timeout = defaultTimeout
		}
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, 0, &Error{Kind: KindRequest, Message: fmt.Sprintf("构造请求失败: %v", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Secret != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Secret)
	}

	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, 0, classifySendErr(ctx, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := readErrMsg(resp.Body)
		return nil, retryAfterOf(resp.Header), classifyStatus(resp.StatusCode, msg)
	}

	res := &Result{}
	acc := newToolCallAcc()
	if req.Stream {
		if err := readStream(resp.Body, func(ch streamChunk) error {
			if ch.Usage != nil {
				res.Usage = *ch.Usage
			}
			for _, choice := range ch.Choices {
				res.Content += choice.Delta.Content
				res.Reasoning += choice.Delta.ReasoningContent
				acc.add(choice.Delta.ToolCalls)
				if choice.FinishReason != "" {
					res.FinishReason = choice.FinishReason
				}
			}
			if onDelta != nil {
				var d StreamDelta
				for _, choice := range ch.Choices {
					d.Content += choice.Delta.Content
					d.Reasoning += choice.Delta.ReasoningContent
					d.ToolCalls = append(d.ToolCalls, choice.Delta.ToolCalls...)
				}
				if err := onDelta(d); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, 0, err
		}
		res.ToolCalls = acc.result()
		return res, 0, nil
	}

	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, &Error{Kind: KindUnknown, Message: fmt.Sprintf("响应体解析失败: %v", err)}
	}
	for _, choice := range out.Choices {
		res.Content += choice.Message.Content
		res.Reasoning += choice.Message.ReasoningContent
		res.ToolCalls = append(res.ToolCalls, choice.Message.ToolCalls...)
		if choice.FinishReason != "" {
			res.FinishReason = choice.FinishReason
		}
	}
	if out.Usage != nil {
		res.Usage = *out.Usage
	}
	return res, 0, nil
}

// toolCallAcc 按 index 聚合流式 tool_call 增量，保持出现顺序。
type toolCallAcc struct {
	order []int
	byIdx map[int]*ToolCall
}

func newToolCallAcc() *toolCallAcc { return &toolCallAcc{byIdx: map[int]*ToolCall{}} }

func (a *toolCallAcc) add(deltas []ToolCallDelta) {
	for _, d := range deltas {
		tc, ok := a.byIdx[d.Index]
		if !ok {
			tc = &ToolCall{}
			a.byIdx[d.Index] = tc
			a.order = append(a.order, d.Index)
		}
		if d.ID != "" {
			tc.ID = d.ID
		}
		if d.Type != "" {
			tc.Type = d.Type
		}
		if d.Function.Name != "" {
			tc.Function.Name = d.Function.Name
		}
		tc.Function.Arguments += d.Function.Arguments
	}
}

// result 返回聚合后的工具调用列表（index 升序，即出现顺序）。
func (a *toolCallAcc) result() []ToolCall {
	if len(a.order) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		out = append(out, *a.byIdx[idx])
	}
	return out
}

// errStreamDone 表示流正常结束（收到 [DONE]）。
var errStreamDone = errors.New("stream done")

// readStream 解析 SSE 流：逐条收集 data: 负载（多行 data 以 "\n" 连接），空行派发一帧。
func readStream(r io.Reader, handle func(streamChunk) error) error {
	br := bufio.NewReader(r)
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		if payload == "[DONE]" {
			return errStreamDone
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return &Error{Kind: KindStream, Message: fmt.Sprintf("流式帧解析失败: %v", err)}
		}
		return handle(chunk)
	}
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimPrefix(strings.TrimRight(line, "\r\n"), "\ufeff")
			switch {
			case line == "": // 空行：一帧结束
				if fErr := flush(); fErr != nil {
					if fErr == errStreamDone {
						return nil
					}
					return fErr
				}
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(line[len("data:"):]))
			} // event:/注释行忽略
		}
		if err != nil {
			if err != io.EOF {
				return &Error{Kind: KindNetwork, Message: fmt.Sprintf("读取流失败: %v", err)}
			}
			// EOF：末尾无空行也要派发残余帧；无 [DONE] 视为正常结束（宽容处理）
			fErr := flush()
			if fErr != nil && fErr != errStreamDone {
				return fErr
			}
			return nil
		}
	}
}

// classifySendErr 把发送阶段的错误归类：取消 / 超时 / 网络。
func classifySendErr(ctx context.Context, err error) *Error {
	if errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled {
		return &Error{Kind: KindCanceled, Message: "请求已取消"}
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
		return &Error{Kind: KindTimeout, Message: "请求超时"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Kind: KindTimeout, Message: fmt.Sprintf("网络超时: %v", err)}
	}
	return &Error{Kind: KindNetwork, Message: fmt.Sprintf("网络错误: %v", err), Retryable: true}
}

// readErrMsg 读取错误响应体，截断为单行摘要。
func readErrMsg(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, 4<<10))
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return "请求失败"
	}
	s := string(bytes.TrimSpace(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// retryAfterOf 解析 Retry-After 头（秒），无则返回 0。
func retryAfterOf(h http.Header) time.Duration {
	s := strings.TrimSpace(h.Get("Retry-After"))
	if s == "" {
		return 0
	}
	if sec, err := strconv.Atoi(s); err == nil && sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return 0
}

// backoffDelay 指数退避：base * 2^(attempt-1)，封顶 retryMaxDelay，并与 Retry-After 取大。
func backoffDelay(attempt int, retryAfter time.Duration) time.Duration {
	d := retryBaseDelay << (attempt - 1)
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	d += time.Duration(rand.Int63n(int64(d / 2))) // 抖动，避免惊群
	if retryAfter > d {
		return retryAfter
	}
	return d
}

// sleepRetry 退避等待；ctx 取消时返回 KindCanceled 错误。
func sleepRetry(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return &Error{Kind: KindCanceled, Message: "请求已取消"}
	case <-t.C:
		return nil
	}
}

// maxRetries 返回生效的最大重试次数。
func (c *Client) maxRetries() int {
	if c.MaxRetries < 0 {
		return defaultMaxRetries
	}
	return c.MaxRetries
}
