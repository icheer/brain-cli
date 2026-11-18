package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSkillFrontmatterAndCommandDrift 守护 skills/brain/SKILL.md 的触发契约与代码同构（CUJ J17 扩展）：
// 1. YAML frontmatter 合法，name=brain，description 兼具中英文触发词与负向边界；
// 2. 不再出现误回退父目录的 $(dirname "$SKILL_DIR")/install.sh；
// 3. 代码块中出现的全部 `brain <subcommand>` 在 newRootCmd() 中真实注册，防文档与 CLI 漂移。
func TestSkillFrontmatterAndCommandDrift(t *testing.T) {
	p := filepath.Join("..", "skills", "brain", "SKILL.md")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 SKILL.md 失败: %v", err)
	}
	s := string(raw)

	if !strings.HasPrefix(s, "---\n") {
		t.Fatal("SKILL.md 缺少起始 YAML frontmatter 分隔线 ---")
	}
	rest := s[4:]
	endIdx := strings.Index(rest, "\n---\n")
	if endIdx < 0 {
		t.Fatal("SKILL.md 缺少闭合 YAML frontmatter 分隔线 ---")
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(rest[:endIdx]), &fm); err != nil {
		t.Fatalf("解析 SKILL.md frontmatter YAML 失败: %v", err)
	}
	if fm.Name != "brain" {
		t.Fatalf("frontmatter name=%q, want \"brain\"", fm.Name)
	}
	if chineseRatio(fm.Description) < 0.05 || !strings.Contains(fm.Description, "Use for") {
		t.Fatalf("frontmatter description 应兼具中英文触发词: %q", fm.Description)
	}
	for _, kw := range []string{"ask", "compare", "council", "decide", "draw"} {
		if !strings.Contains(fm.Description, kw) {
			t.Errorf("frontmatter description 缺少核心原语名 %q", kw)
		}
	}

	if strings.Contains(s, `$(dirname "$SKILL_DIR")`) {
		t.Fatal("SKILL.md 不得包含指向父目录的 $(dirname \"$SKILL_DIR\") 错误路径")
	}
	for _, section := range []string{"## council 速览", "--blind", "brain update", "--uninstall"} {
		if !strings.Contains(s, section) {
			t.Errorf("SKILL.md 缺少关键章节或用法标记 %q", section)
		}
	}

	// 收集根命令已注册的全部子命令名（含内置 help）
	root := newRootCmd()
	known := map[string]bool{"help": true}
	for _, sub := range root.Commands() {
		known[sub.Name()] = true
		for _, a := range sub.Aliases {
			known[a] = true
		}
	}

	// 扫描 bash 代码块中所有调用点 `brain <token>`，确保无幽灵子命令
	inBash := false
	reCall := regexp.MustCompile(`(?:^|[|;&(]\s*|\$\(\s*)brain\s+([a-zA-Z0-9_-]+)`)
	for lineNo, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```bash") {
			inBash = true
			continue
		}
		if inBash && strings.HasPrefix(trimmed, "```") {
			inBash = false
			continue
		}
		if !inBash {
			continue
		}
		// 剥除行内注释再匹配，避免注释中自然语言紧跟 brain 误判
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = line[:idx]
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, m := range reCall.FindAllStringSubmatch(line, -1) {
			tok := m[1]
			if strings.HasPrefix(tok, "-") {
				continue
			}
			if !known[tok] {
				t.Errorf("SKILL.md:%d 引用了未注册的子命令 `brain %s`", lineNo+1, tok)
			}
		}
	}
}
