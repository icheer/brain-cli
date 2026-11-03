package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/draw"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

type drawOptions struct {
	model   string
	images  []string
	aspect  string
	res     string
	timeout int
	quiet   bool
	jsonOut bool
	dryRun  bool
}

var drawOpts drawOptions

var drawCmd = &cobra.Command{
	Use: "draw [prompt] [-m model] [-i ref.png] [--aspect 16:9] [--res 2K]",
	Short: i18n.T("图像生成：双协议（gemini 原生 / OpenAI Images，按模型名推断），自动落盘并打印路径",
		"image generation: dual protocol (Gemini native / OpenAI Images, inferred from the model name); auto-saves and prints the path"),
	Long: i18n.T(`调用生图模型画一张图（v1 固定 n=1，不做自动重试——图像按张计费，失败凭退出码人工重发）。

协议按模型名推断：含 gemini → Gemini 原生（:generateContent），其余 → OpenAI Images
（/v1/images/generations）；config.yaml 的 draw.type（gemini|openai）可覆盖推断。
配置段（base_url/api_key 缺省兜底顶层，末尾 /v1、/v1beta 可写可不写）：

  draw:
    model: gemini-3.1-flash-image   # 必填（不回退 default_model）
    base_url: ""                    # 可选，缺省用顶层 base_url
    api_key: ""                     # 可选，缺省用顶层 api_key
    type: ""                        # 可选，覆盖协议推断

输入：位置参数为 prompt（多个以空行连接）；管道 stdin 追加在其后；
-i 参考图仅本地文件（gemini=inlineData，openai=切 /v1/images/edits）。

--aspect/--res 为统一语义，两协议各自翻译：
  aspect: auto|1:1|16:9|9:16|4:3|3:4|3:2|2:3|5:4|4:5|21:9
          （openai 映射为 size 桶 1024x1024/1536x1024/1024x1536，gemini 直传 aspectRatio）
  res:    auto|1K|2K|4K（openai 映射为 quality low/medium/high，gemini 直传 imageSize）

输出：图片自动落盘（优先当前目录，不可写退系统临时目录），stdout 只打印保存路径
一行；--json 时为恒定单对象（saved_path/mime/usage/latency/error 与 ask --json 同构）。
gemini 响应中模型附带的文字走 stderr。退出码与 ask 一致（0-7/130）。`,
		`Call an image model to draw one image (v1 is fixed to n=1 with no auto-retry —
images are billed per piece; on failure re-send manually per the exit code).

Protocol is inferred from the model name: containing "gemini" -> Gemini native
(:generateContent); everything else -> OpenAI Images (/v1/images/generations).
The draw.type key (gemini|openai) in config.yaml overrides the inference.
Config section (base_url/api_key fall back to the top-level values; a trailing
/v1 or /v1beta is optional):

  draw:
    model: gemini-3.1-flash-image   # required (never falls back to default_model)
    base_url: ""                    # optional; falls back to top-level base_url
    api_key: ""                     # optional; falls back to top-level api_key
    type: ""                        # optional; overrides protocol inference

Input: positional args form the prompt (joined by blank lines); piped stdin is
appended after them; -i reference images accept local files only (gemini ->
inlineData, openai -> switches to /v1/images/edits).

--aspect/--res are unified semantics translated per protocol:
  aspect: auto|1:1|16:9|9:16|4:3|3:4|3:2|2:3|5:4|4:5|21:9
          (openai maps to size buckets 1024x1024/1536x1024/1024x1536;
           gemini passes through as aspectRatio)
  res:    auto|1K|2K|4K (openai maps to quality low/medium/high; gemini passes
          through as imageSize)

Output: the image is auto-saved (cwd first, system temp dir as fallback) and
stdout prints exactly the saved path; with --json a single constant object is
emitted instead (saved_path/mime/usage/latency/error shaped like ask --json).
Gemini side-text (model commentary) goes to stderr. Exit codes match ask (0-7/130).`),
	RunE: runDraw,
}

