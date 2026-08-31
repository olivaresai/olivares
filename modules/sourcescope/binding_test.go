// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestCreateGetListBinding exercises the binding write API round-trip.
func TestCreateGetListBinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")

	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "mcp", "source_ref": "github-mcp", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
		"cred_name": "gh", "cred_ref_kind": "vault", "cred_ref": "secret/gh#token", "cred_hint": "ghp_…ab12",
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %s", r.raw)
	}

	g := h.do("GET", "/v1/m/sourcescope/bindings/"+id, admin, nil, tenantHdr(tenant))
	if g.code != http.StatusOK || g.body["source_ref"] != "github-mcp" || g.body["scope_ref"] != "payments" {
		t.Fatalf("get = %d %s", g.code, g.raw)
	}
	// The credential REFERENCE round-trips (value-free: name+kind+locator), never a value.
	if g.body["cred_ref"] != "secret/gh#token" || g.body["cred_name"] != "gh" {
		t.Errorf("cred ref must round-trip as a locator, got %s", g.raw)
	}

	l := h.do("GET", "/v1/m/sourcescope/bindings?source_type=mcp", admin, nil, tenantHdr(tenant))
	if l.code != http.StatusOK || len(items(l)) != 1 {
		t.Fatalf("list = %d %s", l.code, l.raw)
	}
}

// items returns the items slice of a list response.
func items(r resp) []any {
	if r.body == nil {
		return nil
	}
	out, _ := r.body["items"].([]any)
	return out
}

// TestBindingValidation rejects malformed bindings (deny-closed shape checks).
func TestBindingValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"bad source_type", map[string]any{"source_type": "bogus", "source_ref": "x", "scope_tree": "workspace", "scope_ref": "payments"}, 400},
		{"missing source_ref", map[string]any{"source_type": "model", "source_ref": "", "scope_tree": "workspace", "scope_ref": "payments"}, 400},
		{"bad scope_tree", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "galaxy", "scope_ref": "x"}, 400},
		{"agent_group needs ref", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "agent_group", "scope_ref": ""}, 400},
		{"folder needs ref", map[string]any{"source_type": "knowledge", "source_ref": "kb", "scope_tree": "folder", "scope_ref": ""}, 400},
		{"unknown folder id", map[string]any{"source_type": "knowledge", "source_ref": "kb", "scope_tree": "folder", "scope_ref": "res-ghost"}, 400},
		{"unknown workspace slug", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "ghost"}, 400},
		{"inline credential in ref", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "cred_name": "k", "cred_ref_kind": "other", "cred_ref": "https://u:p@host"}, 400},
		{"cred without name", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "cred_ref_kind": "vault", "cred_ref": "secret/x"}, 400},
		// invalid effect + subject trees that require a non-empty scope_ref.
		{"bad effect", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "effect": "deny"}, 400},
		{"session needs ref", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "session", "scope_ref": ""}, 400},
		{"agent needs ref", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "agent", "scope_ref": ""}, 400},
		{"user needs ref", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "user", "scope_ref": ""}, 400},
		{"user_group needs ref", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "user_group", "scope_ref": ""}, 400},
		{"role needs ref", map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "role", "scope_ref": ""}, 400},
	}
	for _, c := range cases {
		if r := h.createBinding(admin, tenant, c.body); r.code != c.want {
			t.Errorf("%s: want %d, got %d %s", c.name, c.want, r.code, r.raw)
		}
	}
}

// TestBindingUniqueConflict: a second binding for the same (source, scope) conflicts.
func TestBindingUniqueConflict(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	body := map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true}
	if r := h.createBinding(admin, tenant, body); r.code != http.StatusCreated {
		t.Fatalf("first create = %d %s", r.code, r.raw)
	}
	if r := h.createBinding(admin, tenant, body); r.code != http.StatusConflict {
		t.Errorf("duplicate (source,scope) must be 409, got %d %s", r.code, r.raw)
	}
}

