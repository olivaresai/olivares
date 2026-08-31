// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// governanceHarness seeds a governed estate (one cost sample per model ref) and
// hands back the harness plus tokens, mirroring the budget-gate test setup.
func governanceHarness(t *testing.T, modelRefs ...string) (*harness, model.TenantID, string, string) {
	t.Helper()
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	costs := make([]sdkmodel.CostSample, 0, len(modelRefs))
	for _, ref := range modelRefs {
		costs = append(costs, sdkmodel.CostSample{
			ProviderRef: "anthropic", ModelRef: ref,
			InputTokens: 10, OutputTokens: 5, CostMicroUSD: 1, OccurredAt: time.Now(),
		})
	}
	src := &fakeSource{costs: costs}
	if err := rt.AddSource(src, sdk.Config{}, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	h.waitModels(tenant, len(modelRefs))
	return h, tenant, editor, viewer
}

// createPolicy creates a routing policy with the given spec fields and returns its id.
func createPolicy(t *testing.T, h *harness, tenant model.TenantID, editor string, spec map[string]any) string {
	t.Helper()
	r := h.do("POST", "/v1/m/models/routing-policies", editor, spec, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create policy = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// TestResolveGovernanceRetiredDeny proves the lifecycle deny on /resolve: a
// RETIRED model (claude-3-5-sonnet, retired 2025-10-28) routes freely without the
// flag (opt-in), is dropped-and-replaced when a permissible candidate exists, and
// fully denies with 403 + the published replacement when nothing survives.
func TestResolveGovernanceRetiredDeny(t *testing.T) {
	h, tenant, editor, viewer := governanceHarness(t, "claude-3-5-sonnet-20241022", "claude-sonnet-4-6")
	resolve := func(id string) resp {
		return h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	}

	// 1. Opt-in: WITHOUT deny_retired the pinned retired model resolves untouched.
	open := createPolicy(t, h, tenant, editor, map[string]any{
		"name": "open", "enabled": true, "strategy": "pinned", "pinned_model": "claude-3-5-sonnet-20241022",
	})
	r := resolve(open)
	if r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("without deny_retired resolve must succeed, got %d %s", r.code, r.raw)
	}
	if primary, _ := r.body["primary"].(map[string]any); primary["model_ref"] != "claude-3-5-sonnet-20241022" {
		t.Fatalf("pinned primary = %v, want the retired model (opt-in deny)", r.body["primary"])
	}

	// 2. deny_retired with a surviving candidate: the retired primary is dropped and
	// the permissible fallback PROMOTED — the request is served, not failed.
	guarded := createPolicy(t, h, tenant, editor, map[string]any{
		"name": "guarded", "enabled": true, "strategy": "pinned",
		"pinned_model": "claude-3-5-sonnet-20241022", "deny_retired": true,
	})
	r = resolve(guarded)
	if r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("deny_retired with a surviving candidate must resolve, got %d %s", r.code, r.raw)
	}
	if primary, _ := r.body["primary"].(map[string]any); primary["model_ref"] != "claude-sonnet-4-6" {
		t.Fatalf("promoted primary = %v, want claude-sonnet-4-6", r.body["primary"])
	}
	if reason, _ := r.body["reason"].(string); !strings.Contains(reason, "model governance filtered") {
		t.Fatalf("promotion must note the governance filter, got reason %q", r.body["reason"])
	}
}

// TestResolveGovernanceFullDeny proves the 403 when EVERY candidate is denied: the
// decision carries governance_deny + the published replacement, hands back no
// target, and stays generic (no internal detail) for the read-tier caller.
func TestResolveGovernanceFullDeny(t *testing.T) {
	h, tenant, editor, viewer := governanceHarness(t, "claude-3-5-sonnet-20241022")
	id := createPolicy(t, h, tenant, editor, map[string]any{
		"name": "no-retired", "enabled": true, "strategy": "cost", "deny_retired": true,
	})
	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusForbidden || r.body["resolved"] != false {
		t.Fatalf("all-denied resolve = %d %s, want 403 resolved=false", r.code, r.raw)
	}
	if r.body["governance_deny"] != "retired" {
		t.Fatalf("governance_deny = %v, want retired", r.body["governance_deny"])
	}
	if r.body["replacement"] != "claude-sonnet-4-6" {
		t.Fatalf("replacement = %v, want the published claude-sonnet-4-6", r.body["replacement"])
	}
	if r.body["primary"] != nil {
		t.Fatalf("a governance-denied resolve must hand back NO target, got %v", r.body["primary"])
	}
	if reason, _ := r.body["reason"].(string); reason != "routing denied: model is retired" {
		t.Fatalf("reason = %q, want the generic lifecycle deny", reason)
	}
}

// TestResolveGovernanceZDRAndAccessTier proves the two DENY-CLOSED dimensions on
// /resolve: require_zdr denies a Covered Model (Fable 5) even though it is current
// and GA; a Glasswing model (Mythos 5) is denied by DEFAULT and routes only when the
// policy enrolls the tier.
func TestResolveGovernanceZDRAndAccessTier(t *testing.T) {
	h, tenant, editor, viewer := governanceHarness(t, "claude-fable-5", "claude-mythos-5")
	resolve := func(id string) resp {
		return h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	}

	// 1. require_zdr: BOTH models are Covered (no ZDR) → full deny, 403, kind zdr.
	zdr := createPolicy(t, h, tenant, editor, map[string]any{
		"name": "zdr-workload", "enabled": true, "strategy": "pinned",
		"pinned_model": "claude-fable-5", "require_zdr": true,
	})
	r := resolve(zdr)
	if r.code != http.StatusForbidden || r.body["governance_deny"] != "zdr" {
		t.Fatalf("ZDR workload over Covered Models = %d %s, want 403 governance_deny=zdr", r.code, r.raw)
	}

	// 2. Glasswing deny-closed by default: pinning Mythos 5 without enrollment drops
	// it; the GA Fable 5 survives and is promoted.
	unenrolled := createPolicy(t, h, tenant, editor, map[string]any{
		"name": "unenrolled", "enabled": true, "strategy": "pinned", "pinned_model": "claude-mythos-5",
	})
	r = resolve(unenrolled)
	if r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("unenrolled resolve must promote the GA candidate, got %d %s", r.code, r.raw)
	}
	if primary, _ := r.body["primary"].(map[string]any); primary["model_ref"] != "claude-fable-5" {
		t.Fatalf("promoted primary = %v, want claude-fable-5 (mythos dropped)", r.body["primary"])
	}

	// 3. Enrolling the tier opens the restricted model.
	enrolled := createPolicy(t, h, tenant, editor, map[string]any{
		"name": "enrolled", "enabled": true, "strategy": "pinned",
		"pinned_model": "claude-mythos-5", "access_tiers": []string{"glasswing"},
	})
	r = resolve(enrolled)
	if r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("enrolled resolve = %d %s, want 200", r.code, r.raw)
	}
	if primary, _ := r.body["primary"].(map[string]any); primary["model_ref"] != "claude-mythos-5" {
		t.Fatalf("enrolled primary = %v, want claude-mythos-5", r.body["primary"])
	}
}

