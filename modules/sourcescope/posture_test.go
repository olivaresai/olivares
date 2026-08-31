// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// posture_test.go proves the F2/F5 enforcement-posture dual-control: a RELAXING
// change is not applied by one actor — it becomes a pending request that a SECOND, distinct
// principal must approve (ADR-0022 §5).

// tokenFor creates a user with a tenant role and returns its session token (for a distinct
// approver in the dual-control flow).
func (h *harness) tokenFor(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	if role != "" {
		if m := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": r.body["id"], "tenant": tenant.String(), "role": role}, nil); m.code != http.StatusCreated {
			h.t.Fatalf("grant membership = %d %s", m.code, m.raw)
		}
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	return lr.body["token"].(string)
}

// createBindingApproved POSTs a binding that classifies as relaxing (an allow on an
// already-confined source), has a DISTINCT approver apply it, and returns the created row's
// id. It walks the real two-person path rather than seeding the row behind the API, so a
// fixture built with it also proves the path works.
func (h *harness) createBindingApproved(proposer, approver string, tenant model.TenantID, body map[string]any) string {
	h.t.Helper()
	c := h.createBinding(proposer, tenant, body)
	if c.code != http.StatusAccepted || c.body["op"] != "create" {
		h.t.Fatalf("relaxing create must be 202 pending create, got %d %s", c.code, c.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+c.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK || a.body["status"] != "approved" {
		h.t.Fatalf("approve create = %d %s", a.code, a.raw)
	}
	// The decision response is the REQUEST, not the row it created, so find the row by the
	// scope it was proposed for.
	l := h.do("GET", "/v1/m/sourcescope/bindings?source_type="+body["source_type"].(string)+"&source_ref="+body["source_ref"].(string), proposer, nil, tenantHdr(tenant))
	for _, it := range items(l) {
		b, _ := it.(map[string]any)
		if b["scope_tree"] == body["scope_tree"] && b["scope_ref"] == body["scope_ref"] {
			return b["id"].(string)
		}
	}
	h.t.Fatalf("approved create did not produce a binding for %v/%v: %s", body["scope_tree"], body["scope_ref"], l.raw)
	return ""
}

// --- a scope change on a FORBID, and on an ALLOW across trees, are relaxations -----
//
// These drive the REAL HTTP handler and then the REAL resolver, so each one shows the
// consequence and not just the status code: the actor the write would have un-protected is
// asked, before and after. The final approval step is not decoration — it proves the blocked
// mutation genuinely widened access, so the test cannot pass by the update being a no-op.

// TestS590ShrinkingForbidRequiresDualControl is the defect closed. A standing forbid
// that stays a standing forbid and only changes WHICH population it covers used to apply in
// the act, by one actor — while DELETING that same forbid needed two people. The gate was
// bypassable by editing instead of deleting.
func TestS590ShrinkingForbidRequiresDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspace(tenant, "sales")
	h.createAgent(tenant, "a1", eng) // the actor the forbid protects the source from
	confined := h.principalFor(admin, tenant, "c@acme.io", "")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-shrink", "scope_tree": "workspace", "scope_ref": "eng",
		"effect": "forbid", "enabled": true,
	})
	if c.code != http.StatusCreated {
		t.Fatalf("create forbid = %d %s", c.code, c.raw)
	}
	id := c.body["id"].(string)
	if d := h.resolveAgent(tenant, confined, "a1", "model", "m-shrink"); d.Allowed {
		t.Fatalf("the agent in eng must start DENIED by the forbid, got %+v", d)
	}

	// Move the forbid off eng and onto sales: still a forbid, still enabled, only the
	// population changes — and everyone it stops covering is un-denied.
	up := h.do("PUT", "/v1/m/sourcescope/bindings/"+id, admin, map[string]any{
		"scope_tree": "workspace", "scope_ref": "sales", "effect": "forbid", "enabled": true,
	}, tenantHdr(tenant))
	if up.code != http.StatusAccepted || up.body["op"] != "update" || up.body["status"] != "pending" {
		t.Fatalf("shrinking a forbid must be 202 pending, got %d %s", up.code, up.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+id, admin, nil, tenantHdr(tenant)); g.body["scope_ref"] != "eng" {
		t.Errorf("the forbid must still cover eng until approval, got scope_ref=%v", g.body["scope_ref"])
	}
	if d := h.resolveAgent(tenant, confined, "a1", "model", "m-shrink"); d.Allowed {
		t.Errorf("the protected agent must STILL be denied while the request is pending, got %+v", d)
	}

	// Approved by a second, distinct principal, it applies — and only then does the agent
	// gain access. That is what the single actor was able to do before.
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+up.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if d := h.resolveAgent(tenant, confined, "a1", "model", "m-shrink"); !d.Allowed {
		t.Errorf("after approval the agent must be allowed — otherwise the blocked write widened nothing, got %+v", d)
	}
}

