// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/modules/governance"
)

// One unreachable identity source must not take the tenant's other sources down with it.
//
// THE PRODUCT RULE THIS ENFORCES, because the HTTP codes alone do not say it: closing an
// entitlement seam on a commercial connector used to REMOVE capability from a customer who
// paid. A lapsed license on one connector would 502 POST /roster/sync, and with it every
// open-core source that was answering perfectly. The enterprise conjur connector documents
// that trade explicitly — its Snapshot is deliberately ungated and a test pins the reason.
// With failures accumulated, gating that Snapshot costs one line of the report instead of
// the whole tenant's roster.
//
// The module's own seam path (Sync) ALREADY accumulated and continued. Only the route did
// not, so this is one module holding two different policies for the same event, and these
// tests pin the one it already believed.

// failingProvider is a GraphProvider whose Snapshot always fails, standing in for an
// unreachable directory or a connector refused by an entitlement gate.
type failingProvider struct{}

func (*failingProvider) Snapshot(context.Context) (identitysource.Graph, error) {
	return identitysource.Graph{}, errors.New("directory unreachable: dial tcp 10.0.0.9:636: connection refused")
}

func TestRosterSyncSurvivesOneFailingProvider(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// The failing one is FIRST on purpose: the old code returned on the first failure, so a
	// working provider behind it was never reached. Ordering it second would pass either way.
	h.gov.UseRosterProviders([]governance.RosterBinding{
		{Provider: &failingProvider{}, TenantRef: tenant.String()},
		{Provider: &fakeProvider{graph: sampleGraph()}, TenantRef: tenant.String()},
	})

	r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("partial failure = %d, want 200 (the surviving source did real work): %s", r.code, r.raw)
	}
	if got := r.body["sources"].(float64); got != 1 {
		t.Fatalf("sources = %v, want 1 (only the healthy provider answered): %s", got, r.raw)
	}
	// The work of the surviving source actually happened — not an empty 200.
	if r.body["identities"].(float64) != 2 || r.body["collections"].(float64) != 1 {
		t.Fatalf("the healthy provider's reconciliation is missing: %s", r.raw)
	}
	// And the failure is REPORTED. Surviving quietly would be the other way to get this
	// wrong: a caller reading `sources` would see a smaller number with no reason for it.
	failed, ok := r.body["providers_failed"].([]any)
	if !ok || len(failed) != 1 {
		t.Fatalf("providers_failed missing or wrong length: %s", r.raw)
	}
	entry := failed[0].(map[string]any)
	if entry["provider"] != "*governance_test.failingProvider" {
		t.Fatalf("provider = %v, want the failing connector's type: %s", entry["provider"], r.raw)
	}
	// The disclosure posture is unchanged: the report says THAT it failed, never the error
	// text, which can carry a host, a DN or a token fragment. The detail stays in the log.
	if reason, _ := entry["reason"].(string); reason != "snapshot failed" {
		t.Fatalf("reason = %q, want the fixed string: %s", reason, r.raw)
	}
	for _, k := range []string{"10.0.0.9", "connection refused", "dial tcp"} {
		if bodyContains(r.raw, k) {
			t.Fatalf("the response leaks connector error detail (%q): %s", k, r.raw)
		}
	}
}

// ACUMULA, no se queda con el último — un mutante que el contraste encontró y la batería no
// mataba: sustituir el append por una asignación pasaba TODO modules/governance, porque ningún
// caso tenía DOS fuentes caídas a la vez. Con una sola, acumular y sobrescribir se leen igual.
func TestRosterSyncAccumulatesEveryFailureNotJustTheLast(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.gov.UseRosterProviders([]governance.RosterBinding{
		{Provider: &failingProvider{}, TenantRef: tenant.String()},
		{Provider: &failingProvider{}, TenantRef: tenant.String()},
		{Provider: &fakeProvider{graph: sampleGraph()}, TenantRef: tenant.String()},
	})

	// Two down, one healthy: 200 (the healthy one did real work) and BOTH failures listed.
	r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("two down + one healthy = %d, want 200: %s", r.code, r.raw)
	}
	failed, _ := r.body["providers_failed"].([]any)
	if len(failed) != 2 {
		t.Fatalf("providers_failed has %d entries, want 2 — keeping only the last failure reads the same with one", len(failed))
	}
	if got := r.body["providers_configured"].(float64); got != 3 {
		t.Fatalf("providers_configured = %v, want 3 (the providers of THIS tenant)", got)
	}
}

func TestRosterSyncTotalFailureIsStill502(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.gov.UseRosterProviders([]governance.RosterBinding{
		{Provider: &failingProvider{}, TenantRef: tenant.String()},
		{Provider: &failingProvider{}, TenantRef: tenant.String()},
	})

	// Every source down is NOT a 200 with zeroes. That would report "nothing changed" for
	// what is really "I could not look" — the confusion this repository keeps removing.
	r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusBadGateway {
		t.Fatalf("total failure = %d, want 502: %s", r.code, r.raw)
	}
}

func TestRosterSyncWithNoProvidersIsNot502(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// No providers bound to this tenant at all. "Every source failed" and "there are no
	// sources" must not collapse into one answer, which is why the route counts providers
	// matched to the tenant separately from providers that answered.
	h.gov.UseRosterProviders(nil)

	r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("no providers = %d, want 200 with a note: %s", r.code, r.raw)
	}
	if note, _ := r.body["note"].(string); note != "no identity providers configured for this tenant" {
		t.Fatalf("note = %q, want the no-providers note: %s", note, r.raw)
	}
}

func TestRosterSyncTotalFailureNeverClaimsNoProvidersConfigured(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.gov.UseRosterProviders([]governance.RosterBinding{
		{Provider: &failingProvider{}, TenantRef: tenant.String()},
	})

	// FOUND BY A MUTANT, not by review. Removing the total-failure 502 made the body say
	// "no identity providers configured for this tenant" WHILE providers_failed listed the
	// one that failed — absence and failure collapsed into the same sentence. The 502 hid
	// it, so the falsehood was one reordering away from being served. The note is now
	// guarded on providers MATCHED, and this case fails if that guard goes back to
	// counting providers that ANSWERED.
	r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusBadGateway {
		t.Fatalf("total failure = %d, want 502: %s", r.code, r.raw)
	}
	if bodyContains(r.raw, "no identity providers configured") {
		t.Fatalf("the body claims no providers were configured while one FAILED: %s", r.raw)
	}
}

func TestRosterSyncHealthyOnlyReportsNoFailures(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.gov.UseRosterProviders([]governance.RosterBinding{
		{Provider: &fakeProvider{graph: sampleGraph()}, TenantRef: tenant.String()},
	})

	// The not-firing direction: a clean run must not grow a providers_failed key at all
	// (omitempty), or every consumer learns to ignore a field that is usually empty.
	r := h.do("POST", govPath+"/roster/sync", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("healthy sync = %d: %s", r.code, r.raw)
	}
	if _, present := r.body["providers_failed"]; present {
		t.Fatalf("providers_failed present on a clean run: %s", r.raw)
	}
}
