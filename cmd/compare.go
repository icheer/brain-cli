package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

type compareOptions struct {
	models          []string
	files           []string
	images          []string
	system          []string
	timeout         int
	maxInputBytes   int64
	maxOutputTokens int
	reasoningEffort string
	extraBody       []string
	extraHeaders    []string
	jsonOut         bool
	quiet           bool
	dryRun          bool
	blind           bool
}

var compareOpts compareOptions

var compareCmd = &cobra.Command{
	Use: "compare -m <model>... [prompt...]",
	Short: i18n.T("同一问题并行问多个模型，stdout 按模型分隔输出原文（--json 为 responses[]）",
		"Ask several models the same question in parallel; stdout concatenates replies under model headers (--json gives responses[])"),
	Long: i18n.T(`并行独立采样：把同一个问题同时发给 ≥2 个模型，各腿原文按分隔头拼接输出，
裁决留给你（或宿主 Agent）——brain 不内建裁判。compare 里的别名是「模型组快捷方式」：
-m reason 展开为该别名链上的全部模型（与 ask 的链首位+容灾语义不同）；每条腿是具体模型，
不做故障转移（静默转移会摧毁「谁回答了什么」的透明性），但同模型重试仍允许。
≥1 腿成功即 exit 0，失败腿在 stderr 与 --json 中明示。示例：

  brain compare -m reason -m coding "这个方案的并发风险在哪"
  brain compare -m m-a,m-b -m m-c "给出三个风险点"   # 可重复、逗号分隔、与别名混用
  brain compare -m reason --json "推演方案" | jq -r '.responses[] | select(.ok) | .content'
  brain compare -m reason --blind "结论可靠吗"        # 输出匿名 Model_A/B，映射在 stderr`, `Parallel independent sampling: send the same question to >= 2 models at once;
each leg's reply is concatenated under a model header, adjudication is left to you
(or the host agent) — brain ships no built-in judge. An alias in compare is a
"model-group shortcut": -m reason expands to every model on that alias chain
(unlike ask's head-of-chain + failover). Each leg is one concrete model with NO
failover (silent failover would destroy the "who said what" transparency);
same-model retries are still allowed. Any successful leg → exit 0; failed legs
are spelled out on stderr and in --json. Examples:

  brain compare -m reason -m coding "where are the concurrency risks in this plan"
  brain compare -m m-a,m-b -m m-c "list three risks"   # repeatable, comma-separated, mixes with aliases
  brain compare -m reason --json "reason it out" | jq -r '.responses[] | select(.ok) | .content'
  brain compare -m reason --blind "is this conclusion sound"   # prints Model_A/B; mapping on stderr`),
	Args: cobra.ArbitraryArgs,
	RunE: runCompare,
}

