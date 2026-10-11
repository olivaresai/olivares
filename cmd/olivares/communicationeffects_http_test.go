// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

// Track the real fixture drain, including nudges still waiting to claim work.
// Wrapping run before registration avoids racing the goroutine's function read.
var communicationHTTPTestPumps sync.Map // *workOutboxPump -> *communicationHTTPTestPump

type communicationHTTPTestPump struct {
	mu        sync.Mutex
	queued    int
	running   bool
	changed   chan struct{}
	err       error
	beforeRun func(context.Context)
}

func trackCommunicationHTTPTestPump(t *testing.T, p *workOutboxPump) {
	t.Helper()
	tracked := &communicationHTTPTestPump{changed: make(chan struct{})}
	communicationHTTPTestPumps.Store(p, tracked)
	t.Cleanup(func() { communicationHTTPTestPumps.Delete(p) })
	p.nudge = func() {
		tracked.mu.Lock()
		defer tracked.mu.Unlock()
		select {
		case p.nudgeCh <- struct{}{}:
			tracked.queued++
		default: // Preserve the production nudge's coalescing behavior.
		}
	}
	run := p.run
	p.run = func(ctx context.Context) error {
		tracked.mu.Lock()
		tracked.queued--
		tracked.running = true
		before := tracked.beforeRun
		tracked.mu.Unlock()
		if before != nil {
			before(ctx)
		}
		err := run(ctx)
		tracked.mu.Lock()
		tracked.running = false
		tracked.err = err
		close(tracked.changed)
		tracked.changed = make(chan struct{})
		tracked.mu.Unlock()
		return err
	}
}

// Join every queued and active fixture nudge without requesting new work. These
// estates use NoIngest: the periodic scheduler never starts, so an idle or
// joined stopped nudge is concrete proof no asynchronous drain can publish.
// Held/backoff rows and restart-abandoned claims stay available for the tests
// that explicitly exercise recovery. Stable samples cannot establish this.
func communicationHTTPTestSettleOutbox(t *testing.T, eng *engine) {
	t.Helper()
	p := eng.communicationPump
	if p == nil {
		t.Fatal("communication effects census has no fixture pump")
	}
	select {
	case <-p.nudgeExited:
		return // stop joined the only asynchronous drain.
	default:
	}
	value, ok := communicationHTTPTestPumps.Load(p)
	if !ok {
		t.Fatal("communication effects census has an untracked fixture pump")
	}
	tracked := value.(*communicationHTTPTestPump)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	for {
		tracked.mu.Lock()
		queued, running, err, changed := tracked.queued, tracked.running, tracked.err, tracked.changed
		tracked.mu.Unlock()
		if queued == 0 && !running {
			if err != nil {
				t.Fatalf("communication effects census drain failed: %v", err)
			}
			return
		}
		select {
		case <-changed:
		case <-p.nudgeExited:
			return // stop cancels and joins even an in-flight drain.
		case <-ctx.Done():
			t.Fatalf("communication effects census waiting for pump: queued=%d running=%t: %v",
				queued, running, ctx.Err())
		}
	}
}

