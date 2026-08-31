// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestAuthPartitionRoundTrip exercises the auth partition end to end through
// the engine's AuthView/AuthMutate accessors (which bind the system tenant as a
// normal RLS-enforced scope, not the cross-tenant System path).
func TestAuthPartitionRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	businessTenant := provisionTenant(t, st, "auth-round-trip")

	// Create a user, a session, a token and a membership through AuthMutate.
	var userID model.ID
	err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		u, err := a.Users().Create(ctx, model.User{
			Email: "admin@example.com", DisplayName: "Admin",
			Status: model.StatusActive, PasswordHash: "argon2id$stub", IsSuperadmin: true,
		})
		if err != nil {
			return err
		}
		userID = u.ID
		if u.TenantID != model.SystemTenantID {
			t.Fatalf("user tenant = %s, want system tenant", u.TenantID)
		}
		if _, err := a.Sessions().Create(ctx, model.AuthSession{
			UserID: u.ID, Selector: "sel-1", SecretHash: []byte("hash-1"),
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)), CreatedIP: "127.0.0.1",
		}); err != nil {
			return err
		}
		if _, err := a.Tokens().Create(ctx, model.APIToken{
			Name: "ci", UserID: u.ID, Selector: "tok-1", SecretHash: []byte("hash-2"),
			BoundTenantID: businessTenant, Role: "viewer",
		}); err != nil {
			return err
		}
		_, err = a.Memberships().Create(ctx, model.Membership{
			UserID: u.ID, TargetTenantID: businessTenant, Role: "owner",
		})
		return err
	})
	if err != nil {
		t.Fatalf("auth mutate: %v", err)
	}

	// Read back by natural keys through AuthView.
	err = st.AuthView(ctx, func(a store.AuthScope) error {
		users, _, err := a.Users().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "email", Op: model.OpEq, Value: "admin@example.com"}},
		})
		if err != nil {
			return err
		}
		if len(users) != 1 || users[0].ID != userID || !users[0].IsSuperadmin {
			t.Fatalf("user lookup by email = %+v", users)
		}
		sessions, _, err := a.Sessions().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "selector", Op: model.OpEq, Value: "sel-1"}},
		})
		if err != nil {
			return err
		}
		if len(sessions) != 1 || sessions[0].UserID != userID {
			t.Fatalf("session lookup by selector = %+v", sessions)
		}
		mships, _, err := a.Memberships().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: userID.String()}},
		})
		if err != nil {
			return err
		}
		if len(mships) != 1 || mships[0].Role != "owner" {
			t.Fatalf("membership enumeration = %+v", mships)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("auth view: %v", err)
	}

	// A View write must be rejected (read-only scope).
	err = st.AuthView(ctx, func(a store.AuthScope) error {
		_, e := a.Users().Create(ctx, model.User{Email: "x@y.z", Status: model.StatusActive})
		return e
	})
	if !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("write in AuthView err = %v, want ErrReadOnly", err)
	}
}

