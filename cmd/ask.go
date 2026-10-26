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

type askOptions struct {
	model           string
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
	stream          bool
	showThinking    bool
	quiet           bool
	dryRun          bool
}

var askOpts askOptions

var askCmd = &cobra.Command{
	Use: "ask [-m model] [-e effort] [-f file]... [-i image]... [-s system]... [--json] [prompt...]",
	Short: i18n.T("发起一次咨询，stdout 只输出模型正文（--json 时为单行 JSON）",
		"Ask once; stdout carries only the model reply (single-line JSON with --json)"),
	Long: i18n.T(`发起一次咨询。输入按「位置参数 → -f（按出现顺序）→ stdin 管道」拼接，
图片以 -i 追加在文本之后。别名自带故障转移链，失败自动换下一模型（stderr 提示）。示例：

  brain ask -m coding "审查这段代码的并发安全性"
  cat slow.log | brain ask -m reason "找出最慢的三处并解释原因"
  brain ask -m vision -i chart.png "这张图说明了什么"
  brain ask -m reason -e high --json "推演方案" | jq -r .content`, `Ask once. Input is assembled as "positional args → -f files (in order) → piped stdin",
images appended after the text. Aliases carry a fallback chain; on failure brain
switches to the next model automatically (noted on stderr). Examples:

  brain ask -m coding "review this code for concurrency issues"
  cat slow.log | brain ask -m reason "find the three slowest spots and explain why"
  brain ask -m vision -i chart.png "what does this chart tell"
  brain ask -m reason -e high --json "reason it out" | jq -r .content`),
	Args: cobra.ArbitraryArgs,
	RunE: runAsk,
}

func init() {
	askCmd.Flags().StringVarP(&askOpts.model, "model", "m", "",
		i18n.T("模型别名或字面模型 ID（缺省用 config 的 default_model）",
			"model alias or literal model ID (defaults to default_model from config)"))
	askCmd.Flags().StringArrayVarP(&askOpts.files, "file", "f", nil,
		i18n.T("文本文件，可多次", "text file, repeatable"))
	askCmd.Flags().StringArrayVarP(&askOpts.images, "image", "i", nil,
		i18n.T("图片（本地路径或 URL），可多次", "image (local path or URL), repeatable"))
	askCmd.Flags().StringArrayVarP(&askOpts.system, "system", "s", nil,
		i18n.T("系统提示词，可重复（以空行连接）", "system prompt, repeatable (joined by a blank line)"))
	askCmd.Flags().IntVar(&askOpts.timeout, "timeout", 0,
		i18n.T("整体超时秒数（含重试与故障转移，覆盖 config）",
			"overall timeout in seconds, incl. retries and fallback (overrides config)"))
	askCmd.Flags().Int64Var(&askOpts.maxInputBytes, "max-input-bytes", 0,
		i18n.T("输入文本上限字节数（覆盖 config）", "input text size limit in bytes (overrides config)"))
	askCmd.Flags().StringVarP(&askOpts.reasoningEffort, "reasoning-effort", "e", "",
		i18n.T("推理深度纯透传（档位因模型而异，如 low/medium/high/xhigh/max；不设则用各模型默认）",
			"reasoning effort, passed through as-is (model-specific tiers like low/medium/high/xhigh/max; unset = model default)"))
	askCmd.Flags().IntVar(&askOpts.maxOutputTokens, "max-output-tokens", 0,
		i18n.T("输出预算（max_tokens）纯透传：防思维链吃满输出预算、限制长输出；0=不传用网关默认",
			"output budget (max_tokens), passed through as-is: guards against thinking eating the whole budget, caps long output; 0 = omit, gateway default"))
	askCmd.Flags().StringArrayVar(&askOpts.extraBody, "extra-body", nil, extraBodyFlagsHelp())
	askCmd.Flags().StringArrayVar(&askOpts.extraHeaders, "extra-headers", nil, extraHeadersFlagsHelp())
	askCmd.Flags().BoolVarP(&askOpts.quiet, "quiet", "q", false,
		i18n.T("静默 stderr：关闭心跳/耗时/转移提示（警告与错误保留）",
			"quiet stderr: no heartbeat/elapsed/fallback chatter (warnings and errors kept)"))
	askCmd.Flags().BoolVar(&askOpts.dryRun, "dry-run", false,
		i18n.T("只组装并输出最终将发送的 messages（单行 JSON），不发起网络请求",
			"assemble and print the final messages (single-line JSON) without any network call"))
	askCmd.Flags().BoolVar(&askOpts.jsonOut, "json", false,
		i18n.T("单行 JSON 输出（成败恒定一个对象，含 usage/latency/error）",
			"single-line JSON output (always exactly one object, success or failure, with usage/latency/error)"))
	askCmd.Flags().BoolVar(&askOpts.stream, "stream", false,
		i18n.T("正文增量打印到 stdout（人类交互用；开始输出后禁用重试与故障转移）",
			"stream content to stdout incrementally (for humans; disables retry/fallback once output starts)"))
	askCmd.Flags().BoolVar(&askOpts.showThinking, "show-thinking", false,
		i18n.T("思维链走 stderr，stdout 仍只有正文（--stream 时思维链同步增量）",
			"print reasoning to stderr, stdout stays content-only (streams alongside --stream)"))
}

