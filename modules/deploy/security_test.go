// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"net/http"
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestInlineCredentialRejected proves the no-cleartext-secret guarantee: a spec
// with raw credential material (rather than a secret-store reference) is rejected
// at declaration time, and a valid secret_ref is stored as a reference only.
func TestInlineCredentialRejected(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")

	base := func() map[string]any {
		return map[string]any{
			"subject_kind": "agent", "subject_ref": "acme-bot", "name": "billing-agent",
			"environment": "prod", "target": "docker.host/node1", "runtime": "docker",
		}
	}

	// A wiring secret_ref that is raw credential material (not a reference) → 400.
	bad := base()
	bad["spec"] = map[string]any{
		"image": "img:1",
		"wirings": []map[string]any{
			{"resource_kind": "postgres.table", "resource_ref": "public.customers", "mode": "read", "secret_ref": "AKIAIOSFODNN7EXAMPLE"},
		},
	}
	if r := h.do("POST", "/v1/m/deploy/definitions", tok, bad, tenantHdr(tid)); r.code != http.StatusBadRequest {
		t.Fatalf("inline-credential secret_ref = %d %s, want 400", r.code, r.raw)
	}

	// An env_ref carrying an inline password → 400.
	bad2 := base()
	bad2["name"] = "billing-agent-2"
	bad2["spec"] = map[string]any{
		"image":    "img:1",
		"env_refs": []map[string]any{{"name": "DB", "secret_ref": "password=hunter2"}},
	}
	if r := h.do("POST", "/v1/m/deploy/definitions", tok, bad2, tenantHdr(tid)); r.code != http.StatusBadRequest {
		t.Fatalf("inline-credential env_ref = %d %s, want 400", r.code, r.raw)
	}

	// A proper secret-store reference is accepted and stored verbatim as a reference.
	defID := h.createDef(tok, tid, "billing-agent-ok", agentSpec("img:1", "agent:billing"))

	// Apply, then confirm the stored wiring carries the reference, not a secret.
	ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
	h.gate.set(ref, StatusApproved)
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusOK {
		t.Fatalf("apply = %d %s", p2.code, p2.raw)
	}
	wr := h.do("GET", "/v1/m/deploy/wirings?definition_id="+defID, tok, nil, tenantHdr(tid))
	wirings, _ := wr.body["items"].([]any)
	if len(wirings) != 1 {
		t.Fatalf("wirings = %d, want 1 (%s)", len(wirings), wr.raw)
	}
	w := wirings[0].(map[string]any)
	if w["secret_ref"] != "vault:secret/data/pg#dsn" {
		t.Fatalf("stored secret_ref = %v, want the reference", w["secret_ref"])
	}
}

// TestDegradedAttributionMarkedNotFaked proves the honesty rule: when the
// identity binder cannot firmly bind a per-agent identity, the wiring is marked
// degraded (and the published edge is approximate), never faked as firm.
func TestDegradedAttributionMarkedNotFaked(t *testing.T) {
	g := newFakeGate()
	h := newHarnessWith(t, WithApprovalGate(g), WithExecutor(newMockExecutor()), WithIdentityBinder(&fakeBinder{firm: false}))
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
	g.set(ref, StatusApproved)
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusOK {
		t.Fatalf("apply = %d %s", p2.code, p2.raw)
	}

	wr := h.do("GET", "/v1/m/deploy/wirings?definition_id="+defID, tok, nil, tenantHdr(tid))
	wirings, _ := wr.body["items"].([]any)
	if len(wirings) != 1 {
		t.Fatalf("wirings = %d, want 1 (%s)", len(wirings), wr.raw)
	}
	w := wirings[0].(map[string]any)
	if w["attribution"] != attributionDegraded {
		t.Fatalf("attribution = %v, want degraded (binder unavailable)", w["attribution"])
	}

	// The published edge must attribute to the AGENT (not dress the agent name up as
	// a credential identity): OriginKind "agent" + the subject ref + approximate.
	edges := h.capturedEdges(1)
	if len(edges) != 1 {
		t.Fatalf("captured %d edges, want 1", len(edges))
	}
	e := edges[0]
	if e.OriginKind != originKindAgent || e.OriginRef != "acme-bot" {
		t.Fatalf("degraded edge origin = %s/%s, want agent/acme-bot", e.OriginKind, e.OriginRef)
	}
	if e.Source != sdkmodel.SignalPolicy || e.Confidence != sdkmodel.ConfidenceApproximate {
		t.Fatalf("degraded edge = source %s confidence %s, want policy/approximate", e.Source, e.Confidence)
	}
}

// TestMultiTenantIsolation proves a definition created in tenant A is invisible to
// tenant B (the engine pins the data handle to the resolved tenant).
func TestMultiTenantIsolation(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tidA := h.createOrg(root, "acme")
	tidB := h.createOrg(root, "globex")
	tokA := h.roleToken(root, tidA, "a@acme.io", "admin")
	tokB := h.roleToken(root, tidB, "b@globex.io", "admin")

	defID := h.createDef(tokA, tidA, "billing-agent", agentSpec("img:1", "agent:billing"))

	// B cannot see A's definition by id...
	if r := h.do("GET", "/v1/m/deploy/definitions/"+defID, tokB, nil, tenantHdr(tidB)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant get = %d %s, want 404", r.code, r.raw)
	}
	// ...nor in its list.
	r := h.do("GET", "/v1/m/deploy/definitions", tokB, nil, tenantHdr(tidB))
	if items, _ := r.body["items"].([]any); len(items) != 0 {
		t.Fatalf("tenant B sees %d definitions, want 0", len(items))
	}
}

