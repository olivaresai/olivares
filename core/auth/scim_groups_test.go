// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// mustMember creates a password-bearing user and, when role is non-empty,
// grants it role in tenant. It returns the user id and a fresh session
// principal (re-login after changing grants — principals are snapshots).
func mustMember(t *testing.T, ctx context.Context, a *auth.Authenticator, super auth.Principal, tenant model.TenantID, email, role string) (model.ID, auth.Principal) {
	t.Helper()
	u, err := a.CreateUser(ctx, super, auth.NewUser{Email: email, Password: "password-123"})
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	if role != "" {
		if _, err := a.GrantMembership(ctx, super, u.ID, tenant, role, model.ID("")); err != nil {
			t.Fatalf("grant %s: %v", email, err)
		}
	}
	return u.ID, loginPrincipal(t, ctx, a, email)
}

// loginPrincipal logs email in and authenticates the minted session.
func loginPrincipal(t *testing.T, ctx context.Context, a *auth.Authenticator, email string) auth.Principal {
	t.Helper()
	tok, _, err := a.Login(ctx, email, "password-123", "10.1.1.1")
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate %s: %v", email, err)
	}
	return p
}

func TestSCIMGroupCreateAndDedupe(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	if _, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{}); !errors.Is(err, auth.ErrInvalidScimGroup) {
		t.Fatalf("missing displayName err = %v, want ErrInvalidScimGroup", err)
	}

	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "ent-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.Group.MappedRole != "" {
		t.Errorf("MappedRole on create = %q, want empty (never settable inbound)", g.Group.MappedRole)
	}
	// Same externalId again (even under another name) is the IdP re-pushing the
	// same group: 409.
	if _, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering v2", ExternalID: "ent-1"}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate externalId err = %v, want ErrConflict", err)
	}
	// Entra legally provisions a duplicate displayName under a DIFFERENT
	// externalId: both coexist.
	if _, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "ent-2"}); err != nil {
		t.Errorf("duplicate displayName with distinct externalId: %v, want ok (Entra)", err)
	}
	// Without an externalId the displayName is the only correlation key (Okta):
	// a duplicate name is a 409 there.
	if _, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering"}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate displayName without externalId err = %v, want ErrConflict (Okta race)", err)
	}
	if _, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Sales"}); err != nil {
		t.Errorf("fresh displayName without externalId: %v", err)
	}
}

func TestSCIMGroupMemberSkipAndAudit(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	other := provisionTenant(t, st, "other")

	memberID, _ := mustMember(t, ctx, a, super, tenant, "in@acme.com", auth.RoleViewer)
	nonMemberID, _ := mustMember(t, ctx, a, super, tenant, "nowhere@x.com", "")
	foreignID, _ := mustMember(t, ctx, a, super, other, "foreign@other.com", auth.RoleViewer)

	// A non-member, another tenant's member and a bogus id are all skipped the
	// SAME way (no oracle); a repeated id is one member, not a skip.
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Engineering", ExternalID: "ent-1",
		Members: []model.ID{memberID, memberID, nonMemberID, foreignID, model.ID("0190a000-0000-7000-8000-000000000000")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Members) != 1 || g.Members[0].ID != memberID {
		t.Errorf("members = %+v, want exactly the tenant member", g.Members)
	}
	if g.SkippedMembers != 3 {
		t.Errorf("SkippedMembers = %d, want 3", g.SkippedMembers)
	}

	// The skip is on the audit trail (never-silent): the create event's meta
	// carries the counts.
	var sawCounts bool
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		cw, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose WalkCanonical")
		}
		return cw.WalkCanonical(ctx, 1, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			if ev.Action == "scim.group.create" &&
				strings.Contains(metaCanonical, `"skipped_members":3`) &&
				strings.Contains(metaCanonical, `"members":1`) {
				sawCounts = true
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !sawCounts {
		t.Error("no scim.group.create audit event with members/skipped_members counts")
	}
}

func TestSCIMGroupCrossTenantIsolation(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	other := provisionTenant(t, st, "other")

	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "ent-1"})
	if err != nil {
		t.Fatal(err)
	}
	id := g.Group.ID

	// All auth rows share the system tenant: ONLY the target_tenant_id filter
	// isolates tenants here. From another tenant the group must be invisible and
	// immutable, indistinguishable from absent.
	if _, err := a.SCIMGetGroup(ctx, other, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant get err = %v, want ErrNotFound", err)
	}
	if _, err := a.SCIMReplaceGroup(ctx, super, other, id, auth.SCIMGroupInput{DisplayName: "Stolen"}, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant replace err = %v, want ErrNotFound", err)
	}
	if err := a.SCIMDeleteGroup(ctx, super, other, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant delete err = %v, want ErrNotFound", err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, other, id, auth.RoleViewer); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant role map err = %v, want ErrNotFound", err)
	}
	if gs, err := a.SCIMListGroups(ctx, other); err != nil || len(gs) != 0 {
		t.Errorf("cross-tenant list = (%d groups, %v), want empty", len(gs), err)
	}
	// And the group is untouched in its own tenant.
	got, err := a.SCIMGetGroup(ctx, tenant, id)
	if err != nil || got.Group.DisplayName != "Engineering" {
		t.Errorf("own-tenant get after cross-tenant attempts = (%+v, %v)", got.Group, err)
	}
}

