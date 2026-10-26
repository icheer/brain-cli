package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// councilReq 记录一次发言调用的模型与完整请求体（供轮次/发言记录断言）。
type councilReq struct {
	Model string
	Body  string
}

// fakeCouncilGateway 按请求回 "reply:<model>" 的 SSE 应答；failFn(model, body) != 200 时回错。
// body 是原始请求 JSON：议事模板的「这是第 N/M 轮」标记可用于判断轮次（测试环境钉 zh）。
func fakeCouncilGateway(t *testing.T, failFn func(model, body string) int) (*httptest.Server, *[]councilReq) {
	t.Helper()
	var mu sync.Mutex
	seen := []councilReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		model, _ := parsed["model"].(string)
		mu.Lock()
		seen = append(seen, councilReq{Model: model, Body: string(body)})
		mu.Unlock()

		status := http.StatusOK
		if failFn != nil {
			status = failFn(model, string(body))
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

// golden：C=2 全成功——第 1 轮并行盲评（解析序），第 2 轮蛇形（m-b 先），
// stdout 按发言时间序逐字节等于分隔头拼接；首轮请求互不可见，末轮为收尾指令。
func TestCouncilStdoutSections(t *testing.T) {
	srv, seen := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("council", "-m", "fast", "你怎么看")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	want := "===== model: m-a | round 1 =====\nreply:m-a\n\n" +
		"===== model: m-b | round 1 =====\nreply:m-b\n\n" +
		"===== model: m-b | round 2 =====\nreply:m-b\n\n" +
		"===== model: m-a | round 2 =====\nreply:m-a\n"
	if out != want {
		t.Fatalf("stdout 逐字节不符:\n got %q\nwant %q", out, want)
	}
	if len(*seen) != 4 {
		t.Fatalf("应 4 次调用（2 席位×2 轮）: %d", len(*seen))
	}
	if !strings.Contains(errStr, "4/4") {
		t.Fatalf("stderr 汇总应含 4/4: %q", errStr)
	}
	round2 := 0
	for _, req := range *seen {
		if strings.Contains(req.Body, "这是第 1/") { // 第 1 轮指令标记
			if strings.Contains(req.Body, "----- 席位") {
				t.Fatal("第 1 轮请求不应携带发言记录（盲评）")
			}
			continue
		}
		round2++
		if !strings.Contains(req.Body, "这是第 2/2 轮") {
			t.Fatal("应识别为第 2 轮指令")
		}
		if !strings.Contains(req.Body, "----- 席位 m-a（第 1 轮）-----") ||
			!strings.Contains(req.Body, "----- 席位 m-b（第 1 轮）-----") {
			t.Fatalf("第 2 轮请求应携带完整第 1 轮发言记录: %s", req.Body)
		}
		if !strings.Contains(req.Body, "最后一轮") {
			t.Fatal("C=2 的第 2 轮应为收尾轮指令")
		}
	}
	if round2 != 2 {
		t.Fatalf("第 2 轮应有 2 次调用: %d", round2)
	}
}

// C=3：中段轮为「只输出增量」指令、末轮为收尾；发言记录按时间序追加（前缀缓存形态）。
func TestCouncilThreeRounds(t *testing.T) {
	srv, seen := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("council", "-m", "fast", "-C", "3", "问")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	want := "===== model: m-a | round 1 =====\nreply:m-a\n\n" +
		"===== model: m-b | round 1 =====\nreply:m-b\n\n" +
		"===== model: m-b | round 2 =====\nreply:m-b\n\n" +
		"===== model: m-a | round 2 =====\nreply:m-a\n\n" +
		"===== model: m-a | round 3 =====\nreply:m-a\n\n" +
		"===== model: m-b | round 3 =====\nreply:m-b\n"
	if out != want {
		t.Fatalf("stdout 逐字节不符:\n got %q\nwant %q", out, want)
	}
	if len(*seen) != 6 {
		t.Fatalf("应 6 次调用: %d", len(*seen))
	}
	for _, req := range *seen {
		switch {
		case strings.Contains(req.Body, "这是第 2/3 轮"):
			if !strings.Contains(req.Body, "不要复述已有共识") {
				t.Fatal("中段轮应为增量发言指令")
			}
		case strings.Contains(req.Body, "这是第 3/3 轮"):
			if !strings.Contains(req.Body, "最后一轮") {
				t.Fatal("第 3 轮应为收尾指令")
			}
		}
	}
	// 时间序追加：m-a 第 3 轮请求中，第 1 轮条目先于第 2 轮条目、
	// 第 2 轮内 m-b（蛇形先行）先于 m-a。
	var aR3 string
	for _, req := range *seen {
		if req.Model == "m-a" && strings.Contains(req.Body, "这是第 3/3 轮") {
			aR3 = req.Body
		}
	}
	if aR3 == "" {
		t.Fatal("未找到 m-a 第 3 轮请求")
	}
	iB2 := strings.Index(aR3, "----- 席位 m-b（第 2 轮）-----")
	iA2 := strings.Index(aR3, "----- 席位 m-a（第 2 轮）-----")
	iA1 := strings.Index(aR3, "----- 席位 m-a（第 1 轮）-----")
	if iA1 < 0 || iB2 < 0 || iA2 < 0 || !(iA1 < iB2 && iB2 < iA2) {
		t.Fatalf("发言记录应按时间序追加: iA1=%d iB2=%d iA2=%d", iA1, iB2, iA2)
	}
}

func TestCouncilValidation(t *testing.T) {
	srv, seen := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	if code, _, errStr := runBrain("council", "-m", "fast", "--dry-run", "--json", "问"); code != 1 || !strings.Contains(errStr, "互斥") {
		t.Fatalf("--dry-run 与 --json 应互斥: code=%d stderr=%q", code, errStr)
	}
	councilOpts = councilOptions{rounds: 2} // rounds 有非零默认值，重置须保留
	if code, _, errStr := runBrain("council", "-m", "fast", "-C", "0", "问"); code != 1 || !strings.Contains(errStr, "≥1") {
		t.Fatalf("-C 0 应 exit 1: code=%d stderr=%q", code, errStr)
	}
	councilOpts = councilOptions{rounds: 2} // rounds 有非零默认值，重置须保留
	if code, _, errStr := runBrain("council", "-m", "m-a", "问"); code != 1 || !strings.Contains(errStr, "≥2") {
		t.Fatalf("单席位应 exit 1: code=%d stderr=%q", code, errStr)
	}
	if len(*seen) != 0 {
		t.Fatal("校验失败不应发起请求")
	}
}

// 席位整场失败：第 1 轮缺席 → 有效席位 <2 提前终止，已积累发言照常输出 exit 0；
// --json status=partial。
func TestCouncilDropoutEarlyTerm(t *testing.T) {
	srv, _ := fakeCouncilGateway(t, func(model, body string) int {
		if model == "m-b" {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("council", "-m", "fast", "问")
	if code != 0 {
		t.Fatalf("有成功发言应 exit 0, got %d stderr=%s", code, errStr)
	}
	if out != "===== model: m-a | round 1 =====\nreply:m-a\n" {
		t.Fatalf("stdout 只应有 m-a 第 1 轮: %q", out)
	}
	for _, want := range []string{"m-b", "code 5", "提前结束"} {
		if !strings.Contains(errStr, want) {
			t.Fatalf("stderr 应含 %q: %q", want, errStr)
		}
	}

	councilOpts = councilOptions{rounds: 2} // rounds 有非零默认值，重置须保留
	code, out, _ = runBrain("council", "-m", "fast", "--json", "问")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var jr struct {
		Status          string `json:"status"`
		RoundsCompleted int    `json:"rounds_completed"`
		Transcript      []struct {
			Round   int    `json:"round"`
			Speaker string `json:"speaker"`
			OK      bool   `json:"ok"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, out)
	}
	if jr.Status != "partial" || jr.RoundsCompleted != 1 {
		t.Fatalf("status=%s rounds_completed=%d", jr.Status, jr.RoundsCompleted)
	}
	if len(jr.Transcript) != 2 || !jr.Transcript[0].OK || jr.Transcript[1].OK {
		t.Fatalf("transcript 应 1 成功 1 失败: %+v", jr.Transcript)
	}
}

// 席位中途（第 2 轮）失败：缺席标记进入发言记录，后续发言者可见；议会照常完成。
func TestCouncilRound2Dropout(t *testing.T) {
	srv, seen := fakeCouncilGateway(t, func(model, body string) int {
		if model == "m-b" && strings.Contains(body, "这是第 2/") {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("council", "-m", "fast", "问")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	want := "===== model: m-a | round 1 =====\nreply:m-a\n\n" +
		"===== model: m-b | round 1 =====\nreply:m-b\n\n" +
		"===== model: m-a | round 2 =====\nreply:m-a\n"
	if out != want {
		t.Fatalf("stdout 逐字节不符:\n got %q\nwant %q", out, want)
	}
	if !strings.Contains(errStr, "缺席退出") {
		t.Fatalf("stderr 应明示缺席: %q", errStr)
	}
	// 蛇形序 m-b 先行：其失败发生在 m-a 发言前，m-a 的第 2 轮请求应见缺席标记
	last := (*seen)[len(*seen)-1]
	if last.Model != "m-a" || !strings.Contains(last.Body, "缺席退出") {
		t.Fatalf("m-a 第 2 轮请求应含 m-b 缺席标记: model=%s", last.Model)
	}
}

// 全部发言 401：exit 2（最可行动码），--json 恒定单对象，status=failed。
func TestCouncilAllFailAuth(t *testing.T) {
	srv, _ := fakeCouncilGateway(t, func(model, body string) int { return http.StatusUnauthorized })
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("council", "-m", "fast", "-C", "1", "--json", "问")
	if code != 2 {
		t.Fatalf("全失败 401 应 exit 2, got %d", code)
	}
	var jr struct {
		Status     string `json:"status"`
		Transcript []struct {
			OK bool `json:"ok"`
		} `json:"transcript"`
		Error *struct {
			Code int    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("stdout 非单对象 JSON: %v\n%s", err, out)
	}
	if jr.Status != "failed" || jr.Error == nil || jr.Error.Code != 2 || jr.Error.Type != "auth" {
		t.Fatalf("status=%s error=%+v", jr.Status, jr.Error)
	}
	if len(jr.Transcript) != 2 || jr.Transcript[0].OK || jr.Transcript[1].OK {
		t.Fatalf("两次发言都应 ok=false: %+v", jr.Transcript)
	}
}

// --json：usage 合计（4 次调用 × (3,2,5) = (12,8,20)）、status=completed、轮次完整。
func TestCouncilJSONUsageSum(t *testing.T) {
	srv, _ := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("council", "-m", "fast", "--json", "问")
	if code != 0 {
		t.Fatal("code != 0")
	}
	var jr struct {
		Status          string `json:"status"`
		RoundsCompleted int    `json:"rounds_completed"`
		Usage           *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Transcript []struct {
			OK bool `json:"ok"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, out)
	}
	if jr.Status != "completed" || jr.RoundsCompleted != 2 {
		t.Fatalf("status=%s rounds_completed=%d", jr.Status, jr.RoundsCompleted)
	}
	if jr.Usage == nil || jr.Usage.PromptTokens != 12 || jr.Usage.CompletionTokens != 8 || jr.Usage.TotalTokens != 20 {
		t.Fatalf("usage 合计应 (12,8,20): %+v", jr.Usage)
	}
	if len(jr.Transcript) != 4 {
		t.Fatalf("应 4 条发言: %d", len(jr.Transcript))
	}
	for _, e := range jr.Transcript {
		if !e.OK {
			t.Fatalf("全部发言应 ok=true: %+v", jr.Transcript)
		}
	}
}

// --dry-run：零请求；输出模型组、轮次调度表（蛇形）、议事规则与首位席位实际 messages。
func TestCouncilDryRun(t *testing.T) {
	srv, seen := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("council", "-m", "fast", "--dry-run", "问")
	if code != 0 {
		t.Fatal("code != 0")
	}
	if len(*seen) != 0 {
		t.Fatal("dry-run 不应发起请求")
	}
	var dr struct {
		Models   []string   `json:"models"`
		Rounds   int        `json:"rounds"`
		System   string     `json:"system"`
		Schedule [][]string `json:"schedule"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(out), &dr); err != nil {
		t.Fatalf("dry-run 输出应为 JSON: %v\n%s", err, out)
	}
	if len(dr.Models) != 2 || dr.Models[0] != "m-a" || dr.Models[1] != "m-b" || dr.Rounds != 2 {
		t.Fatalf("models/rounds 不符: %v %d", dr.Models, dr.Rounds)
	}
	if len(dr.Schedule) != 2 || len(dr.Schedule[0]) != 2 ||
		dr.Schedule[0][0] != "m-a" || dr.Schedule[1][0] != "m-b" {
		t.Fatalf("调度表应为蛇形: %v", dr.Schedule)
	}
	if !strings.Contains(dr.System, "议会") {
		t.Fatalf("system 应含议事规则: %q", dr.System)
	}
	if len(dr.Messages) != 2 || dr.Messages[0].Role != "system" || dr.Messages[1].Role != "user" {
		t.Fatalf("messages 应为 system+user: %+v", dr.Messages)
	}
}
