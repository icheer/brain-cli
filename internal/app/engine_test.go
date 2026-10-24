package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icheer/brain-cli/internal/gateway"
)

// sseOK 写一段最小成功流。
func sseOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmtSSE(w, `{"choices":[{"delta":{"content":"OK"}}]}`)
	fmtSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	fmtSSE(w, "data: [DONE]\n\n")
}

func fmtSSE(w http.ResponseWriter, payload string) {
	if !strings.HasPrefix(payload, "data:") {
		payload = "data: " + payload
	}
	_, _ = w.Write([]byte(payload + "\n\n"))
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

// decodeModel 从请求体读出 model 字段。
func decodeModel(t *testing.T, r *http.Request) string {
	t.Helper()
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("解析请求: %v", err)
		return ""
	}
	m, _ := req["model"].(string)
	return m
}

func TestEngineFallbackAfterRetries(t *testing.T) {
	var primaryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if decodeModel(t, r) == "p" {
			primaryCalls.Add(1)
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		sseOK(w)
	}))
	defer srv.Close()

	var stderr strings.Builder
	e := &AskEngine{Client: gateway.New(srv.URL, "k", 5*time.Second), Stderr: &stderr, RetryMax: 1}
	res, used, err := e.Ask(context.Background(), []string{"p", "s"},
		func(m string) gateway.ChatRequest { return gateway.ChatRequest{Model: m} }, nil)
	if err != nil || used != "s" || res.Content != "OK" {
		t.Fatalf("err=%v used=%q res=%+v", err, used, res)
	}
	if primaryCalls.Load() != 2 { // 首次 + 1 次重试
		t.Fatalf("主模型应被调用 2 次，实际 %d", primaryCalls.Load())
	}
	if !strings.Contains(stderr.String(), "重试") || !strings.Contains(stderr.String(), "故障转移") {
		t.Fatalf("stderr 应含重试与故障转移提示: %q", stderr.String())
	}
}

func TestEngine429RetryAfterThenOK(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		sseOK(w)
	}))
	defer srv.Close()

	start := time.Now()
	e := &AskEngine{Client: gateway.New(srv.URL, "k", 5*time.Second), Stderr: &strings.Builder{}, RetryMax: 2}
	res, used, err := e.Ask(context.Background(), []string{"p"},
		func(m string) gateway.ChatRequest { return gateway.ChatRequest{Model: m} }, nil)
	if err != nil || used != "p" || res.Content != "OK" {
		t.Fatalf("err=%v used=%q", err, used)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("应尊重 Retry-After 等待 ≥1s，实际 %v", elapsed)
	}
}

func TestEngine404SkipsToNextModel(t *testing.T) {
	var primaryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if decodeModel(t, r) == "p" {
			primaryCalls.Add(1)
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found","type":"model_not_found"}}`))
			return
		}
		sseOK(w)
	}))
	defer srv.Close()

	e := &AskEngine{Client: gateway.New(srv.URL, "k", 5*time.Second), Stderr: &strings.Builder{}, RetryMax: 5}
	res, used, err := e.Ask(context.Background(), []string{"p", "s"},
		func(m string) gateway.ChatRequest { return gateway.ChatRequest{Model: m} }, nil)
	if err != nil || used != "s" || res.Content != "OK" {
		t.Fatalf("err=%v used=%q", err, used)
	}
	if primaryCalls.Load() != 1 { // 404 不重试，直接换模型
		t.Fatalf("404 应立即换模型（主模型仅调用 1 次），实际 %d", primaryCalls.Load())
	}
}

// 真实网关行为：模型不存在以 503 + code=model_not_found 出现，应立即换模型而非按 5xx 重试。
func TestEngine503ModelNotFoundSkips(t *testing.T) {
	var primaryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if decodeModel(t, r) == "p" {
			primaryCalls.Add(1)
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":"model_not_found","message":"No available account"}}`))
			return
		}
		sseOK(w)
	}))
	defer srv.Close()

	start := time.Now()
	e := &AskEngine{Client: gateway.New(srv.URL, "k", 5*time.Second), Stderr: &strings.Builder{}, RetryMax: 5}
	res, used, err := e.Ask(context.Background(), []string{"p", "s"},
		func(m string) gateway.ChatRequest { return gateway.ChatRequest{Model: m} }, nil)
	if err != nil || used != "s" || res.Content != "OK" {
		t.Fatalf("err=%v used=%q", err, used)
	}
	if primaryCalls.Load() != 1 {
		t.Fatalf("model_not_found 应立即换模型（主模型仅 1 次），实际 %d", primaryCalls.Load())
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("不应有退避等待，实际 %v", time.Since(start))
	}
}

func TestEngine401Fatal(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	e := &AskEngine{Client: gateway.New(srv.URL, "k", 5*time.Second), Stderr: &strings.Builder{}, RetryMax: 5}
	_, _, err := e.Ask(context.Background(), []string{"p", "s"},
		func(m string) gateway.ChatRequest { return gateway.ChatRequest{Model: m} }, nil)
	var se *gateway.StatusError
	if !errors.As(err, &se) || se.Status != 401 {
		t.Fatalf("401 应直接终止: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("401 不应换模型（仅 1 次调用），实际 %d", calls.Load())
	}
}

func TestEngineTimeoutSwitchesModel(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(400 * time.Millisecond)
		sseOK(w)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	e := &AskEngine{Client: gateway.New(srv.URL, "k", 1*time.Second), Stderr: &strings.Builder{}, RetryMax: 5}
	_, _, err := e.Ask(ctx, []string{"p", "s"},
		func(m string) gateway.ChatRequest { return gateway.ChatRequest{Model: m} }, nil)
	var te *gateway.TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("预算耗尽应得 TimeoutError，得到 %v", err)
	}
	if calls.Load() < 1 {
		t.Fatalf("至少应发起过请求: %d", calls.Load())
	}
}

func TestEngineBackoffRespectsDeadline(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	// 预算 500ms、500 错误退避 1s：不够等 → 立即按超时收场，不睡满 1s
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	e := &AskEngine{Client: gateway.New(srv.URL, "k", 1*time.Second), Stderr: &strings.Builder{}, RetryMax: 2}
	_, _, err := e.Ask(ctx, []string{"p"},
		func(m string) gateway.ChatRequest { return gateway.ChatRequest{Model: m} }, nil)
	var te *gateway.TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("应得 TimeoutError，得到 %v", err)
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Fatalf("退避等待不应超出预算，实际 %v", time.Since(start))
	}
}
