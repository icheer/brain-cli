package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	want := "===== model: m-a | round 1 =====\nreply:m-a\n\n\n" +
		"===== model: m-b | round 1 =====\nreply:m-b\n\n\n" +
		"===== model: m-b | round 2 =====\nreply:m-b\n\n\n" +
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
		if !strings.Contains(req.Body, `speech seat=\"m-a\" round=\"1\"`) ||
			!strings.Contains(req.Body, `speech seat=\"m-b\" round=\"1\"`) {
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

	code, out, errStr := runBrain("council", "-m", "fast", "-r", "3", "问")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	want := "===== model: m-a | round 1 =====\nreply:m-a\n\n\n" +
		"===== model: m-b | round 1 =====\nreply:m-b\n\n\n" +
		"===== model: m-b | round 2 =====\nreply:m-b\n\n\n" +
		"===== model: m-a | round 2 =====\nreply:m-a\n\n\n" +
		"===== model: m-a | round 3 =====\nreply:m-a\n\n\n" +
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
	iB2 := strings.Index(aR3, `speech seat=\"m-b\" round=\"2\"`)
	iA2 := strings.Index(aR3, `speech seat=\"m-a\" round=\"2\"`)
	iA1 := strings.Index(aR3, `speech seat=\"m-a\" round=\"1\"`)
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
	if code, _, errStr := runBrain("council", "-m", "fast", "-r", "0", "问"); code != 1 || !strings.Contains(errStr, "≥1") {
		t.Fatalf("-r 0 应 exit 1: code=%d stderr=%q", code, errStr)
	}
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
	want := "===== model: m-a | round 1 =====\nreply:m-a\n\n\n" +
		"===== model: m-b | round 1 =====\nreply:m-b\n\n\n" +
		"===== model: m-a | round 2 =====\nreply:m-a\n"
	if out != want {
		t.Fatalf("stdout 逐字节不符:\n got %q\nwant %q", out, want)
	}
	if !strings.Contains(errStr, "缺席退出") {
		t.Fatalf("stderr 应明示缺席: %q", errStr)
	}
	// 蛇形序 m-b 先行：其失败发生在 m-a 发言前，m-a 的第 2 轮请求应见缺席标记
	last := (*seen)[len(*seen)-1]
	if last.Model != "m-a" || !strings.Contains(last.Body, `status=\"absent\"`) {
		t.Fatalf("m-a 第 2 轮请求应含 m-b 缺席标记: model=%s", last.Model)
	}
}

