package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeCompareGateway 按请求的 model 返回 "reply:<model>" 的 SSE 应答；
// statusFn(model) != 200 时该模型腿回错误。记录收到的模型序列供展开/去重断言。
func fakeCompareGateway(t *testing.T, statusFn func(model string) int) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		mu.Lock()
		seen = append(seen, model)
		mu.Unlock()

		status := http.StatusOK
		if statusFn != nil {
			status = statusFn(model)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		emit := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "reply:" + model}}}})
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
		emit(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// sortedSeen 拷贝并排序已见模型（腿并发，到达序不定，断言按集合比较）。
func sortedSeen(t *testing.T, seen *[]string) []string {
	t.Helper()
	got := append([]string{}, (*seen)...)
	sort.Strings(got)
	return got
}

// golden：别名 fast（[m-a, m-b]）展开为两条腿，stdout 逐字节等于分隔头拼接。
func TestCompareStdoutSections(t *testing.T) {
	srv, _ := fakeCompareGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("compare", "-m", "fast", "你怎么看")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	want := "===== model: m-a =====\nreply:m-a\n\n===== model: m-b =====\nreply:m-b\n"
	if out != want {
		t.Fatalf("stdout 逐字节不符:\n got %q\nwant %q", out, want)
	}
	if !strings.Contains(errStr, "2/2") {
		t.Fatalf("stderr 汇总行应含 2/2: %q", errStr)
	}
}

// 字面 ID、逗号分隔与别名混用；跨来源去重（m-b,fast → m-b + fast[m-a,m-b] 去重后 2 个）。
func TestCompareExpansionDedupe(t *testing.T) {
	srv, seen := fakeCompareGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, _, errStr := runBrain("compare", "-m", " m-b,fast ", "问")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if got := sortedSeen(t, seen); len(got) != 2 || got[0] != "m-a" || got[1] != "m-b" {
		t.Fatalf("展开去重后应恰好请求 m-a 与 m-b: %v", got)
	}
}

func TestCompareNeedsTwoModels(t *testing.T) {
	srv, seen := fakeCompareGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, _, errStr := runBrain("compare", "-m", "m-a", "问")
	if code != 1 {
		t.Fatalf("展开后单模型应为用法错误 exit 1, got %d", code)
	}
	if !strings.Contains(errStr, "≥2") {
		t.Fatalf("stderr 应提示需要 ≥2 个模型: %q", errStr)
	}
	if len(*seen) != 0 {
		t.Fatal("不应发起任何请求")
	}
}

// 部分失败：一腿 500（retry_max: 0），成功腿照常交付，exit 0（ping -m 哲学）。
func TestComparePartialFailure(t *testing.T) {
	srv, _ := fakeCompareGateway(t, func(m string) int {
		if m == "m-b" {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("compare", "-m", "fast", "问")
	if code != 0 {
		t.Fatalf("≥1 腿成功应 exit 0, got %d stderr=%s", code, errStr)
	}
	if out != "===== model: m-a =====\nreply:m-a\n" {
		t.Fatalf("stdout 只应有成功腿: %q", out)
	}
	if !strings.Contains(errStr, "m-b") || !strings.Contains(errStr, "code 5") {
		t.Fatalf("stderr 应明示失败腿与码: %q", errStr)
	}
}

// 全部腿 401：exit 2（最可行动码），--json 恒定单对象，error.code 与退出码一致。
func TestCompareAllFailAuth(t *testing.T) {
	srv, _ := fakeCompareGateway(t, func(m string) int { return http.StatusUnauthorized })
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("compare", "-m", "fast", "--json", "问")
	if code != 2 {
		t.Fatalf("全失败 401 应 exit 2, got %d", code)
	}
	var jr struct {
		Responses []struct {
			Model string `json:"model"`
			OK    bool   `json:"ok"`
		} `json:"responses"`
		Error *struct {
			Code int    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("stdout 非单对象 JSON: %v\n%s", err, out)
	}
	if jr.Error == nil || jr.Error.Code != 2 || jr.Error.Type != "auth" {
		t.Fatalf("error 应为 code=2 type=auth: %+v", jr.Error)
	}
	if len(jr.Responses) != 2 || jr.Responses[0].OK || jr.Responses[1].OK {
		t.Fatalf("两条腿都应 ok=false: %+v", jr.Responses)
	}
}

func TestCompareJSONAndBlind(t *testing.T) {
	srv, _ := fakeCompareGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("compare", "-m", "fast", "--json", "--blind", "问")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var jr struct {
		Responses []struct {
			Model   string `json:"model"`
			OK      bool   `json:"ok"`
			Content string `json:"content"`
		} `json:"responses"`
	}
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("解析 --json 失败: %v", err)
	}
	if len(jr.Responses) != 2 || !jr.Responses[0].OK || !jr.Responses[1].OK {
		t.Fatalf("两条腿都应成功: %+v", jr.Responses)
	}
	if jr.Responses[0].Model != "Model_A" || jr.Responses[1].Model != "Model_B" {
		t.Fatalf("--blind 应匿名为 Model_A/B: %+v", jr.Responses)
	}
	if jr.Responses[0].Content != "reply:m-a" {
		t.Fatalf("内容应保留: %+v", jr.Responses[0])
	}
	if !strings.Contains(errStr, "Model_A=m-a") || !strings.Contains(errStr, "Model_B=m-b") {
		t.Fatalf("stderr 应有盲测映射: %q", errStr)
	}
}

func TestCompareDryRun(t *testing.T) {
	srv, seen := fakeCompareGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("compare", "-m", "fast", "--dry-run", "问")
	if code != 0 {
		t.Fatal("code != 0")
	}
	if len(*seen) != 0 {
		t.Fatal("dry-run 不应发起请求")
	}
	var dr struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &dr); err != nil {
		t.Fatalf("dry-run 输出应为 JSON: %v", err)
	}
	if len(dr.Models) != 2 || dr.Models[0] != "m-a" || dr.Models[1] != "m-b" {
		t.Fatalf("models 应展开为 [m-a m-b]: %v", dr.Models)
	}
}

func TestCompareMutualExclusion(t *testing.T) {
	srv, _ := fakeCompareGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, _, errStr := runBrain("compare", "-m", "fast", "--dry-run", "--json", "问")
	if code != 1 || !strings.Contains(errStr, "互斥") {
		t.Fatalf("--dry-run 与 --json 应互斥: code=%d stderr=%q", code, errStr)
	}
}
