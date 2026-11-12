package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/decide"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

type decideOptions struct {
	model        string
	questions    []string // -Q <json|@file>，可重复有序合并
	nouls        []string // --noul <id>=<指令>，可重复速记
	files        []string // -f：state 附件（ask 同款 XML 组装）
	timeout      int
	quiet        bool
	jsonOut      bool
	dryRun       bool
	extraBody    []string
	extraHeaders []string
}

var decideOpts decideOptions

var decideCmd = &cobra.Command{
	Use: "decide [state] [-Q questions.json] [--noul id=指令]",
	Short: i18n.T("决策原语：System One 类型化判断（是/否、单选、评分），answers JSON 直出",
		"decision primitive: System One typed judgments (yes/no, choice, score); answers JSON on stdout"),
	Long: i18n.T(`对一段 state（证据）问一组类型化问题，返回结构化答案（值+概率+可选置信度），零散文。
协议仅 TypeSafe wire（POST /v1/systemone）；不兼容 OpenAI/Gemini 的 decisions 方言。

输入双通道：
  state    = 位置参数 + 管道 stdin + -f 多文件（与 ask 同款装配，多来源 XML 区隔）
  questions = -Q <json|@file>（systemone 原生 map 形态，可重复有序合并）
             + --noul <id>=<指令>（可重复速记；按首个 = 切分）
  同一 map 内 ID 冲突 → exit 1；choice/score 一律走 -Q（有序数组无单行速记语法）。

输出：默认 stdout 为紧凑 answers JSON（服务端原样字节，可直接 jq）；--json 时为
恒定单对象外壳（requested_model/model/answers/usage/latency_ms/error 与 ask 同构）。
类型化仅保证格式合法，不免疫类型化错误答案——只用于可逆的排序与分流。

配置段（model 必填不回退 default_model；base_url/api_key 缺省兜底顶层；无 type）：

  decide:
    model: jev-latest
    base_url: ""   # 可选
    api_key: ""    # 可选

重试：同模型（429/5xx/网络），不跨模型故障转移——概率校准基准不可静默漂移。
图片不经内置语法：逃生口 --extra-body '{"assets":…}' +
--extra-headers '{"TypeSafe-Danger-Zone-Images":"true"}'（保留字 state/questions/model）。`,
		`Ask a set of typed questions about a state (evidence) and get structured
answers (values + probabilities + optional confidence) — zero prose.
Protocol is TypeSafe wire only (POST /v1/systemone); OpenAI/Gemini decisions
dialects are not supported.

Two input channels:
  state     = positional args + piped stdin + -f files (assembled like ask,
              multi-source XML tagging included)
  questions = -Q <json|@file> (native systemone map form, repeatable, merged
              in order) + --noul <id>=<instruction> (repeatable shorthand;
              split on the first =)
  Duplicate IDs within the merged map -> exit 1; choice/score go through -Q
  only (ordered arrays have no single-line shorthand).

Output: by default stdout carries the compact answers JSON (server bytes
verbatim, jq-ready); --json wraps it in the usual constant single object
(requested_model/model/answers/usage/latency_ms/error, shaped like ask).
Typed output guarantees format validity only — it does not immunize against
well-typed wrong answers. Use decide for reversible routing and ranking only.

Config section (model required, never falls back to default_model;
base_url/api_key fall back to top-level; no type key):

  decide:
    model: jev-latest
    base_url: ""   # optional
    api_key: ""    # optional

Retries: same model only (429/5xx/network), never cross-model failover —
probability calibration baselines must not silently drift. Images have no
built-in syntax: escape hatch --extra-body '{"assets":…}' +
--extra-headers '{"TypeSafe-Danger-Zone-Images":"true"}'
(reserved keys: state/questions/model).`),
	RunE: runDecide,
}

