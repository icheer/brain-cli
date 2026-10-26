package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/icheer/brain-cli/internal/app"
	"github.com/icheer/brain-cli/internal/i18n"
)

// reservedBodyKeys --extra-body 的硬保留字：brain 生命线字段（覆盖会破坏传输/输入），
// 出现即拒绝（exit 1）。reasoning_effort / max_tokens 不在此列——它们允许经 extra 传入，
// 但与 -e / --max-output-tokens 冲突时以快车道 flag 为准（applyFastLanePrecedence）。
var reservedBodyKeys = map[string]bool{
	"model": true, "messages": true, "stream": true, "stream_options": true,
}

// reservedHeaderKeys --extra-headers 的保留字：鉴权只走 config/环境变量一条正道
// （防误覆盖造成鉴权混乱），Content-Type/Accept 由传输层决定。
// User-Agent 允许：extra-headers 在默认头之后应用，同名覆盖生效。
var reservedHeaderKeys = map[string]bool{
	"authorization": true, "content-type": true, "accept": true,
}

// parseExtrasJSON 解析 --extra-body/--extra-headers 取值：内联 JSON 或 @file；
// 可重复且有序合并（后出现者的同名键覆盖前者）；必须是 JSON 对象。
func parseExtrasJSON(values []string) (map[string]any, error) {
	out := map[string]any{}
	for _, v := range values {
		raw := v
		if after, ok := strings.CutPrefix(v, "@"); ok {
			b, err := os.ReadFile(after)
			if err != nil {
				return nil, &app.InputError{Msg: i18n.Tf("读取 %s 失败: %v", "failed to read %s: %v", v, err)}
			}
			raw = string(b)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, &app.InputError{Msg: i18n.Tf("%s 不是合法的 JSON 对象: %v", "%s is not a valid JSON object: %v", v, err)}
		}
		for k, val := range m {
			out[k] = val
		}
	}
	return out, nil
}

// parseExtraHeaders 在 parseExtrasJSON 之上要求值全为字符串（HTTP 头）。
func parseExtraHeaders(values []string) (map[string]string, error) {
	m, err := parseExtrasJSON(values)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, &app.InputError{Msg: i18n.Tf("--extra-headers 中 %q 的值必须是字符串", "value of %q in --extra-headers must be a string", k)}
		}
		out[k] = s
	}
	return out, nil
}

// applyFastLanePrecedence 快车道优先（v0.8.1 决策）：-e / --max-output-tokens 是高频
// 快车道，与 --extra-body 同名键冲突时以 flag 为准——从 extras 删除该键（flag 值已在
// ChatRequest 字段里自然生效），stderr 一行提示让覆盖可见可查。
func applyFastLanePrecedence(extraBody map[string]any, effort string, maxTokens int, stderr io.Writer) {
	if extraBody == nil {
		return
	}
	if _, ok := extraBody["reasoning_effort"]; ok && effort != "" {
		delete(extraBody, "reasoning_effort")
		fmt.Fprintf(stderr, "brain: %s\n",
			i18n.T("--extra-body 的 reasoning_effort 与 -e 冲突，以 -e 为准", "reasoning_effort in --extra-body conflicts with -e; -e wins"))
	}
	if _, ok := extraBody["max_tokens"]; ok && maxTokens > 0 {
		delete(extraBody, "max_tokens")
		fmt.Fprintf(stderr, "brain: %s\n",
			i18n.T("--extra-body 的 max_tokens 与 --max-output-tokens 冲突，以 flag 为准", "max_tokens in --extra-body conflicts with --max-output-tokens; the flag wins"))
	}
}

// rejectReserved 保留字拒绝（键匹配大小写不敏感；HTTP 头名本就如此）。
// 报错信息同时给出允许的替代路径，让调用方一次性改对。
func rejectReserved(m map[string]any, reserved map[string]bool, which string) error {
	var hits []string
	for k := range m {
		if reserved[strings.ToLower(k)] {
			hits = append(hits, k)
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Strings(hits)
	allowed := make([]string, 0, len(reserved))
	for k := range reserved {
		allowed = append(allowed, k)
	}
	sort.Strings(allowed)
	return &app.InputError{Msg: i18n.Tf("%s 含保留字（%s）——这些字段归 brain 专属机制管理: %s；reasoning_effort/max_tokens 用 -e/--max-output-tokens，Authorization 用 api_key 配置",
		"%s contains reserved keys (%s) — these fields belong to brain's own mechanisms: %s; use -e/--max-output-tokens for reasoning_effort/max_tokens, and the api_key config for Authorization",
		which, strings.Join(hits, ", "), strings.Join(allowed, "/"))}
}

// parseExtraFlags 统一入口：解析 + 保留字校验，返回可挂载到 ChatRequest 的两个 map。
// （ask 与 compare 共用；任何错误均为调用方用法错误 → exit 1）
func parseExtraFlags(bodyVals, headerVals []string) (map[string]any, map[string]string, error) {
	var body map[string]any
	var headers map[string]string
	var err error
	if len(bodyVals) > 0 {
		if body, err = parseExtrasJSON(bodyVals); err != nil {
			return nil, nil, err
		}
		if err = rejectReserved(body, reservedBodyKeys, "--extra-body"); err != nil {
			return nil, nil, err
		}
	}
	if len(headerVals) > 0 {
		var raw map[string]any
		if raw, err = parseExtrasJSON(headerVals); err != nil {
			return nil, nil, err
		}
		if err = rejectReserved(raw, reservedHeaderKeys, "--extra-headers"); err != nil {
			return nil, nil, err
		}
		if headers, err = parseExtraHeaders(headerVals); err != nil {
			return nil, nil, err
		}
	}
	return body, headers, nil
}

// extraBodyFlagsHelp / extraHeadersFlagsHelp 双语帮助文案（ask 与 compare 复用）。
func extraBodyFlagsHelp() string {
	return i18n.T("附加请求体参数：JSON 对象（内联或 @file），可重复有序合并，顶层覆盖式透传给网关（如 {\"temperature\":0.3,\"response_format\":{...}}）；保留字 model/messages/stream/stream_options 被拒绝；reasoning_effort/max_tokens 可传，但与 -e/--max-output-tokens 冲突时以 flag 为准",
		"extra request-body fields: a JSON object (inline or @file), repeatable and merged in order; top-level keys pass through to the gateway (e.g. {\"temperature\":0.3,\"response_format\":{...}}); reserved keys model/messages/stream/stream_options are rejected; reasoning_effort/max_tokens are allowed but -e/--max-output-tokens win on conflict")
}

func extraHeadersFlagsHelp() string {
	return i18n.T("附加 HTTP 请求头：JSON 对象（内联或 @file），值须为字符串，可重复有序合并；默认头之后应用，同名覆盖（User-Agent 可经此覆盖）；保留字 Authorization/Content-Type/Accept 被拒绝",
		"extra HTTP headers: a JSON object (inline or @file) with string values, repeatable and merged in order; applied after brain's default headers, overriding same-named ones (User-Agent included); reserved keys Authorization/Content-Type/Accept are rejected")
}
