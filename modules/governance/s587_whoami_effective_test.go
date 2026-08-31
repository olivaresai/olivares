// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
)

// whoamiGrant returns the calling principal's grant for tenant as whoami reports it.
func whoamiGrant(t *testing.T, h *harness, token, tenant string) map[string]any {
	t.Helper()
	r := h.do("GET", "/v1/auth/whoami", token, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("whoami = %d %s", r.code, r.raw)
	}
	grants, ok := r.body["grants"].([]any)
	if !ok || len(grants) == 0 {
		t.Fatalf("whoami carried no grants: %s", r.raw)
	}
	for _, g := range grants {
		m, ok := g.(map[string]any)
		if ok && m["tenant"] == tenant {
			return m
		}
	}
	t.Fatalf("whoami has no grant for tenant %s: %s", tenant, r.raw)
	return nil
}

// permSet reads the effective permission set out of a whoami grant.
func permSet(t *testing.T, grant map[string]any) map[string]bool {
	t.Helper()
	raw, ok := grant["permissions"].([]any)
	if !ok {
		t.Fatalf("grant carries no `permissions` array: %v", grant)
	}
	out := make(map[string]bool, len(raw))
	for _, p := range raw {
		s, ok := p.(string)
		if !ok {
			t.Fatalf("permission %v is not a string", p)
		}
		out[s] = true
	}
	return out
}

// TestWhoamiCarriesTheEffectivePermissionSet is the contract, end to end against
// the real engine: whoami hands the console a per-tenant permission set, and the set
// AGREES with the answer the request path gives — a permission in the set reaches its
// route, one absent from it 403s.
func TestWhoamiCarriesTheEffectivePermissionSet(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	_, viewer := h.roleUser(admin, tenant, "view@acme.io", "viewer")

	grant := whoamiGrant(t, h, viewer, tenant.String())
	if grant["role"] != "viewer" {
		t.Fatalf("grant role = %v, want viewer", grant["role"])
	}
	perms := permSet(t, grant)
	if len(perms) == 0 {
		t.Fatal("viewer's effective set is empty; every assertion below is vacuous")
	}
	// A viewer holds the ordinary entity read and NOT the IAM roster read. `user:read`
	// reads like an ordinary viewer read and the engine gates it at ADMIN through the
	// explicit core set — this is the exact class the hand-written console mirror leaked
	// to viewers by routing it through the generic verb tier.
	if !perms["agent:read"] {
		t.Error("viewer's set omits agent:read")
	}
	if perms["user:read"] {
		t.Error("viewer's set carries user:read, which the engine gates at admin")
	}
	if perms["agent:write"] {
		t.Error("viewer's set carries agent:write")
	}

	// The set is not a claim about the engine — it must MATCH what the engine does. Both
	// routes are mounted for GET, so a 403 here is the authorization gate and not a 404
	// or a 405 from some other guard.
	if r := h.do("GET", "/v1/agents", viewer, nil, hdr); r.code != http.StatusOK {
		t.Errorf("viewer GET /v1/agents = %d %s, want 200 — the set says agent:read is held", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/members", viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("viewer GET /v1/members = %d %s, want 403 — the set says user:read is NOT held", r.code, r.raw)
	}
	// The route really is reachable for a principal that HOLDS user:read: without this
	// the 403 above could be any refusal at all, including one that never consulted RBAC.
	if r := h.do("GET", "/v1/members", admin, nil, hdr); r.code != http.StatusOK {
		t.Errorf("superadmin GET /v1/members = %d %s, want 200 — the viewer's 403 must be the authz gate, not a broken route", r.code, r.raw)
	}
}

// TestWhoamiSetIsPerGrantNotPerPrincipal is reason #1 the set travels per GRANT: one
// principal can hold different roles in different tenants, so one set per principal
// would answer the second tenant with the first tenant's authority. Asserted on the
// SERVER side, because the payload is where the shape is decided.
func TestWhoamiSetIsPerGrantNotPerPrincipal(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	acme := h.createOrg(admin, "acme")
	globex := h.createOrg(admin, "globex")

	// One user, viewer in one tenant and admin in the other.
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": "two@acme.io", "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	for _, g := range []struct {
		tenant string
		role   string
	}{{acme.String(), "viewer"}, {globex.String(), "admin"}} {
		if r := h.do("POST", "/v1/memberships", admin, map[string]any{
			"user_id": uid, "tenant": g.tenant, "role": g.role,
		}, nil); r.code != http.StatusCreated {
			t.Fatalf("grant %s in %s = %d %s", g.role, g.tenant, r.code, r.raw)
		}
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "two@acme.io", "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login = %d %s", r.code, r.raw)
	}
	token := r.body["token"].(string)

	viewerSide := permSet(t, whoamiGrant(t, h, token, acme.String()))
	adminSide := permSet(t, whoamiGrant(t, h, token, globex.String()))

	if viewerSide["user:read"] {
		t.Error("the viewer grant carries user:read, which the engine gates at admin")
	}
	if !adminSide["user:read"] {
		t.Error("the admin grant omits user:read")
	}
	if !viewerSide["agent:read"] || !adminSide["agent:read"] {
		t.Error("both grants should carry agent:read; one of them does not")
	}
	if len(viewerSide) >= len(adminSide) {
		t.Errorf("viewer set has %d perms and admin set %d — one set per principal would have made them equal", len(viewerSide), len(adminSide))
	}
}

