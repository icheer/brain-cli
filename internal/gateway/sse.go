package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/icheer/brain-cli/internal/i18n"
)

// ChatResult 是一次完整生成结果，由 SSE 增量累积而来。
type ChatResult struct {
	Content      string // 模型正文
	Thinking     string // 推理模型的思维链（若有）
	FinishReason string // stop / length / content_filter ...
	Usage        *Usage // 网关回传则为非 nil
}

// marshalWithExtras 序列化请求体并应用 --extra-body：结构体字段先落 map，
// extras 顶层键 assign 覆盖（浅层，不递归合并）。保留字由 cmd 层校验，此处信任入参。
func marshalWithExtras(req ChatRequest) ([]byte, error) {
	if len(req.ExtraBody) == 0 {
		return json.Marshal(req)
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range req.ExtraBody {
		m[k] = v
	}
	return json.Marshal(m)
}

// DeltaHandler 收到增量时的回调（content/reasoning 各自可为空串）。
// M2 的 --stream 在此挂载增量打印。
type DeltaHandler func(content, reasoning string)

type streamError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"` // 数值或字符串
}

// streamChunk 是 SSE 单个 data 事件的负载结构。
// 真实网关实测（one-api 系）：错误可能以 HTTP 200 + data:{"error":{...}} 形式出现在流内。
type streamChunk struct {
	Error   *streamError `json:"error"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"` // DeepSeek / one-api 系思维链字段
			Reasoning        string `json:"reasoning"`         // OpenRouter 等流式思维链字段
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// ChatCompletionStream 发起 SSE 请求并累积完整结果。
// 中途断流返回错误并丢弃已收内容——stdout 等完成才输出，不会污染，
// 是否换模型重试由 AskEngine 决定（DESIGN.md 第 0 节 fallback 范围）。
func (c *Client) ChatCompletionStream(ctx context.Context, req ChatRequest, onDelta DeltaHandler) (*ChatResult, error) {
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

	body, err := marshalWithExtras(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("序列化请求失败", "failed to marshal request"), err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("构造请求失败", "failed to build request"), err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("User-Agent", UserAgent)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	// --extra-headers 在默认头之后应用：同名覆盖（如 User-Agent）；
	// 保留字（Authorization/Content-Type/Accept）由 cmd 层拒绝，这里信任入参。
	for k, v := range req.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		if IsTimeout(err) {
			return nil, &TimeoutError{Err: err}
		}
		return nil, fmt.Errorf("%s: %w", i18n.T("网络错误", "network error"), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, &StatusError{
			Status:     resp.StatusCode,
			Body:       Truncate(string(data), 400),
			RetryAfter: RetryAfter(resp),
		}
	}

	var (
		res         ChatResult
		contentBuf  strings.Builder
		thinkingBuf strings.Builder
		nonSSEBuf   strings.Builder
		sawSSEData  bool
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue // 事件分隔空行与网关 keep-alive 注释
		}
		if !strings.HasPrefix(line, "data:") {
			// 个别网关以 HTTP 200 直接返回普通 JSON 错误体（无 data: 前缀），暂存前 4KiB 兜底识别
			if !sawSSEData && nonSSEBuf.Len() < 4096 {
				nonSSEBuf.WriteString(line)
				nonSSEBuf.WriteByte('\n')
			}
			continue
		}
		sawSSEData = true
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var ch streamChunk
		if err := json.Unmarshal([]byte(payload), &ch); err != nil {
			continue // 非 JSON 负载跳过
		}
		if ch.Error != nil {
			// 流内错误事件：转成 StatusError 参与退出码/故障转移分类
			return nil, statusErrFromStreamError(ch.Error)
		}
		if ch.Usage != nil {
			res.Usage = ch.Usage
		}
		if len(ch.Choices) == 0 {
			continue
		}
		d := ch.Choices[0].Delta
		reasoning := d.ReasoningContent
		if reasoning == "" {
			reasoning = d.Reasoning
		}
		if d.Content != "" || reasoning != "" {
			contentBuf.WriteString(d.Content)
			thinkingBuf.WriteString(reasoning)
			if onDelta != nil {
				onDelta(d.Content, reasoning)
			}
		}
		if ch.Choices[0].FinishReason != "" {
			res.FinishReason = ch.Choices[0].FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		if IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &TimeoutError{Err: err}
		}
		return nil, fmt.Errorf("%s: %w", i18n.T("流中断", "stream interrupted"), err)
	}
	res.Content = contentBuf.String()
	res.Thinking = thinkingBuf.String()
	if res.Content == "" && res.Thinking == "" && res.FinishReason == "" {
		if raw := strings.TrimSpace(nonSSEBuf.String()); raw != "" {
			var plain struct {
				Error *streamError `json:"error"`
			}
			if json.Unmarshal([]byte(raw), &plain) == nil && plain.Error != nil &&
				(plain.Error.Message != "" || plain.Error.Type != "" || plain.Error.Code != nil) {
				return nil, statusErrFromStreamError(plain.Error)
			}
			return nil, fmt.Errorf("%s（%s: %s）",
				i18n.T("流式响应没有任何内容（网关异常）", "stream produced no content (gateway misbehaving)"),
				i18n.T("响应片段", "response snippet"), OneLine(raw, 200))
		}
		return nil, fmt.Errorf("%s", i18n.T("流式响应没有任何内容（网关异常）", "stream produced no content (gateway misbehaving)"))
	}
	return &res, nil
}

func statusErrFromStreamError(e *streamError) *StatusError {
	body := fmt.Sprintf("%s (type=%s)", e.Message, e.Type)
	if e.Code != nil && e.Code != "" {
		body = fmt.Sprintf("%s (type=%s, code=%v)", e.Message, e.Type, e.Code)
	}
	return &StatusError{
		Status: errEventCode(e.Code, e.Type),
		Body:   Truncate(body, 400),
	}
}

// errEventCode 把流内 error 事件的 code/type 归一为 HTTP 状态语义（解析失败按 502 上游错误）。
func errEventCode(v any, errType string) int {
	switch c := v.(type) {
	case float64:
		if c >= 100 && c < 600 {
			return int(c)
		}
	case string:
		if n, err := strconv.Atoi(c); err == nil && n >= 100 && n < 600 {
			return n
		}
		if mapped := mapErrorCodeString(c); mapped != 0 {
			return mapped
		}
	}
	if mapped := mapErrorCodeString(errType); mapped != 0 {
		return mapped
	}
	return http.StatusBadGateway
}

func mapErrorCodeString(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "model_not_found":
		return http.StatusNotFound
	case "invalid_api_key", "authentication_error", "unauthorized":
		return http.StatusUnauthorized
	case "insufficient_quota", "permission_denied", "forbidden":
		return http.StatusForbidden
	case "rate_limit_exceeded", "rate_limited":
		return http.StatusTooManyRequests
	case "invalid_request_error", "bad_request":
		return http.StatusBadRequest
	}
	return 0
}
