package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Alias 是 models.yaml 中的一个别名条目：真实模型 fallback 链 + 场景标注（TODO.md 2.3）。
type Alias struct {
	Models []string `yaml:"models"` // 前者为主，故障自动切换后者（M1 落地）
	Use    string   `yaml:"use"`    // 场景标注，供 brain models 展示
}

// ModelsFile 是 models.yaml 的根结构。该文件可共享、不含密钥，整体可选。
type ModelsFile struct {
	Aliases map[string]Alias `yaml:"aliases"`
}

// LoadModels 读取 models.yaml；文件不存在返回空清单。
func LoadModels() (ModelsFile, error) {
	var mf ModelsFile
	p, err := ModelsPath()
	if err != nil {
		return mf, err
	}
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &mf); err != nil {
			return mf, fmt.Errorf("models.yaml 解析失败: %w", err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// models.yaml 可选，缺失时 -m 直接按字面模型 ID 处理
	default:
		return mf, err
	}
	return mf, nil
}

// SaveModels 原子写入 models.yaml（不含密钥，0644，可共享；brain init 生成别名用）。
func SaveModels(mf ModelsFile) error {
	p, err := ModelsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(mf)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
