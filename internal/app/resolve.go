package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/icheer/brain-cli/internal/config"
)

// ResolveModel 解析 -m / default_model：
// models.yaml 别名（取 fallback 链首位）→ 字面模型 ID（交给网关裁决，TODO.md 第 4 节）。
func ResolveModel(name string, mf config.ModelsFile) (string, error) {
	if a, ok := mf.Aliases[name]; ok {
		if len(a.Models) == 0 {
			return "", &InputError{Msg: fmt.Sprintf("models.yaml 中别名 %q 的 models 列表为空", name)}
		}
		return a.Models[0], nil
	}
	return name, nil
}

// Suggest 从候选（本地别名 + 已缓存模型）中挑选相近项，用于模型不可用时的 stderr 提示。
// names 为触发来源（别名与其解析出的模型 ID 都可作种子）；
// 前缀命中优先，其次编辑距离 ≤ 2，最多 5 条。
func Suggest(names []string, mf config.ModelsFile, cachedModels []string) []string {
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

	aliasNames := make([]string, 0, len(mf.Aliases))
	for a := range mf.Aliases {
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
