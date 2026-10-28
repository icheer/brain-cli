// Package cmd 定义 brain 的 CLI 命令树（cobra）。
package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

// 版本信息由 CI/goreleaser 通过 -ldflags 注入（DESIGN.md 第 5 节）：
//
//	-X github.com/icheer/brain-cli/cmd.version=1.0.0 -X github.com/icheer/brain-cli/cmd.commit=abc1234
var (
	version = "dev"
	commit  = "none"
)

var rootCmd = &cobra.Command{
	Use: "brain",
	Short: i18n.T("大模型网关外脑：给宿主 Agent 的一次干净利落的咨询调用",
		"LLM gateway side-brain: one clean advisory call for host agents"),
	Long: i18n.T(`brain 通过 OpenAI Chat Completions 兼容协议调用大模型网关。

stdout 只输出模型正文；警告/耗时/心跳走 stderr（便于管道与 Agent 解析）。
多轮上下文由调用方拼接，工具本身无状态。

快速开始：
  brain config set base_url <https://your-gateway/v1>
  brain config set api_key  <sk-...>
  brain ask -m fast "帮我分析这个问题"`, `brain talks to an LLM gateway via the OpenAI Chat Completions protocol.

stdout carries only the model reply; warnings/timing/heartbeat go to stderr
(pipe- and agent-friendly). Multi-turn context is assembled by the caller;
the tool itself is stateless.

Quick start:
  brain config set base_url <https://your-gateway/v1>
  brain config set api_key  <sk-...>
  brain ask -m fast "help me analyze this"`),
	Version:       version,
	SilenceUsage:  true, // 错误信息单行足够；详细用法看 --help
	SilenceErrors: true, // 错误统一由 exitcode.Fail / run 打印，前缀一致
}

// stdin 注入点：默认接 os.Stdin；测试可替换（golden test 需要）。
var (
	stdin      io.Reader = os.Stdin
	stdinPiped bool
)

// helpCmd 双语替换 cobra 默认 help 命令（框架自带 Short/Long 为英文且不在 i18n 覆盖面内）。
// Run 逻辑与 cobra 默认实现一致：定位目标命令并打印其帮助。
var helpCmd = &cobra.Command{
	Use:   "help [command]",
	Short: i18n.T("查看任意命令的帮助", "Help about any command"),
	Long: i18n.T(`查看 brain 中任意命令的帮助。
直接输入 brain help [命令路径] 查看完整细节。`,
		`Help provides help for any command in the application.
Simply type brain help [path to command] for full details.`),
	Run: func(c *cobra.Command, args []string) {
		cmd, _, e := c.Root().Find(args)
		if cmd == nil || e != nil {
			c.Printf(i18n.T("未知的帮助主题 %q\n", "Unknown help topic %q\n"), args)
			_ = c.Root().Usage()
		} else {
			cmd.InitDefaultHelpFlag()
			_ = cmd.Help()
		}
	},
}

func init() {
	rootCmd.SetVersionTemplate("brain " + version + " (commit " + commit + ")\n")
	// cobra 自带的 completion 命令（shell 补全脚本生成器）对 Agent 场景无用，
	// 且其英文文案不进双语链、嵌套子命令打错不报错，直接移除
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	// 命令清单按注册序展示（默认按字母序，会把 council 挤到 config 后面）：
	// ask/compare/council 三个核心原语相邻前置，其后是配置三件套
	cobra.EnableCommandSorting = false
	rootCmd.SetHelpCommand(helpCmd)
	rootCmd.AddCommand(askCmd, compareCmd, councilCmd, initCmd, configCmd, modelsCmd, pingCmd)
}

func Execute() {
	gateway.UserAgent = "brain/" + version
	stdinPiped = isStdinPiped()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// applyUserAgent 应用配置的 UA 覆盖（个别网关对 UA 取值校验严格）。
// 默认保持诚实的 brain/<version>——便于网关侧遥测与排障，不伪装浏览器。
func applyUserAgent(cfg config.Config) {
	if cfg.UserAgent != "" {
		gateway.UserAgent = cfg.UserAgent
	}
}

// isStdinPiped 判定是否读取 stdin：
// 真实管道（`cat x | brain`）或输入重定向的常规文件（`brain < x`）→ 读；
// 终端（交互）与 socket（后台任务环境，写端永不关闭）→ 跳过。
// 不能用「非字符设备」做判定——那会把永不关闭的 socket 误判为管道，
// ReadAll 将永久阻塞（本 CLI 的头号挂死风险，2026-10-24 真实踩中）。
func isStdinPiped() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	m := fi.Mode()
	return m&os.ModeNamedPipe != 0 || m.IsRegular()
}

// run 供测试直接调用；返回退出码。
func run(args []string, stdout, stderr io.Writer) int {
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	if err := rootCmd.Execute(); err != nil {
		var ee *exitcode.Error
		if errors.As(err, &ee) {
			return ee.Code
		}
		// cobra 的用法错误（未知 flag、缺参数等）
		fmt.Fprintln(stderr, "brain:", err)
		return exitcode.Usage
	}
	return exitcode.OK
}