// TestS590AllowMovedAcrossTreesRequiresDualControl is the SIBLING defect, found by inverting
// the classifier to a whitelist. specificityRank orders trees to choose a CREDENTIAL; it is
// not a containment relation, so "workspace:eng → user:U" ranked as a pure narrowing and
// applied in the act — while reaching a user who was out of scope a moment earlier.
func TestS590AllowMovedAcrossTreesRequiresDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	sales := h.createWorkspace(tenant, "sales")
	h.createAgent(tenant, "outsider", sales) // NOT in eng ⇒ out of the allow's scope
	outsider := h.principalFor(admin, tenant, "o@acme.io", "")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-move", "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
	})
	if c.code != http.StatusCreated {
		t.Fatalf("create allow = %d %s", c.code, c.raw)
	}
	id := c.body["id"].(string)
	if d := h.resolveAgent(tenant, outsider, "outsider", "model", "m-move"); d.Allowed {
		t.Fatalf("an agent outside eng must start denied on a confined source, got %+v", d)
	}

	up := h.do("PUT", "/v1/m/sourcescope/bindings/"+id, admin, map[string]any{
		"scope_tree": "user", "scope_ref": outsider.UserID.String(), "effect": "allow", "enabled": true,
	}, tenantHdr(tenant))
	if up.code != http.StatusAccepted || up.body["status"] != "pending" {
		t.Fatalf("moving an allow to a different population must be 202 pending, got %d %s", up.code, up.raw)
	}
	if d := h.resolveAgent(tenant, outsider, "outsider", "model", "m-move"); d.Allowed {
		t.Errorf("the outsider must STILL be denied while the request is pending, got %+v", d)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+up.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if d := h.resolveAgent(tenant, outsider, "outsider", "model", "m-move"); !d.Allowed {
		t.Errorf("after approval the outsider must be allowed — the blocked write did widen access, got %+v", d)
	}
}

// TestS590CreateAllowOnConfinedSourceRequiresDualControl: the THIRD leak of the same class.
// Creation was classified by nothing at all, so the gate on "widen an allow" was avoidable
// by adding a second allow instead of editing the first.
func TestS590CreateAllowOnConfinedSourceRequiresDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	sales := h.createWorkspace(tenant, "sales")
	h.createAgent(tenant, "outsider", sales)
	outsider := h.principalFor(admin, tenant, "o@acme.io", "")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	// The FIRST allow confines the source: the largest tightening in the module, un-gated.
	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-add", "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Fatalf("the first allow must apply immediately, got %d %s", c.code, c.raw)
	}
	if d := h.resolveAgent(tenant, outsider, "outsider", "model", "m-add"); d.Allowed {
		t.Fatalf("the outsider must start denied, got %+v", d)
	}

	// A SECOND allow reaches a population that could not reach the source: gated.
	second := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-add", "scope_tree": "user", "scope_ref": outsider.UserID.String(), "enabled": true,
	})
	if second.code != http.StatusAccepted || second.body["op"] != "create" {
		t.Fatalf("a second allow on a confined source must be 202 pending create, got %d %s", second.code, second.raw)
	}
	l := h.do("GET", "/v1/m/sourcescope/bindings?source_ref=m-add", admin, nil, tenantHdr(tenant))
	if len(items(l)) != 1 {
		t.Errorf("the proposed binding must NOT exist yet, got %d rows: %s", len(items(l)), l.raw)
	}
	if d := h.resolveAgent(tenant, outsider, "outsider", "model", "m-add"); d.Allowed {
		t.Errorf("the outsider must still be denied while the request is pending, got %+v", d)
	}

	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+second.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if l := h.do("GET", "/v1/m/sourcescope/bindings?source_ref=m-add", admin, nil, tenantHdr(tenant)); len(items(l)) != 2 {
		t.Errorf("approval must create the row, got %d rows: %s", len(items(l)), l.raw)
	}
	if d := h.resolveAgent(tenant, outsider, "outsider", "model", "m-add"); !d.Allowed {
		t.Errorf("after approval the outsider must be allowed, got %+v", d)
	}
}

