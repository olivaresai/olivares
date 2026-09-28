// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import "testing"

func TestExactCompatibleModel(t *testing.T) {
	m, ok := ExactCompatibleModel("kimi-k3")
	if !ok || m.Ref != "kimi-k3" || m.DisplayName != "Kimi K3" || m.ProviderRef != ProviderKimi {
		t.Fatalf("exact = %+v ok %v", m, ok)
	}
	spaced, ok := ExactCompatibleModel("  KiMi-K3  ")
	if !ok || spaced.Ref != "kimi-k3" || spaced.DisplayName != "Kimi K3" {
		t.Fatalf("case and space = %+v ok %v", spaced, ok)
	}
	if _, ok := ExactCompatibleModel(""); ok {
		t.Fatal("empty id matched")
	}
	if _, ok := ExactCompatibleModel("no-such-model"); ok {
		t.Fatal("unknown id matched")
	}
	if _, ok := ExactCompatibleModel("kimi-k3-pro"); ok {
		t.Fatal("a longer id matched a prefix")
	}
	if _, ok := ExactCompatibleModel("kimi"); ok {
		t.Fatal("a shorter id matched a prefix")
	}
	if len(m.Capabilities) == 0 || m.Pricing == nil {
		t.Fatalf("kimi-k3 copy = %+v", m)
	}
	m.DisplayName = "mutated"
	m.Capabilities[0] = CapToolUse
	m.Pricing.InputPerMTokUSD = 99
	again, ok := ExactCompatibleModel("kimi-k3")
	if !ok || again.DisplayName != "Kimi K3" || again.Capabilities[0] != CapStreaming ||
		again.Pricing == nil || again.Pricing.InputPerMTokUSD != 3 {
		t.Fatalf("mutating the copy changed the catalog: %+v", again)
	}
}

func TestKimiCompatibleRowsOmitExtendedThinking(t *testing.T) {
	ids := []string{"kimi-k3", "k3", "k3-256k", "kimi-for-coding", "kimi-for-coding-highspeed"}
	for _, id := range ids {
		m, ok := ExactCompatibleModel(id)
		if !ok {
			t.Fatalf("ExactCompatibleModel(%s) missing", id)
		}
		if Has(m.Capabilities, CapExtendedThinking) {
			t.Fatalf("%s still has CapExtendedThinking", id)
		}
	}
	k3, ok := ExactCompatibleModel("kimi-k3")
	if !ok || !Has(k3.Capabilities, CapStreaming) || !Has(k3.Capabilities, CapPromptCaching) {
		t.Fatalf("kimi-k3 streaming or caching = %+v", k3.Capabilities)
	}
}

func TestSnapshotDateSuffix(t *testing.T) {
	if SnapshotDateSuffix("gpt-6-sol", "gpt-6-sol") {
		t.Fatal("the bare prefix is not a snapshot suffix")
	}
	if !SnapshotDateSuffix("gpt-6-sol-20260927", "gpt-6-sol") {
		t.Fatal("YYYYMMDD suffix")
	}
	if !SnapshotDateSuffix("gpt-6-sol-2026-09-27", "gpt-6-sol") {
		t.Fatal("YYYY-MM-DD suffix")
	}
	for _, id := range []string{"gpt-6-sol-pro", "gpt-6-sol-20260927-extra", "gpt-6-sol-2026-9-27", "gpt-6-solpro"} {
		if SnapshotDateSuffix(id, "gpt-6-sol") {
			t.Fatalf("%s matched as a snapshot suffix", id)
		}
	}
}

func TestHasAndHasCapability(t *testing.T) {
	caps := []Capability{CapVision, CapToolUse, CapPromptCaching}
	if !Has(caps, CapVision) {
		t.Fatal("Has missed a present capability")
	}
	if Has(caps, CapComputerUse) {
		t.Fatal("Has reported an absent capability")
	}
	m := Model{Capabilities: caps}
	if !m.HasCapability(CapToolUse) || m.HasCapability(CapBatch) {
		t.Fatal("HasCapability disagrees with Has")
	}
	if Has(nil, CapVision) {
		t.Fatal("Has on nil slice must be false")
	}
}

func TestFindModel(t *testing.T) {
	cat := Catalog{Models: []Model{
		{Ref: "a", ProviderRef: ProviderOpenAI},
		{Ref: "b", ProviderRef: ProviderAnthropic},
	}}
	if m, ok := cat.FindModel("b"); !ok || m.ProviderRef != ProviderAnthropic {
		t.Fatalf("FindModel(b) = %+v, %v", m, ok)
	}
	if _, ok := cat.FindModel("missing"); ok {
		t.Fatal("FindModel found a missing model")
	}
}
