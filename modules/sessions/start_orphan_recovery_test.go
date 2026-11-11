// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A crash leaves the run's row running with no process behind it. Nothing read
// that row as stopped until somebody asked to stop it, so after a restart a dead
// session showed as running. Start now reads it as stopped and says why.
func TestStartStopsTheRunsACrashedEngineLeftRunning(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "crashed"}), WithCredentialSource(staticCred()))
	m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }
	crashed := startedRun(t, m, tenant)
	live := startedRun(t, m, tenant)
	stopped := startedRun(t, m, tenant)
	if _, err := m.stopRun(ctx, tenant, stopped, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	before, err := m.getRun(ctx, tenant, stopped)
	if err != nil {
		t.Fatal(err)
	}
	// A launch that had reserved its row and not yet started its process, and a run
	// that had already failed: the second is over, whatever the engine did next.
	pending := startedRun(t, m, tenant)
	mutateRunForCredentialTest(t, m.Data, tenant, pending, func(r model.Record) { r[colState] = statePending })
	failed := startedRun(t, m, tenant)
	mutateRunForCredentialTest(t, m.Data, tenant, failed, func(r model.Record) { r[colState] = stateFailed })
	// The crash: the row says running, the engine that held its process is gone.
	m.rt.dropLive(tenant, crashed)
	m.rt.dropLive(tenant, pending)
	m.rt.dropLive(tenant, failed)

	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if d, err := m.getRun(ctx, tenant, pending); err != nil || d.State != stateStopped || !strings.HasPrefix(d.Reason, engineDiedReason) {
		t.Errorf("a pending run: state %q reason %q (%v), want stopped saying the engine died", d.State, d.Reason, err)
	}
	if d, err := m.getRun(ctx, tenant, failed); err != nil || d.State != stateFailed {
		t.Errorf("a failed run: state %q (%v), want it left failed", d.State, err)
	}
	got, err := m.getRun(ctx, tenant, crashed)
	if err != nil || got.State != stateStopped {
		t.Fatalf("crashed run: state %q (%v), want %q", got.State, err, stateStopped)
	}
	if !strings.HasPrefix(got.Reason, "the engine died; resume the session to continue") {
		t.Errorf("crashed run reason = %q, want it to say the engine died", got.Reason)
	}
	if !strings.Contains(got.Reason, "not confirmed terminated") {
		t.Errorf("crashed run reason = %q, want it to keep saying the process is not confirmed terminated", got.Reason)
	}
	if d, err := m.getRun(ctx, tenant, live); err != nil || d.State != stateRunning {
		t.Errorf("a run this engine holds: state %q (%v), want it left running", d.State, err)
	}
	if d, err := m.getRun(ctx, tenant, stopped); err != nil || d.State != stateStopped || d.Reason != before.Reason {
		t.Errorf("a run a person stopped: state %q reason %q (%v), want it as it was: %q", d.State, d.Reason, err, before.Reason)
	}
}

// Start reads a row as the crashed engine's only where it is the serving engine
// and no other runtime may hold the row. A command that is not serving has no tenant
// list, and the runtime that owns a row says another engine may hold its process
// (communication credentials): there only leader recovery decides.
func TestStartLeavesRunsAloneWhereAnotherEngineMayHoldThem(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Module, model.TenantID)
	}{
		{"communication credentials on", func(m *Module, _ model.TenantID) { m.rt.CommunicationCredentialsEnabled = true }},
		{"no tenant list, as in an offline command", func(m *Module, _ model.TenantID) { m.ApprovalRecoveryTenants = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "held-elsewhere"}), WithCredentialSource(staticCred()))
			m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }
			run := startedRun(t, m, tenant)
			m.rt.dropLive(tenant, run)
			tc.setup(m, tenant)

			if err := m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != stateRunning {
				t.Fatalf("state %q (%v), want running: Start must not stop a row another engine may hold", d.State, err)
			}
		})
	}
}

// fixedElector answers the one question Start asks of the store's leader fence.
type fixedElector struct {
	store.LeaderElector
	active bool
}

func (e fixedElector) Active() bool { return e.active }

