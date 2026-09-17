// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// dabPeerWait is the finite budget every phase wait, contention observation and
// join gets. A schedule that has not formed within it is a FAILURE, never a
// longer sleep: the outer go test timeout must never be what ends one of these
// tests.
const dabPeerWait = 20 * time.Second

var (
	// errDABEarlyExit: a worker returned before the phase the schedule was
	// waiting for formed. It is observed as soon as the worker exits, not when the
	// budget elapses.
	errDABEarlyExit = errors.New("schedule worker exited before the phase formed")
	// errDABPhaseDeadline: the phase did not form within the budget while every
	// worker was still running.
	errDABPhaseDeadline = errors.New("schedule phase did not form within its budget")
	// errDABJoinDeadline: workers finished only after the schedule was canceled.
	errDABJoinDeadline = errors.New("schedule workers needed cancellation to finish")
	// errDABNoTermination: workers were still running after cancellation plus a
	// second finite budget. The test reports it instead of waiting further.
	errDABNoTermination = errors.New("schedule workers did not terminate after cancellation")
	// errDABDeliberateRollback is the holder's own rollback outcome.
	errDABDeliberateRollback = errors.New("deliberate rollback")
)

// dabWorker is one goroutine that owns SQL work in a schedule. err is written
// only by that goroutine, before done closes, and read only after done closes.
type dabWorker struct {
	name string
	done chan struct{}
	err  error
}

func (w *dabWorker) finished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// dabSchedule owns the lifetime of every worker in one contention schedule.
//
// All SQL a worker runs uses the schedule's context, so cancellation is finite;
// a holder parks through hold, which also returns on cancellation, so release is
// never the only way out; every phase wait watches worker exits as well as the
// deadline; and joins escalate from release to cancellation within two budgets.
// The test's Cleanup performs the same bounded shutdown, so a t.Fatal at any
// point leaves no goroutine whose end depends on the suite timeout. Cleanup is
// registered after the store fixture's, so it runs BEFORE the store closes.
type dabSchedule struct {
	ctx         context.Context
	cancel      context.CancelFunc
	budget      time.Duration
	release     chan struct{}
	releaseOnce sync.Once
	exited      chan struct{}
	exitedOnce  sync.Once
	mu          sync.Mutex
	workers     []*dabWorker
}

func newDABSchedule(t *testing.T, budget time.Duration) *dabSchedule {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sch := &dabSchedule{
		ctx: ctx, cancel: cancel, budget: budget,
		release: make(chan struct{}), exited: make(chan struct{}),
	}
	t.Cleanup(func() {
		if err := sch.shutdown(); err != nil {
			t.Errorf("schedule cleanup: %v", err)
		}
	})
	return sch
}

// releaseHolder is unconditional and idempotent.
func (s *dabSchedule) releaseHolder() { s.releaseOnce.Do(func() { close(s.release) }) }

// hold parks a holder inside its transaction until release or cancellation.
func (s *dabSchedule) hold(ctx context.Context) error {
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *dabSchedule) goWorker(name string, fn func(context.Context) error) *dabWorker {
	w := &dabWorker{name: name, done: make(chan struct{})}
	s.mu.Lock()
	s.workers = append(s.workers, w)
	s.mu.Unlock()
	go func() {
		defer func() {
			close(w.done)
			s.exitedOnce.Do(func() { close(s.exited) })
		}()
		w.err = fn(s.ctx)
	}()
	return w
}

func (s *dabSchedule) snapshot() []*dabWorker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*dabWorker(nil), s.workers...)
}

func (s *dabSchedule) allFinished() bool {
	for _, w := range s.snapshot() {
		if !w.finished() {
			return false
		}
	}
	return true
}

// earlyExit names every worker that has already returned, with its result.
func (s *dabSchedule) earlyExit(phase string) error {
	var causes []error
	for _, w := range s.snapshot() {
		if !w.finished() {
			continue
		}
		if w.err != nil {
			causes = append(causes, fmt.Errorf("worker %q: %w", w.name, w.err))
		} else {
			causes = append(causes, fmt.Errorf("worker %q returned nil", w.name))
		}
	}
	return fmt.Errorf("%w: phase %q: %w", errDABEarlyExit, phase, errors.Join(causes...))
}