func init() {
	compareCmd.Flags().StringArrayVarP(&compareOpts.models, "model", "m", nil,
		i18n.T("模型别名（展开为整组）或字面模型 ID，可重复且支持逗号分隔；至少展开出 2 个模型",
			"model alias (expands to its whole group) or literal model ID; repeatable, comma-separated; must resolve to >= 2 models"))
	compareCmd.Flags().StringArrayVarP(&compareOpts.files, "file", "f", nil,
		i18n.T("文本文件，可多次", "text file, repeatable"))
	compareCmd.Flags().StringArrayVarP(&compareOpts.images, "image", "i", nil,
		i18n.T("图片（本地路径或 URL），可多次", "image (local path or URL), repeatable"))
	compareCmd.Flags().StringArrayVarP(&compareOpts.system, "system", "s", nil,
		i18n.T("系统提示词，可重复（以空行连接）", "system prompt, repeatable (joined by a blank line)"))
	compareCmd.Flags().IntVar(&compareOpts.timeout, "timeout", 0,
		i18n.T("整体超时秒数（全部腿共享同一预算，墙钟≈最慢腿）",
			"overall timeout in seconds (all legs share one budget; wall clock ≈ slowest leg)"))
	compareCmd.Flags().Int64Var(&compareOpts.maxInputBytes, "max-input-bytes", 0,
		i18n.T("输入文本上限字节数（覆盖 config）", "input text size limit in bytes (overrides config)"))
	compareCmd.Flags().StringVarP(&compareOpts.reasoningEffort, "reasoning-effort", "e", "",
		i18n.T("推理深度纯透传（档位因模型而异；不设则用各模型默认）",
			"reasoning effort, passed through as-is (model-specific tiers; unset = model default)"))
	compareCmd.Flags().IntVar(&compareOpts.maxOutputTokens, "max-output-tokens", 0,
		i18n.T("输出预算（max_tokens）纯透传；0=不传用网关默认",
			"output budget (max_tokens), passed through as-is; 0 = omit, gateway default"))
	compareCmd.Flags().StringArrayVar(&compareOpts.extraBody, "extra-body", nil, extraBodyFlagsHelp())
	compareCmd.Flags().StringArrayVar(&compareOpts.extraHeaders, "extra-headers", nil, extraHeadersFlagsHelp())
	compareCmd.Flags().BoolVarP(&compareOpts.quiet, "quiet", "q", false,
		i18n.T("静默 stderr：关闭心跳/耗时提示（警告与错误保留）",
			"quiet stderr: no heartbeat/elapsed chatter (warnings and errors kept)"))
	compareCmd.Flags().BoolVar(&compareOpts.dryRun, "dry-run", false,
		i18n.T("只输出将发送的 messages 与解析出的模型组（单行 JSON），不发起网络请求",
			"print the assembled messages and resolved model group (single-line JSON) without any network call"))
	compareCmd.Flags().BoolVar(&compareOpts.blind, "blind", false,
		i18n.T("匿名盲测：输出中模型名替换为 Model_A/B/…（真名映射走 stderr），防裁决的品牌偏见",
			"blind review: model names replaced with Model_A/B/... in output (real names on stderr), guarding the adjudicator against brand bias"))
	compareCmd.Flags().BoolVar(&compareOpts.jsonOut, "json", false,
		i18n.T("单行 JSON 输出（成败恒定一个对象，responses[] 每腿含 content/usage/latency/error）",
			"single-line JSON output (always exactly one object; responses[] carries content/usage/latency/error per leg)"))
}

// compareLeg 是一条并行采样腿的结果（成功或失败都占一席，顺序=解析序）。
type compareLeg struct {
	Model        string
	OK           bool
	Content      string
	Thinking     string
	FinishReason string
	Usage        *gateway.Usage
	LatencyMs    int64
	Err          error // 失败时的原始错误（供错误分类）
}

// compareJSON 是 compare --json 的输出结构（DESIGN.md 第 4 节：responses[] 每腿独立成对象）。
type compareJSON struct {
	Responses []compareJSONLeg `json:"responses"`
	Error     *askJSONError    `json:"error"` // 仅全部腿失败时非空；code 与退出码一致
}

type compareJSONLeg struct {
	Model        string         `json:"model"`
	OK           bool           `json:"ok"`
	Content      string         `json:"content,omitempty"`
	Thinking     string         `json:"thinking,omitempty"`
	Usage        *gateway.Usage `json:"usage"`
	LatencyMs    int64          `json:"latency_ms"`
	FinishReason string         `json:"finish_reason,omitempty"`
	Error        *askJSONError  `json:"error,omitempty"`
}

