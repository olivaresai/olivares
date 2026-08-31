// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// assignmentposture_test.go proves the half of the ADR-0022 §5 dual-control: the
// ASSIGNMENT surface decides access exactly as bindings do (ConnectorAssigned is the
// deny-closed gate for every unconfined source, resolver.go:257-264) and, until none
// of its three writers was classified by anything.
//
// The tests below assert the RESOLVER's answer wherever a decision moves, not only the HTTP
// status. A 202 that still let the workspace through would be a gate in name only.

// assignHarness builds a tenant with a proposer (the setup admin) and a DISTINCT approver
// holding the admin-tier posture permission — the two people every test here needs.
func assignHarness(t *testing.T) (*harness, string, string, model.TenantID) {
	t.Helper()
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", "admin")
	return h, admin, approver, tenant
}

// createAssignmentOK POSTs an assignment expected to apply immediately, returning its id.
func (h *harness) createAssignmentOK(token string, tenant model.TenantID, connector, ws string, enabled bool) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/sourcescope/assignments", token, map[string]any{
		"connector_name": connector, "workspace_ref": ws, "enabled": enabled,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create assignment %s→%s = %d, want 201: %s", connector, ws, r.code, r.raw)
	}
	return r.body["id"].(string)
}

// --- E-1: the three writers, each gated by what it actually does to visibility ---------

// TestAssignmentDeleteOfLastRowIsDualControlled is the headline case. Deleting the last
// assignment row does not shrink the connector's audience — it flips the connector back to
// visible from EVERY workspace (assignment.go:291-293). That is the same relaxation
// handleDisableScoping always dual-controls, and it used to be a 204 with a single actor.
func TestAssignmentDeleteOfLastRowIsDualControlled(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	wsEng := h.createWorkspace(tenant, "engineering")
	wsMkt := h.createWorkspace(tenant, "marketing")
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	_ = wsEng
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	id := h.createAssignmentOK(admin, tenant, "github", "engineering", true)

	// Marketing is denied: the connector is assigned, and not to them.
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("before the delete, mkt must be denied, got %+v %v", d, err)
	}

	// The single-actor delete no longer applies: it is proposed.
	del := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted || del.body["op"] != "assignment_delete" {
		t.Fatalf("last-row delete = %d, want 202 assignment_delete: %s", del.code, del.raw)
	}

	// THE POINT: the row is still there and marketing is still denied. A gate that returned
	// 202 and deleted anyway would pass a status-only assertion.
	if g := h.do("GET", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("the proposed delete must not have removed the row, get = %d %s", g.code, g.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("a PENDING delete must not open the connector, got %+v %v", d, err)
	}

	// The proposer cannot self-approve.
	if s := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/approve", admin, nil, tenantHdr(tenant)); s.code != http.StatusConflict {
		t.Fatalf("self-approval must be refused, got %d %s", s.code, s.raw)
	}

	// A DISTINCT approver applies it, and only THEN does the connector go global.
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK || a.body["status"] != "approved" {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant)); g.code != http.StatusNotFound {
		t.Fatalf("after approval the row must be gone, get = %d %s", g.code, g.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || !d.Allowed {
		t.Fatalf("after approval the connector is unassigned and global, got %+v %v", d, err)
	}
}

// TestAssignmentDeleteOfLastDISABLEDRowIsDualControlled is the case a classifier copied from
// the binding surface would wave through, and it is the widest write here.
//
// classifyDelete discards a disabled binding outright (`if !deleted.Enabled { return false }`)
// because a disabled row enforces nothing. The assignment surface does NOT work that way:
// ConnectorAssigned tests `len(allAssign) == 0` BEFORE filtering on enabled, so a connector
// whose only row is disabled is not global — it is denied everywhere — and deleting that row
// opens it to every workspace.
func TestAssignmentDeleteOfLastDISABLEDRowIsDualControlled(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	wsMkt := h.createWorkspace(tenant, "marketing")
	h.createWorkspace(tenant, "engineering")
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	// One row, DISABLED. The connector is denied everywhere — including to its own workspace.
	id := h.createAssignmentOK(admin, tenant, "github", "engineering", false)
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("a connector with one disabled row is denied everywhere, got %+v %v", d, err)
	}

	del := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted {
		t.Fatalf("deleting the last row (disabled) = %d, want 202: %s", del.code, del.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("a PENDING delete must not open the connector, got %+v %v", d, err)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || !d.Allowed {
		t.Fatalf("after approval the connector is unassigned and global, got %+v %v", d, err)
	}
}

// TestAssignmentFirstRowConfinesImmediately pins the deliberate NON-gate: bringing a
// connector under governance must never need two people, or the safe move is the expensive
// one. It is the assignment twin of "the first allow confines the source".
func TestAssignmentFirstRowConfinesImmediately(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	wsMkt := h.createWorkspace(tenant, "marketing")
	h.createWorkspace(tenant, "engineering")
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || !d.Allowed {
		t.Fatalf("an unassigned connector is global, got %+v %v", d, err)
	}
	h.createAssignmentOK(admin, tenant, "github", "engineering", true) // 201, one actor
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("the first assignment must confine immediately, got %+v %v", d, err)
	}
	if l := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant)); len(items(l)) != 0 {
		t.Fatalf("a tightening must not queue an approval: %s", l.raw)
	}
}

