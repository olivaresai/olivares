// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// cutTurnRun launches a stream-json run, opens a turn that the engine stop then cuts,
// and queues one message behind it. It returns the restarted module, after its resume.
func cutTurnRun(t *testing.T, before func(m *Module, st store.Store, tenant model.TenantID, runRef string)) (*Module, *fakeRunner, model.TenantID, string) {
	t.Helper()
	return cutTurnRunWith(t, before, nil, func(d runDTO, _ *fakeRunner) bool { return d.State == stateRunning })
}

// cutTurnRunWith is cutTurnRun with a hook on the restarted module before its start,
// and the state its resume is waited for to reach.
func cutTurnRunWith(t *testing.T, before func(m *Module, st store.Store, tenant model.TenantID, runRef string), onRestart func(restarted *Module, runner *fakeRunner), settled func(runDTO, *fakeRunner) bool) (*Module, *fakeRunner, model.TenantID, string) {
	t.Helper()
	ctx := context.Background()
	runner := &fakeRunner{initSID: "engine-continuation"}
	options := []Option{WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred())}
	m, st, tenant, _ := newRuntimeHarness(t, options...)
	m.QueuedCredentialCapture = sealCredential
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU, queuedCredential: launchCredential(model.NewID())})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation id", func() bool {
		d, _ := m.getRun(ctx, tenant, run.RunRef)
		return d.ClaudeSessionID == "engine-continuation"
	})
	if err := m.sendTextInput(ctx, tenant, run.RunRef, "run the long command"); err != nil {
		t.Fatal(err)
	}
	queueForTest(t, m, tenant, run.RunRef, "queued follow-up", 1)
	if before != nil {
		before(m, st, tenant, run.RunRef)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if turn := lastTurnOf(t, m, tenant, run.RunRef); turn.State != "cut" {
		t.Fatalf("turn open at the engine stop = %+v, want cut", turn)
	}
	restarted := restartModule(t, m, st, tenant, options...)
	if onRestart != nil {
		onRestart(restarted, runner)
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run's resume to settle", func() bool {
		d, err := restarted.getRun(ctx, tenant, run.RunRef)
		return err == nil && settled(d, runner)
	})
	restarted.rt.approvalWG.Wait()
	return restarted, runner, tenant, run.RunRef
}

func sentFrame(p *fakeProc, i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.sent[i])
}

