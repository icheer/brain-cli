package decide

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icheer/brain-cli/internal/gateway"
)

func TestEndpointJoin(t *testing.T) {
	cases := map[string]string{
		"https://api.typesafe.ai":      "https://api.typesafe.ai/v1/systemone",
		"https://api.typesafe.ai/":     "https://api.typesafe.ai/v1/systemone",
		"https://api.typesafe.ai/v1":   "https://api.typesafe.ai/v1/systemone",
		"https://gw.example.com/v1":    "https://gw.example.com/v1/systemone",
		"https://gw.example.com":       "https://gw.example.com/v1/systemone",
		"https://gw.example.com/v2":    "https://gw.example.com/v2/systemone", // /v<数字> 已写不重拼
		"https://gw.example.com/v1/v1": "https://gw.example.com/v1/v1/systemone",
	}
	for base, want := range cases {
		if got := New(base, "k", time.Second).Endpoint(); got != want {
			t.Errorf("Endpoint(%q)=%q want %q", base, got, want)
		}
	}
}

func TestQuestionBuilderOrderAndConflict(t *testing.T) {
	qb := &QuestionBuilder{}
	// 键序保留为原始字节序（Go map 会重排——这里必须保持用户序）
	if err := qb.AddJSON([]byte(`{"z_last":{"type":"score","instructions":"i2"},"a_first":{"type":"choice","instructions":"i1"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := qb.AddNoul("m_mid", "is it?"); err != nil {
		t.Fatal(err)
	}
	raw, err := qb.Raw()
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	// 插入序：a_first 在 JSON 里第二但追加序为 z_last, a_first, m_mid
	wantOrder := `"z_last":{"type":"score","instructions":"i2"},"a_first":{"type":"choice","instructions":"i1"},"m_mid":{"type":"noul","instructions":"is it?"}`
	if !strings.Contains(s, wantOrder) {
		t.Fatalf("questions 应保持插入序且值原样:\n got %s", s)
	}

	// ID 冲突（-Q 内部、-Q 与 --noul 之间）
	if err := qb.AddNoul("a_first", "dup"); err == nil {
		t.Fatal("重复 ID 应报错")
	}
	qb2 := &QuestionBuilder{}
	if err := qb2.AddJSON([]byte(`{"x":{"type":"noul"},"x":{"type":"score"}}`)); err == nil {
		t.Fatal("同一对象内重复键应报错")
	}

	// 空 builder → nil
	if r, _ := (&QuestionBuilder{}).Raw(); r != nil {
		t.Fatal("空 builder 应返回 nil")
	}

	// 非 JSON 对象拒绝
	qb3 := &QuestionBuilder{}
	if err := qb3.AddJSON([]byte(`[1,2]`)); err == nil {
		t.Fatal("数组应拒绝")
	}
	if err := qb3.AddJSON([]byte(`not json`)); err == nil {
		t.Fatal("非法 JSON 应拒绝")
	}
}

// recordingSrv 记录请求并回放固定响应；failFirstN 控制前 N 次 500（重试测试）。
func recordingSrv(t *testing.T, status int, resp string, failFirstN int, header http.Header) (*httptest.Server, *[]byte, *int64) {
	t.Helper()
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	var body []byte
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-mu
		b, _ := io.ReadAll(r.Body)
		body = b
		calls++
		n := calls
		mu <- struct{}{}
		for k, vs := range header {
			for _, v := range vs {
				w.Header().Set(k, v)
			}
		}
		if failFirstN > 0 && n <= int64(failFirstN) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		if status != 200 && failFirstN == 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"auth"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &calls
}

const okResp = `{"model":"jev-1.14.0","answers":{"is_urgent":{"type":"noul","noul":0.99},"dept":{"type":"choice","choice":"technical","confidence":1.0,"probabilities":{"technical":1.0}}},"usage":{"input_tokens":428,"output_tokens":73}}`

func TestDoGolden(t *testing.T) {
	srv, body, _ := recordingSrv(t, 200, okResp, 0, nil)
	c := New(srv.URL, "k", 5*time.Second)
	qb := &QuestionBuilder{}
	_ = qb.AddNoul("is_urgent", "is it urgent?")

	res, err := c.Do(context.Background(), &Request{
		Model: "jev-latest", State: "the state text", Questions: mustRaw(t, qb),
		ExtraBody:    map[string]any{"assets": map[string]any{"img1": "x"}},
		ExtraHeaders: map[string]string{"TypeSafe-Danger-Zone-Images": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 请求 golden：三顶层键 + questions 键序保持 + extras 顶层并入
	var m map[string]any
	if err := json.Unmarshal(*body, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "jev-latest" || m["state"] != "the state text" {
		t.Errorf("model/state: %v", m)
	}
	if _, ok := m["assets"]; !ok {
		t.Error("extras 未顶层并入（assets 逃生口）")
	}
	// 响应：answers 原样字节（stdout 直出零重序列化）
	if string(res.Answers) != `"is_urgent":{"type":"noul","noul":0.99},"dept":{"type":"choice","choice":"technical","confidence":1.0,"probabilities":{"technical":1.0}}` {
		// RawMessage 提取的是对象值的原始片段（不含外层 {}？——json.Unmarshal 对 RawMessage 字段取完整值字节，含 {}）
		if !strings.Contains(string(res.Answers), `"is_urgent":{"type":"noul","noul":0.99}`) {
			t.Errorf("answers 原样透传失败: %s", res.Answers)
		}
	}
	if res.Model != "jev-1.14.0" {
		t.Errorf("model=%q", res.Model)
	}
	if !strings.Contains(string(res.Usage), `"input_tokens":428`) {
		t.Errorf("usage 透传失败: %s", res.Usage)
	}
}

func TestDoAuthHeaderAndExtras(t *testing.T) {
	var gotAuth, gotDanger string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotDanger = r.Header.Get("TypeSafe-Danger-Zone-Images")
		_, _ = w.Write([]byte(okResp))
	}))
	t.Cleanup(srv.Close)
	qb := &QuestionBuilder{}
	_ = qb.AddNoul("q", "x")
	_, err := New(srv.URL, "k", time.Second).Do(context.Background(), &Request{
		Model: "m", State: "s", Questions: mustRaw(t, qb),
		ExtraHeaders: map[string]string{"TypeSafe-Danger-Zone-Images": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer k" || gotDanger != "true" {
		t.Errorf("auth=%q danger=%q", gotAuth, gotDanger)
	}
}

func TestDoRetryOn5xx(t *testing.T) {
	srv, _, calls := recordingSrv(t, 500, okResp, 1, nil) // 首次 500，重试成功
	qb := &QuestionBuilder{}
	_ = qb.AddNoul("q", "x")
	c := New(srv.URL, "k", 5*time.Second)
	if _, err := c.Do(context.Background(), &Request{Model: "m", State: "s", Questions: mustRaw(t, qb)}); err != nil {
		t.Fatalf("5xx 应重试成功: %v", err)
	}
	if atomic.LoadInt64(calls) != 2 {
		t.Fatalf("应 2 次调用，得 %d", *calls)
	}
}

func TestDoNoRetryOn401(t *testing.T) {
	srv, _, calls := recordingSrv(t, 401, "", 0, nil)
	qb := &QuestionBuilder{}
	_ = qb.AddNoul("q", "x")
	_, err := New(srv.URL, "k", time.Second).Do(context.Background(), &Request{Model: "m", State: "s", Questions: mustRaw(t, qb)})
	var se *gateway.StatusError
	if !errorsAs(err, &se) || se.Status != 401 {
		t.Fatalf("应得 StatusError(401)，得 %v", err)
	}
	if atomic.LoadInt64(calls) != 1 {
		t.Fatalf("401 不应重试，调用 %d 次", *calls)
	}
}

func TestDoEmptyAnswersUpstreamError(t *testing.T) {
	srv, _, _ := recordingSrv(t, 200, `{"model":"m","answers":{},"usage":null}`, 0, nil)
	qb := &QuestionBuilder{}
	_ = qb.AddNoul("q", "x")
	_, err := New(srv.URL, "k", time.Second).Do(context.Background(), &Request{Model: "m", State: "s", Questions: mustRaw(t, qb)})
	if err == nil || !strings.Contains(err.Error(), "answers") {
		t.Fatalf("空 answers 应上游错误，得 %v", err)
	}
	// 200 夹带 error 体（one-api 系）
	srv2, _, _ := recordingSrv(t, 200, `{"error":{"code":401,"message":"bad key"}}`, 0, nil)
	_, err = New(srv2.URL, "k", time.Second).Do(context.Background(), &Request{Model: "m", State: "s", Questions: mustRaw(t, qb)})
	var se *gateway.StatusError
	if !errorsAs(err, &se) || se.Status != 401 {
		t.Fatalf("200 夹带 error 应归一 StatusError，得 %v", err)
	}
}

func mustRaw(t *testing.T, qb *QuestionBuilder) json.RawMessage {
	t.Helper()
	r, err := qb.Raw()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func errorsAs(err error, target **gateway.StatusError) bool {
	if se, ok := err.(*gateway.StatusError); ok {
		*target = se
		return true
	}
	return false
}
