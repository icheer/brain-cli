package cmd

// brain update（M13）：委托式自升级。
// Go 侧只做发现/查版本/派活/收尾——下载、布局检测、SKILL.md 与安装器同步全部
// 委托技能目录里的 install.sh（重写即双实现漂移）。设计详见 DESIGN.md 第 0 节
// 「自升级 update」行与 3.7 节。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/i18n"
)

type updateOptions struct {
	check    bool
	skillDir string
}

var updateOpts updateOptions

// 可注入点（测试用）：版本查询端点、当前版本、实路径探测、家目录。
var (
	updateAPIBase    = "https://api.github.com/repos/icheer/brain-cli/releases/latest"
	updateVersionFn  = func() string { return version }
	updateExePathFn  = os.Executable
	updateHomeDirFn  = os.UserHomeDir
)

var updateCmd = &cobra.Command{
	Use:   "update [--check] [--skill-dir <dir>]",
	Short: i18n.T("就地升级：找到技能目录后委托 install.sh 更新二进制/SKILL.md/安装器自身",
		"self-upgrade: locate the skill dir and delegate to install.sh (binary / SKILL.md / installer)"),
	Long: i18n.T(`就地升级 brain（无需手动定位技能目录）：
  1. 发现技能目录：--skill-dir 显式指定，或从自身实路径（解析软链）向上找
     SKILL.md+install.sh，或探测 ~/.claude|~/.codex|~/.agents/skills/brain
  2. 向 GitHub 查询最新版本并与当前比对（--check 只查不更）
  3. 委托技能目录的 install.sh 完成升级（其形态检测顺带处理全局软链/双副本，
     并同步 SKILL.md 与安装器自身）；退出码原样转发

全局裸装（--global）与 go install 布局无技能目录可委托：按 stderr 指引自管
（重跑 install.sh 或 go install ...@latest）。
退出码：0 成功或已是最新；1 未发现技能目录/派生失败；5 版本查询网络失败；
install.sh 自身的退出码原样转发。`, `Upgrade brain in place (no need to locate the skill dir yourself):
  1. Find the skill dir: --skill-dir explicitly, or walk up from this binary's
     real path (symlinks resolved) looking for SKILL.md+install.sh, or probe
     ~/.claude|~/.codex|~/.agents/skills/brain
  2. Check the latest release on GitHub against the current version
     (--check only checks, never downloads)
  3. Delegate to install.sh in the skill dir (its layout detection also handles
     global symlinks / dual copies, and syncs SKILL.md and the installer
     itself); its exit code is relayed as-is

Global-only (--global) and go install layouts have no skill dir to delegate to:
follow the stderr hint (re-run install.sh, or go install ...@latest).
Exit codes: 0 success or already latest; 1 skill dir not found / spawn failed;
5 version-check network failure; install.sh's own exit code is relayed as-is.`),
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().BoolVar(&updateOpts.check, "check", false,
		i18n.T("只查询并报告版本，不下载不升级", "report current/latest versions only; no download, no upgrade"))
	updateCmd.Flags().StringVar(&updateOpts.skillDir, "skill-dir", "",
		i18n.T("显式指定技能目录（须含 install.sh）", "explicit skill dir (must contain install.sh)"))
}

// runUpdate 主流程。所有 stderr 文案走 i18n；stdout 只输出结果行（管理命令同 ping）。
func runUpdate(cmd *cobra.Command, args []string) error {
	// 兜底清尸：上次 update 极端情况下残留的 .old（此刻未被运行中实例锁定即可删）
	exeReal, _ := resolvedExePath()
	if exeReal != "" {
		_ = os.Remove(exeReal + oldSuffix())
	}

	skillDir, err := findSkillDir(updateOpts.skillDir)
	if err != nil {
		if updateOpts.skillDir != "" {
			return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Usage, "%s",
				i18n.T("--skill-dir 指定的目录不含 install.sh：", "--skill-dir points to a dir without install.sh: ")+updateOpts.skillDir)
		}
		return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Usage, "%s", i18n.T("未发现技能目录（需含 SKILL.md 与 install.sh）。\n"+
				"指引：--skill-dir 显式指定；或重跑一次技能目录里的 install.sh（bash <skill>/install.sh）；\n"+
				"go install 用户请自行 go install github.com/icheer/brain-cli@latest",
				"skill dir not found (needs SKILL.md and install.sh).\n"+
					"Hints: pass --skill-dir explicitly; or re-run install.sh from your skill dir (bash <skill>/install.sh);\n"+
					"go install users: go install github.com/icheer/brain-cli@latest yourself"))
	}

	latest, err := fetchLatestTag()
	if err != nil {
		return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Upstream, "%s",
			i18n.T("查询最新版本失败：", "failed to fetch latest version: ")+err.Error())
	}
	cur := updateVersionFn()

	if updateOpts.check {
		ver := i18n.T("已是最新", "up to date")
		if cmpVersion(cur, latest) != 0 {
			ver = i18n.T("有新版本", "update available")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "current %s | latest %s | %s | skill %s\n", cur, latest, ver, skillDir)
		return nil
	}
	if cmpVersion(cur, latest) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), i18n.T("brain %s 已是最新版本（技能目录 %s）\n",
			"brain %s is already the latest release (skill dir %s)\n"), cur, skillDir)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), i18n.T("发现技能目录 %s，委托 install.sh 升级 %s → %s\n",
		"skill dir %s found; delegating to install.sh for %s -> %s\n"), skillDir, cur, latest)

	// 自改名 .old：install.sh 用 cp 覆写目标——Unix 上运行中二进制被 cp 撞 ETXTBSY、
	// Windows 撞文件锁；改名运行中的 exe 两平台均合法，cp 落全新 inode 畅通。
	renamed := false
	if exeReal != "" {
		if err := os.Rename(exeReal, exeReal+oldSuffix()); err == nil {
			renamed = true
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), i18n.T("警告: 自改名 .old 失败（%v），继续委托——若安装器覆写失败将报错退出\n",
				"warning: self-rename to .old failed (%v); delegating anyway — the installer will error out if it cannot overwrite\n"), err)
		}
	}

	inst := exec.Command("bash", filepath.Join(skillDir, "install.sh"))
	inst.Stdin, inst.Stdout, inst.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := inst.Run()

	if runErr != nil {
		// 回滚：子进程失败且原位没有新文件时，把改名让位的自己搬回去，
		// 避免一次失败的升级留下"没有 brain 可执行文件"的系统。
		if renamed {
			if _, statErr := os.Stat(exeReal); statErr != nil && os.Rename(exeReal+oldSuffix(), exeReal) == nil {
				fmt.Fprintf(cmd.ErrOrStderr(), i18n.T("已回滚：%s 复位（升级未完成）\n",
					"rolled back: %s restored (upgrade incomplete)\n"), exeReal)
			}
		}
		if ee, ok := runErr.(*exec.ExitError); ok {
			// install.sh 的退出码原样转发（其失败语义由其自身定义）；信号致死无码 → 1
			code := ee.ExitCode()
			if code <= 0 {
				code = exitcode.Usage
			}
			return exitcode.Fail(cmd.ErrOrStderr(), code, "%s",
				i18n.T("install.sh 升级失败：", "install.sh upgrade failed: ")+runErr.Error())
		}
		return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Usage, "%s",
			i18n.T("派生 install.sh 失败（需要 bash 于 PATH 中）：", "failed to spawn install.sh (bash required on PATH): ")+runErr.Error())
	}

	if renamed {
		cleanupOld(exeReal)
	}
	fmt.Fprintf(cmd.OutOrStdout(), i18n.T("brain %s → %s 完成\n", "brain %s -> %s done\n"), cur, latest)
	return nil
}

