// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"context"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

// listener_test.go drives the CoT bearers (runListeners and its seams) over real
// loopback sockets. It also defines the shared listener test harness — cbRecorder,
// the free-port probes, the send pumps and the wait helpers — reused by
// source_test.go. Everything is channel-synchronized: the only fixed waits are the
// short retry cadences on a socket that has not finished binding, never a sleep
// used to "hope" an event arrived.

// --- shared harness ---------------------------------------------------------

// cbRecorder captures the two listener callbacks. onEvent blocks on a buffered
// channel (which is the backpressure seam) until a test reads it or ctx is done;
// onReject never blocks (a non-blocking send onto a generously buffered channel,
// exactly as the production countReject never blocks).
type cbRecorder struct {
	events  chan acceptedEvent
	rejects chan rejectKey
}

func newRecorder() *cbRecorder {
	return &cbRecorder{
		events:  make(chan acceptedEvent, 32),
		rejects: make(chan rejectKey, 256),
	}
}

func (r *cbRecorder) callbacks() listenerCallbacks {
	return listenerCallbacks{
		onEvent: func(ctx context.Context, ev Event, transport string) error {
			select {
			case r.events <- acceptedEvent{ev: ev, transport: transport}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		onReject: func(reason, transport string) {
			select {
			case r.rejects <- rejectKey{reason: reason, transport: transport}:
			default: // contract: onReject must never block
			}
		},
	}
}

// listenerRun is a runListeners instance under test.
type listenerRun struct {
	rec    *cbRecorder
	cancel context.CancelFunc
	done   chan error
	t      *testing.T
}

func startListeners(t *testing.T, cfg config) *listenerRun {
	t.Helper()
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runListeners(ctx, cfg, rec.callbacks()) }()
	return &listenerRun{rec: rec, cancel: cancel, done: done, t: t}
}

func (r *listenerRun) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		r.t.Error("runListeners did not return within 3s of cancel")
	}
}

// listenerCfg builds a config with the fields the bearers read, defaulted to sane
// values, then applies mut. It bypasses loadConfig so a test can inject an
// arbitrary listen address (including :0-derived free ports) and custom limits.
func listenerCfg(mut func(*config)) config {
	c := config{
		feedRef:      "tak",
		rateLimitEPS: 1000,
		maxTCPConns:  128,
		uidMode:      uidModeHash,
		limits:       Limits{}.withDefaults(),
	}
	if mut != nil {
		mut(&c)
	}
	return c
}

// freeUDPAddr and freeTCPAddr find a loopback address currently free by binding it,
// reading the OS-assigned port, and releasing it for the caller to re-bind. There is
// an inherent TOCTOU window; on a loopback test host it is not observed to race.
func freeUDPAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free UDP port: %v", err)
	}
	addr := c.LocalAddr().String()
	_ = c.Close()
	return addr
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free TCP port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// pumpUDP dials addr and writes payload immediately and then every 10ms until the
// returned stop func is called. Repetition covers the bind race: the first few
// datagrams may hit a not-yet-bound socket and be dropped by the kernel.
func pumpUDP(t *testing.T, addr, payload string) (stop func()) {
	t.Helper()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial UDP %s: %v", addr, err)
	}
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			_, _ = conn.Write([]byte(payload))
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	return func() {
		close(done)
		_ = conn.Close()
	}
}

// dialTCPRetry connects to addr, retrying past the "connection refused" window
// before the listener has finished binding.
func dialTCPRetry(t *testing.T, addr string, d time.Duration) net.Conn {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial TCP %s: %v", addr, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitEvent(t *testing.T, ch <-chan acceptedEvent, d time.Duration) acceptedEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(d):
		t.Fatalf("no event within %v", d)
		return acceptedEvent{}
	}
}

// waitReject blocks until a reject matching reason+transport arrives, ignoring any
// other reject classes, or fails after d.
func waitReject(t *testing.T, ch <-chan rejectKey, reason, transport string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case rk := <-ch:
			if rk.reason == reason && rk.transport == transport {
				return
			}
			// Not the class we are asserting; keep draining.
		case <-deadline:
			t.Fatalf("no %s/%s reject within %v", reason, transport, d)
		}
	}
}

// waitUntil polls cond until it is true or d elapses. It is a synchronization
// primitive (poll a state that another goroutine advances), not a fixed sleep.
func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// --- tests ------------------------------------------------------------------

