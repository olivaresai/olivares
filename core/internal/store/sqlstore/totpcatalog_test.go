// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestTOTPRoundTrip pins the second-factor relations: a sealed-seed
// credential row (one per account) and its hashed recovery codes, written and
// read back through the auth partition accessors, including the NULL
// semantics of a pending (not yet confirmed) enrolment.
func TestTOTPRoundTrip(t *testing.T) {

	st := openInitializedSQLiteTest(t, initializedSQLiteCore)
	testTOTPRoundTrip(t, st)
}

func TestTOTPRoundTripPostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	st, err := Open(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testTOTPRoundTrip(t, st)
}

func testTOTPRoundTrip(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	var accountID model.ID
	err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		u, err := a.Users().Create(ctx, model.User{
			Email: "totp@example.com", DisplayName: "TOTP User",
			Status: model.StatusActive, PasswordHash: "argon2id$stub",
		})
		if err != nil {
			return err
		}
		accountID = u.ID
		// Pending enrolment: sealed seed present, ConfirmedAt nil, no step used.
		c, err := a.TOTPCredentials().Create(ctx, model.TOTPCredential{
			AccountID: u.ID, SeedSealed: "v1:c2VhbGVk", SeedHint: "ab12cd34ef56",
			Algorithm: "SHA1", Digits: 6, Period: 30,
		})
		if err != nil {
			return err
		}
		confirmed := model.NewTimestamp(time.Now().Add(time.Minute))
		c.ConfirmedAt = &confirmed
		c.LastUsedStep = 58_000_000
		if _, err := a.TOTPCredentials().Update(ctx, c); err != nil {
			return err
		}
		_, err = a.TOTPRecoveryCodes().Create(ctx, model.TOTPRecoveryCode{
			AccountID: u.ID, CodeHash: []byte("hash-a"),
		})
		if err != nil {
			return err
		}
		spent := model.NewTimestamp(time.Now().Add(2 * time.Minute))
		_, err = a.TOTPRecoveryCodes().Create(ctx, model.TOTPRecoveryCode{
			AccountID: u.ID, CodeHash: []byte("hash-b"), UsedAt: &spent,
		})
		return err
	})
	if err != nil {
		t.Fatalf("auth mutate: %v", err)
	}

	err = st.AuthView(ctx, func(a store.AuthScope) error {
		creds, _, err := a.TOTPCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		if len(creds) != 1 {
			t.Fatalf("credential enumeration = %+v", creds)
		}
		c := creds[0]
		if c.SeedSealed != "v1:c2VhbGVk" || c.SeedHint != "ab12cd34ef56" ||
			c.Algorithm != "SHA1" || c.Digits != 6 || c.Period != 30 {
			t.Fatalf("credential round-trip = %+v", c)
		}
		if c.ConfirmedAt == nil || c.LastUsedStep != 58_000_000 {
			t.Fatalf("confirmation state = %+v", c)
		}
		codes, _, err := a.TOTPRecoveryCodes().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		if len(codes) != 2 {
			t.Fatalf("recovery-code enumeration = %+v", codes)
		}
		for _, code := range codes {
			if code.AccountID != accountID || len(code.CodeHash) == 0 {
				t.Fatalf("recovery-code round-trip = %+v", code)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("auth view: %v", err)
	}
}

// TestTOTPCredentialRequiresCanonicalAccount pins the directory-authority rule
// the repositories share with WebAuthn credentials: a factor may never attach
// to an account that is not in the directory.
func TestTOTPCredentialRequiresCanonicalAccount(t *testing.T) {
	ctx := context.Background()
	st := openInitializedSQLiteTest(t, initializedSQLiteCore)

	err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		_, err := a.TOTPCredentials().Create(ctx, model.TOTPCredential{
			AccountID: model.NewID(), SeedSealed: "v1:c2VhbGVk", SeedHint: "ab12cd34ef56",
			Algorithm: "SHA1", Digits: 6, Period: 30,
		})
		return err
	})
	if err == nil {
		t.Fatal("credential for a dangling account was accepted")
	}
}
