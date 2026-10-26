package cmd

import (
	"strings"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

// assembleMessages 组装最终将发送的 messages（DESIGN.md 3.1）：
// system（-s 可重复，空行连接）→ user 文本块（位置参数 → -f → 管道 stdin）→ 图片块。
// ask 与 compare 共用同一装配规则；返回 messages 与装配文本字节数。
// InputError / TooLargeError 原样抛出，由调用方映射 exit 1 / exit 6。
func assembleMessages(args, files, images, system []string, limit int64) ([]gateway.Message, int64, error) {
	text, err := app.Assemble(args, files, stdin, stdinPiped, limit)
	if err != nil {
		return nil, 0, err
	}

	// 图片（-i）同样是合法输入：纯图咨询（如 vision 别名）不需要文本
	if text == "" && len(images) == 0 {
		return nil, 0, &app.InputError{Msg: i18n.T("没有输入内容：需要位置参数、-f 文件、管道 stdin 或 -i 图片至少其一",
			"no input: provide at least one of positional args, -f files, piped stdin, or -i images")}
	}

	// 图片块排在文本块之后（DESIGN.md 3.1）；纯图时无文本块
	parts := make([]gateway.ContentPart, 0, 1+len(images))
	if text != "" {
		parts = append(parts, gateway.ContentPart{Type: "text", Text: text})
	}
	for _, spec := range images {
		u, err := app.PrepareImage(spec)
		if err != nil {
			return nil, 0, err
		}
		parts = append(parts, gateway.ContentPart{Type: "image_url", ImageURL: &gateway.ImageURL{URL: u}})
	}

	messages := make([]gateway.Message, 0, 2)
	if sys := strings.Join(system, "\n\n"); sys != "" {
		messages = append(messages, gateway.Message{Role: "system", Content: sys})
	}
	messages = append(messages, gateway.Message{Role: "user", Content: parts})
	return messages, int64(len(text)), nil
}
