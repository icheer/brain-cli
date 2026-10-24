package config

import (
	"os"
	"path/filepath"
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
	if got != in {
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

func TestLoadModelsAbsent(t *testing.T) {
	t.Setenv("BRAIN_MODELS", filepath.Join(t.TempDir(), "models.yaml"))
	mf, err := LoadModels()
	if err != nil {
		t.Fatalf("models.yaml 缺失不应报错: %v", err)
	}
	if len(mf.Aliases) != 0 {
		t.Fatalf("应为空清单: %+v", mf.Aliases)
	}
}

func TestLoadModelsParse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "models.yaml")
	writeFile(t, p, "aliases:\n  fast:\n    models: [a, b]\n    use: 快\n", 0o644)
	t.Setenv("BRAIN_MODELS", p)
	mf, err := LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	a := mf.Aliases["fast"]
	if len(a.Models) != 2 || a.Models[0] != "a" || a.Use != "快" {
		t.Fatalf("解析不符: %+v", a)
	}
}
