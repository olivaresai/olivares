// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// approvepremise_test.go covers the two defects in the APPROVAL path:
//
//	E-6 — applyPosture applied a stored proposal to whatever the row had become, without
//	      re-checking that the state it was classified against was still there.
//	E-4 — an approved change recorded that it was approved, and not what it became.

// auditActions walks the tenant's sealed ledger and returns the Action of every event.
//
// It reads Actions and TargetIDs only. model.AuditEvent.Meta is deliberately nil on EVERY
// read path (core/model/audit.go:41-45 — the stored canonical string is authoritative and
// the decoder refuses to re-parse it), so no in-process test can assert what a Meta payload
// contains. That is a real limit on what the E-4 test below proves, and it is stated rather
// than papered over: the assertion is that the binding/assignment event EXISTS for an
// approved change, which is exactly what was missing, and its payload is the one the
// immediate path already writes through the same auditBinding/auditAssignment helper.
func (h *harness) auditActions(tenant model.TenantID) []string {
	h.t.Helper()
	var out []string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			out = append(out, ev.Action)
			return nil
		})
	}); err != nil {
		h.t.Fatalf("walk ledger: %v", err)
	}
	return out
}

func countAction(actions []string, want string) int {
	n := 0
	for _, a := range actions {
		if a == want {
			n++
		}
	}
	return n
}

// --- E-6: the premise the approver was shown must still hold -----------------------------

// TestApprovingAnUpdateCannotResurrectAnInterveningForbid is the sharp end of E-6, and it is
// the defect reached through the approval door rather than through a classifier.
//
// A relaxing update sits pending. While it waits, a SECOND actor turns the same binding into
// a FORBID — which classifyUpdate correctly applies immediately, because on a source with
// other enabled allows a forbid can only subtract. Then the approval lands, and the stored
// proposal still says effect:allow: it overwrites the forbid. A standing restriction is
// removed by one actor's write plus an approval that was never asked about it.
func TestApprovingAnUpdateCannotResurrectAnInterveningForbid(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	wsEng := h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "sales")
	agentEng := h.createAgent(tenant, "eng-bot", wsEng)
	h.addAgentToGroup(tenant, agentEng.ID, "core", wsEng)
	h.createSession(tenant, "eng-session", agentEng.ID, wsEng)
	pEng := h.principalFor(admin, tenant, "eng@acme.io", "")

	// Two enabled allows, so neither is "the last" and a forbid on one is a tightening.
	first := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	})
	if first.code != http.StatusCreated {
		t.Fatalf("first allow = %d %s", first.code, first.raw)
	}
	engID := first.body["id"].(string)
	h.createBindingApproved(admin, approver, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "agent_group", "scope_ref": "core", "enabled": true,
	})

	// (1) Propose moving the engineering allow to sales. Relaxing: 202.
	move := h.do("PUT", "/v1/m/sourcescope/bindings/"+engID, admin, map[string]any{
		"scope_tree": "workspace", "scope_ref": "sales", "effect": "allow", "enabled": true,
	}, tenantHdr(tenant))
	if move.code != http.StatusAccepted {
		t.Fatalf("scope move = %d, want 202: %s", move.code, move.raw)
	}

	// (2) While it waits, the row becomes a FORBID over engineering. A tightening: applies.
	forbid := h.do("PUT", "/v1/m/sourcescope/bindings/"+engID, approver, map[string]any{
		"scope_tree": "workspace", "scope_ref": "engineering", "effect": "forbid", "enabled": true,
	}, tenantHdr(tenant))
	if forbid.code != http.StatusOK {
		t.Fatalf("turning the row into a forbid is a tightening, want 200: %d %s", forbid.code, forbid.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "repo"); err != nil || d.Allowed {
		t.Fatalf("the forbid must deny engineering, got %+v %v", d, err)
	}

	// (3) The approval must REFUSE: the posture it was classified against is gone.
	a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+move.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant))
	if a.code != http.StatusConflict {
		t.Fatalf("approving against a moved premise = %d, want 409: %s", a.code, a.raw)
	}

	// (4) And the restriction is still standing. This is the assertion that matters: a 409
	// with the forbid already overwritten would be a gate that reports rather than stops.
	g := h.do("GET", "/v1/m/sourcescope/bindings/"+engID, admin, nil, tenantHdr(tenant))
	if g.code != http.StatusOK || g.body["effect"] != "forbid" || g.body["scope_ref"] != "engineering" {
		t.Fatalf("the intervening forbid must survive the refused approval, got %d %s", g.code, g.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "repo"); err != nil || d.Allowed {
		t.Fatalf("engineering must still be denied after the refused approval, got %+v %v", d, err)
	}
}

