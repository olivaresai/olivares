// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The batched grant read (2026-10-02, B9 follow-through): loadGrants prefetches
// the group rows with IN queries instead of one Get per group. Grants are
// security code — this test proves the batched read returns the IDENTICAL grant
// set as the per-row walk it replaced, over a fixture exercising every shape the
// walk has a rule for: a nesting chain, a shared ancestor, a cycle, a
// cross-tenant parent edge, a dangling member row, mapped-role elevation, the
// per-tenant gate (a group confers nothing where the user holds no direct
// membership) and a workspace-confined membership. (Memberships carry no expiry
// and groups no deny flag in this store, so "expired" and "deny" cannot change
// the result set; the shapes that DO reach it are all present.)

// loadGrantsReference is the PRE-BATCH algorithm, frozen verbatim from the
// 2026-10-01 loadGrants, for the equality check. It deliberately does NOT
// prefill: every group resolves through one Get per row, the old N+1.
func loadGrantsReference(ctx context.Context, as store.AuthScope, userID model.ID) (map[model.TenantID]string, map[model.TenantID][]string, map[model.TenantID]model.ID, error) {
	ms, err := drainList(ctx, as.Memberships().List, byEq("user_id", userID.String(), 0))
	if err != nil {
		return nil, nil, nil, err
	}
	g := make(map[model.TenantID]string, len(ms))
	var confined map[model.TenantID]model.ID
	for _, m := range ms {
		if IsRole(m.Role) {
			g[m.TargetTenantID] = m.Role
		}
		if !m.WorkspaceID.IsZero() {
			if confined == nil {
				confined = make(map[model.TenantID]model.ID, 1)
			}
			confined[m.TargetTenantID] = m.WorkspaceID
		}
	}
	rows, err := drainList(ctx, as.GroupMembers().List, byEq("user_id", userID.String(), 0))
	if err != nil {
		return nil, nil, nil, err
	}
	cache := make(map[model.ID]*model.UserGroup, len(rows))
	var subjectGroups map[model.TenantID][]string
	seen := map[model.ID]bool{}
	for _, r := range rows {
		grp, err := groupByID(ctx, as, cache, r.GroupID)
		if err != nil {
			return nil, nil, nil, err
		}
		if grp == nil {
			continue
		}
		cur, member := g[grp.TargetTenantID]
		if !member {
			continue
		}
		mapped, e := loadGroupClosure(ctx, as, cache, grp, seen, &subjectGroups)
		if e != nil {
			return nil, nil, nil, e
		}
		if IsRole(mapped) && RoleRank(mapped) > RoleRank(cur) {
			g[grp.TargetTenantID] = mapped
		}
	}
	return g, subjectGroups, confined, nil
}

func grantTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func grantTestTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(context.Background()); e != nil {
			return e
		}
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("provision tenant %s: %v", slug, err)
	}
	return tenant
}