// A turn the engine's stop cut is continued by the engine's own resume: the first
// message the resumed tool gets says the turn was cut and asks it to confirm before
// repeating anything, and the message queued behind it goes when that turn ends.
func TestTheEnginesResumeContinuesTheCutTurnBeforeTheQueue(t *testing.T) {
	m, runner, tenant, ref := cutTurnRun(t, nil)
	proc := runner.lastProc()
	waitFor(t, "a message reached the resumed child", func() bool { return proc.sentCount() >= 1 })
	if first := sentFrame(proc, 0); !strings.Contains(first, "cut") || strings.Contains(first, "queued follow-up") {
		t.Fatalf("first message after the restart = %s, want the continuation", first)
	}
	if turn := lastTurnOf(t, m, tenant, ref); turn.Source != "continuation" || turn.State != "open" {
		t.Fatalf("turn after the restart = %+v, want an open continuation", turn)
	}
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"result","subtype":"success"}`)}
	waitFor(t, "the queued message after the continuation", func() bool { return proc.sentCount() == 2 })
	if second := sentFrame(proc, 1); !strings.Contains(second, "queued follow-up") {
		t.Fatalf("second message = %s, want the queued follow-up", second)
	}
}

// The chain is bounded: with three continuations of this run in the last five minutes,
// the engine sends no fourth and the queue goes as before.
func TestTheEnginesContinuationsStopAfterThreeInFiveMinutes(t *testing.T) {
	_, runner, _, _ := cutTurnRun(t, func(m *Module, _ store.Store, tenant model.TenantID, runRef string) {
		lr, _ := m.rt.getLive(tenant, runRef)
		// The restarted module reads the window on the wall clock (restartModule passes
		// no test clock), so the three continuations are stamped on it.
		if err := m.Data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			for i := 0; i < 3; i++ {
				if _, err := insertRunTurn(context.Background(), sc, lr, "", "continuation", time.Now()); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	proc := runner.lastProc()
	waitFor(t, "a message reached the resumed child", func() bool { return proc.sentCount() >= 1 })
	if first := sentFrame(proc, 0); !strings.Contains(first, "queued follow-up") {
		t.Fatalf("first message after a fourth restart in five minutes = %s, want the queued follow-up", first)
	}
}

// failEngineTurnReads fails the read of the turn rows made under the engine's own
// resume, and no other: the continuation's lookup of the cut turn.
type failEngineTurnReads struct{ api.ModuleData }

func (d failEngineTurnReads) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if _, engine := engineResumeFrom(ctx); !engine {
		return d.ModuleData.View(ctx, tenant, fn)
	}
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error { return fn(failTurnReadScope{sc}) })
}

type failTurnReadScope struct{ store.Scope }

func (s failTurnReadScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind == runTurnKind {
		return nil, errors.New("test: the turn rows cannot be read")
	}
	return s.Scope.Ext(kind)
}

// A resume that cannot tell whether the cut turn is to be continued must not leave the
// run "running" with nothing in progress: it ends stopped, with a reason that says the
// cut turn was not continued and that resuming the session retries, and nothing the
// person queued is sent into a run that is not carrying on.
func TestAFailedContinuationLookupEndsTheRunStoppedNotRunning(t *testing.T) {
	ctx := context.Background()
	failed := func(d runDTO, _ *fakeRunner) bool {
		return d.State == stateStopped && strings.Contains(d.Reason, "could not be continued")
	}
	m, runner, tenant, ref := cutTurnRunWith(t, nil, func(restarted *Module, _ *fakeRunner) { restarted.Data = failEngineTurnReads{restarted.Data} }, failed)
	d, err := m.getRun(ctx, tenant, ref)
	if err != nil || !failed(d, runner) {
		t.Fatalf("run after the failed lookup = %+v, %v, want stopped and saying the cut turn was not continued", d, err)
	}
	if !strings.Contains(d.Reason, "resume") {
		t.Fatalf("reason = %q, want it to say that resuming the session retries", d.Reason)
	}
	if n := runner.lastProc().sentCount(); n != 0 {
		t.Fatalf("messages that reached the resumed child = %d, want none: the run is not carrying on", n)
	}
}

// failEventWrites rejects the lifecycle record of the named events, and nothing else:
// the store that cannot write them, as a store that keeps refusing writes does.
type failEventWrites struct {
	api.ModuleData
	events map[string]bool
}

func (d failEventWrites) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(failEventScope{sc, d.events}) })
}

type failEventScope struct {
	store.Scope
	events map[string]bool
}

func (s failEventScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != runEventKind {
		return repo, err
	}
	return failEventRepo{repo, s.events}, nil
}

type failEventRepo struct {
	store.GenericRepo
	events map[string]bool
}

func (r failEventRepo) Create(ctx context.Context, row model.Record) (model.Record, error) {
	if r.events[row.String(colEvEvent)] {
		return nil, errors.New("test: the ledger cannot record " + row.String(colEvEvent))
	}
	return r.GenericRepo.Create(ctx, row)
}

// resumedChild is the process the restarted engine launched: the second one.
func resumedChild(r *fakeRunner) *fakeProc {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.procs) < 2 {
		return nil
	}
	return r.procs[1]
}

func (p *fakeProc) isDone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done
}

// When the store keeps refusing the lifecycle writes, the ordinary stop cannot record
// that it is stopping, so it never reaches the child: the child must be ended anyway,
// with no message sent into it, and the resume must report what failed.
func TestAPersistentLedgerFailureStillEndsTheResumedChild(t *testing.T) {
	var logs bytes.Buffer
	childEnded := func(_ runDTO, r *fakeRunner) bool { c := resumedChild(r); return c != nil && c.isDone() }
	_, runner, _, ref := cutTurnRunWith(t, nil, func(restarted *Module, _ *fakeRunner) {
		restarted.Data = failEventWrites{restarted.Data, map[string]bool{"continuation": true, "stopping": true}}
		restarted.log = slog.New(slog.NewTextHandler(&logs, nil))
	}, childEnded)
	if n := resumedChild(runner).sentCount(); n != 0 {
		t.Fatalf("messages that reached the resumed child = %d, want none", n)
	}
	for _, want := range []string{ref, "the ledger cannot record continuation", "the ledger cannot record stopping"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("the log must keep %q: %s", want, logs.String())
		}
	}
}

// A continuation that cannot be recorded, with a store that still takes the stop,
// ends the run stopped and says the cut turn was not continued.
func TestAContinuationThatCannotBeRecordedEndsTheRunStopped(t *testing.T) {
	stopped := func(d runDTO, r *fakeRunner) bool {
		c := resumedChild(r)
		return d.State == stateStopped && strings.Contains(d.Reason, "could not be continued") && c != nil && c.isDone()
	}
	_, runner, _, _ := cutTurnRunWith(t, nil, func(restarted *Module, _ *fakeRunner) {
		restarted.Data = failEventWrites{restarted.Data, map[string]bool{"continuation": true}}
	}, stopped)
	if n := resumedChild(runner).sentCount(); n != 0 {
		t.Fatalf("messages that reached the resumed child = %d, want none", n)
	}
}

// A continuation whose send failed after it was attempted may have reached the tool:
// the run ends stopped and the reason says so, so the person checks before resuming.
func TestAnAttemptedContinuationThatFailsSaysItMayHaveReachedTheTool(t *testing.T) {
	stopped := func(d runDTO, r *fakeRunner) bool {
		c := resumedChild(r)
		return d.State == stateStopped && strings.Contains(d.Reason, "may have reached the tool") && c != nil && c.isDone()
	}
	cutTurnRunWith(t, nil, func(_ *Module, runner *fakeRunner) {
		runner.mu.Lock()
		runner.laterSendErr = errors.New("test: stdin closed")
		runner.mu.Unlock()
	}, stopped)
}
