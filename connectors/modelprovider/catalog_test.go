// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import "testing"

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