// heartbeatInterval 心跳间隔（DESIGN.md 3.2：长推理时的 stderr 进度提示）。
const heartbeatInterval = 15 * time.Second

// askJSON 是 --json 的输出结构（DESIGN.md 3.2：成败恒定一个对象，Agent 可无条件 jq）。
type askJSON struct {
	Content         string         `json:"content"`
	Thinking        string         `json:"thinking,omitempty"`
	Model           string         `json:"model"`
	Requested       string         `json:"requested_model"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Usage           *gateway.Usage `json:"usage"`
	LatencyMs       int64          `json:"latency_ms"`
	FinishReason    string         `json:"finish_reason"`
	Error           *askJSONError  `json:"error"`
}

// askJSONError 扩展字段（第 0 节：纯增量，code/message 语义不变）：
// type 为机器可读分类，retryable 表示「重新发起同样调用是否可能成功」，
// upstream_status 为网关 HTTP 状态码，attempted_models 为 fallback 链实际尝试过的模型。
type askJSONError struct {
	Code            int      `json:"code"`
	Type            string   `json:"type"`
	Message         string   `json:"message"`
	Retryable       bool     `json:"retryable"`
	UpstreamStatus  int      `json:"upstream_status,omitempty"`
	AttemptedModels []string `json:"attempted_models,omitempty"`
}

func runAsk(cmd *cobra.Command, args []string) (retErr error) {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	// --stream 是人类交互模式，--json 是机器解析模式，二者互斥
	if askOpts.stream && askOpts.jsonOut {
		return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("--stream 与 --json 互斥", "--stream and --json are mutually exclusive"))
	}
	// --dry-run 自身输出 JSON，且不发请求，与两者均互斥
	if askOpts.dryRun && askOpts.stream {
		return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("--dry-run 与 --stream 互斥", "--dry-run and --stream are mutually exclusive"))
	}
	if askOpts.dryRun && askOpts.jsonOut {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.T("--dry-run 与 --json 互斥（--dry-run 自身输出单行 JSON）", "--dry-run and --json are mutually exclusive (--dry-run itself prints single-line JSON)"))
	}
	if askOpts.maxOutputTokens < 0 {
		return exitcode.Fail(stderr, exitcode.Usage, "%s", i18n.T("--max-output-tokens 应为正整数", "--max-output-tokens must be a positive integer"))
	}

	// --extra-body / --extra-headers：通用透传口（保留字拒绝见 extras.go）
	var extraBody map[string]any
	var extraHeaders map[string]string
	if len(askOpts.extraBody) > 0 || len(askOpts.extraHeaders) > 0 {
		var exErr error
		extraBody, extraHeaders, exErr = parseExtraFlags(askOpts.extraBody, askOpts.extraHeaders)
		if exErr != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", exErr)
		}
	}

	// --json：无论成败（含配置/输入错误），stdout 恒定输出一个 JSON 对象。
	// engine/rawErr 在网关阶段赋值，供 error.type/retryable/upstream_status/attempted_models 精确填充。
	var (
		engine *app.AskEngine
		rawErr error
	)
	payload := askJSON{}
	defer func() {
		if !askOpts.jsonOut {
			return
		}
		if retErr != nil {
			code := exitcode.Usage
			var ee *exitcode.Error
			if errors.As(retErr, &ee) {
				code = ee.Code
			}
			e := &askJSONError{Code: code, Message: retErr.Error()}
			if rawErr != nil {
				e.Type, e.Retryable = errorTypeInfo(rawErr)
				var se *gateway.StatusError
				if errors.As(rawErr, &se) {
					e.UpstreamStatus = se.Status
				}
			} else {
				e.Type, e.Retryable = errorTypeForCode(code)
			}
			if engine != nil {
				e.AttemptedModels = engine.Attempted
			}
			payload.Error = e
		}
		if b, err := json.Marshal(payload); err == nil {
			fmt.Fprintln(stdout, string(b)) // --json 写点，与正文写点互斥
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

	// CLI flags 最后覆盖（优先级：flags > 环境变量 > config.yaml > 默认值）
	if askOpts.timeout > 0 {
		cfg.TimeoutSeconds = askOpts.timeout
	}
	if askOpts.maxInputBytes > 0 {
		cfg.MaxInputBytes = askOpts.maxInputBytes
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

	name := askOpts.model
	if name == "" {
		name = cfg.DefaultModel
	}
	if name == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("未指定模型，修复：-m <别名或模型ID> 或 brain config set default_model <别名>",
				"no model specified; fix: -m <alias-or-model-id> or brain config set default_model <alias>"))
	}
	payload.Requested = name
	if askOpts.reasoningEffort != "" {
		payload.ReasoningEffort = askOpts.reasoningEffort
	}
	if askOpts.maxOutputTokens > 0 {
		payload.MaxOutputTokens = askOpts.maxOutputTokens
	}

	chain, err := app.ModelChain(name, cfg.Aliases)
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}

	messages, textBytes, err := assembleMessages(args, askOpts.files, askOpts.images, askOpts.system, cfg.MaxInputBytes)
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

	// --dry-run：输出组装结果（请求形状），不发起网络请求（本地图片 data URL 截断展示）
	if askOpts.dryRun {
		out, _ := json.Marshal(dryRunOut{
			Requested:       name,
			Chain:           chain,
			ReasoningEffort: askOpts.reasoningEffort,
			MaxOutputTokens: askOpts.maxOutputTokens,
			ExtraBody:       extraBody,
			ExtraHeaders:    extraHeaders,
			TextBytes:       textBytes,
			Messages:        redactMessages(messages),
		})
		fmt.Fprintln(stdout, string(out))
		return nil
	}

	// 总预算 = timeout_seconds，覆盖全部尝试与退避等待（DESIGN.md 3.4）
	budget := time.Duration(cfg.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(cmd.Context(), budget)
	defer cancel()

	// SIGINT/SIGTERM → exit 130（区别于超时的 exit 4）
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

	// 心跳：长推理每 15s 报一次进度（stderr）；--quiet 时不启动
	var gotChars atomic.Int64
	hbDone := make(chan struct{})
	hbStop := sync.OnceFunc(func() { close(hbDone) })
	if !askOpts.quiet {
		go func() {
			ticker := time.NewTicker(heartbeatInterval)
			defer ticker.Stop()
			start := time.Now()
			for {
				select {
				case <-hbDone:
					return
				case <-ticker.C:
					fmt.Fprintf(stderr, "brain: waiting for model… %.0fs | ~%s chars\n",
						time.Since(start).Seconds(), humanCount(gotChars.Load()))
				}
			}
		}()
	}

	start := time.Now()
	// --quiet：引擎的重试/转移提示也归非必要 stderr，一并静默（错误与警告仍走 stderr）
	engineStderr := io.Writer(stderr)
	if askOpts.quiet {
		engineStderr = io.Discard
	}
	engine = &app.AskEngine{
		Client:   gateway.New(cfg.BaseURL, cfg.APIKey, budget),
		Stderr:   engineStderr,
		RetryMax: cfg.RetryMax,
	}

	// --stream：正文增量直写 stdout（人类交互），一旦写出即禁止重试/切换；
	// --show-thinking 配合时思维链同步增量到 stderr。
	streamed := false
	var onDelta gateway.DeltaHandler
	if askOpts.stream {
		onDelta = func(content, reasoning string) {
			if reasoning != "" && askOpts.showThinking {
				fmt.Fprint(stderr, reasoning)
			}
			if content != "" {
				fmt.Fprint(stdout, content)
				streamed = true
			}
		}
	} else {
		onDelta = func(content, reasoning string) { gotChars.Add(int64(len(content) + len(reasoning))) }
	}
	engine.HasOutput = func() bool { return streamed }

	res, used, err := engine.Ask(ctx, chain,
		func(m string) gateway.ChatRequest {
			return gateway.ChatRequest{
				Model:           m,
				Messages:        messages,
				ReasoningEffort: askOpts.reasoningEffort,
				MaxTokens:       askOpts.maxOutputTokens,
				ExtraBody:       extraBody,
				ExtraHeaders:    extraHeaders,
			}
		},
		onDelta)
	hbStop()

	if err != nil {
		select {
		case <-interrupted:
			return exitcode.Fail(stderr, exitcode.Interrupt, "%s", i18n.T("被中断", "interrupted"))
		default:
		}
		payload.LatencyMs = time.Since(start).Milliseconds()
		rawErr = err // 供 --json 的 defer 做细分类
		// 模型不可用时给出相近建议（若有缓存可用）；别名与链上模型都作种子
		var se *gateway.StatusError
		if errors.As(err, &se) && app.IsModelError(se) {
			if cands := app.Suggest(append([]string{name}, chain...), cfg.Aliases, app.ReadCachedModels()); len(cands) > 0 {
				fmt.Fprintf(stderr, "brain: %s %s\n", i18n.T("相近的可用模型:", "similar available models:"), strings.Join(cands, ", "))
			}
		}
		return exitcode.Fail(stderr, exitCodeFor(err), "%v", err)
	}

	payload.Content = res.Content
	payload.Thinking = res.Thinking
	payload.Model = used
	payload.Usage = res.Usage
	payload.LatencyMs = time.Since(start).Milliseconds()
	payload.FinishReason = res.FinishReason

	if res.Content == "" {
		// 真实场景：思维链把输出预算耗尽（finish_reason=length）导致正文为空
		note := ""
		if res.Thinking != "" {
			note = i18n.Tf("，收到思维链 ~%s 字符（--json 可读取 thinking）",
				"; got ~%s chars of reasoning (readable as thinking via --json)", humanCount(int64(len(res.Thinking))))
		}
		fmt.Fprintf(stderr, "brain: %s %s（finish_reason=%s%s）\n",
			i18n.T("警告:", "warning:"),
			i18n.T("模型返回空内容", "model returned empty content"), res.FinishReason, note)
	}

	// 输出阶段：正文/JSON 两个互斥写点，全程序仅此一处写 stdout（DESIGN.md 3.2）
	switch {
	case askOpts.stream:
		// 增量已写出，只补齐换行约定
		if streamed && !strings.HasSuffix(res.Content, "\n") {
			fmt.Fprint(stdout, "\n")
		}
	case askOpts.jsonOut:
		// 由函数开头的 defer 输出单对象
	default:
		if askOpts.showThinking && res.Thinking != "" {
			dash := i18n.T("——", "--")
			fmt.Fprintf(stderr, "brain: %s %s%s~%s %s%s\n%s\n",
				dash, i18n.T("思维链", "reasoning"), i18n.T("（", " ("), humanCount(int64(len(res.Thinking))), i18n.T("字符", "chars"), i18n.T("）——", ") --"), res.Thinking)
		}
		if res.Content != "" {
			fmt.Fprint(stdout, ensureSingleNewline(res.Content))
		}
	}
	fallbackNote := ""
	if used != chain[0] {
		fallbackNote = i18n.T("（已故障转移）", " (failed over)")
	}
	if !askOpts.quiet {
		fmt.Fprintf(stderr, "brain: %.1fs | %s %s%s\n", time.Since(start).Seconds(),
			i18n.T("模型", "model"), used, fallbackNote)
	}
	return nil
}

// dryRunOut 是 --dry-run 的输出结构：组装完成后、发送前的请求形状。
type dryRunOut struct {
	Requested       string            `json:"requested_model"`
	Chain           []string          `json:"chain"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	ExtraBody       map[string]any    `json:"extra_body,omitempty"`
	ExtraHeaders    map[string]string `json:"extra_headers,omitempty"`
	TextBytes       int64             `json:"text_bytes"`
	Messages        []gateway.Message `json:"messages"`
}