// A standby is not the writer: it revokes a row's credentials before it writes the
// row, so on a standby Start would take them from a session the leader still runs.
func TestStartLeavesRunsToTheLeaderOnAStandby(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
		want   string
	}{
		{"leader", true, stateStopped},
		{"standby", false, stateRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "ha"}), WithCredentialSource(staticCred()))
			m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }
			m.rt.ManagedStopAuthority = &managedStopPorts{elector: fixedElector{active: tc.active}}
			run := startedRun(t, m, tenant)
			m.rt.dropLive(tenant, run)

			if err := m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != tc.want {
				t.Fatalf("state %q (%v), want %q", d.State, err, tc.want)
			}
		})
	}
}

// Start never fails over the runs a crashed engine left: a tenant list it cannot
// read leaves them as they are for the next start.
func TestStartCompletesWhenTheTenantsOfCrashedRunsCannotBeListed(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "unlisted"}), WithCredentialSource(staticCred()))
	m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) {
		return nil, fmt.Errorf("%w: no admin pool", store.ErrEnumerationNotAuthoritative)
	}
	run := startedRun(t, m, tenant)
	m.rt.dropLive(tenant, run)

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start with an unreadable tenant list = %v, want nil", err)
	}
	if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != stateRunning {
		t.Fatalf("state %q (%v), want running: nothing could say the engine died", d.State, err)
	}
}

// A tenant with more crashed runs than one listing reads has them all stopped.
func TestStopOrphanedTenantRunsReadsEveryPage(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "paged"}), WithCredentialSource(staticCred()))
	var runs []string
	for range 5 {
		run := startedRun(t, m, tenant)
		m.rt.dropLive(tenant, run)
		runs = append(runs, run)
	}

	if err := m.stopOrphanedTenantRuns(ctx, tenant, 2); err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if d, err := m.getRun(ctx, tenant, run); err != nil || d.State != stateStopped {
			t.Errorf("run %s: state %q (%v), want stopped: a page was skipped", run, d.State, err)
		}
	}
}

// startedRun launches one run on the fake runner and returns its ref. Its process
// is stopped when the test ends, as in TestRuntime_OrphanReconciledOnStop.
func startedRun(t *testing.T, m *Module, tenant model.TenantID) string {
	t.Helper()
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("created run has no live handle")
	}
	t.Cleanup(func() {
		_ = lr.proc.Stop(context.Background())
		select {
		case <-lr.finalizedCh:
		case <-time.After(3 * time.Second):
			t.Error("the run's process did not finalize")
		}
	})
	return dto.RunRef
}

// An accepted input must outlive the engine that held its provider process.
func TestRunTurnSurvivesEngineCrash(t *testing.T) {
	ctx := context.Background()
	h, fr, admin, tenant := attachHTTPHarness(t)
	fr.initSID = "durable-turn"
	m := h.m
	m.ApprovalRecoveryTenants = func(context.Context) ([]model.TenantID, error) { return []model.TenantID{tenant}, nil }
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json", "name": "durable-turn"})
	base := "/v1/m/sessions/runs/" + ref
	say := func(text string) {
		t.Helper()
		if r := h.doJSON("POST", base+"/input", admin, map[string]any{"text": text}, tenantHdr(tenant)); r.code != http.StatusAccepted {
			t.Fatalf("input %q = %d %s", text, r.code, r.raw)
		}
	}
	ordinal := float64(1)
	check := func(want string) {
		t.Helper()
		r := h.do("GET", base, admin, tenantHdr(tenant))
		turn, _ := r.body["last_turn"].(map[string]any)
		if turn == nil {
			t.Fatalf("accepted input has no durable last_turn: %s", r.raw)
		}
		if turn["state"] != want || turn["ordinal"] != ordinal || turn["started_at"] == "" {
			t.Fatalf("last_turn = %v, want %s ordinal %v with start", turn, want, ordinal)
		}
		if want == "cut" && turn["ended_at"] == nil {
			t.Fatal("cut turn has no end timestamp")
		}
	}
	say("run a long task")
	check("open")
	lr, _ := m.rt.getLive(tenant, ref)
	m.onStdout(ctx, lr, []byte(`{"type":"result","subtype":"success"}`), m.now())
	check("completed")
	ordinal++
	say("run another long task")
	check("open")
	m.rt.dropLive(tenant, ref)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	check("cut")
}

// failTurnData fails the write of a session turn row while fail is set, and nothing else.
type failTurnData struct {
	api.ModuleData
	fail  atomic.Bool
	failN atomic.Int32 // the next N turn-row writes fail, then the store recovers
}

func (d *failTurnData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(failTurnScope{sc, d}) })
}

type failTurnScope struct {
	store.Scope
	d *failTurnData
}