func TestSCIMGroupReplacePreservesMappedRole(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u1, _ := mustMember(t, ctx, a, super, tenant, "u1@acme.com", auth.RoleViewer)
	u2, _ := mustMember(t, ctx, a, super, tenant, "u2@acme.com", auth.RoleViewer)

	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Engineering", ExternalID: "ent-1", Members: []model.ID{u1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatal(err)
	}

	if _, err := a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{}, 0); !errors.Is(err, auth.ErrInvalidScimGroup) {
		t.Errorf("replace without displayName err = %v, want ErrInvalidScimGroup", err)
	}
	// A full inbound replace (new name, new externalId, swapped roster) must NOT
	// touch the operator's role mapping.
	rep, err := a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{
		DisplayName: "Engineering EMEA", ExternalID: "ent-9", Members: []model.ID{u2},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Group.MappedRole != auth.RoleEditor {
		t.Errorf("MappedRole after replace = %q, want %q (preserved)", rep.Group.MappedRole, auth.RoleEditor)
	}
	if rep.Group.DisplayName != "Engineering EMEA" || rep.Group.ExternalID != "ent-9" {
		t.Errorf("attributes after replace = %+v", rep.Group)
	}
	if len(rep.Members) != 1 || rep.Members[0].ID != u2 {
		t.Errorf("members after replace = %+v, want exactly u2", rep.Members)
	}
	// Removing again (empty roster) is idempotent and keeps the mapping.
	rep, err = a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{DisplayName: "Engineering EMEA", ExternalID: "ent-9"}, 0)
	if err != nil || len(rep.Members) != 0 || rep.Group.MappedRole != auth.RoleEditor {
		t.Errorf("empty replace = (%+v, %v)", rep, err)
	}
}

func TestSCIMGroupMemberAddCeiling(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u1, _ := mustMember(t, ctx, a, super, tenant, "u1@acme.com", auth.RoleViewer)
	u2, _ := mustMember(t, ctx, a, super, tenant, "u2@acme.com", auth.RoleViewer)
	_, admin := mustMember(t, ctx, a, super, tenant, "adm@acme.com", auth.RoleAdmin)

	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Owners", ExternalID: "own-1", Members: []model.ID{u1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleOwner); err != nil {
		t.Fatal(err)
	}

	// Adding a member to an owner-mapped group IS granting owner: an admin actor
	// is over its ceiling (vertical privesc guard), and nothing is written.
	if _, err := a.SCIMReplaceGroup(ctx, admin, tenant, g.Group.ID, auth.SCIMGroupInput{
		DisplayName: "Owners", ExternalID: "own-1", Members: []model.ID{u1, u2},
	}, 0); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatalf("admin adding to owner-mapped group err = %v, want ErrRoleCeiling", err)
	}
	got, err := a.SCIMGetGroup(ctx, tenant, g.Group.ID)
	if err != nil || len(got.Members) != 1 {
		t.Errorf("roster after refused add = (%d members, %v), want unchanged 1", len(got.Members), err)
	}
	// The same admin may REMOVE (narrowing) and may edit attributes without adds.
	if _, err := a.SCIMReplaceGroup(ctx, admin, tenant, g.Group.ID, auth.SCIMGroupInput{
		DisplayName: "Owners (renamed)", ExternalID: "own-1",
	}, 0); err != nil {
		t.Errorf("admin narrowing an owner-mapped group: %v", err)
	}
	// A superadmin passes the ceiling.
	if _, err := a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{
		DisplayName: "Owners (renamed)", ExternalID: "own-1", Members: []model.ID{u1, u2},
	}, 0); err != nil {
		t.Errorf("superadmin adding to owner-mapped group: %v", err)
	}
}

