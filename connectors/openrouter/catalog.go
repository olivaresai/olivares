// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openrouter

import "github.com/olivaresai/olivares/connectors/modelprovider"

// catalog.go holds the minimal OFFLINE OpenRouter model list returned by Snapshot
// when no credential is configured (live mode replaces it with GET /api/v1/models).
// OpenRouter prices are LIVE-only (per-model, per-token, read from the Models API),
// so declared models carry NO pricing — a Snapshot without a credential is a bare
// id/name list, never a guessed price (ARCHITECTURE.md). Operators get the priced catalog
// by configuring a key.
var declaredModels = []struct{ id, name string }{
	{"anthropic/claude-sonnet-4", "Anthropic: Claude Sonnet 4"},
	{"anthropic/claude-opus-4", "Anthropic: Claude Opus 4"},
	{"openai/gpt-4o", "OpenAI: GPT-4o"},
	{"openai/o4-mini", "OpenAI: o4-mini"},
	{"google/gemini-2.5-pro", "Google: Gemini 2.5 Pro"},
	{"meta-llama/llama-3.3-70b-instruct", "Meta: Llama 3.3 70B Instruct"},
	{"deepseek/deepseek-chat", "DeepSeek: DeepSeek Chat"},
}

// declaredCatalogModels builds the offline OpenRouter model list (no pricing).
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModels))
	for _, d := range declaredModels {
		out = append(out, modelprovider.Model{
			ProviderRef:      modelprovider.ProviderOpenRouter,
			Ref:              d.id,
			DisplayName:      d.name,
			CapabilitySource: "declared",
		})
	}
	return out
}
