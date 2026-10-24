package app

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 1x1 透明 PNG，用于本地图片转 data URL 的测试。
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func TestPrepareImageLocalPNG(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "dot.png")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	u, err := PrepareImage(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Fatalf("应为 png data URL: %q", u[:40])
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u, "data:image/png;base64,"))
	if err != nil || string(decoded) != string(data) {
		t.Fatalf("base64 往返不一致: %v", err)
	}
}

func TestPrepareImageURLPassthrough(t *testing.T) {
	for _, spec := range []string{"https://example.com/a.png", "http://example.com/b.jpg"} {
		u, err := PrepareImage(spec)
		if err != nil {
			t.Fatal(err)
		}
		if u != spec {
			t.Fatalf("URL 应透传: %q", u)
		}
	}
}

func TestPrepareImageNotAnImage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(p, []byte("只是文本，不是图片"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareImage(p)
	ie, ok := err.(*InputError)
	if !ok {
		t.Fatalf("应得 InputError，得到 %v", err)
	}
	if !strings.Contains(ie.Msg, "不是图片") {
		t.Fatalf("消息不符: %q", ie.Msg)
	}
}

func TestPrepareImageMissing(t *testing.T) {
	_, err := PrepareImage(filepath.Join(t.TempDir(), "absent.png"))
	if _, ok := err.(*InputError); !ok {
		t.Fatalf("应得 InputError，得到 %v", err)
	}
}

// 特殊文件（如 /dev/zero：Stat 大小为 0 但读不完）不得整读进内存。
func TestPrepareImageSpecialFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅 linux 有 /dev/zero")
	}
	_, err := PrepareImage("/dev/zero")
	ie, ok := err.(*InputError)
	if !ok {
		t.Fatalf("应得 InputError，得到 %v", err)
	}
	if !strings.Contains(ie.Msg, "常规") {
		t.Fatalf("消息不符: %q", ie.Msg)
	}
}
