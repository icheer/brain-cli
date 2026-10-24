package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// emitSSE 向响应写一个 SSE data 事件并立即 flush。
func emitSSE(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

// sseChat 返回一段标准成功流：thinking + 两个 content 增量 + finish + usage + DONE。
func sseChat(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": keep-alive\n\n")
	emitSSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "思考"}}}})
	emitSSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "你"}}}})
	emitSSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "好"}}}})
	emitSSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
	emitSSE(w, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestChatCompletionStreamSuccess(t *testing.T) {
	var gotAuth, gotModel string
	var streamFlag any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析请求: %v", err)
		}
		gotModel, _ = req["model"].(string)
		streamFlag = req["stream"]
		sseChat(w)
	}))
	defer srv.Close()

	var contentChars, reasoningChars int
	c := New(srv.URL, "test-key", 5*time.Second)
	res, err := c.ChatCompletionStream(context.Background(),
		ChatRequest{Model: "m-a", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(content, reasoning string) {
			contentChars += len(content)
			reasoningChars += len(reasoning)
		})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization: %q", gotAuth)
	}
	if gotModel != "m-a" || streamFlag != true {
		t.Fatalf("请求体不符: model=%q stream=%v", gotModel, streamFlag)
	}
	if res.Content != "你好" || res.Thinking != "思考" || res.FinishReason != "stop" {
		t.Fatalf("累积结果不符: %+v", res)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 7 {
		t.Fatalf("usage: %+v", res.Usage)
	}
	if contentChars != len("你好") || reasoningChars != len("思考") {
		t.Fatalf("增量回调计数: %d/%d", contentChars, reasoningChars)
	}
}

func TestStreamStatusError(t *testing.T) {
	// 401 无 Retry-After
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	c := New(srv.URL, "k", 5*time.Second)
	_, err := c.ChatCompletionStream(context.Background(), ChatRequest{Model: "m"}, nil)
	srv.Close()
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 401 || se.RetryAfter != 0 {
		t.Fatalf("401 应得 StatusError: %v", err)
	}

	// 429 带 Retry-After: 2
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer srv2.Close()
	c2 := New(srv2.URL, "k", 5*time.Second)
	_, err = c2.ChatCompletionStream(context.Background(), ChatRequest{Model: "m"}, nil)
	if !errors.As(err, &se) || se.Status != 429 || se.RetryAfter != 2*time.Second {
		t.Fatalf("429 应携带 Retry-After=2s: %v", err)
	}
}

func TestStreamMidTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		emitSSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "半"}}}})
		time.Sleep(1 * time.Second)
		emitSSE(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "截"}}}})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c := New(srv.URL, "k", 5*time.Second)
	_, err := c.ChatCompletionStream(ctx, ChatRequest{Model: "m"}, nil)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("中途超时应得 TimeoutError，得到 %v", err)
	}
}

func TestStreamEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(srv.URL, "k", 5*time.Second)
	if _, err := c.ChatCompletionStream(context.Background(), ChatRequest{Model: "m"}, nil); err == nil {
		t.Fatal("空流应报错")
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"c"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", 5*time.Second)
	ids, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[2] != "c" {
		t.Fatalf("应排序返回，得到 %v", ids)
	}
}

// 真实网关行为：HTTP 200 但错误藏在 SSE data 事件里，应转为 StatusError 参与分类。
func TestStreamErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"Requested entity was not found.\",\"type\":\"upstream_api_error\",\"code\":404}}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":22,\"completion_tokens\":0,\"total_tokens\":22}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New(srv.URL, "k", 5*time.Second)
	_, err := c.ChatCompletionStream(context.Background(), ChatRequest{Model: "m"}, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 || !strings.Contains(se.Body, "not found") {
		t.Fatalf("流内 error 事件应映射 404 StatusError，得到 %v", err)
	}
}
