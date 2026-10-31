// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"slices"
	"strings"
)

// The kinds a preset row can take. They are the values of the engine's provider
// record kinds of the same name; a test in the sessions module holds them equal.
const (
	KindAnthropic        = "anthropic"
	KindOpenAICompatible = "openai_compatible"
)

// ServiceDeepSeek is the one governed service: its record serves governed
// inference only and no native tool session (ResolveProviderCredential).
const (
	ServiceDeepSeek   = "deepseek"
	DeepSeekBaseURL   = "https://api.deepseek.com"
	DeepSeekModelsURL = DeepSeekBaseURL + "/models"
	DeepSeekChatURL   = DeepSeekBaseURL + "/chat/completions"
)

// Preset is one vendor endpoint: where it is, which protocol it speaks and how it
// wants the key (Auth: AuthBearer, or AuthAnthropicKey for the x-api-key header).
// It is the one source the engine, the API and the CLI read; the record's
// immutable service column holds a row's Ref. Auth decides both the key-test
// header and the credential variable injected into a Claude Code session.
type Preset struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	// Kind is the record kind the row registers as: anthropic (what Claude Code
	// speaks) or openai_compatible.
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	// ModelsURL is the model list when the base has none of its own: DeepSeek's
	// Anthropic base answers 404 to /v1/models and lists its models on the OpenAI
	// base. Empty means the kind's own path under BaseURL.
	ModelsURL string       `json:"models_url,omitempty"`
	Auth      AuthScheme   `json:"auth"`
	Models    PresetModels `json:"models"`
	DocsURL   string       `json:"docs_url"`
	KeyURL    string       `json:"key_url,omitempty"`
	// KeyCheckURL verifies the key when model discovery is public. It is probe
	// policy, not a user-configurable credential endpoint.
	KeyCheckURL string `json:"-"`
}

// PresetModels is what a row says about the vendor's model names.
type PresetModels struct {
	// Default is the model a new record of this row prefers: the first successful
	// key test saves it as the record's default when the vendor lists it. Only a
	// name the vendor's own Claude Code page sets is written.
	Default string `json:"default,omitempty"`
	// Claude role mappings are internal launch policy, not provider discovery.
	Opus     string `json:"-"`
	Sonnet   string `json:"-"`
	Haiku    string `json:"-"`
	Fable    string `json:"-"`
	Subagent string `json:"-"`
}

