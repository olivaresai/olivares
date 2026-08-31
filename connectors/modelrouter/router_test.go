// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelrouter

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

func price(in, out float64) *modelprovider.ModelPricing {
	return &modelprovider.ModelPricing{InputPerMTokUSD: in, OutputPerMTokUSD: out, Currency: "USD"}
}

// sampleCatalog: a cheap Haiku, a mid Sonnet (vision), an expensive Opus (vision +
// computer use), a deprecated old model, and a cheap local model with measured
// latency but no price.
func sampleCatalog() modelprovider.Catalog {
	return modelprovider.Catalog{
		Models: []modelprovider.Model{
			{ProviderRef: modelprovider.ProviderAnthropic, Ref: "haiku", Pricing: price(0.8, 4), ContextWindow: 200000, Capabilities: []modelprovider.Capability{modelprovider.CapToolUse}},
			{ProviderRef: modelprovider.ProviderAnthropic, Ref: "sonnet", Pricing: price(3, 15), ContextWindow: 200000, Capabilities: []modelprovider.Capability{modelprovider.CapToolUse, modelprovider.CapVision}},
			{ProviderRef: modelprovider.ProviderAnthropic, Ref: "opus", Pricing: price(15, 75), ContextWindow: 200000, Capabilities: []modelprovider.Capability{modelprovider.CapToolUse, modelprovider.CapVision, modelprovider.CapComputerUse}},
			{ProviderRef: modelprovider.ProviderAnthropic, Ref: "old", Pricing: price(0.1, 0.1), Deprecated: true},
			{ProviderRef: modelprovider.ProviderOllama, Ref: "llama-local", ObservedLatencyMillis: 40, Capabilities: []modelprovider.Capability{modelprovider.CapToolUse}},
		},
	}
}

func TestNative_PolicyCost_CheapestFirst(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), PolicyCost)
	d, err := r.Route(context.Background(), Requirement{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Cheapest non-deprecated with known price is haiku (0.8+4). Local has no price
	// so it sorts last. Deprecated "old" is excluded despite being cheapest.
	if d.Primary.ModelRef != "haiku" {
		t.Fatalf("primary = %s, want haiku", d.Primary.ModelRef)
	}
	chain := d.Chain()
	if chain[len(chain)-1].ModelRef != "llama-local" {
		t.Fatalf("unpriced local model should sort last, chain = %v", refs(chain))
	}
	for _, tgt := range chain {
		if tgt.ModelRef == "old" {
			t.Fatal("deprecated model must be excluded by default")
		}
		if tgt.ViaGateway {
			t.Fatal("native router must produce direct targets")
		}
	}
}

func TestNative_CapabilityFilter(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), PolicyCost)
	d, err := r.Route(context.Background(), Requirement{RequiredCapabilities: []modelprovider.Capability{modelprovider.CapComputerUse}})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(d.Chain()) != 1 || d.Primary.ModelRef != "opus" {
		t.Fatalf("computer_use should select only opus, got %v", refs(d.Chain()))
	}
}

func TestNative_NoCandidate(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), PolicyCost)
	_, err := r.Route(context.Background(), Requirement{RequiredCapabilities: []modelprovider.Capability{modelprovider.CapMemoryTool}})
	if !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("err = %v, want ErrNoCandidate", err)
	}
}

func TestNative_MinContextWindow(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), PolicyCost)
	// local model has unknown (0) window -> not excluded; priced models all 200k.
	d, err := r.Route(context.Background(), Requirement{MinContextWindow: 199999})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	for _, tgt := range d.Chain() {
		if tgt.ModelRef == "old" {
			t.Fatal("deprecated still excluded")
		}
	}
	if d.Primary.ModelRef != "haiku" {
		t.Fatalf("primary = %s, want haiku", d.Primary.ModelRef)
	}
}

func TestNative_PreferredProviders(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), PolicyCost)
	d, err := r.Route(context.Background(), Requirement{PreferredProviders: []string{modelprovider.ProviderOllama}})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(d.Chain()) != 1 || d.Primary.ModelRef != "llama-local" {
		t.Fatalf("provider filter failed, got %v", refs(d.Chain()))
	}
}

func TestNative_PolicyPinned(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), PolicyPinned)
	d, err := r.Route(context.Background(), Requirement{PinnedModel: "opus"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Primary.ModelRef != "opus" {
		t.Fatalf("pinned primary = %s, want opus", d.Primary.ModelRef)
	}
	// Remaining ordered by cost: haiku before sonnet.
	fb := refs(d.Fallbacks)
	if indexOf(fb, "haiku") > indexOf(fb, "sonnet") {
		t.Fatalf("fallbacks not cost-ordered: %v", fb)
	}
}

func TestNative_PolicyLatency(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), PolicyLatency)
	d, err := r.Route(context.Background(), Requirement{RequiredCapabilities: []modelprovider.Capability{modelprovider.CapToolUse}})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	// Only the local model has a measured latency, so it leads; unmeasured last.
	if d.Primary.ModelRef != "llama-local" {
		t.Fatalf("latency primary = %s, want llama-local", d.Primary.ModelRef)
	}
}

func TestNative_InvalidPolicyDefaultsToCost(t *testing.T) {
	r := NewNativeRouter(sampleCatalog(), Policy("bogus"))
	d, err := r.Route(context.Background(), Requirement{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Policy != PolicyCost || d.Primary.ModelRef != "haiku" {
		t.Fatalf("invalid policy did not default to cost: %+v", d)
	}
}

func TestGatewayRouter_RewritesTargets(t *testing.T) {
	r := NewGatewayRouter(sampleCatalog(), PolicyCost, "http://litellm.internal:4000")
	d, err := r.Route(context.Background(), Requirement{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	for _, tgt := range d.Chain() {
		if !tgt.ViaGateway || tgt.Endpoint != "http://litellm.internal:4000" {
			t.Fatalf("gateway target not rewritten: %+v", tgt)
		}
	}
	// Selection is still native: cheapest first.
	if d.Primary.ModelRef != "haiku" {
		t.Fatalf("gateway primary = %s, want haiku (native selection)", d.Primary.ModelRef)
	}
}

func refs(ts []Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ModelRef
	}
	return out
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
