package cmd

// update（M13）测试：发现链三层、版本比对、--check 零下载、委托与退出码转发、
// .old 改名/回滚/清尸、无技能目录指引。全部经可注入点驱动，零真实网络。

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeUpdateEnv 搭一个假世界：技能目录（含可执行脚本 install.sh）、实路径注入、
// GitHub API 假端点（返回指定 tag）、家目录注入（隔离真机 ~/.claude）。
type fakeUpdateEnv struct {
	skillDir string
	binPath  string
	api      *httptest.Server
	oldHome  func() (string, error)
	oldExe   func() (string, error)
	oldVer   func() string
	oldBase  string
}

func setupUpdateEnv(t *testing.T, tag string, installerScript string) *fakeUpdateEnv {
	t.Helper()
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "brain")
	bin := filepath.Join(skill, "bin")
	for _, d := range []string{bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFileT(t, filepath.Join(skill, "SKILL.md"), "# brain skill\n")
	writeFileT(t, filepath.Join(skill, "install.sh"), installerScript)
	binPath := filepath.Join(bin, "brain")
	writeFileT(t, binPath, "#!/bin/sh\nexit 0\n")

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	}))
	t.Cleanup(api.Close)

	env := &fakeUpdateEnv{skillDir: skill, binPath: binPath, api: api}
	env.oldHome, env.oldExe, env.oldVer, env.oldBase = updateHomeDirFn, updateExePathFn, updateVersionFn, updateAPIBase
	homeStub := filepath.Join(dir, "home")
	if err := os.MkdirAll(homeStub, 0o755); err != nil {
		t.Fatal(err)
	}
	updateHomeDirFn = func() (string, error) { return homeStub, nil }
	updateAPIBase = api.URL
	t.Cleanup(func() {
		updateHomeDirFn, updateExePathFn, updateVersionFn, updateAPIBase = env.oldHome, env.oldExe, env.oldVer, env.oldBase
	})
	return env
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func resetUpdateOpts() { updateOpts = updateOptions{} }

func TestUpdateDiscoveryWalkUp(t *testing.T) {
	env := setupUpdateEnv(t, "v9.9.9", "#!/bin/bash\nexit 0\n")
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	updateVersionFn = func() string { return "9.9.9" }
	resetUpdateOpts()

	// 已是最新：发现走实路径向上两级（bin → skill），不触网下载
	code, out, errStr := runBrain("update")
	if code != 0 || !strings.Contains(out, "9.9.9") || !strings.Contains(out, env.skillDir) {
		t.Fatalf("code=%d out=%s err=%s", code, out, errStr)
	}
}

func TestUpdateDiscoveryCandidates(t *testing.T) {
	setupUpdateEnv(t, "v9.9.9", "#!/bin/bash\nexit 0\n")
	// 实路径在无关目录（模拟全局副本入口），候选表须命中 ~/.codex/skills/brain
	home, _ := updateHomeDirFn()
	codexSkill := filepath.Join(home, ".codex", "skills", "brain")
	if err := os.MkdirAll(filepath.Join(codexSkill, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(codexSkill, "SKILL.md"), "# codex\n")
	writeFileT(t, filepath.Join(codexSkill, "install.sh"), "#!/bin/bash\nexit 0\n")
	updateExePathFn = func() (string, error) { return filepath.Join(t.TempDir(), "brain"), nil }
	updateVersionFn = func() string { return "9.9.9" }
	resetUpdateOpts()

	code, out, _ := runBrain("update")
	if code != 0 || !strings.Contains(out, codexSkill) {
		t.Fatalf("候选表未命中 ~/.codex/skills/brain: code=%d out=%s", code, out)
	}
}

func TestUpdateDiscoveryNotFound(t *testing.T) {
	setupUpdateEnv(t, "v9.9.9", "#!/bin/bash\nexit 0\n")
	updateExePathFn = func() (string, error) { return filepath.Join(t.TempDir(), "brain"), nil }
	resetUpdateOpts()

	code, _, errStr := runBrain("update")
	if code != 1 || !strings.Contains(errStr, "install.sh") {
		t.Fatalf("未发现技能目录应 exit 1 且带指引: code=%d err=%s", code, errStr)
	}
}

func TestUpdateCheckModeZeroDownload(t *testing.T) {
	// install.sh 一旦被执行就写标记；--check 后标记必须不存在
	marker := filepath.Join(t.TempDir(), "ran")
	env := setupUpdateEnv(t, "v0.13.0", fmt.Sprintf("#!/bin/bash\ntouch %q\nexit 0\n", marker))
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	updateVersionFn = func() string { return "0.12.1" }
	resetUpdateOpts()

	code, out, errStr := runBrain("update", "--check")
	if code != 0 || !strings.Contains(out, "0.12.1") || !strings.Contains(out, "v0.13.0") {
		t.Fatalf("code=%d out=%s err=%s", code, out, errStr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("--check 不应执行 install.sh")
	}
}

func TestUpdateDelegatesAndCleansOld(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	env := setupUpdateEnv(t, "v0.13.0", fmt.Sprintf("#!/bin/bash\necho ran > %q\nexit 0\n", marker))
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	updateVersionFn = func() string { return "0.12.1" }
	resetUpdateOpts()

	code, out, errStr := runBrain("update")
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out, errStr)
	}
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != "ran" {
		t.Fatalf("install.sh 未被执行: %v", err)
	}
	// Unix 立即清尸：不应残留 brain.old
	if _, err := os.Stat(env.binPath + ".old"); !os.IsNotExist(err) {
		t.Fatal("升级后不应残留 .old")
	}
	// 新二进制应已落位（假安装器模拟 cp：把 skill 里随便一个文件当产物——这里验证
	// 走 delegate 路径本身已由 marker 证明，bin 落位由真实 install.sh 负责，不在此断言）
}