// awaitPhase waits for ready, an early worker exit, or the budget. Phases are
// awaited only while every started worker is expected to be alive; a worker
// that closes ready and then returns is not an early exit.
func (s *dabSchedule) awaitPhase(phase string, ready <-chan struct{}) error {
	timer := time.NewTimer(s.budget)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-s.exited:
		select {
		case <-ready:
			return nil
		default:
		}
		return s.earlyExit(phase)
	case <-timer.C:
		return fmt.Errorf("%w: phase %q after %s", errDABPhaseDeadline, phase, s.budget)
	}
}

// awaitAdvisoryWaiter waits until at least one backend of THIS database is
// waiting on an ungranted advisory lock, which is what the directory writer's
// global admission is. It observes the SERVER's own view rather than guessing
// with a sleep, and it is precise: an ordinary row lock or a chain lock does not
// satisfy it, so a pass cannot come from unrelated contention. A worker that
// returns instead of parking ends the wait at once, and every query is bounded.
func (s *dabSchedule) awaitAdvisoryWaiter(db *sql.DB, phase string) error {
	deadline := time.NewTimer(s.budget)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		queryCtx, cancel := context.WithTimeout(s.ctx, s.budget)
		var waiting int
		err := db.QueryRowContext(queryCtx,
			`SELECT count(*) FROM pg_catalog.pg_locks l
			   JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
			  WHERE l.locktype = 'advisory' AND NOT l.granted
			    AND a.datname = pg_catalog.current_database()`).Scan(&waiting)
		cancel()
		if err != nil {
			return fmt.Errorf("observe advisory contention for phase %q: %w", phase, err)
		}
		if waiting > 0 {
			return nil
		}
		select {
		case <-s.exited:
			return s.earlyExit(phase)
		case <-deadline.C:
			return fmt.Errorf("%w: phase %q after %s", errDABPhaseDeadline, phase, s.budget)
		case <-tick.C:
		}
	}
}

func (s *dabSchedule) waitAll(budget time.Duration) error {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for _, w := range s.snapshot() {
		select {
		case <-w.done:
		case <-timer.C:
			return fmt.Errorf("worker %q still running after %s", w.name, budget)
		}
	}
	return nil
}

// abort is the failure path: cancel every SQL context, then release any parked
// holder, then wait one finite budget.
func (s *dabSchedule) abort() error {
	s.cancel()
	s.releaseHolder()
	if err := s.waitAll(s.budget); err != nil {
		return fmt.Errorf("%w: %v", errDABNoTermination, err)
	}
	return nil
}

// join is the success path after the caller released the holder. Workers get
// one budget to finish on their own; otherwise the schedule aborts.
func (s *dabSchedule) join() error {
	if err := s.waitAll(s.budget); err == nil {
		return nil
	} else if abortErr := s.abort(); abortErr != nil {
		return errors.Join(err, abortErr)
	} else {
		return fmt.Errorf("%w: %v", errDABJoinDeadline, err)
	}
}

// shutdown is the Cleanup path: release, let workers finish within one budget,
// and cancel only if they do not.
func (s *dabSchedule) shutdown() error {
	defer s.cancel()
	s.releaseHolder()
	if s.waitAll(s.budget) == nil {
		return nil
	}
	return s.abort()
}

// dabBarrierPort is the goroutine-safe assertion: a worker goroutine must not
// call t.Fatal, so absence is returned as an error instead.
func dabBarrierPort(sc store.Scope) (store.DirectoryAuthoritySnapshotLocker, error) {
	barrier, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
	if !ok {
		return nil, errors.New("ordinary scope lacks the directory-authority barrier")
	}
	return barrier, nil
}

// dabHeldPlan is one ordinary DAB1 holder plus one contending peer.
type dabHeldPlan struct {
	tenant   model.TenantID
	bundle   store.AuthoritySnapshotBundle
	label    string
	rollback bool
	// beforeAdmission runs inside the holder's transaction before the barrier.
	// Only the failure-path cases set it.
	beforeAdmission func(context.Context) error
	peer            func(context.Context) error
}

