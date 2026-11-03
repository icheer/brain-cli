// Package draw 实现图像生成（brain draw，M11）。
// 按模型名在两种协议间选择：Gemini 原生（:generateContent + responseModalities）
// 与 OpenAI Images（/v1/images/generations；带参考图时切换 /v1/images/edits multipart）。
// 请求形状与响应归一（b64_json / url / inlineData / imageUrl 四形态）对照实测页面
// prompt.keyi.ma/image.html 的前端实现逐项对齐（该页面的双协议路径经真实用户量验证）。
// 传输为普通 POST + 一次性 JSON（两协议均无流式必要）；错误归一为 gateway.StatusError /
// gateway.TimeoutError，与 chat 侧共用退出码映射。
package draw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

// 协议类型（draw.type 的取值；空值 = 按模型名推断）。
const (
	TypeGemini = "gemini"
	TypeOpenAI = "openai"
)

// 响应体上限：4K 图的 b64 可达数十 MB（chat 侧 1 MiB 上限对 draw 不适用）。
const (
	MaxResponseBytes  = 128 << 20
	MaxDownloadBytes  = 128 << 20
	maxImagePickCount = 8 // 同一响应里最多检视的图片数（防畸形响应把循环拉爆）
)

// InferType 按模型名推断协议（DESIGN.md 第 0 节）：名字含 gemini（不区分大小写）→
// gemini 原生，其余一律 openai images。config 的 draw.type 可覆盖推断——
// 中转站把 gemini 包成 images 端点、或 nano-banana 裸名直连原生端点两个反例都真实存在。
func InferType(model string) string {
	if strings.Contains(strings.ToLower(model), "gemini") {
		return TypeGemini
	}
	return TypeOpenAI
}

var (
	reVersionSuffix = regexp.MustCompile(`/v\d+$`)
	reV1BetaSuffix  = regexp.MustCompile(`/v1beta$`)
)

// OpenAIBase 把 base_url 归一到「已含版本段」形态（智能拼接）：末尾 /v<数字> 可写
// 可不写——写了不重复拼，没写补 /v1（与 chat 的 base_url 约定一致）。
func OpenAIBase(base string) string {
	base = strings.TrimRight(base, "/")
	if reVersionSuffix.MatchString(base) {
		return base
	}
	return base + "/v1"
}

// GeminiBase 同理归一 /v1beta：已写则原样；写了 /v1（顶层 base_url 兜底进来的形态）
// 则替换为 /v1beta；裸域名则补。这样 draw 不配 base_url 直接复用顶层地址也能走通。
func GeminiBase(base string) string {
	base = strings.TrimRight(base, "/")
	if reV1BetaSuffix.MatchString(base) {
		return base
	}
	if m := reVersionSuffix.FindString(base); m != "" {
		return strings.TrimSuffix(base, m) + "/v1beta"
	}
	return base + "/v1beta"
}

// AspectSize：统一语义 --aspect → openai size 桶（prompt.keyi.ma 实测映射表）。
var AspectSize = map[string]string{
	"1:1":  "1024x1024",
	"21:9": "1536x1024", "16:9": "1536x1024", "3:2": "1536x1024",
	"4:3": "1536x1024", "5:4": "1536x1024",
	"4:5": "1024x1536", "3:4": "1024x1536", "2:3": "1024x1536", "9:16": "1024x1536",
}

// ResQuality：统一语义 --res → openai quality（1K/2K/4K ↔ low/medium/high）。
var ResQuality = map[string]string{"1K": "low", "2K": "medium", "4K": "high"}

// ValidRes --res 的合法取值（auto 由调用方另行放行）。
func ValidRes(res string) bool { _, ok := ResQuality[res]; return ok }

// RefImage 参考图（-i，仅本地文件；cmd 层负责读入与 MIME sniff）。
type RefImage struct {
	MIME string // image/png 等
	Data []byte
	Name string // multipart 文件名（含扩展名）
}

// RefSummary 参考图摘要（draw --dry-run 展示用，不含图片本体）。
type RefSummary struct {
	Name  string `json:"name"`
	MIME  string `json:"mime"`
	Bytes int    `json:"bytes"`
}

// Request draw 的统一请求模型（协议方言由各 adapter 翻译）。
type Request struct {
	Model  string
	Prompt string
	Aspect string // auto | 1:1 | 16:9 | …
	Res    string // auto | 1K | 2K | 4K
	Refs   []RefImage
}