func TestUpdateRelayExitCodeAndRollback(t *testing.T) {
	env := setupUpdateEnv(t, "v0.13.0", "#!/bin/bash\nexit 3\n")
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	updateVersionFn = func() string { return "0.12.1" }
	resetUpdateOpts()

	code, out, errStr := runBrain("update")
	if code != 3 {
		t.Fatalf("install.sh 退出码应原样转发（want 3, got %d）: out=%s err=%s", code, out, errStr)
	}
	// 失败且原位无新文件 → 回滚：二进制必须复位、不残留 .old
	if _, err := os.Stat(env.binPath); err != nil {
		t.Fatalf("失败后二进制未回滚: %v", err)
	}
	if _, err := os.Stat(env.binPath + ".old"); !os.IsNotExist(err) {
		t.Fatal("回滚后不应残留 .old")
	}
	if !strings.Contains(errStr, "install.sh") {
		t.Fatalf("stderr 应指明 install.sh 失败: %s", errStr)
	}
}

func TestUpdateStaleOldSweep(t *testing.T) {
	env := setupUpdateEnv(t, "v9.9.9", "#!/bin/bash\nexit 0\n")
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	updateVersionFn = func() string { return "9.9.9" }
	resetUpdateOpts()
	// 预置上次极端情况的残留
	writeFileT(t, env.binPath+".old", "stale\n")

	code, _, _ := runBrain("update", "--check")
	if code != 0 {
		t.Fatal("check should pass")
	}
	if _, err := os.Stat(env.binPath + ".old"); !os.IsNotExist(err) {
		t.Fatal("启动兜底应清掉残留 .old")
	}
}

func TestUpdateSkillDirFlag(t *testing.T) {
	env := setupUpdateEnv(t, "v9.9.9", "#!/bin/bash\nexit 0\n")
	// 实路径与候选表全部落空，仅 --skill-dir 显式命中
	updateExePathFn = func() (string, error) { return filepath.Join(t.TempDir(), "brain"), nil }
	updateVersionFn = func() string { return "9.9.9" }
	resetUpdateOpts()

	code, out, errStr := runBrain("update", "--skill-dir", env.skillDir)
	if code != 0 || !strings.Contains(out, env.skillDir) {
		t.Fatalf("code=%d out=%s err=%s", code, out, errStr)
	}
	// 显式目录缺 install.sh → exit 1
	resetUpdateOpts()
	empty := t.TempDir()
	code, _, errStr = runBrain("update", "--skill-dir", empty)
	if code != 1 {
		t.Fatalf("缺 install.sh 应 exit 1: code=%d err=%s", code, errStr)
	}
}

func TestUpdateVersionFetchFail(t *testing.T) {
	env := setupUpdateEnv(t, "", "#!/bin/bash\nexit 0\n")
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	resetUpdateOpts()
	// 恒 500：重试 ≤3 后 exit 5
	env.api.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})

	code, _, errStr := runBrain("update")
	if code != 5 || !strings.Contains(errStr, "HTTP 500") {
		t.Fatalf("网络失败应 exit 5: code=%d err=%s", code, errStr)
	}
}

func TestCmpVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.12.1", "v0.12.1", 0},
		{"0.12.1", "v0.13.0", -1},
		{"0.13.0", "v0.12.9", 1},
		{"dev", "v0.13.0", -1}, // dev < 任意数字版（d 解析为 0 前缀段）
		{"0.12", "v0.12.0", 0},
		{"1.0.0", "v0.99.99", 1},
	}
	for _, c := range cases {
		if got := cmpVersion(c.a, c.b); got != c.want {
			t.Errorf("cmpVersion(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

// 本地预发布版本高于远端 Release 时不得误判为"有新版本"或反向降级。
func TestUpdateNoDowngradeWhenAhead(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	env := setupUpdateEnv(t, "v0.13.0", fmt.Sprintf("#!/bin/bash\ntouch %q\nexit 0\n", marker))
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	updateVersionFn = func() string { return "0.14.0" }
	resetUpdateOpts()

	code, out, _ := runBrain("update", "--check")
	if code != 0 || !strings.Contains(out, "已是最新") {
		t.Fatalf("高于远端版本时 --check 应报已是最新: code=%d out=%s", code, out)
	}
	resetUpdateOpts()
	code, out, _ = runBrain("update")
	if code != 0 || !strings.Contains(out, "已是最新") {
		t.Fatalf("高于远端版本时不应降级: code=%d out=%s", code, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("高于远端版本时不应执行 install.sh")
	}
}

// 运行中的二进制位于无关路径（如 go install 的 ~/go/bin/brain）且命中候选技能目录时，
// install.sh 只更新技能目录，update 必须保留原二进制并给出告警（严禁误删）。
func TestUpdatePreservesUnrelatedBinary(t *testing.T) {
	env := setupUpdateEnv(t, "v0.13.0", "#!/bin/bash\necho new > \"$(dirname \"$0\")/bin/brain\"\nexit 0\n")
	home, _ := updateHomeDirFn()
	claudeSkill := filepath.Join(home, ".claude", "skills", "brain")
	if err := os.MkdirAll(filepath.Join(claudeSkill, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(claudeSkill, "SKILL.md"), "# claude\n")
	writeFileT(t, filepath.Join(claudeSkill, "install.sh"), "#!/bin/bash\necho new > \"$(dirname \"$0\")/bin/brain\"\nexit 0\n")

	goBin := filepath.Join(t.TempDir(), "go", "bin", "brain")
	if err := os.MkdirAll(filepath.Dir(goBin), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, goBin, "original-go-install-binary")
	_ = env

	updateExePathFn = func() (string, error) { return goBin, nil }
	updateVersionFn = func() string { return "0.12.0" }
	resetUpdateOpts()

	code, _, errStr := runBrain("update")
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errStr)
	}
	got, err := os.ReadFile(goBin)
	if err != nil || string(got) != "original-go-install-binary" {
		t.Fatalf("go install 二进制被破坏或误删: err=%v content=%q", err, got)
	}
	if _, err := os.Stat(goBin + ".old"); !os.IsNotExist(err) {
		t.Fatal("不应残留 .old")
	}
	if !strings.Contains(errStr, "警告") {
		t.Fatalf("stderr 应告警当前二进制不在 install.sh 更新目标内: %q", errStr)
	}
}

// 升级途中被中断（ctx 取消 / SIGINT）时，必须把 .old 回滚为原二进制并返回 130。
func TestUpdateInterruptRollsBackOld(t *testing.T) {
	// 用 bash 内建 read 挂起于私有 FIFO，避免派生外部 sleep 子进程在 SIGKILL 后孤儿化拖慢测试
	env := setupUpdateEnv(t, "v0.13.0", "#!/bin/bash\nwhile :; do :; done\n")
	updateExePathFn = func() (string, error) { return env.binPath, nil }
	updateVersionFn = func() string { return "0.12.0" }
	resetUpdateOpts()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	rootCmd.SetContext(ctx)
	t.Cleanup(func() { rootCmd.SetContext(context.Background()) })

	code, _, errStr := runBrain("update")
	if code != 130 {
		t.Fatalf("中断应返回 130，实际 %d (stderr=%s)", code, errStr)
	}
	if _, err := os.Stat(env.binPath); err != nil {
		t.Fatalf("中断后原二进制未回滚复位: %v", err)
	}
	if _, err := os.Stat(env.binPath + ".old"); !os.IsNotExist(err) {
		t.Fatal("中断回滚后不应残留 .old")
	}
}

// 沙盒集成测试真实 skills/brain/install.sh：
// 1) SHA256SUMS 缺条目必须 Fail-Closed（exit 1）；
// 2) SHA256 不匹配必须 exit 1；
// 3) 合法包 + .old 形态检测（模拟 brain update 自改名全局副本后委托）正确更新双副本。
func TestInstallShSHA256FailClosedAndOldLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("沙盒 fake curl/uname 测试针对 Unix bash 环境")
	}
	realScript, err := filepath.Abs(filepath.Join("..", "skills", "brain", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	scriptBytes, err := os.ReadFile(realScript)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skill")
	globalDir := filepath.Join(dir, "global-bin")
	fakeBin := filepath.Join(dir, "fake-bin")
	srvDir := filepath.Join(dir, "srv")
	for _, d := range []string{skillDir, globalDir, fakeBin, srvDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFileT(t, filepath.Join(skillDir, "SKILL.md"), "# old skill\n")
	writeFileT(t, filepath.Join(skillDir, "install.sh"), string(scriptBytes))

	// 构造真实 tar.gz 归档（含可执行 brain）
	archName := fmt.Sprintf("brain_0.99.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	tarPath := filepath.Join(srvDir, archName)
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	binContent := []byte("#!/bin/sh\necho 'brain 0.99.0'\n")
	if err := tw.WriteHeader(&tar.Header{Name: "brain", Mode: 0o755, Size: int64(len(binContent))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(binContent)
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()

	rawTar, _ := os.ReadFile(tarPath)
	sum := sha256.Sum256(rawTar)
	goodHash := hex.EncodeToString(sum[:])

	// fake curl：按 URL 末尾提供 latest JSON / tar.gz / SHA256SUMS
	sumsFile := filepath.Join(srvDir, "SHA256SUMS")
	fakeCurl := fmt.Sprintf(`#!/bin/bash
set -e
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */releases/latest)
    printf '{"tag_name":"v0.99.0"}'
    ;;
  */SHA256SUMS)
    cp %q "$out"
    ;;
  *.tar.gz)
    cp %q "$out"
    ;;
esac
`, sumsFile, tarPath)
	writeFileT(t, filepath.Join(fakeBin, "curl"), fakeCurl)

	runInstaller := func() (int, string) {
		cmd := exec.Command("bash", filepath.Join(skillDir, "install.sh"))
		cmd.Env = append(os.Environ(),
			"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"BRAIN_GLOBAL_BIN="+globalDir,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), string(out)
			}
			return 1, err.Error()
		}
		return 0, string(out)
	}

	// 1. SHA256SUMS 不含目标文件 → 必须 Fail-Closed 退出 1（旧版会警告后跳过校验继续安装）
	_ = os.WriteFile(sumsFile, []byte(goodHash+"  other_file.tar.gz\n"), 0o644)
	if code, out := runInstaller(); code == 0 {
		t.Fatalf("SHA256SUMS 缺条目时必须拒绝安装: out=%s", out)
	}

	// 2. SHA256 摘要不匹配 → 必须退出 1
	_ = os.WriteFile(sumsFile, []byte(strings.Repeat("0", 64)+"  "+archName+"\n"), 0o644)
	if code, out := runInstaller(); code == 0 {
		t.Fatalf("SHA256 不匹配时必须拒绝安装: out=%s", out)
	}

	// 3. 模拟 brain update 改名全局二进制为 .old 且技能目录已有二进制 → 应识别为 both 两处均更新
	if err := os.MkdirAll(filepath.Join(skillDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(skillDir, "bin", "brain"), "#!/bin/sh\necho old\n")
	writeFileT(t, filepath.Join(globalDir, "brain.old"), "#!/bin/sh\necho old\n")
	_ = os.WriteFile(sumsFile, []byte(goodHash+"  "+archName+"\n"), 0o644)

	if code, out := runInstaller(); code != 0 {
		t.Fatalf("合法 SHA256 安装应成功: code=%d out=%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(globalDir, "brain")); err != nil {
		t.Fatalf(".old 存在时 install.sh 应识别并重建全局二进制: %v", err)
	}
}
