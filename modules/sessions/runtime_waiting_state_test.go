// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const claudeUserTurn = `{"type":"user","message":{"role":"user","content":"hi"}}`

// TestRuntime_SessionWaitingForInputIsIdle is the issue's acceptance path: start, answer,
// wait. The idle window is left at its five-minute default and the clock does not move, so
// the only thing that can make the run idle is that its turn ended.
func TestRuntime_SessionWaitingForInputIsIdle(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-wait"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	stateOf := func() string {
		d, err := m.getRun(ctx, tenant, dto.RunRef)
		if err != nil {
			t.Fatalf("getRun: %v", err)
		}
		if d.ProcessState != stateRunning {
			t.Fatalf("process_state = %s, want running", d.ProcessState)
		}
		return d.State
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-wait"
	})
	if got := stateOf(); got != stateRunning {
		t.Fatalf("a session that has taken no turn is %s, want running", got)
	}

	for round := 1; round <= 2; round++ {
		if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
			t.Fatalf("round %d: sendInput: %v", round, err)
		}
		if got := stateOf(); got != stateRunning {
			t.Fatalf("round %d: a session working on a turn is %s, want running", round, got)
		}
		fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(resultFrame)}
		waitFor(t, "the answered session to read idle", func() bool {
			d, _ := m.getRun(ctx, tenant, dto.RunRef)
			return d.State == stateIdle
		})
		// The API field a client reads: derived `state` idle, stored `process_state` running.
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		wire, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal run: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(wire, &fields); err != nil {
			t.Fatalf("unmarshal run: %v", err)
		}
		if fields["state"] != stateIdle || fields["process_state"] != stateRunning {
			t.Fatalf("round %d: wire state=%v process_state=%v, want idle/running", round, fields["state"], fields["process_state"])
		}
	}
}

