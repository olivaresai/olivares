// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"net/http"
	"strings"
	"testing"
)

// TestRunScoresAndProducesFindings runs a battery against an authorized target with
// a sandbox that makes the agent vulnerable to the injection family: the scorecard
// reflects the failures, the failed probes become findings, and the run/results are
// persisted.
func TestRunScoresAndProducesFindings(t *testing.T) {
	h := newHarness(t, fakeSandbox{comply: map[string]bool{familyInjection: true}})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	targetID := h.registerAuthorizedTarget(admin, tenant, "agent-under-test")

	r := h.do("POST", "/v1/m/redteam/runs", admin, map[string]any{"target_ref": targetID, "suite": "all"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("launch run = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "completed" {
		t.Fatalf("status = %v, want completed", r.body["status"])
	}
	total := intOf(r.body["total"])
	failed := intOf(r.body["failed"])
	passed := intOf(r.body["passed"])
	wantFailed := len(injectionProbes())
	if total <= 0 {
		t.Fatalf("total = %d, want > 0", total)
	}
	if failed != wantFailed {
		t.Fatalf("failed = %d, want %d (the injection family)", failed, wantFailed)
	}
	if passed != total-failed {
		t.Fatalf("passed = %d, want %d", passed, total-failed)
	}
	score := r.body["score"].(float64)
	if score <= 0 || score >= 100 {
		t.Fatalf("score = %v, want strictly between 0 and 100", score)
	}
	owasp, _ := r.body["owasp_failures"].(map[string]any)
	if len(owasp) == 0 {
		t.Fatalf("expected OWASP failure coverage; got none")
	}

	runID := r.body["id"].(string)

	// Per-probe results: injection probes complied, others refused.
	rr := h.do("GET", "/v1/m/redteam/runs/"+runID+"/results", admin, nil, tenantHdr(tenant))
	if rr.code != http.StatusOK {
		t.Fatalf("results = %d %s", rr.code, rr.raw)
	}
	items, _ := rr.body["items"].([]any)
	if len(items) != total {
		t.Fatalf("results = %d, want %d", len(items), total)
	}
	for _, it := range items {
		m := it.(map[string]any)
		if m["family"] == familyInjection && m["outcome"] != string(OutcomeComplied) {
			t.Fatalf("injection probe %v outcome = %v, want complied", m["probe_id"], m["outcome"])
		}
	}

	// Failures became persisted findings.
	h.waitFindings()
	finds := h.coreFindings(tenant, findingKindRedteam)
	if len(finds) != wantFailed {
		t.Fatalf("persisted redteam findings = %d, want %d", len(finds), wantFailed)
	}
	for _, f := range finds {
		if len(f.DetailHash) == 0 {
			t.Fatalf("finding missing detail_hash")
		}
	}
}

// TestRunRequiresAuthorizedTarget verifies the dual-use RED LINE (docs/SECURITY-HARDENING.md): a run
// against an unauthorized or unknown target is refused.
func TestRunRequiresAuthorizedTarget(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Registered but NOT authorized. (Seed the agent so the ownership gate passes;
	// this test isolates the CONSENT gate, not ownership.)
	h.seedAgent(tenant, "a1")
	reg := h.do("POST", "/v1/m/redteam/targets", admin, map[string]any{"agent_ref": "a1", "name": "a1"}, tenantHdr(tenant))
	if reg.code != http.StatusCreated {
		t.Fatalf("register = %d %s", reg.code, reg.raw)
	}
	id := reg.body["id"].(string)
	if r := h.do("POST", "/v1/m/redteam/runs", admin, map[string]any{"target_ref": id}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("run on unauthorized target = %d, want 403", r.code)
	}

	// Unknown target.
	if r := h.do("POST", "/v1/m/redteam/runs", admin, map[string]any{"target_ref": "00000000-0000-0000-0000-000000000000"}, tenantHdr(tenant)); r.code == http.StatusCreated {
		t.Fatalf("run on unknown target = 201, want refused")
	}
}

// TestRegisterRejectsUnknownAgent is the OWNERSHIP gate (docs/SECURITY-HARDENING.md, R4): a target
// can only be registered for an agent that exists in THIS tenant's inventory.
func TestRegisterRejectsUnknownAgent(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// No agent seeded → registration is refused (422), not silently accepted.
	if r := h.do("POST", "/v1/m/redteam/targets", admin, map[string]any{"agent_ref": "ghost", "name": "ghost"}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("register unknown agent = %d, want 422 (%s)", r.code, r.raw)
	}
	// Seed the agent → registration now succeeds.
	h.seedAgent(tenant, "ghost")
	if r := h.do("POST", "/v1/m/redteam/targets", admin, map[string]any{"agent_ref": "ghost", "name": "ghost"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("register owned agent = %d, want 201 (%s)", r.code, r.raw)
	}
}

// TestRegisterRejectsCrossTenantAgent proves the ownership gate is tenant-scoped:
// an agent owned by tenant A cannot be registered as a target in tenant B (the
// tenant-pinned Scope makes the cross-tenant ref simply not resolve).
func TestRegisterRejectsCrossTenantAgent(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	h.seedAgent(tenantA, "shared-ref")

	if r := h.do("POST", "/v1/m/redteam/targets", admin, map[string]any{"agent_ref": "shared-ref", "name": "x"}, tenantHdr(tenantB)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-tenant register = %d, want 422 (B must not see A's agent) (%s)", r.code, r.raw)
	}
}

// TestLaunchRefusedWhenAgentRemoved proves the launch-time ownership re-check (R4):
// a target whose agent is removed from inventory after registration cannot be run
// against, and no run is recorded.
func TestLaunchRefusedWhenAgentRemoved(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agentID := h.seedAgent(tenant, "transient")

	reg := h.do("POST", "/v1/m/redteam/targets", admin, map[string]any{"agent_ref": "transient", "name": "transient"}, tenantHdr(tenant))
	if reg.code != http.StatusCreated {
		t.Fatalf("register = %d %s", reg.code, reg.raw)
	}
	id := reg.body["id"].(string)
	if ar := h.do("POST", "/v1/m/redteam/targets/"+id+"/authorize", admin, map[string]any{"authorized": true}, tenantHdr(tenant)); ar.code != http.StatusOK {
		t.Fatalf("authorize = %d %s", ar.code, ar.raw)
	}

	h.deleteAgent(tenant, agentID)

	if r := h.do("POST", "/v1/m/redteam/runs", admin, map[string]any{"target_ref": id}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("launch after agent removed = %d, want 403 (%s)", r.code, r.raw)
	}
}

// TestDegradedWithoutSandbox verifies that without an sandbox a run is honestly
// reported as degraded (every probe skipped), never silently scored as a pass.
func TestDegradedWithoutSandbox(t *testing.T) {
	h := newHarness(t, nil) // default offline sandbox
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	targetID := h.registerAuthorizedTarget(admin, tenant, "agent-x")

	r := h.do("POST", "/v1/m/redteam/runs", admin, map[string]any{"target_ref": targetID}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("run = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "degraded" {
		t.Fatalf("status = %v, want degraded (no sandbox)", r.body["status"])
	}
	if intOf(r.body["skipped"]) != intOf(r.body["total"]) {
		t.Fatalf("expected all probes skipped; skipped=%v total=%v", r.body["skipped"], r.body["total"])
	}
	if intOf(r.body["passed"]) != 0 {
		t.Fatalf("expected 0 passed (skipped is not a pass); got %v", r.body["passed"])
	}
}

// TestConsentAndLaunchAreAdminTier verifies the privileged actions are admin-tier: a
// viewer cannot register/authorize a target or launch a run, but can read the catalog.
func TestConsentAndLaunchAreAdminTier(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	if r := h.do("POST", "/v1/m/redteam/targets", viewer, map[string]any{"agent_ref": "a", "name": "a"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer register = %d, want 403", r.code)
	}
	id := h.registerAuthorizedTarget(admin, tenant, "a")
	if r := h.do("POST", "/v1/m/redteam/runs", viewer, map[string]any{"target_ref": id}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer launch = %d, want 403", r.code)
	}
	if r := h.do("GET", "/v1/m/redteam/catalog", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer catalog = %d, want 200", r.code)
	}
}

// TestCatalogIsTaxonomyNotPayloads verifies the catalog exposes the test taxonomy +
// OWASP/ATLAS coverage but NOT the weaponized payloads (docs/SECURITY-HARDENING.md).
func TestCatalogIsTaxonomyNotPayloads(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/m/redteam/catalog", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	if intOf(r.body["total"]) <= 0 {
		t.Fatalf("catalog total = %v, want > 0", r.body["total"])
	}
	if owasp, _ := r.body["owasp_covered"].(map[string]any); len(owasp) == 0 {
		t.Fatalf("expected OWASP coverage in the catalog")
	}
	// No adversarial payload should be returned (the catalog is metadata only).
	for _, payload := range []string{"rm -rf", "DROP TABLE", "169.254.169.254", "TOOLPOISON", "PWNED-INJECTION"} {
		if strings.Contains(r.raw, payload) {
			t.Fatalf("catalog leaked a weaponized payload fragment: %q", payload)
		}
	}
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
