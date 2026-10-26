package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
)

var initOpts struct {
	baseURL      string
	defaultModel string
	force        bool
}

var initCmd = &cobra.Command{
	Use:   "init [--base-url <url>] [--default-model <别名>]",
	Short: "交互式引导写入 config.yaml（首次配置）",
	Long: `首次使用引导：依次询问 base_url、api_key、default_model，写入 config.yaml（0600）。
已提供同名 flag 的项跳过询问，便于脚本化：

  brain init --base-url https://gw.example.com/v1 --default-model fast

base_url 只要是 OpenAI 兼容 API 即可，不限于网关（如 https://api.openai.com/v1）。
api_key 刻意不设 flag（避免进入 shell history / ps 输出）：
交互输入，或留空后由 BRAIN_API_KEY 环境变量提供。
随后可选生成 aliases 段（fast/reason/coding/vision，拉网关模型清单做选单）；
覆盖重建时原有别名默认保留。`,
	RunE: runInit,
}

func init() {
	initCmd.Flags().StringVar(&initOpts.baseURL, "base-url", "", "网关地址（缺省进入交互询问）")
	initCmd.Flags().StringVar(&initOpts.defaultModel, "default-model", "", "默认模型别名（缺省进入交互询问，可留空跳过）")
	initCmd.Flags().BoolVar(&initOpts.force, "force", false, "已存在 config.yaml 时跳过覆盖确认")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, _ []string) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	p, err := config.Path()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	in := bufio.NewReader(stdin)
	ask := func(prompt string) string {
		fmt.Fprint(stdout, prompt)
		line, _ := in.ReadString('\n')
		return strings.TrimSpace(line)
	}

	if _, err := os.Stat(p); err == nil && !initOpts.force {
		if ans := ask(fmt.Sprintf("config.yaml 已存在（%s），覆盖重建？[y/N] ", p)); !strings.EqualFold(ans, "y") {
			fmt.Fprintln(stderr, "brain: 已取消（未做任何修改）")
			return nil
		}
	}

	// 覆盖重建时保留原有别名（单文件化后别名就在 config.yaml 里，不能跟着重建丢掉）
	oldAliases := map[string]config.Alias{}
	if old, _, err := config.Load(); err == nil {
		oldAliases = old.Aliases
	}
	if len(oldAliases) > 0 {
		fmt.Fprintf(stdout, "将保留原有别名 %d 个（brain config edit 可修改）\n", len(oldAliases))
	}

	cfg := config.Default()
	cfg.Aliases = oldAliases

	baseURL := initOpts.baseURL
	if baseURL == "" {
		baseURL = ask("base_url（任意 OpenAI 兼容 API，如 https://api.openai.com/v1）: ")
	}
	if baseURL == "" || !strings.HasPrefix(baseURL, "http") {
		return exitcode.Fail(stderr, exitcode.Usage, "base_url 不能为空且须以 http(s):// 开头")
	}
	cfg.BaseURL = baseURL

	// api_key 不设 flag（DESIGN.md 2.4）；留空则回落 BRAIN_API_KEY 环境变量
	apiKey := ask("api_key（输入会回显；留空则使用 BRAIN_API_KEY 环境变量）: ")
	if apiKey == "" {
		apiKey = os.Getenv("BRAIN_API_KEY")
		if apiKey != "" {
			fmt.Fprintln(stdout, "已使用 BRAIN_API_KEY 环境变量。")
		}
	}
	if apiKey == "" {
		return exitcode.Fail(stderr, exitcode.Usage, "缺少 api_key：请交互输入，或设置 BRAIN_API_KEY 环境变量")
	}
	cfg.APIKey = apiKey

	if initOpts.defaultModel == "" {
		cfg.DefaultModel = ask("default_model（别名或模型 ID，可留空跳过）: ")
	} else {
		cfg.DefaultModel = initOpts.defaultModel
	}

	// 别名生成：SKILL.md 模板依赖 fast/reason/coding/vision，缺失时 -m 别名会按
	// 字面模型 ID 发出而 404（最小安装的第一跳断点，2026-10-25 外脑评审证实）
	regen := len(oldAliases) == 0 // 无别名默认生成；已有别名默认保留（不重建）
	if len(oldAliases) > 0 {
		regen = strings.EqualFold(ask("重新生成别名映射？[y/N] "), "y")
	} else if strings.EqualFold(ask("生成别名映射 fast/reason/coding/vision？[Y/n] "), "n") {
		regen = false
		fmt.Fprintln(stdout, "已跳过（-m 按字面模型 ID 处理；别名示例见仓库 models.example.yaml）")
	}
	if regen {
		if err := initAliases(cmd.Context(), in, stdout, stderr, &cfg); err != nil {
			return err
		}
	}

	if err := config.Save(cfg); err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	fmt.Fprintf(stdout, "已写入 %s（权限 0600）\n", p)
	fmt.Fprintln(stdout, "下一步：brain ping 自检（brain ping -m <别名> 可逐模型探测链可用性）")
	return nil
}