// TestAssignmentCreateOnAssignedConnectorIsDualControlled: an enabled row added to an
// already-assigned connector admits a workspace that could not reach it a moment earlier.
func TestAssignmentCreateOnAssignedConnectorIsDualControlled(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	wsMkt := h.createWorkspace(tenant, "marketing")
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	h.createAssignmentOK(admin, tenant, "github", "engineering", true)

	c := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "marketing", "enabled": true,
	}, tenantHdr(tenant))
	if c.code != http.StatusAccepted || c.body["op"] != "assignment_create" {
		t.Fatalf("second assignment = %d, want 202: %s", c.code, c.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("a PENDING create must not admit marketing, got %+v %v", d, err)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+c.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || !d.Allowed {
		t.Fatalf("after approval marketing must resolve, got %+v %v", d, err)
	}
}

// TestAssignmentEnablingAParkedRowIsDualControlled covers the update writer: the row exists
// and is disabled, so it grants nothing; flipping `enabled` admits its workspace.
func TestAssignmentEnablingAParkedRowIsDualControlled(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	wsMkt := h.createWorkspace(tenant, "marketing")
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	h.createAssignmentOK(admin, tenant, "github", "engineering", true)
	// A parked row for marketing. The create is not a relaxation (it grants nothing), so it
	// applies with one actor — and it must not admit marketing.
	parked := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "marketing", "enabled": false,
	}, tenantHdr(tenant))
	if parked.code != http.StatusCreated {
		t.Fatalf("a disabled assignment grants nothing and applies now, got %d %s", parked.code, parked.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("a parked row must not admit marketing, got %+v %v", d, err)
	}

	u := h.do("PUT", "/v1/m/sourcescope/assignments/"+parked.body["id"].(string), admin, map[string]any{
		"enabled": true,
	}, tenantHdr(tenant))
	if u.code != http.StatusAccepted || u.body["op"] != "assignment_update" {
		t.Fatalf("enabling a parked row = %d, want 202: %s", u.code, u.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("a PENDING enable must not admit marketing, got %+v %v", d, err)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+u.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || !d.Allowed {
		t.Fatalf("after approval marketing must resolve, got %+v %v", d, err)
	}
}

// TestAssignmentTighteningsApplyImmediately pins the other side of the whitelist: the writes
// that provably cannot widen still take one actor. A gate that queues everything is as
// useless as one that queues nothing — it just fails in the direction nobody measures.
func TestAssignmentTighteningsApplyImmediately(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")

	engID := h.createAssignmentOK(admin, tenant, "github", "engineering", true)
	mktID := h.createAssignmentOK(admin, tenant, "github", "marketing", false) // parked: no grant

	// Disabling an enabled row removes a workspace from the audience.
	if u := h.do("PUT", "/v1/m/sourcescope/assignments/"+engID, admin, map[string]any{
		"enabled": false, "note": "paused",
	}, tenantHdr(tenant)); u.code != http.StatusOK {
		t.Fatalf("disabling an assignment is a tightening, want 200, got %d %s", u.code, u.raw)
	}
	// A note edit on a row whose enabled state does not move touches no decision.
	if u := h.do("PUT", "/v1/m/sourcescope/assignments/"+mktID, admin, map[string]any{
		"enabled": false, "note": "still parked", "mode": "r",
	}, tenantHdr(tenant)); u.code != http.StatusOK {
		t.Fatalf("a note/mode edit is neutral, want 200, got %d %s", u.code, u.raw)
	}
	// Deleting a NON-last row leaves the connector confined.
	if d := h.do("DELETE", "/v1/m/sourcescope/assignments/"+mktID, admin, nil, tenantHdr(tenant)); d.code != http.StatusNoContent {
		t.Fatalf("a non-last delete is a tightening, want 204, got %d %s", d.code, d.raw)
	}
	if l := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant)); len(items(l)) != 0 {
		t.Fatalf("no tightening may queue an approval: %s", l.raw)
	}
}

