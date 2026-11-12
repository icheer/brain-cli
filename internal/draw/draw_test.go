package draw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/icheer/brain-cli/internal/gateway"
)

// tinyPNG 1x1 标准 PNG（全链路用：magic sniff、multipart、下载体）。
var tinyPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

// reqCapture 记录假网关收到的最近一次请求（body 原样字节）。
type reqCapture struct {
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// recordingServer 起一个固定响应假网关，记录最近一次请求。
func recordingServer(t *testing.T, status int, resp string) (*httptest.Server, *reqCapture) {
	t.Helper()
	cap := &reqCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.Path, cap.Query, cap.Header = r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		cap.Body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if status != 200 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func TestInferType(t *testing.T) {
	cases := []struct{ model, want string }{
		{"gemini-3.1-flash-image", TypeGemini},
		{"Gemini-3-Pro-Image", TypeGemini},    // 大小写不敏感
		{"anti/gemini-3.8-flash", TypeGemini}, // 网关前缀带斜杠
		{"nano-banana-pro", TypeOpenAI},       // 不含 gemini 字样 → openai（draw.type 覆盖兜此类）
		{"gpt-image-2.5-sunburst", TypeOpenAI},
		{"doubao-seedream-4-0", TypeOpenAI},
		{"qwen-image", TypeOpenAI},
	}
	for _, c := range cases {
		if got := InferType(c.model); got != c.want {
			t.Errorf("InferType(%q)=%s want %s", c.model, got, c.want)
		}
	}
}

func TestBaseJoin(t *testing.T) {
	// OpenAI：末尾 /v<数字> 可写可不写，写了不重复拼
	for in, want := range map[string]string{
		"https://gw.example.com":        "https://gw.example.com/v1",
		"https://gw.example.com/":       "https://gw.example.com/v1",
		"https://gw.example.com/v1":     "https://gw.example.com/v1",
		"https://gw.example.com/v1/":    "https://gw.example.com/v1",
		"https://gw.example.com/v2":     "https://gw.example.com/v2",
		"https://gw.example.com/api":    "https://gw.example.com/api/v1",
		"https://gw.example.com/api/v1": "https://gw.example.com/api/v1",
	} {
		if got := OpenAIBase(in); got != want {
			t.Errorf("OpenAIBase(%q)=%q want %q", in, got, want)
		}
	}
	// Gemini：/v1beta 可写可不写；顶层 /v1 兜底进来时换成 /v1beta
	for in, want := range map[string]string{
		"https://generativelanguage.googleapis.com":        "https://generativelanguage.googleapis.com/v1beta",
		"https://generativelanguage.googleapis.com/":       "https://generativelanguage.googleapis.com/v1beta",
		"https://generativelanguage.googleapis.com/v1beta": "https://generativelanguage.googleapis.com/v1beta",
		"https://gw.example.com/v1":                        "https://gw.example.com/v1beta",
		"https://gw.example.com/v1/":                       "https://gw.example.com/v1beta",
		"https://gw.example.com":                           "https://gw.example.com/v1beta",
		"https://gw.example.com/api":                       "https://gw.example.com/api/v1beta",
	} {
		if got := GeminiBase(in); got != want {
			t.Errorf("GeminiBase(%q)=%q want %q", in, got, want)
		}
	}
}

func TestEndpoint(t *testing.T) {
	req := &Request{Model: "m/x"}
	c := New("https://gw.example.com", "k", TypeGemini, time.Second)
	if got := c.Endpoint(req); got != "https://gw.example.com/v1beta/models/m/x:generateContent" {
		t.Errorf("gemini endpoint=%q", got)
	}
	c = New("https://gw.example.com/v1", "k", TypeOpenAI, time.Second)
	if got := c.Endpoint(req); got != "https://gw.example.com/v1/images/generations" {
		t.Errorf("openai endpoint=%q", got)
	}
	req.Refs = []RefImage{{MIME: "image/png", Data: tinyPNG, Name: "r.png"}}
	if got := c.Endpoint(req); got != "https://gw.example.com/v1/images/edits" {
		t.Errorf("openai edits endpoint=%q", got)
	}
}

// ---- OpenAI Images adapter ----

func TestOpenAIGenerationsGolden(t *testing.T) {
	resp := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `","revised_prompt":"a fluffier cat"}],` +
		`"usage":{"input_tokens":12,"output_tokens":4160,"total_tokens":4172}}`
	srv, cap := recordingServer(t, 200, resp)

	c := New(srv.URL, "k", TypeOpenAI, 5*time.Second)
	res, err := c.Generate(context.Background(), &Request{
		Model: "gpt-image-x", Prompt: "a cat", Aspect: "16:9", Res: "2K",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.Path != "/v1/images/generations" {
		t.Errorf("path=%q", cap.Path)
	}
	if cap.Header.Get("Authorization") != "Bearer k" {
		t.Errorf("Authorization=%q", cap.Header.Get("Authorization"))
	}
	// 请求体 golden：统一语义翻译成 size 桶 + quality；n 必须是 JSON 数字
	// （字符串 "1" 会让 Go 网关的 uint 字段 400——v0.11.0 真实事故，勿回退）
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"model": "gpt-image-x", "prompt": "a cat", "n": float64(1), "size": "1536x1024", "quality": "medium"} {
		if body[k] != want {
			t.Errorf("body[%q]=%#v want %#v", k, body[k], want)
		}
	}
	// 响应归一：b64 解码 + mime sniff + revised_prompt + usage
	if string(res.Image) != string(tinyPNG) || res.MIME != "image/png" {
		t.Errorf("image=%dB mime=%q", len(res.Image), res.MIME)
	}
	if res.RevisedPrompt != "a fluffier cat" {
		t.Errorf("revised_prompt=%q", res.RevisedPrompt)
	}
	if res.Usage == nil || res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 4160 {
		t.Errorf("usage=%+v", res.Usage)
	}
}

func TestOpenAIAutoParams(t *testing.T) {
	resp := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `"}]}`
	srv, cap := recordingServer(t, 200, resp)

	c := New(srv.URL, "k", TypeOpenAI, 5*time.Second)
	if _, err := c.Generate(context.Background(), &Request{Model: "m", Prompt: "p", Aspect: "auto", Res: "auto"}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["size"] != "auto" || body["quality"] != "auto" {
		t.Errorf("auto params: size=%v quality=%v", body["size"], body["quality"])
	}
}

func TestOpenAIURLResponse(t *testing.T) {
	// data[0].url 形态：CLI 自行下载并按 Content-Type 定 mime
	var genHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/images/generations":
			genHit = true
			_, _ = w.Write([]byte(`{"data":[{"url":"http://` + r.Host + `/img.png"}]}`))
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "k", TypeOpenAI, 5*time.Second)
	res, err := c.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if !genHit {
		t.Error("generations 端点未被请求")
	}
	if string(res.Image) != string(tinyPNG) || res.MIME != "image/png" {
		t.Errorf("download image=%dB mime=%q", len(res.Image), res.MIME)
	}
}

func TestOpenAIEditsMultipart(t *testing.T) {
	resp := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `"}]}`
	srv, cap := recordingServer(t, 200, resp)

	c := New(srv.URL, "k", TypeOpenAI, 5*time.Second)
	res, err := c.Generate(context.Background(), &Request{
		Model: "gpt-image-x", Prompt: "make it snowy", Aspect: "1:1", Res: "1K",
		Refs: []RefImage{
			{MIME: "image/png", Data: tinyPNG, Name: "ref.png"},
			{MIME: "image/png", Data: tinyPNG, Name: "ref2.png"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Image) != string(tinyPNG) {
		t.Fatalf("edits image=%dB", len(res.Image))
	}
	if cap.Path != "/v1/images/edits" {
		t.Errorf("edits path=%q", cap.Path)
	}
	// multipart 解析：字段 + image[] 文件（gpt-image 方言）
	mediatype, params, _ := mime.ParseMediaType(cap.Header.Get("Content-Type"))
	if mediatype != "multipart/form-data" {
		t.Fatalf("content-type=%q", cap.Header.Get("Content-Type"))
	}
	mr := multipart.NewReader(bytes.NewReader(cap.Body), params["boundary"])
	form, err := mr.ReadForm(32 << 20)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"model": "gpt-image-x", "prompt": "make it snowy", "n": "1", "size": "1024x1024", "quality": "low"} {
		if form.Value[k][0] != want {
			t.Errorf("field %q=%q want %q", k, form.Value[k][0], want)
		}
	}
	files := form.File["image[]"]
	if len(files) != 2 {
		t.Fatalf("image[] files=%d want 2", len(files))
	}
	f0, _ := files[0].Open()
	got, _ := io.ReadAll(f0)
	f0.Close()
	if string(got) != string(tinyPNG) {
		t.Error("第一个 image[] 内容不符")
	}
}

func TestOpenAIErrorAndEmpty(t *testing.T) {
	// 非 2xx → StatusError
	srv, _ := recordingServer(t, 500, `{"error":{"message":"boom"}}`)
	c := New(srv.URL, "k", TypeOpenAI, 5*time.Second)
	_, err := c.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	var se *gateway.StatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Errorf("500 → %v", err)
	}
	// 200 夹带 error 体（one-api 系老毛病）→ StatusError
	srv2, _ := recordingServer(t, 200, `{"error":{"message":"quota"}}`)
	c2 := New(srv2.URL, "k", TypeOpenAI, 5*time.Second)
	_, err = c2.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	if !errors.As(err, &se) || se.Status != 400 || !strings.Contains(se.Body, "quota") {
		t.Errorf("200-with-error → %v", err)
	}
	// 空数据 → NoImageError
	srv3, _ := recordingServer(t, 200, `{"data":[]}`)
	c3 := New(srv3.URL, "k", TypeOpenAI, 5*time.Second)
	_, err = c3.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	var ni *NoImageError
	if !errors.As(err, &ni) {
		t.Errorf("empty data → %v", err)
	}
}

// ---- Gemini 原生 adapter ----

func TestGeminiGolden(t *testing.T) {
	resp := `{"candidates":[{"content":{"parts":[` +
		`{"text":"已按 16:9 重绘"},` +
		`{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `"}}]}}],` +
		`"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":1290,"totalTokenCount":1299}}`
	srv, cap := recordingServer(t, 200, resp)

	c := New(srv.URL, "k", TypeGemini, 5*time.Second)
	res, err := c.Generate(context.Background(), &Request{
		Model: "gemini-3.1-flash-image", Prompt: "画一只猫", Aspect: "16:9", Res: "2K",
		Refs: []RefImage{{MIME: "image/png", Data: tinyPNG, Name: "r.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 端点 + 双通道鉴权（?key= 页面实测形态；x-goog-api-key 头 key 不进 URL 日志）
	if cap.Path != "/v1beta/models/gemini-3.1-flash-image:generateContent" {
		t.Errorf("path=%q", cap.Path)
	}
	if !strings.Contains(cap.Query, "key=k") {
		t.Errorf("query 缺 key: %q", cap.Query)
	}
	if cap.Header.Get("x-goog-api-key") != "k" {
		t.Errorf("x-goog-api-key=%q", cap.Header.Get("x-goog-api-key"))
	}
	// 请求体 golden：text part + inlineData 参考图 + generationConfig
	parts := cap.bodyParts(t)
	if parts[0].(map[string]any)["text"] != "画一只猫" {
		t.Errorf("text part=%v", parts[0])
	}
	ref := parts[1].(map[string]any)["inlineData"].(map[string]any)
	if ref["mimeType"] != "image/png" {
		t.Errorf("ref mimeType=%v", ref["mimeType"])
	}
	if raw, _ := base64.StdEncoding.DecodeString(ref["data"].(string)); string(raw) != string(tinyPNG) {
		t.Error("ref 图字节不符")
	}
	gcfg := cap.bodyJSON(t)["generationConfig"].(map[string]any)
	if mods := gcfg["responseModalities"].([]any); len(mods) != 2 || mods[0] != "TEXT" || mods[1] != "IMAGE" {
		t.Errorf("responseModalities=%v", mods)
	}
	ic := gcfg["imageConfig"].(map[string]any)
	if ic["aspectRatio"] != "16:9" || ic["imageSize"] != "2K" {
		t.Errorf("imageConfig=%v", ic)
	}
	// 响应归一：text part → Text，inlineData → 图，usageMetadata → usage
	if res.Text != "已按 16:9 重绘" {
		t.Errorf("text=%q", res.Text)
	}
	if string(res.Image) != string(tinyPNG) || res.MIME != "image/png" {
		t.Errorf("image=%dB mime=%q", len(res.Image), res.MIME)
	}
	if res.Usage == nil || res.Usage.InputTokens != 9 || res.Usage.OutputTokens != 1290 {
		t.Errorf("usage=%+v", res.Usage)
	}
}

func (c *reqCapture) bodyJSON(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("请求体非法 JSON: %v", err)
	}
	return m
}

func (c *reqCapture) bodyParts(t *testing.T) []any {
	t.Helper()
	return c.bodyJSON(t)["contents"].([]any)[0].(map[string]any)["parts"].([]any)
}

func TestGeminiAutoParamsOmitImageConfig(t *testing.T) {
	srv, cap := recordingServer(t, 200,
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"`+
			base64.StdEncoding.EncodeToString(tinyPNG)+`"}}]}}]}`)
	c := New(srv.URL, "k", TypeGemini, 5*time.Second)
	if _, err := c.Generate(context.Background(), &Request{Model: "m", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	gcfg := cap.bodyJSON(t)["generationConfig"].(map[string]any)
	if _, has := gcfg["imageConfig"]; has {
		t.Errorf("auto/auto 时不应携带 imageConfig: %v", gcfg)
	}
}

func TestGeminiImageURLVariant(t *testing.T) {
	// 部分网关以 imageUrl / inlineData.url 代替内联 b64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/img") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[` +
			`{"imageUrl":"http://` + r.Host + `/img"},{"text":"见链接"}]}}]}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "k", TypeGemini, 5*time.Second)
	res, err := c.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Image) != string(tinyPNG) || res.Text != "见链接" {
		t.Errorf("url variant image=%dB text=%q", len(res.Image), res.Text)
	}
}

func TestGeminiErrorAndBlocked(t *testing.T) {
	// gemini 错误体自带 code：比 HTTP 状态更贴真实原因
	srv, _ := recordingServer(t, 400, `{"error":{"code":400,"message":"invalid aspect"}}`)
	c := New(srv.URL, "k", TypeGemini, 5*time.Second)
	_, err := c.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	var se *gateway.StatusError
	if !errors.As(err, &se) || se.Status != 400 || !strings.Contains(se.Body, "aspect") {
		t.Errorf("error body → %v", err)
	}
	// 200 无图 + promptFeedback.blockReason（安全拒绝只回拦截标记）
	srv2, _ := recordingServer(t, 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	c2 := New(srv2.URL, "k", TypeGemini, 5*time.Second)
	_, err = c2.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	var ni *NoImageError
	if !errors.As(err, &ni) || ni.BlockReason != "SAFETY" {
		t.Errorf("blocked → %v", err)
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "k", TypeOpenAI, 80*time.Millisecond)
	_, err := c.Generate(context.Background(), &Request{Model: "m", Prompt: "p"})
	var te *gateway.TimeoutError
	if !errors.As(err, &te) {
		t.Errorf("timeout → %v", err)
	}
}

// ---- extras（--extra-body/--extra-headers，2026-11-12）----

// openai generations：extras 顶层 assign（grok 的 aspect_ratio 走这里），
// n 保持 JSON 数字；extra headers 在默认头之后应用。
func TestOpenAIExtraBodyTopLevel(t *testing.T) {
	resp := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `"}]}`
	srv, cap := recordingServer(t, 200, resp)

	c := New(srv.URL, "k", TypeOpenAI, 5*time.Second)
	_, err := c.Generate(context.Background(), &Request{
		Model: "grok-imagine-image-2.0", Prompt: "garden",
		ExtraBody:    map[string]any{"aspect_ratio": "16:9", "response_format": "url"},
		ExtraHeaders: map[string]string{"X-Tenant": "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["aspect_ratio"] != "16:9" || body["response_format"] != "url" {
		t.Errorf("extras 未顶层并入: %v", body)
	}
	if body["n"] != float64(1) { // 勿回退：n 必须保持 JSON 数字（v0.11.0 事故）
		t.Errorf("n=%#v", body["n"])
	}
	if body["size"] != "auto" || body["quality"] != "auto" {
		t.Errorf("默认参数应保留: %v", body)
	}
	if cap.Header.Get("X-Tenant") != "a" {
		t.Errorf("extra header 未生效: %v", cap.Header)
	}
}

// openai edits（multipart）：extras 以表单字段并入，字符串原样、对象 JSON 编码，键序确定。
func TestOpenAIEditsExtraBodyFormFields(t *testing.T) {
	resp := `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `"}]}`
	srv, cap := recordingServer(t, 200, resp)

	c := New(srv.URL, "k", TypeOpenAI, 5*time.Second)
	_, err := c.Generate(context.Background(), &Request{
		Model: "gpt-image-x", Prompt: "edit", Refs: []RefImage{{MIME: "image/png", Data: tinyPNG, Name: "r.png"}},
		ExtraBody: map[string]any{"aspect_ratio": "16:9", "watermark": map[string]any{"enabled": false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(cap.Body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", cap.Header.Get("Content-Type"))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("multipart 解析失败: %v", err)
	}
	if got := r.FormValue("aspect_ratio"); got != "16:9" {
		t.Errorf("aspect_ratio=%q", got)
	}
	if got := r.FormValue("watermark"); got != `{"enabled":false}` {
		t.Errorf("对象应 JSON 编码: %q", got)
	}
	if got := r.FormValue("model"); got != "gpt-image-x" {
		t.Errorf("model=%q", got)
	}
}

// gemini：extras 合并进 generationConfig.imageConfig（不是顶层）。
func TestGeminiExtraBodyIntoImageConfig(t *testing.T) {
	resp := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(tinyPNG) + `"}}]}}]}`
	srv, cap := recordingServer(t, 200, resp)

	c := New(srv.URL, "k", TypeGemini, 5*time.Second)
	_, err := c.Generate(context.Background(), &Request{
		Model: "gemini-3.1-flash-image", Prompt: "cat", Aspect: "16:9",
		ExtraBody: map[string]any{"foo": "bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		GenerationConfig struct {
			ImageConfig map[string]any `json:"imageConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.GenerationConfig.ImageConfig["aspectRatio"] != "16:9" || body.GenerationConfig.ImageConfig["foo"] != "bar" {
		t.Errorf("extras 应并入 imageConfig: %v", body.GenerationConfig.ImageConfig)
	}
}
