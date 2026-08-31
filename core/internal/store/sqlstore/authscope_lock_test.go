// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// The auth partition's callers fail CLOSED when the scope cannot lock, so a
// scope that silently lost the capability would not merely skip a lock — it
// would make first-boot bootstrap refuse on every install. The compile-time
// assertion in authscope.go proves the METHOD exists; this proves the scope
// AuthMutate actually hands out is the one carrying it.
func TestAuthMutateScopeCarriesTheTransactionLock(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	var got bool
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		locker, ok := a.(store.TransactionLocker)
		got = ok
		if !ok {
			return nil
		}
		// SQLite admits one writer, so the lock is a no-op there — but it must
		// be a SUCCESSFUL no-op, not an error, or every SQLite install refuses
		// to bootstrap.
		return locker.LockTransaction(ctx, "auth.bootstrap")
	}); err != nil {
		t.Fatalf("locking inside AuthMutate must succeed on SQLite: %v", err)
	}
	if !got {
		t.Fatal("the scope handed out by AuthMutate must implement store.TransactionLocker")
	}
}

// A read-only scope must refuse rather than claim write authority it does not
// have: the interface says so explicitly, and a lock that "succeeds" on a read
// transaction serializes nothing.
func TestAuthViewScopeRefusesToLock(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	err := st.AuthView(ctx, func(a store.AuthScope) error {
		locker, ok := a.(store.TransactionLocker)
		if !ok {
			t.Fatal("the read-only auth scope must also expose the capability, to be able to refuse")
		}
		return locker.LockTransaction(ctx, "auth.bootstrap")
	})
	if !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("a read-only scope must return ErrReadOnly, got %v", err)
	}
}

// An empty key locks nothing; accepting it would turn a serialization bug into a
// silent pass.
func TestAuthScopeRejectsAnEmptyLockKey(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		return a.(store.TransactionLocker).LockTransaction(ctx, "")
	})
	if err == nil {
		t.Fatal("an empty lock key must be refused, not treated as a no-op")
	}
}
