package cmd

import (
	"errors"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
)

// exitCodeFor 把网关/传输层错误映射到退出码（DESIGN.md 3.3）。
func exitCodeFor(err error) int {
	var se *gateway.StatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == 401 || se.Status == 403:
			return exitcode.Auth
		case se.Status == 429:
			return exitcode.RateLimit
		default: // 5xx 及其余 4xx 统一归上游错误
			return exitcode.Upstream
		}
	}
	var te *gateway.TimeoutError
	if errors.As(err, &te) {
		return exitcode.Timeout
	}
	return exitcode.Upstream
}

// errorTypeInfo 由网关阶段的原始错误推断机器可读类型与可重试性（--json 的 error.type/retryable）。
// retryable 语义：重新发起同样调用是否可能成功（稍后重试 vs 修改输入/配置）。
func errorTypeInfo(err error) (typ string, retryable bool) {
	var se *gateway.StatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == 401 || se.Status == 403:
			return "auth", false
		case se.Status == 404, app.IsModelError(se):
			return "model_not_found", false
		case se.Status == 429:
			return "rate_limited", true
		case se.Status >= 500:
			return "upstream", true
		default: // 其余 4xx：请求本身非法（含模型不支持的 reasoning_effort 档位）
			return "invalid_request", false
		}
	}
	var te *gateway.TimeoutError
	if errors.As(err, &te) {
		return "timeout", true
	}
	return "network", true
}

// errorTypeForCode 按退出码粗分类（无原始错误可依时的兜底，如配置/输入/用法错误）。
func errorTypeForCode(code int) (typ string, retryable bool) {
	switch code {
	case exitcode.Auth:
		return "auth", false
	case exitcode.RateLimit:
		return "rate_limited", true
	case exitcode.Timeout:
		return "timeout", true
	case exitcode.InputTooLarge:
		return "input_too_large", false
	case exitcode.ConfigMissing:
		return "config_missing", false
	case exitcode.Interrupt:
		return "interrupted", false
	case exitcode.Usage:
		return "usage", false
	default:
		return "upstream", true
	}
}