// TestRBACTiers proves the verb-tier split: a viewer cannot declare, an editor
// can declare but cannot apply (the governed mutation), and an admin can apply.
func TestRBACTiers(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	admin := h.roleToken(root, tid, "admin@acme.io", "admin")
	editor := h.roleToken(root, tid, "editor@acme.io", "editor")
	viewer := h.roleToken(root, tid, "viewer@acme.io", "viewer")

	body := map[string]any{
		"subject_kind": "agent", "subject_ref": "acme-bot", "name": "billing-agent",
		"environment": "prod", "target": "docker.host/node1", "runtime": "docker", "spec": agentSpec("img:1", "agent:billing"),
	}

	// viewer cannot declare (write-tier).
	if r := h.do("POST", "/v1/m/deploy/definitions", viewer, body, tenantHdr(tid)); r.code != http.StatusForbidden {
		t.Fatalf("viewer create = %d, want 403", r.code)
	}
	// editor can declare.
	r := h.do("POST", "/v1/m/deploy/definitions", editor, body, tenantHdr(tid))
	if r.code != http.StatusCreated {
		t.Fatalf("editor create = %d %s, want 201", r.code, r.raw)
	}
	defID := r.body["id"].(string)
	// editor CANNOT apply (admin-tier governed mutation).
	if ar := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/apply", editor, map[string]any{}, tenantHdr(tid)); ar.code != http.StatusForbidden {
		t.Fatalf("editor apply = %d %s, want 403", ar.code, ar.raw)
	}
	// admin can drive the apply phase 1.
	if ar := h.applyPhase1(admin, tid, defID); ar.code != http.StatusAccepted {
		t.Fatalf("admin apply phase1 = %d %s, want 202", ar.code, ar.raw)
	}
}

// TestApplyRecordsGovernanceLedger proves the change-management evidence: an
// approved apply records an operation row carrying the approval reference and the
// consumed gate decision (what reads).
func TestApplyRecordsGovernanceLedger(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
	h.gate.set(ref, StatusApproved)
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusOK {
		t.Fatalf("apply = %d %s", p2.code, p2.raw)
	}

	r := h.do("GET", "/v1/m/deploy/operations?definition_id="+defID+"&op=apply&status=applied", tok, nil, tenantHdr(tid))
	items, _ := r.body["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no applied operation recorded (%s)", r.raw)
	}
	op := items[0].(map[string]any)
	if op["approval_ref"] != ref || op["gate_status"] != string(StatusApproved) {
		t.Fatalf("operation ledger missing approval evidence: %+v", op)
	}
}

// TestDeleteDefinitionGuarded proves the control-plane delete refuses to orphan a
// live deployment (it must be retired first) and succeeds once retired.
func TestDeleteDefinitionGuarded(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	// Apply it (now live on the runtime).
	ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
	h.gate.set(ref, StatusApproved)
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusOK {
		t.Fatalf("apply = %d %s", p2.code, p2.raw)
	}

	// Delete is refused while applied.
	if r := h.do("DELETE", "/v1/m/deploy/definitions/"+defID, tok, nil, tenantHdr(tid)); r.code != http.StatusConflict {
		t.Fatalf("delete while applied = %d %s, want 409", r.code, r.raw)
	}

	// Retire (governed), then delete succeeds.
	rref := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/retire", tok, map[string]any{}, tenantHdr(tid)).body["approval_ref"].(string)
	h.gate.set(rref, StatusApproved)
	if rr := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/retire", tok, map[string]any{"approval_ref": rref}, tenantHdr(tid)); rr.code != http.StatusOK {
		t.Fatalf("retire = %d %s", rr.code, rr.raw)
	}
	if r := h.do("DELETE", "/v1/m/deploy/definitions/"+defID, tok, nil, tenantHdr(tid)); r.code != http.StatusNoContent {
		t.Fatalf("delete after retire = %d %s, want 204", r.code, r.raw)
	}
}

// TestApplyFailsClosedWithoutExecutor proves that without a runtime executor,
// plan and apply fail closed (503) rather than pretending to act.
func TestApplyFailsClosedWithoutExecutor(t *testing.T) {
	h := newHarnessWith(t, WithApprovalGate(newFakeGate()), WithIdentityBinder(&fakeBinder{firm: true})) // no executor
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	if r := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/plan", tok, nil, tenantHdr(tid)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("plan without executor = %d %s, want 503", r.code, r.raw)
	}
	if r := h.applyPhase1(tok, tid, defID); r.code != http.StatusServiceUnavailable {
		t.Fatalf("apply without executor = %d %s, want 503", r.code, r.raw)
	}
}
