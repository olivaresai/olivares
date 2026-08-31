// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// fakeGroupMapper is a test stand-in for the reserved enterprise GroupMapper: it
// matches an asserted IdP identifier to a directory group by ExternalID (the same
// correlation the real enterprise mapper uses). It proves the OPEN-CORE
// reconciliation PLUMBING that CompleteSSO drives; the real matching engine and the
// assembled-binary wire-proof live in the enterprise repo.
type fakeGroupMapper struct{}

func (fakeGroupMapper) MapAssertedGroups(asserted []string, groups []model.UserGroup) []model.ID {
	want := map[string]bool{}
	for _, a := range asserted {
		want[a] = true
	}
	var out []model.ID
	for _, g := range groups {
		if g.ExternalID != "" && want[g.ExternalID] {
			out = append(out, g.ID)
		}
	}
	return out
}

// putGlobalSSOConfig writes a minimal global federation config row directly (no
// sealer needed — it carries no secret), so a test can set scope-level flags like
// SCIMAuthoritative without standing up the sealed-config service.
func putGlobalSSOConfig(t *testing.T, st store.Store, cfg model.FederationConfig) {
	t.Helper()
	cfg.TargetTenantID = model.SystemTenantID
	if cfg.Protocol == "" {
		cfg.Protocol = auth.ProtocolOIDC
	}
	if cfg.Status == "" {
		cfg.Status = model.StatusActive
	}
	if err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		_, err := as.FederationConfigs().Create(context.Background(), cfg)
		return err
	}); err != nil {
		t.Fatalf("write global sso config: %v", err)
	}
}

// TestSSOGroupReconciliation_FlipsDecision is the U2 open-core proof: an SSO
// login carrying a groups claim, with a GroupMapper wired, reconciles the user's
// directory-group membership so the group's MappedRole ELEVATES the user's effective
// role — a real authorization decision changed by the assertion, end to end
// (assertion → FederatedIdentity.Groups → CompleteSSO reconcile → Principal.GroupsIn
// → MappedRole elevation → RoleIn).
func TestSSOGroupReconciliation_FlipsDecision(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil).WithGroupMapper(fakeGroupMapper{})
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	// A member with a base viewer membership (the per-tenant gate requires a direct
	// membership before a group can confer anything).
	uid, _ := mustMember(t, ctx, a, super, tenant, "eng@acme.com", auth.RoleViewer)

	// An "engineering" group mapped to editor, with NO members yet.
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "grp-eng"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatal(err)
	}

	// BEFORE: the member is a plain viewer and carries no group.
	before := loginPrincipal(t, ctx, a, "eng@acme.com")
	if role, _ := before.RoleIn(tenant); role != auth.RoleViewer {
		t.Fatalf("before SSO: RoleIn = %q, want viewer", role)
	}
	if len(before.GroupsIn(tenant)) != 0 {
		t.Fatalf("before SSO: carries a group already: %v", before.GroupsIn(tenant))
	}

	// The IdP asserts the engineering group at login.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Email: "eng@acme.com", Subject: "eng-subject", Groups: []string{"grp-eng"},
	}, "10.0.0.9", tenant, false); err != nil {
		t.Fatalf("CompleteSSO: %v", err)
	}

	// AFTER: the reconciled membership elevates the effective role to editor.
	after := loginPrincipal(t, ctx, a, "eng@acme.com")
	if got := after.GroupsIn(tenant); len(got) != 1 || got[0] != g.Group.ID.String() {
		t.Fatalf("after SSO: GroupsIn = %v, want [%s]", got, g.Group.ID)
	}
	if role, _ := after.RoleIn(tenant); role != auth.RoleEditor {
		t.Fatalf("after SSO: RoleIn = %q, want editor (the group's MappedRole) — the decision did not flip", role)
	}
	_ = uid
}

