// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// mutePostgres is a listener that completes the TCP handshake and then says NOTHING.
//
// It is the whole apparatus this file needs, and it needs no database. pgx dials
// successfully, sends its startup packet and waits for a response that never comes, so
// the observer's probe blocks for its full observerProbeTimeout — twenty times the stop
// bound. That is the state under test: a probe still in flight when the caller has
// already given up waiting for it.
//
// A real PostgreSQL cannot be made to produce this reliably. Anything that would stall a
// live server for two seconds (a lock, a sleep) is a race against the same clock the
// test is measuring, and the failure mode is a flake that reads as a bug.
type mutePostgres struct {
	ln net.Listener
	mu sync.Mutex
	// held keeps every accepted connection open. Closing them would let pgx fail fast
	// with a transport error, which is the opposite of what this fixture is for.
	held []net.Conn
	// accepted and hungUp are OBSERVATION, not assertion, and the distinction was learned
	// the hard way.
	//
	// The intent was to measure the effect of closing the pool rather than the flag this
	// package sets itself. It does not: against this fixture the probe times out and pgx
	// discards the broken connection on its own, so hungUp reaches its target whether or
	// not sql.DB.Close is ever called. Measured — deleting only the Close inside closePool
	// left the assertion green, which is precisely the accidental pass this lane keeps
	// finding. The counters are logged, and the closure itself is pinned against a real
	// server in TestObserverPoolClosureEndsItsBackendSession.
	//
	// accepted IS load-bearing: zero means the run took the degraded path and proved
	// nothing about abandonment.
	accepted int
	hungUp   int
}

func newMutePostgres(t *testing.T) *mutePostgres {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m := &mutePostgres{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			m.mu.Lock()
			m.held = append(m.held, c)
			m.accepted++
			m.mu.Unlock()
			// Drain until the client hangs up. Nothing is ever written back — that is the
			// point of the fixture — but reading is how the peer learns the socket closed.
			go func(c net.Conn) {
				buf := make([]byte, 512)
				for {
					if _, err := c.Read(buf); err != nil {
						m.mu.Lock()
						m.hungUp++
						m.mu.Unlock()
						return
					}
				}
			}(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close() //nolint:errcheck // test teardown
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, c := range m.held {
			_ = c.Close() //nolint:errcheck // test teardown
		}
	})
	return m
}

// counts reports what the SERVER side saw.
func (m *mutePostgres) counts() (accepted, hungUp int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.accepted, m.hungUp
}

