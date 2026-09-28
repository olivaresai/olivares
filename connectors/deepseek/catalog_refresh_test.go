// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package deepseek

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func TestL4B2CurrentRows(t *testing.T) {
	flash := buildDeclaredModel("deepseek-flash", "DeepSeek-V4.1-Flash")
	if flash.Pricing != nil || flash.ContextWindow != 1_000_000 || flash.MaxOutputTokens != 384_000 ||
		flash.Deprecated || !flash.HasCapability(modelprovider.CapVision) ||
		!flash.HasCapability(modelprovider.CapToolUse) || !flash.HasCapability(modelprovider.CapStructuredOutputs) {
		t.Fatalf("deepseek-flash = %+v caps %v", flash, flash.Capabilities)
	}
	pro := buildDeclaredModel("deepseek-v4-pro", "DeepSeek-V4-Pro-0813")
	if pro.Pricing != nil || pro.ContextWindow != 1_000_000 || pro.MaxOutputTokens != 384_000 ||
		pro.HasCapability(modelprovider.CapVision) || !pro.HasCapability(modelprovider.CapToolUse) ||
		!pro.HasCapability(modelprovider.CapStructuredOutputs) || pro.Deprecated {
		t.Fatalf("deepseek-v4-pro = %+v caps %v", pro, pro.Capabilities)
	}
	legacy := buildDeclaredModel("deepseek-v4-flash", "legacy")
	if legacy.Pricing != nil || !legacy.Deprecated || len(legacy.Retirements) != 1 ||
		legacy.Retirements[0].ReplacementRef != "deepseek-flash" || legacy.Retirements[0].RetiresOn != "" {
		t.Fatalf("legacy deepseek-v4-flash = %+v", legacy)
	}
	vision := buildDeclaredModel("deepseek-v4-flash-vision-exp", "legacy vision")
	if vision.Pricing != nil || !vision.Deprecated || !vision.HasCapability(modelprovider.CapVision) ||
		len(vision.Retirements) != 1 || vision.Retirements[0].ReplacementRef != "deepseek-flash" {
		t.Fatalf("legacy vision id = %+v caps %v", vision, vision.Capabilities)
	}
	if _, ok := familyFor("deepseek-flash"); ok {
		t.Fatal("deepseek-flash must not take a single static price")
	}
	if _, ok := familyFor("deepseek-v4-pro"); ok {
		t.Fatal("deepseek-v4-pro must not take a single static price")
	}
	if _, ok := familyFor("deepseek-v4-flash"); ok {
		t.Fatal("exact legacy flash id must not keep the old single price")
	}
	for _, id := range []string{"deepseek-flash", "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		if !declaredID(id) {
			t.Fatalf("declared catalog missing %s", id)
		}
	}
}

func TestL4B2LiveCurrentIDs(t *testing.T) {
	doer := &fixtureDoer{t: t, bodyOverride: map[string]string{
		"/models": `{"object":"list","data":[` +
			`{"id":"deepseek-flash","object":"model","created":1,"owned_by":"deepseek"},` +
			`{"id":"deepseek-v4-pro","object":"model","created":1,"owned_by":"deepseek"},` +
			`{"id":"deepseek-v4-flash","object":"model","created":1,"owned_by":"deepseek"},` +
			`{"id":"deepseek-v4-flash-vision-exp","object":"model","created":1,"owned_by":"deepseek"}]}`,
	}}
	s := newSource(t, doer, nil)
	cat, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	flash, ok := cat.FindModel("deepseek-flash")
	if !ok || flash.Pricing != nil || flash.ContextWindow != 1_000_000 || flash.MaxOutputTokens != 384_000 ||
		flash.Deprecated || !flash.HasCapability(modelprovider.CapVision) ||
		!flash.HasCapability(modelprovider.CapToolUse) || !flash.HasCapability(modelprovider.CapStructuredOutputs) ||
		flash.CapabilitySource != "live" {
		t.Fatalf("live deepseek-flash = %+v caps %v", flash, flash.Capabilities)
	}
	pro, ok := cat.FindModel("deepseek-v4-pro")
	if !ok || pro.Pricing != nil || pro.ContextWindow != 1_000_000 || pro.MaxOutputTokens != 384_000 ||
		pro.Deprecated || pro.HasCapability(modelprovider.CapVision) || !pro.HasCapability(modelprovider.CapToolUse) {
		t.Fatalf("live deepseek-v4-pro = %+v caps %v", pro, pro.Capabilities)
	}
	legacy, ok := cat.FindModel("deepseek-v4-flash")
	if !ok || legacy.Pricing != nil || !legacy.Deprecated || legacy.ContextWindow != 1_000_000 ||
		len(legacy.Retirements) != 1 || legacy.Retirements[0].ReplacementRef != "deepseek-flash" {
		t.Fatalf("live legacy flash = %+v", legacy)
	}
	vision, ok := cat.FindModel("deepseek-v4-flash-vision-exp")
	if !ok || vision.Pricing != nil || !vision.Deprecated || !vision.HasCapability(modelprovider.CapVision) ||
		len(vision.Retirements) != 1 || vision.Retirements[0].ReplacementRef != "deepseek-flash" {
		t.Fatalf("live legacy vision = %+v caps %v", vision, vision.Capabilities)
	}
}

func TestL4B2PrefixNeighborAndUnknown(t *testing.T) {
	dated, ok := familyFor("deepseek-v4-flash-20260704")
	if !ok || dated.pricing.InputPerMTokUSD != 0.14 || dated.pricing.OutputPerMTokUSD != 0.28 {
		t.Fatalf("dated flash neighbor = %+v ok %v", dated, ok)
	}
	proDated, ok := familyFor("deepseek-v4-pro-20260704")
	if !ok || proDated.pricing.InputPerMTokUSD != 0.435 {
		t.Fatalf("dated pro neighbor = %+v ok %v", proDated, ok)
	}
	chat, ok := familyFor("deepseek-chat-20260601")
	if !ok || chat.pricing.InputPerMTokUSD != 0.27 {
		t.Fatalf("dated chat neighbor = %+v ok %v", chat, ok)
	}
	if _, ok := familyFor("not-a-deepseek-model"); ok {
		t.Fatal("unknown id resolved to a family row")
	}
}

func TestL4B2DeclaredGolden(t *testing.T) {
	const golden = "deepseek-v4-flash\tDeepSeek V4 Flash\n" +
		"deepseek-v4-pro\tDeepSeek V4 Pro\n" +
		"deepseek-chat\tDeepSeek Chat (V3)\n" +
		"deepseek-reasoner\tDeepSeek Reasoner (R1)\n"
	assertDeclaredGolden(t, golden, declaredModels)
}

func TestL4B2SavedDefaults(t *testing.T) {
	want := []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-chat", "deepseek-reasoner"}
	if len(declaredModels) < len(want) {
		t.Fatalf("declared models = %d, want at least %d", len(declaredModels), len(want))
	}
	for i, id := range want {
		if declaredModels[i].id != id {
			t.Fatalf("declared model %d = %q, want %q", i, declaredModels[i].id, id)
		}
	}
	chat := buildDeclaredModel("deepseek-chat", "DeepSeek Chat (V3)")
	if chat.Pricing == nil || chat.Pricing.InputPerMTokUSD != 0.27 || !chat.Deprecated ||
		len(chat.Retirements) != 1 || chat.Retirements[0].ReplacementRef != "deepseek-v4-flash" {
		t.Fatalf("deepseek-chat defaults changed: %+v", chat)
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
