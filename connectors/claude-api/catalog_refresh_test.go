// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestL4B2CurrentRows binds the current Claude IDs to their own rows.
// A family prefix must not answer for a newer exact ID.
func TestL4B2CurrentRows(t *testing.T) {
	fable51, ctx, out, ok := pricingFor("claude-fable-5-1")
	if !ok || fable51.InputPerMTokUSD != 10 || fable51.OutputPerMTokUSD != 50 ||
		fable51.CacheWritePerMTokUSD != 12.50 || fable51.CacheWrite1hPerMTokUSD != 20 ||
		fable51.CacheReadPerMTokUSD != 0.25 || fable51.AsOf != "2026-09-25" ||
		ctx != 1_000_000 || out != 128_000 {
		t.Fatalf("claude-fable-5-1 = %+v ctx %d out %d ok %v, want 10/50 cache 12.50/20/0.25 as of 2026-09-25, 1M/128K", fable51, ctx, out, ok)
	}
	fable5, _, _, _ := pricingFor("claude-fable-5")
	if fable5.CacheReadPerMTokUSD != 1 || fable5.AsOf != "2026-09-25" {
		t.Fatalf("claude-fable-5 must keep cache read 1 (dataset stamp 2026-09-25), got %+v", fable5)
	}

	opus55, ctx, out, ok := pricingFor("claude-opus-5-5")
	if !ok || opus55.InputPerMTokUSD != 4 || opus55.OutputPerMTokUSD != 20 ||
		opus55.CacheWritePerMTokUSD != 5 || opus55.CacheWrite1hPerMTokUSD != 8 ||
		opus55.CacheReadPerMTokUSD != 0.20 || opus55.AsOf != "2026-09-25" ||
		ctx != 1_000_000 || out != 128_000 {
		t.Fatalf("claude-opus-5-5 = %+v ctx %d out %d ok %v, want 4/20 cache 5/8/0.20 as of 2026-09-25", opus55, ctx, out, ok)
	}
	if def, levels, ok := DefaultEffortFor("claude-opus-5-5"); !ok || def != "medium" || len(levels) != 5 {
		t.Fatalf("claude-opus-5-5 effort = %q %v ok %v, want medium and five levels", def, levels, ok)
	}
	if n := SurfaceMaxOutputsFor("claude-opus-5-5"); len(n) != 2 {
		t.Fatalf("claude-opus-5-5 batch output beta entries = %d, want 2", len(n))
	} else {
		for _, e := range n {
			if e.MaxOutputTokens != 300_000 || e.Beta != "output-300k-2026-03-24" || e.AsOf != "2026-09-27" {
				t.Fatalf("claude-opus-5-5 output beta = %+v", e)
			}
			if e.Surface != model.GatewayDirect && e.Surface != model.GatewayClaudePlatformAWS {
				t.Fatalf("claude-opus-5-5 output beta surface = %s", e.Surface)
			}
		}
	}

	sonnet, ctx, out, ok := pricingFor("claude-sonnet-5")
	if !ok || sonnet.InputPerMTokUSD != 2 || sonnet.OutputPerMTokUSD != 10 ||
		sonnet.CacheWritePerMTokUSD != 2.50 || sonnet.CacheWrite1hPerMTokUSD != 4 ||
		sonnet.CacheReadPerMTokUSD != 0.20 || sonnet.AsOf != "2026-09-25" ||
		ctx != 1_000_000 || out != 128_000 {
		t.Fatalf("claude-sonnet-5 = %+v ctx %d out %d ok %v, want 2/10 cache 2.50/4/0.20 as of 2026-09-25", sonnet, ctx, out, ok)
	}

	haiku, ctx, out, ok := pricingFor("claude-haiku-4-5-20251001")
	if !ok || haiku.InputPerMTokUSD != 1 || haiku.OutputPerMTokUSD != 5 ||
		haiku.CacheWritePerMTokUSD != 1.25 || haiku.CacheWrite1hPerMTokUSD != 2 ||
		haiku.CacheReadPerMTokUSD != 0.10 || haiku.AsOf != "2026-09-25" ||
		ctx != 200_000 || out != 64_000 {
		t.Fatalf("claude-haiku-4-5-20251001 = %+v ctx %d out %d ok %v", haiku, ctx, out, ok)
	}
	if def, _, ok := DefaultEffortFor("claude-haiku-4-5-20251001"); ok || def != "" {
		t.Fatalf("dated Haiku 4.5 effort = %q ok %v, want none", def, ok)
	}
	alias, _, _, ok := pricingFor("claude-haiku-4-5")
	if !ok || alias.AsOf != "2026-09-25" || alias.InputPerMTokUSD != 1 {
		t.Fatalf("haiku alias = %+v ok %v, want the dataset $1 row", alias, ok)
	}

	for _, id := range []string{"claude-fable-5-1", "claude-opus-5-5", "claude-haiku-4-5-20251001"} {
		if !declaredID(id) {
			t.Fatalf("declared catalog missing %s", id)
		}
	}
}

