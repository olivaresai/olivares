// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

type recoveryFixture struct {
	report  RestoreReport
	restore func()
}

func (r *recoveryFixture) Restore(context.Context, Window) (RestoreReport, error) {
	if r.restore != nil {
		r.restore()
	}
	return r.report, nil
}
func (r *recoveryFixture) Report(context.Context, Window) (RestoreReport, error) {
	return r.report, nil
}
func (n *testNM) QualifyRecovery(context.Context) (TargetBinding, error) {
	c := pendingCall()
	return TargetBinding{BusID: c.BusID, Process: c.Target}, nil
}

func TestNetrestore_ReloadSuccessWithoutMeasuredStateIsNotRolledBack(t *testing.T) {
	e, n, c, change := newTestEngine(t)
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	w := cloneWindow(e.windows[change.OperationID])
	w.State = StateRestoring
	w.Phase = "restore"
	w.Attempt = 1
	w.Checkpoint = ""
	if err := e.save(w); err != nil {
		t.Fatal(err)
	}
	n.delay = UpdateInMemory
	e.config.Restorer = &recoveryFixture{report: RestoreReport{Finished: true, MissingIntentSafe: true}}
	c.now += 61 * time.Second
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Status(change.OperationID)
	if s.State == StateRolledBack || s.PendingCalls != 1 {
		t.Fatal("helper success was mistaken for measured recovery", s)
	}
}
func TestGuard_RestoresThroughNetrestoreAfterADaemonRestart(t *testing.T) {
	e, n, c, change := newTestEngine(t)
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	w := cloneWindow(e.windows[change.OperationID])
	w.State = StateRestoring
	w.Phase = "restore"
	w.Attempt = 1
	w.Checkpoint = ""
	if err := e.save(w); err != nil {
		t.Fatal(err)
	}
	called := false
	e.config.Restorer = &recoveryFixture{report: RestoreReport{Finished: true, MissingIntentSafe: true}, restore: func() { called = true; n.profile = n.baseline; n.applied = n.baseline; n.disk = n.baseline }}
	c.now += 61 * time.Second
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Status(change.OperationID)
	if !called || s.State != StateRolledBack || !n.applied.Matches(n.baseline) {
		t.Fatal(called, s)
	}
	// This fixture exercises restoration composition; original process death has its
	// independent protocol controls and the real daemon-restart guest case.
}
func TestGuard_RecordsRolledBackAfterAHostRestartOnlyWithMeasuredState(t *testing.T) {
	e, n, c, change := newTestEngine(t)
	n.delay = UpdateInMemory
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	c.boot = "boot-b"
	c.now = time.Second
	n.dead = DeathUnknown
	recovered, err := NewEngine(e.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := recovered.Status(change.OperationID)
	if s.State == StateRolledBack {
		t.Fatal("unknown pending effect finalized", s)
	}
	n.dead = DeathProven
	if err := recovered.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ = recovered.Status(change.OperationID)
	if s.State != StateRolledBack || !n.applied.Matches(n.baseline) {
		t.Fatal(s)
	}
}
func TestRootLedger_OrphanCompletionIsRefused(t *testing.T) {
	l := rootLedger{dir: t.TempDir(), uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
	c := pendingCall()
	c.Settled = true
	c.Settlement = "correlated_reply"
	if err := l.write(completionName(c), c); err != nil {
		t.Fatal(err)
	}
	if _, err := l.calls(); err == nil {
		t.Fatal("completion without intent accepted")
	}
}
func TestJournal_RestoreAttemptsAreBoundedWithoutLosingPendingCalls(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	n.delay = UpdateInMemory
	if _, _, err := e.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	w := cloneWindow(e.windows[change.OperationID])
	for i := 0; i <= MaxCallsPerWindow/2; i++ {
		w.RestoreAttempts = append(w.RestoreAttempts, RestoreAttemptRecord{Attempt: uint32(i + 1)})
	}
	if err := e.config.Journal.Save(w); !errors.Is(err, ErrJournalFull) {
		t.Fatal(err)
	}
	retained, err := e.config.Journal.Load(w.OperationID)
	if err != nil || !retained.Pending() || len(retained.RestoreAttempts) != 0 {
		t.Fatal(retained, err)
	}
}

func TestRecovery_FinalObservationNamesTheCurrentBoot(t *testing.T) {
	e, n, c, change := newTestEngine(t)
	if _, _, err := e.Apply(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	c.boot = "boot-b"
	c.now = time.Second
	n.dead = DeathProven
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Status(change.OperationID)
	if err != nil || s.State != StateRolledBack || s.FinalBootID != "boot-b" || s.BootID != "boot-a" {
		t.Fatal(s, err)
	}
}

func TestRecovery_SettledFailureStartsAnotherAttemptWithinTheSameWindow(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	if _, _, err := e.Apply(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	w := cloneWindow(e.windows[change.OperationID])
	generation := w.Generation
	w.State = StateRestoring
	w.Phase = "restore"
	w.Attempt = 1
	w.Checkpoint = ""
	if err := e.save(w); err != nil {
		t.Fatal(err)
	}
	e.config.Restorer = &recoveryFixture{report: RestoreReport{Finished: true, MissingIntentSafe: true}}
	n.delay = UpdateInMemory
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	w = e.windows[change.OperationID]
	last := w.Calls[len(w.Calls)-1]
	n.replies <- Reply{BusID: last.BusID, ConnectionGeneration: last.ConnectionGeneration, Sender: last.Target.Unique, ReplySerial: last.Serial, Completed: true, Success: false}
	n.delay = ""
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	w = e.windows[change.OperationID]
	if w.Generation != generation || w.Attempt != 2 || len(e.windows) != 1 || w.State != StateRolledBack {
		t.Fatal(w.State, w.Attempt, len(e.windows))
	}
}

type leaseBus struct {
	*testNM
	lease string
}

func (b *leaseBus) Observe(ctx context.Context, iface string) (Observation, error) {
	o, err := b.testNM.Observe(ctx, iface)
	o.ManagementFingerprint = b.lease
	o.ManagementSettingsFingerprint = "same-public-settings"
	return o, err
}
func TestRollback_ManagementLeaseChangeDoesNotFreezeMeasuredRecovery(t *testing.T) {
	e, n, clock, change := newTestEngine(t)
	bus := &leaseBus{testNM: n, lease: "old-dynamic-address"}
	e.config.Bus = bus
	if _, _, err := e.Apply(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	bus.lease = "renewed-dynamic-address"
	clock.now += 61 * time.Second
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Status(change.OperationID)
	if s.State != StateRolledBack {
		t.Fatal(s)
	}
}
func TestRollback_DynamicRecoveryRequiresFreshProbeEvidence(t *testing.T) {
	e, _, clock, change := newTestEngine(t)
	if _, _, err := e.Apply(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	e.config.Probe = nil
	clock.now += 61 * time.Second
	_ = e.Tick(t.Context())
	s, _ := e.Status(change.OperationID)
	if s.State == StateRolledBack {
		t.Fatal("dynamic addresses alone finalized recovery")
	}
}
