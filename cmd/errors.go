package cmd

import (
	"errors"

	"github.com/icheer/brain-cli/internal/exitcode"
	"github.com/icheer/brain-cli/internal/gateway"
)

// exitCodeFor 把网关/传输层错误映射到退出码（TODO.md 3.3）。
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
