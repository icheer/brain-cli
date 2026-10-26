package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
)

type modelsOptions struct {
	refresh bool
	jsonOut bool
}

var modelsOpts modelsOptions

var modelsCmd = &cobra.Command{
	Use:   "models [--refresh] [--json]",
	Short: "列出本地别名与网关可用模型",
	Long: `合并展示两部分：
  1. config.yaml aliases 段中的别名（含场景标注与 fallback 链）
  2. 网关 /v1/models 的实际可用列表（本地缓存，TTL 默认 24h）

--refresh 强制拉取；网关不可达时自动降级用缓存（stderr 提示）。`,
	RunE: runModels,
}

func init() {
	modelsCmd.Flags().BoolVar(&modelsOpts.refresh, "refresh", false, "忽略缓存强制拉取 /v1/models")
	modelsCmd.Flags().BoolVar(&modelsOpts.jsonOut, "json", false, "单行 JSON 输出（机器可读）")
}

type aliasJSON struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
	Use    string   `json:"use,omitempty"`
}

func runModels(cmd *cobra.Command, _ []string) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	cfg, _, err := config.Load()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%v", err)
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "缺少 base_url / api_key，修复：brain config set（brain config path 查看路径）")
	}
	aliases := cfg.Aliases

	g := gateway.New(cfg.BaseURL, cfg.APIKey, 30*time.Second)
	list, fromCache, err := app.FetchModels(cmd.Context(), g,
		time.Duration(cfg.ModelsCacheTTLSeconds)*time.Second, modelsOpts.refresh)
	if list == nil {
		return exitcode.Fail(stderr, exitCodeFor(err), "%v", err)
	}
	if err != nil {
		fmt.Fprintf(stderr, "brain: 警告: 网关不可达，降级使用本地缓存: %v\n", err)
	} else if fromCache {
		fmt.Fprintf(stderr, "brain: 使用本地缓存（--refresh 强制拉取）\n")
	}

	if modelsOpts.jsonOut {
		aliasesOut := make([]aliasJSON, 0, len(aliases))
		for _, n := range sortedAliasNames(aliases) {
			a := aliases[n]
			aliasesOut = append(aliasesOut, aliasJSON{Name: n, Models: a.Models, Use: a.Use})
		}
		out, _ := json.Marshal(struct {
			Aliases   []aliasJSON `json:"aliases"`
			Models    []string    `json:"models"`
			FromCache bool        `json:"from_cache"`
		}{aliasesOut, list, fromCache})
		fmt.Fprintln(stdout, string(out))
		return nil
	}

	if len(aliases) > 0 {
		fmt.Fprintln(stdout, "别名（config.yaml aliases 段）:")
		for _, n := range sortedAliasNames(aliases) {
			a := aliases[n]
			fmt.Fprintf(stdout, "  %-12s %s\n", n, strings.Join(a.Models, " → "))
			if a.Use != "" {
				fmt.Fprintf(stdout, "  %-12s # %s\n", "", a.Use)
			}
		}
		fmt.Fprintln(stdout)
	} else {
		// 冷启动提示：SKILL.md 模板用 -m fast 等别名，未配置时会按字面模型 ID 发出而 404
		fmt.Fprintln(stdout, "别名：未配置（config.yaml 的 aliases 段，brain init 可生成 fast/reason/coding/vision 映射；未配置时 -m 按字面模型 ID 处理）")
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "可用模型（/v1/models，%d 个%s）:\n", len(list), cacheSuffix(fromCache))
	for _, m := range list {
		fmt.Fprintf(stdout, "  %s\n", m)
	}
	return nil
}

func sortedAliasNames(aliases map[string]config.Alias) []string {
	names := make([]string, 0, len(aliases))
	for n := range aliases {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func cacheSuffix(fromCache bool) string {
	if fromCache {
		return "，来自本地缓存"
	}
	return ""
}
