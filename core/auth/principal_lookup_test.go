// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// PrincipalForUser must produce the SAME authorization-relevant principal a real login
// produces (same roles), by id or email, at the requested assurance.
func TestPrincipalForUserMatchesAuthenticated(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	u, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "Dev@Acme.com", DisplayName: "Dev", Password: "dev-password-1"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Simulated principal (by id) carries the real role, the requested AAL, kind user.
	sim, found, err := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)
	if err != nil || !found {
		t.Fatalf("PrincipalForUser by id = (found=%v, err=%v)", found, err)
	}
	if r, _ := sim.RoleIn(tenant); r != auth.RoleEditor {
		t.Errorf("simulated role = %q, want editor", r)
	}
	if sim.AAL != auth.AAL3 || sim.Kind != auth.KindUser {
		t.Errorf("simulated principal = {AAL %d, Kind %s}, want {3, user}", sim.AAL, sim.Kind)
	}

	// It matches what the authenticated session resolves (the honesty invariant).
	tok, _, err := a.Login(ctx, "dev@acme.com", "dev-password-1", "10.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	live, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	simRole, _ := sim.RoleIn(tenant)
	liveRole, _ := live.RoleIn(tenant)
	if simRole != liveRole || sim.UserID != live.UserID || sim.Superadmin != live.Superadmin {
		t.Errorf("simulated %v/%v/%v != authenticated %v/%v/%v", simRole, sim.UserID, sim.Superadmin, liveRole, live.UserID, live.Superadmin)
	}

	// By email resolves too; AAL clamps into [AAL1, AAL3].
	byEmail, found, err := a.PrincipalForUser(ctx, "DEV@acme.com", 0)
	if err != nil || !found || byEmail.UserID != u.ID {
		t.Fatalf("PrincipalForUser by email = (found=%v, err=%v, id=%v)", found, err, byEmail.UserID)
	}
	if byEmail.AAL != auth.AAL1 {
		t.Errorf("AAL 0 clamped to %d, want AAL1", byEmail.AAL)
	}

	// Unknown subject → not found, no error (it authorizes nothing).
	if _, f, err := a.PrincipalForUser(ctx, "nobody@x.io", auth.AAL3); err != nil || f {
		t.Errorf("unknown user = (found=%v, err=%v), want (false, nil)", f, err)
	}
}

