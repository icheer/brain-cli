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
	"github.com/icheer/brain-cli/internal/i18n"
)

var initOpts struct {
	baseURL      string
	defaultModel string
	force        bool
}

var initCmd = &cobra.Command{
	Use:   "init [--base-url <url>] [--default-model <别名>]",
	Short: i18n.T("交互式引导写入 config.yaml（首次配置）", "interactive first-time setup writing config.yaml"),
	Long: i18n.T(`首次使用引导：依次询问 base_url、api_key、default_model，写入 config.yaml（0600）。
已提供同名 flag 的项跳过询问，便于脚本化：

  brain init --base-url https://gw.example.com/v1 --default-model fast

base_url 只要是 OpenAI 兼容 API 即可，不限于网关（如 https://api.openai.com/v1）。
api_key 刻意不设 flag（避免进入 shell history / ps 输出）：
交互输入，或留空后由 BRAIN_API_KEY 环境变量提供。
随后可选生成 aliases 段（fast/reason/coding/vision，拉网关模型清单做选单）；
末尾可选配置图像生成 draw 段（model 必填，base_url/api_key 留空兜底顶层）——
覆盖重建时原有别名与 draw 段默认保留。`, `Interactive first-time setup: asks for base_url, api_key, default_model in
turn and writes config.yaml (0600). Items given via flags skip the prompt,
which keeps scripting easy:

  brain init --base-url https://gw.example.com/v1 --default-model fast

base_url can be any OpenAI-compatible API, not just a gateway
(e.g. https://api.openai.com/v1). api_key deliberately has no flag (keeps it
out of shell history / ps): type it interactively, or leave blank and provide
BRAIN_API_KEY instead. Optionally generates an aliases section afterwards
(fast/reason/coding/vision, from the gateway model listing), then optionally
sets up the image-generation draw section (model required; blank
base_url/api_key fall back to the top-level values) — rebuilding an
existing config keeps its aliases and draw section by default.`),
	RunE: runInit,
}

