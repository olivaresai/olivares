// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// With a refused API key Claude Code retried ten times before
// the turn failed. The engine interrupts the turn on the first api_retry that says the
// provider refused the credential, records why with what fixes it, and keeps the
// session live; a retry for another cause is left to Claude Code.
func TestClaudeTurnWithARefusedCredentialIsInterruptedAtOnce(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-refused-key"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.ClaudeSessionID != "" })
	p := fr.lastProc()
	retry := func(status int, cause string) OutputFrame {
		b, _ := json.Marshal(map[string]any{"type": "system", "subtype": "api_retry", "attempt": 1, "max_retries": 10,
			"retry_delay_ms": 500, "error_status": status, "error": cause, "session_id": "sess-refused-key"})
		return OutputFrame{Stream: streamStdout, Data: b}
	}
	controlRequests := func() int {
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

	p.out <- retry(529, "server_error")
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the run has no live handle")
	}
	waitFor(t, "the overload retry is bridged", func() bool {
		for _, f := range lr.ring.readFrom(0).frames {
			if strings.Contains(string(f.Data), `"server_error"`) {
				return true
			}
		}
		return false
	})
	if controlRequests() != 0 {
		t.Fatal("a retry for an overloaded provider was interrupted")
	}

	seen := claudeProtocolStub(t, p, "success")
	for i := 0; i < 3; i++ {
		p.out <- retry(401, "authentication_failed")
	}
	select {
	case req := <-seen:
		if r, _ := req["request"].(map[string]any); r["subtype"] != "interrupt" {
			t.Fatalf("the engine sent %v, want Claude Code's interrupt", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn with a refused credential was not interrupted")
	}
	waitFor(t, "the reason on the run", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return strings.Contains(d.Reason, "the provider refused the credential (HTTP 401)")
	})
	d, _ := m.getRun(ctx, tenant, dto.RunRef)
	if d.State != stateRunning || !strings.Contains(d.Reason, "replace the API key in AI tools") {
		t.Fatalf("after the refusal: state %q reason %q, want a live session that says what fixes it", d.State, d.Reason)
	}
	if n := controlRequests(); n != 1 {
		t.Fatalf("%d interrupts for one turn's refused credential, want 1", n)
	}
}

// sendFails is the run's process with a Send that fails the way procrunner reports it.
type sendFails struct {
	Process
	err error
}

func (p sendFails) Send(context.Context, []byte) error { return p.err }

// SR5C on 1285a54c: Process.Send says what happened to the bytes and the caller records
// it. A refusal whose interrupt could not be written records that attempt with the cause
// and what fixes the credential, and claims no interruption.
func TestARefusalWhoseInterruptWriteFailsRecordsWhatHappened(t *testing.T) {
	for _, cause := range []string{
		"sessions: stdin write not attempted: context deadline exceeded",
		"sessions: write stdin: 12 of 80 frame bytes were written before the write ended: write |1: broken pipe",
	} {
		t.Run(cause, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: "refusal-write-fails"}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
			lr, _ := m.rt.getLive(tenant, run.RunRef)
			lr.mu.Lock()
			lr.proc = sendFails{Process: lr.proc, err: errors.New(cause)}
			lr.mu.Unlock()
			turn, _ := lr.beginCredentialRefusal()
			m.stopTurnOnRefusedCredential(lr, 401, turn)
			var recorded []string
			for _, e := range listRunEvents(t, st, tenant, run.RunRef) {
				if e.Actor != credentialRefusedActor {
					continue
				}
				if e.Event != "interrupting" {
					t.Fatalf("a failed write recorded %q: %+v", e.Event, e)
				}
				recorded = append(recorded, e.Detail)
			}
			want := "provider turn interruption write failed: " + clipCause(cause) + "; reason: " + credentialRefusedReason(401)
			if len(recorded) != 1 || recorded[0] != want {
				t.Fatalf("recorded %q, want the one attempt %q", recorded, want)
			}
			d, _ := m.getRun(ctx, tenant, run.RunRef)
			if d.State != stateRunning || !strings.Contains(d.Reason, "write failed") || !strings.Contains(d.Reason, "replace the API key in AI tools") {
				t.Fatalf("after the failed write: state %q reason %q, want a live session that says what happened and what fixes it", d.State, d.Reason)
			}
		})
	}
}

// sendUntilExpired blocks until the write's context expires and answers the way
// procrunner does when its write deadline passes with nothing written.
type sendUntilExpired struct{ Process }

func (sendUntilExpired) Send(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return fmt.Errorf("sessions: write stdin: no bytes were written: %w", ctx.Err())
}

// SR2C on 07a5b505: a native write can spend its whole context, and the outcome written
// under that same context would be refused by the store. The outcome and the remedy
// still commit when the write's context has expired.
func TestARefusalRecordsAFailedWriteEvenWhenItsContextExpired(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "refusal-write-expired"}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, run.RunRef); return d.ClaudeSessionID != "" })
	lr, _ := m.rt.getLive(tenant, run.RunRef)
	lr.mu.Lock()
	lr.proc = sendUntilExpired{lr.proc}
	lr.mu.Unlock()
	rec, err := m.loadRun(ctx, tenant, run.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := lr.beginCredentialRefusal()
	writeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err = m.interruptRefusedTurn(writeCtx, lr, rec, credentialRefusedReason(401), turn)
	if !errors.Is(err, context.DeadlineExceeded) || writeCtx.Err() == nil {
		t.Fatalf("the write did not spend its context: %v", err)
	}
	var recorded []string
	for _, e := range listRunEvents(t, st, tenant, run.RunRef) {
		if e.Actor == credentialRefusedActor {
			recorded = append(recorded, e.Event+": "+e.Detail)
		}
	}
	want := "interrupting: provider turn interruption write failed: sessions: write stdin: no bytes were written: context deadline exceeded; reason: " + credentialRefusedReason(401)
	if len(recorded) != 1 || recorded[0] != want {
		t.Fatalf("recorded %q, want %q", recorded, want)
	}
	if d, _ := m.getRun(ctx, tenant, run.RunRef); !strings.Contains(d.Reason, "replace the API key in AI tools") {
		t.Fatalf("the run's reason %q lost the remedy", d.Reason)
	}
}