// TestAssignmentNaturalKeyIsImmutable pins the premise classifyAssignmentUpdate leans on:
// the (connector, workspace) pair cannot move, so an update can only ever change `enabled`,
// `mode` and `note`. If this ever stops holding, the classifier's move branch — unreachable
// today, kept deliberately — becomes the thing that catches it.
func TestAssignmentNaturalKeyIsImmutable(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")
	id := h.createAssignmentOK(admin, tenant, "github", "engineering", true)

	u := h.do("PUT", "/v1/m/sourcescope/assignments/"+id, admin, map[string]any{
		"connector_name": "gitlab", "workspace_ref": "marketing", "enabled": true,
	}, tenantHdr(tenant))
	if u.code != http.StatusOK {
		t.Fatalf("update = %d %s", u.code, u.raw)
	}
	if u.body["connector_name"] != "github" || u.body["workspace_ref"] != "engineering" {
		t.Fatalf("the natural key must be forced back to the stored row, got %s", u.raw)
	}
}

// --- E-2: the first allow on an ASSIGNED source switches the assignment gate off --------

// TestFirstAllowOnAssignedConnectorIsDualControlled reproduces the escalation named and
// could not close: the first allow binding is exempt from dual-control because "before it,
// the source was global" — but ConnectorAssigned is only consulted while the source has NO
// allow binding, so on an ASSIGNED connector that first allow switches the assignment gate
// OFF and can admit a workspace the assignment set denies.
func TestFirstAllowOnAssignedConnectorIsDualControlled(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	wsEng := h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "sales")
	agentEng := h.createAgent(tenant, "eng-bot", wsEng)
	h.createSession(tenant, "eng-session", agentEng.ID, wsEng)
	pEng := h.principalFor(admin, tenant, "eng@acme.io", "")

	// The connector is assigned to SALES only. Engineering is denied.
	h.createAssignmentOK(admin, tenant, "github", "sales", true)
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("engineering must be denied by the assignment gate, got %+v %v", d, err)
	}

	// The FIRST allow binding for engineering. Before this was a 201 by one actor and
	// engineering resolved immediately.
	c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "github",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	})
	if c.code != http.StatusAccepted || c.body["op"] != "create" {
		t.Fatalf("the first allow on an assigned source = %d, want 202 create: %s", c.code, c.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("a PENDING first allow must not admit engineering, got %+v %v", d, err)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+c.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "github"); err != nil || !d.Allowed {
		t.Fatalf("after approval engineering must resolve, got %+v %v", d, err)
	}
}