// TestWhoamiSetForATokenPrincipal: a token carries at most ONE bound grant and is never
// workspace-confined. It must still receive a real set, or every programmatic consumer
// of whoami reads an empty one and concludes it may do nothing.
func TestWhoamiSetForATokenPrincipal(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("POST", "/v1/tokens", admin, map[string]any{
		"name": "ci", "tenant": tenant.String(), "role": "editor",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("mint token = %d %s", r.code, r.raw)
	}
	tok, _ := r.body["token"].(string)
	if tok == "" {
		t.Fatalf("no token in %s", r.raw)
	}

	who := h.do("GET", "/v1/auth/whoami", tok, nil, nil)
	if who.code != http.StatusOK || who.body["kind"] != "token" {
		t.Fatalf("whoami = %d %s", who.code, who.raw)
	}
	grant := whoamiGrant(t, h, tok, tenant.String())
	perms := permSet(t, grant)
	if !perms["agent:write"] {
		t.Error("an editor token's set omits agent:write")
	}
	if perms["user:read"] {
		t.Error("an editor token's set carries user:read, which the engine gates at admin")
	}
	// A token is never a MEMBERSHIP, so it is never confined.
	if _, present := grant["confined_workspace"]; present {
		t.Errorf("a token grant reported confined_workspace = %v", grant["confined_workspace"])
	}
	// The set matches the engine on the request path, same oracle as everywhere else.
	if r := h.do("GET", "/v1/agents", tok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("token GET /v1/agents = %d %s, want 200", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/members", tok, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("token GET /v1/members = %d %s, want 403 — the set says user:read is NOT held", r.code, r.raw)
	}
}

// TestWhoamiSetNarrowsForAConfinedPrincipal is the Q4-F2 closure. Workspace confinement
// never traveled in whoami, so no client-side expression could narrow anything: a
// confined admin was offered every tenant-wide action its ROLE implies, including the
// access-graph read the engine forbids it. The set now carries the narrowing, and the
// engine's own 403 is the oracle for it.
func TestWhoamiSetNarrowsForAConfinedPrincipal(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	ws := h.createWorkspace(tenant, "payments")
	_, unconfined := h.roleUser(admin, tenant, "open@acme.io", "admin")
	_, confined := h.confinedUser(admin, tenant, "conf@acme.io", "admin", ws)

	openGrant := whoamiGrant(t, h, unconfined, tenant.String())
	confGrant := whoamiGrant(t, h, confined, tenant.String())
	openPerms := permSet(t, openGrant)
	confPerms := permSet(t, confGrant)

	// The confinement is REPORTED, and only for the confined membership.
	if got := confGrant["confined_workspace"]; got != ws.String() {
		t.Errorf("confined grant confined_workspace = %v, want %s", got, ws)
	}
	if _, present := openGrant["confined_workspace"]; present {
		t.Errorf("an unconfined grant reported confined_workspace = %v", openGrant["confined_workspace"])
	}

	// The access-MATRIX recon reads: held by the unconfined admin, absent for the
	// confined one, and each side agrees with the route's own answer.
	for _, p := range []string{"accessgraph:read", "accessmap:graph:read", "accessmap:drift:read", "authz:read"} {
		if !openPerms[p] {
			t.Errorf("unconfined admin's set omits %q; the removal assertion below would be vacuous", p)
		}
		if confPerms[p] {
			t.Errorf("confined admin's set still carries %q — the engine forbids it whatever the action targets", p)
		}
	}
	if r := h.do("GET", "/v1/access-edges", unconfined, nil, hdr); r.code != http.StatusOK {
		t.Errorf("unconfined admin access-edges = %d %s, want 200 (its set says accessgraph:read is held)", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/access-edges", confined, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("confined admin access-edges = %d %s, want 403 (its set says accessgraph:read is NOT held)", r.code, r.raw)
	}

	// …and confinement narrows ONLY that. The ordinary tenant reads and writes a
	// confined admin performs inside its own workspace must survive, or the console
	// would hide the work the operator is there to do.
	for _, p := range []string{"agent:read", "agent:write", "membership:read", "tenant:admin"} {
		if !confPerms[p] {
			t.Errorf("confined admin's set lost %q, which confinement does not remove", p)
		}
	}
	removed := 0
	for p := range openPerms {
		if !confPerms[p] {
			removed++
		}
	}
	if removed != 4 {
		t.Errorf("confinement removed %d permissions, want exactly the 4 access-matrix recon reads", removed)
	}
}
