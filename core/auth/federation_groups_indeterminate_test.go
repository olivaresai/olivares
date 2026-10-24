// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Group overage. An IdP that cannot send a user's whole group list (Entra above 200
// groups in a token, 150 in a SAML assertion) says so, and the provider carries that as
// FederatedIdentity.GroupsIndeterminate. Where CompleteSSO would have used the asserted
// groups, "unknown" must not be read as "no groups": the user never gets the membership
// a deny rule on that group relies on, so the rule would silently stop applying.

const overageEmail = "eng@acme.com"

// overageFixture is a tenant with an IdP that claims the member's domain, one member
// with a base viewer role, and the "engineering" group (mapped to editor) the IdP
// would assert, with the enterprise group mapper wired.
type overageFixture struct {
	*scopeFixture
	tenant model.TenantID
	group  model.ID
}

func newOverageFixture(t *testing.T) *overageFixture {
	t.Helper()
	ctx := context.Background()
	f := newScopeFixture(t)
	f.a.WithGroupMapper(fakeGroupMapper{})
	tenant := provisionTenant(t, f.st, "acme")
	seedConfig(t, f.st, tenant, "default", "https://idp.acme.test", "acme.com")
	mustMember(t, ctx, f.a, f.super, tenant, overageEmail, auth.RoleViewer)
	g, err := f.a.SCIMCreateGroup(ctx, f.super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "grp-eng"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ConfigureGroupRole(ctx, f.super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatal(err)
	}
	return &overageFixture{scopeFixture: f, tenant: tenant, group: g.Group.ID}
}

// overage is the identity an Entra tenant produces for a user with too many groups:
// the groups claim is absent and the provider flags it.
func overage(email string) auth.FederatedIdentity {
	return auth.FederatedIdentity{Email: email, Subject: "eng-subject", GroupsIndeterminate: true}
}

func (f *overageFixture) groups(t *testing.T) []string {
	t.Helper()
	return loginPrincipal(t, context.Background(), f.a, overageEmail).GroupsIn(f.tenant)
}

// TestCompleteSSORefusesIndeterminateGroups is the fix: with the mapper wired, a
// tenant-scoped sign-in whose groups are indeterminate is refused with the stable
// error, no session, no account, and exactly one audit record that carries no
// assertion data.
func TestCompleteSSORefusesIndeterminateGroups(t *testing.T) {
	ctx := context.Background()

	t.Run("an existing member gets no session", func(t *testing.T) {
		f := newOverageFixture(t)
		user, _ := f.account(t, overageEmail)
		sessionsBefore := f.sessions(t, user.ID)
		seq := auditHead(t, ctx, f.st)

		tok, sess, err := f.a.CompleteSSO(ctx, overage(overageEmail), scopeIP, f.tenant, false)
		if !errors.Is(err, auth.ErrSSOGroupsIndeterminate) {
			t.Fatalf("CompleteSSO = %v, want ErrSSOGroupsIndeterminate", err)
		}
		if tok != "" || !sess.ID.IsZero() {
			t.Fatalf("a refused sign-in issued a credential (token issued=%t, session=%s)", tok != "", sess.ID)
		}
		if n := f.sessions(t, user.ID); n != sessionsBefore {
			t.Fatalf("refused sign-in changed the user's sessions from %d to %d", sessionsBefore, n)
		}
		blocked := f.blockedSince(t, seq, "sso_groups_indeterminate")
		if len(blocked) != 1 {
			t.Fatalf("refusal left %d audit records, want exactly 1", len(blocked))
		}
		keys := make([]string, 0, len(blocked[0].Meta))
		for k := range blocked[0].Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if want := []string{"ip", "reason", "scope"}; len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
			t.Fatalf("audit meta keys = %v, want %v (nothing from the assertion)", keys, want)
		}
		if got := blocked[0].Meta["scope"]; got != f.tenant.String() {
			t.Fatalf("audit scope = %v, want %s", got, f.tenant)
		}
		if blocked[0].Actor != "anonymous" {
			t.Fatalf("audit actor = %q, want anonymous (no account is resolved or provisioned)", blocked[0].Actor)
		}
	})

	t.Run("a new account is not provisioned", func(t *testing.T) {
		f := newOverageFixture(t)
		const fresh = "new@acme.com"
		if _, _, err := f.a.CompleteSSO(ctx, overage(fresh), scopeIP, f.tenant, false); !errors.Is(err, auth.ErrSSOGroupsIndeterminate) {
			t.Fatalf("CompleteSSO = %v, want ErrSSOGroupsIndeterminate", err)
		}
		if _, exists := f.account(t, fresh); exists {
			t.Fatal("a refused sign-in provisioned an account")
		}
	})

	t.Run("groups that arrive with the marker are not trusted", func(t *testing.T) {
		f := newOverageFixture(t)
		id := overage(overageEmail)
		id.Groups = []string{"grp-eng"} // a partial list: UserInfo supplied some, the rest is missing
		if _, _, err := f.a.CompleteSSO(ctx, id, scopeIP, f.tenant, false); !errors.Is(err, auth.ErrSSOGroupsIndeterminate) {
			t.Fatalf("CompleteSSO = %v, want ErrSSOGroupsIndeterminate", err)
		}
		if got := f.groups(t); len(got) != 0 {
			t.Fatalf("a refused login reconciled a partial list: GroupsIn = %v", got)
		}
	})

	t.Run("the network allow-list answers first", func(t *testing.T) {
		f := newOverageFixture(t)
		f.a.WithLoginPolicy(&fakeLoginPolicy{networkErr: auth.ErrNetworkNotAllowed})
		seq := auditHead(t, ctx, f.st)
		if _, _, err := f.a.CompleteSSO(ctx, overage(overageEmail), scopeIP, f.tenant, false); !errors.Is(err, auth.ErrNetworkNotAllowed) {
			t.Fatalf("CompleteSSO = %v, want ErrNetworkNotAllowed: a peer outside the allow-list learns nothing about groups", err)
		}
		if n := len(f.blockedSince(t, seq, "sso_groups_indeterminate")); n != 0 {
			t.Fatalf("%d groups refusal records for a peer the allow-list refused", n)
		}
	})

	t.Run("the same login with its groups is admitted and mapped", func(t *testing.T) {
		f := newOverageFixture(t)
		id := auth.FederatedIdentity{Email: overageEmail, Subject: "eng-subject", Groups: []string{"grp-eng"}}
		if _, _, err := f.a.CompleteSSO(ctx, id, scopeIP, f.tenant, false); err != nil {
			t.Fatalf("a login that carries its groups was refused: %v", err)
		}
		if got := f.groups(t); len(got) != 1 || got[0] != f.group.String() {
			t.Fatalf("GroupsIn = %v, want [%s]", got, f.group)
		}
	})

	t.Run("a user with no groups at all is not indeterminate", func(t *testing.T) {
		f := newOverageFixture(t)
		id := auth.FederatedIdentity{Email: overageEmail, Subject: "eng-subject"}
		if _, _, err := f.a.CompleteSSO(ctx, id, scopeIP, f.tenant, false); err != nil {
			t.Fatalf("an empty group list is a known answer and must sign in: %v", err)
		}
	})
}

