package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPingOK(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "")
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("ping")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	for _, want := range []string{"连通性 OK", "鉴权 OK", "模型清单 OK", "默认模型 OK", "fast → m-a"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺 %q:\n%s", want, out)
		}
	}
}

func TestPingBadKeyExit2(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "")
	setupConfig(t, srv.URL+"/v1")
	t.Setenv("BRAIN_API_KEY", "sk-wrong") // 触发假网关 401 分支

	code, _, errStr := runBrain("ping")
	if code != 2 || !strings.Contains(errStr, "api_key") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestPingBadDefaultModelExit1(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "")
	dir := setupConfig(t, srv.URL+"/v1")
	// 改写 config.yaml：fast 链指向网关不存在的模型
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("base_url: "+srv.URL+"/v1\napi_key: test-key\ndefault_model: fast\ntimeout_seconds: 5\nretry_max: 0\n"+
			"aliases:\n  fast:\n    models: [m-missing]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errStr := runBrain("ping")
	if code != 1 || !strings.Contains(errStr, "不在网关可用列表") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestPingNoDefaultModelStillOK(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "")
	dir := setupConfig(t, srv.URL+"/v1")
	// 抹掉 default_model（保留 aliases 段，验证互不干扰）
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("base_url: "+srv.URL+"/v1\napi_key: test-key\ntimeout_seconds: 5\nretry_max: 0\n"+
			"aliases:\n  fast:\n    models: [m-a, m-b]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, _ := runBrain("ping")
	if code != 0 || !strings.Contains(out, "鉴权 OK") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

// ping 与 models 必须同样应用 user_agent 配置、输出 0600 权限警告，并支持无 /v1 尾缀的 base_url。
func TestPingAndModelsApplyUserAgentAndPermWarning(t *testing.T) {
	var gotUAs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUAs = append(gotUAs, r.Header.Get("User-Agent"))
		if r.URL.Path != "/v1/models" {
			t.Errorf("裸 base_url 应自动补 /v1，实际请求路径 %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m-a"}]}`))
	}))
	t.Cleanup(srv.Close)

	dir := setupConfig(t, srv.URL) // 故意不带 /v1
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath,
		[]byte("base_url: "+srv.URL+"\napi_key: test-key\ndefault_model: m-a\nuser_agent: custom-ua/9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfgPath, 0o644); err != nil { // WriteFile 覆写既有 0600 文件不改权限，须显式 Chmod
		t.Fatal(err)
	}

	code, _, errStr := runBrain("ping")
	if code != 0 {
		t.Fatalf("ping failed: code=%d stderr=%s", code, errStr)
	}
	if runtime.GOOS != "windows" && !strings.Contains(errStr, "0600") {
		t.Fatalf("ping 应输出 config.yaml 权限警告: %q", errStr)
	}

	code, _, errStr2 := runBrain("models", "--refresh")
	if code != 0 {
		t.Fatalf("models failed: code=%d stderr=%s", code, errStr2)
	}
	if runtime.GOOS != "windows" && !strings.Contains(errStr2, "0600") {
		t.Fatalf("models 应输出 config.yaml 权限警告: %q", errStr2)
	}
	if len(gotUAs) != 2 || gotUAs[0] != "custom-ua/9.9" || gotUAs[1] != "custom-ua/9.9" {
		t.Fatalf("ping/models 应应用配置的 user_agent: %v", gotUAs)
	}
}
