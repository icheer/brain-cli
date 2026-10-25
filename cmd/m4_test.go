package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// --max-output-tokens 透传：设置才出现在请求体；不设时字段彻底缺席。
func TestMaxOutputTokensPassthrough(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")

	if code, _, errStr := runBrain("ask", "-m", "fast", "--max-output-tokens", "512", "q"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if got := (*body)["max_tokens"]; got != float64(512) {
		t.Fatalf("max_tokens 应透传: %v", got)
	}

	srv2, body2 := fakeGateway(t, 200, "ok")
	setupConfig(t, srv2.URL+"/v1")
	if code, _, errStr := runBrain("ask", "-m", "fast", "q"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if _, ok := (*body2)["max_tokens"]; ok {
		t.Fatalf("未设 --max-output-tokens 时请求体不应含 max_tokens: %v", *body2)
	}
}

// --max-output-tokens 与 --json：回显到输出对象。
func TestMaxOutputTokensEcho(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("ask", "-m", "fast", "--max-output-tokens", "256", "--json", "q")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got struct {
		MaxOutputTokens int `json:"max_output_tokens"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.MaxOutputTokens != 256 {
		t.Fatalf("JSON 应回显 max_output_tokens: %d", got.MaxOutputTokens)
	}
}

// --max-output-tokens 与 --dry-run：回显到请求形状。
func TestMaxOutputTokensDryRunEcho(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("ask", "-m", "fast", "--max-output-tokens", "256", "--dry-run", "q")
	if code != 0 || !strings.Contains(out, `"max_output_tokens":256`) {
		t.Fatalf("dry-run 应回显 max_output_tokens: code=%d out=%s", code, out)
	}
}

func TestMaxOutputTokensNegativeExit1(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1")
	if code, _, errStr := runBrain("ask", "-m", "fast", "--max-output-tokens", "-1", "q"); code != 1 || !strings.Contains(errStr, "正整数") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

// BRAIN_DEFAULT_MODEL 环境变量：无 -m 时覆盖 config 的 default_model（CI/容器免写配置文件）。
func TestBrainDefaultModelEnv(t *testing.T) {
	srv, body := fakeGateway(t, 200, "ok")
	setupConfig(t, srv.URL+"/v1") // config 里 default_model: fast → m-a
	t.Setenv("BRAIN_DEFAULT_MODEL", "m-b")

	code, _, errStr := runBrain("ask", "q")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	if (*body)["model"] != "m-b" {
		t.Fatalf("BRAIN_DEFAULT_MODEL 应覆盖 default_model: %v", (*body)["model"])
	}
}