func TestBatchedGrantReadMatchesThePerRowWalk(t *testing.T) {
	ctx := context.Background()
	st := grantTestStore(t)
	t1 := grantTestTenant(t, st, "eq-one")
	t2 := grantTestTenant(t, st, "eq-two")
	t3 := grantTestTenant(t, st, "eq-three")
	ws := model.NewID()

	var user model.ID
	groups := map[string]model.ID{}
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		hash, err := HashPassword("supersecret1")
		if err != nil {
			return err
		}
		u, err := as.Users().Create(ctx, model.User{
			Email: "eq@x.io", DisplayName: "Eq", Status: model.StatusActive, PasswordHash: hash,
		})
		if err != nil {
			return fmt.Errorf("user create: %w", err)
		}
		user = u.ID
		// Direct grants: viewer in t1; editor in t2 confined to one workspace; a
		// non-role row in t2 that grants nothing (IsRole is false).
		if _, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: t1, Role: RoleViewer}); err != nil {
			return fmt.Errorf("membership t1: %w", err)
		}
		if _, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: t2, Role: RoleEditor, WorkspaceID: ws}); err != nil {
			return fmt.Errorf("membership t2: %w", err)
		}
		// A non-role row grants nothing (IsRole is false) — in t3, so the gate
		// shape and this shape stay distinct (one membership per tenant is unique).
		if _, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: t3, Role: "not-a-role"}); err != nil {
			return fmt.Errorf("membership t3: %w", err)
		}
		mk := func(name string, tenant model.TenantID, parent model.ID, mapped string) error {
			g, err := as.Groups().Create(ctx, model.UserGroup{TargetTenantID: tenant, DisplayName: name, ParentGroupID: parent, MappedRole: mapped})
			if err != nil {
				return fmt.Errorf("mk %s: %w", name, err)
			}
			groups[name] = g.ID
			return nil
		}
		// t1: the chain A→C→D, B under the shared ancestor C, the cycle K↔L, and
		// E whose parent edge crosses into t3 (ended, never followed).
		for _, n := range []string{"A", "B", "C", "D", "K", "L", "E"} {
			mapped := ""
			if n == "A" {
				mapped = RoleEditor
			}
			if n == "C" {
				mapped = RoleAdmin
			}
			if err := mk(n, t1, "", mapped); err != nil {
				return err
			}
		}
		// t3: G has MappedRole owner and the user is a member — but the user holds
		// NO direct t3 membership, so the per-tenant gate refuses all of it.
		if err := mk("G", t3, "", RoleOwner); err != nil {
			return err
		}
		link := func(child, parent model.ID) error {
			g, err := as.Groups().Get(ctx, child)
			if err != nil {
				return fmt.Errorf("link get child=%s parent=%s: %w", child, parent, err)
			}
			g.ParentGroupID = parent
			_, err = as.Groups().Update(ctx, g)
			return err
		}
		if err := link(groups["A"], groups["C"]); err != nil {
			return err
		}
		if err := link(groups["B"], groups["C"]); err != nil {
			return err
		}
		if err := link(groups["C"], groups["D"]); err != nil {
			return err
		}
		if err := link(groups["K"], groups["L"]); err != nil {
			return err
		}
		if err := link(groups["L"], groups["K"]); err != nil {
			return err
		}
		if err := link(groups["E"], groups["G"]); err != nil {
			return err
		}
		join := func(g model.ID) error {
			_, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: g, UserID: user})
			if err != nil {
				return fmt.Errorf("join %s: %w", g, err)
			}
			return nil
		}
		// C is a DIRECT membership too: its own MappedRole (admin) elevates t1 —
		// nesting alone would not (a parent mapping never inherits).
		for _, name := range []string{"A", "B", "C", "K", "E", "G"} {
			if err := join(groups[name]); err != nil {
				return err
			}
		}
		// The dangling member row, the way production gets one: the group existed,
		// the user joined, the group was then deleted.
		gD, err := as.Groups().Create(ctx, model.UserGroup{TargetTenantID: t1, DisplayName: "Dang"})
		if err != nil {
			return fmt.Errorf("mk Dang: %w", err)
		}
		if _, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: gD.ID, UserID: user}); err != nil {
			return fmt.Errorf("join dangling: %w", err)
		}
		if err := as.Groups().Delete(ctx, gD.ID); err != nil {
			return fmt.Errorf("delete dangling: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wantG map[model.TenantID]string
	var wantSubj map[model.TenantID][]string
	var wantConf map[model.TenantID]model.ID
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		wantG, wantSubj, wantConf, err = loadGrantsReference(ctx, as, user)
		return err
	}); err != nil {
		t.Fatalf("reference walk: %v", err)
	}
	var gotG map[model.TenantID]string
	var gotSubj map[model.TenantID][]string
	var gotConf map[model.TenantID]model.ID
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		// The fixture's user is an ordinary (non-superadmin) user: superadmin=false
		// keeps the per-tenant group gate the reference walk implements (upstream
		// 1203c57cfd added the flag for superadmin subject retention).
		gotG, gotSubj, gotConf, err = loadGrants(ctx, as, user, false)
		return err
	}); err != nil {
		t.Fatalf("batched walk: %v", err)
	}
	if !reflect.DeepEqual(gotG, wantG) {
		t.Errorf("grants differ:\n batched: %#v\n per-row:  %#v", gotG, wantG)
	}
	norm := func(m map[model.TenantID][]string) map[model.TenantID][]string {
		// The walk appends in walk order; the property is the SET of subjects.
		for k, v := range m {
			sorted := append([]string(nil), v...)
			sort.Strings(sorted)
			m[k] = sorted
		}
		return m
	}
	if !reflect.DeepEqual(norm(gotSubj), norm(wantSubj)) {
		t.Errorf("subject groups differ:\n batched: %#v\n per-row:  %#v", gotSubj, wantSubj)
	}
	if !reflect.DeepEqual(gotConf, wantConf) {
		t.Errorf("confinement differs:\n batched: %#v\n per-row:  %#v", gotConf, wantConf)
	}
	// The fixture must actually exercise the shapes, or the equality above proves
	// nothing: elevation landed, the gate refused t3, the cycle/cross-tenant edge
	// were walked, the dangling row was met.
	if wantG[t1] != RoleAdmin {
		t.Fatalf("the fixture's mapped elevation must land (t1 = %q, want admin)", wantG[t1])
	}
	if wantG[t2] != RoleEditor {
		t.Fatalf("t2 = %q, want editor (the direct membership is workspace-confined)", wantG[t2])
	}
	if _, gated := wantG[t3]; gated {
		t.Fatal("the gate must refuse t3: the user holds no direct membership there")
	}
	if _, ok := wantConf[t2]; !ok || wantConf[t2] != ws {
		t.Fatalf("the workspace confinement must be carried (t2 = %v, want %s)", wantConf[t2], ws)
	}
}
