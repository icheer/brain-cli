package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsCommand(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "")
	setupConfig(t, srv.URL+"/v1")

	code, out, errStr := runBrain("models")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
	for _, want := range []string{"别名（config.yaml aliases 段）", "fast", "m-a → m-b", "# 快", "可用模型", "m-a", "m-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺 %q:\n%s", want, out)
		}
	}

	// 第二次调用命中缓存（不再请求网关）
	code, out2, errStr2 := runBrain("models")
	if code != 0 || !strings.Contains(errStr2, "本地缓存") {
		t.Fatalf("第二次应命中缓存: code=%d stderr=%s", code, errStr2)
	}
	if !strings.Contains(out2, "m-a") {
		t.Fatalf("缓存输出缺模型: %s", out2)
	}
}

func TestModelsJSON(t *testing.T) {
	srv, _ := fakeGateway(t, 200, "")
	setupConfig(t, srv.URL+"/v1")

	code, out, _ := runBrain("models", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("应为单行 JSON + 单换行: %q", out)
	}
	var got struct {
		Aliases []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
			Use    string   `json:"use"`
		} `json:"aliases"`
		Models    []string `json:"models"`
		FromCache bool     `json:"from_cache"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("解析 JSON: %v\n%s", err, out)
	}
	if len(got.Aliases) != 1 || got.Aliases[0].Name != "fast" || got.Aliases[0].Use != "快" {
		t.Fatalf("aliases: %+v", got.Aliases)
	}
	if len(got.Models) != 2 || got.FromCache {
		t.Fatalf("models: %+v", got)
	}
}

// TestModelsOfflineNoCache 网关不可达且无缓存 → exit 5。
func TestModelsOfflineNoCache(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // 立即关闭，制造连接拒绝
	setupConfig(t, srv.URL+"/v1")

	code, _, errStr := runBrain("models")
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, errStr)
	}
}

// 多网关切换（CUJ J8）：切换 base_url 后缓存按网关隔离，不返回旧网关模型且各自保留缓存。
func TestModelsMultiGatewayCacheIsolation(t *testing.T) {
	var hitsA, hitsB int
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsA++
		_, _ = w.Write([]byte(`{"data":[{"id":"gw-a-model"}]}`))
	}))
	t.Cleanup(srvA.Close)
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsB++
		_, _ = w.Write([]byte(`{"data":[{"id":"gw-b-model"}]}`))
	}))
	t.Cleanup(srvB.Close)

	setupConfig(t, srvA.URL+"/v1")
	code, outA, _ := runBrain("models")
	if code != 0 || !strings.Contains(outA, "gw-a-model") || hitsA != 1 {
		t.Fatalf("网关 A 首拉失败: code=%d hitsA=%d out=%s", code, hitsA, outA)
	}

	// 切换到网关 B：不得命中网关 A 的缓存
	t.Setenv("BRAIN_BASE_URL", srvB.URL+"/v1")
	code, outB, _ := runBrain("models")
	if code != 0 || !strings.Contains(outB, "gw-b-model") || strings.Contains(outB, "gw-a-model") || hitsB != 1 {
		t.Fatalf("网关 B 串台到了 A 的缓存: code=%d hitsB=%d out=%s", code, hitsB, outB)
	}

	// 切回网关 A：A 的缓存仍在有效期内，无需再次请求远端
	t.Setenv("BRAIN_BASE_URL", srvA.URL+"/v1")
	code, outA2, errA2 := runBrain("models")
	if code != 0 || !strings.Contains(outA2, "gw-a-model") || !strings.Contains(errA2, "本地缓存") || hitsA != 1 {
		t.Fatalf("切回网关 A 应命中其独立缓存: code=%d hitsA=%d stderr=%s", code, hitsA, errA2)
	}
}
