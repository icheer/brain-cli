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
)

// ChatResult 是一次完整生成结果，由 SSE 增量累积而来。
type ChatResult struct {
	Content      string // 模型正文
	Thinking     string // 推理模型的思维链（若有）
	FinishReason string // stop / length / content_filter ...
	Usage        *Usage // 网关回传则为非 nil
}

// DeltaHandler 收到增量时的回调（content/reasoning 各自可为空串）。
// M2 的 --stream 在此挂载增量打印。
type DeltaHandler func(content, reasoning string)

// streamChunk 是 SSE 单个 data 事件的负载结构。
// 真实网关实测（one-api 系）：错误可能以 HTTP 200 + data:{"error":{...}} 形式出现在流内。
type streamChunk struct {
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"` // 数值或字符串
	} `json:"error"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"` // DeepSeek 系思维链字段
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// ChatCompletionStream 发起 SSE 请求并累积完整结果。
// 中途断流返回错误并丢弃已收内容——stdout 等完成才输出，不会污染，
// 是否换模型重试由 AskEngine 决定（TODO.md 第 0 节 fallback 范围）。
func (c *Client) ChatCompletionStream(ctx context.Context, req ChatRequest, onDelta DeltaHandler) (*ChatResult, error) {
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("User-Agent", UserAgent)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		if isTimeout(err) {
			return nil, &TimeoutError{Err: err}
		}
		return nil, fmt.Errorf("网络错误: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, &StatusError{
			Status:     resp.StatusCode,
			Body:       truncate(string(data), 400),
			RetryAfter: retryAfter(resp),
		}
	}

	var res ChatResult
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue // 事件分隔空行与网关 keep-alive 注释
		}
		if !strings.HasPrefix(line, "data:") {
			continue // 非 SSE 行，容忍杂音
		}
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
			return nil, &StatusError{
				Status: errEventCode(ch.Error.Code),
				Body:   fmt.Sprintf("%s (type=%s)", ch.Error.Message, ch.Error.Type),
			}
		}
		if ch.Usage != nil {
			res.Usage = ch.Usage
		}
		if len(ch.Choices) == 0 {
			continue
		}
		d := ch.Choices[0].Delta
		if d.Content != "" || d.ReasoningContent != "" {
			res.Content += d.Content
			res.Thinking += d.ReasoningContent
			if onDelta != nil {
				onDelta(d.Content, d.ReasoningContent)
			}
		}
		if ch.Choices[0].FinishReason != "" {
			res.FinishReason = ch.Choices[0].FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		if isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &TimeoutError{Err: err}
		}
		return nil, fmt.Errorf("流中断: %w", err)
	}
	if res.Content == "" && res.Thinking == "" && res.FinishReason == "" {
		return nil, fmt.Errorf("流式响应没有任何内容（网关异常）")
	}
	return &res, nil
}

// errEventCode 把流内 error 事件的 code 归一为 HTTP 状态语义（解析失败按 502 上游错误）。
func errEventCode(v any) int {
	switch c := v.(type) {
	case float64:
		if c >= 100 && c < 600 {
			return int(c)
		}
	case string:
		if n, err := strconv.Atoi(c); err == nil && n >= 100 && n < 600 {
			return n
		}
	}
	return http.StatusBadGateway
}
