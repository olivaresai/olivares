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

// The group-origin contract (generic seam). The two witnesses that matter
// most run FIRST: every pre-existing SCIM/operator behavior on a NULL-origin
// group is exactly today's (the seam adds a column, not a behavior change),
// and then the three boundaries — SCIM replace/delete, the console mappings,
// and the login reconcile — each refuse or skip a claimed group.

// setGroupOrigin writes a group's origin directly through the store, the way
// a provisioner wired at boot does (no Community HTTP path sets it).
func setGroupOrigin(t *testing.T, st store.Store, tenant model.TenantID, groupID model.ID, origin string) {
	t.Helper()
	if err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		g, err := as.Groups().Get(context.Background(), groupID)
		if err != nil {
			return err
		}
		if !mayAdoptGroupOriginForTest(g.ProvisionedBy, origin) {
			return auth.ErrGroupOriginAdopted
		}
		g.ProvisionedBy = origin
		_, err = as.Groups().Update(context.Background(), g)
		return err
	}); err != nil {
		t.Fatalf("set origin %q: %v", origin, err)
	}
}

// mayAdoptGroupOriginForTest mirrors the adoption rule the provisioner-side
// guard enforces (core/auth/groups_origin.go); it is restated here so the test
// pins the rule through behavior below rather than calling unexported code.
func mayAdoptGroupOriginForTest(from, to string) bool {
	if from == to {
		return true
	}
	return from == ""
}

func TestGroupProvisionerVocabulary(t *testing.T) {
	for _, ok := range []string{"", "operator", "a", "abc-123", "ldap"} {
		if err := auth.ValidateGroupProvisioner(ok); err != nil {
			t.Errorf("ValidateGroupProvisioner(%q) = %v, want accepted", ok, err)
		}
	}
	for _, bad := range []string{"Operator", "1abc", "-abc", "abc_", "abc x", "über"} {
		if err := auth.ValidateGroupProvisioner(bad); err == nil {
			t.Errorf("ValidateGroupProvisioner(%q) accepted", bad)
		}
	}
}

func TestQualifyGroupExternalID(t *testing.T) {
	// Two connectors reusing the same local id must not merge under the
	// per-tenant (target_tenant_id, external_id) unique index.
	a := auth.QualifyGroupExternalID("018f2c2e-0000-7000-8000-000000000001", "grp-7")
	b := auth.QualifyGroupExternalID("018f2c2e-0000-7000-8000-000000000002", "grp-7")
	if a == b {
		t.Fatalf("two connectors produced the same qualified id %q", a)
	}
	// The same connector is stable across syncs (correlation survives).
	if again := auth.QualifyGroupExternalID("018f2c2e-0000-7000-8000-000000000001", "grp-7"); again != a {
		t.Fatalf("qualification is not stable: %q vs %q", again, a)
	}
	// The SCIM path (no connector uuid) stores ids unqualified — today's rows
	// keep correlating.
	if got := auth.QualifyGroupExternalID("", "grp-7"); got != "grp-7" {
		t.Fatalf("empty connector qualified the id: %q", got)
	}
	uuid, local := auth.UnqualifyGroupExternalID(a)
	if uuid != "018f2c2e-0000-7000-8000-000000000001" || local != "grp-7" {
		t.Fatalf("unqualified to %q / %q", uuid, local)
	}
	if u, l := auth.UnqualifyGroupExternalID("grp-7"); u != "" || l != "grp-7" {
		t.Fatalf("legacy id unqualified to %q / %q", u, l)
	}
}

func TestGroupOriginNullRowKeepsTodaysBehavior(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "origin-today")

	// SCIM creates (NULL origin by construction), replaces and deletes as it
	// always did; the console maps and nests as it always did.
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Eng", ExternalID: "grp-eng"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.Group.ProvisionedBy != "" {
		t.Fatalf("SCIM create set an origin %q; a provisioned-by-IdP row must be NULL", g.Group.ProvisionedBy)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatalf("map role on NULL-origin group: %v", err)
	}
	if _, err := a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{DisplayName: "Engineering", ExternalID: "grp-eng"}, 0); err != nil {
		t.Fatalf("replace NULL-origin group: %v", err)
	}
	if err := a.SCIMDeleteGroup(ctx, super, tenant, g.Group.ID); err != nil {
		t.Fatalf("delete NULL-origin group: %v", err)
	}
}

