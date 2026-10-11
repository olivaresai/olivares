// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// controlRequestsSent counts the control requests written to the fake child.
func controlRequestsSent(p *fakeProc) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, line := range p.sent {
		if strings.Contains(string(line), `"control_request"`) {
			n++
		}
	}
	return n
}

// afterFrameMutation runs hook once, at the first store mutation after a control frame
// was written: the moment the refusal records its attempt.
type afterFrameMutation struct {
	api.ModuleData
	p     *fakeProc
	hook  func()
	fired atomic.Bool
}

func (d *afterFrameMutation) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if controlRequestsSent(d.p) > 0 && d.fired.CompareAndSwap(false, true) {
		d.hook()
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// SR2C on 4a0b7e20 (its fixture): the refusal closed call admission after recording its
// attempt, whatever had happened meanwhile. A result read during that write ended the
// refused turn and reopened admission for the input already accepted after it; the
// refusal then closed it again, and the next input was refused as "the previous session
// turn has not finished".
func TestARefusalNeverClosesItsAcceptedSuccessorsAdmission(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "refusal-successor"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	input := []byte(`{"type":"user","message":{"role":"user","content":"an accepted turn"}}`)
	for i := 0; i < 2; i++ {
		if err := m.sendInput(ctx, tenant, run.RunRef, input); err != nil {
			t.Fatal(err)
		}
	}
	lr, _ := m.rt.getLive(tenant, run.RunRef)
	d := &afterFrameMutation{ModuleData: m.Data, p: fr.lastProc(), hook: func() {
		m.onStdout(ctx, lr, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"refusal-successor"}`), time.Now())
	}}
	m.UseData(d)
	turn, _ := lr.beginCredentialRefusal()
	seen := claudeProtocolStub(t, fr.lastProc(), "success")
	m.stopTurnOnRefusedCredential(lr, 401, turn)
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("no frame delivered")
	}
	lr.mu.Lock()
	pending := lr.claudePendingTurns
	lr.mu.Unlock()
	if !d.fired.Load() || pending != 1 {
		t.Fatalf("the refused turn did not end with its successor queued: fired=%v pending=%d", d.fired.Load(), pending)
	}
	nextCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := m.sendInput(nextCtx, tenant, run.RunRef, input); err != nil {
		t.Fatalf("the refusal closed its accepted successor's admission: %v", err)
	}
}

// Once its result is read, the refused turn is over: its calls may still be waiting to
// be cancelled, and Claude Code already runs the next accepted input. A refusal that
// gets the run meanwhile writes no frame, so it never interrupts that next turn.
func TestARefusalDoesNotActOnATurnWhoseResultWasRead(t *testing.T) {
	old := claudeInterruptWait
	claudeInterruptWait = 200 * time.Millisecond
	t.Cleanup(func() { claudeInterruptWait = old })
	ctx := context.Background()
	fr := &fakeRunner{initSID: "refusal-read-result"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	lr, _ := m.rt.getLive(tenant, run.RunRef)
	cancelled := make(chan struct{})
	var once sync.Once
	call := &runtimeSessionCall{cancel: func() { once.Do(func() { close(cancelled) }) }, done: make(chan struct{})}
	lr.mu.Lock()
	lr.sessionCalls = append(lr.sessionCalls, call)
	lr.mu.Unlock()
	turn, _ := lr.beginCredentialRefusal()
	read := make(chan struct{})
	go func() {
		defer close(read)
		m.onStdout(ctx, lr, []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"refusal-read-result"}`), time.Now())
	}()
	<-cancelled // The result is read and its turn's call is being cancelled; its owner has not closed it.
	m.stopTurnOnRefusedCredential(lr, 401, turn)
	close(call.done)
	<-read
	if n := controlRequestsSent(fr.lastProc()); n != 0 {
		t.Fatalf("the refusal wrote %d interrupt frames after its turn's result was read, want 0", n)
	}
}
