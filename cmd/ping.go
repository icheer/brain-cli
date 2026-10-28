package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

type pingOptions struct {
	timeout int
	model   string
	jsonOut bool
}

var pingOpts pingOptions

var pingCmd = &cobra.Command{
	Use: "ping [-m model] [--timeout 秒] [--json]",
	Short: i18n.T("自检连通性 / 鉴权 / 模型清单（-m 时逐模型探测别名链）",
		"self-check connectivity / auth / model list (with -m, probe each model in the chain)"),
	Long: i18n.T(`快速自检（SKILL.md 引导 Agent 首次使用 brain 前先跑它）：
  1. 网关连通性
  2. api_key 鉴权
  3. default_model 别名解析与可用性

-m <别名或模型ID> 时额外逐个探测别名链上的模型（各发一次极小补全请求）。
/v1/models 列出不代表对本 key 可用，探测才是硬证据。
stdout 逐项输出结果；--json 时输出单对象（成败恒定，error 与 ask --json 同构）。`, `Quick self-check (SKILL.md tells agents to run this first):
  1. gateway connectivity
  2. api_key auth
  3. default_model alias resolution and availability

With -m <alias-or-id>, additionally probe every model in the chain (one tiny
completion each). Being listed in /v1/models does not mean usable with your key;
an actual probe is the only hard evidence.
Results print line by line on stdout; --json emits exactly one object
(success or failure, error shaped like ask --json).`),
	RunE: runPing,
}

func init() {
	pingCmd.Flags().IntVar(&pingOpts.timeout, "timeout", 20, i18n.T("每项检查的秒数上限（清单检查与逐模型探测各自独立计时）", "per-check timeout in seconds (list check and each probe are timed independently)"))
	pingCmd.Flags().StringVarP(&pingOpts.model, "model", "m", "", i18n.T("别名或模型 ID：逐个探测其 fallback 链", "alias or model ID: probe each model of its fallback chain"))
	pingCmd.Flags().BoolVar(&pingOpts.jsonOut, "json", false, i18n.T("单行 JSON 输出（机器可读）", "single-line JSON output (machine-readable)"))
}

// pingJSON 是 ping --json 的输出结构（成败恒定一个对象，error 与 ask --json 同构）。
type pingJSON struct {
	Connectivity string           `json:"connectivity"` // ok | fail
	BaseURL      string           `json:"base_url,omitempty"`
	Auth         string           `json:"auth,omitempty"` // ok
	ModelCount   int              `json:"model_count,omitempty"`
	DefaultModel *pingDefaultJSON `json:"default_model,omitempty"`
	Probes       []pingProbeJSON  `json:"probes,omitempty"`
	Error        *askJSONError    `json:"error"`
}

type pingDefaultJSON struct {
	Requested string `json:"requested"`
	Resolved  string `json:"resolved"`
	OK        bool   `json:"ok"`
}

type pingProbeJSON struct {
	Model     string `json:"model"`
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

func runPing(cmd *cobra.Command, _ []string) (retErr error) {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	payload := pingJSON{}
	defer func() {
		if !pingOpts.jsonOut {
			return
		}
		if retErr != nil {
			code := exitcode.Usage
			var ee *exitcode.Error
			if errors.As(retErr, &ee) {
				code = ee.Code
			}
			typ, retryable := errorTypeForCode(code)
			payload.Error = &askJSONError{Code: code, Type: typ, Message: retErr.Error(), Retryable: retryable}
		}
		if b, err := json.Marshal(payload); err == nil {
			fmt.Fprintln(stdout, string(b))
		}
	}()

	cfg, _, err := config.Load()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%v", err)
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s", i18n.T("缺少 base_url / api_key，修复：brain config set（brain config path 查看路径）", "missing base_url / api_key; fix with: brain config set (see brain config path)"))
	}
	payload.BaseURL = cfg.BaseURL

	g := gateway.New(cfg.BaseURL, cfg.APIKey, time.Duration(pingOpts.timeout)*time.Second)
	models, err := g.ListModels(cmd.Context())
	if err != nil {
		payload.Connectivity = "fail"
		var se *gateway.StatusError
		if errors.As(err, &se) && (se.Status == 401 || se.Status == 403) {
			return exitcode.Fail(stderr, exitcode.Auth, "%s", i18n.Tf("鉴权失败（HTTP %d）：检查 api_key → brain config set api_key <sk-...>", "auth failed (HTTP %d): check api_key -> brain config set api_key <sk-...>", se.Status))
		}
		return exitcode.Fail(stderr, exitCodeFor(err), "%s", i18n.Tf("网关不可达或异常: %v", "gateway unreachable or abnormal: %v", err))
	}
	payload.Connectivity, payload.Auth = "ok", "ok"
	payload.ModelCount = len(models)
	if !pingOpts.jsonOut { // --json 模式 stdout 只输出单对象，人类可读行走 stderr 或省略
		fmt.Fprintf(stdout, "%s  %s\n", i18n.T("连通性 OK", "connectivity OK"), cfg.BaseURL)
		fmt.Fprintf(stdout, "%s    %s\n", i18n.T("鉴权 OK", "auth OK"), i18n.T("api_key 有效", "api_key valid"))
		fmt.Fprintf(stdout, "%s  %d %s\n", i18n.T("模型清单 OK", "model list OK"), len(models), i18n.T("个（brain models 查看）", "(see brain models)"))
	}

	if cfg.DefaultModel == "" {
		fmt.Fprintln(stderr, "brain:", i18n.T("提示: 未设置 default_model，不影响 -m 显式指定（brain config set default_model <别名>）", "note: default_model not set; -m still works explicitly (brain config set default_model <alias>)"))
	}

	if cfg.DefaultModel != "" {
		chain, err := app.ModelChain(cfg.DefaultModel, cfg.Aliases)
		if err != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
		}
		primary := chain[0]
		ok := false
		for _, m := range models {
			if m == primary {
				ok = true
				break
			}
		}
		payload.DefaultModel = &pingDefaultJSON{Requested: cfg.DefaultModel, Resolved: primary, OK: ok}
		if !ok {
			return exitcode.Fail(stderr, exitcode.Usage, "%s",
				i18n.Tf("默认模型 %s → %s 不在网关可用列表（可能无权限或已下线）：brain models --refresh 核对并更新 config.yaml 的 aliases 段",
					"default model %s -> %s not in gateway listing (no permission or retired?): cross-check with brain models --refresh and update the config.yaml aliases section",
					cfg.DefaultModel, primary))
		}
		if !pingOpts.jsonOut {
			fmt.Fprintf(stdout, "%s  %s → %s\n", i18n.T("默认模型 OK", "default model OK"), cfg.DefaultModel, primary)
		}
	}

	if pingOpts.model == "" {
		return nil
	}
	return probeChain(cmd.Context(), stdout, stderr, g, cfg.Aliases, &payload)
}

