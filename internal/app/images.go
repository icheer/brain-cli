package app

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// MaxImageBytes 单张图片上限（解码后 20 MiB）。
// 图片不计入 max_input_bytes，后者只对装配后的文本计量（TODO.md 3.1）。
const MaxImageBytes = 20 << 20

// PrepareImage 把 -i 参数转成 image_url 的 url：
// http(s) URL 直接透传；本地路径 sniff MIME 后转 data: URL。
// 本地图片只接受常规文件，流式读取限长——防止 /dev/zero 等特殊文件读到 OOM。
func PrepareImage(spec string) (string, error) {
	if u, err := url.Parse(spec); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return spec, nil
	}
	f, err := os.Open(spec)
	if err != nil {
		return "", &InputError{Msg: fmt.Sprintf("读取图片失败 %s: %v", spec, err)}
	}
	defer f.Close()

	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", &InputError{Msg: fmt.Sprintf("不是常规图片文件: %s", spec)}
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return "", &InputError{Msg: fmt.Sprintf("读取图片失败 %s: %v", spec, err)}
	}
	if int64(len(data)) > MaxImageBytes {
		return "", &InputError{Msg: fmt.Sprintf("图片超过单张上限 %d MiB: %s", MaxImageBytes>>20, spec)}
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	mime := http.DetectContentType(sniff)
	if !strings.HasPrefix(mime, "image/") {
		return "", &InputError{Msg: fmt.Sprintf("不是图片文件（检测到 %s）: %s", mime, spec)}
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
