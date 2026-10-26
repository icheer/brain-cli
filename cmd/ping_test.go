package cmd

import (
	"os"
	"path/filepath"
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
