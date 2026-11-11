// Package app 组装一次 ask 的输入（文本/图片）与模型解析，不依赖具体 CLI 框架。
package app

import (
	"fmt"
	"io"
	"os"
	"regexp"
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

// AssembleInfo 输入装配的结构信息（2026-11-11）：数据声明的注入等下游决策依据
// 结构标志而非扫描装配正文——用户问题里讨论标签字面量（如咨询 brain 自身的
// 输出格式）不该误触发「标签内是数据」声明。
type AssembleInfo struct {
	HasAttachment bool
	HasStdin      bool
}

// structuralTagRe 匹配不可信内容中与结构标签撞车的尖括号形式：`<` 或 `</` 紧跟
// 保留标签名，且名字后是 >、空白、/ 或文本末尾（词边界——<attachmentx> 不误伤）。
// RE2 无 lookahead，边界字符捕获进组原样回填。
var structuralTagRe = regexp.MustCompile(`<(/?)(user_message|attachment|stdin|transcript|speech)([>\s/]|$)`)

// EscapeStructural 把不可信内容（附件正文/管道 stdin/council 发言）中与结构标签
// 撞车的 `<` 转义为 &lt;（2026-11-11）：内容里出现 </attachment> 等保留标签时
// 不再产生块边界歧义，附件也无法伪造 <transcript>/<speech> 假发言记录。其余尖括号
// （代码、泛型、HTML 片段）原样保留——本格式是 XML 风格分隔符，不承诺可被
// XML 解析器解析。问题正文与 -s 是指令通道（讨论标签本身合法），不经过本函数。
func EscapeStructural(s string) string {
	return structuralTagRe.ReplaceAllString(s, "&lt;$1$2$3")
}

// xmlAttrEscape 属性值转义：路径含 & < " 时属性仍无歧义（完整 XML 实体编码的最小子集）。
func xmlAttrEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// Assemble 按「位置参数 → -f（按出现顺序）→ stdin（若有）」装配正文文本（DESIGN.md 3.1）。
// 多来源 XML 区隔（2026-10-27）：附件恒以 <attachment id path> 包裹（路径元数据即使
// 单文件也有价值；id 序号 1 起按 -f 顺序，2026-11-11 增——模型援引免写长文件名、
// 重名 basename 消歧，宿主凭 -f 顺序即知映射）；位置参数与 stdin 仅在与其它文本块
// 共存时才包裹 <user_message>/<stdin>——单一来源裸发保持原样（高频纯问答路径零变化）。
// 标签是语言中立的恒定契约，不参与 i18n；不可信内容做保留字最小转义（EscapeStructural），
// 其余尖括号不动。
// limit 为装配后文本的字节上限。
func Assemble(args []string, files []string, stdin io.Reader, piped bool, limit int64) (string, AssembleInfo, error) {
	var info AssembleInfo
	var question, pipedText string
	type attachment struct{ path, content string }
	var attachments []attachment

	if len(args) > 0 {
		question = strings.Join(args, "\n\n")
	}
	for _, f := range files {
		content, err := readTextFile(f, limit)
		if err != nil {
			return "", info, err
		}
		attachments = append(attachments, attachment{f, content})
		info.HasAttachment = true
	}
	if piped && stdin != nil {
		// 读到 limit+1 即止，避免恶意管道把内存读爆；最终统一由大小检查裁决
		data, _ := io.ReadAll(io.LimitReader(stdin, limit+1))
		if len(data) > 0 {
			pipedText = string(data)
			info.HasStdin = true
		}
	}

	blocks := len(attachments)
	if question != "" {
		blocks++
	}
	if pipedText != "" {
		blocks++
	}

	var parts []string
	if question != "" {
		if blocks >= 2 {
			parts = append(parts, "<user_message>\n"+question+"\n</user_message>")
		} else {
			parts = append(parts, question)
		}
	}
	for i, a := range attachments {
		parts = append(parts, fmt.Sprintf("<attachment id=%d path=\"%s\">\n%s\n</attachment>",
			i+1, xmlAttrEscape(a.path), EscapeStructural(a.content)))
	}
	if pipedText != "" {
		if blocks >= 2 {
			parts = append(parts, "<stdin>\n"+EscapeStructural(pipedText)+"\n</stdin>")
		} else {
			parts = append(parts, pipedText) // 单来源裸发：无标签即无边界可保护，零变化
		}
	}

	text := strings.Join(parts, "\n\n")
	if n := int64(len(text)); n > limit {
		return "", info, &TooLargeError{Size: n, Limit: limit}
	}
	return text, info, nil
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
