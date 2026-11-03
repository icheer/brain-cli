package app

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/icheer/brain-cli/internal/i18n"
)

// MaxImageBytes 单张图片上限（解码后 20 MiB）。
// 图片不计入 max_input_bytes，后者只对装配后的文本计量（DESIGN.md 3.1）。
const MaxImageBytes = 20 << 20

// PrepareImage 把 -i 参数转成 image_url 的 url：
// http(s) URL 直接透传；本地路径 sniff MIME 后转 data: URL。
// 本地图片只接受常规文件，流式读取限长——防止 /dev/zero 等特殊文件读到 OOM。
func PrepareImage(spec string) (string, error) {
	data, mime, isURL, err := LoadImage(spec)
	if err != nil {
		return "", err
	}
	if isURL {
		return spec, nil
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// LoadImage 读取一张 -i 图片：http(s) URL 返回 isURL=true（由调用方决定透传还是拒绝，
// draw 参考图走 multipart/inlineData 需要本地字节，URL 形态会明确报错）；本地路径则
// 返回原始字节与 sniff 出的 MIME（ask 转数据 URL、draw 拿原始字节，共用同一读取与防护）。
func LoadImage(spec string) (data []byte, mime string, isURL bool, err error) {
	if u, perr := url.Parse(spec); perr == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return nil, "", true, nil
	}
	f, oerr := os.Open(spec)
	if oerr != nil {
		return nil, "", false, &InputError{Msg: i18n.Tf("读取图片失败 %s: %v", "failed to read image %s: %v", spec, oerr)}
	}
	defer f.Close()

	if info, serr := f.Stat(); serr != nil || !info.Mode().IsRegular() {
		return nil, "", false, &InputError{Msg: i18n.Tf("不是常规图片文件: %s", "not a regular image file: %s", spec)}
	}
	data, rerr := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if rerr != nil {
		return nil, "", false, &InputError{Msg: i18n.Tf("读取图片失败 %s: %v", "failed to read image %s: %v", spec, rerr)}
	}
	if int64(len(data)) > MaxImageBytes {
		return nil, "", false, &InputError{Msg: i18n.Tf("图片超过单张上限 %d MiB: %s", "image exceeds per-image limit of %d MiB: %s", MaxImageBytes>>20, spec)}
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	mime = http.DetectContentType(sniff)
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", false, &InputError{Msg: i18n.Tf("不是图片文件（检测到 %s）: %s", "not an image file (detected %s): %s", mime, spec)}
	}
	return data, mime, false, nil
}