// probeChain 逐模型探测别名链（各发一次极小补全请求），结果写回 payload.Probes。
// 判定：链上至少一个 OK 即成功；全部失败 exit 5；主模型不可用时 stderr 明示（静默
// fallback 到次优模型的隐性损失，Agent 应能看见）。
func probeChain(ctx context.Context, stdout, stderr io.Writer, g *gateway.Client,
	aliases map[string]config.Alias, payload *pingJSON,
) error {
	chain, err := app.ModelChain(pingOpts.model, aliases)
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	// 探测预算：每模型独立 deadline = --timeout（下限 5s）。不能共享一个总预算——
	// 思维链模型对极小 prompt 也可能跑 15s+（实测 deepseek-v4.1-flash 13.9s），
	// 先探测的慢模型会把共享预算吃光，后面的模型直接饿死（2026-10-25 真实网关踩中）
	perProbe := time.Duration(pingOpts.timeout) * time.Second
	if perProbe < 5*time.Second {
		perProbe = 5 * time.Second
	}

	anyOK := false
	for _, m := range chain {
		p := pingProbeJSON{Model: m}
		t0 := time.Now()
		pctx, cancel := context.WithTimeout(ctx, perProbe)
		res, err := g.ChatCompletionStream(pctx, gateway.ChatRequest{
			Model:    m,
			Messages: []gateway.Message{{Role: "user", Content: "只回复:OK"}},
		}, nil)
		cancel()
		p.LatencyMs = time.Since(t0).Milliseconds()
		switch {
		case err != nil:
			p.Error = oneLine(err.Error(), 120)
			fmt.Fprintf(stderr, "brain: %s %s: %s\n", i18n.T("探测失败", "probe failed"), m, p.Error)
		case res.Content == "":
			p.Error = i18n.Tf("空正文（finish_reason=%s，可能思维链耗尽输出预算）", "empty content (finish_reason=%s; thinking may have eaten the output budget)", res.FinishReason)
			fmt.Fprintf(stderr, "brain: %s %s: %s\n", i18n.T("探测失败", "probe failed"), m, p.Error)
		default:
			p.OK = true
			anyOK = true
			if !pingOpts.jsonOut {
				fmt.Fprintf(stdout, "%s  %s (%s)\n", i18n.T("探测 OK", "probe OK"), m, humanMillis(p.LatencyMs))
			}
		}
		payload.Probes = append(payload.Probes, p)
	}

	if !anyOK {
		return exitcode.Fail(stderr, exitcode.Upstream, "%s", i18n.Tf("别名链 %d 个模型全部探测失败", "all %d models in the chain failed probing", len(chain)))
	}
	if !payload.Probes[0].OK {
		fmt.Fprintln(stderr, "brain:", i18n.T("提示: 主模型探测失败，实际调用将由链上后备承接（建议更新 config.yaml 的 aliases 段）", "note: primary model failed probing; calls will fall back to the chain (consider updating the config.yaml aliases section)"))
	}
	return nil
}

// humanMillis 探测耗时展示：<1s 用 ms，否则一位小数秒。
func humanMillis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// oneLine 压成单行并截断（探测失败原因展示用；网关错误体可达数百字符）。
func oneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
