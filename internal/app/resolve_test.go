package app

import (
	"testing"

	"github.com/icheer/brain-cli/internal/config"
)

func TestResolveAlias(t *testing.T) {
	mf := config.ModelsFile{Aliases: map[string]config.Alias{
		"fast": {Models: []string{"m-primary", "m-backup"}},
	}}
	got, err := ResolveModel("fast", mf)
	if err != nil || got != "m-primary" {
		t.Fatalf("别名应取链首位: %q, %v", got, err)
	}
}

func TestResolveLiteral(t *testing.T) {
	got, err := ResolveModel("some/vendor-model", config.ModelsFile{})
	if err != nil || got != "some/vendor-model" {
		t.Fatalf("未知名按字面模型 ID 透传: %q, %v", got, err)
	}
}

func TestResolveEmptyAlias(t *testing.T) {
	mf := config.ModelsFile{Aliases: map[string]config.Alias{"bad": {}}}
	if _, err := ResolveModel("bad", mf); err == nil {
		t.Fatal("空 models 列表应报错")
	}
}

func TestSuggest(t *testing.T) {
	mf := config.ModelsFile{Aliases: map[string]config.Alias{
		"fast":   {Models: []string{"m1"}},
		"reason": {Models: []string{"m2"}},
	}}
	got := Suggest([]string{"fas"}, mf, []string{"fast-pro", "unrelated-long-name"})
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
	mf := config.ModelsFile{Aliases: map[string]config.Alias{
		"fast": {Models: []string{"aihub/gpt-x"}},
	}}
	// 别名 fast 解析出的模型 404 时，两个种子都应参与提示
	got := Suggest([]string{"fast", "aihub/gpt-x"}, mf, []string{"aihub/gpt-y", "anti/gpt-x"})
	found := map[string]bool{}
	for _, s := range got {
		found[s] = true
	}
	if !found["aihub/gpt-y"] {
		t.Fatalf("按模型 ID 种子也应给出相近项: %v", got)
	}
}