// TestResolveGovernanceEntitlementSuspension proves the external-entitlement
// attestation: an enrolled restricted tier routes with no row or a granted row, but
// a provider-side suspended row denies with governance_deny=entitlement.
func TestResolveGovernanceEntitlementSuspension(t *testing.T) {
	h, tenant, editor, viewer := governanceHarness(t, "claude-mythos-5")
	id := createPolicy(t, h, tenant, editor, map[string]any{
		"name": "glasswing", "enabled": true, "strategy": "pinned",
		"pinned_model": "claude-mythos-5", "access_tiers": []string{"glasswing"},
	})
	resolve := func() resp {
		return h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	}

	// No stored entitlement row preserves the existing enrolled-tier behavior.
	r := resolve()
	if r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("no entitlement row must not change routing, got %d %s", r.code, r.raw)
	}

	// A granted attestation is explicit but still non-narrowing.
	r = h.do("PUT", "/v1/m/models/access-tier-entitlements", editor, map[string]any{
		"tier": "glasswing", "state": "granted", "note": "restored by account team", "as_of": "2026-07-03",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["state"] != "granted" {
		t.Fatalf("grant entitlement = %d %s", r.code, r.raw)
	}
	if r = resolve(); r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("granted entitlement must permit enrolled routing, got %d %s", r.code, r.raw)
	}

	// Suspended narrows the already-enrolled tier and returns no usable target.
	r = h.do("PUT", "/v1/m/models/access-tier-entitlements", editor, map[string]any{
		"tier": "glasswing", "state": "suspended", "note": "provider suspended Glasswing", "as_of": "2026-07-03",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["state"] != "suspended" {
		t.Fatalf("suspend entitlement = %d %s", r.code, r.raw)
	}
	l := h.do("GET", "/v1/m/models/access-tier-entitlements?tier=glasswing", viewer, nil, tenantHdr(tenant))
	if l.code != http.StatusOK || len(items(l)) != 1 {
		t.Fatalf("list entitlement = %d n=%d %s", l.code, len(items(l)), l.raw)
	}
	row, _ := items(l)[0].(map[string]any)
	if row["state"] != "suspended" || row["note"] != "provider suspended Glasswing" || row["as_of"] != "2026-07-03" || row["updated_by"] == "" {
		t.Fatalf("entitlement row = %v, want suspended note/as_of/updated_by", row)
	}
	r = resolve()
	if r.code != http.StatusForbidden || r.body["resolved"] != false || r.body["governance_deny"] != "entitlement" {
		t.Fatalf("suspended entitlement resolve = %d %s, want 403 governance_deny=entitlement", r.code, r.raw)
	}
	if r.body["primary"] != nil {
		t.Fatalf("an entitlement-denied resolve must hand back NO target, got %v", r.body["primary"])
	}
	if reason, _ := r.body["reason"].(string); reason != "routing denied: provider entitlement for the restricted access tier is suspended (operator-attested)" {
		t.Fatalf("reason = %q, want generic entitlement deny", reason)
	}

	// Re-attested granted restores the enrolled-tier behavior without changing policy.
	r = h.do("PUT", "/v1/m/models/access-tier-entitlements", editor, map[string]any{
		"tier": "glasswing", "state": "granted", "note": "restored",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("restore entitlement = %d %s", r.code, r.raw)
	}
	if r = resolve(); r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("restored entitlement must permit routing again, got %d %s", r.code, r.raw)
	}
}

// TestWorkspaceResidencyUpsert proves the PERMITTED-side persistence: geos are
// normalized (lowercase, deduped, sorted), the row upserts in place keyed by
// workspace_ref (a catalog re-sync never duplicates), and writes need the editor
// permission while reads are viewer-tier.
func TestWorkspaceResidencyUpsert(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	r := h.do("PUT", "/v1/m/models/workspace-residency", editor, map[string]any{
		"workspace_ref": "wrkspc_01", "allowed_geos": []string{"US ", " global", "us"},
		"default_geo": "US", "workspace_geo": "us", "as_of": "2026-06-10",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("upsert = %d %s", r.code, r.raw)
	}
	if geos, _ := r.body["allowed_geos"].([]any); len(geos) != 2 || geos[0] != "global" || geos[1] != "us" {
		t.Fatalf("normalized geos = %v, want [global us]", r.body["allowed_geos"])
	}
	if r.body["default_geo"] != "us" {
		t.Fatalf("default_geo = %v, want lowercased us", r.body["default_geo"])
	}

	// Re-sync narrows the workspace to us-only: replaced in place, no duplicate.
	r = h.do("PUT", "/v1/m/models/workspace-residency", editor, map[string]any{
		"workspace_ref": "wrkspc_01", "allowed_geos": []string{"us"},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("re-upsert = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/models/workspace-residency", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("rows after re-upsert = %d, want 1 (upsert, never duplicate)", len(items))
	}
	row, _ := items[0].(map[string]any)
	if geos, _ := row["allowed_geos"].([]any); len(geos) != 1 || geos[0] != "us" {
		t.Fatalf("re-synced geos = %v, want [us]", row["allowed_geos"])
	}

	// Writes are editor-tier: a viewer PUT is rejected by the permission layer.
	r = h.do("PUT", "/v1/m/models/workspace-residency", viewer, map[string]any{
		"workspace_ref": "wrkspc_02", "allowed_geos": []string{"us"},
	}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("viewer upsert = %d, want 403", r.code)
	}
}
