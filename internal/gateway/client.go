package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// UserAgent 由 cmd 层在启动时注入实际版本（默认 dev）。
var UserAgent = "brain/dev"

// Client 是网关最小 HTTP 客户端。
type Client struct {
	HTTP    *http.Client
	BaseURL string // 形如 https://gw.example.com/v1
	APIKey  string
}

// New 构造客户端；timeout 覆盖整个请求生命周期（连接+首字节+读体）。
func New(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: timeout},
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
	}
}

// StatusError 表示网关返回了非 2xx 响应。
type StatusError struct {
	Status int
	Body   string // 截断后的响应体片段，便于诊断
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("网关返回 HTTP %d: %s", e.Status, e.Body)
}

// TimeoutError 传输层超时（区别于 SIGINT 中断，对应 exit 4）。
type TimeoutError struct{ Err error }

func (e *TimeoutError) Error() string { return "请求超时: " + e.Err.Error() }
func (e *TimeoutError) Unwrap() error { return e.Err }

// ChatCompletion 发起一次咨询。M0 语义：无重试、无 fallback（M1 引入）。
func (c *Client) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	var out ChatResponse
	if err := c.do(ctx, http.MethodPost, "/chat/completions", req, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("网关响应中没有 choices")
	}
	return &out, nil
}

// ListModels 拉取 /v1/models，返回排序后的模型 ID 列表。
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	var out ModelsResponse
	if err := c.do(ctx, http.MethodGet, "/models", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求失败: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// 归一传输层错误：超时可辨识（exit 4），其余网络错误 exit 5
		if isTimeout(err) {
			return &TimeoutError{Err: err}
		}
		return fmt.Errorf("网络错误: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{Status: resp.StatusCode, Body: truncate(string(data), 400)}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("解析网关响应失败: %w（响应片段: %s）", err, truncate(string(data), 200))
		}
	}
	return nil
}

func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