type dabHeldResult struct {
	holderErr         error
	peerErr           error
	peerStarted       bool
	peerDoneWhileHeld bool
}

// runDABHeldSchedule drives: holder admitted -> peer started -> peer observed
// parked on the server -> completion sampled while the holder HOLDS -> release
// -> bounded join. It returns a harness error for any phase that does not form,
// and it has always finished or reported every worker when it returns.
func runDABHeldSchedule(s *sqlStore, sch *dabSchedule, plan dabHeldPlan) (dabHeldResult, error) {
	var res dabHeldResult
	admitted := make(chan struct{})
	holder := sch.goWorker("holder", func(ctx context.Context) error {
		return s.Mutate(ctx, plan.tenant, func(sc store.Scope) error {
			if plan.beforeAdmission != nil {
				if err := plan.beforeAdmission(ctx); err != nil {
					return err
				}
			}
			barrier, err := dabBarrierPort(sc)
			if err != nil {
				return err
			}
			if err := barrier.LockDirectoryAuthoritySnapshot(ctx, plan.bundle); err != nil {
				return err
			}
			close(admitted)
			if err := sch.hold(ctx); err != nil {
				return err
			}
			if _, err := dabMarker(ctx, sc, plan.label); err != nil {
				return err
			}
			if plan.rollback {
				return errDABDeliberateRollback
			}
			return nil
		})
	})
	collect := func(peer *dabWorker) {
		if holder.finished() {
			res.holderErr = holder.err
		}
		if peer != nil && peer.finished() {
			res.peerErr = peer.err
		}
	}
	if err := sch.awaitPhase("holder admitted", admitted); err != nil {
		err = errors.Join(err, sch.abort())
		collect(nil)
		return res, err
	}

	peerStarted := make(chan struct{})
	var peerDone atomic.Bool
	peer := sch.goWorker("peer", func(ctx context.Context) error {
		close(peerStarted)
		err := plan.peer(ctx)
		peerDone.Store(true)
		return err
	})
	res.peerStarted = true
	for _, step := range []func() error{
		func() error { return sch.awaitPhase("peer started", peerStarted) },
		func() error { return sch.awaitAdvisoryWaiter(s.db, "peer parked behind holder") },
	} {
		if err := step(); err != nil {
			err = errors.Join(err, sch.abort())
			collect(peer)
			return res, err
		}
	}
	// Sample the contender's completion while the holder still holds every lock
	// it took. A flag read after both return cannot prove ordering.
	res.peerDoneWhileHeld = peerDone.Load()
	sch.releaseHolder()
	err := sch.join()
	collect(peer)
	return res, err
}

// dabAuthWriterOrder names the two SUPPORTED AuthMutate schedules. Neither is
// audit-first with respect to DIRECTORY admission: authAuditLog.Append and
// LockAppends both call globalFirst, and a transaction that appended tenant
// audit before any directory write is refused by the tracker rather than
// deadlocking. The difference between them is where the SYSTEM audit append
// (and, with budgeting enabled, the shared global spool row) sits relative to
// the User update that bumps H.
type dabAuthWriterOrder string

const (
	// directory global -> SYSTEM audit + spool -> Users().Update -> H
	dabAuditBeforeUser dabAuthWriterOrder = "audit-before-user"
	// directory global -> Users().Update -> H -> SYSTEM audit + spool
	dabUserBeforeAudit dabAuthWriterOrder = "user-before-audit"
)

func dabRunAuthWriter(
	ctx context.Context,
	s *sqlStore,
	order dabAuthWriterOrder,
	target model.ID,
	label string,
) error {
	return s.AuthMutate(ctx, func(as store.AuthScope) error {
		appendAudit := func() error {
			_, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: "test.auth.writer." + string(order),
			})
			return err
		}
		updateUser := func() error {
			fresh, err := as.Users().Get(ctx, target)
			if err != nil {
				return err
			}
			fresh.DisplayName = label
			_, err = as.Users().Update(ctx, fresh)
			return err
		}
		if order == dabAuditBeforeUser {
			if err := appendAudit(); err != nil {
				return err
			}
			return updateUser()
		}
		if err := updateUser(); err != nil {
			return err
		}
		return appendAudit()
	})
}

