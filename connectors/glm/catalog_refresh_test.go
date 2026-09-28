// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package glm

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestL4B2CurrentRows(t *testing.T) {
	text := buildDeclaredModel("glm-5.3", "GLM-5.3")
	if text.Pricing == nil || text.Pricing.InputPerMTokUSD != 1.40 || text.Pricing.OutputPerMTokUSD != 4.40 ||
		text.Pricing.CacheReadPerMTokUSD != 0.26 || text.Pricing.AsOf != "2026-09-27" ||
		text.ContextWindow != 1_000_000 || text.MaxOutputTokens != 128_000 || text.DefaultEffort != "max" {
		t.Fatalf("glm-5.3 = %+v", text)
	}
	if text.HasCapability(modelprovider.CapVision) || !text.HasCapability(modelprovider.CapToolUse) ||
		!text.HasCapability(modelprovider.CapExtendedThinking) || !text.HasCapability(modelprovider.CapStructuredOutputs) {
		t.Fatalf("glm-5.3 capabilities = %v", text.Capabilities)
	}

	flash := buildDeclaredModel("glm-5.3-flash", "GLM-5.3-Flash")
	if flash.Pricing == nil || flash.Pricing.InputPerMTokUSD != 0.15 || flash.Pricing.OutputPerMTokUSD != 0.50 ||
		flash.Pricing.CacheReadPerMTokUSD != 0.03 || flash.Pricing.AsOf != "2026-09-27" ||
		flash.ContextWindow != 1_000_000 || flash.MaxOutputTokens != 128_000 ||
		!flash.HasCapability(modelprovider.CapVision) {
		t.Fatalf("glm-5.3-flash = %+v caps %v", flash, flash.Capabilities)
	}

	flashx := buildDeclaredModel("glm-5.3-flashx", "GLM-5.3-FlashX")
	if flashx.Pricing == nil || flashx.Pricing.InputPerMTokUSD != 0.37 || flashx.Pricing.OutputPerMTokUSD != 1.25 ||
		flashx.Pricing.CacheReadPerMTokUSD != 0.075 || flashx.Pricing.AsOf != "2026-09-27" ||
		!flashx.HasCapability(modelprovider.CapVision) {
		t.Fatalf("glm-5.3-flashx = %+v", flashx)
	}
	for _, id := range []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-flashx"} {
		if !declaredID(id) {
			t.Fatalf("declared catalog missing %s", id)
		}
	}
}

// TestL4B2P03Negative is the prefix failure: Flash must not inherit the glm-5 text row.
func TestL4B2P03Negative(t *testing.T) {
	flash, ok := declaredFamilyFor("glm-5.3-flash")
	base, bok := declaredFamilyFor("glm-5")
	if !ok || !bok {
		t.Fatalf("flash ok %v glm-5 ok %v", ok, bok)
	}
	if flash.prefix == base.prefix || flash.pricing == nil || flash.pricing.InputPerMTokUSD == 1.00 ||
		!hasCap(flash.capabilities, modelprovider.CapVision) || hasCap(base.capabilities, modelprovider.CapVision) {
		t.Fatalf("glm-5.3-flash resolved as %+v; glm-5 is %+v", flash, base)
	}
}

func TestL4B2PrefixNeighborAndUnknown(t *testing.T) {
	f, ok := familyFor("glm-5.2")
	if !ok || f.pricing.InputPerMTokUSD != 1.40 || f.pricing.OutputPerMTokUSD != 4.40 {
		t.Fatalf("glm-5.2 = %+v ok %v", f, ok)
	}
	dated, ok := familyFor("glm-5.2-20260708")
	if !ok || dated.pricing.InputPerMTokUSD != 1.40 {
		t.Fatalf("glm-5.2 dated neighbor = %+v ok %v", dated, ok)
	}
	base, ok := familyFor("glm-5")
	if !ok || base.pricing.InputPerMTokUSD != 1.00 || base.pricing.OutputPerMTokUSD != 3.20 {
		t.Fatalf("glm-5 = %+v ok %v", base, ok)
	}
	if _, ok := familyFor("glm-9-no-such"); ok {
		t.Fatal("unknown id resolved to a family row")
	}
	older := buildDeclaredModel("glm-5.2", "GLM-5.2")
	if older.MaxOutputTokens != 0 || older.ContextWindow != 1_000_000 || older.DefaultEffort != "" {
		t.Fatalf("glm-5.2 defaults changed: %+v", older)
	}
}

func TestL4B2NonDatedNeighborIsUnknown(t *testing.T) {
	if _, ok := declaredFamilyFor("glm-5.3-pro"); ok {
		t.Fatal("glm-5.3-pro resolved to a GLM row")
	}
	dated, ok := declaredFamilyFor("glm-5.3-20260708")
	if !ok || dated.prefix != "glm-5.3" || dated.pricing == nil || dated.pricing.InputPerMTokUSD != 1.40 {
		t.Fatalf("glm-5.3-20260708 = %+v ok %v", dated, ok)
	}
	flash, ok := declaredFamilyFor("glm-5.3-flash")
	if !ok || flash.prefix != "glm-5.3-flash" {
		t.Fatalf("exact flash = %+v ok %v", flash, ok)
	}
}

func TestL4B2DeclaredGolden(t *testing.T) {
	const golden = "glm-5.2\tGLM-5.2\n" +
		"glm-5.1\tGLM-5.1\n" +
		"glm-5-turbo\tGLM-5-Turbo\n" +
		"glm-5\tGLM-5\n" +
		"glm-4.7-flashx\tGLM-4.7-FlashX\n" +
		"glm-4.7-flash\tGLM-4.7-Flash\n" +
		"glm-4.7\tGLM-4.7\n" +
		"glm-4.6v\tGLM-4.6V\n" +
		"glm-4.6\tGLM-4.6\n" +
		"glm-4.5v\tGLM-4.5V\n" +
		"glm-4.5-airx\tGLM-4.5-AirX\n" +
		"glm-4.5-air\tGLM-4.5-Air\n" +
		"glm-4.5-x\tGLM-4.5-X\n" +
		"glm-4.5-flash\tGLM-4.5-Flash\n" +
		"glm-4.5\tGLM-4.5\n" +
		"glm-4-32b-0414-128k\tGLM-4 32B 0414 128K\n" +
		"glm-4-plus\tGLM-4-Plus\n" +
		"glm-4-flashx\tGLM-4-FlashX\n" +
		"glm-4-flash\tGLM-4-Flash\n"
	assertDeclaredGolden(t, golden, declaredModels)
}

func TestL4B2SavedDefaults(t *testing.T) {
	want := []string{"glm-5.2", "glm-5.1", "glm-5-turbo", "glm-5"}
	if len(declaredModels) < len(want) {
		t.Fatalf("declared models = %d, want at least %d", len(declaredModels), len(want))
	}
	for i, id := range want {
		if declaredModels[i].id != id {
			t.Fatalf("declared model %d = %q, want %q", i, declaredModels[i].id, id)
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
	for _, d := range declaredModels {
		if d.id == id {
			return true
		}
	}
	return false
}

func hasCap(caps []modelprovider.Capability, want modelprovider.Capability) bool {
	return modelprovider.Has(caps, want)
}
