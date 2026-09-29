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
	// Idempotent and write-free: a second provision of an account that is already a
	// member returns the stored one as it stands, and does not duplicate it. The
	// directory attributes differ but the explicit external identity agrees, so a
	// create that wrote the row it found would be caught here.
	again, created2, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "joiner@acme.com", ExternalID: "idp-9", DisplayName: "Rewritten", Active: false,
	})
	if err != nil || created2 {
		t.Errorf("re-provision = (%v, created=%v), want (nil, false)", err, created2)
	}
	if again.ID != u.ID {
		t.Errorf("re-provision returned account %s, want the stored %s (no duplicate)", again.ID, u.ID)
	}
	if again.DisplayName != "Joiner" || again.ExternalID != "idp-9" || again.Status != model.StatusActive {
		t.Errorf("re-provision returned (displayName=%q, externalId=%q, status=%q), want (%q, %q, %q) — the stored account, unwritten",
			again.DisplayName, again.ExternalID, again.Status, "Joiner", "idp-9", model.StatusActive)
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

// TestSCIMDisableOffboardsOnlyFromTheTenant: a tenant's active=false removes the
// account from that tenant — its membership, the tokens bound to the tenant —
// and excludes its account-scope sessions there. It never writes the account's
// global status and never revokes a session that carries another tenant.
func TestSCIMDisableOffboardsOnlyFromTheTenant(t *testing.T) {
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

	if _, err := a.SCIMUpdateUser(ctx, super, tenant, u.ID, auth.SCIMUserInput{UserName: "x@acme.com", Active: false}); err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(ctx, sessTok)
	if err != nil {
		t.Fatalf("the account-scope session after the tenant's disable = %v, want it to authenticate", err)
	}
	if !p.ExcludedFrom(tenant) || p.IsMember(tenant) {
		t.Errorf("the session still carries the disabling tenant")
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("the tenant-bound token after disable = %v, want revoked", err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member after disable = %v, want ErrNotFound (offboarded)", err)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.Users().Get(ctx, u.ID)
		if err == nil && got.Status != model.StatusActive {
			t.Errorf("status = %q, want the account's global status untouched", got.Status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSCIMDeprovisionOffboardsWithoutDeactivating: DELETE removes the account from
// the tenant even when it was the account's last membership, and leaves the
// account's global status, its account-scope sessions and its authenticators to
// the deployment.
func TestSCIMDeprovisionOffboardsWithoutDeactivating(t *testing.T) {
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
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{
			UserID: u.ID, CredentialID: "bGVhdmVyLWtleQ", Credential: []byte(`{}`),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.SCIMDeprovisionUser(ctx, super, tenant, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SCIMGetMember(ctx, tenant, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member after deprovision = %v, want ErrNotFound (offboarded)", err)
	}
	p, err := a.Authenticate(ctx, sessTok)
	if err != nil {
		t.Fatalf("the account-scope session after deprovision = %v, want it to authenticate", err)
	}
	if !p.ExcludedFrom(tenant) || p.IsMember(tenant) {
		t.Errorf("the session still carries the tenant that removed the account")
	}
	if _, err := a.Authenticate(ctx, apiTok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("the tenant-bound token after deprovision = %v, want revoked", err)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.Users().Get(ctx, u.ID)
		if err != nil {
			return err
		}
		if got.Status != model.StatusActive {
			t.Errorf("status after deprovision = %q, want the global status untouched", got.Status)
		}
		creds, _, err := as.WebAuthnCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: u.ID.String()}},
		})
		if err != nil {
			return err
		}
		if len(creds) != 1 {
			t.Errorf("webauthn credentials after deprovision = %d, want the account's one kept", len(creds))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