func (s failTurnScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind == runTurnKind && (s.d.fail.Load() || s.d.failN.Add(-1) >= 0) {
		return nil, errors.New("test: the turn row cannot be written")
	}
	return s.Scope.Ext(kind)
}

// A result that arrived must not be recorded as the process's failure because its own
// write failed once: the outcome is written again when the process ends.
func TestRunTurnCompletedSurvivesAFailedCloseWrite(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "failed-close"}), WithCredentialSource(staticCred()))
	ref := startedRun(t, m, tenant)
	if err := m.sendTextInput(ctx, tenant, ref, "do the work"); err != nil {
		t.Fatal(err)
	}
	data := &failTurnData{ModuleData: m.Data}
	m.Data = data
	lr, _ := m.rt.getLive(tenant, ref)
	data.fail.Store(true)
	m.onStdout(ctx, lr, []byte(`{"type":"result","subtype":"success"}`), m.now())
	data.fail.Store(false)
	m.finalize(lr, 1, nil)
	var turn *runTurnDTO
	if err := m.Data.View(ctx, tenant, func(sc store.Scope) (err error) {
		turn, err = lastRunTurn(ctx, sc, ref)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if turn == nil || turn.State != "completed" {
		t.Fatalf("last turn after a failed close write and a process exit = %+v, want completed", turn)
	}
}

func lastTurnOf(t *testing.T, m *Module, tenant model.TenantID, ref string) *runTurnDTO {
	t.Helper()
	var turn *runTurnDTO
	if err := m.Data.View(context.Background(), tenant, func(sc store.Scope) (err error) {
		turn, err = lastRunTurn(context.Background(), sc, ref)
		return err
	}); err != nil || turn == nil {
		t.Fatalf("last turn of %s: %+v, %v", ref, turn, err)
	}
	return turn
}

// completingSession ends its turn when a frame arrives, as the Codex driver does on turn/completed.
type completingSession struct{ *fakeTurnSession }

func (s completingSession) Deliver(OutputFrame) {
	s.mu.Lock()
	s.turn = ""
	s.mu.Unlock()
}

// refusingInput writes a frame that the provider then refuses: attempted, no turn started.
type refusingInput struct {
	completingSession
	refuse bool
}

func (s *refusingInput) Input(ctx context.Context, text string) (bool, error) {
	if s.refuse {
		return true, errors.New("test: the provider refused the turn")
	}
	return s.completingSession.Input(ctx, text)
}

// driverRun is a started run whose input goes through the driver path.
func driverRun(t *testing.T, session DriverSession) (m *Module, tenant model.TenantID, ref string, lr *liveRun) {
	t.Helper()
	m, _, tenant, _ = newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	ref = startedRun(t, m, tenant)
	lr, _ = m.rt.getLive(tenant, ref)
	lr.session, lr.driver = session, fakeTurnDriver{}
	return m, tenant, ref, lr
}

const codexTurnCompleted = `{"method":"turn/completed"}`

// Codex ends its turn inside Interrupt, so the completion frame pops nothing: the
// interrupted row must leave the queue then, or each later result closes its predecessor.
func TestRunTurnQueueStaysAlignedAfterACodexInterrupt(t *testing.T) {
	ctx := context.Background()
	m, tenant, ref, lr := driverRun(t, completingSession{newFakeTurnSession()})
	input := func(text string) {
		t.Helper()
		if _, err := m.driverInputAdmitted(ctx, lr, text, lr.launchID, lr.claim.SID); err != nil {
			t.Fatal(err)
		}
	}
	input("first")
	if _, err := m.interruptDriverTurn(ctx, lr); err != nil {
		t.Fatal(err)
	}
	m.onStdout(ctx, lr, []byte(codexTurnCompleted), m.now())
	input("second")
	m.onStdout(ctx, lr, []byte(codexTurnCompleted), m.now())
	if turn := lastTurnOf(t, m, tenant, ref); turn.Ordinal != 2 || turn.State != "completed" {
		t.Fatalf("last turn = %+v, want ordinal 2 completed", turn)
	}
}

// An input the provider refused after the write started no turn and gets no result.
func TestRunTurnQueueStaysAlignedAfterARefusedAttempt(t *testing.T) {
	ctx := context.Background()
	session := &refusingInput{completingSession: completingSession{newFakeTurnSession()}, refuse: true}
	m, tenant, ref, lr := driverRun(t, session)
	if _, err := m.driverInputAdmitted(ctx, lr, "refused", lr.launchID, lr.claim.SID); err == nil {
		t.Fatal("the refused input reported no error")
	}
	if turn := lastTurnOf(t, m, tenant, ref); turn.State != "failed" {
		t.Fatalf("refused turn = %+v, want failed", turn)
	}
	session.refuse = false
	if _, err := m.driverInputAdmitted(ctx, lr, "accepted", lr.launchID, lr.claim.SID); err != nil {
		t.Fatal(err)
	}
	m.onStdout(ctx, lr, []byte(codexTurnCompleted), m.now())
	if turn := lastTurnOf(t, m, tenant, ref); turn.Ordinal != 2 || turn.State != "completed" {
		t.Fatalf("last turn = %+v, want ordinal 2 completed", turn)
	}
}

type failSend struct{ Process }

func (failSend) Send(context.Context, []byte) error { return errors.New("test: stdin closed") }

// A Claude input whose write failed may still be running, so its row stays open and the
// turn gate refuses a successor: no second row can shift the queue before the process end.
func TestRunTurnFailedClaudeWriteBlocksSuccessorsUntilTheProcessEnds(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "failed-write"}), WithCredentialSource(staticCred()))
	ref := startedRun(t, m, tenant)
	lr, _ := m.rt.getLive(tenant, ref)
	lr.mu.Lock()
	lr.proc = failSend{lr.proc}
	lr.mu.Unlock()
	if err := m.sendTextInput(ctx, tenant, ref, "lost"); err == nil {
		t.Fatal("the failed write reported no error")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := m.sendTextInput(waitCtx, tenant, ref, "next"); err == nil || !strings.Contains(err.Error(), "previous session turn has not finished") {
		t.Fatalf("a successor behind an open turn: %v, want the turn gate's refusal", err)
	}
	if turn := lastTurnOf(t, m, tenant, ref); turn.Ordinal != 1 || turn.State != "open" {
		t.Fatalf("after the refused successor: %+v, want the first turn still open", turn)
	}
	m.finalize(lr, 1, nil)
	if turn := lastTurnOf(t, m, tenant, ref); turn.State != "failed" {
		t.Fatalf("after the process end: %+v, want failed", turn)
	}
}

