// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// waitAdvisoryWaiter polls pg_locks until key has one granted holder and at least one
// waiter in this database. If the waiting party (done) finishes first, it did not wait.
func waitAdvisoryWaiter(t *testing.T, observer *sql.DB, key string, done <-chan error, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("%s finished while the lock was held (err=%v), want it to wait", what, err)
		default:
		}
		var granted, waiting int
		if err := observer.QueryRowContext(context.Background(), `
SELECT count(*) FILTER (WHERE granted), count(*) FILTER (WHERE NOT granted)
FROM pg_locks
WHERE locktype = 'advisory' AND objsubid = 1
  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND ((classid::bigint << 32) | objid::bigint) = hashtextextended($1, 0)`, key).Scan(&granted, &waiting); err != nil {
			t.Fatalf("observe %s: %v", key, err)
		}
		if granted == 1 && waiting >= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s granted=%d waiting=%d after 15s, want granted=1 waiting>=1", what, key, granted, waiting)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// On PostgreSQL a passkey enrollment takes the per-user WebAuthn lock
// and then, through the credential write, the directory writer lock. Recovery took the
// directory writer lock first, so the two could deadlock. It now takes them in the
// enrollment's order: the recovery waits, the enrollment commits, and the recovery then
// removes that passkey.
func TestRecoverPasskeysAndAnEnrollmentTakeLocksInOneOrderOnPostgres(t *testing.T) {
	st, observer := openLoginCapabilityPostgres(t)
	ctx := context.Background()
	a := auth.NewAuthenticator(st, nil)
	user, err := a.BootstrapSuperadmin(ctx, "recover-pg@x.io", "supersecret-pw")
	if err != nil {
		t.Fatal(err)
	}
	op, err := auth.NewLocalOperator(auth.LocalOperator{Subject: "ops-oncall", Via: "cli:admin-recover", Reason: "lost passkey"})
	if err != nil {
		t.Fatal(err)
	}
	key := "auth.webauthn:" + user.ID.String() // the registration writer's per-user lock

	holding, proceed := make(chan struct{}), make(chan struct{})
	enrolled := make(chan error, 1)
	go func() {
		enrolled <- st.AuthMutate(ctx, func(as store.AuthScope) error {
			if err := as.(store.TransactionLocker).LockTransaction(ctx, key); err != nil {
				return err
			}
			close(holding)
			<-proceed
			_, err := as.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{UserID: user.ID, CredentialID: "aW4tZmxpZ2h0", Credential: []byte(`{}`)})
			return err
		})
	}()
	<-holding
	var got auth.PasskeyRecovery
	recovered := make(chan error, 1)
	go func() {
		var err error
		got, err = a.RecoverPasskeys(ctx, op, user.ID)
		recovered <- err
	}()
	waitAdvisoryWaiter(t, observer, key, recovered, "recovery")
	close(proceed)

	if err := <-enrolled; err != nil {
		t.Fatalf("the enrollment in flight = %v, want it to commit", err)
	}
	if err := <-recovered; err != nil || got.Passkeys != 1 {
		t.Fatalf("recovery after the enrollment = %+v %v, want it to remove that one passkey", got, err)
	}
}
