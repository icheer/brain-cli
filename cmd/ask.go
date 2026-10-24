package cmd

import (
	"context"
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
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
)

type askOptions struct {
	model         string
	files         []string
	images        []string
	system        []string
	timeout       int
	maxInputBytes int64
}

var askOpts askOptions

var askCmd = &cobra.Command{
	Use:   "ask [-m model] [-f file]... [-i image]... [-s system]... [prompt...]",
	Short: "发起一次咨询，stdout 只输出模型正文",
	Long: `发起一次咨询。输入按「位置参数 → -f（按出现顺序）→ stdin 管道」拼接，
图片以 -i 追加在文本之后。示例：

  brain ask -m coding "审查这段代码的并发安全性"
  cat slow.log | brain ask -m reason "找出最慢的三处并解释原因"
  brain ask -m vision -i chart.png "这张图说明了什么"
  brain ask -s "你是严格的代码审查员" -f a.go -f b.go "对比两个实现"`,
	Args: cobra.ArbitraryArgs,
	RunE: runAsk,
}

func init() {
	askCmd.Flags().StringVarP(&askOpts.model, "model", "m", "", "模型别名或字面模型 ID（缺省用 config 的 default_model）")
	askCmd.Flags().StringArrayVarP(&askOpts.files, "file", "f", nil, "文本文件，可多次")
	askCmd.Flags().StringArrayVarP(&askOpts.images, "image", "i", nil, "图片（本地路径或 URL），可多次")
	askCmd.Flags().StringArrayVarP(&askOpts.system, "system", "s", nil, "系统提示词，可重复（以空行连接）")
	askCmd.Flags().IntVar(&askOpts.timeout, "timeout", 0, "整体超时秒数（覆盖 config）")
	askCmd.Flags().Int64Var(&askOpts.maxInputBytes, "max-input-bytes", 0, "输入文本上限字节数（覆盖 config）")
}

func runAsk(cmd *cobra.Command, args []string) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()

	cfg, warnings, err := config.Load()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "%v（brain config edit 可修复）", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "brain: 警告: %s\n", w)
	}

	// CLI flags 最后覆盖（优先级：flags > 环境变量 > config.yaml > 默认值）
	if askOpts.timeout > 0 {
		cfg.TimeoutSeconds = askOpts.timeout
	}
	if askOpts.maxInputBytes > 0 {
		cfg.MaxInputBytes = askOpts.maxInputBytes
	}

	if cfg.BaseURL == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "缺少 base_url，修复：brain config set base_url <https://your-gateway/v1>")
	}
	if cfg.APIKey == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "缺少 api_key，修复：brain config set api_key <sk-...>（或设 BRAIN_API_KEY 环境变量）")
	}

	name := askOpts.model
	if name == "" {
		name = cfg.DefaultModel
	}
	if name == "" {
		return exitcode.Fail(stderr, exitcode.ConfigMissing, "未指定模型，修复：-m <别名或模型ID> 或 brain config set default_model <别名>")
	}

	mf, err := config.LoadModels()
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}
	modelID, err := app.ResolveModel(name, mf)
	if err != nil {
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}

	text, err := app.Assemble(args, askOpts.files, stdin, stdinPiped, cfg.MaxInputBytes)
	if err != nil {
		var tl *app.TooLargeError
		if errors.As(err, &tl) {
			return exitcode.Fail(stderr, exitcode.InputTooLarge,
				"输入 %s 超过上限 %s（--max-input-bytes 可调）", humanBytes(tl.Size), humanBytes(tl.Limit))
		}
		return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
	}

	// 图片（-i）同样是合法输入：纯图咨询（如 vision 别名）不需要文本
	if text == "" && len(askOpts.images) == 0 {
		return exitcode.Fail(stderr, exitcode.Usage,
			"没有输入内容：需要位置参数、-f 文件、管道 stdin 或 -i 图片至少其一")
	}

	// 图片块排在文本块之后（TODO.md 3.1）；纯图时无文本块
	parts := make([]gateway.ContentPart, 0, 1+len(askOpts.images))
	if text != "" {
		parts = append(parts, gateway.ContentPart{Type: "text", Text: text})
	}
	for _, spec := range askOpts.images {
		u, err := app.PrepareImage(spec)
		if err != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
		}
		parts = append(parts, gateway.ContentPart{Type: "image_url", ImageURL: &gateway.ImageURL{URL: u}})
	}

	messages := make([]gateway.Message, 0, 2)
	if sys := strings.Join(askOpts.system, "\n\n"); sys != "" {
		messages = append(messages, gateway.Message{Role: "system", Content: sys})
	}
	messages = append(messages, gateway.Message{Role: "user", Content: parts})

	// SIGINT/SIGTERM → exit 130（区别于超时的 exit 4）
	ctx, cancel := context.WithCancel(cmd.Context())
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

	start := time.Now()
	g := gateway.New(cfg.BaseURL, cfg.APIKey, time.Duration(cfg.TimeoutSeconds)*time.Second)
	resp, err := g.ChatCompletion(ctx, gateway.ChatRequest{Model: modelID, Messages: messages, Stream: false})
	if err != nil {
		select {
		case <-interrupted:
			return exitcode.Fail(stderr, exitcode.Interrupt, "被中断")
		default:
		}
		// 模型不可用时给出相近建议（若有缓存可用）；别名与其解析出的 ID 都作种子
		var se *gateway.StatusError
		if errors.As(err, &se) && looksLikeModelError(se) {
			if cands := app.Suggest([]string{name, modelID}, mf, app.ReadCachedModels()); len(cands) > 0 {
				fmt.Fprintf(stderr, "brain: 相近的可用模型: %s\n", strings.Join(cands, ", "))
			}
		}
		return exitcode.Fail(stderr, exitCodeFor(err), "%v", err)
	}

	content := resp.Choices[0].Message.Content
	if content == "" {
		fmt.Fprintf(stderr, "brain: 警告: 模型返回空内容（finish_reason=%s）\n", resp.Choices[0].FinishReason)
	}

	// 全程序唯一的 stdout 正文写点（TODO.md 3.2「stdout 单一写点」），
	// 换行约定：正文末尾保证恰好一个 \n。
	if content != "" {
		fmt.Fprint(stdout, ensureSingleNewline(content))
	}
	fmt.Fprintf(stderr, "brain: %.1fs | 模型 %s\n", time.Since(start).Seconds(), modelID)
	return nil
}

// ensureSingleNewline 保证正文以恰好一个换行结尾（TODO.md 3.2 换行约定）。
func ensureSingleNewline(s string) string {
	return strings.TrimRight(s, "\n") + "\n"
}

// looksLikeModelError 粗判网关错误是否与模型名有关（404 或错误体提到 model）。
func looksLikeModelError(se *gateway.StatusError) bool {
	if se.Status == 404 {
		return true
	}
	return strings.Contains(strings.ToLower(se.Body), "model")
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
