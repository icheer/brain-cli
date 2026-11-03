package cmd

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// drawPNG 1x1 标准 PNG（测试落盘/参考图用）。
var drawPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

// setupDrawConfig 在 setupConfig 基础上写入 draw 段，返回配置目录。
func setupDrawConfig(t *testing.T, srvURL, model, typ string) string {
	t.Helper()
	dir := setupConfig(t, srvURL)
	lines := "draw:\n  model: " + model + "\n  base_url: " + srvURL + "\n  api_key: test-key\n"
	if typ != "" {
		lines += "  type: " + typ + "\n"
	}
	appendConfig(t, dir, lines)
	return dir
}

func appendConfig(t *testing.T, dir, lines string) {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(old, []byte(lines)...), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeDrawGateway 起一个双协议假网关（openai images + gemini 原生共用一个 server）：
// status!=0 时按错误处理；否则按路径返回携带 b64 PNG 的对应协议响应。
func fakeDrawGateway(t *testing.T, status int) *httptest.Server {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(drawPNG)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case status != 0:
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
		case strings.Contains(r.URL.Path, ":generateContent"):
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"好的"},{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}]}`))
		default: // /v1/images/generations | /v1/images/edits
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDrawNoModelExit7(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupConfig(t, srv.URL+"/v1") // 只有顶层段，无 draw 段

	code, _, errStr := runBrain("draw", "一只猫")
	if code != 7 || !strings.Contains(errStr, "draw.model") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestDrawTypeInvalidExit7(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupDrawConfig(t, srv.URL, "some-model", "foo")

	code, _, errStr := runBrain("draw", "一只猫")
	if code != 7 || !strings.Contains(errStr, "draw.type") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestDrawFlagValidation(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupDrawConfig(t, srv.URL, "gpt-image-x", "openai")

	code, _, errStr := runBrain("draw", "--aspect", "3:7", "x")
	if code != 1 || !strings.Contains(errStr, "--aspect") {
		t.Fatalf("aspect code=%d stderr=%s", code, errStr)
	}
	// 同一测试内第二次 runBrain 需显式带合法 aspect（flag 值跨次执行残留）
	code, _, errStr = runBrain("draw", "--aspect", "1:1", "--res", "8K", "x")
	if code != 1 || !strings.Contains(errStr, "--res") {
		t.Fatalf("res code=%d stderr=%s", code, errStr)
	}
}

func TestDrawNoPromptExit1(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupDrawConfig(t, srv.URL, "gpt-image-x", "openai")

	code, _, errStr := runBrain("draw")
	if code != 1 || !strings.Contains(errStr, "prompt") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestDrawDryRunZeroNetwork(t *testing.T) {
	// base_url 指向不可达端口：dry-run 零网络，依然成功——证明确实没发请求
	setupDrawConfig(t, "https://127.0.0.1:9", "gemini-3.1-flash-image", "")

	code, out, errStr := runBrain("draw", "--dry-run", "--aspect", "16:9", "--res", "2K", "一只猫")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var dr struct {
		Model    string            `json:"model"`
		Type     string            `json:"type"`
		Endpoint string            `json:"endpoint"`
		Params   map[string]string `json:"params"`
		Prompt   string            `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dr); err != nil {
		t.Fatalf("dry-run 输出非 JSON: %v\n%s", err, out)
	}
	if dr.Type != "gemini" || !strings.Contains(dr.Endpoint, "/v1beta/models/gemini-3.1-flash-image:generateContent") {
		t.Errorf("type/endpoint: %s %s", dr.Type, dr.Endpoint)
	}
	if strings.Contains(out, "test-key") {
		t.Error("dry-run 输出泄漏了 api_key")
	}
	if dr.Params["aspectRatio"] != "16:9" || dr.Params["imageSize"] != "2K" {
		t.Errorf("params=%v", dr.Params)
	}
	if dr.Prompt != "一只猫" {
		t.Errorf("prompt=%q", dr.Prompt)
	}
}