func TestUDPListenerAcceptsValidCoT(t *testing.T) {
	addr := freeUDPAddr(t)
	run := startListeners(t, listenerCfg(func(c *config) { c.udpListen = addr }))
	defer run.stop()

	stop := pumpUDP(t, addr, validRaw())
	defer stop()

	got := waitEvent(t, run.rec.events, 3*time.Second)
	if got.transport != transportUDP {
		t.Errorf("transport = %q, want %q", got.transport, transportUDP)
	}
	if got.ev.UID != "ANDROID-1" {
		t.Errorf("ev.UID = %q, want ANDROID-1 (the fixture uid)", got.ev.UID)
	}
}

func TestTCPListenerOpenSquirtClose(t *testing.T) {
	addr := freeTCPAddr(t)
	run := startListeners(t, listenerCfg(func(c *config) { c.tcpListen = addr }))
	defer run.stop()

	conn := dialTCPRetry(t, addr, 3*time.Second)
	if _, err := conn.Write([]byte(validRaw())); err != nil {
		t.Fatalf("write CoT: %v", err)
	}
	// Open-squirt-CLOSE: the sender closes after one message; that is the EOF the
	// server's ReadAll waits for. Buffered data is flushed before the FIN.
	_ = conn.Close()

	got := waitEvent(t, run.rec.events, 3*time.Second)
	if got.transport != transportTCP {
		t.Errorf("transport = %q, want %q", got.transport, transportTCP)
	}
	// Exactly one message per connection: no second event should follow.
	select {
	case dup := <-run.rec.events:
		t.Fatalf("second event from one open-squirt-close connection: %+v", dup)
	case <-time.After(75 * time.Millisecond):
	}
}

func TestOversizeIsRejected(t *testing.T) {
	// A 64-byte event cap makes the ~230-byte canonical event oversize on both
	// bearers. UDP truncates the datagram into buf and detects n > cap; TCP's
	// LimitReader stops one byte past the cap and detects len > cap.
	small := func(c *config) {
		c.limits = Limits{MaxEventBytes: 64, MaxDetailBytes: 32}.withDefaults()
	}

	t.Run("udp", func(t *testing.T) {
		addr := freeUDPAddr(t)
		run := startListeners(t, listenerCfg(func(c *config) { c.udpListen = addr; small(c) }))
		defer run.stop()

		stop := pumpUDP(t, addr, validRaw())
		defer stop()

		waitReject(t, run.rec.rejects, reasonOversize, transportUDP, 3*time.Second)
		select {
		case ev := <-run.rec.events:
			t.Fatalf("oversize datagram must not yield an event: %+v", ev)
		default:
		}
	})

	t.Run("tcp", func(t *testing.T) {
		addr := freeTCPAddr(t)
		run := startListeners(t, listenerCfg(func(c *config) { c.tcpListen = addr; small(c) }))
		defer run.stop()

		conn := dialTCPRetry(t, addr, 3*time.Second)
		_, _ = conn.Write([]byte(validRaw()))
		_ = conn.Close()

		waitReject(t, run.rec.rejects, reasonOversize, transportTCP, 3*time.Second)
	})
}

func TestMalformedIsRejected(t *testing.T) {
	const garbage = "this-is-not-a-cot-event"

	t.Run("udp", func(t *testing.T) {
		addr := freeUDPAddr(t)
		run := startListeners(t, listenerCfg(func(c *config) { c.udpListen = addr }))
		defer run.stop()

		stop := pumpUDP(t, addr, garbage)
		defer stop()

		waitReject(t, run.rec.rejects, reasonMalformed, transportUDP, 3*time.Second)
	})

	t.Run("tcp", func(t *testing.T) {
		addr := freeTCPAddr(t)
		run := startListeners(t, listenerCfg(func(c *config) { c.tcpListen = addr }))
		defer run.stop()

		conn := dialTCPRetry(t, addr, 3*time.Second)
		_, _ = conn.Write([]byte(garbage))
		_ = conn.Close()

		waitReject(t, run.rec.rejects, reasonMalformed, transportTCP, 3*time.Second)
	})
}

