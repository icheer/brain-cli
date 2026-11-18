package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSplitCommandArgs(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{`code --wait`, []string{"code", "--wait"}, false},
		{`vim -u NONE -c "set binary"`, []string{"vim", "-u", "NONE", "-c", "set binary"}, false},
		{`'/Applications/Visual Studio Code.app/bin/code' --wait`, []string{"/Applications/Visual Studio Code.app/bin/code", "--wait"}, false},
		{`"C:\Program Files\Editor\ed.exe" --flag`, []string{`C:\Program Files\Editor\ed.exe`, "--flag"}, false},
		{`vim -c "echo \"ok\""`, []string{"vim", "-c", `echo "ok"`}, false},
		{`vim "unclosed`, nil, true},
	}
	for _, c := range cases {
		got, err := splitCommandArgs(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitCommandArgs(%q) 应报错，实际返回 %v", c.in, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCommandArgs(%q) = (%v, %v), want (%v, nil)", c.in, got, err, c.want)
		}
	}
}

func TestConfigEditWithArgsAndSpacedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell stub 测试仅在 Unix 运行")
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	t.Setenv("BRAIN_CONFIG", cfgPath)

	// 1. VISUAL 带参数（如 `editor.sh --append-tag`），且初始 config.yaml 不存在时自动创建默认模板后打开
	edScript := filepath.Join(dir, "editor.sh")
	scriptBody := "#!/bin/sh\nif [ \"$1\" != \"--append-tag\" ]; then exit 2; fi\necho \"# edited-with-flag\" >> \"$2\"\n"
	if err := os.WriteFile(edScript, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", edScript+" --append-tag")
	t.Setenv("EDITOR", "")

	code, _, errStr := runBrain("config", "edit")
	if code != 0 {
		t.Fatalf("VISUAL 带参数时 config edit 失败: code=%d stderr=%s", code, errStr)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "base_url:") || !strings.Contains(string(got), "# edited-with-flag") {
		t.Fatalf("config.yaml 应含默认模板与编辑器追加标记:\n%s", got)
	}

	// 2. 编辑器路径自身含空格且未加引号（整串 LookPath 快路径，不应按空格拆碎）
	spacedDir := filepath.Join(dir, "dir with spaces")
	if err := os.MkdirAll(spacedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spacedEd := filepath.Join(spacedDir, "my editor")
	spacedBody := "#!/bin/sh\necho \"# edited-from-spaced-path\" >> \"$1\"\n"
	if err := os.WriteFile(spacedEd, []byte(spacedBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", spacedEd)

	code, _, errStr = runBrain("config", "edit")
	if code != 0 {
		t.Fatalf("含空格编辑器路径 config edit 失败: code=%d stderr=%s", code, errStr)
	}
	got2, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(got2), "# edited-from-spaced-path") {
		t.Fatalf("含空格编辑器未生效:\n%s", got2)
	}
}