// Usage 归一后的用量（两协议字段名不同：openai input_tokens / gemini promptTokenCount）。
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// Result 归一后的生成结果。
type Result struct {
	Image         []byte
	MIME          string
	Text          string // gemini TEXT part（模型附带的说明/拒绝辞令），由调用方走 stderr
	RevisedPrompt string // openai revised_prompt
	ImagesTotal   int    // 响应中的图片总数（v1 固定单张，多余的在此计数并提示）
	Usage         *Usage
}

// NoImageError：网关 2xx 但响应里没有图片（常见形态——安全拒绝只回文字）。
type NoImageError struct {
	BlockReason string // gemini promptFeedback.blockReason（若有）
	Text        string // 模型回的文字（已截断）
}

func (e *NoImageError) Error() string {
	if e.BlockReason != "" {
		return i18n.Tf("响应被拦截（blockReason=%s）且无图片", "response blocked (blockReason=%s) with no image", e.BlockReason)
	}
	if e.Text != "" {
		return i18n.Tf("响应中没有图片，模型返回了文字: %s", "no image in response; the model replied with text: %s", e.Text)
	}
	return i18n.T("响应中没有图片数据", "no image data in response")
}

// Client 图像生成客户端。BaseURL 为用户原始填写值（版本段可写可不写），
// 端点拼接在 Endpoint/各 adapter 内完成。
type Client struct {
	HTTP    *http.Client
	BaseURL string
	APIKey  string
	Type    string // 已解析协议（gemini|openai）
}

// New 构造客户端；timeout 作为传输层兜底，总预算由调用方经 ctx 控制（同 gateway.New）。
func New(baseURL, apiKey, typ string, timeout time.Duration) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: timeout},
		BaseURL: baseURL,
		APIKey:  apiKey,
		Type:    typ,
	}
}

// Endpoint 返回实际请求端点（dry-run 展示与请求共用；不含鉴权参数）。
func (c *Client) Endpoint(req *Request) string {
	if c.Type == TypeGemini {
		return GeminiBase(c.BaseURL) + "/models/" + req.Model + ":generateContent"
	}
	if len(req.Refs) > 0 {
		return OpenAIBase(c.BaseURL) + "/images/edits"
	}
	return OpenAIBase(c.BaseURL) + "/images/generations"
}

// Generate 执行一次生图（v1 固定 n=1；不做自动重试——图像按张计费，
// 失败请凭退出码人工重发，见 DESIGN.md 第 0 节 draw 行）。
func (c *Client) Generate(ctx context.Context, req *Request) (*Result, error) {
	// 归一空值：直接以构造体调用（测试/库用法）时 Aspect/Res 可能是零值
	if req.Aspect == "" {
		req.Aspect = "auto"
	}
	if req.Res == "" {
		req.Res = "auto"
	}
	if c.Type == TypeGemini {
		return c.gemini(ctx, req)
	}
	return c.openai(ctx, req)
}

// ---- OpenAI Images adapter ----

