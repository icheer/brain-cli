// Package config 管理个人配置 config.yaml（含密钥，0600）与
// 可共享模型清单 models.yaml（不含密钥），以及「默认值 < 文件 < 环境变量」的合并。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config 与 DESIGN.md 2.2 的 config.yaml 一一对应。
type Config struct {
	BaseURL               string `yaml:"base_url"`
	APIKey                string `yaml:"api_key"`
	DefaultModel          string `yaml:"default_model"`
	TimeoutSeconds        int    `yaml:"timeout_seconds"`
	MaxInputBytes         int64  `yaml:"max_input_bytes"`
	RetryMax              int    `yaml:"retry_max"`
	ModelsCacheTTLSeconds int    `yaml:"models_cache_ttl_seconds"`
	UserAgent             string `yaml:"user_agent"` // 逃生口：个别网关对 UA 取值校验严格时覆盖默认 brain/<version>
}

// Default 返回代码内默认值（优先级最低）。
func Default() Config {
	return Config{
		TimeoutSeconds:        300,
		MaxInputBytes:         1 << 20, // 1 MiB
		RetryMax:              2,
		ModelsCacheTTLSeconds: 86400,
	}
}

// Path 返回 config.yaml 路径；BRAIN_CONFIG 环境变量可指定完整文件路径覆盖。
func Path() (string, error) {
	if p := os.Getenv("BRAIN_CONFIG"); p != "" {
		return p, nil
	}
	return userPath("config.yaml")
}

// ModelsPath 返回 models.yaml 路径；BRAIN_MODELS 环境变量可覆盖。
func ModelsPath() (string, error) {
	if p := os.Getenv("BRAIN_MODELS"); p != "" {
		return p, nil
	}
	return userPath("models.yaml")
}

func userPath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户配置目录: %w", err)
	}
	return filepath.Join(dir, "brain", name), nil
}

// FileError 表示配置文件层面的失败（损坏/不可读），归 exit 7 并给出修复入口。
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }

// Load 按「代码默认值 < config.yaml < 环境变量」合并（CLI flags 由 cmd 层最后覆盖）。
// 配置文件不存在不算错误；返回的 warnings 供调用方写 stderr。
func Load() (Config, []string, error) {
	cfg := Default()
	var warnings []string

	p, err := Path()
	if err != nil {
		return cfg, warnings, err
	}
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, warnings, &FileError{Path: p, Err: fmt.Errorf("YAML 解析失败: %w", err)}
		}
		// 权限检查：含密钥的文件应保持 0600（DESIGN.md 2.4）
		if runtime.GOOS != "windows" {
			if info, statErr := os.Stat(p); statErr == nil && info.Mode().Perm() != 0o600 {
				warnings = append(warnings, fmt.Sprintf(
					"config.yaml 权限为 %04o（应为 0600）：chmod 600 %s", info.Mode().Perm(), p))
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		// 未初始化，走调用方的「无配置」引导
	default:
		return cfg, warnings, &FileError{Path: p, Err: err}
	}

	// 环境变量覆盖（一次性使用场景：不落盘、不进 history）
	if v := os.Getenv("BRAIN_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("BRAIN_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("BRAIN_USER_AGENT"); v != "" {
		cfg.UserAgent = v
	}
	if v := os.Getenv("BRAIN_DEFAULT_MODEL"); v != "" {
		cfg.DefaultModel = v
	}
	if v := os.Getenv("BRAIN_TIMEOUT"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil {
			return cfg, warnings, fmt.Errorf("BRAIN_TIMEOUT 应为秒数: %w", perr)
		}
		cfg.TimeoutSeconds = n
	}

	// 字段缺失/非法时回退默认值，避免 0 值导致立即超时或拒绝一切输入
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 300
	}
	if cfg.MaxInputBytes <= 0 {
		cfg.MaxInputBytes = 1 << 20
	}
	if cfg.ModelsCacheTTLSeconds <= 0 {
		cfg.ModelsCacheTTLSeconds = 86400
	}
	if cfg.RetryMax < 0 {
		cfg.RetryMax = 0
	}
	return cfg, warnings, nil
}

// Save 原子写入 config.yaml（临时文件 + rename），目录 0700、文件 0600。
func Save(cfg Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
