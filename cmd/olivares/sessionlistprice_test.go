// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// The pricer the sessions module is wired with prices a Codex model the embedded
// table carries, under the provider Codex names, and nothing it does not carry.
func TestSessionListPricePricesWhatTheTableCarries(t *testing.T) {
	// gpt-6-astra: $10 input, $1 cache read, $12.50 cache write, $50 output per
	// million tokens (genai-prices v0.1.9), so each field lands on its own rate.
	turn := sessions.TurnTokens{UncachedInput: 15007, CacheRead: 7168, CacheWrite: 1000, Output: 142}
	if cost, ok := sessionListPrice("openai", "gpt-6-astra", turn); !ok || cost != 15007*10+7168*1+12500+142*50 {
		t.Fatalf("gpt-6-astra = %d, %v", cost, ok)
	}
	if cost, ok := sessionListPrice("olivares-local", "a-local-model", turn); ok || cost != 0 {
		t.Fatalf("an unknown model was priced: %d, %v", cost, ok)
	}
}
