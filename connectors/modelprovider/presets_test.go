// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestPresetTableRowsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets() {
		if p.Ref == "" || p.Title == "" || p.DocsURL == "" {
			t.Errorf("row %+v lacks ref, title or docs_url", p)
		}
		if seen[p.Ref] {
			t.Errorf("ref %q appears twice", p.Ref)
		}
		seen[p.Ref] = true
		if p.Kind != KindAnthropic && p.Kind != KindOpenAICompatible {
			t.Errorf("%s: kind %q is neither anthropic nor openai_compatible", p.Ref, p.Kind)
		}
		if p.Auth != AuthBearer && p.Auth != AuthAnthropicKey {
			t.Errorf("%s: auth %q is neither bearer nor x-api-key", p.Ref, p.Auth)
		}
		for _, raw := range []string{p.BaseURL, p.ModelsURL, p.KeyCheckURL, p.DocsURL, p.KeyURL} {
			if raw == "" {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" {
				t.Errorf("%s: %q is not a plain https URL", p.Ref, raw)
			}
		}
		if p.BaseURL == "" {
			t.Errorf("%s: base_url is empty", p.Ref)
		}
		if strings.HasSuffix(p.BaseURL, "/") {
			t.Errorf("%s: base_url %q ends in a slash", p.Ref, p.BaseURL)
		}
	}
}

func TestPresetLookupAndRefsReadOneTable(t *testing.T) {
	refs := PresetRefs()
	if len(refs) != len(Presets()) || !slices.IsSorted(refs) {
		t.Fatalf("refs %v are not the sorted refs of the table", refs)
	}
	for _, ref := range refs {
		p, ok := LookupPreset(ref)
		if !ok || p.Ref != ref {
			t.Fatalf("lookup %q = %+v, %v", ref, p, ok)
		}
	}
	if _, ok := LookupPreset("not-a-vendor"); ok {
		t.Fatal("an unknown ref resolved")
	}
	// A caller that edits the returned slice must not edit the table.
	rows := Presets()
	rows[0].BaseURL = "https://attacker.example"
	if Presets()[0].BaseURL == "https://attacker.example" {
		t.Fatal("Presets returns the table itself")
	}
}

func TestDeepSeekRowsKeepTheDocumentedEndpoints(t *testing.T) {
	governed, ok := LookupPreset(ServiceDeepSeek)
	if !ok || governed.Kind != KindOpenAICompatible || governed.BaseURL != "https://api.deepseek.com" || governed.Auth != AuthBearer ||
		governed.ModelsURL != "" || DeepSeekBaseURL != "https://api.deepseek.com" || DeepSeekModelsURL != "https://api.deepseek.com/models" ||
		DeepSeekChatURL != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("governed deepseek row or constants changed: %+v", governed)
	}
}

// Only the governed deepseek service is closed to tool sessions; an unknown ref is
// closed too, so a row that is later renamed cannot turn a stored record native.
func TestNativeServiceFailsClosed(t *testing.T) {
	if !NativeService("") {
		t.Fatal("an untagged record serves tool sessions")
	}
	if NativeService(ServiceDeepSeek) || NativeService("not-a-vendor") {
		t.Fatal("the governed service and an unknown ref are closed to tool sessions")
	}
	// LookupPreset trims its ref, so the governed service must stay closed however it is spelled.
	if NativeService(" deepseek") || NativeService("deepseek\n") {
		t.Fatal("a padded deepseek ref found its row and counted as native")
	}
	for _, ref := range PresetRefs() {
		if ref != ServiceDeepSeek && !NativeService(ref) {
			t.Errorf("%s is closed to tool sessions: only deepseek is", ref)
		}
	}
}

