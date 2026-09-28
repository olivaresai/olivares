// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestL4B2CurrentRows(t *testing.T) {
	cases := []struct {
		id      string
		in, out float64
		write   float64
		read    float64
	}{
		{"gpt-6-astra", 10, 50, 12.5, 1},
		{"gpt-6-sol", 2, 10, 2.5, 0.2},
		{"gpt-6-luna", 0.10, 0.50, 0.125, 0.01},
	}
	for _, c := range cases {
		p, caps, ctx, out, ok := pricingFor(c.id)
		if !ok || p.InputPerMTokUSD != c.in || p.OutputPerMTokUSD != c.out ||
			p.CacheWritePerMTokUSD != c.write || p.CacheReadPerMTokUSD != c.read ||
			p.CacheWrite1hPerMTokUSD != 0 || p.AsOf != "2026-09-27" ||
			ctx != 1_050_000 || out != 128_000 {
			t.Fatalf("%s = %+v ctx %d out %d ok %v", c.id, p, ctx, out, ok)
		}
		if !modelprovider.Has(caps, modelprovider.CapToolUse) ||
			!modelprovider.Has(caps, modelprovider.CapVision) ||
			!modelprovider.Has(caps, modelprovider.CapStructuredOutputs) ||
			modelprovider.Has(caps, modelprovider.CapFiles) {
			t.Fatalf("%s capabilities = %v", c.id, caps)
		}
		m := buildModel(modelprovider.ProviderOpenAI, c.id, c.id)
		if m.Pricing == nil || m.Pricing.InputPerMTokUSD != c.in || m.Deprecated {
			t.Fatalf("%s model = %+v", c.id, m)
		}
		if !declaredID(c.id) {
			t.Fatalf("declared catalog missing %s", c.id)
		}
	}
}

func TestL4B2PrefixNeighborAndUnknown(t *testing.T) {
	p, _, ctx, out, ok := pricingFor("gpt-5.6-sol")
	if !ok || p.InputPerMTokUSD != 5 || p.OutputPerMTokUSD != 30 ||
		p.CacheWritePerMTokUSD != 6.25 || p.CacheReadPerMTokUSD != 0.50 ||
		p.AsOf != "2026-07-15" || ctx != 1_050_000 || out != 128_000 {
		t.Fatalf("gpt-5.6-sol = %+v ctx %d out %d ok %v", p, ctx, out, ok)
	}
	terra, _, _, _, ok := pricingFor("gpt-5.6-terra")
	if !ok || terra.InputPerMTokUSD != 2.50 || terra.OutputPerMTokUSD != 15 {
		t.Fatalf("gpt-5.6-terra = %+v ok %v", terra, ok)
	}
	gpt55 := buildModel(modelprovider.ProviderOpenAI, "gpt-5.5", "GPT-5.5")
	if gpt55.Deprecated || len(gpt55.Retirements) != 0 || gpt55.Pricing == nil || gpt55.Pricing.InputPerMTokUSD != 5 {
		t.Fatalf("API gpt-5.5 must stay priced and not retired: %+v", gpt55)
	}
	if _, _, _, _, ok := pricingFor("gpt-6-nope"); ok {
		t.Fatal("unknown id resolved to a family row")
	}
	if _, _, _, _, ok := pricingFor("not-a-model"); ok {
		t.Fatal("unknown id resolved to a family row")
	}
}

func TestL4B2NonDatedNeighborIsUnknown(t *testing.T) {
	if _, _, _, _, ok := pricingFor("gpt-6-sol-pro"); ok {
		t.Fatal("gpt-6-sol-pro resolved to a GPT-6 row")
	}
	p, _, _, _, ok := pricingFor("gpt-6-sol-20260927")
	if !ok || p.InputPerMTokUSD != 2 || p.OutputPerMTokUSD != 10 {
		t.Fatalf("gpt-6-sol-20260927 = %+v ok %v", p, ok)
	}
	p, _, _, _, ok = pricingFor("gpt-6-sol-2026-09-27")
	if !ok || p.InputPerMTokUSD != 2 {
		t.Fatalf("gpt-6-sol-2026-09-27 = %+v ok %v", p, ok)
	}
	astra, _, _, _, ok := pricingFor("gpt-6-astra")
	if !ok || astra.InputPerMTokUSD != 10 {
		t.Fatalf("exact gpt-6-astra = %+v ok %v", astra, ok)
	}
}

func TestL4B2DeclaredGolden(t *testing.T) {
	const golden = "gpt-5.5\tGPT-5.5\n" +
		"gpt-4o\tGPT-4o\n" +
		"gpt-4o-mini\tGPT-4o mini\n" +
		"gpt-4.1\tGPT-4.1\n"
	assertDeclaredGolden(t, golden, declaredModelIDs)
}

func TestL4B2SavedDefaults(t *testing.T) {
	want := []string{"gpt-5.5", "gpt-4o", "gpt-4o-mini", "gpt-4.1"}
	if len(declaredModelIDs) < len(want) {
		t.Fatalf("declared models = %d, want at least %d", len(declaredModelIDs), len(want))
	}
	for i, id := range want {
		if declaredModelIDs[i].id != id {
			t.Fatalf("declared model %d = %q, want %q", i, declaredModelIDs[i].id, id)
		}
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