// TestS590OrdinaryPostureWritesStillApply is the other half of the whitelist, and the one a
// gate-everything mutant fails: a classifier that returned "relaxing" for everything would
// satisfy every test above and this one would catch it. Each row is a write that cannot
// widen who may reach the source, and must still be a single-actor write.
func TestS590OrdinaryPostureWritesStillApply(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	h.createWorkspace(tenant, "sales")

	// 1. The first allow on an unbound source confines it.
	first := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-ord", "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
	})
	if first.code != http.StatusCreated {
		t.Fatalf("first allow = %d %s", first.code, first.raw)
	}
	allowID := first.body["id"].(string)

	// 2. Adding a FORBID to that confined source only ever subtracts access.
	fb := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-ord", "scope_tree": "user", "scope_ref": "u-blocked",
		"effect": "forbid", "enabled": true,
	})
	if fb.code != http.StatusCreated {
		t.Fatalf("adding a forbid must apply immediately, got %d %s", fb.code, fb.raw)
	}
	forbidID := fb.body["id"].(string)

	// 3. A note edit on that standing forbid touches no population.
	if u := h.do("PUT", "/v1/m/sourcescope/bindings/"+forbidID, admin, map[string]any{
		"scope_tree": "user", "scope_ref": "u-blocked", "effect": "forbid", "enabled": true, "note": "ticket OPS-1",
	}, tenantHdr(tenant)); u.code != http.StatusOK || u.body["note"] != "ticket OPS-1" {
		t.Errorf("a note edit on a standing forbid must apply immediately, got %d %s", u.code, u.raw)
	}

	// 4. An allow created DISABLED enforces nothing: loadEnabledBindings skips it.
	if p := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-ord", "scope_tree": "workspace", "scope_ref": "sales", "enabled": false,
	}); p.code != http.StatusCreated {
		t.Errorf("a parked allow must apply immediately, got %d %s", p.code, p.raw)
	}

	// 5. Flipping a NON-LAST allow to a forbid: the source keeps its confinement from the
	// other enabled allow, this row loses its grant, and a forbid can only subtract. A second
	// enabled allow has to exist for that to be true, so it is created through the real
	// two-person path (the source is already confined, so creating it is itself gated).
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	h.createBindingApproved(admin, approver, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-ord", "scope_tree": "user", "scope_ref": "u-second", "enabled": true,
	})
	if u := h.do("PUT", "/v1/m/sourcescope/bindings/"+allowID, admin, map[string]any{
		"scope_tree": "workspace", "scope_ref": "eng", "effect": "forbid", "enabled": true,
	}, tenantHdr(tenant)); u.code != http.StatusOK || u.body["effect"] != "forbid" {
		t.Errorf("a NON-LAST allow→forbid must apply immediately, got %d %s", u.code, u.raw)
	}
}

