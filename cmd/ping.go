package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
)

var pingOpts struct {
	timeout int
}

var pingCmd = &cobra.Command{
	Use:   "ping [--timeout 秒]",
	Short: "自检连通性 / 鉴权 / 默认模型可用性",
	Long: `快速自检（SKILL.md 引导 Agent 首次使用 brain 前先跑它）：
  1. 网关连通性
  2. api_key 鉴权
  3. default_model 别名解析与可用性

stdout 逐项输出 OK 结果；失败项走 stderr 并按退出码收场。`,
	RunE: runPing,
}

func init() {
	pingCmd.Flags().IntVar(&pingOpts.timeout, "timeout", 20, "检查的秒数上限")
	rootCmd.AddCommand(pingCmd)
}

func runPing(cmd *cobra.Command, _ []string) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	cfg, _, err := config.Load()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%v", err)
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "缺少 base_url / api_key，修复：brain config set（brain config path 查看路径）")
	}

	g := gateway.New(cfg.BaseURL, cfg.APIKey, time.Duration(pingOpts.timeout)*time.Second)
	models, err := g.ListModels(cmd.Context())
	if err != nil {
		var se *gateway.StatusError
		if errors.As(err, &se) && (se.Status == 401 || se.Status == 403) {
			return exitcode.Fail(stderr, exitcode.Auth, "鉴权失败（HTTP %d）：检查 api_key → brain config set api_key <sk-...>", se.Status)
		}
		return exitcode.Fail(stderr, exitCodeFor(err), "网关不可达或异常: %v", err)
	}
	fmt.Fprintf(stdout, "连通性 OK  %s\n", cfg.BaseURL)
	fmt.Fprintf(stdout, "鉴权 OK    api_key 有效\n")
	fmt.Fprintf(stdout, "模型清单 OK  %d 个（brain models 查看）\n", len(models))

	if cfg.DefaultModel == "" {
		fmt.Fprintln(stderr, "brain: 提示: 未设置 default_model，不影响 -m 显式指定（brain config set default_model <别名>）")
		return nil
	}
	mf, err := config.LoadModels()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	chain, err := app.ModelChain(cfg.DefaultModel, mf)
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	primary := chain[0]
	for _, m := range models {
		if m == primary {
			fmt.Fprintf(stdout, "默认模型 OK  %s → %s\n", cfg.DefaultModel, primary)
			return nil
		}
	}
	return exitcode.Fail(stderr, exitcode.Usage,
		"默认模型 %s → %s 不在网关可用列表（可能无权限或已下线）：brain models --refresh 核对并更新 models.yaml",
		cfg.DefaultModel, primary)
}