// TestFirstAllowOnUNassignedSourceStillAppliesImmediately is the boundary of E-2, and the
// reason the fix is keyed on "this ref has assignment rows" rather than on the source kind:
// with no assignment rows there is no second gate to switch off, so the first allow is the
// pure tightening deliberately left ungated.
func TestFirstAllowOnUNassignedSourceStillAppliesImmediately(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")

	c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "unassigned-source",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	})
	if c.code != http.StatusCreated {
		t.Fatalf("the first allow on an UNassigned source = %d, want 201: %s", c.code, c.raw)
	}
	if l := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant)); len(items(l)) != 0 {
		t.Fatalf("confining an unassigned source must not queue an approval: %s", l.raw)
	}
}

// TestAssignmentGateKeysOnRefNotSourceType pins C-2: there is no `connector` source_type
// (validSourceTypes is mcp|model|provider|knowledge|data) and the resolver hands
// ConnectorAssigned the source REF alone. So the E-2 gate must fire for ANY source type
// whose ref carries assignment rows — keying it on a type would silently exempt four of the
// five kinds.
func TestAssignmentGateKeysOnRefNotSourceType(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "sales-only")
	h.createAssignmentOK(admin, tenant, "shared-ref", "sales-only", true)

	for _, st := range []string{"mcp", "model", "provider", "knowledge", "data"} {
		t.Run(st, func(t *testing.T) {
			c := h.createBinding(admin, tenant, map[string]any{
				"source_type": st, "source_ref": "shared-ref",
				"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
			})
			if c.code != http.StatusAccepted {
				t.Fatalf("source_type %s: first allow on an assigned ref = %d, want 202: %s", st, c.code, c.raw)
			}
		})
	}
}

// --- the wire shape of an assignment proposal ------------------------------------------

// TestAssignmentProposalDoesNotDecodeAsABinding is the E-4-flavored defect this change had
// to avoid creating: both surfaces store their proposal in one JSON column, and assignmentDTO
// and bindingDTO share the `enabled` and `note` tags. Decoding an assignment proposal into a
// bindingDTO does not fail, it SUCCEEDS — and shows the approver a binding that was never
// proposed. The reviewer must see the assignment.
func TestAssignmentProposalDoesNotDecodeAsABinding(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")
	h.createAssignmentOK(admin, tenant, "github", "engineering", true)

	c := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "marketing", "enabled": true, "mode": "r",
	}, tenantHdr(tenant))
	if c.code != http.StatusAccepted {
		t.Fatalf("create = %d %s", c.code, c.raw)
	}
	if _, present := c.body["proposed"]; present {
		t.Errorf("an assignment proposal must not surface as a binding `proposed`: %s", c.raw)
	}
	pa, ok := c.body["proposed_assignment"].(map[string]any)
	if !ok {
		t.Fatalf("no proposed_assignment on the request: %s", c.raw)
	}
	if pa["connector_name"] != "github" || pa["workspace_ref"] != "marketing" || pa["mode"] != "r" {
		t.Errorf("the proposal must say what was actually proposed, got %v", pa)
	}
	// And it survives a re-read (the stored column, not just the in-memory response).
	g := h.do("GET", "/v1/m/sourcescope/posture-requests/"+c.body["id"].(string), admin, nil, tenantHdr(tenant))
	if g.code != http.StatusOK {
		t.Fatalf("get request = %d %s", g.code, g.raw)
	}
	if pg, ok := g.body["proposed_assignment"].(map[string]any); !ok || pg["workspace_ref"] != "marketing" {
		t.Errorf("the STORED proposal must decode as an assignment, got %s", g.raw)
	}
}