// TestS590LastAllowBecomingForbidRequiresDualControl is the leg the adversarial panel found
// still open after first pass, and it is the same class one more time. An enabled
// allow carries TWO things: its own grant, and the CONFINEMENT that keeps the source from
// being global. The first whitelist reasoned about the row's effect — "a forbid can only
// deny from here" — and forgot the second. Turning the LAST enabled allow into a forbid
// leaves zero enabled allows, and an unconfined source is reachable by EVERYONE.
//
// `enabled:false` and `effect:forbid` reach a bit-identical enforcement state here, so the
// two spellings must classify identically: gating one and waving the other through is the
// original defect wearing different clothes.
func TestS590LastAllowBecomingForbidRequiresDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	insider := h.principalFor(admin, tenant, "i@acme.io", "") // confined: no tenant role
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	// One enabled allow naming somebody else ⇒ the source is confined and the insider is out.
	c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-last", "scope_tree": "user", "scope_ref": "somebody-else", "enabled": true,
	})
	if c.code != http.StatusCreated {
		t.Fatalf("create allow = %d %s", c.code, c.raw)
	}
	id := c.body["id"].(string)
	if d := h.resolveAgent(tenant, insider, "no-agent", "model", "m-last"); d.Allowed {
		t.Fatalf("the insider must start denied on a confined source, got %+v", d)
	}

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"turned into a forbid", map[string]any{"scope_tree": "user", "scope_ref": "somebody-else", "effect": "forbid", "enabled": true}},
		{"turned into a parked forbid", map[string]any{"scope_tree": "user", "scope_ref": "somebody-else", "effect": "forbid", "enabled": false}},
		{"simply disabled", map[string]any{"scope_tree": "user", "scope_ref": "somebody-else", "effect": "allow", "enabled": false}},
	} {
		u := h.do("PUT", "/v1/m/sourcescope/bindings/"+id, admin, tc.body, tenantHdr(tenant))
		if u.code != http.StatusAccepted || u.body["status"] != "pending" {
			t.Errorf("the last enabled allow %s must be 202 pending, got %d %s", tc.name, u.code, u.raw)
			continue
		}
		// Reject it so the next spelling starts from the same state — the point is that all
		// three routes to "zero enabled allows" are gated, not just the one that was.
		if rj := h.do("POST", "/v1/m/sourcescope/posture-requests/"+u.body["id"].(string)+"/reject", approver, nil, tenantHdr(tenant)); rj.code != http.StatusOK {
			t.Fatalf("reject = %d %s", rj.code, rj.raw)
		}
	}
	if d := h.resolveAgent(tenant, insider, "no-agent", "model", "m-last"); d.Allowed {
		t.Errorf("nothing was approved, so the insider must still be denied, got %+v", d)
	}

	// Approving one of them proves the blocked write really did make the source global.
	u := h.do("PUT", "/v1/m/sourcescope/bindings/"+id, admin, map[string]any{
		"scope_tree": "user", "scope_ref": "somebody-else", "effect": "forbid", "enabled": true,
	}, tenantHdr(tenant))
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+u.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if d := h.resolveAgent(tenant, insider, "no-agent", "model", "m-last"); !d.Allowed {
		t.Errorf("after approval the source is unconfined and global — the insider must be allowed, got %+v", d)
	}
}

// TestS590ConfinementSignalCountsOnlyEnabledAllows pins BOTH terms of the signal
// classifyCreate decides on. The panel measured that neither was covered: making
// createPreflight count disabled rows, or count forbids as allows, left the suite green. A
// source is confined only by an ENABLED, ALLOW binding — so on a source carrying only a
// parked allow, or only forbids, the next allow is still the FIRST one and applies in the act.
// Get either term wrong and bringing a source under governance starts demanding two people.
func TestS590ConfinementSignalCountsOnlyEnabledAllows(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	h.createWorkspace(tenant, "sales")

	// A source whose only allow is PARKED is not confined.
	if p := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-parked", "scope_tree": "workspace", "scope_ref": "eng", "enabled": false,
	}); p.code != http.StatusCreated {
		t.Fatalf("parked allow = %d %s", p.code, p.raw)
	}
	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-parked", "scope_tree": "workspace", "scope_ref": "sales", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Errorf("a disabled allow does not confine, so this is the FIRST allow: want 201, got %d %s", c.code, c.raw)
	}

	// A FORBID-only source is not confined either (ADR-0022 §2: global minus the forbidden).
	if f := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-fbonly", "scope_tree": "user", "scope_ref": "u-x",
		"effect": "forbid", "enabled": true,
	}); f.code != http.StatusCreated {
		t.Fatalf("forbid = %d %s", f.code, f.raw)
	}
	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-fbonly", "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Errorf("a forbid does not confine, so this is the FIRST allow: want 201, got %d %s", c.code, c.raw)
	}

	// And the other side of the same signal: DELETING that first allow, now the only enabled
	// one, unconfines the source and IS gated. The panel measured this leg untested.
	l := h.do("GET", "/v1/m/sourcescope/bindings?source_ref=m-fbonly&scope_tree=workspace", admin, nil, tenantHdr(tenant))
	id := items(l)[0].(map[string]any)["id"].(string)
	if d := h.do("DELETE", "/v1/m/sourcescope/bindings/"+id, admin, nil, tenantHdr(tenant)); d.code != http.StatusAccepted {
		t.Errorf("deleting the LAST enabled allow must be 202 pending, got %d %s", d.code, d.raw)
	}
}

