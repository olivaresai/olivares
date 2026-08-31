// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openRacePG opens a PostgreSQL-backed store on its own isolated database.
func openRacePG(t *testing.T) (context.Context, store.Store) {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 8,
	}, nil)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return ctx, st
}

// TestPostgresAdmitsTheCheckThenActRaceTheBootstrapLockCloses is the POSITIVE
// CONTROL for the lock: it shows, on the real engine, that the window the lock
// closes is genuinely open.
//
// It drives the same check-then-act the bootstrap performs — read "does any user
// exist", then insert — directly against the store, with every racer held at a
// barrier between the read and the write. Under READ COMMITTED all six read zero
// and all six insert. If this test ever goes green with ONE row, the engine (or
// the schema) started serializing these writes on its own and the lock's
// justification must be re-derived, not assumed.
func TestPostgresAdmitsTheCheckThenActRaceTheBootstrapLockCloses(t *testing.T) {
	ctx, st := openRacePG(t)

	const racers = 6
	var wg sync.WaitGroup
	var checked sync.WaitGroup
	checked.Add(racers)
	errs := make([]error, racers)
	created := make([]bool, racers)

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = st.AuthMutate(ctx, func(as store.AuthScope) error {
				existing, _, err := as.Users().List(ctx, model.Query{Limit: 1})
				if err != nil {
					return err
				}
				// Nobody writes until everybody has read: the interleaving is
				// MADE here rather than hoped for. Released together, these six
				// otherwise run sequentially and the window never opens.
				checked.Done()
				checked.Wait()
				if len(existing) > 0 {
					return nil
				}
				if _, err := as.Users().Create(ctx, model.User{
					Email: fmt.Sprintf("racer%d@example.com", i), DisplayName: "Racer",
					Status: model.StatusActive, PasswordHash: "argon2id$stub", IsSuperadmin: true,
				}); err != nil {
					return err
				}
				created[i] = true
				return nil
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d failed for a reason other than the race: %v", i, err)
		}
	}
	n := 0
	for _, c := range created {
		if c {
			n++
		}
	}
	if n != racers {
		t.Fatalf("the control must show the window OPEN: %d of %d racers inserted, so the race this lock closes is no longer reproducible here and its justification must be re-derived", n, racers)
	}
}

// TestConcurrentBootstrapYieldsExactlyOneSuperadminOnPostgres is what the lock
// buys, measured on the engine that can exhibit the defect.
//
// The racers rendezvous BEFORE the lock, so every one of them is inside the
// bootstrap transaction with the window open; the lock is then the only thing
// standing between them and six superadmins. The emails DIFFER on purpose:
// identical ones would be caught by UNIQUE(email) and the test would pass with
// the lock removed, proving nothing.
//
// The mutant it is written to catch is deleting the lockBootstrapTransaction
// call, which still compiles and still passes every SQLite leg.
func TestConcurrentBootstrapYieldsExactlyOneSuperadminOnPostgres(t *testing.T) {
	ctx, st := openRacePG(t)
	a := NewAuthenticator(st, model.SystemClock{})

	const racers = 6
	var arrived sync.WaitGroup
	arrived.Add(racers)
	bootstrapRendezvous = func() {
		arrived.Done()
		arrived.Wait()
	}
	t.Cleanup(func() { bootstrapRendezvous = nil })

	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = a.BootstrapSuperadmin(ctx, fmt.Sprintf("root%d@example.com", i), "bootstrap-pass-1234")
		}(i)
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrSetupComplete):
			// The losers are told the truth: by the time they held the lock, a
			// superadmin existed.
		default:
			t.Fatalf("racer %d failed for an unexpected reason: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one concurrent bootstrap may succeed, %d did", winners)
	}

	// The row count is the invariant, not the error tally: a call that errored
	// AFTER inserting would still leave two superadmins behind.
	var users int
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		list, _, err := as.Users().List(ctx, model.Query{Limit: 100})
		users = len(list)
		return err
	}); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Fatalf("the estate must hold exactly one superadmin after a concurrent bootstrap, holds %d", users)
	}
}
