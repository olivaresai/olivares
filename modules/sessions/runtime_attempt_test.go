// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"strings"
	"testing"
	"time"
)

func TestRuntimeAttemptOperationsRefuseReplacementWhileWaitingForRunLock(t *testing.T) {
	for _, operation := range []string{"attach", "interrupt"} {
		t.Run(operation, func(t *testing.T) {
			runner := &fakeRunner{initSID: "exact-attempt-provider"}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
			dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser})
			if err != nil {
				t.Fatal(err)
			}
			identity, ok := dto.Completion.Identity()
			if !ok {
				t.Fatal("missing launch witness")
			}
			release := m.rt.lockRun(liveKey(tenant, dto.RunRef))
			started := make(chan struct{})
			done := make(chan error, 1)
			emitted := 0
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() {
				close(started)
				if operation == "attach" {
					done <- m.AttachRuntimeOutput(ctx, identity, "user:operator", model.ActorUser, "inspect operation", 0, func(RuntimeOutputEvent) error { emitted++; return nil })
				} else {
					result, err := m.InterruptRuntimeAttempt(ctx, RuntimeInputTarget{Tenant: tenant, WorkspaceID: identity.WorkspaceID, RunRef: identity.RunRef, ExpectedLaunch: identity.RuntimeLaunchID, ExpectedSID: identity.SessionSID}, "user:operator", model.ActorUser, "cancel wrong turn")
					if result.Attempted {
						done <- errors.New("attempted old target")
						return
					}
					done <- err
				}
			}()
			<-started
			err = mutateRunForWorkTest(m, tenant, dto.RunRef, func(row model.Record) { row[colRuntimeLaunchID] = model.NewID().String() })
			release()
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("superseded attempt was admitted")
			}
			if emitted != 0 || runner.procs[0].sentCount() != 0 {
				t.Fatal("replacement received output or input")
			}
		})
	}
}

func TestRuntimeAttemptOutputUsesOriginalRing(t *testing.T) {
	runner := &fakeRunner{initSID: "exact-output-provider"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := dto.Completion.Identity()
	live, _ := m.rt.getLive(tenant, dto.RunRef)
	seq := live.ring.append("stdout", []byte("bounded output"), time.Now())
	stop := errors.New("observed one output")
	seen := false
	err = m.AttachRuntimeOutput(context.Background(), identity, "user:operator", model.ActorUser, "inspect output", seq, func(event RuntimeOutputEvent) error {
		if event.Type == "output" {
			seen = true
			if event.Seq != seq || event.Line != "bounded output" {
				t.Fatalf("output %+v", event)
			}
			return stop
		}
		return nil
	})
	if !seen || !errors.Is(err, stop) {
		t.Fatalf("output seen=%v error=%v", seen, err)
	}
}

func TestRuntimeAttemptInterruptKeepsOwnedProcessAndRecordsReason(t *testing.T) {
	m, st, tenant, profile := codexHarness(t, AuthSourceAccountHome, WithClock(&testClock{now: time.Now().UTC()}))
	setCodexFixture(t, profile, codexFixture{ThreadID: "thread-exact-interrupt", Account: "apikey"})
	dto, err := codexLaunch(t, m, tenant, profile)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := dto.Completion.Identity()
	live, _ := m.rt.getLive(tenant, dto.RunRef)
	if err = m.sendTextInput(context.Background(), tenant, dto.RunRef, "long task"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "active provider turn", func() bool { return live.session.ActiveTurn() != "" })
	target := RuntimeInputTarget{Tenant: tenant, WorkspaceID: identity.WorkspaceID, RunRef: identity.RunRef, ExpectedLaunch: identity.RuntimeLaunchID, ExpectedSID: identity.SessionSID}
	result, err := m.InterruptRuntimeAttempt(context.Background(), target, "user:operator", model.ActorUser, "wrong implementation direction")
	if err != nil || result.Outcome != RuntimeInputAccepted || !result.Attempted {
		t.Fatalf("interrupt %+v %v", result, err)
	}
	if !processRunning(live.proc.PID()) {
		t.Fatal("interrupt ended owned process")
	}
	found := false
	for _, event := range listRunEvents(t, st, tenant, dto.RunRef) {
		if event.Event == "interrupted" && strings.Contains(event.Detail, "wrong implementation direction") {
			found = true
		}
	}
	if !found {
		t.Fatal("human reason absent from fenced event audit")
	}
}

func TestRuntimeAttemptOutputDrainsFinalTailAndEnd(t *testing.T) {
	runner := &fakeRunner{initSID: "finished-output-provider"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:operator", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := dto.Completion.Identity()
	live, _ := m.rt.getLive(tenant, dto.RunRef)
	first := live.ring.append("stdout", []byte("first"), time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finalSeen, endSeen := false, false
	err = m.AttachRuntimeOutput(ctx, identity, "user:operator", model.ActorUser, "inspect finished output", first, func(event RuntimeOutputEvent) error {
		if event.Type == "output" && event.Line == "first" {
			live.ring.append("stdout", []byte("final"), time.Now())
			runner.procs[0].finish(0)
			select {
			case <-live.finalizedCh:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if event.Type == "output" && event.Line == "final" {
			finalSeen = true
		}
		if event.Type == "end" {
			endSeen = true
		}
		return nil
	})
	if err != nil || !finalSeen || !endSeen {
		t.Fatalf("final=%v end=%v err=%v", finalSeen, endSeen, err)
	}
}
