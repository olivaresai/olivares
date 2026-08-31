// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// mintUserCreds creates a live session token and a tenant-bound API token owned
// by user, directly through the store, and returns their wire tokens — so a test
// can verify the SCIM leaver/disable actually invalidates them.
func mintUserCreds(t *testing.T, st store.Store, userID model.ID, tenant model.TenantID) (sessionTok, apiTok string) {
	t.Helper()
	ctx := context.Background()
	sc, err := auth.NewCredential(auth.PrefixSession)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.Sessions().Create(ctx, model.AuthSession{
			UserID: userID, Selector: sc.Selector, SecretHash: sc.SecretHash,
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
		}); err != nil {
			return err
		}
		_, err := as.Tokens().Create(ctx, model.APIToken{
			Name: "u-tok", UserID: userID, Selector: tc.Selector, SecretHash: tc.SecretHash,
			BoundTenantID: tenant, Role: auth.RoleViewer,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return sc.Token, tc.Token
}

func TestSCIMProvisionJoinsTenant(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	u, created, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "Joiner@Acme.com", ExternalID: "idp-9", DisplayName: "Joiner", Active: true,
	})
	if err != nil || !created {
		t.Fatalf("provision = (%v, created=%v)", err, created)
	}
	if u.Email != "joiner@acme.com" {
		t.Errorf("email = %q, want normalized", u.Email)
	}
	// Idempotent: a second provision (full resource) updates, does not duplicate,
	// stays a member.
	if _, created2, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "joiner@acme.com", ExternalID: "idp-9", DisplayName: "Joiner", Active: true}); err != nil || created2 {
		t.Errorf("re-provision = (%v, created=%v), want (nil, false)", err, created2)
	}
	if _, found, err := a.SCIMFindMember(ctx, tenant, "external_id", "idp-9"); err != nil || !found {
		t.Errorf("find by externalId = (found=%v, %v)", found, err)
	}
	// A SCIM token for a DIFFERENT tenant cannot see this member.
	other := provisionTenant(t, st, "other")
	if _, err := a.SCIMGetMember(ctx, other, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant get = %v, want ErrNotFound (isolation)", err)
	}
}

func TestSCIMDisableRevokesAccessButKeepsRecord(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "x@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)

	// active=false: cut access but keep the membership/record.
	if _, err := a.SCIMUpdateUser(ctx, super, tenant, u.ID, auth.SCIMUserInput{UserName: "x@acme.com", Active: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after disable = %v, want revoked", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("token after disable = %v, want revoked", err)
	}
	// The record is still retrievable as a member (a disable, not a removal).
	got, err := a.SCIMGetMember(ctx, tenant, u.ID)
	if err != nil {
		t.Fatalf("member after disable = %v, want still present", err)
	}
	if got.Status != model.StatusInactive {
		t.Errorf("status = %q, want inactive", got.Status)
	}
}

func TestSCIMDeprovisionOffboardsAndDeactivates(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{UserName: "leaver@acme.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sessTok, apiTok := mintUserCreds(t, st, u.ID, tenant)
	// A registered authenticator must go with the account on offboard.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{
			UserID: u.ID, CredentialID: "bGVhdmVyLWtleQ", Credential: []byte(`{}`),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// DELETE (leaver): remove membership, revoke creds, deactivate (no tenants left).
	if err := a.SCIMDeprovisionUser(ctx, super, tenant, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member after deprovision = %v, want ErrNotFound (offboarded)", err)
	}
	if _, err := a.Authenticate(ctx, sessTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("session after deprovision = %v, want revoked", err)
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("token after deprovision = %v, want revoked", err)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		creds, _, err := as.WebAuthnCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: u.ID.String()}},
		})
		if err != nil {
			return err
		}
		if len(creds) != 0 {
			t.Errorf("webauthn credentials after deprovision = %d, want 0 (hardware bindings offboarded)", len(creds))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
