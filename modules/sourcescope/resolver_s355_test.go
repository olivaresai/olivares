// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// --- subject-axis resolution matrix (session/agent/user/user_group/role) --------
//
// These exercise the resolver algebra of ADR-0022: containment on the five subject axes,
// absolute forbid-overrides-allow, and the honest per-consumer axis availability. The
// write-API round-trip is covered by binding_test.go (E1); here we drive the resolver.

// resolveSession drives the session-aware entrypoint (models ScopeGate / runtime).
func (h *harness) resolveSession(tenant model.TenantID, p auth.Principal, sessionRef, sourceType, sourceRef string) sourcescope.Decision {
	h.t.Helper()
	d, err := h.resolver.ResolveForSession(context.Background(), tenant, p, sessionRef, sourceType, sourceRef)
	if err != nil {
		h.t.Fatalf("ResolveForSession(%s/%s): %v", sourceType, sourceRef, err)
	}
	return d
}

// principalWithGroups creates a user WITH a tenant membership (required by the S256
// per-tenant gate for directory groups to count) and seeds it into directory groups
// (created in the auth store), then logs in so principal.GroupsIn is populated. It returns
// the authenticated principal and the created group ids (the scope_ref for a user_group
// binding). A directory group confers a subject identity ONLY where the user holds a
// direct membership in the group's tenant (loadGrants gate) — hence role must be a role.
func (h *harness) principalWithGroups(admin string, tenant model.TenantID, email, role string, groupNames ...string) (auth.Principal, []string) {
	h.t.Helper()
	ur := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if ur.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, ur.code, ur.raw)
	}
	uid := ur.body["id"].(string)
	if role != "" {
		if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
			h.t.Fatalf("grant membership = %d %s", r.code, r.raw)
		}
	}
	var groupIDs []string
	if len(groupNames) > 0 {
		if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
			for _, name := range groupNames {
				g, err := as.Groups().Create(context.Background(), model.UserGroup{TargetTenantID: tenant, DisplayName: name})
				if err != nil {
					return err
				}
				if _, err := as.GroupMembers().Create(context.Background(), model.UserGroupMember{GroupID: g.ID, UserID: model.ID(uid)}); err != nil {
					return err
				}
				groupIDs = append(groupIDs, g.ID.String())
			}
			return nil
		}); err != nil {
			h.t.Fatalf("seed directory groups: %v", err)
		}
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	p, err := h.authr.Authenticate(context.Background(), lr.body["token"].(string))
	if err != nil {
		h.t.Fatalf("authenticate %s: %v", email, err)
	}
	return p, groupIDs
}

// mustBind creates an ALLOW/forbid binding and fails on a non-201.
func (h *harness) mustBind(admin string, tenant model.TenantID, sourceRef, tree, ref, effect string) {
	h.t.Helper()
	body := map[string]any{"source_type": "model", "source_ref": sourceRef, "scope_tree": tree, "scope_ref": ref, "enabled": true}
	if effect != "" {
		body["effect"] = effect
	}
	if r := h.createBinding(admin, tenant, body); r.code != http.StatusCreated {
		h.t.Fatalf("bind %s/%s %s = %d %s", tree, ref, effect, r.code, r.raw)
	}
}

// TestS355UserAxis: a source bound (allow) to a specific user is resolvable by that user's
// principal and denied for another — regardless of the acting agent (the user axis matches
// the authenticated principal, not the actor scope).
func TestS355UserAxis(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	u := h.principalFor(admin, tenant, "u@acme.io", "") // confined: no tenant role ⇒ no RBAC bypass
	other := h.principalFor(admin, tenant, "other@acme.io", "")

	h.mustBind(admin, tenant, "m-user", "user", u.UserID.String(), "allow")

	if d := h.resolveAgent(tenant, u, "no-agent", sourcescope.SourceModel, "m-user"); !d.Allowed || !strings.Contains(d.Reason, "user:") {
		t.Errorf("named user must be allowed via user containment, got %+v", d)
	}
	if d := h.resolveAgent(tenant, other, "no-agent", sourcescope.SourceModel, "m-user"); d.Allowed {
		t.Errorf("a different user must be denied (confined source), got %+v", d)
	}
}

