// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestAuthTenantAuthorityConcurrentWriterPG is oracle 6, PostgreSQL leg: a
// supported target-user writer must WAIT behind the admitted global directory
// writer and then proceed once the admitted transaction finishes. Both actors are
// real, distinct transactions and every phase is signalled, never slept on.
func TestAuthTenantAuthorityConcurrentWriterPG(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, store.EnginePostgres)
			tenant, admin, target := tenants[0], users[0], users[1]
			live := ataFacts(t, s, tenant, admin.ID)

			admitted := make(chan struct{})      // A has the barrier and the global lock
			writerStarted := make(chan struct{}) // B is about to contend
			release := make(chan struct{})       // A may finish
			// Releasing peers is unconditional and finite: a t.Fatal below must not
			// leave A parked on <-release until the outer go test timeout.
			var releaseOnce sync.Once
			releasePeers := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releasePeers)
			var (
				wg sync.WaitGroup
				// bFinished is sampled while A still HOLDS its locks. A timestamp
				// sampled after each transaction returns cannot prove ordering: B may
				// return and read its clock before A reads its own.
				bFinished     atomic.Bool
				bFinishedHeld bool
				bErr          error
				aErr          error
				bBlocked      bool
			)

			wg.Add(1)
			go func() { // transaction A: admitted, then holds the global lock
				defer wg.Done()
				aErr = s.AuthMutate(ctx, func(as store.AuthScope) error {
					if err := as.(store.AuthTenantAuthorityBarrier).
						LockAuthTenantAuthority(ctx, tenant, live); err != nil {
						return err
					}
					close(admitted)
					<-release
					if _, err := ataMarker(ctx, as, tenant, "ata-pg-"+outcome); err != nil {
						return err
					}
					if outcome == "rollback" {
						return errors.New("deliberate rollback")
					}
					return nil
				})
			}()

			<-admitted
			wg.Add(1)
			go func() { // transaction B: a supported target-User writer
				defer wg.Done()
				close(writerStarted)
				bErr = s.AuthMutate(ctx, func(as store.AuthScope) error {
					fresh, err := as.Users().Get(ctx, target.ID)
					if err != nil {
						return err
					}
					fresh.DisplayName = "contended-" + outcome
					_, err = as.Users().Update(ctx, fresh)
					return err
				})
				bFinished.Store(true)
			}()

			<-writerStarted
			// B must still be contending while A holds admission. Observe it on the
			// server rather than guessing: PostgreSQL reports the waiting backend.
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				var waiting int
				if err := s.db.QueryRowContext(ctx,
					`SELECT count(*) FROM pg_catalog.pg_stat_activity
					   WHERE wait_event_type = 'Lock' AND state = 'active'
					     AND datname = pg_catalog.current_database()`).Scan(&waiting); err != nil {
					t.Fatalf("observe contention: %v", err)
				}
				if waiting > 0 {
					bBlocked = true
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if !bBlocked {
				t.Fatal("the supported writer never waited behind the admitted global writer")
			}
			// Sample the contender's completion while A still holds every lock it
			// took. This is the held-phase assertion the ordering actually needs.
			bFinishedHeld = bFinished.Load()
			releasePeers()
			wg.Wait()

			if outcome == "commit" && aErr != nil {
				t.Fatalf("admitted transaction: %v", aErr)
			}
			if outcome == "rollback" && aErr == nil {
				t.Fatal("deliberate rollback committed")
			}
			if bErr != nil {
				t.Fatalf("supported writer did not proceed after A finished: %v", bErr)
			}
			if bFinishedHeld {
				t.Error("the supported writer completed while the admitted transaction held its locks")
			}
			// The writer really landed, and A's marker followed its own outcome.
			if err := s.AuthView(ctx, func(as store.AuthScope) error {
				u, err := as.Users().Get(ctx, target.ID)
				if err != nil {
					return err
				}
				if u.DisplayName != "contended-"+outcome {
					t.Errorf("writer did not commit: DisplayName=%q", u.DisplayName)
				}
				return nil
			}); err != nil {
				t.Fatalf("read back: %v", err)
			}
			want := 1
			if outcome == "rollback" {
				want = 0
			}
			if got := ataCountMarkers(t, s, "ata-pg-"+outcome); got != want {
				t.Errorf("markers=%d, want %d", got, want)
			}
		})
	}
}

// TestAuthTenantAuthorityStaleLoserPG is oracle 6's controlled stale-authority
// schedule: the loser's admission fails on the authority the winner changed, and
// the ordering is driven by real phases rather than a sleep-only guess.
func TestAuthTenantAuthorityStaleLoserPG(t *testing.T) {
	ctx := context.Background()
	s, _, users, tenants := f2aFreshTarget(t, store.EnginePostgres)
	tenant, admin := tenants[0], users[0]
	// Both actors start from the SAME observed authority.
	observed := ataFacts(t, s, tenant, admin.ID)

	admitted := make(chan struct{})
	loserContending := make(chan struct{})
	// The winner parks on <-loserContending; release it unconditionally so an
	// early failure cannot strand a peer goroutine.
	var contendOnce sync.Once
	releasePeers := func() { contendOnce.Do(func() { close(loserContending) }) }
	t.Cleanup(releasePeers)
	var wg sync.WaitGroup
	var winnerErr, loserErr error

	wg.Add(1)
	go func() { // winner: admitted first, then bumps the very H the loser pinned
		defer wg.Done()
		winnerErr = s.AuthMutate(ctx, func(as store.AuthScope) error {
			if err := as.(store.AuthTenantAuthorityBarrier).
				LockAuthTenantAuthority(ctx, tenant, observed); err != nil {
				return err
			}
			close(admitted)
			<-loserContending
			fresh, err := as.Users().Get(ctx, admin.ID)
			if err != nil {
				return err
			}
			fresh.DisplayName = "winner-bumped"
			_, err = as.Users().Update(ctx, fresh) // bumps H(admin)
			return err
		})
	}()

	<-admitted
	wg.Add(1)
	go func() { // loser: same bundle, blocks on admission, then finds H moved
		defer wg.Done()
		releasePeers()
		loserErr = s.AuthMutate(ctx, func(as store.AuthScope) error {
			if err := as.(store.AuthTenantAuthorityBarrier).
				LockAuthTenantAuthority(ctx, tenant, observed); err != nil {
				return err
			}
			_, err := ataMarker(ctx, as, tenant, "ata-pg-loser")
			return err
		})
	}()

	wg.Wait()
	if winnerErr != nil {
		t.Fatalf("winner: %v", winnerErr)
	}
	if !errors.Is(loserErr, store.ErrConflict) {
		t.Fatalf("loser err=%v, want ErrConflict", loserErr)
	}
	if got := ataCountMarkers(t, s, "ata-pg-loser"); got != 0 {
		t.Errorf("loser committed %d markers", got)
	}
	// The winner's change is the one that survives.
	if err := s.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, admin.ID)
		if err != nil {
			return err
		}
		if u.DisplayName != "winner-bumped" {
			t.Errorf("winner did not commit: DisplayName=%q", u.DisplayName)
		}
		h, err := as.(store.AuthUserAuthorityEvidenceScope).ReadUserAuthorityFact(ctx, admin.ID)
		if err != nil {
			return err
		}
		if h.Version <= observed.UserAuthorities[0].Version {
			t.Errorf("H did not advance: %d", h.Version)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
	_ = model.SystemTenantID
}