// TestDirectoryAuthoritySupportedAuthSchedulesPG is the PostgreSQL schedule with
// the global spool budget ENABLED. Both supported AuthMutate orders must park
// behind the ordinary DAB1 holder's directory global admission and complete only
// after that transaction commits OR rolls back. Blocking is established from the
// server's own lock view; completion is sampled while the holder still HOLDS.
func TestDirectoryAuthoritySupportedAuthSchedulesPG(t *testing.T) {
	for _, order := range []dabAuthWriterOrder{dabAuditBeforeUser, dabUserBeforeAudit} {
		for _, outcome := range []string{"commit", "rollback"} {
			t.Run(string(order)+"/"+outcome, func(t *testing.T) {
				ctx := context.Background()
				s, users, tenants := dabSpoolTarget(t, store.EnginePostgres)
				tenant, admin, target := tenants[0], users[0], users[1]
				live := ataFacts(t, s, tenant, admin.ID)
				label := "dab-pg-" + string(order) + "-" + outcome

				sch := newDABSchedule(t, dabPeerWait)
				res, err := runDABHeldSchedule(s, sch, dabHeldPlan{
					tenant: tenant, bundle: live, label: label, rollback: outcome == "rollback",
					peer: func(ctx context.Context) error {
						return dabRunAuthWriter(ctx, s, order, target.ID, label)
					},
				})
				if err != nil {
					t.Fatalf("schedule: %v (holder=%v peer=%v)", err, res.holderErr, res.peerErr)
				}
				if outcome == "commit" && res.holderErr != nil {
					t.Fatalf("admitted transaction: %v", res.holderErr)
				}
				if outcome == "rollback" && !errors.Is(res.holderErr, errDABDeliberateRollback) {
					t.Fatalf("holder err=%v, want the deliberate rollback", res.holderErr)
				}
				if res.peerErr != nil {
					t.Fatalf("supported auth writer did not proceed after A finished: %v", res.peerErr)
				}
				if res.peerDoneWhileHeld {
					t.Error("the supported auth writer completed while the admitted " +
						"transaction held its locks")
				}
				// B really landed, and A's marker followed its own outcome.
				if err := s.AuthView(ctx, func(as store.AuthScope) error {
					u, err := as.Users().Get(ctx, target.ID)
					if err != nil {
						return err
					}
					if u.DisplayName != label {
						t.Errorf("auth writer did not commit: DisplayName=%q", u.DisplayName)
					}
					return nil
				}); err != nil {
					t.Fatalf("read back: %v", err)
				}
				want := 1
				if outcome == "rollback" {
					want = 0
				}
				if got := dabCountMarkers(t, s, tenant, label); got != want {
					t.Errorf("markers=%d, want %d", got, want)
				}
			})
		}
	}
}

// TestDirectoryAuthorityOrdinaryAndSystemSchedulesPG covers the two remaining
// declared schedules: an ordinary lineage writer on the SAME business tenant,
// and a SYSTEM-partition writer that creates a new User (which takes directory
// admission to insert its H). Both must park behind the DAB1 holder and complete
// after it.
func TestDirectoryAuthorityOrdinaryAndSystemSchedulesPG(t *testing.T) {
	peers := map[string]func(ctx context.Context, s *sqlStore, tenant model.TenantID, label string) error{
		"ordinary-lineage-same-tenant": func(ctx context.Context, s *sqlStore, tenant model.TenantID, label string) error {
			return s.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.Identities().Create(ctx, model.Identity{
					Name: label, Kind: "service", ExternalID: label,
				})
				return err
			})
		},
		"system-partition-writer": func(ctx context.Context, s *sqlStore, _ model.TenantID, label string) error {
			return s.AuthMutate(ctx, func(as store.AuthScope) error {
				_, err := as.Users().Create(ctx, model.User{
					Email: label + "@example.test", Status: model.StatusActive,
				})
				return err
			})
		},
	}
	for name, peer := range peers {
		t.Run(name, func(t *testing.T) {
			s, users, tenants := dabSpoolTarget(t, store.EnginePostgres)
			tenant, admin := tenants[0], users[0]
			live := ataFacts(t, s, tenant, admin.ID)
			label := "dab-pg-" + name

			sch := newDABSchedule(t, dabPeerWait)
			res, err := runDABHeldSchedule(s, sch, dabHeldPlan{
				tenant: tenant, bundle: live, label: label,
				peer: func(ctx context.Context) error { return peer(ctx, s, tenant, label) },
			})
			if err != nil {
				t.Fatalf("schedule: %v (holder=%v peer=%v)", err, res.holderErr, res.peerErr)
			}
			if res.holderErr != nil {
				t.Fatalf("admitted transaction: %v", res.holderErr)
			}
			if res.peerErr != nil {
				t.Fatalf("peer writer did not proceed after the holder finished: %v", res.peerErr)
			}
			if res.peerDoneWhileHeld {
				t.Error("the peer writer completed while the admitted transaction held its locks")
			}
			if got := dabCountMarkers(t, s, tenant, label); got != 1 {
				t.Errorf("markers=%d, want 1", got)
			}
		})
	}
}

