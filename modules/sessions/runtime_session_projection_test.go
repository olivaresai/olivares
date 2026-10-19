// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntime_InterruptedClaudeLateEventStaysIdle(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-late-interrupt"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conversation capture", func() bool {
		d, err := m.getRun(ctx, tenant, dto.RunRef)
		return err == nil && d.ClaudeSessionID != ""
	})
	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
		t.Fatal(err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("no live process")
	}
	claudeProtocolStub(t, fr.lastProc(), "success")
	if _, err := m.interruptRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	// The old result and tool output are still evidence, but do not reopen work.
	for _, frame := range []string{resultFrame, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"late-tool","name":"Read","input":{}}]}}`} {
		m.onStdout(ctx, lr, []byte(frame), m.now())
		d, err := m.getRun(ctx, tenant, dto.RunRef)
		if err != nil || d.State != stateIdle || d.ProcessState != stateRunning {
			t.Fatalf("late Claude event: state/process_state=%s/%s err=%v, want idle/running", d.State, d.ProcessState, err)
		}
	}
	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
		t.Fatal(err)
	}
	d, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil || d.State != stateRunning {
		t.Fatalf("next accepted Claude turn: state=%s err=%v, want running", d.State, err)
	}
}

func TestRuntime_InterruptedClaudeRefusedInputKeepsIdle(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-refused-input"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
		t.Fatal(err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("no live process")
	}
	claudeProtocolStub(t, fr.lastProc(), "success")
	if _, err := m.interruptRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	m.onStdout(ctx, lr, []byte(resultFrame), m.now())
	// Late output opens the old turn again; only accepted input may reopen work.
	m.onStdout(ctx, lr, []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"late-tool","name":"Read","input":{}}]}}`), m.now())
	original := lr.proc
	lr.proc = sendFails{Process: original, err: errors.New("stdin closed")}
	err = m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn))
	lr.proc = original
	if err == nil {
		t.Fatal("input to a broken stdin was accepted")
	}
	if d, err := m.getRun(ctx, tenant, dto.RunRef); err != nil || d.State != stateIdle {
		t.Fatalf("refused input after interruption: state=%s err=%v, want idle", d.State, err)
	}
}

func TestRuntime_UnconfirmedClaudeInterruptDoesNotProjectIdle(t *testing.T) {
	old := claudeInterruptWait
	claudeInterruptWait = 200 * time.Millisecond
	defer func() { claudeInterruptWait = old }()
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-unanswered-interrupt"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
		t.Fatal(err)
	}
	claudeProtocolStub(t, fr.lastProc(), "")
	if _, err := m.interruptRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err == nil {
		t.Fatal("an unanswered interrupt reported success")
	}
	if d, err := m.getRun(ctx, tenant, dto.RunRef); err != nil || d.State != stateRunning {
		t.Fatalf("unanswered interrupt: state=%s err=%v, want running", d.State, err)
	}
}

func TestRuntime_UnconfirmedInterruptDoesNotProjectIdle(t *testing.T) {
	fx := newFakeTurnFixture(t)
	fx.startTurn(t)
	fx.session.programInterrupt(true, errors.New("unconfirmed"), nil)
	ctx := context.Background()
	if err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence); err == nil {
		t.Fatal("expected an unconfirmed interrupt")
	}
	d, err := fx.m.getRun(ctx, fx.tenant, fx.runRef)
	if err != nil || d.State != stateRunning {
		t.Fatalf("unconfirmed interrupt: state=%s err=%v, want running", d.State, err)
	}
}