// TestAssignmentDuplicateIsRefusedBeforeItIsProposed: a proposal the unique index will reject
// is one the approver cannot rescue, so the 409 must land on the PROPOSER. This is the
// ordering handleCreateBinding documents, applied to the second surface.
func TestAssignmentDuplicateIsRefusedBeforeItIsProposed(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")
	h.createAssignmentOK(admin, tenant, "github", "engineering", true)
	h.createAssignmentOK(admin, tenant, "gitlab", "marketing", true)

	// (github, engineering) already exists: 409, and NOT a 202 that would sit pending.
	d := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "engineering", "enabled": true,
	}, tenantHdr(tenant))
	if d.code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409: %s", d.code, d.raw)
	}
	if l := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant)); len(items(l)) != 0 {
		t.Fatalf("a refused create must leave no pending request: %s", l.raw)
	}
}

// TestAssignmentUnknownWorkspaceIsRefusedBeforeItIsProposed: same ordering rule for the other
// thing that can refuse the write.
func TestAssignmentUnknownWorkspaceIsRefusedBeforeItIsProposed(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createAssignmentOK(admin, tenant, "github", "engineering", true)

	d := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "ghost", "enabled": true,
	}, tenantHdr(tenant))
	if d.code != http.StatusBadRequest {
		t.Fatalf("unknown workspace = %d, want 400: %s", d.code, d.raw)
	}
	if l := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant)); len(items(l)) != 0 {
		t.Fatalf("a refused create must leave no pending request: %s", l.raw)
	}
}

// TestAssignmentRejectedProposalChangesNothing: the reject leg, on the widest op.
func TestAssignmentRejectedProposalChangesNothing(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	wsMkt := h.createWorkspace(tenant, "marketing")
	h.createWorkspace(tenant, "engineering")
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	id := h.createAssignmentOK(admin, tenant, "github", "engineering", true)
	del := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}
	if rj := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/reject", approver, nil, tenantHdr(tenant)); rj.code != http.StatusOK || rj.body["status"] != "rejected" {
		t.Fatalf("reject = %d %s", rj.code, rj.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("a rejected delete must leave the row, got %d %s", g.code, g.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("nothing was approved, so the connector must still be confined, got %+v %v", d, err)
	}
}

// TestDisableScopingCopyMatchesWhatItActuallyDoes pins the sentence an APPROVER reads against
// the behavior, not against intent. "The source becomes global" is true for an unbound ref
// and FALSE for one carrying assignment rows: disabling the bindings unconfines the source,
// which hands it back to ConnectorAssigned rather than opening it. A two-person control whose
// copy overstates the change is teaching approvers that the text is decoration.
func TestDisableScopingCopyMatchesWhatItActuallyDoes(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	wsMkt := h.createWorkspace(tenant, "marketing")
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	h.createAssignmentOK(admin, tenant, "github", "engineering", true)
	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "github",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	}); c.code != http.StatusAccepted { // the first allow on an ASSIGNED ref is gated (E-2)
		t.Fatalf("first allow on an assigned ref = %d, want 202: %s", c.code, c.raw)
	} else if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+c.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}

	ds := h.do("POST", "/v1/m/sourcescope/sources/disable-scoping", admin, map[string]any{
		"source_type": "data", "source_ref": "github",
	}, tenantHdr(tenant))
	if ds.code != http.StatusAccepted {
		t.Fatalf("disable-scoping = %d %s", ds.code, ds.raw)
	}
	reason, _ := ds.body["reason"].(string)
	if !strings.Contains(reason, "UNCONFINED, not global") {
		t.Errorf("the approver must be told the source falls back to the assignment gate, got %q", reason)
	}

	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+ds.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	// And the behavior the corrected sentence describes: still denied, by the OTHER gate.
	d, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github")
	if err != nil || d.Allowed {
		t.Fatalf("marketing must still be denied by the assignment gate, got %+v %v", d, err)
	}
	if !strings.Contains(d.Reason, "not assigned") {
		t.Errorf("the denial must come from the assignment gate, got %q", d.Reason)
	}
}
