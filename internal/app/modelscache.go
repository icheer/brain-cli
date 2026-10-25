package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/icheer/brain-cli/internal/gateway"
)

// modelsCache 是 /v1/models 的本地缓存（os.UserCacheDir，DESIGN.md 第 4 节）。
type modelsCache struct {
	FetchedAt time.Time `json:"fetched_at"`
	Models    []string  `json:"models"`
}

func cachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "brain", "models.json"), nil
}

// FetchModels 返回可用模型列表：
// TTL 内用缓存；过期或 force 时拉远端并回写缓存；
// 远端失败且存在（哪怕已过期的）缓存则降级返回缓存——此时 err 非 nil 但 models 可用。
func FetchModels(ctx context.Context, g *gateway.Client, ttl time.Duration, force bool) (models []string, fromCache bool, err error) {
	cached := readCache()

	if !force && time.Since(cached.FetchedAt) < ttl && len(cached.Models) > 0 {
		return cached.Models, true, nil
	}

	models, err = g.ListModels(ctx)
	if err == nil {
		writeCache(modelsCache{FetchedAt: time.Now(), Models: models})
		return models, false, nil
	}
	if len(cached.Models) > 0 {
		return cached.Models, true, err
	}
	return nil, false, err
}

// ReadCachedModels 只读缓存，供模型解析失败时给相近建议；任何失败返回 nil。
func ReadCachedModels() []string {
	return readCache().Models
}

func readCache() modelsCache {
	var c modelsCache
	p, err := cachePath()
	if err != nil {
		return c
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c // 无缓存文件
	}
	if json.Unmarshal(data, &c) != nil {
		return c // 损坏即弃用，等价于无缓存
	}
	return c
}

func writeCache(c modelsCache) {
	p, err := cachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	// 缓存不含敏感信息，0644 即可
	_ = os.WriteFile(p, data, 0o644)
}
