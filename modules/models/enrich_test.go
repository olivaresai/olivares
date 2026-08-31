// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// newMod opens a real SQLite store with the module schema, provisions a tenant
// and wires the module's data handle (mirrors the inventory test harness, so the
// tests exercise the real generic repository and core typed repos, not a fake).
func newMod(t *testing.T) (*Module, store.Store, model.TenantID) {
	t.Helper()
	m := New()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return m, st, tenant
}

// getModel returns the single model named ref in the tenant.
func getModel(t *testing.T, st store.Store, tenant model.TenantID, ref string) (model.Model, bool) {
	t.Helper()
	var (
		out   model.Model
		found bool
	)
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		ms, _, err := sc.Models().List(context.Background(), model.Query{Filters: []model.Filter{eq("name", ref)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(ms) > 0 {
			out, found = ms[0], true
		}
		return nil
	}); err != nil {
		t.Fatalf("getModel: %v", err)
	}
	return out, found
}

func TestEnrichClaudeModel(t *testing.T) {
	m, st, tenant := newMod(t)
	if err := m.enrichFromCost(context.Background(), tenant, "anthropic", "claude-opus-4-8"); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	md, ok := getModel(t, st, tenant, "claude-opus-4-8")
	if !ok {
		t.Fatal("model not created")
	}
	if md.Family != "claude-opus" {
		t.Errorf("family = %q, want claude-opus", md.Family)
	}
	if md.ContextWindow != 200000 {
		t.Errorf("context_window = %d, want 200000", md.ContextWindow)
	}
	if md.InputCostMicroUSD != 5 || md.OutputCostMicroUSD != 25 {
		t.Errorf("per-token cost = %d/%d, want 5/25 (current Opus)", md.InputCostMicroUSD, md.OutputCostMicroUSD)
	}
	if e, _ := md.Metadata[metaEnriched].(bool); !e {
		t.Error("model not marked enriched")
	}
	caps, _ := md.Metadata[metaCapabilities].([]any)
	hasComputerUse := false
	for _, c := range caps {
		if c == "computer_use" {
			hasComputerUse = true
		}
	}
	if !hasComputerUse {
		t.Errorf("capabilities missing computer_use: %v", caps)
	}
	// The provider was materialized too.
	if _, ok := getModel(t, st, tenant, "claude-opus-4-8"); !ok {
		t.Error("model missing")
	}
}

func TestEnrichIsIdempotent(t *testing.T) {
	m, st, tenant := newMod(t)
	ctx := context.Background()
	if err := m.enrichFromCost(ctx, tenant, "anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatal(err)
	}
	md1, _ := getModel(t, st, tenant, "claude-sonnet-4-5")
	// One create (v1) + one enrich update (v2) within the first call.
	if md1.Version != 2 {
		t.Fatalf("version after first enrich = %d, want 2", md1.Version)
	}
	if err := m.enrichFromCost(ctx, tenant, "anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatal(err)
	}
	md2, _ := getModel(t, st, tenant, "claude-sonnet-4-5")
	if md2.Version != 2 {
		t.Errorf("version after second enrich = %d, want 2 (no churn)", md2.Version)
	}
}

func TestEnrichUnknownModelLeftBare(t *testing.T) {
	m, st, tenant := newMod(t)
	if err := m.enrichFromCost(context.Background(), tenant, "ollama", "my-private-llm"); err != nil {
		t.Fatal(err)
	}
	md, ok := getModel(t, st, tenant, "my-private-llm")
	if !ok {
		t.Fatal("model not created")
	}
	if md.Family != "" || md.InputCostMicroUSD != 0 {
		t.Errorf("unknown model enriched with invented data: family=%q cost=%d", md.Family, md.InputCostMicroUSD)
	}
	if e, _ := md.Metadata[metaEnriched].(bool); e {
		t.Error("unknown model wrongly marked enriched")
	}
}

func TestEnrichCheapModelPrecisionInMetadata(t *testing.T) {
	m, st, tenant := newMod(t)
	if err := m.enrichFromCost(context.Background(), tenant, "openai", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	md, _ := getModel(t, st, tenant, "gpt-4o-mini")
	// The coarse per-token integer floors to 0 for a sub-µUSD model...
	if md.InputCostMicroUSD != 0 {
		t.Errorf("gpt-4o-mini per-token input = %d, want 0 (coarse)", md.InputCostMicroUSD)
	}
	// ...but the precise USD/MTok price is preserved in metadata.
	if v, _ := md.Metadata[metaInPerMTok].(float64); v != 0.15 {
		t.Errorf("metadata input_per_mtok_usd = %v, want 0.15", v)
	}
}

// A declared family with no verified price (Pricing nil) must not push the zero
// default over a cost the operator set. Before this was guarded, re-enriching
// such a model wiped 3/15 to 0/0: the reference saying "we have not verified a
// price" was written to the record as "this model costs nothing", and the only
// figure the model had was gone. Nil pricing is a statement about our table,
// never about the model being free.
func TestEnrichNilPricedFamilyKeepsOperatorCost(t *testing.T) {
	m, st, tenant := newMod(t)
	ctx := context.Background()
	const ref = "claude-3-5-sonnet-20241022" // declared family, Pricing nil

	if err := m.enrichFromCost(ctx, tenant, "anthropic", ref); err != nil {
		t.Fatalf("first enrich: %v", err)
	}
	md, ok := getModel(t, st, tenant, ref)
	if !ok {
		t.Fatal("model not created")
	}
	if md.Family != "claude-3-5-sonnet" {
		t.Fatalf("family = %q, want claude-3-5-sonnet (the nil-priced family)", md.Family)
	}

	md.InputCostMicroUSD, md.OutputCostMicroUSD = 3, 15
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Models().Update(ctx, md)
		return err
	}); err != nil {
		t.Fatalf("operator sets cost: %v", err)
	}

	if err := m.enrichFromCost(ctx, tenant, "anthropic", ref); err != nil {
		t.Fatalf("second enrich: %v", err)
	}
	got, _ := getModel(t, st, tenant, ref)
	if got.InputCostMicroUSD != 3 || got.OutputCostMicroUSD != 15 {
		t.Errorf("operator cost after re-enrich = %d/%d, want 3/15 kept",
			got.InputCostMicroUSD, got.OutputCostMicroUSD)
	}
	// Control: the metadata still carries no price keys, because we still have none.
	if _, has := got.Metadata[metaInPerMTok]; has {
		t.Error("metadata gained a price key for a family whose reference has none")
	}
}

// Control for the pair above: a family that DOES declare a price still writes it,
// so the guard above cannot pass by simply never writing costs.
func TestEnrichPricedFamilyStillWritesItsCost(t *testing.T) {
	m, st, tenant := newMod(t)
	ctx := context.Background()
	const ref = "claude-opus-4-8"

	if err := m.enrichFromCost(ctx, tenant, "anthropic", ref); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	md, _ := getModel(t, st, tenant, ref)
	if md.InputCostMicroUSD != 5 || md.OutputCostMicroUSD != 25 {
		t.Errorf("priced family cost = %d/%d, want 5/25", md.InputCostMicroUSD, md.OutputCostMicroUSD)
	}
}
