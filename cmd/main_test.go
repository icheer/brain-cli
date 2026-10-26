package cmd

import (
	"os"
	"testing"
)

// TestMain 钉住界面语言为中文：既有断言全部基于中文文案。
// 需要英文断言的用例在自身内 t.Setenv("BRAIN_LANG", "en") 覆盖。
func TestMain(m *testing.M) {
	os.Setenv("BRAIN_LANG", "zh")
	os.Exit(m.Run())
}
