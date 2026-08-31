// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package residency_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
)

func TestNewRegistry(t *testing.T) {
	t.Parallel()
	// Empty home = single-region mode: nil registry, not enforcing.
	if reg, err := residency.NewRegistry("", nil); err != nil || reg.Enforces() {
		t.Fatalf("empty home: want nil non-enforcing registry, got reg=%v err=%v", reg, err)
	}
	// Known regions without a home is a misconfiguration (fail closed at boot).
	if _, err := residency.NewRegistry("", []string{"eu"}); err == nil {
		t.Fatal("known regions without a home: want error, got nil")
	}
	// Invalid region codes fail closed.
	if _, err := residency.NewRegistry("EU west", nil); err == nil {
		t.Fatal("invalid home code: want error, got nil")
	}
	if _, err := residency.NewRegistry("eu", []string{"us", "bad/code"}); err == nil {
		t.Fatal("invalid known code: want error, got nil")
	}
	// Valid: home is normalized and implicitly known; declared knowns are included.
	reg, err := residency.NewRegistry("EU", []string{"us"})
	if err != nil {
		t.Fatalf("valid registry: unexpected error %v", err)
	}
	if reg.Home() != "eu" {
		t.Fatalf("home not normalized: got %q", reg.Home())
	}
	if !reg.IsKnown("eu") || !reg.IsKnown("us") {
		t.Fatalf("known set wrong: %v", reg.Known())
	}
	if reg.IsKnown("apac") {
		t.Fatal("apac must not be known")
	}
}

func TestRegistryServes(t *testing.T) {
	t.Parallel()
	reg, _ := residency.NewRegistry("eu", []string{"us"})
	cases := []struct {
		pin  string
		want bool
	}{
		{"", true},      // unpinned: served anywhere
		{"eu", true},    // home region
		{"EU", true},    // case-insensitive
		{"us", false},   // another region: denied
		{"apac", false}, // unknown region: denied
	}
	for _, c := range cases {
		if got := reg.Serves(c.pin); got != c.want {
			t.Errorf("Serves(%q) = %v, want %v", c.pin, got, c.want)
		}
	}
	// A nil/non-enforcing registry serves everything (single-region mode).
	var none *residency.Registry
	if !none.Serves("us") || !none.Serves("") {
		t.Fatal("non-enforcing registry must serve every pin")
	}
}

func TestRegistryValidatePin(t *testing.T) {
	t.Parallel()
	reg, _ := residency.NewRegistry("eu", []string{"us"})
	if err := reg.ValidatePin(""); err != nil {
		t.Errorf("empty pin must be allowed (unpinned): %v", err)
	}
	if err := reg.ValidatePin("eu"); err != nil {
		t.Errorf("home pin must be allowed: %v", err)
	}
	if err := reg.ValidatePin("us"); err == nil {
		t.Error("a known non-home pin must be rejected on a region-scoped instance")
	}
	if err := reg.ValidatePin("apac"); err == nil {
		t.Error("an unknown pin must be rejected")
	}
	// Single-region instance: a non-empty pin cannot be honored (no false promise).
	none, _ := residency.NewRegistry("", nil)
	if err := none.ValidatePin("eu"); err == nil {
		t.Error("pinning without a home region must be rejected")
	}
	if err := none.ValidatePin(""); err != nil {
		t.Errorf("empty pin on a single-region instance must be allowed: %v", err)
	}
}

func TestInferenceGeoCompatible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pin, geo string
		want     bool
	}{
		{"", "us", true},        // unpinned: any geo
		{"", "", true},          // unpinned + unknown geo
		{"eu", "eu", true},      // in-region
		{"eu", "EU", true},      // case-insensitive
		{"us", "us", true},      // in-region
		{"eu", "us", false},     // crosses region
		{"eu", "global", false}, // global may route anywhere: not in-region
		{"eu", "", false},       // residency unprovable when pinned
		{"us", "not_available", false},
	}
	for _, c := range cases {
		if got := residency.InferenceGeoCompatible(c.pin, c.geo); got != c.want {
			t.Errorf("InferenceGeoCompatible(%q,%q) = %v, want %v", c.pin, c.geo, got, c.want)
		}
	}
	if got := residency.AllowedInferenceGeos(""); got != nil {
		t.Errorf("unpinned AllowedInferenceGeos = %v, want nil", got)
	}
	if got := residency.AllowedInferenceGeos("eu"); len(got) != 1 || got[0] != "eu" {
		t.Errorf("eu AllowedInferenceGeos = %v, want [eu]", got)
	}
}

