package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/exitcode"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "查看/修改个人配置（config.yaml，含密钥，0600）",
	Long: `管理 config.yaml（个人配置，含密钥，永不进仓库）：

  brain config path            打印配置文件路径
  brain config get <key>       查看某项（api_key 原样输出）
  brain config set <key> <v>   修改某项（写入后权限保持 0600）
  brain config list            列出全部（api_key 打码）
  brain config edit            用编辑器打开（$VISUAL / $EDITOR）`,
}

// configKeys 允许 get/set/list 的键，与 config.Config 字段一一对应。
var configKeys = []string{
	"base_url", "api_key", "default_model",
	"timeout_seconds", "max_input_bytes", "retry_max", "models_cache_ttl_seconds", "user_agent",
}

func init() {
	configCmd.AddCommand(configPathCmd, configGetCmd, configSetCmd, configListCmd, configEditCmd)
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "打印配置文件路径",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cp, err := config.Path()
		if err != nil {
			return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Usage, "定位配置路径失败: %v", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), cp)
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "查看某项配置",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := config.Load()
		if err != nil {
			return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Usage, "%v", err)
		}
		v, ok := cfgValue(cfg, args[0])
		if !ok {
			return unknownKey(cmd, args[0])
		}
		fmt.Fprintln(cmd.OutOrStdout(), v)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "修改某项配置",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		stderr := cmd.ErrOrStderr()
		cfg, _, err := config.Load()
		if err != nil {
			var fe *config.FileError
			if !errors.As(err, &fe) {
				return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
			}
			// 文件损坏时从默认值重建，避免用户被锁死
			cfg = config.Default()
		}
		if err := cfgSetValue(&cfg, args[0], args[1]); err != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
		}
		if err := config.Save(cfg); err != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
		}
		p, _ := config.Path()
		fmt.Fprintf(stderr, "brain: 已写入 %s\n", p)
		return nil
	},
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出全部配置（api_key 打码）",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, _, err := config.Load()
		if err != nil {
			return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Usage, "%v", err)
		}
		out := cmd.OutOrStdout()
		for _, k := range configKeys {
			v, _ := cfgValue(cfg, k)
			if k == "api_key" {
				v = mask(v)
			}
			fmt.Fprintf(out, "%s = %s\n", k, v)
		}
		return nil
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "用编辑器打开 config.yaml",
	RunE: func(cmd *cobra.Command, _ []string) error {
		stderr := cmd.ErrOrStderr()
		p, err := config.Path()
		if err != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := config.Save(config.Default()); err != nil {
				return exitcode.Fail(stderr, exitcode.Usage, "%v", err)
			}
		}
		editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), defaultEditor())
		c := exec.Command(editor, p)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return exitcode.Fail(stderr, exitcode.Usage, "编辑器 %s 退出异常: %v", editor, err)
		}
		return nil
	},
}

func defaultEditor() string {
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func unknownKey(cmd *cobra.Command, key string) error {
	return exitcode.Fail(cmd.ErrOrStderr(), exitcode.Usage,
		"未知配置项 %q，可用: %s", key, strings.Join(configKeys, ", "))
}

func cfgValue(cfg config.Config, key string) (string, bool) {
	switch key {
	case "base_url":
		return cfg.BaseURL, true
	case "api_key":
		return cfg.APIKey, true
	case "default_model":
		return cfg.DefaultModel, true
	case "timeout_seconds":
		return strconv.Itoa(cfg.TimeoutSeconds), true
	case "max_input_bytes":
		return strconv.FormatInt(cfg.MaxInputBytes, 10), true
	case "retry_max":
		return strconv.Itoa(cfg.RetryMax), true
	case "models_cache_ttl_seconds":
		return strconv.Itoa(cfg.ModelsCacheTTLSeconds), true
	case "user_agent":
		return cfg.UserAgent, true
	}
	return "", false
}

func cfgSetValue(cfg *config.Config, key, value string) error {
	switch key {
	case "base_url":
		cfg.BaseURL = value
	case "api_key":
		cfg.APIKey = value
	case "default_model":
		cfg.DefaultModel = value
	case "timeout_seconds":
		return setPositiveInt(key, value, &cfg.TimeoutSeconds, 1)
	case "max_input_bytes":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("max_input_bytes 应为正整数，得到 %q", value)
		}
		cfg.MaxInputBytes = n
	case "retry_max":
		return setPositiveInt(key, value, &cfg.RetryMax, 0)
	case "models_cache_ttl_seconds":
		return setPositiveInt(key, value, &cfg.ModelsCacheTTLSeconds, 1)
	case "user_agent":
		cfg.UserAgent = value
	default:
		return fmt.Errorf("未知配置项 %q，可用: %s", key, strings.Join(configKeys, ", "))
	}
	return nil
}

func setPositiveInt(key, value string, dst *int, minVal int) error {
	n, err := strconv.Atoi(value)
	if err != nil || n < minVal {
		return fmt.Errorf("%s 应为不小于 %d 的整数，得到 %q", key, minVal, value)
	}
	*dst = n
	return nil
}

// mask 打码密钥用于 list 展示：保留前 3 后 4。
func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:3] + "****" + s[len(s)-4:]
}