// TestS355RoleAxis: a source bound (allow) to a tenant role is resolvable by a principal
// holding that role and denied for a confined principal (no role).
func TestS355RoleAxis(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.principalFor(admin, tenant, "v@acme.io", auth.RoleViewer)
	confined := h.principalFor(admin, tenant, "c@acme.io", "")

	h.mustBind(admin, tenant, "m-role", "role", auth.RoleViewer, "allow")

	if d := h.resolveAgent(tenant, viewer, "no-agent", sourcescope.SourceModel, "m-role"); !d.Allowed || !strings.Contains(d.Reason, "role:") {
		t.Errorf("role holder must be allowed via role containment, got %+v", d)
	}
	if d := h.resolveAgent(tenant, confined, "no-agent", sourcescope.SourceModel, "m-role"); d.Allowed {
		t.Errorf("confined principal (no role) must be denied, got %+v", d)
	}
}

// TestS355UserGroupAxis: a source bound (allow) to a directory group is resolvable by a
// member (matched via principal.GroupsIn, S256) and denied for a non-member.
func TestS355UserGroupAxis(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	member, groups := h.principalWithGroups(admin, tenant, "eng@acme.io", auth.RoleViewer, "Engineering")
	nonMember := h.principalFor(admin, tenant, "c@acme.io", "")

	h.mustBind(admin, tenant, "m-grp", "user_group", groups[0], "allow")

	if d := h.resolveAgent(tenant, member, "no-agent", sourcescope.SourceModel, "m-grp"); !d.Allowed || !strings.Contains(d.Reason, "user_group:") {
		t.Errorf("directory-group member must be allowed via group containment, got %+v", d)
	}
	if d := h.resolveAgent(tenant, nonMember, "no-agent", sourcescope.SourceModel, "m-grp"); d.Allowed {
		t.Errorf("non-member must be denied (confined source), got %+v", d)
	}
}

// TestS355SessionAxis: a source bound (allow) to a specific session is resolvable on the
// session-aware path for that session and denied for another — and NOT enforced on the
// agent-only path (no session in context), where it simply confines the source.
func TestS355SessionAxis(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ws := h.createWorkspace(tenant, "w")
	a := h.createAgent(tenant, "a1", ws)
	h.createSession(tenant, "sess-1", a.ID, ws)
	confined := h.principalFor(admin, tenant, "c@acme.io", "")

	h.mustBind(admin, tenant, "m-sess", "session", "sess-1", "allow")

	if d := h.resolveSession(tenant, confined, "sess-1", sourcescope.SourceModel, "m-sess"); !d.Allowed || !strings.Contains(d.Reason, "session:") {
		t.Errorf("bound session must be allowed, got %+v", d)
	}
	if d := h.resolveSession(tenant, confined, "sess-other", sourcescope.SourceModel, "m-sess"); d.Allowed {
		t.Errorf("a different session must be denied, got %+v", d)
	}
	// Agent-only path: no session in context ⇒ the session binding cannot match; the agent
	// is not otherwise in scope ⇒ deny-closed (the session axis does not leak to retrieval).
	if d := h.resolveAgent(tenant, confined, "a1", sourcescope.SourceModel, "m-sess"); d.Allowed {
		t.Errorf("session binding must NOT be enforced on the agent-only path, got %+v", d)
	}
}

// TestS355AgentAxis: a source bound (allow) to a specific agent is resolvable for that
// agent (directly, and via a session it owns) and denied for another agent.
func TestS355AgentAxis(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ws := h.createWorkspace(tenant, "w")
	a1 := h.createAgent(tenant, "a1", ws)
	h.createAgent(tenant, "a2", ws)
	h.createSession(tenant, "sess-1", a1.ID, ws) // session owned by a1
	confined := h.principalFor(admin, tenant, "c@acme.io", "")

	h.mustBind(admin, tenant, "m-agent", "agent", "a1", "allow")

	if d := h.resolveAgent(tenant, confined, "a1", sourcescope.SourceModel, "m-agent"); !d.Allowed || !strings.Contains(d.Reason, "agent:") {
		t.Errorf("bound agent must be allowed, got %+v", d)
	}
	if d := h.resolveAgent(tenant, confined, "a2", sourcescope.SourceModel, "m-agent"); d.Allowed {
		t.Errorf("a different agent must be denied, got %+v", d)
	}
	// Via the session it owns: the acting agent is resolved from the session (a1) ⇒ allowed.
	if d := h.resolveSession(tenant, confined, "sess-1", sourcescope.SourceModel, "m-agent"); !d.Allowed {
		t.Errorf("agent binding must match via the session's owning agent, got %+v", d)
	}
}