func init() {
	drawCmd.Flags().StringVarP(&drawOpts.model, "model", "m", "",
		i18n.T("生图模型 ID 或别名（缺省用 config.yaml 的 draw.model）",
			"image model ID or alias (defaults to draw.model in config.yaml)"))
	drawCmd.Flags().StringArrayVarP(&drawOpts.images, "image", "i", nil,
		i18n.T("参考图（可重复，仅本地文件；gemini=inlineData 参考图，openai=切 /v1/images/edits）",
			"reference image (repeatable, local files only; gemini -> inlineData parts, openai -> /v1/images/edits)"))
	drawCmd.Flags().StringVar(&drawOpts.aspect, "aspect", "auto",
		i18n.T("画幅：auto|1:1|16:9|9:16|4:3|3:4|3:2|2:3|5:4|4:5|21:9",
			"aspect ratio: auto|1:1|16:9|9:16|4:3|3:4|3:2|2:3|5:4|4:5|21:9"))
	drawCmd.Flags().StringVar(&drawOpts.res, "res", "auto",
		i18n.T("分辨率：auto|1K|2K|4K", "resolution: auto|1K|2K|4K"))
	drawCmd.Flags().IntVar(&drawOpts.timeout, "timeout", 0,
		i18n.T("秒数上限（缺省用 timeout_seconds）", "timeout in seconds (defaults to timeout_seconds)"))
	drawCmd.Flags().BoolVarP(&drawOpts.quiet, "quiet", "q", false,
		i18n.T("关闭心跳/耗时等非必要 stderr（警告与错误保留）", "silence non-essential stderr like heartbeat/timing (warnings and errors stay)"))
	drawCmd.Flags().BoolVar(&drawOpts.jsonOut, "json", false,
		i18n.T("单行 JSON 输出（机器可读）", "single-line JSON output (machine-readable)"))
	drawCmd.Flags().BoolVar(&drawOpts.dryRun, "dry-run", false,
		i18n.T("输出最终请求形状（单行 JSON），不发请求不花钱", "print the final request shape (single-line JSON); no request, no cost"))
}

// drawJSON 是 draw --json 的输出结构（成败恒定一个对象，与 ask --json 同构）。
type drawJSON struct {
	Model         string        `json:"model"`
	Requested     string        `json:"requested_model"`
	Type          string        `json:"type"`
	SavedPath     string        `json:"saved_path,omitempty"`
	MIME          string        `json:"mime,omitempty"`
	Bytes         int           `json:"bytes,omitempty"`
	ImagesTotal   int           `json:"images_total,omitempty"`
	RevisedPrompt string        `json:"revised_prompt,omitempty"`
	Usage         *draw.Usage   `json:"usage,omitempty"`
	LatencyMs     int64         `json:"latency_ms"`
	Error         *askJSONError `json:"error"`
}

