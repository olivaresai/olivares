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
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: true})
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
	foreign, _, err := a.SCIMProvisionUser(ctx, super, other, auth.SCIMUserInput{UserName: "foreign@other.com", Active: true})
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