// TestDirectoryAuthorityAuditBeforeAdmissionRefusedPG is the negative baseline.
// A transaction that appends its tenant audit BEFORE any directory admission is
// refused IN-TRANSACTION by the tracker — it never reaches the global lock — so
// the evidence for the schedules above cannot be confused with this case. It
// runs while a DAB1 holder is admitted precisely so a refusal and a lock wait
// are told apart: a wait would not return until the holder released, and this
// one returns while the holder is still inside its callback.
func TestDirectoryAuthorityAuditBeforeAdmissionRefusedPG(t *testing.T) {
	s, users, tenants := dabSpoolTarget(t, store.EnginePostgres)
	tenant, other, admin := tenants[0], tenants[1], users[0]
	live := ataFacts(t, s, tenant, admin.ID)
	otherLive := ataFacts(t, s, other, admin.ID)

	sch := newDABSchedule(t, dabPeerWait)
	admitted := make(chan struct{})
	refused := make(chan struct{})
	var (
		refusal         error
		tookGlobal      bool
		poisonedTracker bool
		poisonedBinding bool
	)

	holder := sch.goWorker("holder", func(ctx context.Context) error {
		return s.Mutate(ctx, tenant, func(sc store.Scope) error {
			barrier, err := dabBarrierPort(sc)
			if err != nil {
				return err
			}
			if err := barrier.LockDirectoryAuthoritySnapshot(ctx, live); err != nil {
				return err
			}
			close(admitted)
			// The holder is released only AFTER the negative case has returned its
			// verdict (or its bounded wait ended), so a verdict that arrives is by
			// construction one that landed while this transaction still held
			// global directory admission.
			if err := sch.hold(ctx); err != nil {
				return err
			}
			_, err = dabMarker(ctx, sc, "dab-pg-negative-holder")
			return err
		})
	})
	if err := sch.awaitPhase("holder admitted", admitted); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	peer := sch.goWorker("audit-first peer", func(ctx context.Context) error {
		// A DIFFERENT tenant, so the ordinary lineage prelude cannot be what
		// serializes it: only the directory tracker's own order rule can.
		return s.Mutate(ctx, other, func(sc store.Scope) error {
			ts := sc.(*tenantScope)
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:" + admin.ID.String(), ActorKind: model.ActorUser,
				Action: "test.audit.before.admission",
			}); err != nil {
				return err
			}
			barrier, err := dabBarrierPort(sc)
			if err != nil {
				return err
			}
			refusal = barrier.LockDirectoryAuthoritySnapshot(ctx, otherLive)
			tookGlobal = ts.directoryWriter.locked
			// An ORDER refusal is precise and NON-poisoning by contract: it is
			// checked before admission and performs no source write, so the
			// transaction stays usable. That is the documented difference from an
			// entry-state or post-admission failure, and it is asserted here
			// rather than assumed.
			poisonedTracker = ts.directoryWriter.poisoned != nil
			poisonedBinding = ts.bindingPoison != nil
			close(refused)
			return refusal
		})
	})

	// Wait for the verdict, an early peer exit, or the budget; then release the
	// holder and join within a finite budget. Never release after joining.
	verdictErr := sch.awaitPhase("audit-first peer refused", refused)
	sch.releaseHolder()
	if err := sch.join(); err != nil {
		t.Fatalf("join: %v", err)
	}
	if verdictErr != nil {
		t.Fatalf("the refusal did not land while the admitted holder still held its locks, "+
			"so it cannot be distinguished from a lock wait: %v", verdictErr)
	}

	if !errors.Is(refusal, errDirectoryAuthorityOrder) {
		t.Fatalf("refusal=%v, want the tracked audit-before-directory order refusal", refusal)
	}
	if tookGlobal {
		t.Error("the refused transaction acquired global directory admission")
	}
	if !errors.Is(peer.err, errDirectoryAuthorityOrder) {
		t.Fatalf("peer transaction err=%v, want the order refusal through the envelope", peer.err)
	}
	if poisonedTracker || poisonedBinding {
		t.Errorf("an order refusal poisoned the transaction (tracker=%v binding=%v): "+
			"a pre-admission refusal must stay precise and recoverable",
			poisonedTracker, poisonedBinding)
	}
	if holder.err != nil {
		t.Fatalf("admitted holder: %v", holder.err)
	}
	// The holder is untouched by the refused peer.
	if got := dabCountMarkers(t, s, tenant, "dab-pg-negative-holder"); got != 1 {
		t.Errorf("holder markers=%d, want 1", got)
	}
}