// TestGuardDenyClosed is the heart of residency enforcement: a region-scoped
// instance serves only its
// home region's tenants, and refuses every cross-region access fail-closed. It uses a
// real SQLite store with three orgs (eu, us, unpinned) wrapped by Guard(home=eu).
func TestGuardDenyClosed(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	t.Cleanup(func() { _ = st.Close() })

	euT := createOrg(t, st, "eu-corp", "eu")
	usT := createOrg(t, st, "us-corp", "us")
	unpinnedT := createOrg(t, st, "global-corp", "")

	reg, err := residency.NewRegistry("eu", []string{"eu", "us"})
	if err != nil {
		t.Fatal(err)
	}
	g := residency.Guard(st, reg, nil)

	// Home-region tenant: View and Mutate both run.
	mustRun(t, g, euT, "home-region eu tenant")
	// Unpinned tenant: no residency requirement, served anywhere.
	mustRun(t, g, unpinnedT, "unpinned tenant")
	// System tenant: passthrough (the local auth/provisioning partition).
	mustRun(t, g, model.SystemTenantID, "system tenant")

	// Cross-region tenant (pinned us, reached the eu instance): denied closed.
	mustDeny(t, g, usT, "us tenant on eu instance")
	// A tenant with no org row here (resident elsewhere): denied closed, not silently empty.
	mustDeny(t, g, model.NewTenantID(), "non-resident tenant")
}

// TestGuardPassthrough: with no home region (single-region mode) Guard returns the
// store untouched and never enforces.
func TestGuardPassthrough(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	t.Cleanup(func() { _ = st.Close() })
	none, _ := residency.NewRegistry("", nil)
	if g := residency.Guard(st, none, nil); g != st {
		t.Fatal("single-region Guard must return the store unchanged (passthrough)")
	}
	if g := residency.Guard(st, nil, nil); g != st {
		t.Fatal("nil-registry Guard must return the store unchanged (passthrough)")
	}
}

// TestPinRoundtrip verifies CreateOrg persists the pin and SetOrgRegion changes it,
// reflected by GetOrg.
func TestPinRoundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t)
	t.Cleanup(func() { _ = st.Close() })

	tenant := createOrg(t, st, "acme", "eu")
	// CreateOrg persisted the pin.
	if got := getOrgRegion(t, st, tenant); got != "eu" {
		t.Fatalf("CreateOrg pin: got %q want eu", got)
	}
	// SetOrgRegion changes it (here: clear the pin).
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgRegion(ctx, tenant, "")
		return e
	}); err != nil {
		t.Fatalf("SetOrgRegion: %v", err)
	}
	if got := getOrgRegion(t, st, tenant); got != "" {
		t.Fatalf("after clear: got %q want empty", got)
	}
	// SetOrgRegion refuses the reserved system tenant.
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgRegion(ctx, model.SystemTenantID, "eu")
		return e
	}); err == nil {
		t.Fatal("SetOrgRegion on the system tenant must fail")
	}
}

// --- helpers -----------------------------------------------------------------

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(context.Background())
		return e
	}); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	return st
}

func createOrg(t *testing.T, st store.Store, slug, region string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(context.Background(), model.Org{
			Name: slug, Slug: slug, Status: model.StatusActive, DataRegion: region,
		})
		if e != nil {
			return e
		}
		tenant = o.TenantID
		return nil
	}); err != nil {
		t.Fatalf("create org %q: %v", slug, err)
	}
	return tenant
}

func getOrgRegion(t *testing.T, st store.Store, tenant model.TenantID) string {
	t.Helper()
	var region string
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, e := sys.GetOrg(context.Background(), tenant)
		if e != nil {
			return e
		}
		region = o.DataRegion
		return nil
	}); err != nil {
		t.Fatalf("get org: %v", err)
	}
	return region
}

// mustRun asserts View and Mutate both execute the closure (access allowed).
func mustRun(t *testing.T, g store.Store, tenant model.TenantID, what string) {
	t.Helper()
	ran := false
	if err := g.View(context.Background(), tenant, func(store.Scope) error { ran = true; return nil }); err != nil {
		t.Fatalf("%s: View must be allowed, got %v", what, err)
	}
	if !ran {
		t.Fatalf("%s: View closure did not run", what)
	}
	ran = false
	if err := g.Mutate(context.Background(), tenant, func(store.Scope) error { ran = true; return nil }); err != nil {
		t.Fatalf("%s: Mutate must be allowed, got %v", what, err)
	}
	if !ran {
		t.Fatalf("%s: Mutate closure did not run", what)
	}
}