// TestRateLimiterRejects drives the token-bucket clock by hand and calls dispatch
// directly, so nothing depends on wall time. The decisive assertion is that a
// MALFORMED payload comes back classified as rate_limited, not malformed: the bucket
// is consulted before ParseEvent, so an empty bucket short-circuits the parser.
func TestRateLimiterRejects(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }
	bucket := newTokenBucket(1, clock) // capacity 1, starts full

	if !bucket.allow() {
		t.Fatal("first allow() must succeed on a full bucket")
	}
	if bucket.allow() {
		t.Fatal("second allow() must fail: clock frozen, no refill")
	}

	cfg := listenerCfg(nil)
	var (
		mu      sync.Mutex
		rejects []rejectKey
	)
	cb := listenerCallbacks{
		onEvent: func(context.Context, Event, string) error {
			t.Error("onEvent must not fire while the bucket is empty")
			return nil
		},
		onReject: func(reason, transport string) {
			mu.Lock()
			rejects = append(rejects, rejectKey{reason: reason, transport: transport})
			mu.Unlock()
		},
	}

	if stop := dispatch(context.Background(), cfg, bucket, cb, []byte("not-cot"), transportUDP); stop {
		t.Error("dispatch must not signal stop for a rate-limited reject")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(rejects) != 1 {
		t.Fatalf("rejects = %d, want 1", len(rejects))
	}
	if rejects[0].reason != reasonRateLimited {
		t.Fatalf("reason = %q, want %q — ParseEvent, not the limiter, rejected the event",
			rejects[0].reason, reasonRateLimited)
	}
	if rejects[0].transport != transportUDP {
		t.Errorf("transport = %q, want %q", rejects[0].transport, transportUDP)
	}

	// Advancing the clock a full second refills one token, re-admitting an event.
	now = now.Add(time.Second)
	if !bucket.allow() {
		t.Error("allow() must succeed after a 1s refill at 1 eps")
	}
}

// TestTCPConnLimit pins one connection in its handler (it never squirts, so the
// handler blocks in ReadAll holding the only slot) and asserts a second connection
// is refused with conn_limit and closed at once.
func TestTCPConnLimit(t *testing.T) {
	addr := freeTCPAddr(t)
	run := startListeners(t, listenerCfg(func(c *config) { c.tcpListen = addr; c.maxTCPConns = 1 }))
	defer run.stop()

	// Conn #1 takes the single slot and holds it (no write => handler parks in
	// ReadAll until readTimeout, well past this test).
	hold := dialTCPRetry(t, addr, 3*time.Second)
	defer hold.Close()

	// Conn #2 arrives after #1 (FIFO accept backlog) and must be refused.
	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial #2: %v", err)
	}
	defer second.Close()

	waitReject(t, run.rec.rejects, reasonConnLimit, transportTCP, 3*time.Second)

	// The server closes a refused connection immediately: a read returns EOF fast.
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := second.Read(buf); err != io.EOF {
		t.Fatalf("conn #2 read = %v, want io.EOF (a refused conn must be closed promptly)", err)
	}
}

// TestRunListenersReturnsOnContextCancel asserts a clean cancel returns nil promptly
// and leaves no goroutines behind. Both bearers are brought fully up first, so this
// exercises teardown of RUNNING listeners, not a pre-bind early return.
func TestRunListenersReturnsOnContextCancel(t *testing.T) {
	udp := freeUDPAddr(t)
	tcp := freeTCPAddr(t)
	cfg := listenerCfg(func(c *config) { c.udpListen = udp; c.tcpListen = tcp })
	rec := newRecorder()

	base := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runListeners(ctx, cfg, rec.callbacks()) }()

	// Confirm both bearers are live before canceling.
	stop := pumpUDP(t, udp, validRaw())
	_ = waitEvent(t, rec.events, 3*time.Second)
	stop()
	c := dialTCPRetry(t, tcp, 3*time.Second)
	_ = c.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runListeners returned %v, want nil on clean cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runListeners did not return within 3s of cancel")
	}

	// Goroutines must settle back to the baseline (small slack for transient runtime
	// and the just-stopped pump goroutine). This is a settle loop, not a fixed sleep.
	settled := waitUntil(2*time.Second, func() bool {
		return runtime.NumGoroutine() <= base+2
	})
	if !settled {
		t.Fatalf("goroutine leak: NumGoroutine=%d, baseline=%d", runtime.NumGoroutine(), base)
	}
}

// TestBindFailureIsHardError points a listener at an address that cannot bind and
// asserts runListeners returns a non-nil error at once (deny-closed: a configured
// listener that did not come up must not look like running ingest).
func TestBindFailureIsHardError(t *testing.T) {
	cfg := listenerCfg(func(c *config) { c.udpListen = "256.0.0.1:1" }) // 256 is not a valid octet

	done := make(chan error, 1)
	go func() { done <- runListeners(context.Background(), cfg, newRecorder().callbacks()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("runListeners returned nil, want a bind error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runListeners did not return; a bind failure must be immediate")
	}
}
