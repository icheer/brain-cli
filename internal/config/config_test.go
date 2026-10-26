package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// isolateEnv 清空环境变量覆盖，避免宿主机环境影响测试。
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"BRAIN_BASE_URL", "BRAIN_API_KEY", "BRAIN_TIMEOUT"} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("BRAIN_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	isolateEnv(t)
	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TimeoutSeconds != 300 || cfg.MaxInputBytes != 1<<20 ||
		cfg.RetryMax != 2 || cfg.ModelsCacheTTLSeconds != 86400 {
		t.Fatalf("默认值不符: %+v", cfg)
	}
}

func TestLoadFileAndEnvPriority(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, p, "base_url: https://file.example/v1\napi_key: sk-file\ndefault_model: fast\ntimeout_seconds: 120\n", 0o600)
	t.Setenv("BRAIN_CONFIG", p)
	t.Setenv("BRAIN_API_KEY", "sk-env")
	t.Setenv("BRAIN_BASE_URL", "")
	t.Setenv("BRAIN_TIMEOUT", "")

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// 环境变量 > config.yaml
	if cfg.APIKey != "sk-env" {
		t.Fatalf("api_key 应被环境变量覆盖，得到 %q", cfg.APIKey)
	}
	// 未被覆盖的走文件值
	if cfg.BaseURL != "https://file.example/v1" || cfg.TimeoutSeconds != 120 || cfg.DefaultModel != "fast" {
		t.Fatalf("文件值不符: %+v", cfg)
	}
}

func TestSavePerm0600AndRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("BRAIN_CONFIG", p)

	in := Config{
		BaseURL: "https://x.example/v1", APIKey: "sk-1", DefaultModel: "fast",
		TimeoutSeconds: 10, MaxInputBytes: 100, RetryMax: 1, ModelsCacheTTLSeconds: 60,
		Aliases: map[string]Alias{"fast": {Models: []string{"m-a", "m-b"}, Use: "快"}},
	}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("权限应为 0600，得到 %04o", info.Mode().Perm())
	}
	// 残留的临时文件不应存在
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("临时文件残留: %v", err)
	}

	got, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Config 含 map 字段不可直接比较，用 DeepEqual
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip 不一致: %+v", got)
	}
}

func TestPermissionWarning(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, p, "base_url: https://x/v1\n", 0o644)
	t.Setenv("BRAIN_CONFIG", p)
	isolateEnv(t)

	_, warnings, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "0600") {
		t.Fatalf("应产生权限警告，得到 %v", warnings)
	}
}

func TestLoadCorruptYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, p, "base_url: [unclosed\n", 0o600)
	t.Setenv("BRAIN_CONFIG", p)
	isolateEnv(t)
	_, _, err := Load()
	if err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("损坏 YAML 应报错，得到 %v", err)
	}
}

// aliases 段随 config.yaml 一并解析（2026-10-26 单文件化）；无该段时为空且不报错。
func TestLoadAliasesSection(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, p, "base_url: https://x/v1\napi_key: k\naliases:\n  fast:\n    models: [a, b]\n    use: 快\n", 0o600)
	t.Setenv("BRAIN_CONFIG", p)
	isolateEnv(t)

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Aliases["fast"]
	if len(a.Models) != 2 || a.Models[0] != "a" || a.Use != "快" {
		t.Fatalf("解析不符: %+v", a)
	}

	// 无 aliases 段：空 map，不报错
	p2 := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, p2, "base_url: https://x/v1\napi_key: k\n", 0o600)
	t.Setenv("BRAIN_CONFIG", p2)
	cfg2, _, err := Load()
	if err != nil || len(cfg2.Aliases) != 0 {
		t.Fatalf("无 aliases 段应为空: %+v, %v", cfg2.Aliases, err)
	}
}