// resolvedExePath 返回自身实路径（解析软链；失败时尽力返回原始路径，全败返回空）。
func resolvedExePath() (string, error) {
	p, err := updateExePathFn()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, nil
	}
	return p, nil
}

// findSkillDir 发现链：--skill-dir → 实路径向上 ≤3 级 → 家目录候选表。
func findSkillDir(flagDir string) (string, error) {
	isSkill := func(dir string) bool {
		return fileExists(filepath.Join(dir, "SKILL.md")) && fileExists(filepath.Join(dir, "install.sh"))
	}
	hasInst := func(dir string) bool {
		return fileExists(filepath.Join(dir, "install.sh"))
	}

	if flagDir != "" {
		abs, err := filepath.Abs(flagDir)
		if err != nil || !hasInst(abs) {
			return "", fmt.Errorf("--skill-dir 无 install.sh: %s", flagDir)
		}
		return abs, nil
	}
	if p, err := resolvedExePath(); err == nil && p != "" {
		d := filepath.Dir(p)
		for i := 0; i < 3; i++ {
			if isSkill(d) {
				return d, nil
			}
			d = filepath.Dir(d)
		}
	}
	if home, err := updateHomeDirFn(); err == nil {
		for _, c := range []string{".claude/skills/brain", ".codex/skills/brain", ".agents/skills/brain"} {
			p := filepath.Join(home, filepath.FromSlash(c))
			if isSkill(p) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("skill dir not found")
}

// fetchLatestTag 查询 GitHub 最新 Release tag（429/5xx/网络错误重试 ≤3，1s/2s 退避）。
func fetchLatestTag() (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		req, err := http.NewRequest("GET", updateAPIBase, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "brain-updater")
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, body)
		}
		var payload struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.TagName == "" {
			return "", fmt.Errorf("invalid response: %.200s", body)
		}
		return payload.TagName, nil
	}
	return "", lastErr
}

// cmpVersion 语义化比较（剥 v 前缀、按点分数值比，缺位补 0）：-1/<0 ==0 >0。
func cmpVersion(a, b string) int {
	norm := func(s string) []int {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		var out []int
		for _, f := range strings.Split(s, ".") {
			n := 0
			for _, ch := range f {
				if ch < '0' || ch > '9' {
					break
				}
				n = n*10 + int(ch-'0')
			}
			out = append(out, n)
		}
		return out
	}
	x, y := norm(a), norm(b)
	for len(x) < 3 {
		x = append(x, 0)
	}
	for len(y) < 3 {
		y = append(y, 0)
	}
	for i := range x {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func oldSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe.old"
	}
	return ".old"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// cleanupOld 清理改名让位的旧二进制：Unix 立即删（unlink 运行中二进制合法，
// 零残留零延迟进程）；Windows 运行中不可删，派生 cmd 延迟清尸（系统自带零依赖，
// ping -n 3 ≈ 2s 覆盖本进程退出），失败则留给下次 update 启动时的兜底清扫。
func cleanupOld(exeReal string) {
	old := exeReal + oldSuffix()
	if runtime.GOOS == "windows" {
		_ = exec.Command("cmd", "/c",
			fmt.Sprintf(`ping -n 3 127.0.0.1 >nul & del /f /q "%s"`, old)).Start()
		return
	}
	_ = os.Remove(old)
}