// The rows are pinned by literal, not read back from the table, so a typo in a
// vendor URL or header fails here. Each value is the vendor's own documented setup
// (the row's docs_url), and the model lists were checked to answer 401 or 200 at
// these paths on 2026-10-26.
func TestAnthropicRowsAreThePinnedVendorSetups(t *testing.T) {
	for _, want := range []Preset{
		{Ref: "deepseek-anthropic", Kind: KindAnthropic, BaseURL: "https://api.deepseek.com/anthropic", ModelsURL: "https://api.deepseek.com/models", Auth: AuthBearer, Models: PresetModels{Default: "deepseek-flash", Opus: "deepseek-flash[1m]", Sonnet: "deepseek-flash[1m]", Haiku: "deepseek-flash", Fable: "deepseek-flash", Subagent: "deepseek-flash"}},
		{Ref: "zai-anthropic", Kind: KindAnthropic, BaseURL: "https://api.z.ai/api/anthropic", Auth: AuthBearer, Models: PresetModels{Default: "glm-5.3", Opus: "glm-5.3[1m]", Sonnet: "glm-5.3[1m]", Haiku: "glm-5.3-flash[1m]", Fable: "glm-5.3-flash[1m]", Subagent: "glm-5.3-flash[1m]"}},
		{Ref: "kimi-code", Kind: KindAnthropic, BaseURL: "https://api.kimi.ai/coding", Auth: AuthAnthropicKey},
		{Ref: "moonshot-anthropic", Kind: KindAnthropic, BaseURL: "https://api.moonshot.ai/anthropic", ModelsURL: "https://api.moonshot.ai/v1/models", Auth: AuthBearer},
		{Ref: "openrouter-anthropic", Kind: KindAnthropic, BaseURL: "https://openrouter.ai/api", ModelsURL: "https://openrouter.ai/api/v1/models", Auth: AuthBearer, KeyCheckURL: "https://openrouter.ai/api/v1/key"},
		{Ref: "opencode-zen", Kind: KindAnthropic, BaseURL: "https://opencode.ai/inference/anthropic", ModelsURL: "https://opencode.ai/inference/v1/models", Auth: AuthBearer, Models: PresetModels{Default: "claude-sonnet-5-5"}},
		{Ref: "opencode-go", Kind: KindAnthropic, BaseURL: "https://opencode.ai/inference/go/anthropic", ModelsURL: "https://opencode.ai/inference/v1/models", Auth: AuthBearer, Models: PresetModels{Default: "claude-haiku-5-5"}},
		{Ref: "zai", Kind: KindOpenAICompatible, BaseURL: "https://api.z.ai/api/paas/v4", Auth: AuthBearer},
		{Ref: "moonshot", Kind: KindOpenAICompatible, BaseURL: "https://api.moonshot.ai/v1", Auth: AuthBearer},
		{Ref: "openrouter", Kind: KindOpenAICompatible, BaseURL: "https://openrouter.ai/api/v1", Auth: AuthBearer, KeyCheckURL: "https://openrouter.ai/api/v1/key"},
		{Ref: "mistral", Kind: KindOpenAICompatible, BaseURL: "https://api.mistral.ai/v1", Auth: AuthBearer},
	} {
		got, ok := LookupPreset(want.Ref)
		if !ok || got.Kind != want.Kind || got.BaseURL != want.BaseURL || got.ModelsURL != want.ModelsURL || got.Auth != want.Auth || got.Models != want.Models || got.KeyCheckURL != want.KeyCheckURL {
			t.Errorf("%s = %+v, want %+v", want.Ref, got, want)
		}
	}
}

// The key test sends the credential to models_url, so it must stay on the host the
// session is held to: a row edited to name another host would send the key there.
func TestModelsURLStaysOnTheBaseHost(t *testing.T) {
	for _, p := range Presets() {
		for _, endpoint := range []string{p.ModelsURL, p.KeyCheckURL} {
			if endpoint == "" {
				continue
			}
			base, _ := url.Parse(p.BaseURL)
			other, _ := url.Parse(endpoint)
			if base.Host != other.Host {
				t.Errorf("%s: probe host %s differs from base host %s", p.Ref, other.Host, base.Host)
			}
		}
	}
}

func TestEveryDocumentedVendorHasARow(t *testing.T) {
	for _, ref := range []string{
		"deepseek", "deepseek-anthropic", "zai", "zai-anthropic", "kimi-code",
		"moonshot", "moonshot-anthropic", "openrouter", "openrouter-anthropic", "mistral", "opencode-zen", "opencode-go",
	} {
		if _, ok := LookupPreset(ref); !ok {
			t.Errorf("no row for %s", ref)
		}
	}
}
