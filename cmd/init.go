package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
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
交互输入，或留空后由 BRAIN_API_KEY 环境变量提供。`,
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

	cfg := config.Default()

	baseURL := initOpts.baseURL
	if baseURL == "" {
		baseURL = ask("base_url（任意 OpenAI 兼容 API，如 https://api.openai.com/v1）: ")
	}
	if baseURL == "" || !strings.HasPrefix(baseURL, "http") {
		return exitcode.Fail(stderr, exitcode.Usage, "base_url 不能为空且须以 http(s):// 开头")
	}
	cfg.BaseURL = baseURL

	// api_key 不设 flag（TODO.md 2.4）；留空则回落 BRAIN_API_KEY 环境变量
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

	if err := config.Save(cfg); err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	mp, _ := config.ModelsPath()
	fmt.Fprintf(stdout, "已写入 %s（权限 0600）\n", p)
	fmt.Fprintf(stdout, "下一步：brain ping 自检；别名映射可参考仓库 models.example.yaml 放到 %s\n", mp)
	return nil
}
