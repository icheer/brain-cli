package app

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssembleOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("内容A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("内容B"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, err := Assemble([]string{"问题", "补充"}, []string{a, b}, strings.NewReader("管道内容"), true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want := "<user_message>\n问题\n\n补充\n</user_message>\n\n" +
		"<attachment path=\"" + a + "\">\n内容A\n</attachment>\n\n" +
		"<attachment path=\"" + b + "\">\n内容B\n</attachment>\n\n" +
		"<stdin>\n管道内容\n</stdin>"
	if text != want {
		t.Fatalf("装配结果不符:\n got: %q\nwant: %q", text, want)
	}
}

func TestAssembleNoInput(t *testing.T) {
	// 文本来源全空：返回空串不报错（图片是另一路输入，判断在 cmd 层）
	text, err := Assemble(nil, nil, nil, false, 1<<20)
	if err != nil || text != "" {
		t.Fatalf("应为空串无错: %q, %v", text, err)
	}
	// stdin 有 reader 但未标记 piped（交互式 TTY 场景）也不算输入
	if text, err := Assemble(nil, nil, strings.NewReader("x"), false, 1<<20); err != nil || text != "" {
		t.Fatalf("未 piped 的 stdin 不应计入: %q, %v", text, err)
	}
}

func TestAssembleTooLarge(t *testing.T) {
	_, err := Assemble([]string{strings.Repeat("长", 100)}, nil, nil, false, 10)
	tl, ok := err.(*TooLargeError)
	if !ok {
		t.Fatalf("应得 TooLargeError，得到 %v", err)
	}
	if tl.Limit != 10 {
		t.Fatalf("limit: %d", tl.Limit)
	}
}

func TestAssembleFileErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.txt")
	dirPath := dir
	nonUTF8 := filepath.Join(dir, "bin")
	if err := os.WriteFile(nonUTF8, []byte{0xff, 0xfe, 0x00, 'a'}, 0o644); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{"缺失": missing, "目录": dirPath, "非UTF-8": nonUTF8} {
		_, err := Assemble([]string{"q"}, []string{path}, nil, false, 1<<20)
		ie, ok := err.(*InputError)
		if !ok {
			t.Fatalf("%s: 应得 InputError，得到 %v", name, err)
		}
		if ie.Msg == "" {
			t.Fatalf("%s: 错误消息为空", name)
		}
	}
}

// 特殊文件（/dev/zero：Stat 大小为 0 但读不完）必须被 LimitReader 截停。
func TestAssembleSpecialFileBounded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅 linux 有 /dev/zero")
	}
	_, err := Assemble([]string{"q"}, []string{"/dev/zero"}, nil, false, 1024)
	if _, ok := err.(*TooLargeError); !ok {
		t.Fatalf("应得 TooLargeError（限长截停），得到 %v", err)
	}
}

// 超过上限的常规文件在读取阶段即截停，无需整读。
func TestAssembleOversizedFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(p, bytes.Repeat([]byte("a"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Assemble([]string{"q"}, []string{p}, nil, false, 10)
	if _, ok := err.(*TooLargeError); !ok {
		t.Fatalf("应得 TooLargeError，得到 %v", err)
	}
}

// 单一来源裸发（不加标签）：纯问答路径零变化；单文件仍带 <attachment>（路径元数据）。
func TestAssembleSingleSourceUntagged(t *testing.T) {
	text, err := Assemble([]string{"就一个问题"}, nil, nil, false, 1<<20)
	if err != nil || text != "就一个问题" {
		t.Fatalf("单问题应裸发: %q, %v", text, err)
	}
	text, err = Assemble(nil, nil, strings.NewReader("管道独占"), true, 1<<20)
	if err != nil || text != "管道独占" {
		t.Fatalf("单管道应裸发: %q, %v", text, err)
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "only.txt")
	if err := os.WriteFile(f, []byte("唯一附件"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err = Assemble(nil, []string{f}, nil, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<attachment path=\"" + f + "\">\n唯一附件\n</attachment>"; text != want {
		t.Fatalf("单附件应带标签保路径:\n got %q\nwant %q", text, want)
	}
}
