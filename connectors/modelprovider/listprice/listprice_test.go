// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package listprice

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// A Codex turn is priced from the embedded dataset under the provider Codex names
// ("openai"), and a model or provider the dataset does not carry has no price
// rather than a zero one.
func TestCostMicroUSDPricesWhatTheDatasetCarries(t *testing.T) {
	// genai-prices v0.1.9 lists gpt-6-astra at $10 input, $1 cache read, $12.50
	// cache write and $50 output per million tokens (internal/data/prices.json), and
	// a dollar per million tokens is a micro-dollar per token.
	u := modelprovider.Usage{InputTokens: 15007, CacheReadTokens: 7168, CacheWriteTokens: 1000, OutputTokens: 142}
	cost, ok := CostMicroUSD(modelprovider.ProviderOpenAI, "gpt-6-astra", u)
	if want := int64(15007*10 + 7168*1 + 1000*12.5 + 142*50); !ok || cost != want {
		t.Fatalf("gpt-6-astra = %d, %v; want %d", cost, ok, want)
	}
	for _, tc := range [][2]string{
		{modelprovider.ProviderOpenAI, "no-such-model"},
		{"olivares-local", "gpt-6-astra"},
		// The names a key-bound Codex gives an OpenAI-compatible or local server:
		// never billed at OpenAI's price.
		{"olivares_record", "gpt-6-astra"},
		{"olivares_ollama", "gpt-6-astra"},
		{"", "gpt-6-astra"},
		{modelprovider.ProviderOpenAI, ""},
	} {
		if cost, ok := CostMicroUSD(tc[0], tc[1], u); ok || cost != 0 {
			t.Errorf("%s/%s = %d, %v; want no price", tc[0], tc[1], cost, ok)
		}
	}
}