// TestSSOGroupReconciliation_NoMapperIsNoOp proves the OPEN build (no GroupMapper)
// extracts the asserted groups but confers nothing — the honest cap.
func TestSSOGroupReconciliation_NoMapperIsNoOp(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil) // no group mapper — the base build
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	mustMember(t, ctx, a, super, tenant, "eng@acme.com", auth.RoleViewer)
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "grp-eng"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Email: "eng@acme.com", Subject: "eng-subject", Groups: []string{"grp-eng"},
	}, "10.0.0.9", tenant, false); err != nil {
		t.Fatal(err)
	}
	p := loginPrincipal(t, ctx, a, "eng@acme.com")
	if len(p.GroupsIn(tenant)) != 0 {
		t.Fatalf("open build must not reconcile groups (no mapper): %v", p.GroupsIn(tenant))
	}
	if role, _ := p.RoleIn(tenant); role != auth.RoleViewer {
		t.Fatalf("open build must not elevate: RoleIn = %q, want viewer", role)
	}
}

// TestSSOScimAuthoritative_RefusesJITAndReconcile proves the D4 toggle: a
// SCIM-authoritative scope never JIT-creates a new account from a login and never
// reconciles groups (SCIM owns both).
func TestSSOScimAuthoritative_RefusesJITAndReconcile(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil).WithGroupMapper(fakeGroupMapper{})
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	putGlobalSSOConfig(t, st, model.FederationConfig{SCIMAuthoritative: true})

	// A brand-new identity (no local user) is REFUSED, not provisioned.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Email: "newcomer@acme.com", Subject: "new-sub",
	}, "10.0.0.9", tenant, true); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("scim-authoritative JIT err = %v, want ErrUnauthenticated", err)
	}

	// An EXISTING member authenticates, but their groups are NOT reconciled from the
	// login (SCIM owns the roster).
	mustMember(t, ctx, a, super, tenant, "eng@acme.com", auth.RoleViewer)
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "grp-eng"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Email: "eng@acme.com", Subject: "eng-sub", Groups: []string{"grp-eng"},
	}, "10.0.0.9", tenant, true); err != nil {
		t.Fatal(err)
	}
	p := loginPrincipal(t, ctx, a, "eng@acme.com")
	if len(p.GroupsIn(tenant)) != 0 {
		t.Fatalf("scim-authoritative must NOT reconcile groups at login: %v", p.GroupsIn(tenant))
	}
}

// TestSSOCorrelatesByEmail proves SSO correlation is by the verified, globally-unique
// EMAIL — the safe key that cannot select the wrong account across IdPs (issuer-
// qualified external_id correlation is deferred with its migration §D5). A
// subject match is NOT used: an identity with a matching subject but a DIFFERENT email
// resolves by email (here provisioning a fresh account), never by the bare subject.
func TestSSOCorrelatesByEmail(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)

	// Seed an SSO-only user whose external_id is the IdP subject.
	var seeded model.User
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Create(ctx, model.User{
			Email: "a@acme.com", DisplayName: "E", Status: model.StatusActive, ExternalID: "stable-sub-1",
		})
		seeded = u
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Same email ⇒ the seeded user (email correlation).
	if u, err := a.FindOrProvisionByEmail(ctx, auth.FederatedIdentity{Subject: "stable-sub-1", Email: "a@acme.com"}); err != nil || u.ID != seeded.ID {
		t.Fatalf("email match = (%s, %v), want %s", u.ID, err, seeded.ID)
	}
	// Same SUBJECT but a different email ⇒ NOT the seeded user (subject is not a match
	// key — a fresh account is provisioned), proving correlation is email-only.
	u, err := a.FindOrProvisionByEmail(ctx, auth.FederatedIdentity{Subject: "stable-sub-1", Email: "b@acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == seeded.ID {
		t.Fatal("a bare subject must NOT correlate (cross-IdP collision risk); email is the key")
	}
}

// TestSSORefusesSuperadmin proves an SSO completion never mints a session for a
// superadmin account (the cross-tenant/system root), even when an IdP asserts its
// email — the password/first-party bypass the review caught.
func TestSSORefusesSuperadmin(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@acme.com", "bootstrap-pass-123"); err != nil {
		t.Fatal(err)
	}
	// An IdP asserting the superadmin's email must be refused, not logged in.
	if _, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{
		Email: "root@acme.com", Subject: "attacker-sub",
	}, "10.0.0.9", "", false); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("SSO into superadmin err = %v, want ErrUnauthenticated (must never mint a system-root session via SSO)", err)
	}
}