// TestAuthAssuranceRoundTrip pins the columns: session aal/amr/aal_expires_at
// and the webauthn_credentials table, including the NULL semantics a legacy
// session row relies on (zero AAL, nil AMR, nil expiry).
func TestAuthAssuranceRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	exp := model.NewTimestamp(time.Now().Add(15 * time.Minute))
	var userID model.ID
	err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		u, err := a.Users().Create(ctx, model.User{
			Email: "op@example.com", DisplayName: "Op", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		userID = u.ID
		if _, err := a.Sessions().Create(ctx, model.AuthSession{
			UserID: u.ID, Selector: "sel-aal", SecretHash: []byte("h"),
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
			AAL:       3, AMR: []string{"pwd", "webauthn"}, AALExpiresAt: &exp,
		}); err != nil {
			return err
		}
		// A legacy-shaped session: zero AAL, no AMR (NULL columns).
		if _, err := a.Sessions().Create(ctx, model.AuthSession{
			UserID: u.ID, Selector: "sel-legacy", SecretHash: []byte("h"),
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
		}); err != nil {
			return err
		}
		_, err = a.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{
			UserID: u.ID, Name: "yubikey", CredentialID: "Y3JlZC1pZA",
			Credential: []byte(`{"id":"Y3JlZC1pZA","publicKey":"cGs"}`),
		})
		return err
	})
	if err != nil {
		t.Fatalf("auth mutate: %v", err)
	}

	err = st.AuthView(ctx, func(a store.AuthScope) error {
		ss, _, err := a.Sessions().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "selector", Op: model.OpEq, Value: "sel-aal"}},
		})
		if err != nil {
			return err
		}
		if len(ss) != 1 || ss[0].AAL != 3 || len(ss[0].AMR) != 2 || ss[0].AMR[1] != "webauthn" {
			t.Fatalf("elevated session round-trip = %+v", ss)
		}
		if ss[0].AALExpiresAt == nil || !ss[0].AALExpiresAt.Time().Equal(exp.Time()) {
			t.Fatalf("aal_expires_at round-trip = %v, want %v", ss[0].AALExpiresAt, exp)
		}
		legacy, _, err := a.Sessions().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "selector", Op: model.OpEq, Value: "sel-legacy"}},
		})
		if err != nil {
			return err
		}
		if len(legacy) != 1 || legacy[0].AAL != 0 || legacy[0].AMR != nil || legacy[0].AALExpiresAt != nil {
			t.Fatalf("legacy session NULL semantics = %+v", legacy)
		}
		creds, _, err := a.WebAuthnCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "credential_id", Op: model.OpEq, Value: "Y3JlZC1pZA"}},
		})
		if err != nil {
			return err
		}
		if len(creds) != 1 || creds[0].Name != "yubikey" || string(creds[0].Credential) == "" {
			t.Fatalf("webauthn credential round-trip = %+v", creds)
		}
		// Per-RP uniqueness: a second credential with the same id must conflict.
		return nil
	})
	if err != nil {
		t.Fatalf("auth view: %v", err)
	}

	err = st.AuthMutate(ctx, func(a store.AuthScope) error {
		_, err := a.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{
			UserID: userID, CredentialID: "Y3JlZC1pZA", Credential: []byte(`{}`),
		})
		return err
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate credential_id err = %v, want ErrConflict", err)
	}
}

