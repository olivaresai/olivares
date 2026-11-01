// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// A nudged drain that does not complete leaves its rows pending, and a held row
// gets no second insert to wake it. With the periodic drain disabled
// (OLIVARES_WORK_OUTBOX_INTERVAL=0) nothing else would ever wake the pump, so
// the nudge goroutine retries any drain that did not complete with the same 1 s
// doubling backoff, up to the safety cadence.

// A nudged drain that returns an error did not complete: the rows it could not
// reach stay pending, and the retry schedule must drain again rather than wait
// for the safety tick.
func TestANudgedDrainThatFailsIsRetried(t *testing.T) {
	p := &workOutboxPump{st: fakeStoreLeader(true), log: discardLog()}
	var drains atomic.Int64
	p.run = func(context.Context) error {
		drains.Add(1)
		return errors.New("tenant drain failed")
	}
	if err := p.register(&spyScheduler{}); err != nil {
		t.Fatal(err)
	}
	defer p.stop()

	p.nudgeCh <- struct{}{}
	eventually(t, "the failed nudged drain's retry", func() bool { return drains.Load() >= 2 })
}

// A nudged drain that cannot run because this node does not lead has run
// nothing: the rows stay pending, so the retry schedule must try again without
// another insert.
func TestANudgedDrainWhileNotLeadingIsRetried(t *testing.T) {
	p := newWorkOutboxPump(func(string) string { return "" }, fakeStoreLeader(false), &sessions.Module{}, discardLog())
	var drains atomic.Int64
	p.run = func(ctx context.Context) error {
		drains.Add(1)
		return p.runOnce(ctx)
	}
	if err := p.register(&spyScheduler{}); err != nil {
		t.Fatal(err)
	}
	defer p.stop()

	p.nudgeCh <- struct{}{}
	eventually(t, "the retry of a nudged drain that could not run", func() bool { return drains.Load() >= 2 })
}

// A pump with no authority holds K3 at its own caller restriction. That hold
// leaves the same pending rows an authority refusal does, so it must ask for
// the same retry.
func TestAPumpWithoutAuthorityAsksForARetryAfterHoldingK3Work(t *testing.T) {
	e := bootMessagingEstate(t, "sqlite")
	p := e.eng.communicationPump
	communicationHTTPTestSettleOutbox(t, e.eng)
	// Withdraw the insert nudge so the row below stays pending and this test
	// drives the drain itself. The heldBack check then proves no retry is armed,
	// so the pump goroutine is idle and cannot read p.authority while this test
	// replaces it.
	e.eng.sessionsMod.SetWorkOutboxNudge(nil)
	if p.heldBack.Load() {
		t.Fatal("the settled pump still has a retry armed; its authority cannot be replaced safely")
	}
	if !p.st.Leader().Active() {
		t.Fatal("the estate is not leading; this test needs the caller restriction, not the leadership hold")
	}
	before := communicationHTTPTestOutboxStates(t, e.eng, e.tT)
	e.sendNotice(e.comm.control.id)
	held := communicationHTTPTestOutboxStates(t, e.eng, e.tT)
	if held["pending"] <= before["pending"] {
		t.Fatalf("the notice left no pending row: before %v, now %v", before, held)
	}

	// The composition binds an authority on every production boot; a pump
	// without one holds K3 itself, and that hold needs the retry.
	p.authority = nil
	p.heldBack.Store(false)
	if err := p.runOnce(t.Context()); err != nil {
		t.Fatalf("the drain of a pump without an authority: %v", err)
	}
	if !p.heldBack.Load() {
		t.Fatal("a pump without an authority that held a K3 row must ask for a retry")
	}
	if states := communicationHTTPTestOutboxStates(t, e.eng, e.tT); states["pending"] != held["pending"] {
		t.Fatalf("the held K3 row changed state: after the notice %v, after the drain %v", held, states)
	}
}