func runDraw(cmd *cobra.Command, args []string) (retErr error) {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	if drawOpts.dryRun && drawOpts.jsonOut {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.T("--dry-run 与 --json 互斥（--dry-run 自身输出单行 JSON）", "--dry-run and --json are mutually exclusive (--dry-run itself prints single-line JSON)"))
	}

	// --json：无论成败，stdout 恒定输出一个 JSON 对象（rawErr 供 error.type 精确填充）
	var rawErr error
	payload := drawJSON{}
	defer func() {
		if !drawOpts.jsonOut {
			return
		}
		if retErr != nil {
			code := exitcode.Usage
			var ee *exitcode.Error
			if errors.As(retErr, &ee) {
				code = ee.Code
			}
			e := &askJSONError{Code: code, Message: retErr.Error()}
			var ni *draw.NoImageError
			switch {
			case errors.As(rawErr, &ni):
				e.Type, e.Retryable = "upstream", true // 2xx 无图（多为安全拒绝/模型失灵），重发可能换一种结果
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
			fmt.Fprintln(stdout, string(b)) // --json 写点，与路径写点互斥
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
	if drawOpts.timeout > 0 {
		cfg.TimeoutSeconds = drawOpts.timeout
	}

	// ---- 配置装配：draw 段缺省兜底顶层（第 0 节 draw 行） ----
	baseURL := cfg.Draw.BaseURL
	if baseURL == "" {
		baseURL = cfg.BaseURL
	}
	apiKey := cfg.Draw.APIKey
	if apiKey == "" {
		apiKey = cfg.APIKey
	}
	name := drawOpts.model
	if name == "" {
		name = cfg.Draw.Model
	}
	if name == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("未设置生图模型，修复：brain config set draw.model <模型ID>（或本次 -m <模型ID>）；draw 不回退 default_model",
				"no image model set; fix: brain config set draw.model <model-id> (or pass -m <model-id>); draw never falls back to default_model"))
	}
	model, err := app.ResolveModel(name, cfg.Aliases)
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	typ := strings.ToLower(strings.TrimSpace(cfg.Draw.Type))
	if typ == "" {
		typ = draw.InferType(model)
	}
	if typ != draw.TypeGemini && typ != draw.TypeOpenAI {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.Tf("draw.type 仅支持 gemini|openai（留空按模型名推断），当前 %q；修复：brain config set draw.type gemini",
				"draw.type accepts gemini|openai only (empty = infer from model name), got %q; fix: brain config set draw.type gemini", cfg.Draw.Type))
	}
	if baseURL == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("缺少 base_url，修复：brain config set draw.base_url <https://...>（缺省时用顶层 base_url）",
				"missing base_url; fix: brain config set draw.base_url <https://...> (falls back to top-level base_url)"))
	}
	if apiKey == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%s",
			i18n.T("缺少 api_key，修复：brain config set draw.api_key <sk-...>（缺省时用顶层 api_key）",
				"missing api_key; fix: brain config set draw.api_key <sk-...> (falls back to top-level api_key)"))
	}

	// ---- 参数校验（统一语义，方言翻译在各 adapter） ----
	aspect := drawOpts.aspect
	if aspect != "auto" {
		if _, ok := draw.AspectSize[aspect]; !ok {
			return exitcode.Fail(stderr, exitcode.Usage, "%s",
				i18n.Tf("--aspect 取值非法: %q（合法：auto|1:1|16:9|9:16|4:3|3:4|3:2|2:3|5:4|4:5|21:9）",
					"invalid --aspect: %q (valid: auto|1:1|16:9|9:16|4:3|3:4|3:2|2:3|5:4|4:5|21:9)", aspect))
		}
	}
	res := strings.ToUpper(strings.TrimSpace(drawOpts.res))
	if res == "AUTO" {
		res = "auto"
	}
	if res != "auto" && !draw.ValidRes(res) {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.Tf("--res 取值非法: %q（合法：auto|1K|2K|4K）",
				"invalid --res: %q (valid: auto|1K|2K|4K)", drawOpts.res))
	}

	// ---- prompt 装配：位置参数 + 管道 stdin（draw 不做 -f/XML 标签——生图 prompt 是单一指令） ----
	var promptParts []string
	if len(args) > 0 {
		promptParts = append(promptParts, strings.Join(args, "\n\n"))
	}
	if stdinPiped && stdin != nil {
		data, _ := io.ReadAll(io.LimitReader(stdin, cfg.MaxInputBytes+1))
		if len(data) > 0 {
			promptParts = append(promptParts, string(data))
		}
	}
	prompt := strings.Join(promptParts, "\n\n")
	if prompt == "" {
		return exitcode.Fail(stderr, exitcode.Usage, "%s",
			i18n.T("缺少 prompt：需要位置参数或管道 stdin 至少其一", "missing prompt: provide positional args or piped stdin"))
	}
	if n := int64(len(prompt)); n > cfg.MaxInputBytes {
		return exitcode.Fail(stderr, exitcode.InputTooLarge, "%s",
			i18n.Tf("输入 %s 超过上限 %s（--max-input-bytes 可调）",
				"input %s exceeds limit %s (adjustable via --max-input-bytes)", humanBytes(n), humanBytes(cfg.MaxInputBytes)))
	}

	// ---- 参考图（-i，仅本地文件） ----
	var refs []draw.RefImage
	for _, spec := range drawOpts.images {
		data, mime, isURL, lerr := app.LoadImage(spec)
		if lerr != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", lerr)
		}
		if isURL {
			return exitcode.Fail(stderr, exitcode.Usage, "%s",
				i18n.Tf("参考图仅支持本地文件（URL 请先下载）: %s", "reference images accept local files only (download first): %s", spec))
		}
		refs = append(refs, draw.RefImage{MIME: mime, Data: data, Name: filepath.Base(spec)})
	}

	req := &draw.Request{Model: model, Prompt: prompt, Aspect: aspect, Res: res, Refs: refs}
	client := draw.New(baseURL, apiKey, typ, time.Duration(cfg.TimeoutSeconds)*time.Second)
	payload.Model, payload.Requested, payload.Type = model, name, typ

	// --dry-run：输出最终请求形状（endpoint 不含鉴权参数），不发请求
	if drawOpts.dryRun {
		out, _ := json.Marshal(drawDryRun{
			Requested: name, Model: model, Type: typ, Endpoint: client.Endpoint(req),
			Params: translateParams(typ, aspect, res), Prompt: prompt, Refs: refSummaries(refs),
		})
		fmt.Fprintln(stdout, string(out))
		return nil
	}

	// 总预算 = timeout_seconds（4K 生图 1-2 分钟属正常，默认 300s）
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

	// 心跳：生图耗时 30s+ 属常态，每 15s 报一次（stderr）；--quiet 时不启动
	hbDone := make(chan struct{})
	hbStop := sync.OnceFunc(func() { close(hbDone) })
	if !drawOpts.quiet {
		go func() {
			ticker := time.NewTicker(heartbeatInterval)
			defer ticker.Stop()
			start := time.Now()
			for {
				select {
				case <-hbDone:
					return
				case <-ticker.C:
					fmt.Fprintf(stderr, "brain: waiting for image… %.0fs\n", time.Since(start).Seconds())
				}
			}
		}()
	}
	if !drawOpts.quiet {
		fmt.Fprintf(stderr, "brain: draw · %s · %s · %s\n", model, typLabel(typ), client.Endpoint(req))
	}

	start := time.Now()
	resImg, gerr := client.Generate(ctx, req)
	hbStop()
	payload.LatencyMs = time.Since(start).Milliseconds()
	select {
	case <-interrupted: // SIGINT 优先于错误归一（区别于超时的 exit 4）
		return exitcode.Fail(stderr, exitcode.Interrupt, "%s", i18n.T("被中断", "interrupted"))
	default:
	}
	if gerr != nil {
		rawErr = gerr
		var ni *draw.NoImageError
		if errors.As(gerr, &ni) {
			return exitcode.Fail(stderr, exitcode.Upstream, "%v", ni)
		}
		return exitcode.Fail(stderr, exitCodeFor(gerr), "%v", gerr)
	}

	// ---- 落盘：优先当前目录，不可写退系统临时目录 ----
	path, serr := saveImage(resImg.Image, resImg.MIME, stderr)
	if serr != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%s: %v",
			i18n.T("图片落盘失败", "failed to save the image"), serr)
	}
	if abs, aerr := filepath.Abs(path); aerr == nil {
		path = abs
	}

	if !drawOpts.quiet {
		fmt.Fprintf(stderr, "brain: %s %s（%s, %s, %s）\n",
			i18n.T("已保存", "saved"), path, humanBytes(int64(len(resImg.Image))), resImg.MIME, humanMillis(payload.LatencyMs))
		if resImg.ImagesTotal > 1 {
			fmt.Fprintf(stderr, "brain: %s\n",
				i18n.Tf("响应含 %d 张图，已保存第 1 张（v1 固定单张）", "response carried %d images; saved the first one (v1 is fixed to one image)", resImg.ImagesTotal))
		}
		if resImg.RevisedPrompt != "" {
			fmt.Fprintf(stderr, "brain: revised_prompt: %s\n", truncateLine(resImg.RevisedPrompt, 200))
		}
		if resImg.Text != "" {
			fmt.Fprintf(stderr, "brain: %s %s\n", i18n.T("模型附言:", "model note:"), truncateLine(resImg.Text, 300))
		}
	}

	payload.SavedPath, payload.MIME = path, resImg.MIME
	payload.Bytes = len(resImg.Image)
	payload.ImagesTotal = resImg.ImagesTotal
	payload.RevisedPrompt = resImg.RevisedPrompt
	payload.Usage = resImg.Usage
	if !drawOpts.jsonOut {
		fmt.Fprintln(stdout, path) // draw 非 --json 的 stdout 写点：恰一行保存路径（与 --json 写点互斥）
	}
	return nil
}

