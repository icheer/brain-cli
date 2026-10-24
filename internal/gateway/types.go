// Package gateway 实现 OpenAI Chat Completions 兼容协议的客户端
// （/v1/chat/completions + /v1/models）。M0 为非流式请求，M1 换 SSE 传输。
package gateway

// ContentPart 是 user 消息 content 数组中的块：文本块或图片块。
type ContentPart struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL 的 URL 取值：本地图片为 data:image/...;base64,...，远程图片直接透传原 URL。
type ImageURL struct {
	URL string `json:"url"`
}

// Message 的 Content 为 string（system）或 []ContentPart（user 多模态）。
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ChatRequest 只声明 brain 用到的字段；采样参数刻意不传（TODO.md 第 0 节已定）。
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// ChatResponse 同样只保留用到的字段；M1 扩展 SSE 增量结构。
type ChatResponse struct {
	Choices []Choice `json:"choices"`
}

// Choice 是一次生成的结果；brain 固定取 choices[0]。
type Choice struct {
	Index   int    `json:"index"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// ModelsResponse 是 /v1/models 的响应。
type ModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}
