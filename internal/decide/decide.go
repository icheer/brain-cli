// Package decide 实现决策原语（brain decide，M12）。
// 协议仅 TypeSafe wire（systemone 事实标准）：POST {base}/v1/systemone，请求
// {model, state, questions}（questions=map id→{type: noul|choice|score, …}），
// 响应 {model, answers, usage}。answers/usage 以 json.RawMessage 原样透传
// （键序=服务端返回序，confidence 为可选字段不合成）；questions 由调用方经
// QuestionBuilder 有序构建（json.RawMessage 字节流不重排——Amazon 实测键序
// 可移动概率 0.19）。不做跨模型故障转移（概率校准基准不可互换，见 DESIGN §0）。
package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/icheer/brain-cli/internal/draw"
	"github.com/icheer/brain-cli/internal/gateway"
	"github.com/icheer/brain-cli/internal/i18n"
)

// 响应体上限：answers 是类型化小对象（值+概率分布），1 MiB 绰绰有余。
const maxResponseBytes = 1 << 20

// Request decide 的统一请求模型（单方言，无 type 字段）。
type Request struct {
	Model     string
	State     string          // 证据（cmd 层用 ask 同款装配：positional/管道/-f+XML）
	Questions json.RawMessage // 有序构建的 questions 对象（QuestionBuilder 产物）
	// ExtraBody --extra-body 顶层合并（保留字 state/questions/model 已在 cmd 层拒绝；
	// 图片经 assets 走此通道 + --extra-headers 的 Danger-Zone 头——文档化的逃生口用法）。
	ExtraBody    map[string]any
	ExtraHeaders map[string]string
}

// Response 归一后的决策结果：Answers/Usage 原样字节（stdout 直出、零重序列化）。
type Response struct {
	Model   string
	Answers json.RawMessage
	Usage   json.RawMessage
}

// Client 决策客户端。BaseURL 为用户原始填写值（末尾 /v1 可写可不写，拼接同 draw）。
type Client struct {
	HTTP    *http.Client
	BaseURL string
	APIKey  string
}

// New 构造客户端；timeout 为传输层兜底，总预算由调用方经 ctx 控制。
func New(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{HTTP: &http.Client{Timeout: timeout}, BaseURL: baseURL, APIKey: apiKey}
}

// Endpoint 实际请求端点（dry-run 展示与请求共用；不含鉴权参数）。
func (c *Client) Endpoint() string {
	return draw.OpenAIBase(c.BaseURL) + "/systemone"
}

