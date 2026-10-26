// Package i18n 提供 brain 的双语界面（中文/英文）。
// 检测顺序（DESIGN.md 第 0 节）：BRAIN_LANG > LC_ALL > LC_MESSAGES > LANG
// （zh* → 中文，任何其他已设值 → 英文）；均未设时按时区偏移兜底——
// UTC+8 → 中文（海外服务器上把系统时区设为中国的典型部署），否则英文。
// UTC+8 内无实行夏令时的时区，按偏移判断稳定可靠，也免去维护时区名清单。
//
// 语言每次动态读取环境（进程内环境不变，等价于缓存但无初始化时序问题）；
// cobra 的 help 文案在包 init 期取值，遵循的就是进程启动时的环境。
package i18n

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Lang int

const (
	Zh Lang = iota
	En
)

// Detect 按环境变量与时区推断界面语言（见包注释的检测链）。
func Detect() Lang {
	if v := os.Getenv("BRAIN_LANG"); v != "" {
		return fromLocale(v)
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return fromLocale(v)
		}
	}
	if _, off := time.Now().In(time.Local).Zone(); off == 8*3600 {
		return Zh
	}
	return En
}

// fromLocale：zh 开头（zh_CN / zh_TW / zh-HK）→ 中文；其他任何值 → 英文。
func fromLocale(v string) Lang {
	if strings.HasPrefix(strings.ToLower(v), "zh") {
		return Zh
	}
	return En
}

// T 按当前语言取文案。双字符串直书调用点，不需要集中注册表。
func T(zh, en string) string {
	if Detect() == Zh {
		return zh
	}
	return en
}

// Tf 带格式化参数的 T。
func Tf(zh, en string, a ...any) string {
	if Detect() == Zh {
		return fmt.Sprintf(zh, a...)
	}
	return fmt.Sprintf(en, a...)
}