// TestS355ForbidOverridesAllowAbsolute: a forbid on the user axis overrides a workspace
// allow the actor would otherwise satisfy — forbid is absolute (ADR-0022 §2), overriding
// containment (and, since viewer holds model:read, tenant RBAC too).
func TestS355ForbidOverridesAllowAbsolute(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ws := h.createWorkspace(tenant, "w")
	a := h.createAgent(tenant, "a1", ws)
	h.createSession(tenant, "sess-1", a.ID, ws)
	h.createSession(tenant, "sess-2", a.ID, ws)
	u := h.principalFor(admin, tenant, "u@acme.io", auth.RoleViewer)
	other := h.principalFor(admin, tenant, "o@acme.io", auth.RoleViewer)

	h.mustBind(admin, tenant, "m-fb", "workspace", "w", "allow")
	h.mustBind(admin, tenant, "m-fb", "user", u.UserID.String(), "forbid")

	if d := h.resolveSession(tenant, u, "sess-1", sourcescope.SourceModel, "m-fb"); d.Allowed {
		t.Errorf("forbid on the user must override the workspace allow (and RBAC), got %+v", d)
	}
	if d := h.resolveSession(tenant, other, "sess-2", sourcescope.SourceModel, "m-fb"); !d.Allowed {
		t.Errorf("a non-forbidden user in the workspace must still be allowed, got %+v", d)
	}
}

// TestS355UserInTwoGroupsForbidOneWins is the DoD wire-proof: a user who is a member of TWO
// directory groups, with a forbid on ONE of them, is denied — forbid wins over the
// workspace allow and over the user's other (non-forbidden) group.
func TestS355UserInTwoGroupsForbidOneWins(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ws := h.createWorkspace(tenant, "w")
	a := h.createAgent(tenant, "a1", ws)
	h.createSession(tenant, "sess-1", a.ID, ws)
	u, groups := h.principalWithGroups(admin, tenant, "u@acme.io", auth.RoleViewer, "G1", "G2")
	g1, g2 := groups[0], groups[1]

	h.mustBind(admin, tenant, "m-2g", "workspace", "w", "allow") // u's session is in w ⇒ would allow
	h.mustBind(admin, tenant, "m-2g", "user_group", g2, "forbid")

	if d := h.resolveSession(tenant, u, "sess-1", sourcescope.SourceModel, "m-2g"); d.Allowed {
		t.Errorf("forbid on ONE of the user's groups must win, got %+v", d)
	}
	// A member of only the OTHER group (not the forbidden one) is allowed by the workspace.
	u1, _ := h.principalWithGroups(admin, tenant, "u1@acme.io", auth.RoleViewer, "G1only")
	h.createSession(tenant, "sess-2", a.ID, ws)
	_ = g1
	if d := h.resolveSession(tenant, u1, "sess-2", sourcescope.SourceModel, "m-2g"); !d.Allowed {
		t.Errorf("a user not in the forbidden group must remain allowed, got %+v", d)
	}
}

// TestS355ForbidOnlySourceStaysGlobalMinusForbidden: a source with ONLY a forbid binding
// (no allow) is unconfined — global for everyone EXCEPT the forbidden subject (the
// "restrict certain subjects" posture, ADR-0022 §2).
func TestS355ForbidOnlySourceStaysGlobalMinusForbidden(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	u := h.principalFor(admin, tenant, "u@acme.io", "")
	other := h.principalFor(admin, tenant, "o@acme.io", "")

	h.mustBind(admin, tenant, "m-fo", "user", u.UserID.String(), "forbid")

	if d := h.resolveAgent(tenant, u, "no-agent", sourcescope.SourceModel, "m-fo"); d.Allowed {
		t.Errorf("the forbidden user must be denied, got %+v", d)
	}
	if d := h.resolveAgent(tenant, other, "no-agent", sourcescope.SourceModel, "m-fo"); !d.Allowed {
		t.Errorf("a non-forbidden user must see the (unconfined) source, got %+v", d)
	}
}