// TestExtRejectsCoreNamespace confirms a module-facing Scope cannot reach core
// (credential) entities through the generic Ext path.
func TestExtRejectsCoreNamespace(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		for _, k := range []model.Kind{"core.user", "core.api_token", "core.agent", "core.org"} {
			if _, e := sc.Ext(k); !errors.Is(e, store.ErrUnknownEntity) {
				t.Fatalf("Ext(%q) err = %v, want ErrUnknownEntity", k, e)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestEnsureSystemTenantIdempotent confirms the system org is provisioned once
// and re-provisioning is a no-op.
func TestEnsureSystemTenantIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	var first, second model.Org
	run := func(dst *model.Org) {
		if err := st.System(ctx, func(sys store.SystemScope) error {
			o, err := sys.EnsureSystemTenant(ctx)
			*dst = o
			return err
		}); err != nil {
			t.Fatalf("ensure system tenant: %v", err)
		}
	}
	run(&first)
	run(&second)
	if first.ID != model.ID(model.SystemTenantID) || first.Slug != "system" {
		t.Fatalf("system org = %+v", first)
	}
	if second.ID != first.ID || second.CreatedAt.String() != first.CreatedAt.String() {
		t.Fatalf("EnsureSystemTenant not idempotent: %+v vs %+v", first, second)
	}
	// The system chain should verify cleanly from genesis.
	if err := st.System(ctx, func(sys store.SystemScope) error {
		rep, err := sys.Verify(ctx, model.SystemTenantID, 1)
		if err != nil {
			return err
		}
		if !rep.OK {
			t.Fatalf("system chain verify: %+v", rep)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// TestDropTenantPurgesGroups confirms dropping a tenant removes the SCIM groups
// provisioned into it and their member rows (which live in the system tenant
// and reference the tenant only through the group's target_tenant_id), while
// another tenant's groups survive.
func TestDropTenantPurgesGroups(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	victim := provisionTenant(t, st, "victim")
	other := provisionTenant(t, st, "other")

	var victimGroup, otherGroup model.ID
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		u, err := a.Users().Create(ctx, model.User{Email: "g@v.w", Status: model.StatusActive})
		if err != nil {
			return err
		}
		for _, c := range []struct {
			tenant model.TenantID
			dst    *model.ID
		}{{victim, &victimGroup}, {other, &otherGroup}} {
			g, err := a.Groups().Create(ctx, model.UserGroup{
				TargetTenantID: c.tenant, DisplayName: "Engineering",
			})
			if err != nil {
				return err
			}
			*c.dst = g.ID
			if _, err := a.GroupMembers().Create(ctx, model.UserGroupMember{
				GroupID: g.ID, UserID: u.ID,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, victim)
	}); err != nil {
		t.Fatalf("drop tenant: %v", err)
	}

	if err := st.AuthView(ctx, func(a store.AuthScope) error {
		if _, err := a.Groups().Get(ctx, victimGroup); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("victim group after drop: err = %v, want ErrNotFound", err)
		}
		rows, _, err := a.GroupMembers().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "group_id", Op: model.OpEq, Value: victimGroup.String()}},
		})
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			t.Errorf("victim group member rows after drop = %d, want 0", len(rows))
		}
		// The other tenant's group and roster survive.
		if _, err := a.Groups().Get(ctx, otherGroup); err != nil {
			t.Errorf("other tenant's group after drop: %v", err)
		}
		rows, _, err = a.GroupMembers().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "group_id", Op: model.OpEq, Value: otherGroup.String()}},
		})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Errorf("other tenant's member rows after drop = %d, want 1", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("verify drop: %v", err)
	}
}

// TestDropTenantPurgesCredentials confirms dropping a tenant removes the
// memberships and tokens that reference it (which live in the system tenant).
func TestDropTenantPurgesCredentials(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	target := provisionTenant(t, st, "victim")

	var userID model.ID
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		u, err := a.Users().Create(ctx, model.User{Email: "u@v.w", Status: model.StatusActive})
		if err != nil {
			return err
		}
		userID = u.ID
		if _, err := a.Memberships().Create(ctx, model.Membership{
			UserID: u.ID, TargetTenantID: target, Role: "admin",
		}); err != nil {
			return err
		}
		_, err = a.Tokens().Create(ctx, model.APIToken{
			Name: "t", UserID: u.ID, Selector: "s", SecretHash: []byte("h"),
			BoundTenantID: target, Role: "viewer",
		})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, target)
	}); err != nil {
		t.Fatalf("drop tenant: %v", err)
	}

	if err := st.AuthView(ctx, func(a store.AuthScope) error {
		ms, _, err := a.Memberships().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: userID.String()}},
		})
		if err != nil {
			return err
		}
		if len(ms) != 0 {
			t.Fatalf("memberships after drop = %d, want 0", len(ms))
		}
		ts, _, err := a.Tokens().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "bound_tenant_id", Op: model.OpEq, Value: target.String()}},
		})
		if err != nil {
			return err
		}
		if len(ts) != 0 {
			t.Fatalf("tokens after drop = %d, want 0", len(ts))
		}
		// The user itself (global) survives.
		u, err := a.Users().Get(ctx, userID)
		if err != nil {
			return err
		}
		if u.Email != "u@v.w" {
			t.Fatalf("user gone after drop: %+v", u)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify drop: %v", err)
	}
}