// ---- dry-run 输出 ----

type drawDryRun struct {
	Requested string            `json:"requested_model"`
	Model     string            `json:"model"`
	Type      string            `json:"type"`
	Endpoint  string            `json:"endpoint"` // 不含鉴权参数
	Params    map[string]string `json:"params"`
	Prompt    string            `json:"prompt"`
	Refs      []draw.RefSummary `json:"refs,omitempty"`
}

// translateParams 把统一语义翻成目标协议的实际参数（dry-run 可核对翻译结果）。
func translateParams(typ, aspect, res string) map[string]string {
	if typ == draw.TypeGemini {
		p := map[string]string{}
		if aspect != "auto" {
			p["aspectRatio"] = aspect
		}
		if res != "auto" {
			p["imageSize"] = res
		}
		return p
	}
	size, quality := "auto", "auto"
	if s, ok := draw.AspectSize[aspect]; ok {
		size = s
	}
	if q, ok := draw.ResQuality[res]; ok {
		quality = q
	}
	return map[string]string{"size": size, "quality": quality}
}

func refSummaries(refs []draw.RefImage) []draw.RefSummary {
	out := make([]draw.RefSummary, 0, len(refs))
	for _, r := range refs {
		out = append(out, draw.RefSummary{Name: r.Name, MIME: r.MIME, Bytes: len(r.Data)})
	}
	return out
}

