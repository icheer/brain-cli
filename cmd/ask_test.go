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
	"time"
)

// fakeGateway 起一个 OpenAI 兼容假网关：记录请求体，按给定状态码返回固定正文。
// /models 固定返回 m-a、m-b 两个模型。
func fakeGateway(t *testing.T, status int, content string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"m-a"},{"id":"m-b"}]}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		contentJSON, _ := json.Marshal(content)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":` +
			string(contentJSON) + `},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

// setupConfig 写入临时 config.yaml / models.yaml 并隔离缓存目录。
func setupConfig(t *testing.T, baseURL string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := "base_url: " + baseURL + "\napi_key: test-key\ndefault_model: fast\ntimeout_seconds: 5\n"
	cfg += strings.Join(extra, "\n")
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"),
		[]byte("aliases:\n  fast:\n    models: [m-a, m-b]\n    use: 快\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_CONFIG", cfgPath)
	t.Setenv("BRAIN_MODELS", filepath.Join(dir, "models.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	for _, k := range []string{"BRAIN_BASE_URL", "BRAIN_API_KEY", "BRAIN_TIMEOUT"} {
		t.Setenv(k, "")
	}
	// cobra 复用同一命令对象，重置上次测试残留的 flag 值
	askOpts = askOptions{}
	modelsOpts = modelsOptions{}
	return dir
}

func runBrain(args ...string) (code int, stdout, stderr string) {
	var out, errB strings.Builder
	code = run(args, &out, &errB)
	return code, out.String(), errB.String()
}

// userTextPart 取出请求里 user 消息的第一个文本块。
func userTextPart(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("messages 为空")
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	parts, _ := last["content"].([]any)
	if len(parts) == 0 {
		t.Fatal("content 无文本块")
	}
	p0, _ := parts[0].(map[string]any)
	s, _ := p0["text"].(string)
	return s
}

// golden：stdout 逐字节等于「正文 + 恰好一个换行」，别名解析取链首位。
func TestAskStdoutPurity(t *testing.T) {
	srv, body := fakeGateway(t, 200, "你好，外脑。\n\n")
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("ask", "-m", "fast", "问个问题")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if out != "你好，外脑。\n" {
		t.Fatalf("stdout 逐字节不符: %q", out)
	}
	if !strings.Contains(errStr, "模型 m-a") {
		t.Fatalf("stderr 应含耗时/模型行: %q", errStr)
	}
	if (*body)["model"] != "m-a" {
		t.Fatalf("别名应解析到链首位 m-a: %v", (*body)["model"])
	}
	if got := userTextPart(t, *body); got != "问个问题" {
		t.Fatalf("文本块不符: %q", got)
	}
}

func TestAskStdinPipe(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")
	stdin = strings.NewReader("管道日志内容")
	stdinPiped = true
	t.Cleanup(func() { stdin = os.Stdin; stdinPiped = false })

	code, _, _ := runBrain("ask", "-m", "fast", "分析以下日志")
	if code != 0 {
		t.Fatal("code != 0")
	}
	if want := "分析以下日志\n\n管道日志内容"; userTextPart(t, *body) != want {
		t.Fatalf("拼接顺序不符: %q", userTextPart(t, *body))
	}
}

func TestAskFileSeparator(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	dir := setupConfig(t, srv.URL+"/v1")
	p := filepath.Join(dir, "src.go")
	if err := os.WriteFile(p, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, _ := runBrain("ask", "-m", "fast", "-f", p)
	if code != 0 {
		t.Fatal("code != 0")
	}
	if want := "----- file: " + p + " -----\npackage main"; !strings.Contains(userTextPart(t, *body), want) {
		t.Fatalf("分隔头不符: %q", userTextPart(t, *body))
	}
}

func TestAskImageDataURL(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	dir := setupConfig(t, srv.URL+"/v1")
	png, err := base64.StdEncoding.DecodeString(tinyPNGForCmd)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "dot.png")
	if err := os.WriteFile(p, png, 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errStr := runBrain("ask", "-m", "fast", "-i", p, "这张图里是什么")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	msgs := (*body)["messages"].([]any)
	user := msgs[len(msgs)-1].(map[string]any)
	parts := user["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("应为 1 文本 + 1 图片块: %d", len(parts))
	}
	img := parts[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Fatalf("第二块应为 image_url: %v", img)
	}
	url := img["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("应为 png data URL: %.40s", url)
	}
}

func TestAskSystemPrompt(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")

	code, _, _ := runBrain("ask", "-m", "fast", "-s", "你是审查员", "-s", "只报严重问题", "看代码")
	if code != 0 {
		t.Fatal("code != 0")
	}
	msgs := (*body)["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应含 system + user 两条: %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "你是审查员\n\n只报严重问题" {
		t.Fatalf("system 不符: %v", sys)
	}
}

func TestAskExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   int
	}{
		{"认证失败", 401, 2},
		{"限流", 429, 3},
		{"上游错误", 500, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := fakeGateway(t, c.status, "x")
			setupConfig(t, srv.URL+"/v1")
			code, _, errStr := runBrain("ask", "-m", "fast", "q")
			if code != c.want {
				t.Fatalf("code=%d（want %d）stderr=%s", code, c.want, errStr)
			}
		})
	}
}

func TestAskTimeoutExit4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	setupConfig(t, srv.URL+"/v1")

	// 走 --timeout flag 覆盖（避免与 config 内 timeout_seconds 重复键）
	code, _, errStr := runBrain("ask", "-m", "fast", "--timeout", "1", "q")
	if code != 4 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestAskInputTooLargeExit6(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "x")
	setupConfig(t, srv.URL+"/v1")
	code, _, errStr := runBrain("ask", "-m", "fast", "--max-input-bytes", "10", strings.Repeat("长", 20))
	if code != 6 || !strings.Contains(errStr, "--max-input-bytes") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestAskNoInputExit1(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "x")
	setupConfig(t, srv.URL+"/v1")
	code, _, _ := runBrain("ask")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestAskMissingConfigExit7(t *testing.T) {
	t.Setenv("BRAIN_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv("BRAIN_MODELS", filepath.Join(t.TempDir(), "absent.yaml"))
	for _, k := range []string{"BRAIN_BASE_URL", "BRAIN_API_KEY", "BRAIN_TIMEOUT"} {
		t.Setenv(k, "")
	}
	code, _, errStr := runBrain("ask", "q")
	if code != 7 || !strings.Contains(errStr, "brain config set") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestAskUnknownFlagExit1(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "x")
	setupConfig(t, srv.URL+"/v1")
	code, _, _ := runBrain("ask", "--nope")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
}

const tinyPNGForCmd = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