func TestCommunicationEffectsWaitForPumpCompletion(t *testing.T) {
	for _, phase := range []string{"delayed-nudge", "held-sink"} {
		t.Run(phase, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			resume := sync.OnceFunc(func() { close(release) })
			defer resume() // Release before engine cleanup joins the nudge goroutine.
			var once sync.Once
			hold := func() {
				once.Do(func() { close(entered) })
				<-release
			}
			e := bootIncomingHandoffHTTPEstate(t, communicationHTTPTestSQLiteStore(t), func(p *workOutboxPump) {
				if phase == "held-sink" {
					// Assemble the constructor-owned port before registration starts
					// the first outbox consumer; keep forwarding to the real sink.
					sessions.WithWorkEventSink(stoppingWorkSink{
						inner: p.sessions.WorkEventSink,
						after: func(event sessions.WorkEventEnvelope) {
							if event.Type == "work.handoff.offered" {
								hold()
							}
						},
					})(p.sessions)
				}
			})
			communicationHTTPTestSettleOutbox(t, e.eng)
			if phase == "delayed-nudge" {
				value, ok := communicationHTTPTestPumps.Load(e.eng.communicationPump)
				if !ok {
					t.Fatal("fixture pump is not tracked")
				}
				tracked := value.(*communicationHTTPTestPump)
				tracked.mu.Lock()
				tracked.beforeRun = func(context.Context) { hold() }
				tracked.mu.Unlock()
			}
			// The HTTP offer must finish while the real publication effect is held.
			// A synchronous drain would otherwise hang this test before its oracle.
			offerDone := make(chan struct{})
			checked := make(chan struct{})
			go func() {
				defer close(checked)
				select {
				case <-offerDone:
				case <-time.After(30 * time.Second):
					t.Error("HTTP offer waited for the controlled publication effect")
					resume() // Unblock the request so the test can report the failure.
				}
			}()
			finishOffer := sync.OnceFunc(func() { close(offerDone); <-checked })
			defer finishOffer()
			offer := e.offer(t, map[string]any{"kind": "user", "ref": e.recipient.id}, phase, time.Hour)
			finishOffer()
			if t.Failed() {
				return
			}
			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("real pump did not reach the controlled pause")
			}
			ready := make(chan communicationHTTPTestEffectCounts, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				ready <- communicationHTTPTestEffects(t, e.eng, e.tenant)
			}()
			defer func() { resume(); <-done }()
			// Exceed both old stability windows while the real drain is held.
			wait := 250 * time.Millisecond
			if phase == "held-sink" {
				wait = 2500 * time.Millisecond
			}
			select {
			case <-ready:
				t.Fatal("effects census returned before the live pump completed")
			case <-time.After(wait):
			}
			resume()
			var before communicationHTTPTestEffectCounts
			select {
			case before = <-ready:
			case <-time.After(30 * time.Second):
				t.Fatal("effects census did not return after pump completion")
			}
			states := communicationHTTPTestOutboxStates(t, e.eng, e.tenant)
			if states["pending"] != 0 || states["delivering"] != 0 || states["published"] == 0 {
				t.Fatalf("effects baseline captured unfinished publication: %v", states)
			}
			if got := e.detail(t, e.recipient.token, offer.DeliveryID); got.status != 200 {
				t.Fatalf("read the settled handoff = %d", got.status)
			}
			assertCommunicationHTTPTestNoEffects(t, e.eng, e.tenant, before, "read after delayed publication")
		})
	}
}

// A baseline must not consume work the journey intends to recover explicitly.
// Withdraw the nudge through the existing fixture seam, leaving real committed
// K3 rows pending, then prove only the explicitly invoked pump publishes them.
func TestCommunicationEffectsPreservePendingRecovery(t *testing.T) {
	e := bootIncomingHandoffHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	sessions.SetWorkOutboxNudge(nil)
	communicationHTTPTestSettleOutbox(t, e.eng)
	e.offer(t, map[string]any{"kind": "user", "ref": e.recipient.id}, "pending recovery", time.Hour)
	before := communicationHTTPTestOutboxStates(t, e.eng, e.tenant)
	if before["pending"] == 0 {
		t.Fatalf("recovery fixture has no committed pending work: %v", before)
	}
	communicationHTTPTestEffects(t, e.eng, e.tenant)
	if after := communicationHTTPTestOutboxStates(t, e.eng, e.tenant); !maps.Equal(before, after) {
		t.Fatalf("effects census consumed pending recovery work: before=%v after=%v", before, after)
	}
	if err := e.eng.communicationPump.runOnce(t.Context()); err != nil {
		t.Fatalf("explicit recovery drain: %v", err)
	}
	after := communicationHTTPTestOutboxStates(t, e.eng, e.tenant)
	if after["pending"] != 0 || after["delivering"] != 0 || after["published"] <= before["published"] {
		t.Fatalf("explicit pump did not publish the recovery work: before=%v after=%v", before, after)
	}
	e.eng.communicationPump.stop()
	communicationHTTPTestEffects(t, e.eng, e.tenant) // A joined stop is also a safe census boundary.
}