func TestRuntime_InterruptedDriverLateEventStaysIdle(t *testing.T) {
	fx := newFakeTurnFixture(t)
	fx.startTurn(t)
	ctx := context.Background()
	if err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence); err != nil {
		t.Fatal(err)
	}
	// Replay a late tool notification through the same parser as the output pump.
	fx.clock.set(baseTime.Add(time.Minute))
	fx.m.onStdout(ctx, fx.live, []byte(`{"method":"item/completed","params":{"type":"tool"}}`), fx.m.now())
	dto, err := fx.m.getRun(ctx, fx.tenant, fx.runRef)
	if err != nil {
		t.Fatal(err)
	}
	if dto.State != stateIdle || dto.ProcessState != stateRunning {
		t.Fatalf("late tool event after interrupt: state/process_state = %s/%s, want idle/running", dto.State, dto.ProcessState)
	}
	// The driver can refuse before moving any byte. That must keep the confirmed
	// interruption's display, rather than making a nonexistent turn look active.
	if attempted, err := fx.m.driverInput(ctx, fx.live, ""); attempted || err == nil {
		t.Fatalf("empty driver input: attempted=%v err=%v", attempted, err)
	}
	dto, err = fx.m.getRun(ctx, fx.tenant, fx.runRef)
	if err != nil || dto.State != stateIdle {
		t.Fatalf("refused input after interruption: state=%s err=%v, want idle", dto.State, err)
	}
	fx.startTurn(t)
	dto, err = fx.m.getRun(ctx, fx.tenant, fx.runRef)
	if err != nil || dto.State != stateRunning {
		t.Fatalf("next accepted turn: state=%s err=%v, want running", dto.State, err)
	}
}

func TestRuntime_InterruptedClaudeAcceptedQueuedTurnIsWorking(t *testing.T) {
	for _, order := range []string{"ack before result", "result before ack", "result before send"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: "sess-queued-interrupt"}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
			})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
					t.Fatal(err)
				}
			}
			lr, ok := m.rt.getLive(tenant, dto.RunRef)
			if !ok {
				t.Fatal("no live process")
			}
			if order == "result before send" {
				done := make(chan struct{})
				lr.mu.Lock()
				lr.sessionCalls = append(lr.sessionCalls, &runtimeSessionCall{
					done: done,
					cancel: func() {
						// The old call closes while cancellation waits, then its
						// result admits the already accepted successor.
						lr.mu.Lock()
						lr.sessionCalls = nil
						lr.mu.Unlock()
						m.onStdout(ctx, lr, []byte(resultFrame), m.now())
						close(done)
					},
				})
				lr.mu.Unlock()
			}
			claudeProtocolStub(t, fr.lastProc(), "success", func() {
				if order == "result before ack" {
					m.onStdout(ctx, lr, []byte(resultFrame), m.now())
				}
			})
			if _, err := m.interruptRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
				t.Fatal(err)
			}
			if order == "ack before result" {
				m.onStdout(ctx, lr, []byte(resultFrame), m.now())
			}
			d, err := m.getRun(ctx, tenant, dto.RunRef)
			want := stateRunning
			if order == "result before send" {
				want = stateIdle // the actual frame interrupted the successor
			}
			if err != nil || d.State != want {
				t.Fatalf("previously accepted queued turn: state=%s err=%v, want %s", d.State, err, want)
			}
		})
	}
}

// A tool that goes silent while its process and turn stay alive (the fixture's
// active turn reports nothing after it starts). The engine serves running for the
// five-minute idle window after the last output and idle after it. That is the
// existing quiet projection, and it is the answer for a silent tool: idle says
// "no output for five minutes", not "the tool stopped"; the turn stays active and
// nothing is stopped or marked failed.
func TestRuntime_SilentActiveTurnDefaultFiveMinuteProjection(t *testing.T) {
	fx := newFakeTurnFixture(t)
	fx.startTurn(t)
	ctx := context.Background()
	for _, test := range []struct {
		age  time.Duration
		want string
	}{
		{5 * time.Minute, stateRunning},
		{5*time.Minute + time.Second, stateIdle},
	} {
		fx.clock.set(baseTime.Add(test.age))
		dto, err := fx.m.getRun(ctx, fx.tenant, fx.runRef)
		if err != nil {
			t.Fatal(err)
		}
		if dto.State != test.want || dto.ProcessState != stateRunning || fx.session.ActiveTurn() == "" || fx.proc.stopCount() != 0 {
			t.Fatalf("silent live tool at %s: state/process_state=%s/%s active=%q stops=%d, want %s/running and a live turn", test.age, dto.State, dto.ProcessState, fx.session.ActiveTurn(), fx.proc.stopCount(), test.want)
		}
	}
}
