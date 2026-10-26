package gateway

import (
	"os"
	"testing"
)

// TestMain 钉住界面语言为中文：本包断言基于中文错误文案。
func TestMain(m *testing.M) {
	os.Setenv("BRAIN_LANG", "zh")
	os.Exit(m.Run())
}