func TestConfigureGroupRole(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	_, admin := mustMember(t, ctx, a, super, tenant, "adm@acme.com", auth.RoleAdmin)

	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "ent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, "captain"); !errors.Is(err, auth.ErrInvalidGroupRole) {
		t.Errorf("unknown role err = %v, want ErrInvalidGroupRole", err)
	}
	// An admin may map roles up to its own rank, not above.
	if _, err := a.ConfigureGroupRole(ctx, admin, tenant, g.Group.ID, auth.RoleOwner); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Errorf("admin mapping owner err = %v, want ErrRoleCeiling", err)
	}
	mapped, err := a.ConfigureGroupRole(ctx, admin, tenant, g.Group.ID, auth.RoleEditor)
	if err != nil || mapped.MappedRole != auth.RoleEditor {
		t.Errorf("admin mapping editor = (%q, %v)", mapped.MappedRole, err)
	}
	// Superadmin may map owner; clearing needs no ceiling (it only narrows).
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleOwner); err != nil {
		t.Fatal(err)
	}
	cleared, err := a.ConfigureGroupRole(ctx, admin, tenant, g.Group.ID, "")
	if err != nil || cleared.MappedRole != "" {
		t.Errorf("clear mapping = (%q, %v), want empty", cleared.MappedRole, err)
	}
}

func TestGroupMappingFoldsIntoGrants(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	uViewer, _ := mustMember(t, ctx, a, super, tenant, "viewer@acme.com", auth.RoleViewer)
	uOwner, _ := mustMember(t, ctx, a, super, tenant, "owner@acme.com", auth.RoleOwner)
	uOutsider, _ := mustMember(t, ctx, a, super, tenant, "outsider@x.com", "")

	adminGroup, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Admins", ExternalID: "adm-1", Members: []model.ID{uViewer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, adminGroup.Group.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	viewerGroup, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Everyone", ExternalID: "all-1", Members: []model.ID{uOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, viewerGroup.Group.ID, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	// Seed a member row for a user WITHOUT a direct membership straight into the
	// store (the SCIM path would have skipped it): the fold must still grant
	// NOTHING — groups elevate, they never admit.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.GroupMembers().Create(ctx, model.UserGroupMember{
			GroupID: adminGroup.Group.ID, UserID: uOutsider,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// viewer + admin-mapped group => the session principal acts as admin.
	p := loginPrincipal(t, ctx, a, "viewer@acme.com")
	if r, ok := p.RoleIn(tenant); !ok || r != auth.RoleAdmin {
		t.Errorf("viewer in admin-mapped group RoleIn = (%q, %v), want admin", r, ok)
	}
	// owner + viewer-mapped group => the higher direct role is never downgraded.
	p = loginPrincipal(t, ctx, a, "owner@acme.com")
	if r, ok := p.RoleIn(tenant); !ok || r != auth.RoleOwner {
		t.Errorf("owner in viewer-mapped group RoleIn = (%q, %v), want owner", r, ok)
	}
	// group member with NO direct membership => no grant at all in the tenant.
	p = loginPrincipal(t, ctx, a, "outsider@x.com")
	if p.IsMember(tenant) {
		t.Error("group membership alone granted tenant access; the per-tenant gate must deny it")
	}
}

func TestSCIMDeprovisionCleansGroupRows(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	other := provisionTenant(t, st, "other")

	uid, _ := mustMember(t, ctx, a, super, tenant, "leaver@acme.com", auth.RoleViewer)
	if _, err := a.GrantMembership(ctx, super, uid, other, auth.RoleViewer, model.ID("")); err != nil {
		t.Fatal(err)
	}
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Admins", ExternalID: "adm-1", Members: []model.ID{uid},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	og, err := a.SCIMCreateGroup(ctx, super, other, auth.SCIMGroupInput{
		DisplayName: "Other folks", ExternalID: "oth-1", Members: []model.ID{uid},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: the mapping elevates while the user is a member.
	if r, _ := loginPrincipal(t, ctx, a, "leaver@acme.com").RoleIn(tenant); r != auth.RoleAdmin {
		t.Fatalf("pre-deprovision role = %q, want admin", r)
	}

	if err := a.SCIMDeprovisionUser(ctx, super, tenant, uid); err != nil {
		t.Fatal(err)
	}
	// The leaver left this tenant's rosters too — otherwise a later re-join
	// would silently re-elevate it to admin through the stale row.
	got, err := a.SCIMGetGroup(ctx, tenant, g.Group.ID)
	if err != nil || len(got.Members) != 0 {
		t.Errorf("group roster after deprovision = (%d members, %v), want empty", len(got.Members), err)
	}
	// The OTHER tenant's roster is untouched (a tenant's leaver path never edits
	// foreign rosters).
	if gotOther, err := a.SCIMGetGroup(ctx, other, og.Group.ID); err != nil || len(gotOther.Members) != 1 {
		t.Errorf("other tenant's roster after deprovision = (%+v, %v), want 1 member", gotOther.Members, err)
	}
	// Re-joining grants exactly the new membership's role — no resurrection.
	if _, err := a.GrantMembership(ctx, super, uid, tenant, auth.RoleViewer, model.ID("")); err != nil {
		t.Fatal(err)
	}
	if r, _ := loginPrincipal(t, ctx, a, "leaver@acme.com").RoleIn(tenant); r != auth.RoleViewer {
		t.Errorf("post-rejoin role = %q, want viewer (no stale re-elevation)", r)
	}
}
