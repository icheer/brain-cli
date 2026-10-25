package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -e 透传：设置才出现在请求体；不设时字段彻底缺席（老用户请求字节不变）。
func TestReasoningEffortPassthrough(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")

	if code, _, errStr := runBrain("ask", "-m", "fast", "-e", "high", "q"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if got := (*body)["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort 应透传: %v", got)
	}

	// 不设 -e：字段缺席，而非空串
	srv2, body2 := fakeGateway(t, 200, "ok")
	dir := setupConfig(t, srv2.URL+"/v1")
	_ = dir
	if code, _, errStr := runBrain("ask", "-m", "fast", "q"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if _, ok := (*body2)["reasoning_effort"]; ok {
		t.Fatalf("未设 -e 时请求体不应含 reasoning_effort: %v", *body2)
	}
}

// -e 与 --json：effort 回显到输出对象（可观测）。
func TestReasoningEffortJSONEcho(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("ask", "-m", "fast", "-e", "low", "--json", "q")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got struct {
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.ReasoningEffort != "low" {
		t.Fatalf("JSON 应回显 reasoning_effort: %q", got.ReasoningEffort)
	}
}

// --json 失败对象扩展：type/retryable/upstream_status/attempted_models（纯增量，code 不变）。
func TestAskJSONErrorDetail(t *testing.T) {
	srv, _ := fakeGateway(t, 500, "x")
	setupConfig(t, srv.URL+"/v1") // retry_max: 0，两模型各打一次即结束

	code, out, _ := runBrain("ask", "-m", "fast", "--json", "q")
	if code != 5 {
		t.Fatalf("code=%d", code)
	}
	var got struct {
		Error *struct {
			Code            int      `json:"code"`
			Type            string   `json:"type"`
			Retryable       bool     `json:"retryable"`
			UpstreamStatus  int      `json:"upstream_status"`
			AttemptedModels []string `json:"attempted_models"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("解析 JSON: %v\n%s", err, out)
	}
	e := got.Error
	if e == nil || e.Code != 5 || e.Type != "upstream" || !e.Retryable || e.UpstreamStatus != 500 {
		t.Fatalf("错误对象不符: %+v", e)
	}
	if len(e.AttemptedModels) != 2 || e.AttemptedModels[0] != "m-a" || e.AttemptedModels[1] != "m-b" {
		t.Fatalf("attempted_models 应为完整链: %v", e.AttemptedModels)
	}
}

// --quiet：心跳/耗时/转移提示静默，错误与警告保留。
func TestAskQuiet(t *testing.T) {
	srv, _ := fakeGateway(t, 500, "x")
	setupConfig(t, srv.URL+"/v1")

	code, _, errStr := runBrain("ask", "-m", "fast", "--quiet", "q")
	if code != 5 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(errStr, "故障转移") {
		t.Fatalf("--quiet 下不应有转移提示: %q", errStr)
	}
	if strings.Contains(errStr, "| 模型") {
		t.Fatalf("--quiet 下不应有耗时行: %q", errStr)
	}
	if !strings.Contains(errStr, "HTTP 500") {
		t.Fatalf("错误本身必须保留: %q", errStr)
	}
}

// --dry-run：输出组装结果（单行 JSON），不发网络请求。
func TestAskDryRun(t *testing.T) {
	srv, body := fakeGateway(t, 200, "x")
	dir := setupConfig(t, srv.URL+"/v1")

	// 大一点的 PNG，保证 data URL 超过截断阈值
	png, err := base64.StdEncoding.DecodeString(tinyPNGForCmd)
	if err != nil {
		t.Fatal(err)
	}
	png = append(png, make([]byte, 300)...)
	img := filepath.Join(dir, "big.png")
	if err := os.WriteFile(img, png, 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "a.go")
	os.WriteFile(src, []byte("package main"), 0o644)

	code, out, errStr := runBrain("ask", "-m", "fast", "-f", src, "-i", img, "-s", "你是审查员", "--dry-run", "看代码")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("应为单行 JSON + 单换行: %q", out)
	}
	for _, want := range []string{
		`"chain":["m-a","m-b"]`,
		"你是审查员",
		"----- file:",
		"data:image/png;base64",
		"省略",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run 输出缺 %q:\n%s", want, out)
		}
	}
	if len(*body) != 0 {
		t.Fatalf("--dry-run 不应发起网络请求: %v", *body)
	}
}

func TestAskDryRunMutualExclusions(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "x")
	setupConfig(t, srv.URL+"/v1")

	for _, args := range [][]string{
		{"ask", "--dry-run", "--json", "q"},
		{"ask", "--dry-run", "--stream", "q"},
	} {
		if code, _, errStr := runBrain(args...); code != 1 || !strings.Contains(errStr, "互斥") {
			t.Fatalf("%v: code=%d stderr=%s", args, code, errStr)
		}
	}
}

// probeGateway：/models 返回 m-a/m-b；chat 按模型分流——ok 集合内走 SSE，其余 404。
func probeGateway(t *testing.T, ok map[string]bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = fmt.Fprint(w, `{"data":[{"id":"m-a"},{"id":"m-b"}]}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fl, _ := w.(http.Flusher)
		if ok[body["model"].(string)] {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		} else {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"message":"no such model","code":404}}`)
		}
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ping -m：逐模型探测别名链；主模型挂、后备通 → exit 0 但 stderr 明示。
func TestPingProbeChain(t *testing.T) {
	srv := probeGateway(t, map[string]bool{"m-a": true}) // m-b → 404
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("ping", "-m", "fast")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if !strings.Contains(out, "探测 OK  m-a") {
		t.Fatalf("stdout 应含 m-a 探测成功: %q", out)
	}
	if !strings.Contains(errStr, "探测失败 m-b") {
		t.Fatalf("stderr 应含 m-b 探测失败: %q", errStr)
	}
}

// ping -m：链上全部失败 → exit 5。
func TestPingProbeAllFail(t *testing.T) {
	srv := probeGateway(t, map[string]bool{})
	setupConfig(t, srv.URL+"/v1")
	// default_model: fast 解析到 m-a 且不在可用列表会先 exit 1 —— 换成链上模型都在列表的场景：
	// 这里 /models 恒返回 m-a/m-b，default_model fast → m-a 在列表，通过；随后探测全 404。
	code, _, errStr := runBrain("ping", "-m", "fast")
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

// ping --json：单对象输出，probes 数组机器可读。
func TestPingJSON(t *testing.T) {
	srv := probeGateway(t, map[string]bool{"m-a": true, "m-b": true})
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("ping", "-m", "fast", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got pingJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("解析 JSON: %v\n%s", err, out)
	}
	if got.Connectivity != "ok" || got.Auth != "ok" || got.ModelCount != 2 {
		t.Fatalf("基础字段不符: %+v", got)
	}
	if got.DefaultModel == nil || !got.DefaultModel.OK || got.DefaultModel.Resolved != "m-a" {
		t.Fatalf("default_model 不符: %+v", got.DefaultModel)
	}
	if len(got.Probes) != 2 || !got.Probes[0].OK || !got.Probes[1].OK || got.Error != nil {
		t.Fatalf("probes 不符: %+v", got)
	}
}

// brain models：未配置 models.yaml 时给冷启动提示。
func TestModelsNoAliasNote(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "x")
	dir := setupConfig(t, srv.URL+"/v1")
	t.Setenv("BRAIN_MODELS", filepath.Join(dir, "absent-models.yaml"))

	code, out, _ := runBrain("models")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "未配置 models.yaml") || !strings.Contains(out, "brain init") {
		t.Fatalf("应提示别名冷启动: %q", out)
	}
}

// init 别名向导：序号/逗号链/跳过/default_model 闭环。
func TestInitAliasWizard(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "x") // /models → m-a, m-b
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	t.Setenv("BRAIN_CONFIG", cfgPath)
	t.Setenv("BRAIN_MODELS", filepath.Join(dir, "models.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("BRAIN_API_KEY", "")
	t.Setenv("BRAIN_BASE_URL", "")

	// api_key / default_model(空) / 生成别名(空=Y) / fast=1 / reason=2,1 / coding=空 / vision=空 / 设 fast 为默认=y
	stdin = strings.NewReader("test-key\n\n\n1\n2,1\n\n\ny\n")
	t.Cleanup(func() { stdin = os.Stdin })

	code, out, errStr := runBrain("init", "--base-url", srv.URL+"/v1")
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out, errStr)
	}
	mf, err := os.ReadFile(filepath.Join(dir, "models.yaml"))
	if err != nil {
		t.Fatalf("models.yaml 应已生成: %v", err)
	}
	s := string(mf)
	if !strings.Contains(s, "fast:") || !strings.Contains(s, "m-a") {
		t.Fatalf("fast 别名不符:\n%s", s)
	}
	if !strings.Contains(s, "reason:") || !strings.Contains(s, "- m-b") {
		t.Fatalf("reason 链不符:\n%s", s)
	}
	if strings.Contains(s, "coding") {
		t.Fatalf("跳过的别名不应写入:\n%s", s)
	}
	cfg, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(cfg), "default_model: fast") {
		t.Fatalf("default_model 应闭环设为 fast:\n%s", cfg)
	}
	if info, _ := os.Stat(filepath.Join(dir, "models.yaml")); info.Mode().Perm() != 0o644 {
		t.Fatalf("models.yaml 权限应为 0644: %04o", info.Mode().Perm())
	}
}