func TestL4B2PrefixNeighborAndUnknown(t *testing.T) {
	opus48, _, _, ok := pricingFor("claude-opus-4-8")
	if !ok || opus48.InputPerMTokUSD != 5 || opus48.OutputPerMTokUSD != 25 {
		t.Fatalf("claude-opus-4-8 = %+v ok %v", opus48, ok)
	}
	sonnet46, _, _, ok := pricingFor("claude-sonnet-4-6")
	if !ok || sonnet46.InputPerMTokUSD != 3 || sonnet46.OutputPerMTokUSD != 15 {
		t.Fatalf("claude-sonnet-4-6 = %+v ok %v", sonnet46, ok)
	}
	if _, _, _, ok := pricingFor("not-a-claude-model"); ok {
		t.Fatal("unknown id resolved to a family row")
	}
}

func TestL4B2NonDatedNeighborIsUnknown(t *testing.T) {
	if _, _, _, ok := pricingFor("claude-opus-5-5-pro"); ok {
		t.Fatal("claude-opus-5-5-pro resolved to a Claude row")
	}
	if _, _, _, ok := pricingFor("claude-fable-5-1-pro"); ok {
		t.Fatal("claude-fable-5-1-pro resolved to a Claude row")
	}
	p, _, _, ok := pricingFor("claude-opus-5-5-20260927")
	if !ok || p.InputPerMTokUSD != 4 || p.OutputPerMTokUSD != 20 {
		t.Fatalf("claude-opus-5-5-20260927 = %+v ok %v", p, ok)
	}
	fable, _, _, ok := pricingFor("claude-fable-5")
	if !ok || fable.CacheReadPerMTokUSD != 1 {
		t.Fatalf("claude-fable-5 = %+v ok %v", fable, ok)
	}
}

func TestL4B2DeclaredGolden(t *testing.T) {
	const golden = "claude-fable-5\tClaude Fable 5\n" +
		"claude-opus-4-8\tClaude Opus 4.8\n" +
		"claude-sonnet-5\tClaude Sonnet 5\n" +
		"claude-sonnet-4-6\tClaude Sonnet 4.6\n" +
		"claude-haiku-4-5\tClaude Haiku 4.5\n"
	assertDeclaredGolden(t, golden, declaredModelIDs)
}

func TestL4B2SavedDefaults(t *testing.T) {
	want := []string{
		"claude-fable-5",
		"claude-opus-4-8",
		"claude-sonnet-5",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
	}
	if len(declaredModelIDs) < len(want) {
		t.Fatalf("declared models = %d, want at least %d", len(declaredModelIDs), len(want))
	}
	for i, id := range want {
		if declaredModelIDs[i].id != id {
			t.Fatalf("declared model %d = %q, want %q", i, declaredModelIDs[i].id, id)
		}
	}
	def, levels, ok := DefaultEffortFor("claude-opus-4-8")
	if !ok || def != "high" || len(levels) != 5 {
		t.Fatalf("opus 4.8 default effort = %q %v ok %v", def, levels, ok)
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
