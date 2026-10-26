// Package app 组装一次 ask 的输入（文本/图片）与模型解析，不依赖具体 CLI 框架。
package app

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/icheer/brain-cli/internal/i18n"
)

// InputError 调用方输入问题（exit 1）。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// TooLargeError 装配后文本超过 max_input_bytes（exit 6）。
type TooLargeError struct {
	Size  int64
	Limit int64
}

func (e *TooLargeError) Error() string {
	return i18n.Tf("输入 %d 字节超过上限 %d 字节", "input of %d bytes exceeds limit of %d bytes", e.Size, e.Limit)
}

// Assemble 按「位置参数 → -f（按出现顺序）→ stdin（若有）」装配正文文本，
// 多文件以分隔头标记（DESIGN.md 3.1）。limit 为装配后文本的字节上限。
// 全部来源为空时返回空串且不报错——图片（-i）同样是合法输入，
// 「完全无输入」的判断由调用方结合图片来源进行。
func Assemble(args []string, files []string, stdin io.Reader, piped bool, limit int64) (string, error) {
	parts := make([]string, 0, len(args)+len(files)+1)

	if len(args) > 0 {
		parts = append(parts, strings.Join(args, "\n\n"))
	}

	for _, f := range files {
		content, err := readTextFile(f, limit)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("----- file: %s -----\n%s", f, content))
	}

	if piped && stdin != nil {
		// 读到 limit+1 即止，避免恶意管道把内存读爆；最终统一由大小检查裁决
		data, _ := io.ReadAll(io.LimitReader(stdin, limit+1))
		if len(data) > 0 {
			parts = append(parts, string(data))
		}
	}

	text := strings.Join(parts, "\n\n")
	if n := int64(len(text)); n > limit {
		return "", &TooLargeError{Size: n, Limit: limit}
	}
	return text, nil
}

// readTextFile 读取一个 -f 文本文件：存在性/权限/大小/UTF-8 校验。
// 大小防护必须流式：Open + LimitReader(limit+1)，
// 不能 os.ReadFile 先整读——/dev/zero 这类特殊文件会读到 OOM。
func readTextFile(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", &InputError{Msg: i18n.Tf("读取文件失败 %s: %v", "failed to read file %s: %v", path, err)}
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil && info.IsDir() {
		return "", &InputError{Msg: i18n.Tf("-f 指向的是目录: %s", "-f points to a directory: %s", path)}
	}

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", &InputError{Msg: i18n.Tf("读取文件失败 %s: %v", "failed to read file %s: %v", path, err)}
	}
	if int64(len(data)) > limit {
		return "", &TooLargeError{Size: int64(len(data)), Limit: limit}
	}
	if !utf8.Valid(data) {
		return "", &InputError{Msg: i18n.Tf("文件不是 UTF-8 文本（PDF/二进制请先转文本）: %s", "file is not UTF-8 text (convert PDF/binary first): %s", path)}
	}
	return string(data), nil
}
