// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These are full serving boots using the production PostgreSQL elector and its
// advisory lock. Only edition recovery is substituted, at the edition seam.
func TestEditionPromotionPostgresFailoverAndReacquisition(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	var instances []*editionRecoveryFixture
	var leaders []store.LeaderElector
	var closed atomic.Int32
	var attempts atomic.Int32
	observed := make(chan int32, 8)
	release := make(chan struct{}, 8)
	failure := errors.New("edition recovery unavailable")
	thisEdition.promotionRecovery = func(_ context.Context, _ EditionConfig, _ []api.Module, st store.Store, _ *slog.Logger) (EditionPromotionRecovery, error) {
		index := len(instances)
		le := st.Leader()
		leaders = append(leaders, le)
		r := &editionRecoveryFixture{recover: func(ctx context.Context) error {
			if le.Active() || le.IsLeader() {
				return errors.New("edition recovery ran after public leadership became visible")
			}
			// Exercise the actual shared bootstrap: replacing its callback loses SYSTEM.
			if err := st.System(ctx, func(sys store.SystemScope) error {
				orgs, err := sys.ListOrgs(ctx)
				if err == nil && len(orgs) == 0 {
					return errors.New("core provisioning callback was lost")
				}
				return err
			}); err != nil {
				return err
			}
			if index == 0 {
				return nil
			}
			attempt := attempts.Add(1)
			observed <- attempt
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
			if attempt == 1 {
				return failure
			}
			return nil
		}, close: func() error {
			if le.Active() || le.IsLeader() {
				return errors.New("recovery closed before the elector drained")
			}
			closed.Add(1)
			return nil
		}}
		instances = append(instances, r)
		return r, nil
	}
	thisEdition.bindModuleDependencies = func(_ context.Context, _ EditionConfig, _ []api.Module, deps EditionDependencies, _ *slog.Logger) ([]io.Closer, error) {
		if deps.PromotionRecovery != instances[len(instances)-1] {
			return nil, errors.New("late binder received different recovery state")
		}
		return nil, nil
	}
	active := startWaitingPromotionNode(t, pg)
	standby := startWaitingPromotionNode(t, pg)
	if len(instances) != 2 || !leaders[0].IsLeader() || leaders[1].Active() || attempts.Load() != 0 {
		t.Fatal("initial election or standby recovery was incorrect")
	}
	readyCode := func(e *engine) int {
		rr := httptest.NewRecorder()
		e.api.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rr.Code
	}
	if readyCode(standby) != http.StatusServiceUnavailable {
		t.Fatal("standby was exposed as ready")
	}
	if err := standby.store.System(t.Context(), func(sys store.SystemScope) error {
		_, err := sys.CreateOrg(t.Context(), model.Org{Name: "Standby must refuse", Slug: "standby-refusal", Status: model.StatusActive})
		return err
	}); !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("standby write = %v", err)
	}
	awaitAttempt := func(want int32) {
		t.Helper()
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("recovery attempt = %d, want %d", got, want)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("elector did not run edition recovery")
		}
		if leaders[1].Active() || leaders[1].IsLeader() || readyCode(standby) != http.StatusServiceUnavailable {
			t.Fatal("unfinished recovery became visible")
		}
	}
	awaitLeader := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for !leaders[1].IsLeader() {
			select {
			case <-ctx.Done():
				t.Fatal("recovered elector did not establish leadership")
			case <-tick.C:
			}
		}
		if readyCode(standby) != http.StatusOK {
			t.Fatal("completed recovery did not make the engine ready")
		}
	}
	epoch := leaders[0].Epoch()
	if err := leaders[0].Resign(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitAttempt(1)
	release <- struct{}{} // This attempt fails. The same live follower must retry.
	awaitAttempt(2)
	if leaders[1].Epoch() != 0 {
		t.Fatal("failed recovery published a fencing epoch")
	}
	release <- struct{}{}
	awaitLeader()
	if leaders[1].Epoch() <= epoch {
		t.Fatal("failover did not advance the epoch")
	}
	epoch = leaders[1].Epoch()

	// Terminate only this isolated database's held election session. The same
	// boot must lose leadership and recover again before it reacquires the lock.
	db, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var terminated bool
	err = db.QueryRowContext(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_locks
 WHERE locktype = 'advisory' AND granted AND objsubid = 1
 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
 AND ((classid::bigint << 32) | objid::bigint) = hashtextextended('olivares.leader.v1', 0)`).Scan(&terminated)
	if err != nil || !terminated {
		t.Fatalf("terminate this test's election session: %v", err)
	}
	awaitAttempt(3)
	if leaders[1].Epoch() != epoch {
		t.Fatal("epoch advanced before reacquisition recovery completed")
	}
	release <- struct{}{}
	awaitLeader()
	if leaders[1].Epoch() <= epoch {
		t.Fatal("reacquisition did not advance the epoch")
	}
	if err := standby.Close(); err != nil {
		t.Fatal(err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 2 {
		t.Fatalf("closed recovery instances = %d, want 2", closed.Load())
	}
}

func TestEditionPromotionPostgresInitialFailure(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	failure := errors.New("initial edition recovery failed")
	var le store.LeaderElector
	recovered, closed := 0, 0
	thisEdition.promotionRecovery = func(_ context.Context, _ EditionConfig, _ []api.Module, st store.Store, _ *slog.Logger) (EditionPromotionRecovery, error) {
		le = st.Leader()
		return &editionRecoveryFixture{recover: func(context.Context) error {
			recovered++
			if le.Active() || le.IsLeader() {
				t.Error("initial recovery became visible before completion")
			}
			return failure
		}, close: func() error { closed++; return nil }}, nil
	}
	e, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
		Version: "test", ServeMode: true, Logger: discardLogger()})
	if e != nil {
		_ = e.Close()
		t.Fatal("failed initial recovery returned a serving engine")
	}
	if !errors.Is(err, failure) || recovered != 1 || closed != 1 || le.IsLeader() || le.Active() || le.Epoch() != 0 {
		t.Fatalf("initial failure: err=%v recovered=%d closed=%d", err, recovered, closed)
	}
	// The failed acquisition released the real advisory lock, permitting another boot.
	thisEdition.promotionRecovery = nil
	next := startWaitingPromotionNode(t, pg)
	if !next.store.Leader().IsLeader() {
		t.Fatal("initial recovery failure retained the advisory lock")
	}
}

// Cancellation cleanup can outlive the elector's five-second resignation
// deadline. No early or late edition resource may close during that cleanup.
func TestEditionPromotionPostgresShutdownJoinsRecovery(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	entered := make(chan struct{})
	canceled := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var closed, boundClosed atomic.Bool
	index := 0
	thisEdition.promotionRecovery = func(_ context.Context, _ EditionConfig, _ []api.Module, _ store.Store, _ *slog.Logger) (EditionPromotionRecovery, error) {
		node := index
		index++
		return &editionRecoveryFixture{recover: func(ctx context.Context) error {
			if node == 0 {
				return nil
			}
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-releaseCleanup
			if closed.Load() || boundClosed.Load() {
				return errors.New("edition state closed while recovery was unwinding")
			}
			return ctx.Err()
		}, close: func() error {
			if node == 1 {
				closed.Store(true)
			}
			return nil
		}}, nil
	}
	thisEdition.bindModuleDependencies = func(_ context.Context, _ EditionConfig, _ []api.Module, _ EditionDependencies, _ *slog.Logger) ([]io.Closer, error) {
		node := index - 1
		return []io.Closer{closerFunc(func() error {
			if node == 1 {
				boundClosed.Store(true)
			}
			return nil
		})}, nil
	}
	active := startWaitingPromotionNode(t, pg)
	standby := startWaitingPromotionNode(t, pg)
	// This cleanup is registered after the engines' cleanup, so even a failure
	// unblocks recovery before either engine's final Close joins it.
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(releaseCleanup)
		}
	}
	t.Cleanup(release)
	if err := active.store.Leader().Resign(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("standby recovery did not start")
	}
	done := make(chan error, 1)
	go func() { done <- standby.Close() }()
	select {
	case <-canceled:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not cancel recovery")
	}
	// Outlast the production resignation deadline, without releasing recovery.
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before recovery joined: %v", err)
	case <-time.After(6 * time.Second):
	}
	if closed.Load() || boundClosed.Load() {
		t.Fatal("shutdown closed edition resources before recovery joined")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not complete after recovery joined")
	}
	if !closed.Load() || !boundClosed.Load() {
		t.Fatal("shutdown failed to release both early and late resources")
	}
}
