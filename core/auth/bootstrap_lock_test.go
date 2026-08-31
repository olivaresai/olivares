// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// scopeWithoutLock is a store.AuthScope that does NOT provide
// store.TransactionLocker. The embedded interface is nil on purpose: nothing in
// these tests calls a repository, and what is under test is the TYPE assertion,
// which resolves against the concrete type and so must fail here.
type scopeWithoutLock struct{ store.AuthScope }

// scopeWithLock records the key it was asked to lock and can be told to fail, so
// one fake covers both "the lock refused" and "the lock was taken, with WHICH
// key" — a lock on the wrong key serializes nothing and would otherwise pass.
type scopeWithLock struct {
	store.AuthScope
	key  string
	err  error
	call int
}

func (s *scopeWithLock) LockTransaction(_ context.Context, key string) error {
	s.call++
	s.key = key
	return s.err
}

func TestBootstrapRefusesWhenTheScopeCannotLock(t *testing.T) {
	err := lockBootstrapTransaction(context.Background(), scopeWithoutLock{})
	if !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("a scope without the locking capability must fail closed with ErrCoordinationUnavailable, got %v", err)
	}
	// It must NOT be reported as "setup already complete": that would tell an
	// operator credentials exist when nothing was written.
	if errors.Is(err, ErrSetupComplete) {
		t.Fatalf("coordination failure must stay distinct from ErrSetupComplete, got %v", err)
	}
}

func TestBootstrapRefusesWhenTheLockItselfFails(t *testing.T) {
	cause := errors.New("connection reset mid-lock")
	sc := &scopeWithLock{err: cause}
	err := lockBootstrapTransaction(context.Background(), sc)
	if !errors.Is(err, ErrCoordinationUnavailable) {
		t.Fatalf("a failing lock must fail closed, got %v", err)
	}
	// The cause survives: "bootstrap refused" without WHY is a diagnosis an
	// operator cannot act on.
	if !errors.Is(err, cause) {
		t.Fatalf("the underlying cause must be preserved, got %v", err)
	}
}

func TestBootstrapLocksTheGlobalKeyExactlyOnce(t *testing.T) {
	sc := &scopeWithLock{}
	if err := lockBootstrapTransaction(context.Background(), sc); err != nil {
		t.Fatalf("a scope that can lock must succeed, got %v", err)
	}
	if sc.call != 1 {
		t.Fatalf("the bootstrap transaction must take the lock exactly once, took it %d times", sc.call)
	}
	// The key carries NO tenant. A per-tenant key would let two tenants each
	// bootstrap their own "first" superadmin, which is the invariant at stake.
	if sc.key != "auth.bootstrap" {
		t.Fatalf("bootstrap must serialize on the single global key, locked %q", sc.key)
	}
	if sc.key == "" {
		t.Fatal("an empty key locks nothing")
	}
}