// TestApprovingACreateRefusesADuplicateThatAppearedMeanwhile is E-6 AS BRIEFED, and it is a
// REGRESSION test, not a reproduction — measured, and the brief has this one wrong.
//
// The brief's sequence (propose a create; a forbid takes the same (tree, ref) without a gate;
// approve) does not apply anything to a state nobody proposed: the store's unique index
// refuses the insert and the approval fails. Run against the pre-fix commit this test PASSES,
// while the other four in this file fail. So the create leg was already closed — by the
// index, opaquely, but closed.
//
// What changed is where the refusal is decided (a named conflict from the preflight, before
// the insert) and what it costs to be wrong. What was NOT closed is the UPDATE leg above and
// the assignment-delete leg below, which applied silently. Kept because the behavior it pins
// — 409, and the intervening forbid still standing afterwards — is exactly what must not
// regress when someone later "simplifies" the preflight away.
func TestApprovingACreateRefusesADuplicateThatAppearedMeanwhile(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "sales")

	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Fatalf("first allow = %d %s", c.code, c.raw)
	}

	// A second allow on a confined source: relaxing, 202.
	pending := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "workspace", "scope_ref": "sales", "enabled": true,
	})
	if pending.code != http.StatusAccepted {
		t.Fatalf("second allow = %d, want 202: %s", pending.code, pending.raw)
	}

	// A forbid at the SAME (tree, ref) is a tightening and applies with one actor.
	if f := h.createBinding(approver, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "workspace", "scope_ref": "sales", "effect": "forbid", "enabled": true,
	}); f.code != http.StatusCreated {
		t.Fatalf("forbid create = %d, want 201: %s", f.code, f.raw)
	}

	a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+pending.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant))
	if a.code != http.StatusConflict {
		t.Fatalf("approving onto an occupied scope = %d, want 409: %s", a.code, a.raw)
	}
	// The forbid is untouched, and no allow was created beside it.
	l := h.do("GET", "/v1/m/sourcescope/bindings?source_type=data&source_ref=repo", admin, nil, tenantHdr(tenant))
	sales := 0
	for _, it := range items(l) {
		b := it.(map[string]any)
		if b["scope_ref"] == "sales" {
			sales++
			if b["effect"] != "forbid" {
				t.Errorf("the sales row must still be the forbid, got %v", b)
			}
		}
	}
	if sales != 1 {
		t.Fatalf("want exactly one sales row, got %d: %s", sales, l.raw)
	}
}

// TestApprovingAnAssignmentDeleteRefusesAMovedPremise: the same premise check on the second
// surface. A last-row delete is proposed; another row appears while it waits, so the delete
// is no longer the global flip that was approved-of.
func TestApprovingAnAssignmentDeleteRefusesAMovedPremise(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")
	wsMkt := h.createWorkspace(tenant, "ops")
	agentOps := h.createAgent(tenant, "ops-bot", wsMkt)
	h.createSession(tenant, "ops-session", agentOps.ID, wsMkt)
	pOps := h.principalFor(admin, tenant, "ops@acme.io", "")

	id := h.createAssignmentOK(admin, tenant, "github", "engineering", true)
	del := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted {
		t.Fatalf("last-row delete = %d, want 202: %s", del.code, del.raw)
	}

	// A parked row for marketing appears: not a relaxation, applies with one actor. Now the
	// pending delete is no longer the LAST row, so it is an ordinary tightening — not the
	// change the approver was asked about.
	if c := h.do("POST", "/v1/m/sourcescope/assignments", approver, map[string]any{
		"connector_name": "github", "workspace_ref": "marketing", "enabled": false,
	}, tenantHdr(tenant)); c.code != http.StatusCreated {
		t.Fatalf("parked row = %d, want 201: %s", c.code, c.raw)
	}

	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusConflict {
		t.Fatalf("approving against a moved premise = %d, want 409: %s", a.code, a.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("the refused approval must leave the row, got %d %s", g.code, g.raw)
	}
	// The connector is still confined, so ops is still denied.
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pOps, "ops-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("the connector must still be confined, got %+v %v", d, err)
	}
}

// --- E-4: an approval that does not say what it approved is not evidence -----------------