// compareDryRunOut 是 compare --dry-run 的输出结构。
type compareDryRunOut struct {
	Models          []string          `json:"models"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	ExtraBody       map[string]any    `json:"extra_body,omitempty"`
	ExtraHeaders    map[string]string `json:"extra_headers,omitempty"`
	TextBytes       int64             `json:"text_bytes"`
	Messages        []gateway.Message `json:"messages"`
}

// lockedWriter 串行化多腿并发的 stderr 写入（测试里的 Builder 非并发安全；
// 终端下单次 Fprintf 通常原子，加锁无害且确定）。
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func runCompare(cmd *cobra.Command, args []string) (retErr error) {
	stdout := cmd.OutOrStdout()
	stderr := &lockedWriter{w: cmd.ErrOrStderr()}

	if compareOpts.dryRun && compareOpts.jsonOut {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.T("--dry-run 与 --json 互斥（--dry-run 自身输出单行 JSON）",
				"--dry-run and --json are mutually exclusive (--dry-run itself prints single-line JSON)"))
	}
	if compareOpts.maxOutputTokens < 0 {
		return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("--max-output-tokens 应为正整数", "--max-output-tokens must be a positive integer"))
	}

	// --extra-body / --extra-headers：通用透传口（保留字拒绝见 extras.go），全腿共用
	var extraBody map[string]any
	var extraHeaders map[string]string
	if len(compareOpts.extraBody) > 0 || len(compareOpts.extraHeaders) > 0 {
		var exErr error
		extraBody, extraHeaders, exErr = parseExtraFlags(compareOpts.extraBody, compareOpts.extraHeaders)
		if exErr != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", exErr)
		}
		// 快车道优先：-e / --max-output-tokens 与 extras 同名冲突时以 flag 为准
		applyFastLanePrecedence(extraBody, compareOpts.reasoningEffort, compareOpts.maxOutputTokens, stderr)
	}

	// --json：无论成败 stdout 恒定输出一个 JSON 对象；rawRep 为全失败时的代表错误。
	var (
		legs   []compareLeg
		models []string
		rawRep error
	)
	payload := compareJSON{}
	defer func() {
		if !compareOpts.jsonOut {
			return
		}
		if retErr != nil {
			code := exitcode.Usage
			var ee *exitcode.Error
			if errors.As(retErr, &ee) {
				code = ee.Code
			}
			e := &askJSONError{Code: code, Message: retErr.Error()}
			if rawRep != nil {
				e.Type, e.Retryable = errorTypeInfo(rawRep)
				var se *gateway.StatusError
				if errors.As(rawRep, &se) {
					e.UpstreamStatus = se.Status
				}
				e.AttemptedModels = models
			} else {
				e.Type, e.Retryable = errorTypeForCode(code)
			}
			payload.Error = e
		}
		if b, err := json.Marshal(payload); err == nil {
			fmt.Fprintln(stdout, string(b)) // --json 写点，与分隔头写点互斥
		}
	}()

	cfg, warnings, err := config.Load()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.Tf("%v（brain config edit 可修复）", "%v (fix with: brain config edit)", err))
	}
	applyUserAgent(cfg)
	for _, w := range warnings {
		fmt.Fprintf(stderr, "brain: %s %s\n", i18n.T("警告:", "warning:"), w)
	}

	if compareOpts.timeout > 0 {
		cfg.TimeoutSeconds = compareOpts.timeout
	}
	if compareOpts.maxInputBytes > 0 {
		cfg.MaxInputBytes = compareOpts.maxInputBytes
	}

	if cfg.BaseURL == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("缺少 base_url，修复：brain config set base_url <https://your-gateway/v1>",
				"missing base_url; fix: brain config set base_url <https://your-gateway/v1>"))
	}
	if cfg.APIKey == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("缺少 api_key，修复：brain config set api_key <sk-...>（或设 BRAIN_API_KEY 环境变量）",
				"missing api_key; fix: brain config set api_key <sk-...> (or set BRAIN_API_KEY)"))
	}

	// 模型组解析（第 0 节「多模型编排」：别名=模型组快捷方式，去重保序，≥2 才合法）
	names := compareOpts.models
	if len(names) == 0 {
		if cfg.DefaultModel == "" {
			return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
				i18n.T("compare 需要 ≥2 个模型：-m <别名或模型ID>（可重复/逗号分隔），或多模型 default_model",
					"compare needs >= 2 models: -m <alias-or-id> (repeatable / comma-separated), or a multi-model default_model"))
		}
		names = []string{cfg.DefaultModel}
	}
	models, err = app.CompareModels(names, cfg.Aliases)
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	if len(models) < 2 {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.Tf("compare 需要并行采样 ≥2 个模型，展开后只有 %d 个（%s）；-m 可重复或逗号分隔，别名会展开为整组",
				"compare needs >= 2 models to sample in parallel, but only %d resolved (%s); repeat -m or use commas; aliases expand to their whole group",
				len(models), strings.Join(models, ", ")))
	}

	messages, textBytes, err := assembleMessages(args, compareOpts.files, compareOpts.images, compareOpts.system, cfg.MaxInputBytes)
	if err != nil {
		var tl *app.TooLargeError
		if errors.As(err, &tl) {
			return exitcode.Fail(stderr, exitcode.InputTooLarge, "%s",
				i18n.Tf("输入 %s 超过上限 %s（--max-input-bytes 可调）",
					"input %s exceeds limit %s (adjustable via --max-input-bytes)",
					humanBytes(tl.Size), humanBytes(tl.Limit)))
		}
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}

	if compareOpts.dryRun {
		out, _ := json.Marshal(compareDryRunOut{
			Models:          models,
			ReasoningEffort: compareOpts.reasoningEffort,
			MaxOutputTokens: compareOpts.maxOutputTokens,
			ExtraBody:       extraBody,
			ExtraHeaders:    extraHeaders,
			TextBytes:       textBytes,
			Messages:        redactMessages(messages),
		})
		fmt.Fprintln(stdout, string(out))
		return nil
	}

	// 盲测映射：解析序 Model_A/B/…（--json 也匿名，映射只走 stderr）
	labels := map[string]string{}
	display := func(m string) string {
		if !compareOpts.blind {
			return m
		}
		return labels[m]
	}
	if compareOpts.blind {
		pairs := make([]string, 0, len(models))
		for i, m := range models {
			l := fmt.Sprintf("Model_%d", i+1)
			if i < 26 {
				l = fmt.Sprintf("Model_%c", 'A'+i)
			}
			labels[m] = l
			pairs = append(pairs, l+"="+m)
		}
		fmt.Fprintf(stderr, "brain: %s: %s\n", i18n.T("盲测映射（--blind）", "blind mapping (--blind)"), strings.Join(pairs, "  "))
	}

	// 总预算：全部腿共享同一 deadline（腿自 0 并行启动，墙钟≈最慢腿）
	budget := time.Duration(cfg.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(cmd.Context(), budget)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	interrupted := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			close(interrupted)
			cancel()
		case <-ctx.Done():
		}
	}()

	// 心跳：每 15s 报「已完成腿数」（stderr，--quiet 关闭）
	var done atomic.Int64
	hbDone := make(chan struct{})
	hbStop := sync.OnceFunc(func() { close(hbDone) })
	if !compareOpts.quiet {
		go func() {
			ticker := time.NewTicker(heartbeatInterval)
			defer ticker.Stop()
			start := time.Now()
			for {
				select {
				case <-hbDone:
					return
				case <-ticker.C:
					fmt.Fprintf(stderr, "brain: compare %s… %.0fs | %d/%d %s\n",
						i18n.T("等待中", "waiting"), time.Since(start).Seconds(),
						done.Load(), len(models), i18n.T("完成", "done"))
				}
			}
		}()
	}

	// 并行采样：每腿一个单元素链的 AskEngine——无故障转移，仅同模型重试。
	// 引擎的重试提示带真名，盲测时丢弃（我们自打匿名摘要行）。
	engineStderr := io.Writer(stderr)
	if compareOpts.quiet || compareOpts.blind {
		engineStderr = io.Discard
	}
	client := gateway.New(cfg.BaseURL, cfg.APIKey, budget)
	legs = make([]compareLeg, len(models))
	startAll := time.Now()
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func(i int, m string) {
			defer wg.Done()
			defer done.Add(1)
			eng := &app.AskEngine{Client: client, Stderr: engineStderr, RetryMax: cfg.RetryMax}
			start := time.Now()
			res, _, err := eng.Ask(ctx, []string{m},
				func(model string) gateway.ChatRequest {
					return gateway.ChatRequest{
						Model:           model,
						Messages:        messages,
						ReasoningEffort: compareOpts.reasoningEffort,
						MaxTokens:       compareOpts.maxOutputTokens,
						ExtraBody:       extraBody,
						ExtraHeaders:    extraHeaders,
					}
				},
				func(content, reasoning string) {})
			l := &legs[i]
			l.Model = m
			l.LatencyMs = time.Since(start).Milliseconds()
			if err != nil {
				l.Err = err
				return
			}
			l.OK = true
			l.Content = res.Content
			l.Thinking = res.Thinking
			l.FinishReason = res.FinishReason
			l.Usage = res.Usage
		}(i, m)
	}
	wg.Wait()
	hbStop()

	// 逐腿落账：失败摘要与空正文警告（stderr），--json 的 responses[]
	anyOK := false
	for i := range legs {
		l := &legs[i]
		if l.OK {
			anyOK = true
			if l.Content == "" {
				note := ""
				if l.Thinking != "" {
					note = i18n.Tf("，收到思维链 ~%s 字符", "; got ~%s chars of reasoning", humanCount(int64(len(l.Thinking))))
				}
				fmt.Fprintf(stderr, "brain: %s %s（finish_reason=%s%s）\n", i18n.T("警告:", "warning:"),
					i18n.Tf("%s 返回空内容", "%s returned empty content", display(l.Model)), l.FinishReason, note)
			}
		} else {
			fmt.Fprintf(stderr, "brain: compare: %s %s（code %d）: %s\n",
				display(l.Model), i18n.T("腿失败", "leg failed"), exitCodeFor(l.Err), firstLine(l.Err.Error()))
		}
		if compareOpts.jsonOut {
			r := compareJSONLeg{
				Model: display(l.Model), OK: l.OK, Content: l.Content, Thinking: l.Thinking,
				Usage: l.Usage, LatencyMs: l.LatencyMs, FinishReason: l.FinishReason,
			}
			if !l.OK {
				c := exitCodeFor(l.Err)
				e := &askJSONError{Code: c, Message: firstLine(l.Err.Error())}
				e.Type, e.Retryable = errorTypeInfo(l.Err)
				var se *gateway.StatusError
				if errors.As(l.Err, &se) {
					e.UpstreamStatus = se.Status
				}
				r.Error = e
			}
			payload.Responses = append(payload.Responses, r)
		}
	}

	// 被中断且一无所获 → 130（有成功腿则照常交付，与「已输出正文永不丢」同构）
	if !anyOK {
		select {
		case <-interrupted:
			return exitcode.Fail(stderr, exitcode.Interrupt, "%s", i18n.T("被中断", "interrupted"))
		default:
		}
		code := compareWorstCode(legs)
		for i := range legs {
			if !legs[i].OK && exitCodeFor(legs[i].Err) == code {
				rawRep = legs[i].Err
				break
			}
		}
		return exitcode.Fail(stderr, code, "%s",
			i18n.Tf("全部 %d 条腿失败（各腿错误见上）", "all %d legs failed (per-leg errors above)", len(models)))
	}

	// 输出阶段：分隔头拼接 / --json 单对象（defer），两个互斥写点
	if !compareOpts.jsonOut {
		first := true
		for i := range legs {
			l := &legs[i]
			if !l.OK {
				continue // 失败腿不出现在 stdout（stderr/--json 已明示）
			}
			if !first {
				fmt.Fprint(stdout, "\n")
			}
			first = false
			fmt.Fprintf(stdout, "===== model: %s =====\n", display(l.Model))
			if l.Content != "" {
				fmt.Fprint(stdout, ensureSingleNewline(l.Content))
			}
		}
	}
	okCount := 0
	for i := range legs {
		if legs[i].OK {
			okCount++
		}
	}
	if !compareOpts.quiet {
		fmt.Fprintf(stderr, "brain: compare %.1fs | %d/%d %s\n",
			time.Since(startAll).Seconds(), okCount, len(models), i18n.T("成功", "ok"))
	}
	return nil
}

// compareWorstCode 全失败时挑「最可行动」退出码：认证(2) > 限流(3) > 超时(4) > 上游(5)。
func compareWorstCode(legs []compareLeg) int {
	rank := func(c int) int {
		switch c {
		case exitcode.Auth:
			return 0
		case exitcode.RateLimit:
			return 1
		case exitcode.Timeout:
			return 2
		case exitcode.Upstream:
			return 3
		}
		return 4
	}
	best, bestRank := exitcode.Upstream, 1<<30
	for i := range legs {
		if legs[i].OK {
			continue
		}
		c := exitCodeFor(legs[i].Err)
		if r := rank(c); r < bestRank {
			best, bestRank = c, r
		}
	}
	return best
}

// firstLine 取错误首行（StatusError 的响应体片段可能含换行，stderr/JSON 里要单行）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
