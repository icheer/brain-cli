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
	"time"
)

// fakeGateway 起一个 OpenAI 兼容假网关（SSE 传输）：
// 记录请求体；status==200 时把 content 拆成两个增量流出，否则返回错误 JSON。
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
		runes := []rune(content)
		mid := len(runes) / 2
		if mid == 0 {
			mid = len(runes)
		}
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": string(runes[:mid])}}}})
		if mid < len(runes) {
			emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": string(runes[mid:])}}}})
		}
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
		emit(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

// setupConfig 写入临时 config.yaml（含 aliases 段）并隔离缓存目录。
func setupConfig(t *testing.T, baseURL string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := "base_url: " + baseURL + "\napi_key: test-key\ndefault_model: fast\ntimeout_seconds: 5\nretry_max: 0\n" +
		"aliases:\n  fast:\n    models: [m-a, m-b]\n    use: 快\n"
	cfg += strings.Join(extra, "\n")
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_CONFIG", cfgPath)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	for _, k := range []string{"BRAIN_BASE_URL", "BRAIN_API_KEY", "BRAIN_TIMEOUT", "BRAIN_MODELS", "BRAIN_DEFAULT_MODEL"} {
		t.Setenv(k, "")
	}
	// cobra 复用同一命令对象，重置上次测试残留的 flag 值
	askOpts = askOptions{}
	compareOpts = compareOptions{}
	councilOpts = councilOptions{rounds: 2, order: "snake"} // rounds/order 有非零默认值，重置须保留
	modelsOpts = modelsOptions{}
	pingOpts = pingOptions{}
	initOpts = struct {
		baseURL      string
		defaultModel string
		force        bool
	}{}
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
	if want := "<user_message>\n分析以下日志\n</user_message>\n\n<stdin>\n管道日志内容\n</stdin>"; userTextPart(t, *body) != want {
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
	if want := "<attachment path=\"" + p + "\">\npackage main\n</attachment>"; !strings.Contains(userTextPart(t, *body), want) {
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
	for _, k := range []string{"BRAIN_BASE_URL", "BRAIN_API_KEY", "BRAIN_TIMEOUT", "BRAIN_DEFAULT_MODEL"} {
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

// --json：成功时单行 JSON，error 为 null。
func TestAskJSONSuccess(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "正文内容")
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("ask", "-m", "fast", "--json", "q")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("应为单行 JSON + 单换行: %q", out)
	}
	var got struct {
		Content      string `json:"content"`
		Model        string `json:"model"`
		Requested    string `json:"requested_model"`
		FinishReason string `json:"finish_reason"`
		Usage        *struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("解析 JSON: %v\n%s", err, out)
	}
	if got.Content != "正文内容" || got.Model != "m-a" || got.Requested != "fast" ||
		got.FinishReason != "stop" || got.Usage == nil || got.Usage.TotalTokens != 7 {
		t.Fatalf("字段不符: %+v", got)
	}
	if got.Error != nil {
		t.Fatalf("成功时 error 应为 null: %+v", got.Error)
	}
}

// --json：失败时 stdout 仍恒定输出一个 JSON 对象，error.code 与退出码一致。
func TestAskJSONFailureStillOneObject(t *testing.T) {
	srv, _ := fakeGateway(t, 500, "x")
	setupConfig(t, srv.URL+"/v1") // retry_max: 0，两模型各打一次即结束

	code, out, errStr := runBrain("ask", "-m", "fast", "--json", "q")
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("应为单行 JSON: %q", out)
	}
	var got struct {
		Content string `json:"content"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("解析 JSON: %v\n%s", err, out)
	}
	if got.Error == nil || got.Error.Code != 5 || got.Content != "" {
		t.Fatalf("失败对象不符: %+v", got)
	}
	if !strings.Contains(errStr, "故障转移") {
		t.Fatalf("stderr 应含故障转移提示: %q", errStr)
	}
}

const tinyPNGForCmd = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// 多来源标签块 → system 前置数据声明（英文常量）；单问题 → 无 system。
func TestAskDataTagDeclaration(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	dir := setupConfig(t, srv.URL+"/v1")
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("内容"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, _ := runBrain("ask", "-m", "fast", "-f", p, "看看这个文件")
	if code != 0 {
		t.Fatal("code != 0")
	}
	// body 为「最近一次请求」的覆盖式记录：多来源请求 → 首条 message 应是声明 system
	msgs, _ := (*body)["messages"].([]any)
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "system" || !strings.Contains(fmt.Sprint(m0["content"]), "<attachment>") {
		t.Fatalf("多来源应有声明 system: %v", m0)
	}

	askOpts = askOptions{} // cobra flag 状态会跨 runBrain 泄漏（首个 runBrain 带过 -f）
	code, _, _ = runBrain("ask", "-m", "fast", "就一个问题")
	if code != 0 {
		t.Fatal("code != 0")
	}
	msgs, _ = (*body)["messages"].([]any)
	if m0, _ := msgs[0].(map[string]any); m0["role"] == "system" {
		t.Fatalf("单问题不应有 system: %v", m0)
	}
}
