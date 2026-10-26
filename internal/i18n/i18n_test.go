package i18n

import (
	"testing"
	"time"
)

func TestDetectEnvChain(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Lang
	}{
		{"BRAIN_LANG=zh 覆盖一切", map[string]string{"BRAIN_LANG": "zh", "LC_ALL": "C.UTF-8"}, Zh},
		{"BRAIN_LANG=en 覆盖 zh locale", map[string]string{"BRAIN_LANG": "en", "LC_ALL": "zh_CN.UTF-8"}, En},
		{"LC_ALL 优先于 LANG", map[string]string{"LC_ALL": "zh_CN.UTF-8", "LANG": "en_US.UTF-8"}, Zh},
		{"LC_MESSAGES 优先于 LANG", map[string]string{"LC_MESSAGES": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, En},
		{"LANG=zh_TW 繁体也算中文", map[string]string{"LANG": "zh_TW.UTF-8"}, Zh},
		{"LANG=C → 英文", map[string]string{"LANG": "C"}, En},
		{"LANG=en → 英文", map[string]string{"LANG": "en_US.UTF-8"}, En},
		{"LANG=de → 英文（只支持两种语言）", map[string]string{"LANG": "de_DE.UTF-8"}, En},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []string{"BRAIN_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
				t.Setenv(k, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := Detect(); got != c.want {
				t.Fatalf("Detect()=%d, want %d", got, c.want)
			}
		})
	}
}

// 无任何 locale 环境变量 → 按时区偏移兜底。
func TestDetectTimezoneFallback(t *testing.T) {
	for _, k := range []string{"BRAIN_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(k, "")
	}

	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("无 tzdata")
	}
	t.Setenv("TZ", "Asia/Shanghai")
	time.Local = shanghai
	if got := Detect(); got != Zh {
		t.Fatalf("UTC+8 应中文: %d", got)
	}

	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("无 tzdata")
	}
	t.Setenv("TZ", "Asia/Tokyo")
	time.Local = tokyo
	if got := Detect(); got != En {
		t.Fatalf("UTC+9 应英文: %d", got)
	}

	utc, _ := time.LoadLocation("UTC")
	t.Setenv("TZ", "UTC")
	time.Local = utc
	if got := Detect(); got != En {
		t.Fatalf("UTC 应英文: %d", got)
	}
}

func TestTF(t *testing.T) {
	t.Setenv("BRAIN_LANG", "zh")
	if got := Tf("配置项 %q 非法", "config key %q invalid", "k"); got != `配置项 "k" 非法` {
		t.Fatalf("zh: %q", got)
	}
	t.Setenv("BRAIN_LANG", "en")
	if got := Tf("配置项 %q 非法", "config key %q invalid", "k"); got != `config key "k" invalid` {
		t.Fatalf("en: %q", got)
	}
}
