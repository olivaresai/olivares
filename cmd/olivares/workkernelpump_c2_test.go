// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"
)

// --- C2 item 5: the work-outbox nudge lifecycle (SR2C P2) ---------------------

// The nudge goroutine has immutable channel custody, an owned cancellable
// context, and stop JOINS it: a nudge drains promptly, stop cancels the drain's
// context and returns only after the goroutine exited, and no drain can run
// afterwards (against a closing store, for instance).
func TestWorkOutboxNudgeDrainsCancelsAndJoins(t *testing.T) {
	p := &workOutboxPump{st: fakeStoreLeader(true), log: discardLog()}
	calls := make(chan context.Context, 4)
	p.run = func(ctx context.Context) error { calls <- ctx; return nil }
	if err := p.register(&spyScheduler{}); err != nil {
		t.Fatal(err)
	}

	// A nudge drains promptly, with the pump's owned context.
	p.nudgeCh <- struct{}{}
	var drainCtx context.Context
	select {
	case drainCtx = <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("a nudge did not drain")
	}

	// stop cancels that context and joins the goroutine before returning.
	p.stop()
	if err := drainCtx.Err(); err == nil {
		t.Fatal("stop must cancel the drain's owned context")
	}
	select {
	case <-p.nudgeExited: // already closed: stop joined the goroutine
	default:
		t.Fatal("stop returned before the nudge goroutine exited")
	}

	// Nothing drains after stop, and a double stop is silent.
	p.nudgeCh <- struct{}{}
	select {
	case <-calls:
		t.Fatal("a drain ran after stop")
	case <-time.After(150 * time.Millisecond):
	}
	p.stop()
}
