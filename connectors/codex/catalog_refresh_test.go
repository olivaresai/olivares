// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
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
		p, _, ok := pricingFor(c.id)
		if !ok || p.InputPerMTokUSD != c.in || p.OutputPerMTokUSD != c.out ||
			p.CacheWritePerMTokUSD != c.write || p.CacheReadPerMTokUSD != c.read ||
			p.AsOf != "2026-09-27" {
			t.Fatalf("%s = %+v ok %v", c.id, p, ok)
		}
		if !declaredID(c.id) {
			t.Fatalf("declared catalog missing %s", c.id)
		}
	}
}

func TestL4B2ChatGPTCodexRetirement(t *testing.T) {
	m := buildModel("gpt-5.5", "GPT-5.5")
	if m.Deprecated || len(m.Retirements) != 1 || m.Retirements[0].RetiresOn != "2026-10-14" ||
		m.Retirements[0].Surface != model.Gateway("chatgpt-authenticated-codex") ||
		m.Retirements[0].ReplacementRef != "" {
		t.Fatalf("codex gpt-5.5 row = deprecated %v retirements %+v", m.Deprecated, m.Retirements)
	}
	current := buildModel("gpt-5.6-sol", "GPT-5.6 Sol")
	if current.Deprecated || len(current.Retirements) != 0 {
		t.Fatalf("gpt-5.6-sol retirement changed: %+v", current.Retirements)
	}
}

func TestL4B2PrefixNeighborAndUnknown(t *testing.T) {
	p, _, ok := pricingFor("gpt-5.6-sol")
	if !ok || p.InputPerMTokUSD != 5 || p.OutputPerMTokUSD != 30 || p.AsOf != "2026-07-15" {
		t.Fatalf("gpt-5.6-sol = %+v ok %v", p, ok)
	}
	terra, _, ok := pricingFor("gpt-5.6-terra")
	if !ok || terra.InputPerMTokUSD != 2.50 || terra.OutputPerMTokUSD != 15 {
		t.Fatalf("gpt-5.6-terra = %+v ok %v", terra, ok)
	}
	if _, _, ok := pricingFor("gpt-6-nope"); ok {
		t.Fatal("unknown id resolved to a family row")
	}
}

func TestL4B2NonDatedNeighborIsUnknown(t *testing.T) {
	if _, _, ok := pricingFor("gpt-6-sol-pro"); ok {
		t.Fatal("gpt-6-sol-pro resolved to a GPT-6 row")
	}
	p, _, ok := pricingFor("gpt-6-sol-20260927")
	if !ok || p.InputPerMTokUSD != 2 {
		t.Fatalf("gpt-6-sol-20260927 = %+v ok %v", p, ok)
	}
}

func TestL4B2DeclaredGolden(t *testing.T) {
	const golden = "gpt-5.6-sol\tGPT-5.6 Sol\n" +
		"gpt-5.6-terra\tGPT-5.6 Terra\n" +
		"gpt-5.6-luna\tGPT-5.6 Luna\n" +
		"gpt-5-codex\tGPT-5 Codex\n" +
		"gpt-5-codex-mini\tGPT-5 Codex mini\n" +
		"codex-mini-latest\tCodex mini (latest)\n"
	assertDeclaredGolden(t, golden, declaredModelIDs)
}

func TestL4B2SavedDefaults(t *testing.T) {
	want := []string{
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5-codex",
		"gpt-5-codex-mini",
		"codex-mini-latest",
	}
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
