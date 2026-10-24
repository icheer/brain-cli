package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --stream：增量写出最终与一次性输出字节一致（换行约定补齐）。
func TestAskStream(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "流式正文内容")
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("ask", "-m", "fast", "--stream", "q")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if out != "流式正文内容\n" {
		t.Fatalf("stdout 逐字节不符: %q", out)
	}
}

// abruptGateway 发一个增量后直接掐断 TCP 连接（制造中途断流）。
func abruptGateway(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"半截\"}}]}\n\n")
		fl.Flush()
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --stream 中途断流：半截正文保留在 stdout、不再故障转移、exit 5（已输出即认命）。
func TestAskStreamAbortPartialNoFallback(t *testing.T) {
	srv := abruptGateway(t)
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("ask", "-m", "fast", "--stream", "q")
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if out != "半截" {
		t.Fatalf("半截正文应保留: %q", out)
	}
	if strings.Contains(errStr, "故障转移") {
		t.Fatalf("已输出正文不得故障转移: %q", errStr)
	}
}

func TestAskStreamJSONConflict(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "x")
	setupConfig(t, srv.URL+"/v1")
	code, _, errStr := runBrain("ask", "--stream", "--json", "q")
	if code != 1 || !strings.Contains(errStr, "互斥") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

// --show-thinking：思维链走 stderr（带标头），stdout 仍只有正文。
func TestAskShowThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先想想\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"正文答案\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("ask", "-m", "fast", "--show-thinking", "q")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if out != "正文答案\n" {
		t.Fatalf("stdout 应只有正文: %q", out)
	}
	if !strings.Contains(errStr, "思维链") || !strings.Contains(errStr, "先想想") {
		t.Fatalf("stderr 应含思维链: %q", errStr)
	}
}

func TestInitWritesConfig(t *testing.T) {
	dir := setupConfig(t, "unused") // 借用环境隔离（BRAIN_CONFIG 指向临时路径）
	cfgPath := filepath.Join(dir, "config.yaml")
	os.Remove(cfgPath) // init 需要「不存在」起点

	stdin = strings.NewReader("https://gw.example.com/v1\nsk-abc123\nfast\n")
	t.Cleanup(func() { stdin = os.Stdin })

	code, out, errStr := runBrain("init")
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out, errStr)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"base_url: https://gw.example.com/v1", "api_key: sk-abc123", "default_model: fast"} {
		if !strings.Contains(s, want) {
			t.Fatalf("config 缺 %q:\n%s", want, s)
		}
	}
	if info, _ := os.Stat(cfgPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("权限应为 0600: %04o", info.Mode().Perm())
	}
}

func TestInitEnvKeyFallback(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	t.Setenv("BRAIN_CONFIG", cfgPath)
	t.Setenv("BRAIN_API_KEY", "sk-from-env")
	t.Setenv("BRAIN_MODELS", filepath.Join(dir, "models.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))

	stdin = strings.NewReader("https://gw.example.com/v1\n\nfast\n") // api_key 留空 → 用环境变量
	t.Cleanup(func() { stdin = os.Stdin })

	code, _, _ := runBrain("init")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "api_key: sk-from-env") {
		t.Fatalf("应回落 BRAIN_API_KEY:\n%s", data)
	}
}

func TestInitCancelOverwrite(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	t.Setenv("BRAIN_CONFIG", cfgPath)
	t.Setenv("BRAIN_MODELS", filepath.Join(dir, "models.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	os.WriteFile(cfgPath, []byte("base_url: https://keep/v1\n"), 0o600)

	stdin = strings.NewReader("n\n")
	t.Cleanup(func() { stdin = os.Stdin })

	code, _, _ := runBrain("init")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "keep") {
		t.Fatalf("取消覆盖后原配置应保留:\n%s", data)
	}
}
