// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// concurrentAttempts is how many logins race for the one guess a tripped address
// allows a clean account. Any number above one exposes the gap; eight keeps the
// test's argon2id cost small while leaving no doubt about which answer is right.
const concurrentAttempts = 8

// TestATrippedAddressAdmitsOneAttemptPerCleanAccountUnderConcurrency is the bound
// the whole design rests on, measured the way an attacker would break it.
//
// The refusal of a spray is a COUNT — one guess per account per window from a
// tripped address — and a count is only a bound if it is taken under the same lock
// as the decision. Deciding, then waiting, then recording the failure leaves a
// window in which every parallel request sees the same clean account and is
// admitted: C guesses where the design promises one, and the promise is what
// justifies letting a bystander through at all.
//
// The attempts are released together and held at the password check, so the
// result does not depend on scheduling: every attempt has made its decision
// before any of them is allowed to finish.
func TestATrippedAddressAdmitsOneAttemptPerCleanAccountUnderConcurrency(t *testing.T) {
	var (
		mu      sync.Mutex
		reached int // attempts that got past the decision, to the password check
		past    int // attempts that have made their decision, admitted or refused
	)
	held := make(chan struct{})
	arrive := func() {
		mu.Lock()
		past++
		all := past == concurrentAttempts
		mu.Unlock()
		if all {
			close(held)
		}
	}

	// The wait a tripped address charges is where an admitted attempt is counted
	// and held. Only an admitted attempt reaches it, and the password check is the
	// next thing it does.
	saved := sleepLoginDelay
	t.Cleanup(func() { sleepLoginDelay = saved })
	sleepLoginDelay = func(_ context.Context, _ time.Duration) error {
		mu.Lock()
		reached++
		mu.Unlock()
		arrive()
		<-held
		return nil
	}

	ctx := context.Background()
	a, admin := throttledLoginFixture(t)
	const sharedPeer = "203.0.113.30"
	const (
		decoy  = "decoy@example.test"
		target = "target@example.test"
	)
	for _, acct := range []string{decoy, target} {
		if _, err := a.CreateUser(ctx, admin, NewUser{Email: acct, DisplayName: "Member", Password: "member-password-1"}); err != nil {
			t.Fatalf("create %s: %v", acct, err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, _, err := a.Login(ctx, decoy, "wrong", sharedPeer); err == nil {
			t.Fatalf("failed attempt %d against the decoy unexpectedly succeeded", i)
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < concurrentAttempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer arrive() // a refused attempt has made its decision too
			<-start
			_, _, _ = a.Login(ctx, target, "wrong", sharedPeer)
		}()
	}
	close(start)
	wg.Wait()

	mu.Lock()
	got := reached
	mu.Unlock()
	if got != 1 {
		t.Fatalf("%d of %d concurrent attempts on one clean account reached the password check from a tripped address, want 1", got, concurrentAttempts)
	}
}

// throttledLoginFixture opens an in-memory store, bootstraps the first
// superadmin and returns it with the authenticator, so a throttle test can create
// the accounts it needs through the ordinary verbs.
func throttledLoginFixture(t *testing.T) (*Authenticator, Principal) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := NewAuthenticator(st, nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@example.test", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap the first superadmin: %v", err)
	}
	tok, _, err := a.Login(ctx, "root@example.test", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatalf("log the first superadmin in: %v", err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("resolve the superadmin session: %v", err)
	}
	return a, p
}
