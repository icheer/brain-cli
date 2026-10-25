// Package gateway 实现 OpenAI Chat Completions 兼容协议的客户端
// （/v1/chat/completions + /v1/models）。
// 传输层始终走 SSE（DESIGN.md 第 0 节：防中间层掐断空闲连接、支撑心跳）。
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

// StreamOptions 请求流式响应在末尾携带 usage；个别网关不支持时静默忽略（usage 为 nil）。
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatRequest 只声明 brain 用到的字段；采样参数刻意不传（DESIGN.md 第 0 节已定）。
// reasoning_effort 是行为开关而非采样参数：-e 显式设置才透传，空则字段缺席（各模型用自身默认）。
type ChatRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	Stream          bool           `json:"stream"`
	StreamOptions   *StreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

// Usage 是一次调用的 token 计量（--json 输出用）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ModelsResponse 是 /v1/models 的响应。
type ModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}
