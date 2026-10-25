// Package exitcode 集中定义退出码规范（DESIGN.md 3.3）。
// 退出码是宿主 Agent 消费的第一优先级信号，全程序只从这里取值。
package exitcode

import (
	"fmt"
	"io"
)

const (
	OK            = 0   // 成功
	Usage         = 1   // 用法错误（参数/标志非法、输入文件问题等调用方错误）
	Auth          = 2   // 认证失败（401/403，提示检查 api_key）
	RateLimit     = 3   // 限流且重试耗尽（429）
	Timeout       = 4   // 超时
	Upstream      = 5   // 上游/网络错误（含重试耗尽的 5xx）
	InputTooLarge = 6   // 输入超限
	ConfigMissing = 7   // 配置缺失（无 key / 无 base_url，stderr 给出修复命令）
	Interrupt     = 130 // 被中断（SIGINT），区别于超时
)

// Error 携带退出码的错误，由 cmd 层统一转成 os.Exit。
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Fail 向 w（通常 stderr）打印一行错误消息，并返回携带退出码的错误。
// 统一 "brain: " 前缀，便于 Agent 在输出中识别。
func Fail(w io.Writer, code int, format string, a ...any) *Error {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintf(w, "brain: %s\n", msg)
	return &Error{Code: code, Msg: msg}
}
