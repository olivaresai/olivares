// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// leaverAction is the action the tenant's Identity leaver card reads
// (GET /v1/audit?action=scim.user.deprovision; docs/contracts-identity.md).
const leaverAction = "scim.user.deprovision"

// tenantEventsNamed returns, in chain order, the tenant's own ledger events with
// the given action, each with its stored metadata decoded (a plain Walk leaves
// Meta nil by design).
func tenantEventsNamed(t *testing.T, ctx context.Context, st store.Store, tenant model.TenantID, action string) []model.AuditEvent {
	t.Helper()
	var out []model.AuditEvent
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit lacks canonical metadata")
		}
		return walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != action {
				return nil
			}
			if meta != "" {
				if err := json.Unmarshal([]byte(meta), &ev.Meta); err != nil {
					return err
				}
			}
			out = append(out, ev)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// offboardCauses returns the cause of every membership.offboard event on the system
// chain that targets user, in chain order.
func offboardCauses(t *testing.T, ctx context.Context, st store.Store, user model.ID) []string {
	t.Helper()
	var causes []string
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit lacks canonical metadata")
		}
		return walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != "membership.offboard" || ev.TargetID != user {
				return nil
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(meta), &m); err != nil {
				return err
			}
			cause, _ := m["cause"].(string)
			causes = append(causes, cause)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return causes
}

// TestRemoveMemberIsRecordedAsARemovalNotAsSCIM: a member taken out of a tenant by an
// administrator is on both chains with its own cause, member.remove. The leaver
// evidence keeps its published action name, so the Identity leaver card lists a
// removal and a SCIM leaver alike, and the cause says which one it was.
func TestRemoveMemberIsRecordedAsARemovalNotAsSCIM(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, err := a.CreateUser(ctx, super, auth.NewUser{Email: "leaver@acme.com", Password: "pw-" + model.NewID().String(), Tenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.RemoveMember(ctx, super, tenant, u.ID); err != nil {
		t.Fatalf("RemoveMember = %v, want nil", err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member after the removal = %v, want ErrNotFound", err)
	}
	if got := offboardCauses(t, ctx, st, u.ID); len(got) != 1 || got[0] != "member.remove" {
		t.Errorf("system chain offboard causes = %v, want [member.remove]", got)
	}
	got := tenantEventsNamed(t, ctx, st, tenant, leaverAction)
	if len(got) != 1 {
		t.Fatalf("tenant ledger holds %d %s events after the removal, want 1", len(got), leaverAction)
	}
	if cause, _ := got[0].Meta["cause"].(string); cause != "member.remove" {
		t.Errorf("leaver event cause = %q, want member.remove", cause)
	}
	if got[0].TargetID != u.ID || got[0].Actor != super.Actor() {
		t.Errorf("leaver event = target %s actor %q, want %s by %q", got[0].TargetID, got[0].Actor, u.ID, super.Actor())
	}
}

// TestRemoveMemberOfANonMemberWritesNothing: an id that is not a member of the tenant
// is store.ErrNotFound and leaves no event, no retirement record.
func TestRemoveMemberOfANonMemberWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	other := provisionTenant(t, st, "other")
	foreign, err := a.CreateUser(ctx, super, auth.NewUser{Email: "foreign@other.com", Password: "pw-" + model.NewID().String(), Tenant: other, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]model.ID{"unknown id": model.NewID(), "another tenant's member": foreign.ID} {
		if err := a.RemoveMember(ctx, super, tenant, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: RemoveMember = %v, want ErrNotFound", name, err)
		}
	}
	if got := tenantEventsNamed(t, ctx, st, tenant, leaverAction); len(got) != 0 {
		t.Errorf("tenant ledger holds %d %s events after refused removals, want 0", len(got), leaverAction)
	}
	if _, err := a.SCIMGetMember(ctx, other, foreign.ID); err != nil {
		t.Errorf("another tenant's member after the refused removal = %v, want still a member", err)
	}
}

// suspend writes the suspension exclusion the Business build's suspend writes.
func suspend(t *testing.T, ctx context.Context, st store.Store, user model.ID, tenant model.TenantID) {
	t.Helper()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.TenantExclusions().Create(ctx, model.TenantExclusion{UserID: user, TargetTenantID: tenant, Kind: model.ExclusionSuspend})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// A suspended owner cannot run the organization: removing the only other owner is
// refused as the last active owner while the second is suspended, and allowed once
// it is not.
func TestSuspendedOwnerDoesNotCountAsTheActiveOwner(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	first, err := a.CreateUser(ctx, super, auth.NewUser{Email: "o1@acme.com", Password: "pw-" + model.NewID().String(), Tenant: tenant, Role: auth.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.CreateUser(ctx, super, auth.NewUser{Email: "o2@acme.com", Password: "pw-" + model.NewID().String(), Tenant: tenant, Role: auth.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	suspend(t, ctx, st, second.ID, tenant)
	if err := a.RemoveMember(ctx, super, tenant, first.ID); !errors.Is(err, auth.ErrLastOwner) {
		t.Fatalf("removing the owner beside a suspended one = %v, want ErrLastOwner", err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.TenantExclusions().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		for _, x := range rows {
			if err := as.TenantExclusions().Delete(ctx, x.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveMember(ctx, super, tenant, first.ID); err != nil {
		t.Fatalf("removing the owner beside an active one = %v, want nil", err)
	}
}

// Removing a suspended member removes the suspension with the membership, so a
// person added again later is not still suspended.
func TestRemovingASuspendedMemberLiftsTheSuspension(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, err := a.CreateUser(ctx, super, auth.NewUser{Email: "leaver@acme.com", Password: "pw-" + model.NewID().String(), Tenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	suspend(t, ctx, st, u.ID, tenant)
	if err := a.RemoveMember(ctx, super, tenant, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.TenantExclusions().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "kind", Op: model.OpEq, Value: string(model.ExclusionSuspend)},
		}})
		if len(rows) != 0 {
			t.Errorf("suspension rows after the removal = %d, want 0", len(rows))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
