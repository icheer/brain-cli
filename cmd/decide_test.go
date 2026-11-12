package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupDecideConfig(t *testing.T, srvURL, model string) string {
	t.Helper()
	dir := setupConfig(t, srvURL)
	lines := "decide:\n  model: " + model + "\n  base_url: " + srvURL + "\n  api_key: decide-key\n"
	appendConfig(t, dir, lines)
	return dir
}

// fakeDecideGateway 固定响应的假 systemone 端点，记录最近一次请求。
func fakeDecideGateway(t *testing.T, status int) (*httptest.Server, *[]byte) {
	t.Helper()
	var body []byte
	done := make(chan struct{}, 1)
	done <- struct{}{}
	resp := `{"model":"jev-1.14.0","answers":{"is_urgent":{"type":"noul","noul":0.97},"dept":{"type":"choice","choice":"technical","confidence":0.92,"probabilities":{"technical":0.92,"billing":0.08}}},"usage":{"input_tokens":210,"output_tokens":40}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
		b, _ := io.ReadAll(r.Body)
		body = b
		done <- struct{}{}
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

func TestDecideNoModelExit7(t *testing.T) {
	srv, _ := fakeDecideGateway(t, 200)
	setupConfig(t, srv.URL+"/v1") // 无 decide 段

	code, _, errStr := runBrain("decide", "-Q", `{"q":{"type":"noul","instructions":"x"}}`, "state")
	if code != 7 || !strings.Contains(errStr, "decide.model") {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

func TestDecideInputValidation(t *testing.T) {
	srv, _ := fakeDecideGateway(t, 200)
	setupDecideConfig(t, srv.URL, "jev-latest")

	// 无 state
	code, _, errStr := runBrain("decide", "-Q", `{"q":{"type":"noul","instructions":"x"}}`)
	if code != 1 || !strings.Contains(errStr, "state") {
		t.Fatalf("无 state 应 exit 1: code=%d stderr=%s", code, errStr)
	}
	// 无 questions
	decideOpts = decideOptions{}
	code, _, errStr = runBrain("decide", "some state")
	if code != 1 || !strings.Contains(errStr, "questions") {
		t.Fatalf("无 questions 应 exit 1: code=%d stderr=%s", code, errStr)
	}
	// --noul 格式错
	decideOpts = decideOptions{}
	code, _, errStr = runBrain("decide", "--noul", "no-equals-here", "state")
	if code != 1 || !strings.Contains(errStr, "--noul") {
		t.Fatalf("--noul 格式错应 exit 1: code=%d stderr=%s", code, errStr)
	}
	// ID 冲突（-Q 与 --noul）
	decideOpts = decideOptions{}
	code, _, errStr = runBrain("decide", "-Q", `{"q":{"type":"noul","instructions":"x"}}`, "--noul", "q=y", "state")
	if code != 1 || !strings.Contains(errStr, "重复") {
		t.Fatalf("ID 冲突应 exit 1: code=%d stderr=%s", code, errStr)
	}
}

func TestDecideStateAssemblyAskLike(t *testing.T) {
	srv, body := fakeDecideGateway(t, 200)
	setupDecideConfig(t, srv.URL, "jev-latest")
	dir := t.TempDir()
	f := filepath.Join(dir, "evidence.txt")
	if err := os.WriteFile(f, []byte("evidence body"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errStr := runBrain("decide", "-f", f, "--noul", "is_ok=looks fine?", "the claim")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var m map[string]any
	if err := json.Unmarshal(*body, &m); err != nil {
		t.Fatal(err)
	}
	state, _ := m["state"].(string)
	// ask 同款 XML 区隔：user_message + attachment id 序号
	if !strings.Contains(state, "<user_message>") || !strings.Contains(state, `<attachment id=1`) {
		t.Errorf("state 应走 ask 同款 XML 装配: %q", state)
	}
	// 键序敏感：断言须用原始字节（重新 Marshal map 会按字典序重排，恰好是被测的缺陷）
	if !strings.Contains(string(*body), `"is_ok":{"type":"noul","instructions":"looks fine?"}`) {
		t.Errorf("questions 合并/键序失败: %s", *body)
	}
}

func TestDecideQuestionsFile(t *testing.T) {
	srv, body := fakeDecideGateway(t, 200)
	setupDecideConfig(t, srv.URL, "jev-latest")
	dir := t.TempDir()
	qf := filepath.Join(dir, "q.json")
	if err := os.WriteFile(qf, []byte(`{"risk":{"type":"score","instructions":"how risky","criteria":["low","high"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errStr := runBrain("decide", "-Q", "@"+qf, "--noul", "extra=sure?", "state")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var m map[string]any
	if err := json.Unmarshal(*body, &m); err != nil {
		t.Fatal(err)
	}
	qs, _ := json.Marshal(m["questions"])
	if !strings.Contains(string(qs), `"risk"`) || !strings.Contains(string(qs), `"extra"`) {
		t.Errorf("@file + --noul 合并失败: %s", qs)
	}
}

func TestDecideStdoutAndJSONContract(t *testing.T) {
	srv, _ := fakeDecideGateway(t, 200)
	setupDecideConfig(t, srv.URL, "jev-latest")

	// 默认 stdout = answers 原样字节（可直接 jq）
	code, out, errStr := runBrain("decide", "--noul", "q=x?", "state")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var answers map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &answers); err != nil {
		t.Fatalf("stdout 应为 answers JSON: %v\n%s", err, out)
	}
	if answers["is_urgent"] == nil {
		t.Fatalf("stdout 应含 is_urgent: %s", out)
	}

	// --json 恒定单对象外壳
	decideOpts = decideOptions{}
	code, out, errStr = runBrain("decide", "--json", "--noul", "q=x?", "state")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var env struct {
		Requested string          `json:"requested_model"`
		Model     string          `json:"model"`
		Answers   json.RawMessage `json:"answers"`
		Usage     json.RawMessage `json:"usage"`
		LatencyMs int64           `json:"latency_ms"`
		Error     *askJSONError   `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("非单对象 JSON: %v\n%s", err, out)
	}
	if env.Requested != "jev-latest" || env.Model != "jev-1.14.0" || env.Error != nil || env.LatencyMs < 0 {
		t.Errorf("envelope: %+v", env)
	}
	if !strings.Contains(string(env.Answers), `"noul":0.97`) {
		t.Errorf("answers 应内嵌原样: %s", env.Answers)
	}
}

func TestDecideJSONErrorContract(t *testing.T) {
	srv, _ := fakeDecideGateway(t, 401)
	setupDecideConfig(t, srv.URL, "jev-latest")

	code, out, _ := runBrain("decide", "--json", "--noul", "q=x?", "state")
	if code != 2 { // 401 → exit 2（ask 同构）
		t.Fatalf("code=%d", code)
	}
	var env struct {
		Error *askJSONError `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil || env.Error == nil {
		t.Fatalf("error envelope: %v\n%s", err, out)
	}
	if env.Error.Code != 2 || env.Error.UpstreamStatus != 401 {
		t.Errorf("error=%+v", env.Error)
	}
}

func TestDecideDryRunShape(t *testing.T) {
	srv, _ := fakeDecideGateway(t, 200)
	setupDecideConfig(t, srv.URL, "jev-latest")

	code, out, errStr := runBrain("decide", "--dry-run", "--noul", "q=x?",
		"--extra-body", `{"assets":{"i1":"..."}}`, "--extra-headers", `{"TypeSafe-Danger-Zone-Images":"true"}`, "state")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var d struct {
		Endpoint     string            `json:"endpoint"`
		State        string            `json:"state"`
		Questions    json.RawMessage   `json:"questions"`
		ExtraBody    map[string]any    `json:"extra_body"`
		ExtraHeaders map[string]string `json:"extra_headers"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		t.Fatal(err)
	}
	if d.Endpoint != srv.URL+"/v1/systemone" {
		t.Errorf("endpoint=%q", d.Endpoint)
	}
	if !strings.Contains(string(d.Questions), `"type":"noul"`) || d.ExtraHeaders["TypeSafe-Danger-Zone-Images"] != "true" || d.ExtraBody["assets"] == nil {
		t.Errorf("dry-run 形状: %s", out)
	}
}

func TestDecideExtraBodyReserved(t *testing.T) {
	srv, _ := fakeDecideGateway(t, 200)
	setupDecideConfig(t, srv.URL, "jev-latest")

	code, _, errStr := runBrain("decide", "--extra-body", `{"state":"x"}`, "--noul", "q=x?", "s")
	if code != 1 || !strings.Contains(errStr, "保留字") || !strings.Contains(errStr, "-Q") {
		t.Fatalf("保留字应拒绝并给替代路径: code=%d stderr=%s", code, errStr)
	}
}

// setupConfig 里 reset decideOpts（cobra flag 残留防护），此测试验证逃生口图片路径的字段级形状。
func TestDecideEscapeHatchImageShape(t *testing.T) {
	srv, body := fakeDecideGateway(t, 200)
	setupDecideConfig(t, srv.URL, "jev-latest")

	code, _, errStr := runBrain("decide",
		"--extra-body", `{"assets":{"img-1":{"type":"image","format":"png","data_base64":"iVBOR"}}}`,
		"--extra-headers", `{"TypeSafe-Danger-Zone-Images":"true"}`,
		"--noul", "same=does photo match?", "check")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	var m map[string]any
	if err := json.Unmarshal(*body, &m); err != nil {
		t.Fatal(err)
	}
	assets, ok := m["assets"].(map[string]any)
	if !ok || assets["img-1"] == nil {
		t.Fatalf("assets 应经逃生口透传: %v", m["assets"])
	}
}