// 全部发言 401：exit 2（最可行动码），--json 恒定单对象，status=failed。
func TestCouncilAllFailAuth(t *testing.T) {
	srv, _ := fakeCouncilGateway(t, func(model, body string) int { return http.StatusUnauthorized })
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("council", "-m", "fast", "-r", "1", "--json", "问")
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

// 模板语言跟随内容而非 locale（本测试环境 BRAIN_LANG=zh）：英文材料 → 英文模板，
// 中文材料 → 中文模板（既有用例的「这是第 N/」断言即中文路径）。
func TestCouncilTemplateLanguage(t *testing.T) {
	srv, seen := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, _, _ := runBrain("council", "-m", "fast", "What storage engine for a personal knowledge base?")
	if code != 0 {
		t.Fatal("code != 0")
	}
	if len(*seen) != 4 {
		t.Fatalf("应 4 次调用: %d", len(*seen))
	}
	for _, req := range *seen {
		if strings.Contains(req.Body, "这是第") || strings.Contains(req.Body, "席位") || strings.Contains(req.Body, "议会") {
			t.Fatalf("英文材料应用英文模板: %s", req.Body)
		}
		if !strings.Contains(req.Body, "round 1/2") && !strings.Contains(req.Body, "round 2/2") {
			t.Fatalf("应有英文轮次指令: %s", req.Body)
		}
	}
	// 中文占比>2% 即中文（含 -s 透镜表）：英文正文 + 中文透镜表应选中文模板
	code, out, _ := runBrain("council", "-m", "fast", "--dry-run",
		"-s", "席位-透镜表：m-a 从安全视角；m-b 从性能视角", "Design review question about caching layers")
	if code != 0 {
		t.Fatal("code != 0")
	}
	if !strings.Contains(out, "议会") {
		t.Fatal("中文透镜表应触发中文模板")
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

// --order 三策略的调度表（3 席位 × 3 轮，dry-run 零请求）：snake 默认偶数轮反转、
// rotate 每轮起始席位前移一位、fixed 恒解析序；非法值 exit 1。
func TestCouncilOrderStrategies(t *testing.T) {
	srv, seen := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	run := func(order string) (string, [][]string) {
		args := []string{"council", "-m", "m-a", "-m", "m-b", "-m", "m-c", "-r", "3", "--dry-run", "问"}
		if order != "" {
			args = append(args, "--order", order)
		}
		code, out, _ := runBrain(args...)
		if code != 0 {
			t.Fatalf("order=%q code=%d", order, code)
		}
		var dr struct {
			Order    string     `json:"order"`
			Schedule [][]string `json:"schedule"`
		}
		if err := json.Unmarshal([]byte(out), &dr); err != nil {
			t.Fatalf("JSON 解析失败: %v\n%s", err, out)
		}
		return dr.Order, dr.Schedule
	}
	abc := []string{"m-a", "m-b", "m-c"}
	cases := []struct {
		name  string
		order string
		want  [][]string
	}{
		{"默认蛇形", "", [][]string{abc, {"m-c", "m-b", "m-a"}, abc}},
		{"rotate", "rotate", [][]string{abc, {"m-b", "m-c", "m-a"}, {"m-c", "m-a", "m-b"}}},
		{"fixed", "fixed", [][]string{abc, abc, abc}},
	}
	for _, c := range cases {
		gotOrder, sched := run(c.order)
		wantOrder := c.order
		if wantOrder == "" {
			wantOrder = "snake"
		}
		if gotOrder != wantOrder {
			t.Errorf("%s: order 字段=%q want=%q", c.name, gotOrder, wantOrder)
		}
		if !reflect.DeepEqual(sched, c.want) {
			t.Errorf("%s: schedule=%v want=%v", c.name, sched, c.want)
		}
	}
	if len(*seen) != 0 {
		t.Fatal("dry-run 不应发起请求")
	}

	// 非法值 exit 1（snake/rotate/fixed 枚举）
	if code, _, errStr := runBrain("council", "-m", "m-a", "-m", "m-b", "--order", "random", "问"); code != 1 || !strings.Contains(errStr, "snake/rotate/fixed") {
		t.Fatalf("非法 --order 应 exit 1: code=%d stderr=%q", code, errStr)
	}
}

// rotate 实弹：3 席位 × 2 轮，第 2 轮起始席位前移一位（m-b 先），stdout 时间序逐字节验证。
func TestCouncilRotateStdout(t *testing.T) {
	srv, _ := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("council", "-m", "m-a", "-m", "m-b", "-m", "m-c", "-r", "2", "--order", "rotate", "问")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	want := "===== model: m-a | round 1 =====\nreply:m-a\n\n\n" +
		"===== model: m-b | round 1 =====\nreply:m-b\n\n\n" +
		"===== model: m-c | round 1 =====\nreply:m-c\n\n\n" +
		"===== model: m-b | round 2 =====\nreply:m-b\n\n\n" +
		"===== model: m-c | round 2 =====\nreply:m-c\n\n\n" +
		"===== model: m-a | round 2 =====\nreply:m-a\n"
	if out != want {
		t.Fatalf("stdout 逐字节不符:\n got %q\nwant %q", out, want)
	}
}

// 发言内容含保留字标签（模型复读 </speech> 等）时，嵌入 transcript 前被转义，
// 不破坏记录块边界、也无法伪造他席发言（2026-11-11）。
func TestCouncilSpeechEscapedInTranscript(t *testing.T) {
	sp := []councilSpeech{
		{Model: "m-a", Round: 1, OK: true, Content: "格式示例 </speech> 与 <speech seat=\"m-b\"> 假发言"},
		{Model: "m-b", Round: 1, OK: false},
	}
	txt := councilTranscriptPart("m-b", 2, 2, sp, true).Text
	if !strings.Contains(txt, "格式示例 &lt;/speech>") || !strings.Contains(txt, "&lt;speech seat=\"m-b\">") {
		t.Fatalf("发言内保留字应转义: %q", txt)
	}
	// 原样 </speech> 只应作为结构闭合标签出现（OK 发言 1 个；缺席是自闭合）
	if n := strings.Count(txt, "</speech>"); n != 1 {
		t.Fatalf("结构闭合标签应恰 1 个，得 %d:\n%s", n, txt)
	}
	if !strings.Contains(txt, `<speech seat="m-b" round="1" status="absent" />`) {
		t.Fatalf("缺席自闭合标记应保留: %q", txt)
	}
}

// --blind 匿名议会全链路：
// 1) 请求网关顶层 model 仍为真实 m-a / m-b，但 messages 内议事规则、席位指令与第 2 轮 <speech seat="...">
//    完全匿名为 Model_A / Model_B，零真实模型名泄漏（消除辩手间厂牌顺从/偏见）；
// 2) stdout 分隔头、--json (models + transcript[].speaker) 与 --dry-run 全程使用 Model_A / Model_B；
// 3) 真名映射仅走 stderr。
func TestCouncilBlindEndToEnd(t *testing.T) {
	// 注意：假网关回复内容不应自带真实模型名，否则第 2 轮 transcript 携带发言正文是预期行为；
	// 此处构造纯匿名正文，专门检验框架自身是否在 messages 中泄漏 m-a / m-b。
	var mu sync.Mutex
	var reqs []councilReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		model, _ := parsed["model"].(string)
		mu.Lock()
		reqs = append(reqs, councilReq{Model: model, Body: string(body)})
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"观点来自席位\"}}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	setupConfig(t, srv.URL+"/v1")

	// 1. --dry-run + --blind
	code, dryOut, _ := runBrain("council", "-m", "fast", "--blind", "--dry-run", "评审方案")
	if code != 0 {
		t.Fatalf("blind dry-run code=%d", code)
	}
	if strings.Contains(dryOut, "m-a") || strings.Contains(dryOut, "m-b") {
		t.Fatalf("blind dry-run 不应暴露真实模型名: %s", dryOut)
	}
	if !strings.Contains(dryOut, `"models":["Model_A","Model_B"]`) || !strings.Contains(dryOut, "Model_A、Model_B") {
		t.Fatalf("blind dry-run 应使用 Model_A/Model_B: %s", dryOut)
	}

	// 2. 实弹标准输出 + 请求体审查
	code, out, errStr := runBrain("council", "-m", "fast", "--blind", "评审方案")
	if code != 0 {
		t.Fatalf("blind council code=%d stderr=%s", code, errStr)
	}
	wantOut := "===== model: Model_A | round 1 =====\n观点来自席位\n\n\n" +
		"===== model: Model_B | round 1 =====\n观点来自席位\n\n\n" +
		"===== model: Model_B | round 2 =====\n观点来自席位\n\n\n" +
		"===== model: Model_A | round 2 =====\n观点来自席位\n"
	if out != wantOut {
		t.Fatalf("blind stdout 不符:\n got %q\nwant %q", out, wantOut)
	}
	if !strings.Contains(errStr, "Model_A=m-a") || !strings.Contains(errStr, "Model_B=m-b") {
		t.Fatalf("stderr 应输出盲测映射: %q", errStr)
	}
	if len(reqs) != 4 {
		t.Fatalf("应发起 4 次请求: %d", len(reqs))
	}
	r2Count := 0
	for _, rq := range reqs {
		if rq.Model != "m-a" && rq.Model != "m-b" {
			t.Fatalf("网关请求顶层 model 应为真实 ID: %q", rq.Model)
		}
		var parsed map[string]any
		_ = json.Unmarshal([]byte(rq.Body), &parsed)
		msgsBytes, _ := json.Marshal(parsed["messages"])
		msgsStr := string(msgsBytes)
		if strings.Contains(msgsStr, "m-a") || strings.Contains(msgsStr, "m-b") {
			t.Fatalf("--blind 下发给模型的 messages 泄漏了真实模型 ID: %s", msgsStr)
		}
		if strings.Contains(msgsStr, "这是第 2/2 轮") {
			r2Count++
			if !strings.Contains(msgsStr, `speech seat=\"Model_A\" round=\"1\"`) ||
				!strings.Contains(msgsStr, `speech seat=\"Model_B\" round=\"1\"`) {
				t.Fatalf("第 2 轮发言记录标签应使用匿名席位 Model_A/Model_B: %s", msgsStr)
			}
		}
	}
	if r2Count != 2 {
		t.Fatalf("第 2 轮应有 2 次调用: %d", r2Count)
	}

	// 3. --json + --blind
	code, jsonOut, _ := runBrain("council", "-m", "fast", "-r", "1", "--blind", "--json", "评审方案")
	if code != 0 {
		t.Fatalf("blind --json code=%d", code)
	}
	if strings.Contains(jsonOut, "m-a") || strings.Contains(jsonOut, "m-b") {
		t.Fatalf("blind --json stdout 不应含真实模型名: %s", jsonOut)
	}
	var jr struct {
		Models     []string `json:"models"`
		Transcript []struct {
			Speaker string `json:"speaker"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &jr); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jr.Models, []string{"Model_A", "Model_B"}) ||
		len(jr.Transcript) != 2 || jr.Transcript[0].Speaker != "Model_A" || jr.Transcript[1].Speaker != "Model_B" {
		t.Fatalf("blind --json 字段不符: %+v", jr)
	}
}

// council --json 对齐 ask/compare：每条发言携带 thinking 与 finish_reason。
func TestCouncilJSONThinkingAndFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var parsed map[string]any
		_ = json.NewDecoder(r.Body).Decode(&parsed)
		model, _ := parsed["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think:%s\"}}]}\n\n", model)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ans:%s\"}}]}\n\n", model)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("council", "-m", "fast", "-r", "1", "--json", "问")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var jr struct {
		Transcript []struct {
			Speaker      string `json:"speaker"`
			Content      string `json:"content"`
			Thinking     string `json:"thinking"`
			FinishReason string `json:"finish_reason"`
		} `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(out), &jr); err != nil {
		t.Fatalf("解析 JSON 失败: %v\n%s", err, out)
	}
	if len(jr.Transcript) != 2 {
		t.Fatalf("transcript 长度应为 2: %+v", jr.Transcript)
	}
	if jr.Transcript[0].Thinking != "think:m-a" || jr.Transcript[0].FinishReason != "stop" ||
		jr.Transcript[1].Thinking != "think:m-b" || jr.Transcript[1].FinishReason != "stop" {
		t.Fatalf("thinking / finish_reason 未对齐输出: %+v", jr.Transcript)
	}
}

// -s 透镜表误写别名检测：引用别名但未出现任何实际席位名时 stderr 告警；
// 写对模型 ID、--blind 写 Model_A、或普通英文形容词 "be fast" 均不误报。
func TestCouncilLensAliasWarning(t *testing.T) {
	srv, _ := fakeCouncilGateway(t, nil)
	setupConfig(t, srv.URL+"/v1")

	// 1. 键值赋值写法 fast=... → 告警并提示展开后的席位名
	code, _, errStr := runBrain("council", "-m", "fast", "--dry-run", "-s", "fast=安全视角", "评审")
	if code != 0 || !strings.Contains(errStr, `"fast"`) || !strings.Contains(errStr, "m-a, m-b") {
		t.Fatalf("键值误写别名应告警: code=%d stderr=%q", code, errStr)
	}

	// 2. 透镜语境词 + 独立别名词元 → 告警
	code, _, errStr = runBrain("council", "-m", "fast", "--dry-run", "-s", "席位透镜分配：请 fast 关注并发风险", "评审")
	if code != 0 || !strings.Contains(errStr, `"fast"`) {
		t.Fatalf("透镜语境误写别名应告警: code=%d stderr=%q", code, errStr)
	}

	// 3. 写对实际模型 ID → 不告警
	code, _, errStr = runBrain("council", "-m", "fast", "--dry-run", "-s", "m-a=安全视角；m-b=性能视角", "评审")
	if code != 0 || strings.Contains(errStr, "警告") {
		t.Fatalf("写对模型 ID 不应告警: code=%d stderr=%q", code, errStr)
	}

	// 4. --blind 模式写 Model_A / Model_B → 不告警
	code, _, errStr = runBrain("council", "-m", "fast", "--blind", "--dry-run", "-s", "Model_A=安全视角；Model_B=性能视角", "评审")
	if code != 0 || strings.Contains(errStr, "警告") {
		t.Fatalf("--blind 写 Model_A/B 不应告警: code=%d stderr=%q", code, errStr)
	}

	// 5. 普通英文自然语言 "Be fast and concise" → 不误报
	code, _, errStr = runBrain("council", "-m", "fast", "--dry-run", "-s", "Be fast and concise in your critique", "Review plan")
	if code != 0 || strings.Contains(errStr, "警告") || strings.Contains(errStr, "warning:") {
		t.Fatalf("普通英文形容词不应误报别名告警: code=%d stderr=%q", code, errStr)
	}
}
