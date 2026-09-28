// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelrouter"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
)

// seedEstate enriches a representative spread of models so the router has a real
// governed catalog to select from.
func seedEstate(t *testing.T, m *Module, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	seeds := []struct{ provider, ref string }{
		{"anthropic", "claude-opus-4-8"},
		{"anthropic", "claude-haiku-4-5"},
		{"openai", "gpt-4o"},
		{"google", "gemini-1.5-flash"},
		{"ollama", "my-private-llm"}, // unpriced, no caps
	}
	for _, s := range seeds {
		if err := m.enrichFromCost(ctx, tenant, s.provider, s.ref); err != nil {
			t.Fatalf("seed %s: %v", s.ref, err)
		}
	}
}

// resolveSpec builds the catalog from the governed estate and resolves a spec.
func resolveSpec(t *testing.T, st store.Store, tenant model.TenantID, spec routingSpec) (modelrouter.Decision, error) {
	t.Helper()
	ctx := context.Background()
	var (
		dec  modelrouter.Decision
		derr error
	)
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cat, err := buildCatalog(ctx, sc)
		if err != nil {
			return err
		}
		dec, derr = spec.resolve(ctx, cat)
		return nil
	}); err != nil {
		t.Fatalf("resolveSpec view: %v", err)
	}
	return dec, derr
}

