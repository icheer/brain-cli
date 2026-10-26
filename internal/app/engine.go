package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

// errAction 单类错误的处置策略。
type errAction int

const (
	actionFatal     errAction = iota // 401/403、总预算耗尽：立即终止
	actionNextModel                  // 404/模型相关 4xx/超时：换下一个模型
	actionRetrySame                  // 429/5xx/网络错误：退避后重试同一模型
)

// AskEngine 在别名 fallback 链上执行「重试 + 故障转移」（DESIGN.md 3.4 与第 0 节已定范围）。
// 已定范围：连接失败/超时/429/5xx/404 可切换；401/403 不切；
// stdout 等完成才输出，任何时点失败都不会污染已输出内容——但 --stream 模式例外，
// 通过 HasOutput 感知「正文已写出到 stdout」，此后禁止重试与切换（已输出正文永不切）。
type AskEngine struct {
	Client    *gateway.Client
	Stderr    io.Writer
	RetryMax  int         // 同一模型最大重试次数（不含首次）
	HasOutput func() bool // --stream 场景：返回 true 表示已有正文写到 stdout
	Attempted []string    // 已尝试的模型（按顺序），供 --json 的 attempted_models
}

// Ask 依次尝试链上模型，返回结果与实际使用的模型。
// 总超时由调用方通过 ctx deadline 控制，退避等待同样计入总预算。
func (e *AskEngine) Ask(ctx context.Context, chain []string,
	build func(model string) gateway.ChatRequest, onDelta gateway.DeltaHandler,
) (*gateway.ChatResult, string, error) {
	var lastErr error
	for i, model := range chain {
		e.Attempted = append(e.Attempted, model)
		if i > 0 {
			fmt.Fprintf(e.Stderr, "brain: %s %s → %s（%s）\n",
				i18n.T("故障转移", "failover"), chain[i-1], model, shortErr(lastErr))
		}
	nextAttempt:
		for attempt := 0; ; attempt++ {
			res, err := e.Client.ChatCompletionStream(ctx, build(model), onDelta)
			if err == nil {
				return res, model, nil
			}
			lastErr = err

			// 正文已流出到 stdout：接受失败，禁止重试与切换（第 0 节铁律）
			if e.HasOutput != nil && e.HasOutput() {
				return nil, model, err
			}

			switch actionFor(ctx, err) {
			case actionFatal:
				return nil, model, err
			case actionNextModel:
				break nextAttempt
			}

			if attempt >= e.RetryMax {
				break nextAttempt
			}
			backoff, ok := e.backoff(err, attempt, ctx)
			if !ok {
				// 剩余预算不够再等一次退避：按总超时收场
				return nil, model, &gateway.TimeoutError{Err: ctx.Err()}
			}
			fmt.Fprintf(e.Stderr, "brain: %s %s（%s），%.1fs %s\n", model, i18n.T("失败", "failed"),
				shortErr(err), backoff.Seconds(), i18n.T("后重试", "until retry"))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, model, &gateway.TimeoutError{Err: ctx.Err()}
			}
		}
	}
	return nil, "", lastErr
}

// actionFor 把单次错误映射为处置策略。
func actionFor(ctx context.Context, err error) errAction {
	if ctx.Err() != nil {
		return actionFatal // 总预算耗尽，再试无意义
	}
	var se *gateway.StatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == 401 || se.Status == 403:
			return actionFatal // 第 0 节：401/403 不切模型
		case se.Status == 404, IsModelError(se):
			// 真实网关实测：模型不存在也会以 503 + code=model_not_found 出现，勿按 5xx 重试
			return actionNextModel
		case se.Status == 429, se.Status >= 500:
			return actionRetrySame
		default: // 其余 4xx 视为模型/请求相关，换模型尝试
			return actionNextModel
		}
	}
	var te *gateway.TimeoutError
	if errors.As(err, &te) {
		return actionNextModel // 超时换模型：同模型大概率还是慢
	}
	return actionRetrySame // 连接失败等传输层错误
}

// backoff 计算重试等待：指数退避（1s/2s/4s，封顶 8s），429 时尊重 Retry-After；
// 返回 ok=false 表示剩余预算已不够等待。
func (e *AskEngine) backoff(err error, attempt int, ctx context.Context) (time.Duration, bool) {
	d := time.Second
	for i := 0; i < attempt && i < 3; i++ {
		d *= 2
	}
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	var se *gateway.StatusError
	if errors.As(err, &se) && se.RetryAfter > d {
		d = se.RetryAfter
	}
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem <= d+100*time.Millisecond {
			return 0, false
		}
	}
	return d, true
}

// IsModelError 粗判错误是否与模型选择有关（404 或错误体提到 model），
// 供 cmd 层在最终失败时给出「相近可用模型」建议。
func IsModelError(se *gateway.StatusError) bool {
	if se.Status == 404 {
		return true
	}
	return strings.Contains(strings.ToLower(se.Body), "model")
}

// shortErr stderr 提示用的单行短错误。
func shortErr(err error) string {
	s := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