func init() {
	decideCmd.Flags().StringVarP(&decideOpts.model, "model", "m", "",
		i18n.T("决策模型 ID 或别名（缺省用 config.yaml 的 decide.model）",
			"decision model ID or alias (defaults to decide.model in config.yaml)"))
	decideCmd.Flags().StringArrayVarP(&decideOpts.questions, "questions", "Q", nil,
		i18n.T("questions JSON 对象（systemone 原生 map：id→{type,instructions,criteria}；内联或 @file，可重复有序合并）",
			"questions JSON object (native systemone map: id→{type,instructions,criteria}; inline or @file, repeatable, merged in order)"))
	decideCmd.Flags().StringArrayVar(&decideOpts.nouls, "noul", nil,
		i18n.T("是/否问题速记 <id>=<指令>（可重复；按首个 = 切分）",
			"yes/no question shorthand <id>=<instruction> (repeatable; split on the first =)"))
	decideCmd.Flags().StringArrayVarP(&decideOpts.files, "file", "f", nil,
		i18n.T("state 附件（可重复；与 ask 同款 <attachment> XML 组装）",
			"state attachments (repeatable; assembled into <attachment> XML like ask)"))
	decideCmd.Flags().IntVar(&decideOpts.timeout, "timeout", 0,
		i18n.T("秒数上限（缺省用 timeout_seconds）", "timeout in seconds (defaults to timeout_seconds)"))
	decideCmd.Flags().BoolVarP(&decideOpts.quiet, "quiet", "q", false,
		i18n.T("关闭计时行等非必要 stderr（警告与错误保留）", "silence non-essential stderr like the timing line (warnings and errors stay)"))
	decideCmd.Flags().BoolVar(&decideOpts.jsonOut, "json", false,
		i18n.T("恒定单对象外壳输出（机器可读）", "constant single-object envelope output (machine-readable)"))
	decideCmd.Flags().BoolVar(&decideOpts.dryRun, "dry-run", false,
		i18n.T("输出最终请求形状（单行 JSON），不发请求不花钱", "print the final request shape (single-line JSON); no request, no cost"))
	decideCmd.Flags().StringArrayVar(&decideOpts.extraBody, "extra-body", nil,
		i18n.T("附加请求体参数（JSON 对象或 @file）；图片经 assets 走此通道；保留字 state/questions/model 拒绝",
			"extra request-body fields (JSON object or @file); images ride the assets key here; reserved keys state/questions/model are rejected"))
	decideCmd.Flags().StringArrayVar(&decideOpts.extraHeaders, "extra-headers", nil,
		i18n.T("附加 HTTP 头（JSON 对象或 @file）；图片解锁需 TypeSafe-Danger-Zone-Images",
			"extra HTTP headers (JSON object or @file); images need TypeSafe-Danger-Zone-Images"))
}