// A broken turn table must not leave an orphaned run unrecoverable.
func TestOrphanReconciliationDoesNotNeedTheTurnTable(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "orphan-turns"}), WithCredentialSource(staticCred()))
	ref := startedRun(t, m, tenant)
	if err := m.sendTextInput(ctx, tenant, ref, "work"); err != nil {
		t.Fatal(err)
	}
	m.rt.dropLive(tenant, ref)
	data := &failTurnData{ModuleData: m.Data}
	data.fail.Store(true)
	m.Data = data
	if err := m.stopOrphanedTenantRuns(ctx, tenant, 2); err != nil {
		t.Fatal(err)
	}
	if d, err := m.getRun(ctx, tenant, ref); err != nil || d.State != stateStopped {
		t.Fatalf("orphaned run: state %q (%v), want stopped", d.State, err)
	}
}

// The store fails the outcome's write and then the first process-end write: the outcome
// that did arrive is kept and written on the retry, and no row stays open.
func TestRunTurnProcessEndRetriesAFailedClose(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{initSID: "end-retry"}), WithCredentialSource(staticCred()))
	ref := startedRun(t, m, tenant)
	if err := m.sendTextInput(ctx, tenant, ref, "work"); err != nil {
		t.Fatal(err)
	}
	data := &failTurnData{ModuleData: m.Data}
	m.Data = data
	lr, _ := m.rt.getLive(tenant, ref)
	data.fail.Store(true)
	m.onStdout(ctx, lr, []byte(`{"type":"result","subtype":"success"}`), m.now())
	data.fail.Store(false)
	data.failN.Store(1)
	m.finalize(lr, 1, nil)
	if turn := lastTurnOf(t, m, tenant, ref); turn.State != "completed" {
		t.Fatalf("last turn after a failed write and a failed first close = %+v, want completed", turn)
	}
}

// Claude Code reports a provider error as a success subtype with is_error set.
func TestRunTurnProviderErrorResultIsFailed(t *testing.T) {
	ctx := context.Background()
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	ref := startedRun(t, m, tenant)
	lr, _ := m.rt.getLive(tenant, ref)
	if _, err := m.openRunTurn(ctx, lr, "", "stream-json"); err != nil {
		t.Fatal(err)
	}
	m.onStdout(ctx, lr, []byte(usageLimitResultFrame), m.now())
	if turn := lastTurnOf(t, m, tenant, ref); turn.State != "failed" {
		t.Fatalf("turn that ended in a provider error = %+v, want failed", turn)
	}
}

