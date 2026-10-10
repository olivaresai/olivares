// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/store"
	"log/slog"
	"testing"
	"time"
)

// C2 item 3: with no enabled schedule configured the pump evaluates at most
// once per hour (the safety cadence); with one it evaluates every tick. A probe
// error never throttles (the due path owns its failure reporting).
type fakeDRProbe struct {
	configured bool
	err        error
	evals      int
}

func (f *fakeDRProbe) ScheduledBackupConfigured(context.Context) (bool, error) {
	return f.configured, f.err
}

func (f *fakeDRProbe) RunDueScheduledBackup(context.Context, time.Time) (bool, error) {
	f.evals++
	return false, nil
}

func TestDRSchedulePumpThrottlesTheUnconfiguredCase(t *testing.T) {
	probe := &fakeDRProbe{configured: false}
	p := &drSchedulePump{st: fakeStoreLeader(true), api: probe, interval: time.Minute, clock: time.Now, log: slog.Default()}
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probe.evals != 1 {
		t.Fatalf("the first tick always evaluates (lastEval starts zero), got %d", probe.evals)
	}
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probe.evals != 1 {
		t.Fatalf("an unconfigured pump must wait out the safety cadence, evals = %d", probe.evals)
	}
	// The safety window elapsed: it evaluates again.
	p.lastEval = time.Now().Add(-2 * drScheduleSafetyInterval)
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probe.evals != 2 {
		t.Fatalf("after the safety interval it evaluates again, evals = %d", probe.evals)
	}
}

func TestDRSchedulePumpEvaluatesEveryTickWhenConfigured(t *testing.T) {
	probe := &fakeDRProbe{configured: true}
	p := &drSchedulePump{st: fakeStoreLeader(true), api: probe, interval: time.Minute, clock: time.Now, log: slog.Default()}
	for i := 0; i < 3; i++ {
		if err := p.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if probe.evals != 3 {
		t.Fatalf("a configured pump evaluates every tick, evals = %d, want 3", probe.evals)
	}
}

func TestDRSchedulePumpProbeErrorNeverThrottles(t *testing.T) {
	probe := &fakeDRProbe{configured: false, err: errors.New("store down")}
	p := &drSchedulePump{st: fakeStoreLeader(true), api: probe, interval: time.Minute, clock: time.Now, log: slog.Default()}
	for i := 0; i < 2; i++ {
		if err := p.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if probe.evals != 2 {
		t.Fatalf("a probe error must not hide the due path: evals = %d, want 2", probe.evals)
	}
}

// fakeStoreLeader answers the pump's leader check. Everything else on
// store.Store is unreachable in these tests.
func fakeStoreLeader(active bool) store.Store { return leaderStub{active: active} }

type leaderStub struct {
	store.Store
	active bool
}

func (s leaderStub) Leader() store.LeaderElector { return leaderElectorStub{active: s.active} }

type leaderElectorStub struct{ active bool }

func (l leaderElectorStub) IsLeader() bool                        { return l.active }
func (l leaderElectorStub) Active() bool                          { return l.active }
func (l leaderElectorStub) Epoch() uint64                         { return 1 }
func (l leaderElectorStub) Run(context.Context) error             { return nil }
func (l leaderElectorStub) Resign(context.Context) error          { return nil }
func (l leaderElectorStub) OnPromote(func(context.Context) error) {}
