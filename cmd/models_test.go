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
	for _, want := range []string{"别名（models.yaml）", "fast", "m-a → m-b", "# 快", "可用模型", "m-a", "m-b"} {
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