// mustDeny asserts View and Mutate both fail with ErrResidencyViolation BEFORE the
// closure runs (deny-closed).
func mustDeny(t *testing.T, g store.Store, tenant model.TenantID, what string) {
	t.Helper()
	err := g.View(context.Background(), tenant, func(store.Scope) error {
		t.Fatalf("%s: View closure must NOT run (deny-closed)", what)
		return nil
	})
	if !errors.Is(err, store.ErrResidencyViolation) {
		t.Fatalf("%s: View want ErrResidencyViolation, got %v", what, err)
	}
	err = g.Mutate(context.Background(), tenant, func(store.Scope) error {
		t.Fatalf("%s: Mutate closure must NOT run (deny-closed)", what)
		return nil
	})
	if !errors.Is(err, store.ErrResidencyViolation) {
		t.Fatalf("%s: Mutate want ErrResidencyViolation, got %v", what, err)
	}
}

// TestExportIsGatedByResidency: the portability carve-out crosses the SERVICE door
// and must not cross the REGION one. Getting your data out is not a reason to read
// it from a region that may not serve it — the copy would leave across exactly the
// border the pin exists to hold, and residency is a legal boundary, not a
// commercial one.
func TestExportIsGatedByResidency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t)
	t.Cleanup(func() { _ = st.Close() })

	euT := createOrg(t, st, "eu-corp", "eu")
	usT := createOrg(t, st, "us-corp", "us")

	reg, err := residency.NewRegistry("eu", []string{"eu", "us"})
	if err != nil {
		t.Fatal(err)
	}
	g := residency.Guard(st, reg, nil)

	var ran bool
	if err := g.Export(ctx, euT, func(es store.ExportScope) error {
		ran = true
		_, _, e := es.Audit().Head(ctx)
		return e
	}); err != nil {
		t.Fatalf("export for a home-region tenant must run: %v", err)
	}
	if !ran {
		t.Fatal("export did not run the caller's work for a home-region tenant")
	}

	ran = false
	err = g.Export(ctx, usT, func(store.ExportScope) error { ran = true; return nil })
	if !errors.Is(err, store.ErrResidencyViolation) {
		t.Fatalf("export for a cross-region tenant: got %v, want ErrResidencyViolation", err)
	}
	if ran {
		t.Fatal("export ran for a tenant resident in another region: the copy would cross the pin")
	}

	ran = false
	err = g.Export(ctx, model.NewTenantID(), func(store.ExportScope) error { ran = true; return nil })
	if !errors.Is(err, store.ErrResidencyViolation) {
		t.Fatalf("export for a non-resident tenant: got %v, want ErrResidencyViolation", err)
	}
	if ran {
		t.Fatal("export ran for a non-resident tenant")
	}
}

// TestCustodyIsGatedByResidency is the half of the custody door that must NOT be
// open. core/suspension deliberately does not gate Store.Custody: keeping a
// customer's evidence anchored is a custodial obligation that outlives their
// paying, so checkpointing, the DR chain tip and the key-transition marker keep
// working for a withdrawn tenant.
//
// Residency is the opposite case and the distinction is the whole point. A region
// pin is a legal boundary, not a commercial one, and custody is no excuse to read
// a tenant's ledger from a region that may not serve it. Because the suspension
// guard composes OUTSIDE this one, a custody call descends through here — so if
// this check were dropped, custody would become a way around residency for every
// tenant in the estate, suspended or not.
func TestCustodyIsGatedByResidency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t)
	t.Cleanup(func() { _ = st.Close() })

	euT := createOrg(t, st, "eu-corp", "eu")
	usT := createOrg(t, st, "us-corp", "us")

	reg, err := residency.NewRegistry("eu", []string{"eu", "us"})
	if err != nil {
		t.Fatal(err)
	}
	g := residency.Guard(st, reg, nil)

	// Home-region tenant: custody runs.
	var ran bool
	if err := g.Custody(ctx, euT, func(cs store.CustodyScope) error {
		ran = true
		_, _, e := cs.Audit().Head(ctx)
		return e
	}); err != nil {
		t.Fatalf("custody on a home-region tenant must run: %v", err)
	}
	if !ran {
		t.Fatal("custody did not run the caller's work for a home-region tenant")
	}

	// Cross-region tenant: denied closed, and the work must not have run.
	ran = false
	err = g.Custody(ctx, usT, func(cs store.CustodyScope) error { ran = true; return nil })
	if !errors.Is(err, store.ErrResidencyViolation) {
		t.Fatalf("custody on a cross-region tenant: got %v, want ErrResidencyViolation", err)
	}
	if ran {
		t.Fatal("custody ran the caller's work for a tenant resident in another region")
	}

	// A tenant with no org row here (resident elsewhere) is denied too, never run
	// as though it simply had an empty ledger.
	ran = false
	err = g.Custody(ctx, model.NewTenantID(), func(cs store.CustodyScope) error { ran = true; return nil })
	if !errors.Is(err, store.ErrResidencyViolation) {
		t.Fatalf("custody on a non-resident tenant: got %v, want ErrResidencyViolation", err)
	}
	if ran {
		t.Fatal("custody ran the caller's work for a non-resident tenant")
	}
}
