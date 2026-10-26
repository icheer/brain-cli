package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// captureGateway 记录每个请求的完整 body 与选中的请求头（供 extra 透传断言）。
func captureGateway(t *testing.T) (*httptest.Server, *[]map[string]any, *map[string]string) {
	t.Helper()
	var mu sync.Mutex
	bodies := []map[string]any{}
	hdrs := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		hdrs["User-Agent"] = r.Header.Get("User-Agent")
		hdrs["X-Tenant"] = r.Header.Get("X-Tenant")
		hdrs["Content-Type"] = r.Header.Get("Content-Type")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		emit := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "ok"}}}})
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
		emit(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies, &hdrs
}

// 透传生效：temperature 顶层 assign 进请求体，brain 自有字段不受影响。
func TestExtraBodyPassthrough(t *testing.T) {
	srv, bodies, _ := captureGateway(t)
	setupConfig(t, srv.URL+"/v1")

	code, _, errStr := runBrain("ask", "-m", "fast", "--extra-body", `{"temperature":0.3}`, "问")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if len(*bodies) != 1 {
		t.Fatalf("应恰好一个请求: %d", len(*bodies))
	}
	b := (*bodies)[0]
	if b["temperature"] != 0.3 {
		t.Fatalf("temperature 应透传: %v", b["temperature"])
	}
	if b["model"] != "m-a" || b["stream"] != true {
		t.Fatalf("brain 自有字段应完好: model=%v stream=%v", b["model"], b["stream"])
	}
}

// @file 取值与多值有序合并（后者同名键覆盖）。
func TestExtraBodyFileAndMerge(t *testing.T) {
	srv, bodies, _ := captureGateway(t)
	setupConfig(t, srv.URL+"/v1")
	dir := t.TempDir()
	f := filepath.Join(dir, "p.json")
	if err := os.WriteFile(f, []byte(`{"top_k":50,"seed":7}`), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, _ := runBrain("ask", "-m", "fast",
		"--extra-body", "@"+f, "--extra-body", `{"seed":9}`, "问")
	if code != 0 {
		t.Fatal("code != 0")
	}
	b := (*bodies)[0]
	if b["top_k"] != float64(50) || b["seed"] != float64(9) {
		t.Fatalf("文件+内联合并（后者覆盖）不符: %v", b)
	}
}

// 保留字拒绝：body 生命线字段与专属 flag 字段；headers 鉴权字段。
func TestExtraReservedRejected(t *testing.T) {
	srv, bodies, _ := captureGateway(t)
	setupConfig(t, srv.URL+"/v1")

	for _, arg := range []string{
		`{"stream":false}`, `{"messages":[]}`, `{"model":"evil"}`,
		`{"reasoning_effort":"high"}`, `{"max_tokens":100}`,
	} {
		code, _, errStr := runBrain("ask", "-m", "fast", "--extra-body", arg, "问")
		if code != 1 {
			t.Fatalf("保留字 %s 应 exit 1, got %d", arg, code)
		}
		if len(errStr) == 0 {
			t.Fatalf("应有报错信息")
		}
	}
	code, _, _ := runBrain("ask", "-m", "fast", "--extra-headers", `{"Authorization":"Bearer x"}`, "问")
	if code != 1 {
		t.Fatalf("Authorization 保留应 exit 1, got %d", code)
	}
	if len(*bodies) != 0 {
		t.Fatalf("全部保留字请求都不应发出: %d", len(*bodies))
	}
}

// headers：默认头之后应用——自定义头透传、UA 可覆盖、Content-Type 不受影响。
func TestExtraHeadersApplied(t *testing.T) {
	srv, _, hdrs := captureGateway(t)
	setupConfig(t, srv.URL+"/v1")

	code, _, _ := runBrain("ask", "-m", "fast",
		"--extra-headers", `{"X-Tenant":"team-a","User-Agent":"probe/1.0"}`, "问")
	if code != 0 {
		t.Fatal("code != 0")
	}
	if (*hdrs)["X-Tenant"] != "team-a" || (*hdrs)["User-Agent"] != "probe/1.0" {
		t.Fatalf("extra headers 应生效: %v", *hdrs)
	}
	if (*hdrs)["Content-Type"] != "application/json" {
		t.Fatalf("Content-Type 不应被破坏: %v", *hdrs)
	}
}

// dry-run 可见最终合并结果（护栏 3）。
func TestExtraDryRunVisibility(t *testing.T) {
	srv, _, _ := captureGateway(t)
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("ask", "-m", "fast", "--dry-run",
		"--extra-body", `{"temperature":0.3}`, "--extra-headers", `{"X-Tenant":"t"}`, "问")
	if code != 0 {
		t.Fatal("code != 0")
	}
	var dr struct {
		ExtraBody    map[string]any    `json:"extra_body"`
		ExtraHeaders map[string]string `json:"extra_headers"`
	}
	if err := json.Unmarshal([]byte(out), &dr); err != nil {
		t.Fatal(err)
	}
	if dr.ExtraBody["temperature"] != 0.3 || dr.ExtraHeaders["X-Tenant"] != "t" {
		t.Fatalf("dry-run 应展示 extras: %s", out)
	}
}

// compare 全腿共用同一份 extras。
func TestExtraCompareAllLegs(t *testing.T) {
	srv, bodies, _ := captureGateway(t)
	setupConfig(t, srv.URL+"/v1")

	code, _, _ := runBrain("compare", "-m", "fast", "--extra-body", `{"temperature":0.1}`, "问")
	if code != 0 {
		t.Fatal("code != 0")
	}
	if len(*bodies) != 2 {
		t.Fatalf("两条腿各一请求: %d", len(*bodies))
	}
	for _, b := range *bodies {
		if b["temperature"] != 0.1 {
			t.Fatalf("每条腿都应携带 extras: %v", b)
		}
	}
}

// 非对象 JSON 与非字符串头值均为用法错误。
func TestExtraMalformed(t *testing.T) {
	srv, bodies, _ := captureGateway(t)
	setupConfig(t, srv.URL+"/v1")

	if code, _, _ := runBrain("ask", "-m", "fast", "--extra-body", `[1,2]`, "问"); code != 1 {
		t.Fatalf("数组应 exit 1, got %d", code)
	}
	if code, _, _ := runBrain("ask", "-m", "fast", "--extra-body", `not json`, "问"); code != 1 {
		t.Fatalf("非法 JSON 应 exit 1, got %d", code)
	}
	if code, _, _ := runBrain("ask", "-m", "fast", "--extra-headers", `{"X-N":3}`, "问"); code != 1 {
		t.Fatalf("头值非字符串应 exit 1, got %d", code)
	}
	if len(*bodies) != 0 {
		t.Fatalf("不应发出请求: %d", len(*bodies))
	}
}
