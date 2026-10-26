package app

import (
	"sort"
	"strings"

	"github.com/icheer/brain-cli/internal/config"
	"github.com/icheer/brain-cli/internal/i18n"
)

// ModelChain 把 -m / default_model 展开为尝试链：
// 别名 → 其 fallback 链（原顺序，M1 起逐个故障转移）；字面 ID → 单元素链。
func ModelChain(name string, aliases map[string]config.Alias) ([]string, error) {
	if a, ok := aliases[name]; ok {
		if len(a.Models) == 0 {
			return nil, &InputError{Msg: i18n.Tf("config.yaml 别名 %q 的 models 列表为空", "alias %q in config.yaml has an empty models list", name)}
		}
		return a.Models, nil
	}
	return []string{name}, nil
}

// CompareModels 把 compare 的 -m 值列表展开为并行采样组（第 0 节「多模型编排」）：
// 别名 → 其全部模型（模型组快捷方式，与 ask 的链首位+容灾语义不同）；
// 字面 ID → 自身；每个值还支持逗号分隔；跨来源去重、保持首次出现顺序。
func CompareModels(names []string, aliases map[string]config.Alias) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(m string) {
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		out = append(out, m)
	}
	for _, name := range names {
		for _, n := range strings.Split(name, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if a, ok := aliases[n]; ok {
				if len(a.Models) == 0 {
					return nil, &InputError{Msg: i18n.Tf("config.yaml 别名 %q 的 models 列表为空", "alias %q in config.yaml has an empty models list", n)}
				}
				for _, m := range a.Models {
					add(m)
				}
				continue
			}
			add(n)
		}
	}
	return out, nil
}

// ResolveModel 解析链首位（DESIGN.md 第 4 节：别名 → 字面模型 ID，ID 交网关裁决）。
func ResolveModel(name string, aliases map[string]config.Alias) (string, error) {
	chain, err := ModelChain(name, aliases)
	if err != nil {
		return "", err
	}
	return chain[0], nil
}

// Suggest 从候选（本地别名 + 已缓存模型）中挑选相近项，用于模型不可用时的 stderr 提示。
// names 为触发来源（别名与其解析出的模型 ID 都可作种子）；
// 前缀命中优先，其次编辑距离 ≤ 2，最多 5 条。
func Suggest(names []string, aliases map[string]config.Alias, cachedModels []string) []string {
	seen := map[string]bool{}
	var out []string

	add := func(seed, c string) {
		if c == "" {
			return
		}
		for _, n := range names {
			if strings.EqualFold(c, n) {
				return
			}
		}
		if seen[c] {
			return
		}
		if strings.HasPrefix(strings.ToLower(c), strings.ToLower(seed)) {
			seen[c] = true
			out = append(out, c)
		} else if levenshtein(strings.ToLower(seed), strings.ToLower(c)) <= 2 {
			seen[c] = true
			out = append(out, c)
		}
	}

	aliasNames := make([]string, 0, len(aliases))
	for a := range aliases {
		aliasNames = append(aliasNames, a)
	}
	sort.Strings(aliasNames)
	for _, seed := range names {
		for _, a := range aliasNames {
			add(seed, a)
		}
		for _, m := range cachedModels {
			add(seed, m)
		}
	}

	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// levenshtein 计算两串编辑距离（输入已 lower，长度有限，O(nm) 足够）。
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