// TenantPrincipals is the candidate population: tenant members + superadmins + active
// bound tokens; a revoked token is excluded (it authorizes nothing).
func TestTenantPrincipalsPopulation(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	ed, _ := a.CreateUser(ctx, admin, auth.NewUser{Email: "ed@acme.com", Password: "dev-password-1"})
	if _, err := a.GrantMembership(ctx, admin, ed.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatal(err)
	}
	vw, _ := a.CreateUser(ctx, admin, auth.NewUser{Email: "vw@acme.com", Password: "dev-password-1"})
	if _, err := a.GrantMembership(ctx, admin, vw.ID, tenant, auth.RoleViewer, model.ID("")); err != nil {
		t.Fatal(err)
	}
	_, liveTok, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "ci", BoundTenant: tenant, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	_, deadTok, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "old", BoundTenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := a.RevokeToken(ctx, admin, deadTok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	pop, err := a.TenantPrincipals(ctx, tenant, auth.AAL3)
	if err != nil {
		t.Fatalf("TenantPrincipals: %v", err)
	}

	users := map[string]string{} // userID -> role
	tokens := map[string]bool{}
	superadmins := 0
	for _, p := range pop {
		if p.Superadmin {
			superadmins++
		}
		if p.Kind == auth.KindToken {
			tokens[p.CredID.String()] = true
			continue
		}
		r, _ := p.RoleIn(tenant)
		users[p.UserID.String()] = r
	}

	if users[ed.ID.String()] != auth.RoleEditor {
		t.Errorf("editor missing/wrong role: %v", users[ed.ID.String()])
	}
	if users[vw.ID.String()] != auth.RoleViewer {
		t.Errorf("viewer missing/wrong role: %v", users[vw.ID.String()])
	}
	if superadmins < 1 {
		t.Errorf("superadmin (root) must appear in every tenant's population; got %d", superadmins)
	}
	if !tokens[liveTok.ID.String()] {
		t.Error("active bound token must be in the population")
	}
	if tokens[deadTok.ID.String()] {
		t.Error("revoked token must NOT be in the population (it authorizes nothing)")
	}

	// LIVE by construction: adding a member is reflected on the very next call (the
	// default enumerator reads the store fresh — no cache, so it cannot go stale, the
	// no-divergence guarantee a cached PrincipalEnumerator must preserve).
	before := len(pop)
	nu, _ := a.CreateUser(ctx, admin, auth.NewUser{Email: "new@acme.com", Password: "dev-password-1"})
	if _, err := a.GrantMembership(ctx, admin, nu.ID, tenant, auth.RoleViewer, model.ID("")); err != nil {
		t.Fatal(err)
	}
	pop2, err := a.TenantPrincipals(ctx, tenant, auth.AAL3)
	if err != nil {
		t.Fatal(err)
	}
	if len(pop2) != before+1 {
		t.Errorf("population after adding a member = %d, want %d (live, no stale cache)", len(pop2), before+1)
	}
}

// PrincipalForToken reflects a token's bound grant and excludes a revoked one.
func TestPrincipalForToken(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	_, tok, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "ci", BoundTenant: tenant, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	p, found, err := a.PrincipalForToken(ctx, tok.ID)
	if err != nil || !found {
		t.Fatalf("PrincipalForToken = (found=%v, err=%v)", found, err)
	}
	if p.Kind != auth.KindToken || p.AAL != 0 {
		t.Errorf("token principal = {Kind %s, AAL %d}, want {token, 0}", p.Kind, p.AAL)
	}
	if r, _ := p.RoleIn(tenant); r != auth.RoleAdmin {
		t.Errorf("token role = %q, want admin", r)
	}

	if err := a.RevokeToken(ctx, admin, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := a.PrincipalForToken(ctx, tok.ID); found {
		t.Error("a revoked token must resolve to not-found (no access)")
	}
}

// An agent-bound (agent-OBO exchanged) token must simulate with the SAME AgentIdentity the
// live Authenticate path carries: agent-scoped policy answers from the AuthZEN/access-review
// enumeration paths diverge from enforcement otherwise. Regression: principalFromToken
// promised byte-parity with authToken but dropped the AgentRef binding.
func TestPrincipalForTokenCarriesAgentIdentity(t *testing.T) {
	ctx := context.Background()
	f := newAgentExchangeFixture(t)
	const agentRef = "ext-agent-claude-1"

	f.a.SetAgentLifecycleChecker(&mockAgentChecker{
		validAgents: map[string]string{agentRef: f.sponsorExtID},
	})
	caller, err := f.a.Authenticate(ctx, f.sessionTok)
	if err != nil {
		t.Fatal(err)
	}
	req := accessReq(f.sessionTok)
	req.RequestedActorRef = agentRef
	res, err := f.a.ExchangeToken(ctx, caller, req)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	live, err := f.a.Authenticate(ctx, res.AccessToken)
	if err != nil {
		t.Fatalf("authenticate child token: %v", err)
	}
	sim, found, err := f.a.PrincipalForToken(ctx, res.Stored.ID)
	if err != nil || !found {
		t.Fatalf("PrincipalForToken = (found=%v, err=%v)", found, err)
	}
	if sim.AgentIdentity != live.AgentIdentity {
		t.Errorf("simulated AgentIdentity = %q, live = %q — simulation diverges from enforcement",
			sim.AgentIdentity, live.AgentIdentity)
	}
	if sim.AgentIdentity != agentRef {
		t.Errorf("simulated AgentIdentity = %q, want %q", sim.AgentIdentity, agentRef)
	}
}
