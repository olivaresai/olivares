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

// TestSCIMLeaverIsEvidencedOnTheTenantLedger: a SCIM leaver, however the IdP
// signals it (DELETE, PATCH active:false, a SET deactivate), is recorded on the
// TENANT's chain as scim.user.deprovision with its cause, targeted at the
// departing account. Before this the offboard landed only on the system chain
// (membership.offboard), which a tenant admin cannot read, so the Identity
// leaver card showed "No deprovision events" for a tenant that had leavers.
func TestSCIMLeaverIsEvidencedOnTheTenantLedger(t *testing.T) {
	leave := map[string]struct {
		cause string
		do    func(ctx context.Context, a *auth.Authenticator, actor auth.Principal, tenant model.TenantID, id model.ID) error
	}{
		"delete": {"scim.deprovision", func(ctx context.Context, a *auth.Authenticator, actor auth.Principal, tenant model.TenantID, id model.ID) error {
			return a.SCIMDeprovisionUser(ctx, actor, tenant, id)
		}},
		"patch active false": {"scim.deactivate", func(ctx context.Context, a *auth.Authenticator, actor auth.Principal, tenant model.TenantID, id model.ID) error {
			_, err := a.SCIMUpdateUser(ctx, actor, tenant, id, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: false})
			return err
		}},
		"SET deactivate": {"scim.set.deactivate", func(ctx context.Context, a *auth.Authenticator, actor auth.Principal, tenant model.TenantID, id model.ID) error {
			return a.SCIMSetMemberActive(ctx, actor, tenant, id, false)
		}},
	}
	for name, tc := range leave {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "acme")
			other := provisionTenant(t, st, "other")
			u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := tenantEventsNamed(t, ctx, st, tenant, leaverAction); len(got) != 0 {
				t.Fatalf("before the leaver the tenant ledger holds %d %s events, want 0", len(got), leaverAction)
			}

			if err := tc.do(ctx, a, super, tenant, u.ID); err != nil {
				t.Fatal(err)
			}

			got := tenantEventsNamed(t, ctx, st, tenant, leaverAction)
			if len(got) != 1 {
				t.Fatalf("tenant ledger holds %d %s events after the leaver, want 1", len(got), leaverAction)
			}
			ev := got[0]
			if ev.TargetID != u.ID {
				t.Errorf("event target = %s, want the departing account %s", ev.TargetID, u.ID)
			}
			if ev.Actor != super.Actor() {
				t.Errorf("event actor = %q, want the acting principal %q", ev.Actor, super.Actor())
			}
			if cause, _ := ev.Meta["cause"].(string); cause != tc.cause {
				t.Errorf("event cause = %q, want %q", cause, tc.cause)
			}
			if leaked := tenantEventsNamed(t, ctx, st, other, leaverAction); len(leaked) != 0 {
				t.Errorf("another tenant's ledger holds %d %s events, want 0 (tenant isolation)", len(leaked), leaverAction)
			}
		})
	}
}

// TestSCIMLeaverEvidenceNeedsAnActualOffboard: only an offboard that happened is
// evidence. A repeat of the same leaver, an account that is not a member and an
// activation signal each write nothing to the tenant ledger.
func TestSCIMLeaverEvidenceNeedsAnActualOffboard(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.SCIMSetMemberActive(ctx, super, tenant, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := tenantEventsNamed(t, ctx, st, tenant, leaverAction); len(got) != 0 {
		t.Fatalf("an activation wrote %d %s events, want 0", len(got), leaverAction)
	}

	if err := a.SCIMDeprovisionUser(ctx, super, tenant, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.SCIMDeprovisionUser(ctx, super, tenant, u.ID); err != nil {
		t.Fatalf("repeat deprovision = %v, want an idempotent no-op", err)
	}
	if err := a.SCIMDeprovisionUser(ctx, super, tenant, model.NewID()); err != nil {
		t.Fatalf("deprovision of a non-member = %v, want an idempotent no-op", err)
	}
	if got := tenantEventsNamed(t, ctx, st, tenant, leaverAction); len(got) != 1 {
		t.Errorf("tenant ledger holds %d %s events after one leaver and two no-ops, want 1", len(got), leaverAction)
	}
}

// TestSCIMUpdateThatKeepsTheMemberWritesNoLeaverEvidence: a PATCH or PUT that
// leaves the account active changes attributes, not membership, so the tenant
// ledger gets no leaver event. It protects the guard on the update path: an
// update that recorded a leaver unconditionally would be caught here.
func TestSCIMUpdateThatKeepsTheMemberWritesNoLeaverEvidence(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "stays@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.SCIMUpdateUser(ctx, super, tenant, u.ID, auth.SCIMUserInput{UserName: "stays@acme.com", DisplayName: "Stays", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); err != nil {
		t.Fatalf("member after an attribute update = %v, want still a member", err)
	}
	if got := tenantEventsNamed(t, ctx, st, tenant, leaverAction); len(got) != 0 {
		t.Errorf("an update that kept the member wrote %d %s events, want 0", len(got), leaverAction)
	}
}

// failingTenantStore refuses every tenant-scoped write for one tenant and passes
// everything else, the auth partition included, to the real store.
type failingTenantStore struct {
	store.Store
	tenant model.TenantID
}

func (s failingTenantStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if tenant == s.tenant {
		return errors.New("tenant chain unavailable")
	}
	return s.Store.Mutate(ctx, tenant, fn)
}

// TestSCIMLeaverSucceedsWhenTheTenantLedgerRefusesTheRecord: the offboard has
// committed by the time the tenant chain is written, so a refused append must not
// turn a completed DELETE into an error (the published SCIM status codes stay),
// and the evidence that does exist, membership.offboard on the system chain,
// stays.
func TestSCIMLeaverSucceedsWhenTheTenantLedgerRefusesTheRecord(t *testing.T) {
	ctx := context.Background()
	raw := testStore(t)
	super := mustSuperadmin(t, ctx, auth.NewAuthenticator(raw, nil))
	tenant := provisionTenant(t, raw, "acme")
	a := auth.NewAuthenticator(failingTenantStore{Store: raw, tenant: tenant}, nil)
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.SCIMDeprovisionUser(ctx, super, tenant, u.ID); err != nil {
		t.Fatalf("deprovision with a refusing tenant chain = %v, want nil: the offboard committed", err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member after the deprovision = %v, want ErrNotFound (offboarded)", err)
	}
	var offboards int
	if err := raw.AuthView(ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == "membership.offboard" && ev.TargetID == u.ID {
				offboards++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if offboards != 1 {
		t.Errorf("system chain holds %d membership.offboard events for the account, want 1", offboards)
	}
}