// TestBindingWriteRequiresWriteTier: a viewer (read tier) may not create a binding.
func TestBindingWriteRequiresWriteTier(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	// principalFor grants a membership; reuse its login by issuing the same flow but we
	// need the TOKEN, so log in directly here.
	if r := h.do("POST", "/v1/users", admin, map[string]any{"email": "v@acme.io", "password": "memberpass1"}, nil); r.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", r.code, r.raw)
	} else if r2 := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": r.body["id"], "tenant": tenant.String(), "role": auth.RoleViewer}, nil); r2.code != http.StatusCreated {
		t.Fatalf("grant = %d %s", r2.code, r2.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "v@acme.io", "password": "memberpass1"}, nil)
	viewerTok := lr.body["token"].(string)

	r := h.createBinding(viewerTok, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true})
	if r.code != http.StatusForbidden {
		t.Errorf("viewer create binding must be 403, got %d %s", r.code, r.raw)
	}
}

// TestAccessMapProjection: creating a binding projects a PERMITTED edge
// (Source=scoped_grant) for each agent in the bound scope, so the access map's drift
// view reflects the configured scope (observe→enforce).
func TestAccessMapProjection(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	payments := h.createWorkspace(tenant, "payments")
	h.createAgent(tenant, "pay-bot", payments)

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-edge", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}

	var found bool
	for _, e := range h.host.edges() {
		if e.Source == sdkmodel.SignalScopedGrant && e.OriginKind == "agent" && e.OriginRef == "pay-bot" && e.ResourceRef == "m-edge" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a scoped_grant permitted edge agent=pay-bot → m-edge, got %d edges", len(h.host.edges()))
	}
}

// TestS355AccessMapProjection: the single-subject axes (session/agent/user) each
// project ONE permitted edge with the matching origin kind; the group/role axes and any
// forbid project NOTHING (member enumeration needs the auth scope; a forbid is not a
// permitted edge) — ADR-0022 §6.
func TestS355AccessMapProjection(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.mustBind(admin, tenant, "m-sess", "session", "sess-42", "allow")
	h.mustBind(admin, tenant, "m-agt", "agent", "agent-42", "allow")
	h.mustBind(admin, tenant, "m-usr", "user", "user-42", "allow")
	h.mustBind(admin, tenant, "m-grp", "user_group", "grp-1", "allow") // deferred → no edge
	h.mustBind(admin, tenant, "m-rol", "role", "member", "allow")      // deferred → no edge
	h.mustBind(admin, tenant, "m-fb", "user", "user-99", "forbid")     // forbid → no edge

	want := map[string][2]string{
		"m-sess": {"session", "sess-42"},
		"m-agt":  {"agent", "agent-42"},
		"m-usr":  {"identity", "user-42"},
	}
	seen := map[string]bool{}
	for _, e := range h.host.edges() {
		if e.Source != sdkmodel.SignalScopedGrant {
			continue
		}
		switch e.ResourceRef {
		case "m-grp", "m-rol", "m-fb":
			t.Errorf("%s must project NO permitted edge, got %s/%s", e.ResourceRef, e.OriginKind, e.OriginRef)
		default:
			if w, ok := want[e.ResourceRef]; ok {
				if e.OriginKind == w[0] && e.OriginRef == w[1] {
					seen[e.ResourceRef] = true
				} else {
					t.Errorf("%s: edge origin %s/%s, want %s/%s", e.ResourceRef, e.OriginKind, e.OriginRef, w[0], w[1])
				}
			}
		}
	}
	for ref := range want {
		if !seen[ref] {
			t.Errorf("missing permitted edge for %s", ref)
		}
	}
}