// presets is the closed table, one row per vendor endpoint. Each row is the
// vendor's own documented setup, read from the page named in DocsURL. Auth is what
// that page sets (ANTHROPIC_AUTH_TOKEN is bearer, ANTHROPIC_API_KEY x-api-key);
// where it names none, the value is the Anthropic API's own (x-api-key).
//
// Role mappings use the vendor setup; Fable and subagents use its small role
// unless the vendor documents a separate choice.
var presets = []Preset{
	{
		Ref: ServiceDeepSeek, Title: "DeepSeek", Kind: KindOpenAICompatible, BaseURL: DeepSeekBaseURL, Auth: AuthBearer,
		DocsURL: "https://api-docs.deepseek.com/",
		KeyURL:  "https://platform.deepseek.com/api_keys",
	},
	{
		Ref: "deepseek-anthropic", Title: "DeepSeek (Claude Code)", Kind: KindAnthropic, BaseURL: DeepSeekBaseURL + "/anthropic",
		ModelsURL: DeepSeekModelsURL, Auth: AuthBearer, Models: PresetModels{Default: "deepseek-flash", Opus: "deepseek-flash[1m]", Sonnet: "deepseek-flash[1m]", Haiku: "deepseek-flash", Fable: "deepseek-flash", Subagent: "deepseek-flash"},
		DocsURL: "https://api-docs.deepseek.com/quick_start/agent_integrations/claude_code",
		KeyURL:  "https://platform.deepseek.com/api_keys",
	},
	{
		Ref: "zai", Title: "Z.ai", Kind: KindOpenAICompatible, BaseURL: "https://api.z.ai/api/paas/v4", Auth: AuthBearer,
		DocsURL: "https://docs.z.ai/",
		KeyURL:  "https://z.ai/manage-apikey/apikey-list",
	},
	{
		Ref: "zai-anthropic", Title: "Z.ai (Claude Code)", Kind: KindAnthropic, BaseURL: "https://api.z.ai/api/anthropic",
		Auth: AuthBearer, Models: PresetModels{Default: "glm-5.3", Opus: "glm-5.3[1m]", Sonnet: "glm-5.3[1m]", Haiku: "glm-5.3-flash[1m]", Fable: "glm-5.3-flash[1m]", Subagent: "glm-5.3-flash[1m]"},
		DocsURL: "https://docs.z.ai/devpack/tool/claude",
		KeyURL:  "https://z.ai/manage-apikey/apikey-list",
	},
	{
		Ref: "kimi-code", Title: "Kimi Code", Kind: KindAnthropic, BaseURL: "https://api.kimi.ai/coding", Auth: AuthAnthropicKey,
		DocsURL: "https://www.kimi.com/code/docs/en/",
	},
	{
		Ref: "moonshot", Title: "Moonshot", Kind: KindOpenAICompatible, BaseURL: "https://api.moonshot.ai/v1", Auth: AuthBearer,
		DocsURL: "https://platform.kimi.ai/docs/guide/agent-support",
	},
	{
		Ref: "moonshot-anthropic", Title: "Moonshot (Claude Code)", Kind: KindAnthropic, BaseURL: "https://api.moonshot.ai/anthropic",
		ModelsURL: "https://api.moonshot.ai/v1/models", Auth: AuthBearer,
		DocsURL: "https://platform.kimi.ai/docs/guide/claude-code-kimi",
	},
	// OpenRouter lists models publicly, including when a wrong key is supplied.
	// Its key endpoint must accept the credential before discovery can report success.
	{
		Ref: "openrouter", Title: "OpenRouter", Kind: KindOpenAICompatible, BaseURL: "https://openrouter.ai/api/v1", Auth: AuthBearer, KeyCheckURL: "https://openrouter.ai/api/v1/key",
		DocsURL: "https://openrouter.ai/docs/quickstart",
		KeyURL:  "https://openrouter.ai/settings/keys",
	},
	{
		Ref: "openrouter-anthropic", Title: "OpenRouter (Claude Code)", Kind: KindAnthropic, BaseURL: "https://openrouter.ai/api",
		ModelsURL: "https://openrouter.ai/api/v1/models", Auth: AuthBearer, KeyCheckURL: "https://openrouter.ai/api/v1/key",
		DocsURL: "https://openrouter.ai/docs/guides/guides/claude-code-integration",
		KeyURL:  "https://openrouter.ai/settings/keys",
	},
	{
		Ref: "mistral", Title: "Mistral", Kind: KindOpenAICompatible, BaseURL: "https://api.mistral.ai/v1", Auth: AuthBearer,
		DocsURL: "https://docs.mistral.ai/",
		KeyURL:  "https://console.mistral.ai/api-keys",
	},
	{
		Ref: "opencode-zen", Title: "OpenCode Zen (Claude Code)", Kind: KindAnthropic, BaseURL: "https://opencode.ai/inference/anthropic",
		ModelsURL: "https://opencode.ai/inference/v1/models", Auth: AuthBearer, Models: PresetModels{Default: "claude-sonnet-5-5"},
		DocsURL: "https://opencode.ai/v2/docs/console/harnesses/claude-code/",
	},
	{
		Ref: "opencode-go", Title: "OpenCode Go (Claude Code)", Kind: KindAnthropic, BaseURL: "https://opencode.ai/inference/go/anthropic",
		ModelsURL: "https://opencode.ai/inference/v1/models", Auth: AuthBearer, Models: PresetModels{Default: "claude-haiku-5-5"},
		DocsURL: "https://opencode.ai/v2/docs/console/harnesses/claude-code/",
	},
}

// Presets returns the table, one row per vendor endpoint, in the order shown.
func Presets() []Preset { return slices.Clone(presets) }

// PresetRefs returns every ref, sorted.
func PresetRefs() []string {
	refs := make([]string, 0, len(presets))
	for _, p := range presets {
		refs = append(refs, p.Ref)
	}
	slices.Sort(refs)
	return refs
}

// LookupPreset returns the row a ref names.
func LookupPreset(ref string) (Preset, bool) {
	ref = strings.TrimSpace(ref)
	i := slices.IndexFunc(presets, func(p Preset) bool { return p.Ref == ref })
	if i < 0 {
		return Preset{}, false
	}
	return presets[i], true
}

// NativeService reports whether a record with this service may serve a tool
// session: no service (an ordinary record), or a row that is not governed. The
// governed deepseek service, and a ref the table does not know, fail closed.
func NativeService(ref string) bool {
	if ref == "" {
		return true
	}
	row, ok := LookupPreset(ref)
	return ok && row.Ref != ServiceDeepSeek
}
