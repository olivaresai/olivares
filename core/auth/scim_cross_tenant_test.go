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

// auditHead returns the current tip of the evidence chain, or 0 for an empty one.
func auditHead(t *testing.T, ctx context.Context, st store.Store) int64 {
	t.Helper()
	var seq int64
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		head, ok, err := as.Audit().Head(ctx)
		if err != nil || !ok {
			return err
		}
		seq = head.Seq
		return nil
	}); err != nil {
		t.Fatalf("audit head: %v", err)
	}
	return seq
}

// TestSCIMProvisionUserRefusesAnAccountOutsideTheBoundTenant pins the boundary in
// the verb every SCIM create goes through, not in one of its callers: a check
// that lives in a caller leaves the next caller unguarded, and this verb is the
// one place where an account is matched by address across the whole deployment.
//
// It also pins the two halves of "a create never writes what it did not create":
// the refusal leaves the account and the evidence chain alone, and the branch
// that does create something records both of the changes it makes.
func TestSCIMProvisionUserRefusesAnAccountOutsideTheBoundTenant(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	owning := provisionTenant(t, st, "globex")
	claiming := provisionTenant(t, st, "acme")

	victim, created, err := a.SCIMProvisionUser(ctx, super, owning, auth.SCIMUserInput{
		UserName: "ceo@globex.example", ExternalID: "idp-owner-7", DisplayName: "Blair Okonjo", Active: true,
	})
	if err != nil || !created {
		t.Fatalf("seed provision in the owning tenant = (%v, created=%v)", err, created)
	}
	sessTok, apiTok := mintUserCreds(t, st, victim.ID, owning)

	before := auditHead(t, ctx, st)
	_, claimed, err := a.SCIMProvisionUser(ctx, super, claiming, auth.SCIMUserInput{
		UserName: "ceo@globex.example", ExternalID: "idp-claimant-1", DisplayName: "Claimed", Active: false,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("provisioning an account that belongs to another tenant = (created=%v, %v), want store.ErrConflict", claimed, err)
	}
	if after := auditHead(t, ctx, st); after != before {
		t.Errorf("the refused create appended %d audit event(s); a call that changes nothing records nothing", after-before)
	}

	// Nothing of the account moved, and it was never enrolled.
	got, err := a.SCIMGetMember(ctx, owning, victim.ID)
	if err != nil {
		t.Fatalf("owner read after the refused create = %v", err)
	}
	if got.Status != model.StatusActive {
		t.Errorf("status = %q, want %q: another tenant's create disabled the account, which stops its sessions in EVERY tenant", got.Status, model.StatusActive)
	}
	if got.DisplayName != "Blair Okonjo" || got.ExternalID != "idp-owner-7" {
		t.Errorf("displayName/externalId = %q/%q, want %q/%q: another tenant's create overwrote them",
			got.DisplayName, got.ExternalID, "Blair Okonjo", "idp-owner-7")
	}
	if _, err := a.SCIMGetMember(ctx, claiming, victim.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("membership in the claiming tenant = %v, want ErrNotFound (never enrolled)", err)
	}
	if _, err := a.Authenticate(ctx, sessTok); err != nil {
		t.Errorf("the account's session = %v, want still valid: another tenant's create cut its access", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); err != nil {
		t.Errorf("the account's token = %v, want still valid: another tenant's create cut its access", err)
	}

	// The unchanged half: an address nobody holds still provisions, and the two
	// changes it makes — the account and the membership — are both on the record.
	fresh, created, err := a.SCIMProvisionUser(ctx, super, claiming, auth.SCIMUserInput{
		UserName: "newcomer@acme.example", Active: true,
	})
	if err != nil || !created {
		t.Fatalf("create of an unused address = (%v, created=%v), want (nil, true)", err, created)
	}
	recorded := map[string]bool{"scim.user.create": false, "scim.user.join": false}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.TargetID == fresh.ID {
				if _, tracked := recorded[ev.Action]; tracked {
					recorded[ev.Action] = true
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	for action, saw := range recorded {
		if !saw {
			t.Errorf("no %s event for the provisioned account: a branch that changes something must say so", action)
		}
	}
}
