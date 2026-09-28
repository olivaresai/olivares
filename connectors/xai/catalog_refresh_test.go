// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package xai

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestL4B2CurrentRows(t *testing.T) {
	f, ok := familyFor("grok-4.7")
	if !ok || f.pricing.InputPerMTokUSD != 2 || f.pricing.OutputPerMTokUSD != 6 ||
		f.pricing.CacheReadPerMTokUSD != 0.50 || f.pricing.CacheWritePerMTokUSD != 0 ||
		f.pricing.AsOf != "2026-09-27" || f.context != 500_000 {
		t.Fatalf("grok-4.7 family = %+v ok %v", f, ok)
	}
	m := buildDeclaredModel("grok-4.7", "Grok 4.7")
	if m.Pricing == nil || m.ContextWindow != 500_000 || m.MaxOutputTokens != 0 || m.DefaultEffort != "high" {
		t.Fatalf("grok-4.7 model = %+v", m)
	}
	if !m.HasCapability(modelprovider.CapVision) || !m.HasCapability(modelprovider.CapToolUse) ||
		!m.HasCapability(modelprovider.CapExtendedThinking) || m.HasCapability(modelprovider.CapStructuredOutputs) {
		t.Fatalf("grok-4.7 capabilities = %v", m.Capabilities)
	}
	if !declaredID("grok-4.7") {
		t.Fatal("declared catalog missing grok-4.7")
	}
	if _, ok := familyFor("grok-4.7-fast"); ok {
		t.Fatal("grok-4.7-fast is not a public API id and must not inherit grok-4.7")
	}
}

func TestL4B2PrefixNeighborAndUnknown(t *testing.T) {
	f, ok := familyFor("grok-4.3")
	if !ok || f.pricing.InputPerMTokUSD != 1.25 || f.pricing.OutputPerMTokUSD != 2.50 ||
		f.pricing.CacheReadPerMTokUSD != 0.20 || f.context != 1_000_000 {
		t.Fatalf("grok-4.3 = %+v ok %v", f, ok)
	}
	reason, ok := familyFor("grok-4.20-0309-reasoning")
	if !ok || !hasCap(reason.capabilities, modelprovider.CapExtendedThinking) {
		t.Fatalf("grok-4.20 reasoning = %+v ok %v", reason, ok)
	}
	if _, ok := familyFor("grok-9"); ok {
		t.Fatal("unknown id resolved to a family row")
	}
}

func TestL4B2DeclaredGolden(t *testing.T) {
	const golden = "grok-4.3\tGrok 4.3\n" +
		"grok-4.20-0309-reasoning\tGrok 4.20 (reasoning)\n" +
		"grok-4.20-0309-non-reasoning\tGrok 4.20 (non-reasoning)\n" +
		"grok-4.20-multi-agent-0309\tGrok 4.20 (multi-agent)\n" +
		"grok-build-0.1\tGrok Build 0.1\n"
	assertDeclaredGolden(t, golden, declaredModelIDs)
}

func TestL4B2SavedDefaults(t *testing.T) {
	want := []string{
		"grok-4.3",
		"grok-4.20-0309-reasoning",
		"grok-4.20-0309-non-reasoning",
		"grok-4.20-multi-agent-0309",
		"grok-build-0.1",
	}
	if len(declaredModelIDs) < len(want) {
		t.Fatalf("declared models = %d, want at least %d", len(declaredModelIDs), len(want))
	}
	for i, id := range want {
		if declaredModelIDs[i].id != id {
			t.Fatalf("declared model %d = %q, want %q", i, declaredModelIDs[i].id, id)
		}
	}
	m := buildDeclaredModel("grok-4.3", "Grok 4.3")
	if m.DefaultEffort != "" || m.ContextWindow != 1_000_000 {
		t.Fatalf("grok-4.3 defaults changed: %+v", m)
	}
}

func assertDeclaredGolden(t *testing.T, golden string, rows []struct{ id, displayName string }) {
	t.Helper()
	var got string
	for _, d := range rows {
		got += d.id + "\t" + d.displayName + "\n"
	}
	if len(got) < len(golden) || got[:len(golden)] != golden {
		t.Fatalf("base declared list is not the golden prefix:\n%s", got)
	}
}

func declaredID(id string) bool {
	for _, d := range declaredModelIDs {
		if d.id == id {
			return true
		}
	}
	return false
}