func TestGroupOriginSCIMBoundary(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "origin-scim")

	for _, origin := range []string{"operator", "ldap"} {
		g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
			DisplayName: "G-" + origin, ExternalID: "grp-" + origin,
		})
		if err != nil {
			t.Fatalf("create %s: %v", origin, err)
		}
		setGroupOrigin(t, st, tenant, g.Group.ID, origin)

		if _, err := a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{
			DisplayName: "Renamed", ExternalID: "grp-" + origin,
		}, 0); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
			t.Fatalf("SCIM replace on %q-origin group err = %v, want ErrGroupOriginReadOnly", origin, err)
		}
		if err := a.SCIMDeleteGroup(ctx, super, tenant, g.Group.ID); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
			t.Fatalf("SCIM delete on %q-origin group err = %v, want ErrGroupOriginReadOnly", origin, err)
		}
	}
}

func TestGroupOriginConsoleBoundary(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "origin-console")

	// The console may write NULL and its own origin; a named provisioner's
	// group is read-only to it.
	local, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Local", ExternalID: "grp-local"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setGroupOrigin(t, st, tenant, local.Group.ID, "operator")
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, local.Group.ID, auth.RoleViewer); err != nil {
		t.Fatalf("map role on operator group: %v", err)
	}
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, local.Group.ID, model.ID("")); err != nil {
		t.Fatalf("clear parent on operator group: %v", err)
	}

	foreign, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Foreign", ExternalID: "grp-foreign"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setGroupOrigin(t, st, tenant, foreign.Group.ID, "ldap")
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, foreign.Group.ID, auth.RoleViewer); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
		t.Fatalf("map role on named-origin group err = %v, want ErrGroupOriginReadOnly", err)
	}
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, foreign.Group.ID, local.Group.ID); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
		t.Fatalf("nest named-origin group err = %v, want ErrGroupOriginReadOnly", err)
	}
}

// The login-time reconcile adds an asserted membership to the NULL-origin
// group it always did, and SKIPS a claimed group instead of erroring.
func TestGroupOriginReconcileBoundary(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil).WithGroupMapper(fakeGroupMapper{})
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "origin-reconcile")
	seedConfig(t, st, tenant, "default", "https://idp.origin.test", "origin.test")

	uid, _ := mustMember(t, ctx, a, super, tenant, "eng@origin.test", auth.RoleViewer)
	idp, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "IdP-Eng", ExternalID: "grp-idp"})
	if err != nil {
		t.Fatalf("create idp group: %v", err)
	}
	_ = idp
	local, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Local-Eng", ExternalID: "grp-local"})
	if err != nil {
		t.Fatalf("create local group: %v", err)
	}
	setGroupOrigin(t, st, tenant, local.Group.ID, "operator")

	asserted := []string{"grp-idp", "grp-local"}
	fed := auth.FederatedIdentity{Subject: "eng-ext", Email: "eng@origin.test", Groups: asserted}
	if _, _, err := a.CompleteSSO(ctx, fed, "10.1.1.9", tenant, false); err != nil {
		t.Fatalf("CompleteSSO: %v", err)
	}
	// The NULL-origin assertion landed; the operator one did not.
	memberOf := func(gid model.ID) bool {
		found := false
		if err := st.AuthView(ctx, func(as store.AuthScope) error {
			rows, _, err := as.GroupMembers().List(ctx, model.Query{
				Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: uid.String()}},
			})
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.GroupID == gid {
					found = true
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("list memberships: %v", err)
		}
		return found
	}
	if !memberOf(idp.Group.ID) {
		t.Fatal("the IdP-managed group did not receive the asserted membership")
	}
	if memberOf(local.Group.ID) {
		t.Fatal("the operator-managed group received an IdP-asserted membership")
	}
}
