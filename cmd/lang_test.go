package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// 英文界面：BRAIN_LANG=en 时错误提示与命令/flag 帮助均输出英文（验证 newRootCmd 动态 i18n，不再冻结于 init 期）。
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

	// 动态 --help：同进程内先 en 后 zh 切换，均按当次 BRAIN_LANG 构造帮助树
	code, helpEn, _ := runBrain("council", "--help")
	if code != 0 || !strings.Contains(helpEn, "Serial debate") || !strings.Contains(helpEn, "blind debate") {
		t.Fatalf("BRAIN_LANG=en 下 council --help 应为英文: code=%d out=%s", code, helpEn)
	}
	t.Setenv("BRAIN_LANG", "zh")
	code, helpZh, _ := runBrain("council", "--help")
	if code != 0 || !strings.Contains(helpZh, "串行辩论") || !strings.Contains(helpZh, "匿名辩论") {
		t.Fatalf("BRAIN_LANG=zh 下 council --help 应为中文: code=%d out=%s", code, helpZh)
	}

	srv, _ := fakeGateway(t, 200, "x")
	setupConfig(t, srv.URL+"/v1")
	t.Setenv("BRAIN_LANG", "en") // setupConfig 清过环境，重新钉住
	code, _, errStr = runBrain("ask", "--stream", "--json", "q")
	if code != 1 || !strings.Contains(errStr, "mutually exclusive") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}