// decideJSON 是 decide --json 的输出结构（成败恒定一个对象，与 ask --json 同构）。
type decideJSON struct {
	Requested string          `json:"requested_model"`
	Model     string          `json:"model"`
	Answers   json.RawMessage `json:"answers,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	LatencyMs int64           `json:"latency_ms"`
	Error     *askJSONError   `json:"error"`
}

func runDecide(cmd *cobra.Command, args []string) (retErr error) {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	if decideOpts.dryRun && decideOpts.jsonOut {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.T("--dry-run 与 --json 互斥（--dry-run 自身输出单行 JSON）", "--dry-run and --json are mutually exclusive (--dry-run itself prints single-line JSON)"))
	}

	// --json：无论成败，stdout 恒定输出一个 JSON 对象（rawErr 供 error.type 精确填充）
	var rawErr error
	payload := decideJSON{}
	defer func() {
		if !decideOpts.jsonOut {
			return
		}
		if retErr != nil {
			code := exitcode.Usage
			var ee *exitcode.Error
			if errors.As(retErr, &ee) {
				code = ee.Code
			}
			e := &askJSONError{Code: code, Message: retErr.Error()}
			switch {
			case rawErr != nil:
				e.Type, e.Retryable = errorTypeInfo(rawErr)
				var se *gateway.StatusError
				if errors.As(rawErr, &se) {
					e.UpstreamStatus = se.Status
				}
			default:
				e.Type, e.Retryable = errorTypeForCode(code)
			}
			payload.Error = e
		}
		if b, err := json.Marshal(payload); err == nil {
			fmt.Fprintln(stdout, string(b))
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
	if decideOpts.timeout > 0 {
		cfg.TimeoutSeconds = decideOpts.timeout
	}

	// ---- 配置装配：decide 段缺省兜底顶层（第 0 节 decide 行；单方言无 type） ----
	baseURL := cfg.Decide.BaseURL
	if baseURL == "" {
		baseURL = cfg.BaseURL
	}
	apiKey := cfg.Decide.APIKey
	if apiKey == "" {
		apiKey = cfg.APIKey
	}
	name := decideOpts.model
	if name == "" {
		name = cfg.Decide.Model
	}
	if name == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("未设置决策模型，修复：brain config set decide.model <模型ID>（或本次 -m <模型ID>）；decide 不回退 default_model",
				"no decision model set; fix: brain config set decide.model <model-id> (or pass -m <model-id>); decide never falls back to default_model"))
	}
	model, err := app.ResolveModel(name, cfg.Aliases)
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	if baseURL == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("缺少 base_url，修复：brain config set decide.base_url <https://...>（缺省时用顶层 base_url）",
				"missing base_url; fix: brain config set decide.base_url <https://...> (falls back to top-level base_url)"))
	}
	if apiKey == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("缺少 api_key，修复：brain config set decide.api_key <sk-...>（缺省时用顶层 api_key）",
				"missing api_key; fix: brain config set decide.api_key <sk-...> (falls back to top-level api_key)"))
	}

	// ---- state 装配：ask 同款（positional → -f → stdin，多来源 XML 区隔全适用） ----
	state, _, err := app.Assemble(args, decideOpts.files, stdin, stdinPiped, cfg.MaxInputBytes)
	if err != nil {
		var tl *app.TooLargeError
		if errors.As(err, &tl) {
			return exitcode.Fail(stderr, exitcode.InputTooLarge, "%s",
				i18n.Tf("输入 %s 超过上限 %s（--max-input-bytes 可调）",
					"input %s exceeds limit %s (adjustable via --max-input-bytes)", humanBytes(tl.Size), humanBytes(tl.Limit)))
		}
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	if state == "" {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.T("缺少 state：需要位置参数、-f 文件或管道 stdin 至少其一（state 是被判断的证据，问题在 -Q/--noul）",
				"missing state: provide at least one of positional args, -f files, or piped stdin (state is the evidence being judged; questions go in -Q/--noul)"))
	}

	// ---- questions 装配：-Q 有序合并 + --noul 速记（ID 冲突 exit 1） ----
	qb := &decide.QuestionBuilder{}
	for _, q := range decideOpts.questions {
		rawBytes := []byte(q)
		if after, ok := strings.CutPrefix(q, "@"); ok {
			b, rerr := os.ReadFile(after)
			if rerr != nil {
				return exitcode.Fail(stderr, exitcode.Usage, "%s: %v",
					i18n.T("读取 questions 文件失败", "failed to read questions file"), rerr)
			}
			rawBytes = b
		}
		if qerr := qb.AddJSON(rawBytes); qerr != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", qerr)
		}
	}
	for _, n := range decideOpts.nouls {
		id, instr, found := strings.Cut(n, "=")
		if !found || strings.TrimSpace(id) == "" || strings.TrimSpace(instr) == "" {
			return exitcode.Fail(stderr, exitcode.Usage, "%s: %q（%s）",
				i18n.T("--noul 格式应为 <id>=<指令>", "--noul expects <id>=<instruction>"), n,
				i18n.T("按首个 = 切分，id 与指令非空", "split on the first =; both id and instruction non-empty"))
		}
		if qerr := qb.AddNoul(id, instr); qerr != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", qerr)
		}
	}
	questions, qerr := qb.Raw()
	if qerr != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", qerr)
	}
	if questions == nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.T("缺少 questions：需要 -Q <json|@file> 或 --noul <id>=<指令> 至少其一",
				"missing questions: provide at least one of -Q <json|@file> or --noul <id>=<instruction>"))
	}

	// ---- extras：逃生口（保留字 state/questions/model；图片走 assets + Danger-Zone 头） ----
	var extraBody map[string]any
	var extraHeaders map[string]string
	if len(decideOpts.extraBody) > 0 || len(decideOpts.extraHeaders) > 0 {
		if extraBody, extraHeaders, err = parseDecideExtras(decideOpts.extraBody, decideOpts.extraHeaders); err != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
		}
	}

	req := &decide.Request{Model: model, State: state, Questions: questions,
		ExtraBody: extraBody, ExtraHeaders: extraHeaders}
	client := decide.New(baseURL, apiKey, time.Duration(cfg.TimeoutSeconds)*time.Second)
	payload.Requested, payload.Model = name, model

	// --dry-run：输出最终请求形状（endpoint 不含鉴权参数），不发请求
	if decideOpts.dryRun {
		out, _ := json.Marshal(decideDryRun{
			Requested: name, Model: model, Endpoint: client.Endpoint(),
			State: state, Questions: questions,
			ExtraBody: extraBody, ExtraHeaders: extraHeaders,
		})
		fmt.Fprintln(stdout, string(out))
		return nil
	}

	// 总预算 = timeout_seconds（决策模型亚秒级，预算主要兜异常端点）
	budget := time.Duration(cfg.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(cmd.Context(), budget)
	defer cancel()

	// SIGINT/SIGTERM → exit 130（区别于超时的 exit 4；draw 同构）
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

	start := time.Now()
	res, derr := client.Do(ctx, req)
	payload.LatencyMs = time.Since(start).Milliseconds()
	select {
	case <-interrupted: // SIGINT 优先于错误归一（区别于超时的 exit 4）
		return exitcode.Fail(stderr, exitcode.Interrupt, "%s", i18n.T("被中断", "interrupted"))
	default:
	}
	if derr != nil {
		rawErr = derr
		return exitcode.Fail(stderr, exitCodeFor(derr), "%v", derr)
	}

	if !decideOpts.quiet {
		m := res.Model
		if m == "" {
			m = model
		}
		fmt.Fprintf(stderr, "brain: %s | model %s\n", humanMillis(payload.LatencyMs), m)
	}
	if res.Model != "" {
		payload.Model = res.Model
	}
	payload.Answers, payload.Usage = res.Answers, res.Usage
	if !decideOpts.jsonOut {
		fmt.Fprintln(stdout, string(res.Answers)) // 非 --json 的 stdout 写点：answers 原样字节（与 --json 写点互斥）
	}
	return nil
}

// decideDryRun --dry-run 的输出结构。
type decideDryRun struct {
	Requested    string            `json:"requested_model"`
	Model        string            `json:"model"`
	Endpoint     string            `json:"endpoint"` // 不含鉴权参数
	State        string            `json:"state"`
	Questions    json.RawMessage   `json:"questions"`
	ExtraBody    map[string]any    `json:"extra_body,omitempty"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
}