type openAIImage struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type openAIUsage struct {
	InputTokens     int64 `json:"input_tokens"`
	PromptTokens    int64 `json:"prompt_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CompletionToken int64 `json:"completion_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

type openAIResponse struct {
	Data  []openAIImage `json:"data"`
	Usage *openAIUsage  `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *Client) openai(ctx context.Context, req *Request) (*Result, error) {
	size := "auto"
	if s, ok := AspectSize[req.Aspect]; ok {
		size = s
	}
	quality := "auto"
	if q, ok := ResQuality[req.Res]; ok {
		quality = q
	}

	endpoint := c.Endpoint(req)
	var contentType string
	var body io.Reader
	if len(req.Refs) > 0 {
		// 参考图 → /v1/images/edits multipart（gpt-image 方言，prompt.keyi.ma 同款）
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for k, v := range map[string]string{
			"model": req.Model, "prompt": req.Prompt, "n": "1", "size": size, "quality": quality,
		} {
			_ = w.WriteField(k, v)
		}
		for _, r := range req.Refs {
			fw, err := w.CreateFormFile("image[]", r.Name)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", i18n.T("构造 multipart 失败", "failed to build multipart"), err)
			}
			if _, err := fw.Write(r.Data); err != nil {
				return nil, fmt.Errorf("%s: %w", i18n.T("构造 multipart 失败", "failed to build multipart"), err)
			}
		}
		if err := w.Close(); err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("构造 multipart 失败", "failed to build multipart"), err)
		}
		contentType, body = w.FormDataContentType(), &buf
	} else {
		// n 必须是 JSON 数字而非字符串——OpenAI 规范如此，且 Go 实现的网关（new-api 系）
		// 把 n 解析成 uint，收到 "1" 直接 400（v0.11.0 实弹事故：cannot unmarshal string
		// into Go struct field Alias.n of type uint）
		b, err := json.Marshal(map[string]any{
			"model": req.Model, "prompt": req.Prompt, "n": 1, "size": size, "quality": quality,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("序列化请求失败", "failed to marshal request"), err)
		}
		contentType, body = "application/json", bytes.NewReader(b)
	}

	data, err := c.post(ctx, endpoint, contentType, body, map[string]string{"Authorization": "Bearer " + c.APIKey})
	if err != nil {
		return nil, err
	}
	var out openAIResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w（%s: %s）", i18n.T("解析网关响应失败", "failed to parse gateway response"), err,
			i18n.T("响应片段", "response snippet"), snippet(data, 200))
	}
	if out.Error != nil { // 个别网关 200 夹带 error 体（one-api 系老毛病，chat 侧流内错误同源）
		return nil, &gateway.StatusError{Status: http.StatusBadRequest, Body: out.Error.Message}
	}
	res := &Result{}
	for i, img := range out.Data {
		if i >= maxImagePickCount {
			break
		}
		if img.RevisedPrompt != "" && res.RevisedPrompt == "" {
			res.RevisedPrompt = img.RevisedPrompt
		}
		switch {
		case img.B64JSON != "":
			raw, err := decodeB64(img.B64JSON)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", i18n.T("b64_json 解码失败", "failed to decode b64_json"), err)
			}
			res.ImagesTotal++
			if res.Image == nil {
				res.Image, res.MIME = raw, sniffMIME(raw)
			}
		case img.URL != "":
			raw, mime, derr := c.download(ctx, img.URL)
			if derr != nil {
				return nil, derr
			}
			res.ImagesTotal++
			if res.Image == nil {
				res.Image, res.MIME = raw, mime
			}
		}
	}
	if out.Usage != nil {
		res.Usage = &Usage{
			InputTokens:  nonzero(out.Usage.InputTokens, out.Usage.PromptTokens),
			OutputTokens: nonzero(out.Usage.OutputTokens, out.Usage.CompletionToken),
			TotalTokens:  out.Usage.TotalTokens,
		}
	}
	if res.Image == nil {
		return nil, &NoImageError{}
	}
	return res, nil
}

// ---- Gemini 原生 adapter ----

type geminiInline struct {
	MIME string `json:"mimeType,omitempty"`
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"` // 部分网关以 URL 代替内联 b64
}

type geminiPart struct {
	Text     string        `json:"text,omitempty"`
	ImageURL string        `json:"imageUrl,omitempty"` // 部分网关的图片 URL 形态
	Inline   *geminiInline `json:"inlineData,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback,omitempty"`
	UsageMetadata *struct {
		PromptTokens     int64 `json:"promptTokenCount"`
		CandidatesTokens int64 `json:"candidatesTokenCount"`
		TotalTokens      int64 `json:"totalTokenCount"`
	} `json:"usageMetadata,omitempty"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *Client) gemini(ctx context.Context, req *Request) (*Result, error) {
	parts := []map[string]any{{"text": req.Prompt}}
	for _, r := range req.Refs {
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{
				"mimeType": r.MIME,
				"data":     base64.StdEncoding.EncodeToString(r.Data),
			},
		})
	}
	genCfg := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	imageCfg := map[string]any{}
	if req.Res != "auto" {
		imageCfg["imageSize"] = req.Res
	}
	if req.Aspect != "auto" {
		imageCfg["aspectRatio"] = req.Aspect
	}
	if len(imageCfg) > 0 {
		genCfg["imageConfig"] = imageCfg
	}
	body, err := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": parts}},
		"generationConfig": genCfg,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("序列化请求失败", "failed to marshal request"), err)
	}

	// 鉴权双通道：?key=（页面实测形态）+ x-goog-api-key 头（官方推荐，key 不进 URL 日志），
	// 同值并存两端皆收；query 里的 key 绝不出现在任何错误/展示输出中（Endpoint 不含它）。
	endpoint := c.Endpoint(req) + "?key=" + url.QueryEscape(c.APIKey)
	data, err := c.post(ctx, endpoint, "application/json", bytes.NewReader(body),
		map[string]string{"x-goog-api-key": c.APIKey})
	if err != nil {
		return nil, err
	}
	var out geminiResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w（%s: %s）", i18n.T("解析网关响应失败", "failed to parse gateway response"), err,
			i18n.T("响应片段", "response snippet"), snippet(data, 200))
	}
	if out.Error != nil { // 非 2xx 时 post 已归一；这里兜 200 夹带 error 的网关
		return nil, &gateway.StatusError{Status: out.Error.Code, Body: out.Error.Message}
	}

	res := &Result{}
	for _, cand := range out.Candidates {
		for i, p := range cand.Content.Parts {
			if i >= maxImagePickCount {
				break
			}
			switch {
			case p.Inline != nil && p.Inline.Data != "":
				raw, derr := decodeB64(p.Inline.Data)
				if derr != nil {
					return nil, fmt.Errorf("%s: %w", i18n.T("inlineData 解码失败", "failed to decode inlineData"), derr)
				}
				res.ImagesTotal++
				if res.Image == nil {
					res.Image, res.MIME = raw, p.Inline.MIME
				}
			case p.Inline != nil && p.Inline.URL != "", p.ImageURL != "":
				u := p.ImageURL
				if u == "" {
					u = p.Inline.URL
				}
				raw, mime, derr := c.download(ctx, u)
				if derr != nil {
					return nil, derr
				}
				res.ImagesTotal++
				if res.Image == nil {
					res.Image, res.MIME = raw, mime
				}
			case p.Text != "":
				if res.Text != "" {
					res.Text += "\n"
				}
				res.Text += p.Text
			}
		}
	}
	if res.Image == nil {
		br := ""
		if out.PromptFeedback != nil {
			br = out.PromptFeedback.BlockReason
		}
		return nil, &NoImageError{BlockReason: br, Text: snippet([]byte(res.Text), 300)}
	}
	if res.MIME == "" {
		res.MIME = sniffMIME(res.Image)
	}
	if out.UsageMetadata != nil {
		res.Usage = &Usage{
			InputTokens:  out.UsageMetadata.PromptTokens,
			OutputTokens: out.UsageMetadata.CandidatesTokens,
			TotalTokens:  out.UsageMetadata.TotalTokens,
		}
	}
	return res, nil
}

// ---- 共用传输底座 ----

// post 发送请求并归一错误（非 2xx → StatusError，携网关错误体片段；
// 传输超时 → TimeoutError；gemini 错误体优先取其自报 code）。
func (c *Client) post(ctx context.Context, endpoint, contentType string, body io.Reader, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("构造请求失败", "failed to build request"), err)
	}
	req.Header.Set("User-Agent", gateway.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, &gateway.TimeoutError{Err: err}
		}
		return nil, fmt.Errorf("%s: %w", i18n.T("网络错误", "network error"), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("读取响应失败", "failed to read response"), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// gemini 错误体自带 code/message，优先透传（比 HTTP 状态更贴真实原因）
		var ge struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &ge) == nil && ge.Error != nil && ge.Error.Code != 0 {
			return nil, &gateway.StatusError{Status: ge.Error.Code, Body: ge.Error.Message}
		}
		return nil, &gateway.StatusError{Status: resp.StatusCode, Body: snippet(data, 400), RetryAfter: retryAfter(resp)}
	}
	return data, nil
}

// download 拉取 URL 形态的回图（openai data[].url / gemini imageUrl 变体）。
func (c *Client) download(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", i18n.T("构造请求失败", "failed to build request"), err)
	}
	req.Header.Set("User-Agent", gateway.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, "", &gateway.TimeoutError{Err: err}
		}
		return nil, "", fmt.Errorf("%s: %w", i18n.T("网络错误", "network error"), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxDownloadBytes))
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", i18n.T("读取响应失败", "failed to read response"), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", &gateway.StatusError{Status: resp.StatusCode, Body: rawURL}
	}
	mime := strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	if !strings.HasPrefix(mime, "image/") {
		mime = sniffMIME(data)
	}
	return data, mime, nil
}

// retryAfter 解析 Retry-After 头（秒数；非法返回 0）。与 gateway 包同构（429 场景）。
func retryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err == nil && secs > 0 && secs < 3600 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// isTimeout 与 gateway 包同构：net.Error 超时或 ctx deadline。
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// decodeB64 宽容解码：标准表优先，缺 padding 的裸串回退 Raw 表（个别网关如此）。
func decodeB64(s string) ([]byte, error) {
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// sniffMIME 按字节头嗅探（响应未给 mime 时的兜底）。
func sniffMIME(data []byte) string {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	m := http.DetectContentType(head)
	if m == "application/octet-stream" {
		return ""
	}
	return m
}

// snippet 截断展示（错误体/模型附言）。
func snippet(data []byte, n int) string {
	s := strings.TrimSpace(string(data))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// nonzero 取第一个非零值（openai usage 新旧字段名兼容）。
func nonzero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}