// TestS590GatedCreateStillRefusesAnUnknownScope: a proposal is only worth queueing if
// approving it can succeed. The scope is resolved BEFORE classification, so an unknown
// workspace is deny-closed at PROPOSE time (400) rather than becoming a 202 that the approver
// meets as a 409, with the request stuck pending and no route to withdraw it.
func TestS590GatedCreateStillRefusesAnUnknownScope(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")

	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-ghost", "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Fatalf("first allow = %d %s", c.code, c.raw)
	}
	// The source is now confined, so this create WOULD be gated — but its scope does not exist.
	c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-ghost", "scope_tree": "workspace", "scope_ref": "ghost", "enabled": true,
	})
	if c.code != http.StatusBadRequest {
		t.Errorf("an unknown workspace must be 400 even on the gated path, got %d %s", c.code, c.raw)
	}
	if l := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant)); len(items(l)) != 0 {
		t.Errorf("no unappliable request may be parked in the reviewer queue, got %s", l.raw)
	}
}

// TestS355RelaxingDeleteRequiresDualControl: deleting a forbid is a relaxation — it is not
// applied, it becomes a pending request that a DISTINCT approver must approve.
func TestS355RelaxingDeleteRequiresDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	// A forbid binding, then delete it (a relaxation: a restriction removed).
	c := h.createBinding(admin, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "user", "scope_ref": "user-x", "effect": "forbid", "enabled": true})
	if c.code != http.StatusCreated {
		t.Fatalf("create forbid = %d %s", c.code, c.raw)
	}
	bindingID := c.body["id"].(string)

	del := h.do("DELETE", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted || del.body["status"] != "pending" || del.body["op"] != "delete" {
		t.Fatalf("relaxing delete must be 202 pending, got %d %s", del.code, del.raw)
	}
	reqID := del.body["id"].(string)

	// The binding still exists — the relaxation has NOT been applied.
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("binding must survive until approval, got %d", g.code)
	}
	// The proposer cannot self-approve (dual-control).
	if s := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", admin, nil, tenantHdr(tenant)); s.code != http.StatusConflict {
		t.Fatalf("self-approval must be 409, got %d %s", s.code, s.raw)
	}
	// A DISTINCT approver applies it.
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK || a.body["status"] != "approved" {
		t.Fatalf("approval by a distinct principal must be 200 approved, got %d %s", a.code, a.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); g.code != http.StatusNotFound {
		t.Errorf("binding must be deleted after approval, got %d", g.code)
	}
}

