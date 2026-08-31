// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"
)

// TestResolvePreview exercises the baseline resolver preview: in-scope allow with
// the scoped credential surfaced as name+hint, out-of-scope deny, and — the load-bearing
// property — that the CALLER's authority never leaks into the verdict (a superadmin
// previewing an out-of-scope agent still sees the deny that agent would get).
func TestResolvePreview(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wsA := h.createWorkspace(tenant, "team-a")
	wsB := h.createWorkspace(tenant, "team-b")
	h.createAgent(tenant, "agent-a", wsA)
	h.createAgent(tenant, "agent-b", wsB)

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "mcp", "source_ref": "github", "scope_tree": "workspace", "scope_ref": "team-a",
		"cred_name": "gh-scoped", "cred_ref_kind": "vault", "cred_ref": "kv/data/gh", "cred_hint": "ghp_****",
		"enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	// In-scope agent: allowed, bound, with the scoped credential name+hint (no locator).
	r := h.do("GET", "/v1/m/sourcescope/resolve?source_type=mcp&source_ref=github&actor_kind=agent&actor_ref=agent-a", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("resolve in-scope = %d %s", r.code, r.raw)
	}
	if r.body["allowed"] != true || r.body["bound"] != true || r.body["baseline"] != true {
		t.Fatalf("in-scope verdict = %s", r.raw)
	}
	if r.body["cred_name"] != "gh-scoped" || r.body["cred_hint"] != "ghp_****" {
		t.Fatalf("in-scope cred = %s", r.raw)
	}
	if _, leaked := r.body["cred_ref"]; leaked {
		t.Fatalf("preview leaked the credential locator: %s", r.raw)
	}

	// Out-of-scope agent: deny-closed — even though the CALLER is a superadmin whose
	// own RBAC would open every source. This is the baseline-principal property.
	r = h.do("GET", "/v1/m/sourcescope/resolve?source_type=mcp&source_ref=github&actor_kind=agent&actor_ref=agent-b", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("resolve out-of-scope = %d %s", r.code, r.raw)
	}
	if r.body["allowed"] != false || r.body["bound"] != true {
		t.Fatalf("out-of-scope verdict must be deny-closed baseline: %s", r.raw)
	}

	// Unknown actor: matches nothing on a confined source (deny-closed).
	r = h.do("GET", "/v1/m/sourcescope/resolve?source_type=mcp&source_ref=github&actor_kind=agent&actor_ref=ghost", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["allowed"] != false {
		t.Fatalf("unknown actor = %d %s", r.code, r.raw)
	}

	// An UNBOUND source stays global at baseline.
	r = h.do("GET", "/v1/m/sourcescope/resolve?source_type=mcp&source_ref=unbound&actor_kind=agent&actor_ref=agent-b", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["allowed"] != true || r.body["bound"] != false {
		t.Fatalf("unbound source = %d %s", r.code, r.raw)
	}

	// Parameter validation: bad source_type / missing refs / bad actor_kind → 400.
	for _, path := range []string{
		"/v1/m/sourcescope/resolve?source_type=nope&source_ref=github&actor_kind=agent&actor_ref=a",
		"/v1/m/sourcescope/resolve?source_type=mcp&actor_kind=agent&actor_ref=a",
		"/v1/m/sourcescope/resolve?source_type=mcp&source_ref=github&actor_kind=cat&actor_ref=a",
	} {
		if r := h.do("GET", path, admin, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", path, r.code, r.raw)
		}
	}

	// A user with no membership in the tenant holds no binding:read → 403.
	h.do("POST", "/v1/users", admin, map[string]any{"email": "noone@x.io", "password": "memberpass1"}, nil)
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "noone@x.io", "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login = %d %s", lr.code, lr.raw)
	}
	r = h.do("GET", "/v1/m/sourcescope/resolve?source_type=mcp&source_ref=github&actor_kind=agent&actor_ref=agent-a", lr.body["token"].(string), nil, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("confined resolve = %d, want 403: %s", r.code, r.raw)
	}
}

// TestResolvePreviewSessionAxis covers the session actor kind end to end: a session
// binding matches only the named session, and the agent axis rides the session's row.
func TestResolvePreviewSessionAxis(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ws := h.createWorkspace(tenant, "team-a")
	agent := h.createAgent(tenant, "agent-a", ws)
	h.createSession(tenant, "sess-1", agent.ID, ws)

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "knowledge", "source_ref": "kb-secrets", "scope_tree": "session", "scope_ref": "sess-1",
		"enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("create session binding = %d %s", r.code, r.raw)
	}

	r := h.do("GET", "/v1/m/sourcescope/resolve?source_type=knowledge&source_ref=kb-secrets&actor_kind=session&actor_ref=sess-1", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["allowed"] != true {
		t.Fatalf("named session = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/sourcescope/resolve?source_type=knowledge&source_ref=kb-secrets&actor_kind=session&actor_ref=sess-2", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["allowed"] != false {
		t.Fatalf("other session must be denied = %d %s", r.code, r.raw)
	}
}