func TestDrawDryRunOpenAITranslation(t *testing.T) {
	setupDrawConfig(t, "https://127.0.0.1:9", "gpt-image-x", "openai")

	ref := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(ref, drawPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runBrain("draw", "--dry-run", "--aspect", "9:16", "--res", "4K", "-i", ref, "x")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var dr struct {
		Params map[string]string `json:"params"`
		Refs   []struct {
			Name  string `json:"name"`
			MIME  string `json:"mime"`
			Bytes int    `json:"bytes"`
		} `json:"refs"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dr); err != nil {
		t.Fatal(err)
	}
	if dr.Params["size"] != "1024x1536" || dr.Params["quality"] != "high" {
		t.Errorf("params=%v", dr.Params)
	}
	if !strings.HasSuffix(dr.Endpoint, "/images/edits") {
		t.Errorf("带参考图应走 edits 端点: %s", dr.Endpoint)
	}
	if len(dr.Refs) != 1 || dr.Refs[0].MIME != "image/png" || dr.Refs[0].Bytes != len(drawPNG) {
		t.Errorf("refs=%+v", dr.Refs)
	}
}

func TestDrawGeminiSuccessSavesFile(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupDrawConfig(t, srv.URL, "gemini-3.1-flash-image", "")
	t.Chdir(t.TempDir()) // 落盘在当前工作目录

	code, out, errStr := runBrain("draw", "--res", "1K", "一只戴帽子的猫")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	path := strings.TrimSpace(out)
	if strings.Contains(path, "\n") || !filepath.IsAbs(path) {
		t.Fatalf("stdout 应恰一行绝对路径: %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回落盘文件失败: %v", err)
	}
	if string(data) != string(drawPNG) {
		t.Errorf("落盘字节不符: %dB", len(data))
	}
	if !strings.Contains(filepath.Base(path), "brain-draw-") || !strings.HasSuffix(path, ".png") {
		t.Errorf("文件名形态: %s", path)
	}
	if !strings.Contains(errStr, "已保存") || !strings.Contains(errStr, "模型附言") {
		t.Errorf("stderr 缺摘要: %s", errStr)
	}
}

func TestDrawOpenAIJSONContract(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupDrawConfig(t, srv.URL, "gpt-image-x", "openai")
	t.Chdir(t.TempDir())

	code, out, errStr := runBrain("draw", "--json", "a cat")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var payload drawJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		t.Fatalf("非单对象 JSON: %v\n%s", err, out)
	}
	if payload.Error != nil || payload.Type != "openai" || payload.SavedPath == "" || payload.MIME != "image/png" {
		t.Errorf("payload=%+v", payload)
	}
	if data, err := os.ReadFile(payload.SavedPath); err != nil || string(data) != string(drawPNG) {
		t.Errorf("落盘核对失败: %v", err)
	}
}

func TestDrawAuthFailExit2(t *testing.T) {
	srv := fakeDrawGateway(t, 401)
	setupDrawConfig(t, srv.URL, "gpt-image-x", "openai")
	t.Chdir(t.TempDir())

	code, _, errStr := runBrain("draw", "a cat")
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestDrawAuthFailJSON(t *testing.T) {
	srv := fakeDrawGateway(t, 401)
	setupDrawConfig(t, srv.URL, "gpt-image-x", "openai")
	t.Chdir(t.TempDir())

	code, out, _ := runBrain("draw", "--json", "a cat")
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	var payload struct {
		Error *askJSONError `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		t.Fatalf("非单对象 JSON: %v", err)
	}
	if payload.Error == nil || payload.Error.Code != 2 || payload.Error.Type != "auth" || payload.Error.UpstreamStatus != 401 {
		t.Errorf("error=%+v", payload.Error)
	}
}

func TestDrawRefImageEdits(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupDrawConfig(t, srv.URL, "gpt-image-x", "openai")
	t.Chdir(t.TempDir())

	ref := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(ref, drawPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errStr := runBrain("draw", "-i", ref, "make it snowy")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if data, err := os.ReadFile(strings.TrimSpace(out)); err != nil || string(data) != string(drawPNG) {
		t.Errorf("落盘核对失败: %v", err)
	}
}

func TestDrawURLRefRejected(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	setupDrawConfig(t, srv.URL, "gpt-image-x", "openai")

	code, _, errStr := runBrain("draw", "-i", "https://example.com/x.png", "x")
	if code != 1 || !strings.Contains(errStr, "本地文件") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestDrawConfigSetDrawKeys(t *testing.T) {
	srv := fakeDrawGateway(t, 0)
	dir := setupConfig(t, srv.URL+"/v1")

	code, _, _ := runBrain("config", "set", "draw.model", "m-img")
	if code != 0 {
		t.Fatalf("set draw.model code=%d", code)
	}
	code, _, _ = runBrain("config", "set", "draw.type", "GEMINI") // 大小写归一
	if code != 0 {
		t.Fatalf("set draw.type code=%d", code)
	}
	code, out, _ := runBrain("config", "get", "draw.type")
	if code != 0 || strings.TrimSpace(out) != "gemini" {
		t.Fatalf("get draw.type=%q", out)
	}
	code, _, errStr := runBrain("config", "set", "draw.type", "foo")
	if code != 1 || !strings.Contains(errStr, "draw.type") {
		t.Fatalf("非法 type 应 exit 1: code=%d stderr=%s", code, errStr)
	}
	code, out, _ = runBrain("config", "list")
	if code != 0 || !strings.Contains(out, "draw.model = m-img") {
		t.Fatalf("list 缺 draw.model: %s", out)
	}
	// 无 draw 段的既有配置 set 后 round-trip 不应引入空段以外的东西
	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(data), "draw:") {
		t.Fatal("draw 段未写入")
	}
}

func TestSaveImageAndExtFor(t *testing.T) {
	t.Chdir(t.TempDir())

	p, err := saveImage(drawPNG, "image/png", os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(p), "brain-draw-") || !strings.HasSuffix(p, ".png") {
		t.Errorf("path=%s", p)
	}
	// 同秒再存一张：序号递增不覆盖
	p2, err := saveImage(drawPNG, "image/png", os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if p == p2 {
		t.Errorf("同秒冲突未避让: %s", p)
	}
	// 未知 mime 用字节头 sniff
	if ext := extFor(drawPNG, ""); ext != ".png" {
		t.Errorf("extFor sniff=%q", ext)
	}
	if ext := extFor([]byte("plain text"), ""); ext != ".bin" {
		t.Errorf("extFor unknown=%q", ext)
	}
	if ext := extFor(nil, "image/webp"); ext != ".webp" {
		t.Errorf("extFor webp=%q", ext)
	}
}

// ---- init 向导的 draw 步骤 ----

// setupInitTest init 类测试的隔离环境（无既有 config.yaml 起点，draw 环境变量清空）。
func setupInitTest(t *testing.T) (dir, cfgPath string) {
	t.Helper()
	dir = t.TempDir()
	cfgPath = filepath.Join(dir, "config.yaml")
	t.Setenv("BRAIN_CONFIG", cfgPath)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("BRAIN_API_KEY", "sk-env")
	for _, k := range []string{"BRAIN_DRAW_MODEL", "BRAIN_DRAW_BASE_URL", "BRAIN_DRAW_API_KEY", "BRAIN_DRAW_TYPE"} {
		t.Setenv(k, "")
	}
	initOpts = struct {
		baseURL      string
		defaultModel string
		force        bool
	}{}
	return dir, cfgPath
}

func TestInitDrawSetupFlow(t *testing.T) {
	_, cfgPath := setupInitTest(t)
	// base_url / api_key / default_model / 跳过别名 / 配置 draw=y / model / 留空 base_url / 留空 key / 留空 type
	stdin = strings.NewReader("https://gw.example.com/v1\nsk-abc\nfast\nn\ny\ngpt-image-x\n\n\n\n")
	t.Cleanup(func() { stdin = os.Stdin })

	code, _, errStr := runBrain("init")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	data, _ := os.ReadFile(cfgPath)
	s := string(data)
	if !strings.Contains(s, "draw:") || !strings.Contains(s, "model: gpt-image-x") {
		t.Fatalf("缺 draw 段:\n%s", s)
	}
	if strings.Contains(s, "draw.base_url") || strings.Contains(s, "base_url: \"\"") ||
		strings.Contains(s, "draw.type") || strings.Contains(s, "api_key: \"\"") {
		t.Fatalf("留空项不应写入（omitempty 兜底语义）:\n%s", s)
	}
}

func TestInitDrawSkipOnEnter(t *testing.T) {
	_, cfgPath := setupInitTest(t)
	stdin = strings.NewReader("https://gw.example.com/v1\nsk-abc\nfast\nn\n\n") // draw 问题回车 → 跳过
	t.Cleanup(func() { stdin = os.Stdin })

	code, _, _ := runBrain("init")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	data, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(data), "draw:") {
		t.Fatalf("回车跳过不应有 draw 段:\n%s", data)
	}
}

// init 覆盖重建：已有 draw 段默认保留（此前会被静默清空——与别名当初栽的是同一个坑）。
func TestInitOverwriteKeepsDraw(t *testing.T) {
	_, cfgPath := setupInitTest(t)
	os.WriteFile(cfgPath, []byte("base_url: https://old/v1\napi_key: k\n"+
		"draw:\n  model: keep-me\n  type: gemini\n"+
		"aliases:\n  reason:\n    models: [r-1]\n"), 0o600)

	// 覆盖 y / 新 base_url / api_key 空(→env) / default_model 空 / 重新生成别名空(→保留) / 重新配置 draw EOF(→保留)
	stdin = strings.NewReader("y\nhttps://new.example/v1\n\n\n\n")
	t.Cleanup(func() { stdin = os.Stdin })

	code, _, _ := runBrain("init")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	data, _ := os.ReadFile(cfgPath)
	s := string(data)
	if !strings.Contains(s, "base_url: https://new.example/v1") {
		t.Fatalf("应写入新 base_url:\n%s", s)
	}
	if !strings.Contains(s, "model: keep-me") || !strings.Contains(s, "type: gemini") {
		t.Fatalf("覆盖重建应保留既有 draw 段:\n%s", s)
	}
}

func TestInitDrawInvalidTypeFallsBack(t *testing.T) {
	_, cfgPath := setupInitTest(t)
	stdin = strings.NewReader("https://gw.example.com/v1\nsk-abc\nfast\nn\ny\nm\n\n\nfoo\n") // type 输 foo
	t.Cleanup(func() { stdin = os.Stdin })

	code, _, _ := runBrain("init")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	data, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(data), "foo") {
		t.Fatalf("非法 type 应清空（按模型名推断）:\n%s", data)
	}
}