// redactMessages 复制 messages 并把本地图片的 data URL 截断为前缀 + 省略字节数
// （--dry-run 是给人/CI 看的，完整 base64 动辄数 MB，没有信息量）。
func redactMessages(msgs []gateway.Message) []gateway.Message {
	out := make([]gateway.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		parts, ok := out[i].Content.([]gateway.ContentPart)
		if !ok {
			continue
		}
		np := make([]gateway.ContentPart, len(parts))
		copy(np, parts)
		for j := range np {
			if np[j].ImageURL == nil {
				continue
			}
			if u := np[j].ImageURL.URL; strings.HasPrefix(u, "data:") && len(u) > 160 {
				np[j].ImageURL = &gateway.ImageURL{
					URL: u[:96] + i18n.Tf("…（省略 %d 字节）", "... (%d bytes omitted)", len(u)-96),
				}
			}
		}
		out[i].Content = np
	}
	return out
}

// ensureSingleNewline 保证正文以恰好一个换行结尾（DESIGN.md 3.2 换行约定）。
func ensureSingleNewline(s string) string {
	return strings.TrimRight(s, "\n") + "\n"
}

// humanCount 心跳里的字符数显示：<1000 原样，否则 k 计。
func humanCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// humanBytes 给 Agent/人类都易读的字节数。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