// Do 执行一次决策请求：同模型重试（429 含 Retry-After/5xx/网络错误，至多 3 次尝试，
// 总预算内），不跨模型故障转移。
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	// 请求体三个顶层键（map 序经 json.Marshal 排序为 model/questions/state——键序
	// 敏感的是 questions/criteria 内部，那是 RawMessage 原样字节，不受影响）
	body := map[string]any{
		"model":     req.Model,
		"state":     req.State,
		"questions": req.Questions,
	}
	for k, v := range req.ExtraBody {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("序列化请求失败", "failed to marshal request"), err)
	}

	var data []byte
	for attempt := 1; ; attempt++ {
		data, err = c.post(ctx, b, req.ExtraHeaders)
		if err == nil {
			break
		}
		if attempt >= 3 || !retryable(err) || ctx.Err() != nil {
			return nil, err
		}
		wait := time.Duration(attempt) * time.Second // 1s/2s 退避
		var se *gateway.StatusError
		if errors.As(err, &se) && se.RetryAfter > 0 {
			wait = se.RetryAfter // 429 的 Retry-After 优先
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, &gateway.TimeoutError{Err: ctx.Err()}
		}
	}

	var out struct {
		Model   string          `json:"model"`
		Answers json.RawMessage `json:"answers"`
		Usage   json.RawMessage `json:"usage"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w（%s: %s）", i18n.T("解析网关响应失败", "failed to parse gateway response"), err,
			i18n.T("响应片段", "response snippet"), snippet(data, 200))
	}
	if out.Error != nil { // 个别网关 200 夹带 error 体（one-api 系老毛病）
		return nil, &gateway.StatusError{Status: out.Error.Code, Body: out.Error.Message}
	}
	// answers 缺失或空 → 上游错误（exit 5）：单答案缺 confidence 不补不合成（协议如实）
	var shadow map[string]json.RawMessage
	if err := json.Unmarshal(out.Answers, &shadow); err != nil || len(shadow) == 0 {
		return nil, fmt.Errorf("%s: %s", i18n.T("上游响应无有效 answers", "upstream response has no valid answers"), snippet(data, 200))
	}
	return &Response{Model: out.Model, Answers: out.Answers, Usage: out.Usage}, nil
}

// retryAfter/wait 之后的 post：Bearer 鉴权 + extras 头（默认头之后应用，同名覆盖）。
func (c *Client) post(ctx context.Context, body []byte, extraHeaders map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("构造请求失败", "failed to build request"), err)
	}
	req.Header.Set("User-Agent", gateway.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	for k, v := range extraHeaders {
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("读取响应失败", "failed to read response"), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &gateway.StatusError{Status: resp.StatusCode, Body: snippet(data, 400), RetryAfter: retryAfter(resp)}
	}
	return data, nil
}

// retryable 同模型重试的判定：429/5xx 与网络错误；超时不重试（总预算语义）。
func retryable(err error) bool {
	var se *gateway.StatusError
	if errors.As(err, &se) {
		return se.Status == http.StatusTooManyRequests || se.Status >= 500
	}
	var te *gateway.TimeoutError
	return !errors.As(err, &te) // 网络错误重试，超时不重试
}

// ---- QuestionBuilder：questions 的有序构建（字节流不重排键序）----

// QuestionBuilder 按插入序合并 questions（-Q 的 JSON 对象按原键序展开，--noul 速记
// 按出现序追加），重复 ID 即报错（ID 冲突 exit 1 由 cmd 层映射）。
type QuestionBuilder struct {
	ids  []string
	vals map[string]json.RawMessage
}

// AddJSON 并入一个 questions JSON 对象（-Q 取值；键序保留为原字节序）。
func (qb *QuestionBuilder) AddJSON(raw []byte) error {
	kvs, err := orderedObject(raw)
	if err != nil {
		return err
	}
	for _, kv := range kvs {
		if err := qb.add(kv.Key, kv.Value); err != nil {
			return err
		}
	}
	return nil
}

// AddNoul 追加一个 noul 速记问题（type 固定，instructions 由调用方切分）。
// 值手拼保持官方文档形态（type 在前）——json.Marshal(map) 会按字典序重排键。
func (qb *QuestionBuilder) AddNoul(id, instructions string) error {
	v, err := json.Marshal(instructions)
	if err != nil {
		return err
	}
	return qb.add(id, json.RawMessage(`{"type":"noul","instructions":`+string(v)+`}`))
}

// Raw 序列化为 questions 对象（插入序；值保持原始字节）。
func (qb *QuestionBuilder) Raw() (json.RawMessage, error) {
	if len(qb.ids) == 0 {
		return nil, nil
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, id := range qb.ids {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(id)
		b.Write(k)
		b.WriteByte(':')
		b.Write(qb.vals[id])
	}
	b.WriteByte('}')
	return json.RawMessage(b.Bytes()), nil
}

func (qb *QuestionBuilder) add(id string, v json.RawMessage) error {
	if qb.vals == nil {
		qb.vals = map[string]json.RawMessage{}
	}
	if _, dup := qb.vals[id]; dup {
		return fmt.Errorf("%s: %s", i18n.T("问题 ID 重复", "duplicate question ID"), id)
	}
	qb.ids = append(qb.ids, id)
	qb.vals[id] = v
	return nil
}

// orderedObject 按原键序解析一个 JSON 对象（Go map 会重排，故用 Decoder 逐令牌走）。
func orderedObject(raw []byte) ([]struct {
	Key   string
	Value json.RawMessage
}, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("不是合法的 JSON 对象", "not a valid JSON object"), err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New(i18n.T("questions 必须是 JSON 对象（map id→问题）", "questions must be a JSON object (map id→question)"))
	}
	var out []struct {
		Key   string
		Value json.RawMessage
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("不是合法的 JSON 对象", "not a valid JSON object"), err)
		}
		key, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("不是合法的 JSON 对象", "not a valid JSON object"), err)
		}
		out = append(out, struct {
			Key   string
			Value json.RawMessage
		}{key, v})
	}
	return out, nil
}

// ---- 与 draw/gateway 同构的小助手 ----

// retryAfter 解析 Retry-After 头（秒数；非法返回 0）。
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
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

// snippet 截断展示（错误体片段）。
func snippet(data []byte, n int) string {
	s := string(data)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
