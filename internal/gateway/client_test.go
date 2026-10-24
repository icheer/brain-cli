package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatCompletionSuccess(t *testing.T) {
	var gotAuth, gotModel string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("路径不符: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("解析请求失败: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key", 5*time.Second)
	resp, err := c.ChatCompletion(context.Background(), ChatRequest{
		Model:    "m-a",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Stream:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization: %q", gotAuth)
	}
	if gotModel, _ = gotBody["model"].(string); gotModel != "m-a" {
		t.Fatalf("model: %q", gotModel)
	}
	if gotBody["stream"] != false {
		t.Fatalf("stream 应为 false: %v", gotBody["stream"])
	}
	if resp.Choices[0].Message.Content != "你好" || resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("响应不符: %+v", resp.Choices[0])
	}
}

func TestStatusErrors(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
		}))
		c := New(srv.URL, "k", 5*time.Second)
		_, err := c.ChatCompletion(context.Background(), ChatRequest{Model: "m"})
		srv.Close()

		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("status %d: 应得 StatusError，得到 %v", status, err)
		}
		if se.Status != status {
			t.Fatalf("status %d: 得到 %d", status, se.Status)
		}
		if se.Body == "" {
			t.Fatalf("status %d: Body 为空", status)
		}
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", 50*time.Millisecond)
	_, err := c.ChatCompletion(context.Background(), ChatRequest{Model: "m"})
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("应得 TimeoutError，得到 %v", err)
	}
}

func TestNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", 5*time.Second)
	_, err := c.ChatCompletion(context.Background(), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("空 choices 应报错")
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