// TestRuntime_ATurnClaudeStartsByItselfIsWorking: an answered session reads idle, and
// output with no input behind it (a background task finishing) is a turn again, until its
// result. Reading idle there would hide Interrupt on a session that is working.
func TestRuntime_ATurnClaudeStartsByItselfIsWorking(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-self"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	stateIs := func(want string) func() bool {
		return func() bool {
			d, _ := m.getRun(ctx, tenant, dto.RunRef)
			return d.State == want
		}
	}
	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
		t.Fatalf("sendInput: %v", err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(resultFrame)}
	waitFor(t, "the answered session to read idle", stateIs(stateIdle))

	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"assistant","message":{"content":[]}}`)}
	waitFor(t, "output with no input behind it to read running", stateIs(stateRunning))

	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(resultFrame)}
	waitFor(t, "its result to read idle again", stateIs(stateIdle))
}

// TestRuntime_ADriverRunReadsRunningFromTheInputToTheAnswer: the provider names a turn only
// when it answers the input, and ActiveTurn is empty until then. The run must not read idle
// in that gap just because its previous turn ended.
func TestRuntime_ADriverRunReadsRunningFromTheInputToTheAnswer(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) }) // never leave the peer waiting
	m, _, tenant, prof := codexHarness(t, AuthSourceAccountHome)
	record := setCodexFixture(t, prof, codexFixture{
		ThreadID: "thread-gap", Account: "apikey", CompleteTurn: true, HoldSecondTurnPath: release,
	})
	ctx := context.Background()
	dto, err := codexLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	stateIs := func(want string) func() bool {
		return func() bool {
			d, _ := m.getRun(ctx, tenant, dto.RunRef)
			return d.State == want
		}
	}
	if err := m.sendTextInput(ctx, tenant, dto.RunRef, "first"); err != nil {
		t.Fatalf("first input: %v", err)
	}
	waitFor(t, "the answered run to read idle", stateIs(stateIdle))

	second := make(chan error, 1)
	go func() { second <- m.sendTextInput(ctx, tenant, dto.RunRef, "second") }()
	waitFor(t, "the peer to receive the second turn/start", func() bool {
		starts := 0
		for _, method := range readFixtureRecord(t, record).Methods {
			if method == codexMethodTurnStart {
				starts++
			}
		}
		return starts == 2
	})
	// The provider holds its answer: no turn has a name, and the run is working.
	if d, _ := m.getRun(ctx, tenant, dto.RunRef); d.State != stateRunning {
		t.Fatalf("a run whose input the provider has not answered reads %s, want running", d.State)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release the peer: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second input: %v", err)
	}
	waitFor(t, "the answered run to read idle again", stateIs(stateIdle))
}

// TestGrokRuntimeHTTPAnsweredRunReadsIdleOnTheRouteAndTheList is the issue's acceptance path
// over the real sessions routes with a real child process: start, answer, wait. A client
// reading one run or the list sees running while the turn runs and idle once it is answered,
// with the process still running (process_state).
func TestGrokRuntimeHTTPAnsweredRunReadsIdleOnTheRouteAndTheList(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	fx := newGrokHTTPFixture(t, "grok-idle", AuthSourceAccountHome)
	setGrokFixture(t, fx.prof, grokFixture{
		SessionID: "conv-idle", PromptMode: "hold", PromptReleasePath: release,
	})
	runRef := fx.createRun(t)
	lr, ok := fx.m.rt.getLive(fx.tenant, runRef)
	if !ok {
		t.Fatal("the run is not live")
	}
	// The two GETs are separate requests, so they agree only while the state holds
	// still: compare them at the held points, and wait for both after the release.
	read := func() (state, listed, process string) {
		one := fx.h.doJSON(http.MethodGet, "/v1/m/sessions/runs/"+runRef, fx.admin, nil, tenantHdr(fx.tenant))
		list := fx.h.doJSON(http.MethodGet, "/v1/m/sessions/runs", fx.admin, nil, tenantHdr(fx.tenant))
		if one.code != http.StatusOK || list.code != http.StatusOK {
			t.Fatalf("read run = %d %s, list = %d", one.code, one.raw, list.code)
		}
		items, _ := list.body["items"].([]any)
		for _, item := range items {
			if row, _ := item.(map[string]any); row["run_ref"] == runRef {
				listed, _ = row["state"].(string)
			}
		}
		state, _ = one.body["state"].(string)
		process, _ = one.body["process_state"].(string)
		return state, listed, process
	}

	if state, listed, process := read(); state != stateRunning || listed != stateRunning || process != stateRunning {
		t.Fatalf("a session that has taken no turn reads %s (list %s)/%s, want running/running", state, listed, process)
	}
	if accepted := fx.input("answer me", runRef); accepted.code != http.StatusAccepted {
		t.Fatalf("input = %d %s", accepted.code, accepted.raw)
	}
	waitFor(t, "the turn is active", func() bool { return lr.session.ActiveTurn() != "" })
	if state, listed, _ := read(); state != stateRunning || listed != stateRunning {
		t.Fatalf("a session answering reads %s (list %s), want running", state, listed)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release the prompt: %v", err)
	}
	waitFor(t, "the answered run to read idle on the route and the list", func() bool {
		state, listed, process := read()
		return state == stateIdle && listed == stateIdle && process == stateRunning
	})
}

// TestRuntime_IdleWindowStillIdlesASilentSession keeps the old rule beside the new one: a
// session that never finished a turn and has been silent past the window is still idle.
func TestRuntime_IdleWindowStillIdlesASilentSession(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-silent"}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithRuntimeIdleWindow(time.Minute))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if err := m.sendInput(ctx, tenant, dto.RunRef, []byte(claudeUserTurn)); err != nil {
		t.Fatalf("sendInput: %v", err)
	}
	clk.set(baseTime.Add(2 * time.Minute))
	if d, _ := m.getRun(ctx, tenant, dto.RunRef); d.State != stateIdle {
		t.Fatalf("a turn silent past the idle window reads %s, want idle", d.State)
	}
}