// TestCompleteSSOIndeterminateGroupsAreOnlyRefusedWhereTheyWouldBeUsed pins the
// predicate shared with the reconciliation: where CompleteSSO never reads the asserted
// groups, an unreadable list changes nothing, so those logins are admitted and leave no
// refusal record.
func TestCompleteSSOIndeterminateGroupsAreOnlyRefusedWhereTheyWouldBeUsed(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		open  bool // the open build: no group mapper wired
		scope func(*overageFixture) model.TenantID
		scim  bool
	}{
		{"the open build has no mapper and uses no asserted groups", true, func(f *overageFixture) model.TenantID { return f.tenant }, false},
		{"SCIM is authoritative for groups", false, func(f *overageFixture) model.TenantID { return f.tenant }, true},
		{"the deployment-wide provider does not map groups", false, func(*overageFixture) model.TenantID { return auth.GlobalFederationScope }, false},
		{"an empty scope does not map groups", false, func(*overageFixture) model.TenantID { return "" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOverageFixture(t)
			a := f.a
			if tc.open {
				a = auth.NewAuthenticator(f.st, nil)
			}
			seq := auditHead(t, ctx, f.st)
			if _, _, err := a.CompleteSSO(ctx, overage(overageEmail), scopeIP, tc.scope(f), tc.scim); err != nil {
				t.Fatalf("login refused where the asserted groups are not used: %v", err)
			}
			if n := len(f.blockedSince(t, seq, "sso_groups_indeterminate")); n != 0 {
				t.Fatalf("%d groups refusal records for an admitted login", n)
			}
		})
	}
}