// parseDecideExtras decide 版 extras 统一入口：保留字与 chat/draw 不同（无快车道）。
func parseDecideExtras(bodyVals, headerVals []string) (map[string]any, map[string]string, error) {
	var body map[string]any
	var headers map[string]string
	var err error
	if len(bodyVals) > 0 {
		if body, err = parseExtrasJSON(bodyVals); err != nil {
			return nil, nil, err
		}
		if err = rejectReservedHint(body, map[string]bool{
			"state": true, "questions": true, "model": true,
		}, "--extra-body",
			i18n.T("state 用位置参数/-f/管道，questions 用 -Q/--noul，模型用 -m/decide.model",
				"use positional/-f/pipe for state, -Q/--noul for questions, and -m/decide.model for the model")); err != nil {
			return nil, nil, err
		}
	}
	if len(headerVals) > 0 {
		var raw map[string]any
		if raw, err = parseExtrasJSON(headerVals); err != nil {
			return nil, nil, err
		}
		if err = rejectReservedHint(raw, reservedHeaderKeys, "--extra-headers",
			i18n.T("Authorization 用 api_key 配置", "use the api_key config for Authorization")); err != nil {
			return nil, nil, err
		}
		if headers, err = parseExtraHeaders(headerVals); err != nil {
			return nil, nil, err
		}
	}
	return body, headers, nil
}