// waitForHangups blocks until the peer has seen want disconnections, or the deadline.
func (m *mutePostgres) waitForHangups(want int, within time.Duration) (accepted, hungUp int) {
	deadline := time.Now().Add(within)
	for {
		accepted, hungUp = m.counts()
		if hungUp >= want || time.Now().After(deadline) {
			return accepted, hungUp
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// dsn is a syntactically valid PostgreSQL DSN aimed at the mute listener.
func (m *mutePostgres) dsn() string {
	return fmt.Sprintf("postgres://observer:observer@%s/observed?sslmode=disable",
		m.ln.Addr().String())
}

// poolIsClosed reports the observer's own view of pool ownership, plus the pool's.
func (o *blockObserver) poolIsClosed() (flag bool, open int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	flag = o.poolClosed
	if o.pool != nil {
		open = o.pool.Stats().OpenConnections
	}
	return flag, open
}

// TestObserverAbandonedGoroutineStillClosesItsPool is the other half of the stop bound.
//
// stopAndReport is allowed to walk away from a probe that is still in flight — that is
// the correction that stopped the diagnostic from spending up to two seconds of the very
// deadline it exists to explain. But walking away leaves the pool owned by nobody
// obvious, and the first version of that correction leaked one connection per abandoned
// observation, on a BOOT path: every migration unit that observed a wait and gave up
// waiting for its observer left a connection behind against a server whose connection
// limit is exactly what makes contention worth observing.
//
// So the contract has two halves and this pins the second: whoever finishes LAST closes
// the pool. Here that is the abandoned goroutine.
//
// Mutation that must turn this red: delete `defer o.closePool()` from run(). The
// abandonment path returns before stopAndReport's own close, so nothing else can.
func TestObserverAbandonedGoroutineStillClosesItsPool(t *testing.T) {
	t.Parallel()

	mute := newMutePostgres(t)
	obs := startBlockObserver(context.Background(), mute.dsn(), 12345)
	if obs.pool == nil {
		t.Fatal("the observer did not open a pool; this test needs the non-degraded path")
	}

	// THE PREMISE IS ASSERTED, not assumed: stopAndReport must actually have given up
	// rather than joined a goroutine that finished quickly. Without this the test would
	// pass just as well against a server that answered instantly, proving nothing about
	// abandonment — which is exactly the accidental-pass shape this lane keeps finding.
	started := time.Now()
	_, degraded := obs.stopAndReport()
	waited := time.Since(started)
	if degraded == nil || !strings.Contains(degraded.Error(), "observation_incomplete") {
		t.Fatalf("stopAndReport returned %v after %s; the probe was supposed to still be in flight and the observer abandoned",
			degraded, waited)
	}
	if waited >= observerProbeTimeout {
		t.Fatalf("stopAndReport waited %s, which is the whole probe timeout: it joined the goroutine instead of abandoning it", waited)
	}
	t.Logf("ABANDONED|waited=%s|bound=%s|probe=%s", waited, observerStopTimeout, observerProbeTimeout)

	// The goroutine is still running here, holding the pool. Give it until well past the
	// probe timeout to finish on its own and clean up.
	select {
	case <-obs.done:
	case <-time.After(observerProbeTimeout + 5*time.Second):
		t.Fatal("the abandoned probe goroutine never finished; it is the only thing that can close the pool now")
	}

	closed, open := obs.poolIsClosed()
	if !closed {
		t.Error("the abandoned goroutine finished without closing the observer's pool: one connection leaks per abandoned observation, on a boot path, against the connection limit that made the wait worth observing")
	}
	if open != 0 {
		t.Errorf("the observer's pool still reports %d open connections after the goroutine finished", open)
	}

	// WHAT THIS TEST DOES AND DOES NOT ESTABLISH, stated because the distinction was got
	// wrong once already.
	//
	// It establishes that the abandoned goroutine REACHES closePool — deleting
	// `defer o.closePool()` from run() turns it red. It does NOT establish that the
	// underlying connection is dropped: against this fixture the probe times out and pgx
	// discards the broken connection by itself, so the peer sees a hang-up whether or not
	// sql.DB.Close is ever called. Removing only the Close inside closePool leaves this
	// test green — measured, not assumed.
	//
	// That half is pinned against a real server instead, where a healthy pooled connection
	// exists to be closed: TestObserverPoolClosureEndsItsBackendSession.
	accepted, hungUp := mute.waitForHangups(1, 10*time.Second)
	t.Logf("PEER|accepted=%d|hung_up=%d (not discriminating: the probe timeout drops it either way)", accepted, hungUp)
	if accepted == 0 {
		t.Fatal("the fixture never accepted a connection, so this run exercised the degraded path, not abandonment")
	}
}

// TestObserverPoolCloseIsIdempotentWhenBothClosersConverge is the same contract from the
// other side.
//
// When the goroutine finishes within the bound, BOTH paths reach the close: stopAndReport
// runs o.closePool() and run()'s deferred call does too. What must hold is that the pool
// ends up closed and that converging on it is harmless.
//
// It is deliberately NOT called "exactly once", which is what it was called and did not
// show: removing `&& !o.poolClosed` from closePool — leaving the mutex, the flag and the
// Close — kept it green, because sql.DB.Close is itself idempotent and returns nil. The
// dangerous half was never the double close; it was the version where each closer assumed
// the other would do it and NEITHER did, and that is what the abandonment test pins.
// Asserting a cardinality this cannot observe would be a claim borrowed from the code
// rather than measured.
//
// Run this one under -race: it is the short seam where the two closers actually meet.
func TestObserverPoolCloseIsIdempotentWhenBothClosersConverge(t *testing.T) {
	t.Parallel()

	mute := newMutePostgres(t)
	obs := startBlockObserver(context.Background(), mute.dsn(), 12345)
	if obs.pool == nil {
		t.Fatal("the observer did not open a pool")
	}

	// Wait for the goroutine to finish its in-flight probe FIRST, so stopAndReport takes
	// the JOINED path rather than the abandoned one. The probe recording its degradation
	// is the observable signal that it returned.
	deadline := time.Now().Add(observerProbeTimeout + 5*time.Second)
	probeReturned := false
	for time.Now().Before(deadline) {
		obs.mu.Lock()
		probeReturned = obs.degraded != nil
		obs.mu.Unlock()
		if probeReturned {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// THE PREMISE, ASSERTED. Without this the test would silently degrade into a second
	// copy of the abandonment case and stop covering the path it was written for.
	if !probeReturned {
		t.Fatalf("the first probe never returned within %s, so this test cannot exercise the joined path",
			observerProbeTimeout+5*time.Second)
	}

	// Now stop. The probe loop is between ticks, so it exits promptly and both closers
	// converge.
	if _, err := obs.stopAndReport(); err == nil {
		t.Log("the observer stopped without reporting degradation; the pool contract below is what this test is for either way")
	}
	select {
	case <-obs.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe goroutine did not exit after stop")
	}

	closed, open := obs.poolIsClosed()
	if !closed {
		t.Error("neither closer closed the pool")
	}
	if open != 0 {
		t.Errorf("the pool reports %d open connections after both closers ran", open)
	}
	accepted, hungUp := mute.waitForHangups(1, 10*time.Second)
	t.Logf("PEER|accepted=%d|hung_up=%d (observation only; see the note in the abandonment test)", accepted, hungUp)
	// Idempotent by contract: calling again must neither panic nor double-close.
	obs.closePool()
	obs.closePool()
}

// TestObserverStoppingTwiceIsSafe pins the other re-entrancy the stop path has.
//
// stopAndReport closes o.stop, and a second call must not close an already-closed
// channel — which panics, on a boot path, inside the component whose entire contract is
// that its failure is never the boot's failure.
func TestObserverStoppingTwiceIsSafe(t *testing.T) {
	t.Parallel()

	mute := newMutePostgres(t)
	obs := startBlockObserver(context.Background(), mute.dsn(), 12345)
	_, _ = obs.stopAndReport()
	_, _ = obs.stopAndReport()
}

// TestObserverDegradedPathHasNoPoolToClose covers the branch where the pool was never
// opened: closePool must be a no-op rather than a nil dereference.
//
// That branch is reached at exactly the worst moment — max_connections exhausted, which
// is when there IS contention to observe — so a panic here would turn the diagnostic
// into the outage.
func TestObserverDegradedPathHasNoPoolToClose(t *testing.T) {
	t.Parallel()

	obs := startBlockObserver(context.Background(), "not a dsn at all", 12345)
	if obs.degraded == nil {
		t.Fatal("an unparseable DSN should have produced a degraded observer")
	}
	if obs.pool != nil {
		t.Fatal("a degraded observer should hold no pool")
	}
	obs.closePool()
	hs, err := obs.stopAndReport()
	if len(hs) != 0 {
		t.Errorf("a degraded observer reported %d holders", len(hs))
	}
	if err == nil {
		t.Error("a degraded observer reported no degradation; 'I could not look' must never read as 'nobody was blocking'")
	}
}
