// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// selectiveRelayTrace records the relay orderings the private hook reports. Two
// goroutines write to it — the envelope's and the relay's — so it is guarded.
type selectiveRelayTrace struct {
	mu     sync.Mutex
	stages []selectiveRelayStage
}

func (r *selectiveRelayTrace) record(stage selectiveRelayStage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, stage)
}

func (r *selectiveRelayTrace) seen() []selectiveRelayStage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]selectiveRelayStage(nil), r.stages...)
}

func (r *selectiveRelayTrace) equals(want ...selectiveRelayStage) bool {
	got := r.seen()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestSelectiveRelayStopRace drives the one ordering in the envelope that no
// public API can observe: the request is canceled in the instant the envelope is
// stopping the relay that watches it.
//
// context.AfterFunc's stop function returning false does NOT mean the relay
// finished cancelling — only that it had already started. The envelope must join
// the relay before it refuses, or it would perform its cleanup while an
// unfinished relay was still about to cancel the transaction owner, racing the
// rollback whose result it reports.
//
// The hook the test drives is private, nil in production, and reports orderings
// only; it changes no decision.
func TestSelectiveRelayStopRace(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "selective-relay-race")
	raw := st.(*sqlStore)
	selective := st.(store.SelectiveMutator)
	plan, err := store.NewTransactionLockPlan("selective:relay:race")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("stop_loses_to_a_started_relay", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		trace := &selectiveRelayTrace{}
		relayStarted := make(chan struct{})
		releaseRelay := make(chan struct{})
		raw.selectiveRelayTestHook = func(stage selectiveRelayStage) {
			trace.record(stage)
			switch stage {
			case selectiveRelayBeforeStop:
				// Cancel now, and do not proceed until the relay has provably
				// started: the stop below is then guaranteed to lose.
				cancel()
				<-relayStarted
			case selectiveRelayStarted:
				// Hold the relay BEFORE it cancels the owner, so "started" and
				// "finished cancelling" are distinguishable states.
				close(relayStarted)
				<-releaseRelay
			case selectiveRelayJoining:
				// The envelope is about to join. Release the relay from here, so
				// the only thing that can order the two is the join itself.
				close(releaseRelay)
			}
		}
		t.Cleanup(func() { raw.selectiveRelayTestHook = nil })
		ran := false
		err := selective.MutateCoordination(callCtx, tenant, plan,
			func(store.CoordinationMutationScope) error { ran = true; return nil })
		raw.selectiveRelayTestHook = nil
		if ran {
			t.Error("a callback ran after admission lost to a request cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want one wrapping context.Canceled", err)
		}
		if !trace.equals(
			selectiveRelayBeforeStop,
			selectiveRelayStarted,
			selectiveRelayJoining,
			selectiveRelayCanceledOwner,
			selectiveRelayRefusedAfterCancellation,
		) {
			t.Errorf("relay ordering = %v; the refusal did not follow a completed relay cancellation",
				trace.seen())
		}
	})

	t.Run("already_canceled_request_is_joined_not_guessed", func(t *testing.T) {
		// context.AfterFunc runs its function immediately when the context is
		// already done, so stopping always loses here too.
		dead, cancel := context.WithCancel(ctx)
		cancel()
		trace := &selectiveRelayTrace{}
		raw.selectiveRelayTestHook = trace.record
		t.Cleanup(func() { raw.selectiveRelayTestHook = nil })
		ran := false
		err := selective.MutateCoordination(dead, tenant, plan,
			func(store.CoordinationMutationScope) error { ran = true; return nil })
		raw.selectiveRelayTestHook = nil
		if ran {
			t.Error("a callback ran for a request that was already canceled")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want one wrapping context.Canceled", err)
		}
		t.Logf("already-canceled request relay ordering = %v", trace.seen())
	})

	t.Run("stop_wins_and_the_relay_never_runs", func(t *testing.T) {
		// The ordinary path, and the reason the second stop in the envelope's
		// cleanup must not consult the stop function again: after a successful
		// stop the relay will never run, so a repeated join would wait forever on
		// a channel nothing closes. This subtest returning at all is that control.
		trace := &selectiveRelayTrace{}
		raw.selectiveRelayTestHook = trace.record
		t.Cleanup(func() { raw.selectiveRelayTestHook = nil })
		ran := false
		err := selective.MutateCoordination(ctx, tenant, plan,
			func(store.CoordinationMutationScope) error { ran = true; return nil })
		raw.selectiveRelayTestHook = nil
		if err != nil || !ran {
			t.Fatalf("uncanceled selective transaction ran=%t err=%v", ran, err)
		}
		if !trace.equals(selectiveRelayBeforeStop) {
			t.Errorf("relay ordering = %v, want only the stop attempt", trace.seen())
		}
	})
}