func TestRoutingCostPicksCheapest(t *testing.T) {
	m, st, tenant := newMod(t)
	seedEstate(t, m, tenant)
	dec, err := resolveSpec(t, st, tenant, routingSpec{Strategy: "cost"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// gemini-1.5-flash (0.075+0.3) is the cheapest priced model; the unpriced
	// local model sorts last, never first.
	if dec.Primary.ModelRef != "gemini-1.5-flash" {
		t.Errorf("cost primary = %s, want gemini-1.5-flash", dec.Primary.ModelRef)
	}
	last := dec.Chain()[len(dec.Chain())-1]
	if last.ModelRef != "my-private-llm" {
		t.Errorf("unpriced model should sort last, chain ends with %s", last.ModelRef)
	}
}

func TestRoutingCapabilityFilterExcludes(t *testing.T) {
	m, st, tenant := newMod(t)
	seedEstate(t, m, tenant)
	// Only opus/sonnet declare computer_use; of the seeds only opus qualifies.
	dec, err := resolveSpec(t, st, tenant, routingSpec{
		Strategy:             "capability",
		RequiredCapabilities: []string{"computer_use"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if dec.Primary.ModelRef != "claude-opus-4-8" {
		t.Errorf("capability primary = %s, want claude-opus-4-8", dec.Primary.ModelRef)
	}
	if len(dec.Fallbacks) != 0 {
		t.Errorf("want no fallbacks (only one capable model), got %d", len(dec.Fallbacks))
	}
}

func TestRoutingPinnedThenCost(t *testing.T) {
	m, st, tenant := newMod(t)
	seedEstate(t, m, tenant)
	dec, err := resolveSpec(t, st, tenant, routingSpec{Strategy: "pinned", PinnedModel: "gpt-4o"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if dec.Primary.ModelRef != "gpt-4o" {
		t.Errorf("pinned primary = %s, want gpt-4o", dec.Primary.ModelRef)
	}
	// The first fallback is the cheapest of the rest.
	if len(dec.Fallbacks) == 0 || dec.Fallbacks[0].ModelRef != "gemini-1.5-flash" {
		t.Errorf("pinned fallback[0] = %v, want gemini-1.5-flash first", dec.Fallbacks)
	}
}

func TestRoutingGatewayMarksTargets(t *testing.T) {
	m, st, tenant := newMod(t)
	seedEstate(t, m, tenant)
	dec, err := resolveSpec(t, st, tenant, routingSpec{Strategy: "cost", GatewayEndpoint: "http://gw:4000"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !dec.Primary.ViaGateway || dec.Primary.Endpoint != "http://gw:4000" {
		t.Errorf("gateway primary = %+v, want ViaGateway via http://gw:4000", dec.Primary)
	}
}

func TestRoutingNoCandidate(t *testing.T) {
	m, st, tenant := newMod(t)
	seedEstate(t, m, tenant)
	// No seeded Google model declares memory_tool → no candidate.
	_, err := resolveSpec(t, st, tenant, routingSpec{
		Strategy:             "capability",
		RequiredCapabilities: []string{"memory_tool"},
		PreferredProviders:   []string{"google"},
	})
	if !errors.Is(err, modelrouter.ErrNoCandidate) {
		t.Errorf("err = %v, want ErrNoCandidate", err)
	}
}

// TestBuildCatalog_NilReferencePriceUsesOperatorCost is M3: a reference row
// whose Pricing is nil must keep the operator per-token cost, with Source
// operator. A reference that has a list price keeps that price.
func TestBuildCatalog_NilReferencePriceUsesOperatorCost(t *testing.T) {
	_, st, tenant := newMod(t)
	ctx := context.Background()
	type row struct {
		provider, ref string
		in, out       int64
	}
	rows := []row{
		{"deepseek", "deepseek-flash", 17, 19},
		{"kimi", "kimi-for-coding", 23, 29},
		{"anthropic", "claude-opus-4-8", 99, 99},
		{"deepseek", "deepseek-v4-pro", -50, 10},
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		pids := map[string]model.ID{}
		for _, row := range rows {
			pid, ok := pids[row.provider]
			if !ok {
				p, err := sc.Providers().Create(ctx, model.Provider{Name: row.provider, Kind: row.provider, Status: model.StatusActive})
				if err != nil {
					return err
				}
				pid = p.ID
				pids[row.provider] = pid
			}
			md, err := sc.Models().Create(ctx, model.Model{Name: row.ref, ProviderID: pid, Status: model.StatusActive})
			if err != nil {
				return err
			}
			md.InputCostMicroUSD = row.in
			md.OutputCostMicroUSD = row.out
			if _, err := sc.Models().Update(ctx, md); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var cat mp.Catalog
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		cat, err = buildCatalog(ctx, sc)
		return err
	}); err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	byRef := make(map[string]mp.Model, len(cat.Models))
	for _, mm := range cat.Models {
		byRef[mm.Ref] = mm
	}
	for _, row := range rows[:2] {
		mm, ok := byRef[row.ref]
		if !ok {
			t.Fatalf("catalog missing %s", row.ref)
		}
		if mm.Pricing == nil {
			t.Fatalf("%s pricing is nil; operator cost %d/%d must be kept when the reference price is nil", row.ref, row.in, row.out)
		}
		if mm.Pricing.Source != mp.PricingOperator {
			t.Fatalf("%s source = %q, want operator", row.ref, mm.Pricing.Source)
		}
		if mm.Pricing.InputPerMTokUSD != float64(row.in) || mm.Pricing.OutputPerMTokUSD != float64(row.out) || mm.Pricing.Currency != "USD" {
			t.Fatalf("%s price = %+v, want operator %d/%d USD", row.ref, mm.Pricing, row.in, row.out)
		}
	}
	opus, ok := byRef["claude-opus-4-8"]
	if !ok || opus.Pricing == nil || opus.Pricing.Source != mp.PricingList || opus.Pricing.InputPerMTokUSD != 5 || opus.Pricing.OutputPerMTokUSD != 25 {
		t.Fatalf("priced reference must keep list 5/25, got ok=%v pricing=%+v", ok, opus.Pricing)
	}
	mixed, ok := byRef["deepseek-v4-pro"]
	if !ok || mixed.Pricing != nil {
		t.Fatalf("mixed-sign operator cost must not be a price, got ok=%v pricing=%+v", ok, mixed.Pricing)
	}
}