// aliasDefs 标准别名及场景标注（与 models.example.yaml / SKILL.md 模板一致）。
var aliasDefs = []struct{ name, use string }{
	{"fast", "快速问答、简单总结"},
	{"reason", "复杂推理、方案交叉验证"},
	{"coding", "代码审查、重构建议"},
	{"vision", "图表/截图解析（需多模态模型）"},
}

// initAliases 交互生成别名段（写入 cfg.Aliases，由调用方统一 Save）：
// 拉取 /v1/models 做选单（不可达时允许直接输入 ID），每个别名可输序号或完整 ID，
// 多个（逗号/空格分隔）组成 fallback 链，回车跳过。
// in 复用 runInit 的 bufio.Reader——对同一底层 stdin 新建 Reader 会因缓冲已耗尽而瞬间 EOF。
func initAliases(ctx context.Context, in *bufio.Reader, stdout, stderr io.Writer, cfg *config.Config) error {
	askLine := func(prompt string) string {
		fmt.Fprint(stdout, prompt)
		line, _ := in.ReadString('\n')
		return strings.TrimSpace(line)
	}

	var list []string
	g := gateway.New(cfg.BaseURL, cfg.APIKey, 10*time.Second)
	if l, err := g.ListModels(ctx); err != nil {
		fmt.Fprintf(stdout, "网关模型清单不可达（%v）：可直接粘贴完整模型 ID\n", err)
	} else {
		list = l
		const showMax = 40
		fmt.Fprintf(stdout, "网关可用模型（%d 个，仅列前 %d；序号或完整 ID 均可）：\n", len(list), showMax)
		for i, m := range list {
			if i >= showMax {
				fmt.Fprintf(stdout, "  …（其余 %d 个见 brain models）\n", len(list)-showMax)
				break
			}
			fmt.Fprintf(stdout, "  %2d. %s\n", i+1, m)
		}
	}

	aliases := map[string]config.Alias{}
	for _, d := range aliasDefs {
		input := askLine(fmt.Sprintf("%s（%s）→ ", d.name, d.use))
		if models := pickModels(input, list); len(models) > 0 {
			aliases[d.name] = config.Alias{Models: models, Use: d.use}
		}
	}
	if len(aliases) == 0 {
		fmt.Fprintln(stdout, "未选择任何别名（-m 按字面模型 ID 处理）")
		return nil
	}
	cfg.Aliases = aliases
	fmt.Fprintf(stdout, "别名 %d 个将写入 config.yaml\n", len(aliases))

	// 闭环：没设 default_model 时若配出了 fast，顺手设为默认
	if cfg.DefaultModel == "" {
		if _, ok := aliases["fast"]; ok {
			if ans := askLine("把 default_model 设为 fast？[Y/n] "); !strings.EqualFold(ans, "n") {
				cfg.DefaultModel = "fast"
				fmt.Fprintln(stdout, "default_model 已设为 fast")
			}
		}
	}
	return nil
}

// pickModels 解析用户输入：序号（对网关清单）或完整模型 ID，逗号/空格分隔多个。
func pickModels(input string, list []string) []string {
	var out []string
	seen := map[string]bool{}
	split := func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '，' || r == '、'
	}
	for _, tok := range strings.FieldsFunc(input, split) {
		var m string
		if n, err := strconv.Atoi(tok); err == nil && list != nil && n >= 1 && n <= len(list) {
			m = list[n-1] // 序号按显示顺序（ListModels 已排序）
		} else {
			m = tok
		}
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}