// TestApprovedChangesRecordTheResultingState pins that an APPROVED change writes the same
// binding/assignment event the immediate path writes. Before auditBinding was reachable
// only from binding.go — that is, from exactly the writes that did NOT need a second person —
// so the two-person path, the one whose whole purpose is evidence, left the thinnest trail.
//
// What this proves and what it does not: it proves the event EXISTS with the right action for
// each approved op, which is what was missing. It cannot inspect the event's Meta, because
// Meta is nil on every read path by construction (see auditActions above).
func TestApprovedChangesRecordTheResultingState(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")

	// Approved binding CREATE.
	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Fatalf("first allow = %d %s", c.code, c.raw)
	}
	before := countAction(h.auditActions(tenant), "sourcescope.binding.create")
	bindID := h.createBindingApproved(admin, approver, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "workspace", "scope_ref": "marketing", "enabled": true,
	})
	if got := countAction(h.auditActions(tenant), "sourcescope.binding.create"); got != before+1 {
		t.Errorf("an approved create must record the resulting binding: sourcescope.binding.create %d → %d, want +1", before, got)
	}

	// Approved binding DELETE (of a non-last... here it IS the last allow after we delete
	// one, so route the widest one): delete the marketing allow while engineering remains,
	// which is ordinary; instead delete BOTH to reach the gated one.
	if d := h.do("DELETE", "/v1/m/sourcescope/bindings/"+bindID, admin, nil, tenantHdr(tenant)); d.code != http.StatusNoContent {
		t.Fatalf("non-last delete = %d, want 204: %s", d.code, d.raw)
	}
	l := h.do("GET", "/v1/m/sourcescope/bindings?source_type=data&source_ref=repo", admin, nil, tenantHdr(tenant))
	lastID := ""
	for _, it := range items(l) {
		lastID = it.(map[string]any)["id"].(string)
	}
	beforeDel := countAction(h.auditActions(tenant), "sourcescope.binding.delete")
	gated := h.do("DELETE", "/v1/m/sourcescope/bindings/"+lastID, admin, nil, tenantHdr(tenant))
	if gated.code != http.StatusAccepted {
		t.Fatalf("last-allow delete = %d, want 202: %s", gated.code, gated.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+gated.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve delete = %d %s", a.code, a.raw)
	}
	if got := countAction(h.auditActions(tenant), "sourcescope.binding.delete"); got != beforeDel+1 {
		t.Errorf("an approved delete must record what it removed: sourcescope.binding.delete %d → %d, want +1", beforeDel, got)
	}

	// Approved ASSIGNMENT create.
	h.createAssignmentOK(admin, tenant, "github", "engineering", true)
	beforeAssign := countAction(h.auditActions(tenant), "sourcescope.connector_assignment.create")
	ac := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "marketing", "enabled": true,
	}, tenantHdr(tenant))
	if ac.code != http.StatusAccepted {
		t.Fatalf("second assignment = %d, want 202: %s", ac.code, ac.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+ac.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve assignment = %d %s", a.code, a.raw)
	}
	if got := countAction(h.auditActions(tenant), "sourcescope.connector_assignment.create"); got != beforeAssign+1 {
		t.Errorf("an approved assignment create must record the resulting row: %d → %d, want +1", beforeAssign, got)
	}
}

// TestApprovedDisableScopingRecordsEveryRowItTurnedOff: "scoping was disabled" is not a
// state; which bindings ended up off is. One event per row.
func TestApprovedDisableScopingRecordsEveryRowItTurnedOff(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	wsEng := h.createWorkspace(tenant, "engineering")
	agentEng := h.createAgent(tenant, "eng-bot", wsEng)
	h.addAgentToGroup(tenant, agentEng.ID, "core", wsEng)
	h.createWorkspace(tenant, "marketing")

	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Fatalf("first allow = %d %s", c.code, c.raw)
	}
	h.createBindingApproved(admin, approver, tenant, map[string]any{
		"source_type": "data", "source_ref": "repo",
		"scope_tree": "agent_group", "scope_ref": "core", "enabled": true,
	})

	before := countAction(h.auditActions(tenant), "sourcescope.binding.update")
	ds := h.do("POST", "/v1/m/sourcescope/sources/disable-scoping", admin, map[string]any{
		"source_type": "data", "source_ref": "repo",
	}, tenantHdr(tenant))
	if ds.code != http.StatusAccepted {
		t.Fatalf("disable-scoping = %d, want 202: %s", ds.code, ds.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+ds.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if got := countAction(h.auditActions(tenant), "sourcescope.binding.update"); got != before+2 {
		t.Errorf("disable-scoping turned off 2 bindings: sourcescope.binding.update %d → %d, want +2", before, got)
	}
}