// ---- 落盘 ----

// saveImage 自动落盘：优先当前工作目录，写不动退系统临时目录（stderr 提示一次）。
// 文件名 brain-draw-<时间戳><ext>，同秒冲突追加 -2/-3…；临时文件 + rename 原子落位。
func saveImage(data []byte, mime string, stderr io.Writer) (string, error) {
	var dirs []string
	if wd, err := os.Getwd(); err == nil && wd != "" {
		dirs = append(dirs, wd)
	}
	dirs = append(dirs, os.TempDir())
	var lastErr error
	for i, dir := range dirs {
		if i > 0 {
			fmt.Fprintf(stderr, "brain: %s\n",
				i18n.Tf("当前目录不可写，改存 %s", "cwd not writable; saving under %s instead", dir))
		}
		ext := extFor(data, mime)
		stamp := time.Now().Format("20060102-150405")
		for n := 0; n < 100; n++ {
			name := "brain-draw-" + stamp + ext
			if n > 0 {
				name = fmt.Sprintf("brain-draw-%s-%d%s", stamp, n+1, ext)
			}
			p := filepath.Join(dir, name)
			if _, serr := os.Stat(p); serr == nil {
				continue // 同秒已有同名文件：序号递增避让（rename 会静默覆盖，不能依赖它报错）
			}
			if werr := os.WriteFile(p+".tmp", data, 0o644); werr != nil {
				lastErr = werr
				break // 本目录写不动（权限/只读），换下一候选目录
			}
			if rerr := os.Rename(p+".tmp", p); rerr != nil {
				_ = os.Remove(p + ".tmp")
				lastErr = rerr
				break
			}
			return p, nil
		}
	}
	return "", lastErr
}

// extFor 由响应 mime/字节头推扩展名；推不出时用 .bin（不撒谎成 .png）。
func extFor(data []byte, mime string) string {
	m := mime
	if !strings.HasPrefix(m, "image/") {
		head := data
		if len(head) > 512 {
			head = head[:512]
		}
		if d := http.DetectContentType(head); strings.HasPrefix(d, "image/") {
			m = d
		}
	}
	switch m {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	if sub := strings.TrimPrefix(m, "image/"); sub != m && sub != "" {
		return "." + strings.ToLower(strings.ReplaceAll(sub, "/", "-"))
	}
	return ".bin"
}

// typLabel 协议的展示名（stderr 预检行用）。
func typLabel(typ string) string {
	if typ == draw.TypeGemini {
		return i18n.T("gemini 原生", "gemini native")
	}
	return "openai images"
}

// truncateLine 压成单行并截断（模型附言/revised_prompt 展示）。
func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
