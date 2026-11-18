package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/icheer/brain-cli/internal/gateway"
)

// modelsCacheEntry 是单个网关（按归一 BaseURL 区分）的 /v1/models 缓存条目。
type modelsCacheEntry struct {
	FetchedAt time.Time `json:"fetched_at"`
	Models    []string  `json:"models"`
}

// modelsCache 是 /v1/models 的本地缓存文件结构（os.UserCacheDir，DESIGN.md 第 4 节）。
// 按网关 BaseURL 隔离条目，避免 BRAIN_CONFIG / BRAIN_BASE_URL 切换多网关时串台或互相覆盖。
type modelsCache struct {
	Entries map[string]modelsCacheEntry `json:"entries,omitempty"`
}

func cachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "brain", "models.json"), nil
}

// FetchModels 返回可用模型列表：
// TTL 内用当前网关缓存；过期或 force 时拉远端并回写该网关条目；
// 远端失败且存在同一网关（哪怕已过期的）缓存则降级返回缓存——此时 err 非 nil 但 models 可用。
func FetchModels(ctx context.Context, g *gateway.Client, ttl time.Duration, force bool) (models []string, fromCache bool, err error) {
	key := gateway.OpenAIBase(g.BaseURL)
	cached := readCache()
	entry := cached.Entries[key]

	if !force && time.Since(entry.FetchedAt) < ttl && len(entry.Models) > 0 {
		return entry.Models, true, nil
	}

	models, err = g.ListModels(ctx)
	if err == nil {
		if cached.Entries == nil {
			cached.Entries = map[string]modelsCacheEntry{}
		}
		cached.Entries[key] = modelsCacheEntry{FetchedAt: time.Now(), Models: models}
		writeCache(cached)
		return models, false, nil
	}
	if len(entry.Models) > 0 {
		return entry.Models, true, err
	}
	return nil, false, err
}

// ReadCachedModels 只读指定网关的缓存模型，供模型解析失败时给相近建议；任何失败返回 nil。
func ReadCachedModels(baseURL string) []string {
	key := gateway.OpenAIBase(baseURL)
	if key == "" {
		return nil
	}
	return readCache().Entries[key].Models
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
		return modelsCache{} // 损坏即弃用，等价于无缓存
	}
	return c
}

func writeCache(c modelsCache) {
	p, err := cachePath()
	if err != nil {
		return
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	// 临时文件 + Rename 原子落盘（防并发写入半截 JSON），缓存不含敏感信息 0644 即可
	f, err := os.CreateTemp(dir, "models-*.tmp")
	if err != nil {
		return
	}
	tmp := f.Name()
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return
	}
	_ = f.Chmod(0o644)
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}