func init() {
	initCmd.Flags().StringVar(&initOpts.baseURL, "base-url", "", i18n.T("网关地址（缺省进入交互询问）", "gateway URL (interactive prompt if omitted)"))
	initCmd.Flags().StringVar(&initOpts.defaultModel, "default-model", "", i18n.T("默认模型别名（缺省进入交互询问，可留空跳过）", "default model alias (interactive prompt if omitted; blank to skip)"))
	initCmd.Flags().BoolVar(&initOpts.force, "force", false, i18n.T("已存在 config.yaml 时跳过覆盖确认", "skip the overwrite confirmation when config.yaml exists"))
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
		if ans := ask(i18n.Tf("config.yaml 已存在（%s），覆盖重建？[y/N] ", "config.yaml already exists (%s); rebuild? [y/N] ", p)); !strings.EqualFold(ans, "y") {
			fmt.Fprintln(stderr, "brain:", i18n.T("已取消（未做任何修改）", "cancelled (nothing changed)"))
			return nil
		}
	}

	// 覆盖重建时保留原有别名与 draw 段（单文件化后两者都在 config.yaml 里，不能跟着重建丢掉；
	// draw 段 2026-11-03 补——此前 init 覆盖重建会静默清空生图配置）
	oldAliases := map[string]config.Alias{}
	var oldDraw config.DrawConfig
	var oldDecide config.DecideConfig
	if old, _, err := config.Load(); err == nil {
		oldAliases = old.Aliases
		oldDraw = old.Draw
		oldDecide = old.Decide
	}
	if len(oldAliases) > 0 {
		fmt.Fprintf(stdout, "%s\n", i18n.Tf("将保留原有别名 %d 个（brain config edit 可修改）", "keeping %d existing alias(es) (edit via brain config edit)", len(oldAliases)))
	}

	cfg := config.Default()
	cfg.Aliases = oldAliases
	cfg.Draw = oldDraw
	cfg.Decide = oldDecide

	baseURL := initOpts.baseURL
	if baseURL == "" {
		baseURL = ask(i18n.T("base_url（任意 OpenAI 兼容 API，如 https://api.openai.com/v1）: ", "base_url (any OpenAI-compatible API, e.g. https://api.openai.com/v1): "))
	}
	if baseURL == "" || !strings.HasPrefix(baseURL, "http") {
		return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("base_url 不能为空且须以 http(s):// 开头", "base_url must be non-empty and start with http(s)://"))
	}
	cfg.BaseURL = baseURL

	// api_key 不设 flag（DESIGN.md 2.4）；留空则回落 BRAIN_API_KEY 环境变量
	apiKey := ask(i18n.T("api_key（输入会回显；留空则使用 BRAIN_API_KEY 环境变量）: ", "api_key (input is echoed; blank uses BRAIN_API_KEY): "))
	if apiKey == "" {
		apiKey = os.Getenv("BRAIN_API_KEY")
		if apiKey != "" {
			fmt.Fprintln(stdout, i18n.T("已使用 BRAIN_API_KEY 环境变量。", "using BRAIN_API_KEY from the environment."))
		}
	}
	if apiKey == "" {
		return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("缺少 api_key：请交互输入，或设置 BRAIN_API_KEY 环境变量", "missing api_key: type it interactively or set BRAIN_API_KEY"))
	}
	cfg.APIKey = apiKey

	if initOpts.defaultModel == "" {
		cfg.DefaultModel = ask(i18n.T("default_model（别名或模型 ID，可留空跳过）: ", "default_model (alias or model ID; blank to skip): "))
	} else {
		cfg.DefaultModel = initOpts.defaultModel
	}

	// 别名生成：SKILL.md 模板依赖 fast/reason/coding/vision，缺失时 -m 别名会按
	// 字面模型 ID 发出而 404（最小安装的第一跳断点，2026-10-25 外脑评审证实）
	regen := len(oldAliases) == 0 // 无别名默认生成；已有别名默认保留（不重建）
	if len(oldAliases) > 0 {
		regen = strings.EqualFold(ask(i18n.T("重新生成别名映射？[y/N] ", "regenerate alias mappings? [y/N] ")), "y")
	} else if strings.EqualFold(ask(i18n.T("生成别名映射 fast/reason/coding/vision？[Y/n] ", "generate alias mappings fast/reason/coding/vision? [Y/n] ")), "n") {
		regen = false
		fmt.Fprintln(stdout, i18n.T("已跳过（-m 按字面模型 ID 处理；完整示例见仓库 config.example.yaml）", "skipped (-m is treated as a literal model ID; see config.example.yaml in the repo)"))
	}
	if regen {
		if err := initAliases(cmd.Context(), in, stdout, stderr, &cfg); err != nil {
			return err
		}
	}

	// 图像生成（draw）段：可选引导，不必手填 yaml；已有 draw 配置默认保留（同别名待遇），
	// 无则默认跳过——回车/EOF 均为「不配置」，脚本化与非交互运行安全
	hasOldDraw := oldDraw.Model != "" || oldDraw.BaseURL != "" || oldDraw.APIKey != "" || oldDraw.Type != ""
	setupDraw := false
	if hasOldDraw {
		fmt.Fprintf(stdout, "%s\n", i18n.Tf("已保留现有 draw 配置（model=%s）", "keeping existing draw config (model=%s)", oldDraw.Model))
		setupDraw = strings.EqualFold(ask(i18n.T("重新配置图像生成？[y/N] ", "reconfigure image generation? [y/N] ")), "y")
	} else {
		setupDraw = strings.EqualFold(ask(i18n.T("配置图像生成 brain draw？（可选，回车跳过）[y/N] ", "set up image generation brain draw? (optional, Enter skips) [y/N] ")), "y")
	}
	if setupDraw {
		d := config.DrawConfig{}
		d.Model = ask(i18n.T("draw.model 生图模型 ID（名字含 gemini 走 Gemini 原生，其余走 OpenAI Images）: ",
			"draw.model image model ID (a name containing gemini goes Gemini native, anything else OpenAI Images): "))
		if d.Model == "" {
			fmt.Fprintln(stdout, i18n.T("未填模型，draw 配置已跳过（之后 brain config set draw.model 可补）",
				"no model given; draw setup skipped (add later via brain config set draw.model)"))
		} else {
			d.BaseURL = ask(i18n.T("draw.base_url（留空=用顶层 base_url；末尾 /v1、/v1beta 可写可不写）: ",
				"draw.base_url (blank = top-level base_url; trailing /v1 or /v1beta optional): "))
			if d.BaseURL != "" && !strings.HasPrefix(d.BaseURL, "http") {
				return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("draw.base_url 须以 http(s):// 开头", "draw.base_url must start with http(s)://"))
			}
			d.APIKey = ask(i18n.T("draw.api_key（留空=用顶层 api_key；输入会回显）: ",
				"draw.api_key (blank = top-level api_key; input is echoed): "))
			d.Type = strings.ToLower(ask(i18n.T("draw.type（gemini|openai，回车=按模型名推断）: ",
				"draw.type (gemini|openai; Enter = infer from the model name): ")))
			if d.Type != "" && d.Type != "gemini" && d.Type != "openai" {
				fmt.Fprintln(stdout, i18n.T("draw.type 仅支持 gemini|openai，已按留空（模型名推断）处理",
					"draw.type accepts gemini|openai only; treated as blank (inferred)"))
				d.Type = ""
			}
			cfg.Draw = d
		}
	}

	// 决策（decide）段：可选引导（同 draw 待遇；无 type——单方言 TypeSafe wire）
	hasOldDecide := oldDecide.Model != "" || oldDecide.BaseURL != "" || oldDecide.APIKey != ""
	setupDecide := false
	if hasOldDecide {
		fmt.Fprintf(stdout, "%s\n", i18n.Tf("已保留现有 decide 配置（model=%s）", "keeping existing decide config (model=%s)", oldDecide.Model))
		setupDecide = strings.EqualFold(ask(i18n.T("重新配置决策模型？[y/N] ", "reconfigure the decision model? [y/N] ")), "y")
	} else {
		setupDecide = strings.EqualFold(ask(i18n.T("配置决策原语 brain decide？（可选，回车跳过）[y/N] ", "set up the decision primitive brain decide? (optional, Enter skips) [y/N] ")), "y")
	}
	if setupDecide {
		d := config.DecideConfig{}
		d.Model = ask(i18n.T("decide.model 决策模型 ID（如 jev-latest，走 TypeSafe wire /v1/systemone）: ",
			"decide.model decision model ID (e.g. jev-latest, TypeSafe wire /v1/systemone): "))
		if d.Model == "" {
			fmt.Fprintln(stdout, i18n.T("未填模型，decide 配置已跳过（之后 brain config set decide.model 可补）",
				"no model given; decide setup skipped (add later via brain config set decide.model)"))
		} else {
			d.BaseURL = ask(i18n.T("decide.base_url（留空=用顶层 base_url；末尾 /v1 可写可不写）: ",
				"decide.base_url (blank = top-level base_url; trailing /v1 optional): "))
			if d.BaseURL != "" && !strings.HasPrefix(d.BaseURL, "http") {
				return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("decide.base_url 须以 http(s):// 开头", "decide.base_url must start with http(s)://"))
			}
			d.APIKey = ask(i18n.T("decide.api_key（留空=用顶层 api_key；输入会回显）: ",
				"decide.api_key (blank = top-level api_key; input is echoed): "))
			cfg.Decide = d
		}
	}

	if err := config.Save(cfg); err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	fmt.Fprintf(stdout, "%s %s（0600）\n", i18n.T("已写入", "written"), p)
	fmt.Fprintln(stdout, i18n.T("下一步：brain ping 自检（brain ping -m <别名> 可逐模型探测链可用性）", "next: brain ping to self-check (brain ping -m <alias> probes each model in the chain)"))
	return nil
}

