package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// 英文界面：BRAIN_LANG=en 时错误提示输出英文（锁定语言切换路径）。
func TestEnglishMessages(t *testing.T) {
	t.Setenv("BRAIN_LANG", "en")
	t.Setenv("BRAIN_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	for _, k := range []string{"BRAIN_BASE_URL", "BRAIN_API_KEY"} {
		t.Setenv(k, "")
	}

	code, _, errStr := runBrain("ask", "q")
	if code != 7 || !strings.Contains(errStr, "missing base_url") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}

	srv, _ := fakeGateway(t, 200, "x")
	setupConfig(t, srv.URL+"/v1")
	t.Setenv("BRAIN_LANG", "en") // setupConfig 清过环境，重新钉住
	code, _, errStr = runBrain("ask", "--stream", "--json", "q")
	if code != 1 || !strings.Contains(errStr, "mutually exclusive") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}