// TestUpdateAndDeleteBinding: a NON-relaxing update (note only) and a NON-relaxing delete
// (a non-last allow) apply immediately. The relaxing paths (which route through F2
// dual-control) are covered in posture_test.go.
//
// Changed how binding B gets here, and deliberately: the FIRST allow (A) confines the
// source and still applies in the act, but a SECOND allow widens who may reach an
// already-confined source, so it is now proposed and applied by a distinct approver. Taking
// that path here rather than seeding B behind the API keeps the fixture honest — and
// exercises the create leg of applyPosture end to end.
func TestUpdateAndDeleteBinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	h.createWorkspace(tenant, "research")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	// Two allow bindings for one source, so deleting one does not unconfine it.
	a := h.createBinding(admin, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true})
	if a.code != http.StatusCreated {
		t.Fatalf("create A (the first allow confines the source; must apply now) = %d %s", a.code, a.raw)
	}
	idA := a.body["id"].(string)
	idB := h.createBindingApproved(admin, approver, tenant, map[string]any{"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "research", "enabled": true})

	// A NEUTRAL update (note only; access unchanged) applies immediately.
	u := h.do("PUT", "/v1/m/sourcescope/bindings/"+idA, admin, map[string]any{
		"source_type": "ignored", "source_ref": "ignored", // immutable natural key — forced to stored
		"scope_tree": "workspace", "scope_ref": "payments", "enabled": true, "note": "annotated",
	}, tenantHdr(tenant))
	if u.code != http.StatusOK || u.body["note"] != "annotated" || u.body["source_ref"] != "m" {
		t.Fatalf("neutral update = %d %s (source_ref must stay 'm', note must flip)", u.code, u.raw)
	}

	// Deleting a NON-LAST allow (B; A remains) tightens ⇒ applies immediately.
	if d := h.do("DELETE", "/v1/m/sourcescope/bindings/"+idB, admin, nil, tenantHdr(tenant)); d.code != http.StatusNoContent {
		t.Fatalf("delete non-last allow = %d %s", d.code, d.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/bindings/"+idB, admin, nil, tenantHdr(tenant)); g.code != http.StatusNotFound {
		t.Errorf("get after delete must be 404, got %d", g.code)
	}
	_ = model.StatusActive // keep model import used across the test file
}

// --- subject-axis bindings + effect (write-API round-trip; resolver is E2) -----

// TestS355SubjectTreeBindings creates a binding for each of the five subject axes
// (shape-only refs — no store entity is needed at bind time) and asserts the scope_tree,
// scope_ref and defaulted effect round-trip through create + get.
func TestS355SubjectTreeBindings(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	cases := []struct{ tree, ref string }{
		{"session", "sess-abc"},
		{"agent", "agent-x"},
		{"user", "user-123"},
		{"user_group", "grp-eng"},
		{"role", "member"},
	}
	for _, c := range cases {
		r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "model", "source_ref": "m-" + c.tree, "scope_tree": c.tree, "scope_ref": c.ref, "enabled": true,
		})
		if r.code != http.StatusCreated {
			t.Fatalf("%s: create = %d %s", c.tree, r.code, r.raw)
		}
		id := r.body["id"].(string)
		g := h.do("GET", "/v1/m/sourcescope/bindings/"+id, admin, nil, tenantHdr(tenant))
		if g.code != http.StatusOK || g.body["scope_tree"] != c.tree || g.body["scope_ref"] != c.ref {
			t.Fatalf("%s: get = %d %s", c.tree, g.code, g.raw)
		}
		if g.body["effect"] != "allow" {
			t.Errorf("%s: effect must default to allow, got %v", c.tree, g.body["effect"])
		}
	}
}

// TestS355EffectForbidRoundTrips: a forbid binding persists and reads back as "forbid".
func TestS355EffectForbidRoundTrips(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m", "scope_tree": "user_group", "scope_ref": "grp-eng", "effect": "forbid", "enabled": true,
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create forbid = %d %s", r.code, r.raw)
	}
	if r.body["effect"] != "forbid" {
		t.Errorf("create response effect must be forbid, got %v", r.body["effect"])
	}
	g := h.do("GET", "/v1/m/sourcescope/bindings/"+r.body["id"].(string), admin, nil, tenantHdr(tenant))
	if g.body["effect"] != "forbid" {
		t.Errorf("stored effect must be forbid, got %v (%s)", g.body["effect"], g.raw)
	}
}

// TestS355EffectDefaultsAllowBackCompat: a binding created WITHOUT an effect (the pre
// wire shape) reads back as an explicit allow — the expand-only column is back-compat.
func TestS355EffectDefaultsAllowBackCompat(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")

	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if r.body["effect"] != "allow" {
		t.Errorf("effect must default to allow, got %v (%s)", r.body["effect"], r.raw)
	}
}
