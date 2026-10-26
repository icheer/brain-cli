package app

import (
	"testing"

	"github.com/icheer/brain-cli/internal/config"
)

func TestResolveAlias(t *testing.T) {
	aliases := map[string]config.Alias{
		"fast": {Models: []string{"m-primary", "m-backup"}},
	}
	got, err := ResolveModel("fast", aliases)
	if err != nil || got != "m-primary" {
		t.Fatalf("别名应取链首位: %q, %v", got, err)
	}
}

func TestResolveLiteral(t *testing.T) {
	got, err := ResolveModel("some/vendor-model", map[string]config.Alias{})
	if err != nil || got != "some/vendor-model" {
		t.Fatalf("未知名按字面模型 ID 透传: %q, %v", got, err)
	}
}

func TestResolveEmptyAlias(t *testing.T) {
	aliases := map[string]config.Alias{"bad": {}}
	if _, err := ResolveModel("bad", aliases); err == nil {
		t.Fatal("空 models 列表应报错")
	}
}

func TestModelChain(t *testing.T) {
	aliases := map[string]config.Alias{
		"fast": {Models: []string{"m-primary", "m-backup"}},
	}
	chain, err := ModelChain("fast", aliases)
	if err != nil || len(chain) != 2 || chain[0] != "m-primary" || chain[1] != "m-backup" {
		t.Fatalf("别名应展开为完整链: %v, %v", chain, err)
	}
	chain, err = ModelChain("literal/model-id", aliases)
	if err != nil || len(chain) != 1 || chain[0] != "literal/model-id" {
		t.Fatalf("字面 ID 应为单元素链: %v, %v", chain, err)
	}
}

func TestCompareModels(t *testing.T) {
	aliases := map[string]config.Alias{
		"fast":  {Models: []string{"m-a", "m-b"}},
		"empty": {},
	}
	// 别名=模型组快捷方式：整组展开
	got, err := CompareModels([]string{"fast"}, aliases)
	if err != nil || len(got) != 2 || got[0] != "m-a" || got[1] != "m-b" {
		t.Fatalf("别名应展开为整组: %v, %v", got, err)
	}
	// 逗号分隔 + 混用 + 跨来源去重保序
	got, err = CompareModels([]string{"m-b,fast", "m-c", "m-a"}, aliases)
	if err != nil || len(got) != 3 || got[0] != "m-b" || got[1] != "m-a" || got[2] != "m-c" {
		t.Fatalf("逗号/混用/去重保序不符: %v, %v", got, err)
	}
	// 空值与空白项跳过
	got, err = CompareModels([]string{"m-a,", " ,m-b"}, aliases)
	if err != nil || len(got) != 2 {
		t.Fatalf("空项应跳过: %v, %v", got, err)
	}
	// 空别名报错
	if _, err = CompareModels([]string{"empty"}, aliases); err == nil {
		t.Fatal("空 models 列表应报错")
	}
}

func TestSuggest(t *testing.T) {
	aliases := map[string]config.Alias{
		"fast":   {Models: []string{"m1"}},
		"reason": {Models: []string{"m2"}},
	}
	got := Suggest([]string{"fas"}, aliases, []string{"fast-pro", "unrelated-long-name"})
	if len(got) == 0 || got[0] != "fast" {
		t.Fatalf("前缀命中优先: %v", got)
	}
	found := map[string]bool{}
	for _, s := range got {
		found[s] = true
	}
	if !found["fast-pro"] {
		t.Fatalf("模型前缀命中也应包含: %v", got)
	}
	if found["reason"] || found["unrelated-long-name"] {
		t.Fatalf("无关项不应入选: %v", got)
	}
}

func TestSuggestMultipleSeeds(t *testing.T) {
	aliases := map[string]config.Alias{
		"fast": {Models: []string{"aihub/gpt-x"}},
	}
	// 别名 fast 解析出的模型 404 时，两个种子都应参与提示
	got := Suggest([]string{"fast", "aihub/gpt-x"}, aliases, []string{"aihub/gpt-y", "anti/gpt-x"})
	found := map[string]bool{}
	for _, s := range got {
		found[s] = true
	}
	if !found["aihub/gpt-y"] {
		t.Fatalf("按模型 ID 种子也应给出相近项: %v", got)
	}
}