// TestS355RelaxingUpdateForbidToAllowDualControl: flipping a forbid to an allow is a
// relaxation applied only on approval.
func TestS355RelaxingUpdateForbidToAllowDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	c := h.createBinding(admin, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "user", "scope_ref": "user-x", "effect": "forbid", "enabled": true})
	if c.code != http.StatusCreated {
		t.Fatalf("create forbid = %d %s", c.code, c.raw)
	}
	bindingID := c.body["id"].(string)

	up := h.do("PUT", "/v1/m/sourcescope/bindings/"+bindingID, admin, map[string]any{
		"scope_tree": "user", "scope_ref": "user-x", "effect": "allow", "enabled": true,
	}, tenantHdr(tenant))
	if up.code != http.StatusAccepted || up.body["op"] != "update" {
		t.Fatalf("forbid→allow must be 202 pending, got %d %s", up.code, up.raw)
	}
	reqID := up.body["id"].(string)
	// Not yet applied: still a forbid.
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); g.body["effect"] != "forbid" {
		t.Fatalf("effect must remain forbid until approval, got %v", g.body["effect"])
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); g.body["effect"] != "allow" {
		t.Errorf("effect must be allow after approval, got %v", g.body["effect"])
	}
}

// TestS355DisableScopingDualControl: the one-way disable-scoping op is dual-controlled;
// on approval every enabled binding for the source is disabled (the source goes global).
func TestS355DisableScopingDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	c := h.createBinding(admin, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true})
	if c.code != http.StatusCreated {
		t.Fatalf("create = %d %s", c.code, c.raw)
	}
	bindingID := c.body["id"].(string)

	req := h.do("POST", "/v1/m/sourcescope/sources/disable-scoping", admin, map[string]any{"source_type": "model", "source_ref": "m"}, tenantHdr(tenant))
	if req.code != http.StatusAccepted || req.body["op"] != "disable_scoping" {
		t.Fatalf("disable-scoping must be 202 pending, got %d %s", req.code, req.raw)
	}
	reqID := req.body["id"].(string)
	// Not applied yet: the binding is still enabled.
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); g.body["enabled"] != true {
		t.Fatalf("binding must stay enabled until approval, got %v", g.body["enabled"])
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); g.body["enabled"] != false {
		t.Errorf("binding must be disabled after approval (source global), got %v", g.body["enabled"])
	}
}

// TestS355PostureRejectLeavesUnchanged: rejecting a pending relaxation applies nothing.
func TestS355PostureRejectLeavesUnchanged(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	c := h.createBinding(admin, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "user", "scope_ref": "user-x", "effect": "forbid", "enabled": true})
	if c.code != http.StatusCreated {
		t.Fatalf("create forbid = %d %s", c.code, c.raw)
	}
	bindingID := c.body["id"].(string)
	del := h.do("DELETE", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant))
	reqID := del.body["id"].(string)

	if rj := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/reject", approver, nil, tenantHdr(tenant)); rj.code != http.StatusOK || rj.body["status"] != "rejected" {
		t.Fatalf("reject = %d %s", rj.code, rj.raw)
	}
	// The forbid still stands.
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); g.code != http.StatusOK || g.body["effect"] != "forbid" {
		t.Errorf("binding must be unchanged after reject, got %d %v", g.code, g.body["effect"])
	}
	// A re-decision of a non-pending request is refused.
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusConflict {
		t.Errorf("deciding an already-rejected request must be 409, got %d", a.code)
	}
}

// TestS355PostureRequestListed: a pending relaxation surfaces in the review queue.
func TestS355PostureRequestListed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	c := h.createBinding(admin, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "user", "scope_ref": "user-x", "effect": "forbid", "enabled": true})
	bindingID := c.body["id"].(string)
	if d := h.do("DELETE", "/v1/m/sourcescope/bindings/"+bindingID, admin, nil, tenantHdr(tenant)); d.code != http.StatusAccepted {
		t.Fatalf("relaxing delete = %d %s", d.code, d.raw)
	}
	l := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant))
	if l.code != http.StatusOK || len(items(l)) != 1 {
		t.Fatalf("pending queue must list 1 request, got %d %s", l.code, l.raw)
	}
}

func TestS358GuardPublicOnlyRequiresDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	req := h.do("PUT", "/v1/m/sourcescope/guard-postures", admin, map[string]any{
		"source_type": "knowledge", "source_ref": "handbook", "profile": "public_only",
		"reason": "break-glass public-only demo",
	}, tenantHdr(tenant))
	if req.code != http.StatusAccepted || req.body["status"] != "pending" || req.body["op"] != "public_only" {
		t.Fatalf("public_only downgrade must be 202 pending, got %d %s", req.code, req.raw)
	}
	reqID := req.body["id"].(string)

	// Not applied until the second, distinct approver.
	if l := h.do("GET", "/v1/m/sourcescope/guard-postures?source_type=knowledge&source_ref=handbook", admin, nil, tenantHdr(tenant)); l.code != http.StatusOK || len(items(l)) != 0 {
		t.Fatalf("guard posture must not apply before approval, got %d %s", l.code, l.raw)
	}
	if s := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", admin, nil, tenantHdr(tenant)); s.code != http.StatusConflict {
		t.Fatalf("self-approval must be rejected, got %d %s", s.code, s.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK || a.body["status"] != "approved" {
		t.Fatalf("approval by a distinct principal must apply, got %d %s", a.code, a.raw)
	}

	l := h.do("GET", "/v1/m/sourcescope/guard-postures?source_type=knowledge&source_ref=handbook", admin, nil, tenantHdr(tenant))
	if l.code != http.StatusOK || len(items(l)) != 1 {
		t.Fatalf("approved guard posture must be listed, got %d %s", l.code, l.raw)
	}
	gp := items(l)[0].(map[string]any)
	if gp["profile"] != "public_only" || gp["source_ref"] != "handbook" {
		t.Fatalf("guard posture = %v, want public_only handbook", gp)
	}
	if !h.auditHas(tenant, "sourcescope.posture.approve", "public_only") {
		t.Fatal("approved public_only posture must be recorded in the audit ledger")
	}
}

func TestS358GuardReEnableIsImmediateAndAudited(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	req := h.do("PUT", "/v1/m/sourcescope/guard-postures", admin, map[string]any{
		"source_type": "knowledge", "source_ref": "handbook", "profile": "public_only",
	}, tenantHdr(tenant))
	reqID := req.body["id"].(string)
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+reqID+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}

	tighten := h.do("PUT", "/v1/m/sourcescope/guard-postures", admin, map[string]any{
		"source_type": "knowledge", "source_ref": "handbook", "profile": "acl_aware",
		"reason": "restore ACL-aware guard",
	}, tenantHdr(tenant))
	if tighten.code != http.StatusOK || tighten.body["profile"] != "acl_aware" {
		t.Fatalf("acl_aware tightening must be immediate, got %d %s", tighten.code, tighten.raw)
	}
	if l := h.do("GET", "/v1/m/sourcescope/guard-postures?source_type=knowledge&source_ref=handbook", admin, nil, tenantHdr(tenant)); l.code != http.StatusOK || len(items(l)) != 0 {
		t.Fatalf("acl_aware posture should remove the downgrade row, got %d %s", l.code, l.raw)
	}
	if !h.auditHas(tenant, "sourcescope.guard_posture.tighten", "acl_aware") {
		t.Fatal("acl_aware tightening must be recorded in the audit ledger")
	}
}

func (h *harness) auditHas(tenant model.TenantID, action, profile string) bool {
	h.t.Helper()
	found := false
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		cw, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
				if ev.Action != action {
					return nil
				}
				if profile == "" ||
					fmt.Sprint(ev.Meta["guard_profile"]) == profile ||
					fmt.Sprint(ev.Meta["profile"]) == profile ||
					fmt.Sprint(ev.Meta["op"]) == profile {
					found = true
				}
				return nil
			})
		}
		return cw.WalkCanonical(context.Background(), 1, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			if ev.Action != action {
				return nil
			}
			var meta map[string]any
			_ = json.Unmarshal([]byte(metaCanonical), &meta)
			if profile == "" ||
				fmt.Sprint(meta["guard_profile"]) == profile ||
				fmt.Sprint(meta["profile"]) == profile ||
				fmt.Sprint(meta["op"]) == profile {
				found = true
			}
			return nil
		})
	}); err != nil {
		h.t.Fatalf("audit walk: %v", err)
	}
	return found
}
