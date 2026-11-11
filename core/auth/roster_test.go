// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/auth/authtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestTenantRosterEnrichesAndIsolates: the members roster returns each tenant
// member with the effective role from their membership and the directory groups
// they belong to, and NEVER another tenant's members (the reconnaissance guard).
func TestTenantRosterEnrichesAndIsolates(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	acme := provisionTenant(t, st, "acme")
	globex := provisionTenant(t, st, "globex")

	// acme: an editor and a viewer.
	ed, err := a.CreateUser(ctx, super, auth.NewUser{Email: "ed@acme.com", DisplayName: "Ed", Password: "dev-password-1", Tenant: acme, Role: auth.RoleEditor})
	if err != nil {
		t.Fatalf("create ed: %v", err)
	}
	if _, err := a.CreateUser(ctx, super, auth.NewUser{Email: "vw@acme.com", DisplayName: "Vi", Password: "dev-password-1", Tenant: acme, Role: auth.RoleViewer}); err != nil {
		t.Fatalf("create vw: %v", err)
	}

	// globex: an owner that must never surface in acme's roster.
	if _, err := a.CreateUser(ctx, super, auth.NewUser{Email: "ow@globex.com", DisplayName: "Ow", Password: "dev-password-1", Tenant: globex, Role: auth.RoleOwner}); err != nil {
		t.Fatalf("create ow: %v", err)
	}

	// A directory group in acme with ed as its only member.
	authtest.Group(t, st, acme, "Engineering", "", ed.ID)

	roster, err := a.TenantRoster(ctx, acme)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	if len(roster) != 2 {
		t.Fatalf("acme roster = %d members, want 2: %+v", len(roster), roster)
	}
	byEmail := map[string]auth.RosterMember{}
	for _, m := range roster {
		if m.User.Email == "ow@globex.com" {
			t.Fatalf("tenant leak: acme roster contains a globex member")
		}
		byEmail[m.User.Email] = m
	}
	if got := byEmail["ed@acme.com"].Role; got != auth.RoleEditor {
		t.Errorf("ed role = %q, want editor", got)
	}
	if gs := byEmail["ed@acme.com"].Groups; len(gs) != 1 || gs[0] != "Engineering" {
		t.Errorf("ed groups = %v, want [Engineering]", gs)
	}
	if gs := byEmail["vw@acme.com"].Groups; len(gs) != 0 {
		t.Errorf("vw groups = %v, want none", gs)
	}

	// globex sees only its owner.
	gRoster, err := a.TenantRoster(ctx, globex)
	if err != nil {
		t.Fatalf("globex roster: %v", err)
	}
	if len(gRoster) != 1 || gRoster[0].User.Email != "ow@globex.com" || gRoster[0].Role != auth.RoleOwner {
		t.Fatalf("globex roster = %+v, want a single owner", gRoster)
	}
}

// TestTenantRosterRejectsSystemAndZero: the roster is a business-tenant read; the
// system tenant (where the rows physically live) and the zero tenant are never
// enumerable through it.
func TestTenantRosterRejectsSystemAndZero(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)

	if r, err := a.TenantRoster(ctx, model.SystemTenantID); err != nil || len(r) != 0 {
		t.Errorf("system-tenant roster = (%d, %v), want (0, nil)", len(r), err)
	}
	if r, err := a.TenantRoster(ctx, model.TenantID("")); err != nil || len(r) != 0 {
		t.Errorf("zero-tenant roster = (%d, %v), want (0, nil)", len(r), err)
	}
}

// A suspension exclusion refuses the member's session in the tenant at once and
// marks the member in the roster, and lifting it restores both: the membership,
// role and group places never moved.
func TestSuspensionExclusionRefusesAtOnceAndLiftsCleanly(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	acme := provisionTenant(t, st, "acme")
	const password = "dev-password-1"
	ed, err := a.CreateUser(ctx, super, auth.NewUser{Email: "ed@acme.com", Password: password, Tenant: acme, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser(ctx, super, auth.NewUser{Email: "vw@acme.com", Password: password, Tenant: acme, Role: auth.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Login(ctx, "ed@acme.com", password, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	member := func() bool {
		p, err := a.Authenticate(ctx, token)
		if err != nil {
			return false
		}
		_, ok := p.RoleIn(acme)
		return ok
	}
	suspended := func() map[string]bool {
		roster, err := a.TenantRoster(ctx, acme)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, m := range roster {
			out[m.User.Email] = m.Suspended
		}
		return out
	}
	if !member() {
		t.Fatal("control: ed is not a member before the suspension")
	}

	var exclusion model.TenantExclusion
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		exclusion, err = as.TenantExclusions().Create(ctx, model.TenantExclusion{
			UserID: ed.ID, TargetTenantID: acme, Kind: model.ExclusionSuspend, CreatedBy: super.Actor(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if member() {
		t.Error("a suspended member's session still reaches the tenant")
	}
	if got := suspended(); !got["ed@acme.com"] || got["vw@acme.com"] {
		t.Errorf("roster suspended = %v, want only ed", got)
	}

	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.TenantExclusions().Delete(ctx, exclusion.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if !member() {
		t.Error("lifting the suspension did not restore the session")
	}
	if got := suspended(); got["ed@acme.com"] {
		t.Errorf("roster still marks ed suspended after the lift: %v", got)
	}
}