// aliasDefs 标准别名及场景标注（与 config.example.yaml / SKILL.md 模板一致）。
var aliasDefs = []struct {
	name string
	use  func() string
}{
	{"fast", func() string { return i18n.T("快速问答、简单总结", "quick Q&A, light summarization") }},
	{"reason", func() string { return i18n.T("复杂推理、方案交叉验证", "hard reasoning, cross-checking") }},
	{"coding", func() string { return i18n.T("代码审查、重构建议", "code review, refactoring advice") }},
	{"vision", func() string {
		return i18n.T("图表/截图解析（需多模态模型）", "charts/screenshots (needs a multimodal model)")
	}},
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
		fmt.Fprintf(stdout, "%s\n", i18n.Tf("网关模型清单不可达（%v）：可直接粘贴完整模型 ID", "gateway model listing unreachable (%v): paste full model IDs instead", err))
	} else {
		list = l
		const showMax = 40
		fmt.Fprintf(stdout, "%s\n", i18n.Tf("网关可用模型（%d 个，仅列前 %d；序号或完整 ID 均可）：", "gateway models (%d total, showing first %d; index or full ID both work):", len(list), showMax))
		for i, m := range list {
			if i >= showMax {
				fmt.Fprintf(stdout, "  …%s\n", i18n.Tf("（其余 %d 个见 brain models）", "(%d more; see brain models)", len(list)-showMax))
				break
			}
			fmt.Fprintf(stdout, "  %2d. %s\n", i+1, m)
		}
	}

	aliases := map[string]config.Alias{}
	for _, d := range aliasDefs {
		input := askLine(fmt.Sprintf("%s (%s) → ", d.name, d.use()))
		if models := pickModels(input, list); len(models) > 0 {
			aliases[d.name] = config.Alias{Models: models, Use: d.use()}
		}
	}
	if len(aliases) == 0 {
		fmt.Fprintln(stdout, i18n.T("未选择任何别名（-m 按字面模型 ID 处理）", "no aliases selected (-m is treated as a literal model ID)"))
		return nil
	}
	cfg.Aliases = aliases
	fmt.Fprintf(stdout, "%s\n", i18n.Tf("别名 %d 个将写入 config.yaml", "%d alias(es) will be written into config.yaml", len(aliases)))

	// 闭环：没设 default_model 时若配出了 fast，顺手设为默认
	if cfg.DefaultModel == "" {
		if _, ok := aliases["fast"]; ok {
			if ans := askLine(i18n.T("把 default_model 设为 fast？[Y/n] ", "set default_model to fast? [Y/n] ")); !strings.EqualFold(ans, "n") {
				cfg.DefaultModel = "fast"
				fmt.Fprintln(stdout, i18n.T("default_model 已设为 fast", "default_model set to fast"))
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