// TestDirectoryAuthorityStaleLoserPG is the controlled cross-partition stale
// schedule: a SYSTEM auth writer bumps the very H an ordinary DAB1 caller pinned.
// The loser parks on the winner's directory global admission and, once admitted,
// finds the authority moved. The ordering is driven by real observed phases, not
// by a sleep.
func TestDirectoryAuthorityStaleLoserPG(t *testing.T) {
	ctx := context.Background()
	s, users, tenants := dabSpoolTarget(t, store.EnginePostgres)
	tenant, admin := tenants[0], users[0]
	// The loser pins the authority observed BEFORE the winner moves it.
	observed := ataFacts(t, s, tenant, admin.ID)

	sch := newDABSchedule(t, dabPeerWait)
	bumped := make(chan struct{})
	winner := sch.goWorker("winner", func(ctx context.Context) error {
		// Takes directory global, bumps H(admin), then commits on release.
		return s.AuthMutate(ctx, func(as store.AuthScope) error {
			fresh, err := as.Users().Get(ctx, admin.ID)
			if err != nil {
				return err
			}
			fresh.DisplayName = "winner-bumped"
			if _, err := as.Users().Update(ctx, fresh); err != nil {
				return err
			}
			close(bumped)
			return sch.hold(ctx)
		})
	})
	if err := sch.awaitPhase("winner bumped H", bumped); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	loser := sch.goWorker("loser", func(ctx context.Context) error {
		// Same bundle: blocks on admission, then finds H moved.
		return s.Mutate(ctx, tenant, func(sc store.Scope) error {
			barrier, err := dabBarrierPort(sc)
			if err != nil {
				return err
			}
			if err := barrier.LockDirectoryAuthoritySnapshot(ctx, observed); err != nil {
				return err
			}
			_, err = dabMarker(ctx, sc, "dab-pg-loser")
			return err
		})
	})
	if err := sch.awaitAdvisoryWaiter(s.db, "loser parked behind winner"); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	sch.releaseHolder()
	if err := sch.join(); err != nil {
		t.Fatalf("join: %v", err)
	}

	if winner.err != nil {
		t.Fatalf("winner: %v", winner.err)
	}
	if !errors.Is(loser.err, store.ErrConflict) {
		t.Fatalf("loser err=%v, want ErrConflict", loser.err)
	}
	if got := dabCountMarkers(t, s, tenant, "dab-pg-loser"); got != 0 {
		t.Errorf("loser committed %d markers", got)
	}
	// The winner's change is the one that survives, and H really advanced.
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
}