// A slow call owner fails the call wait, not the interrupt: the driver ended the turn
// either way, so its row must leave the queue.
func TestRunTurnQueueStaysAlignedWhenACallOwnerIsSlowToClose(t *testing.T) {
	ctx := context.Background()
	m, tenant, ref, lr := driverRun(t, completingSession{newFakeTurnSession()})
	input := func(text string) {
		t.Helper()
		if _, err := m.driverInputAdmitted(ctx, lr, text, lr.launchID, lr.claim.SID); err != nil {
			t.Fatal(err)
		}
	}
	input("first")
	lr.mu.Lock()
	lr.sessionCalls = append(lr.sessionCalls, &runtimeSessionCall{cancel: func() {}, done: make(chan struct{})})
	lr.mu.Unlock()
	if _, err := m.interruptDriverTurn(ctx, lr); err == nil {
		t.Fatal("the call that never closed reported no error")
	}
	lr.mu.Lock()
	lr.sessionCalls, lr.sessionCallsEnded = nil, false
	lr.mu.Unlock()
	m.onStdout(ctx, lr, []byte(codexTurnCompleted), m.now())
	input("second")
	m.onStdout(ctx, lr, []byte(codexTurnCompleted), m.now())
	if turn := lastTurnOf(t, m, tenant, ref); turn.Ordinal != 2 || turn.State != "completed" {
		t.Fatalf("last turn = %+v, want ordinal 2 completed", turn)
	}
}

// startFinishedInput takes an input whose turn is already over when the start response
// is read, so the driver never records an active turn for it.
type startFinishedInput struct {
	completingSession
	finished bool
}

func (s *startFinishedInput) Input(ctx context.Context, text string) (bool, error) {
	if s.finished {
		return true, nil
	}
	return s.completingSession.Input(ctx, text)
}

func TestRunTurnQueueStaysAlignedAfterATurnThatFinishedInItsStartResponse(t *testing.T) {
	ctx := context.Background()
	session := &startFinishedInput{completingSession: completingSession{newFakeTurnSession()}, finished: true}
	m, tenant, ref, lr := driverRun(t, session)
	if _, err := m.driverInputAdmitted(ctx, lr, "instant", lr.launchID, lr.claim.SID); err != nil {
		t.Fatal(err)
	}
	if turn := lastTurnOf(t, m, tenant, ref); turn.State != "completed" {
		t.Fatalf("turn finished in its start response = %+v, want completed", turn)
	}
	session.finished = false
	if _, err := m.driverInputAdmitted(ctx, lr, "next", lr.launchID, lr.claim.SID); err != nil {
		t.Fatal(err)
	}
	m.onStdout(ctx, lr, []byte(codexTurnCompleted), m.now())
	if turn := lastTurnOf(t, m, tenant, ref); turn.Ordinal != 2 || turn.State != "completed" {
		t.Fatalf("last turn = %+v, want ordinal 2 completed", turn)
	}
}

// forgettingSession drops the head row while its frame is delivered, as an interrupt
// on another goroutine does between the pump's read of the queue and its pop.
type forgettingSession struct {
	completingSession
	lr *liveRun
}

func (s forgettingSession) Deliver(f OutputFrame) {
	s.lr.forgetDurableTurn(s.lr.durableTurn(false))
	s.completingSession.Deliver(f)
}

func TestRunTurnPumpPopsItsOwnRowNotTheNewHead(t *testing.T) {
	ctx := context.Background()
	fake := completingSession{newFakeTurnSession()}
	m, _, _, lr := driverRun(t, fake)
	lr.session = forgettingSession{completingSession: fake, lr: lr}
	var second model.ID
	for i := 0; i < 2; i++ {
		id, err := m.openRunTurn(ctx, lr, "", "codex")
		if err != nil {
			t.Fatal(err)
		}
		second = id
	}
	fake.mu.Lock()
	fake.turn = "turn-1"
	fake.mu.Unlock()
	m.onStdout(ctx, lr, []byte(codexTurnCompleted), m.now())
	if got := lr.durableTurn(false); got != second {
		t.Fatalf("queue head after the pump settled the first row = %q, want the second row %q", got, second)
	}
}