// TestDirectoryAuthorityScheduleFailureTerminates proves the failure paths of the
// schedule harness end in bounded time instead of waiting for the suite timeout.
//
//   - pre-admission-refusal: the holder's REAL barrier refuses a malformed bundle
//     before admission, so the admitted phase can never form. The wait must end
//     on the holder's exit — far inside the budget — with that refusal, and the
//     peer must never start.
//   - stalled-before-admission: the holder parks inside its open transaction and
//     never reaches the barrier. The wait must end at a short budget, cancel the
//     transaction's context, and observe the holder terminate with that
//     cancellation.
//
// Neither case reaches the PostgreSQL-only contention observer, so both engines
// run them.
func TestDirectoryAuthorityScheduleFailureTerminates(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			s, users, tenants := dabSpoolTarget(t, engine)
			tenant, admin := tenants[0], users[0]

			t.Run("pre-admission-refusal", func(t *testing.T) {
				malformed := ataFacts(t, s, tenant, admin.ID)
				malformed.Facts[0].Version = 0
				label := "dab-failure-pre-admission"
				var peerRan atomic.Bool

				sch := newDABSchedule(t, dabPeerWait)
				start := time.Now()
				res, err := runDABHeldSchedule(s, sch, dabHeldPlan{
					tenant: tenant, bundle: malformed, label: label,
					peer: func(context.Context) error { peerRan.Store(true); return nil },
				})
				elapsed := time.Since(start)

				if !errors.Is(err, errDABEarlyExit) {
					t.Fatalf("schedule err=%v, want the early worker exit", err)
				}
				if errors.Is(err, errDABPhaseDeadline) || errors.Is(err, errDABNoTermination) {
					t.Errorf("early exit was reported through a deadline: %v", err)
				}
				if elapsed >= dabPeerWait/4 {
					t.Errorf("early exit observed after %s; it must not wait for the %s budget",
						elapsed, dabPeerWait)
				}
				if res.holderErr == nil || !strings.Contains(res.holderErr.Error(), "malformed fact") {
					t.Errorf("holder err=%v, want the barrier's pre-admission malformed-fact refusal",
						res.holderErr)
				}
				if res.peerStarted || peerRan.Load() {
					t.Error("the peer started although the holder was never admitted")
				}
				if !sch.allFinished() {
					t.Error("a schedule worker is still running after the harness returned")
				}
				if got := dabCountMarkers(t, s, tenant, label); got != 0 {
					t.Errorf("markers=%d, want 0", got)
				}
			})

			t.Run("stalled-before-admission", func(t *testing.T) {
				live := ataFacts(t, s, tenant, admin.ID)
				label := "dab-failure-stalled"
				const budget = 750 * time.Millisecond
				entered := make(chan struct{})
				var peerRan atomic.Bool

				sch := newDABSchedule(t, budget)
				start := time.Now()
				res, err := runDABHeldSchedule(s, sch, dabHeldPlan{
					tenant: tenant, bundle: live, label: label,
					beforeAdmission: func(ctx context.Context) error {
						close(entered)
						<-ctx.Done()
						return ctx.Err()
					},
					peer: func(context.Context) error { peerRan.Store(true); return nil },
				})
				elapsed := time.Since(start)

				select {
				case <-entered:
				default:
					t.Fatal("the holder never entered its transaction; the stall was not exercised")
				}
				if !errors.Is(err, errDABPhaseDeadline) {
					t.Fatalf("schedule err=%v, want the phase deadline", err)
				}
				if errors.Is(err, errDABNoTermination) {
					t.Errorf("the canceled holder did not terminate: %v", err)
				}
				if limit := 3*budget + 2*time.Second; elapsed > limit {
					t.Errorf("stalled schedule ended after %s, want within %s", elapsed, limit)
				}
				if !errors.Is(res.holderErr, context.Canceled) {
					t.Errorf("holder err=%v, want its transaction context canceled", res.holderErr)
				}
				if res.peerStarted || peerRan.Load() {
					t.Error("the peer started although the holder was never admitted")
				}
				if !sch.allFinished() {
					t.Error("a schedule worker is still running after the harness returned")
				}
				if got := dabCountMarkers(t, s, tenant, label); got != 0 {
					t.Errorf("markers=%d, want 0", got)
				}
			})
		})
	}
}
