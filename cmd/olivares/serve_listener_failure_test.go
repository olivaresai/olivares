// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// DS1 permanent regression: a fatal listener error must be drained, not escaped.
//
// waitAndShutdown used to return the wrapped listener cause immediately, before any
// Shutdown/GracefulStop call, leaving every transport the process had started still
// admitting work (reproduced by DTS1 as behavior B1). The oracles below hold the
// repaired contract: the fatal cause enters the existing drain sequence, the sequence
// completes, and the original wrapped cause is returned afterwards with errors.Is
// still true for its root.
//
// Every server, listener, client, connection, handler and goroutine here is owned by
// the test and handed to the real, unmodified waitAndShutdown as its arguments; no
// product logic is copied or re-implemented. No t.Parallel anywhere: waitAndShutdown
// installs process signal handlers, so its calls are deliberately serialized.
//
// # Three separate facts, never conflated
//
// This file counts three different things, and each is named for what it is:
//
//   - HANDLER INVOCATION: a full call of the fixture's http.Handler. Every fixture
//     server wraps its handler in listenerFailureHandlerProbe, which counts entry and
//     the deferred return of every invocation, admitted or refused alike.
//   - HELD-BODY ADMISSION: whether an invocation was allowed into the held body of
//     listenerFailureHeldWork. Closing that admission does NOT prevent an invocation —
//     it only steers the invocation to its refusal branch — so a held-body count is
//     never reported as a handler count and never used as a handler join.
//   - CONNECTION CUSTODY: the terminal fate of every connection a fixture server
//     accepted, taken from that server's own ConnState callbacks. This is the fact that
//     actually joins handler invocations.
//
// # Connection custody, and why it is the join
//
// For these plain HTTP/1 loopback fixtures the pinned go1.26.6 net/http gives an exact
// ordering, verified before being relied on:
//
//   - server.go:3461 sets StateNew from inside the accept loop, before the connection's
//     serve goroutine starts. Once that loop has returned — which the fixture proves by
//     joining its own Serve call — no further connection can ever be registered. That
//     join, not a timer, is what seals the tracker, so there is no Add/Wait race.
//   - server.go:1893-1911 sets StateClosed from that serve goroutine's own deferred
//     call, after the handler returned, and only when the connection was not hijacked.
//   - server.go setState runs the ConnState hook synchronously.
//   - Server.Close closes the raw connection and drops its bookkeeping WITHOUT waiting
//     for the serve goroutine, so a returned Close proves nothing about the handler.
//     An entry is therefore never erased because Close returned.
//
// StateHijacked is not supported by this fixture: net/http never reports StateClosed for
// a hijacked connection, so such an entry is recorded as unresolved ownership and is
// never silently treated as terminal. Each server owns its tracker before Serve starts.
// This is fixture custody. It is not a general product HTTP shutdown guarantee.
//
// # Readiness
//
// One consistent mechanism proves a fixture server has actually started serving, for
// HTTP and for gRPC alike: the fixture wraps its own listener in an accept barrier and
// waits for the first Accept call, which only the server's own Serve loop makes. For
// gRPC this is the fact that matters and a TCP dial is not: in grpc v1.82.1 Serve
// registers the listener in s.lis and only then enters lis.Accept(), so an Accept entry
// rules out the schedule in which Serve starts after a stop and returns
// ErrServerStopped. A raw dial can sit in the kernel accept queue and prove nothing
// about the server goroutine. HTTP fixtures additionally answer one real request, which
// is the "was admitting" premise of the closure oracle. All four product trigger sites
// go through startCall, which refuses to trigger while any owned gRPC fixture has not
// passed its real barrier.
//
// # Custody
//
// Every test builds a listenerFailureHarness FIRST, before any fixture exists and
// before any observation is made. The harness registers exactly one t.Cleanup, so the
// teardown order is explicit rather than an artifact of t.Cleanup's LIFO stack; it is
// idempotent, so a control can drive the real thing; and it runs in full even when an
// assertion calls t.Fatal in the middle of the test:
//
//	1. release held work and close held-body admission;
//	2. remove the request producers — owned raw connections, then each client request
//	   and transport, joined on its own completion fact;
//	3. close each owned HTTP server and join its Serve loop; ONLY if that explicit join
//	   completed, read and require its closed-server sentinel and seal its connection
//	   tracker — an expired bound leaves registration unsealed and the result unread;
//	4. stop each owned gRPC server and join its Serve loop under the same rule;
//	5. await the terminal ConnState fact of every connection those servers accepted —
//	   the join that covers every handler invocation, admitted or refused — and retain
//	   each server's outcome, not just its counts;
//	6. reconcile held-body admission, reported as the separate fact it is;
//	7. require each server's handler-invocation balance, claimed as FINAL only where
//	   that server's custody actually completed, and reported as observed otherwise;
//	8. cancel the fixture context and join every waitAndShutdown goroutine;
//	9. print whatever the product logged.
//
// The order also matters for the product: a waitAndShutdown parked inside
// http.Server.Shutdown is released by steps 1 and 3, not by cancelling a context,
// because the product derives its own shutdown context from context.Background(). The
// fixture context exists only for step 8 and is never cancelled while a test is running,
// so the injected fatal listener error stays the only ready arm of the product's select.
// Cleanup only ever reports with t.Errorf, and every bounded wait that expires is
// reported as UNRESOLVED — never as completion. Completion provenance is kept: a
// fixture publishes its Serve result only after an actual receive from its completion
// channel, every reader goes through observedServeResult, and an expired bound never
// fabricates a join, a sealed registration or a safe-to-read result. A first failure
// does not stop the remaining, independently eligible cleanup.
//
// # Closure evidence
//
// "The request failed" is not proof that a listener closed. Each closure claim is three
// facts about one owned listener generation: the fixture's own Serve call returned the
// exact closed-server sentinel of the pinned implementation; a fresh dial to the same
// address reports syscall.ECONNREFUSED specifically (a timeout, reset or any other error
// is recorded as an incomplete observation, not as closure); and closing the very
// net.Listener the readiness probe was served from reports net.ErrClosed. The dial
// always precedes that close, so the fixture cannot manufacture the refusal.
//
// # Controls
//
// The three TestServeListenerFailureControl tests exercise paths a passing regression
// run never reaches. Control A drives the real full teardown while its owned request is
// still incomplete and no handler invocation has entered. Control B observes, from
// inside the cleanup itself, that the teardown reached its connection-custody wait while
// a refused invocation is still parked, then shows the teardown cannot finish until that
// invocation is released; it retains the admitted held-request case. Control C reads the
// fixture's real Accept barrier inside the control-owned trigger function and keeps the
// exact real Serve result. They assert on facts of their own — ConnState callbacks,
// invocation counters, response bytes, Serve results — never on a boolean assigned by
// the helper under test.
//
// No OS signal is raised, no network beyond loopback is used, and no engine, store,
// browser or database is involved.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// errListenerFailureFixture is the known root cause the fixtures inject into
// waitAndShutdown's own error channel. The oracles assert this exact sentinel is still
// reachable with errors.Is on the value the function returns.
var errListenerFailureFixture = errors.New("serve-listener-failure: synthetic listener failure (fixture-injected, not a real bind error)")

// listenerFailureHTTPSlots names the eight *http.Server parameters of waitAndShutdown
// in signature order: the main API server plus the seven auxiliary slots. The signature
// at cmd_serve.go is authoritative; this list mirrors it and the regression starts one
// real loopback server for every entry.
var listenerFailureHTTPSlots = [8]string{
	"httpSrv",
	"hitlSrv",
	"voiceWebhookSrv",
	"gatewaySrv",
	"hookPEPSrv",
	"codexPEPSrv",
	"grokPEPSrv",
	"proxySrv",
}

const (
	// listenerFailureBody is answered by an invocation admitted to the held body.
	listenerFailureBody = "serve-listener-failure-ok"
	// listenerFailureRejected is answered by an invocation that reached a closed
	// held-body admission. It is a real response the controls read off the wire.
	listenerFailureRejected = "serve-listener-failure-admission-closed"

	// Local, explicit bounds so every fixture returns a verdict instead of hanging.
	// None of them is a product budget: the only budget the function carries is its
	// own 20 s HTTP shutdown context, which this file does not touch.
	listenerFailureReadHeaderTimeout = 5 * time.Second
	listenerFailureClientTimeout     = 3 * time.Second
	listenerFailureHeldClientTimeout = 60 * time.Second
	listenerFailureDialTimeout       = 2 * time.Second
	listenerFailureJoinTimeout       = 10 * time.Second
	listenerFailureReturnBound       = 30 * time.Second
	listenerFailureServeExitBound    = 25 * time.Second
	listenerFailurePendingWindow     = 200 * time.Millisecond
	listenerFailureHandlerGuard      = 60 * time.Second
)

// listenerFailureLogBuffer collects what the product function logs so the oracles can
// check the fatal branch is neither silent nor leaking the raw cause.
type listenerFailureLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *listenerFailureLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *listenerFailureLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// listenerFailureAcceptBarrier wraps an owned listener so that the first Accept call —
// which only the server's own Serve loop makes — becomes an observable fact. It is the
// single readiness mechanism of this file, for HTTP and gRPC alike.
type listenerFailureAcceptBarrier struct {
	net.Listener
	once    sync.Once
	entered chan struct{}
}

func newListenerFailureAcceptBarrier(ln net.Listener) *listenerFailureAcceptBarrier {
	return &listenerFailureAcceptBarrier{Listener: ln, entered: make(chan struct{})}
}

func (l *listenerFailureAcceptBarrier) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.entered) })
	return l.Listener.Accept()
}

// listenerFailureConnTracker is one HTTP server's custody of the connections it
// accepted, mirroring exactly what net/http reports through ConnState. See the file
// header for the pinned ordering this relies on: registration at StateNew from the
// accept loop, retirement only at the serve goroutine's terminal StateClosed, and a
// hijacked connection recorded as unresolved because no StateClosed will ever follow.
type listenerFailureConnTracker struct {
	label string

	mu          sync.Mutex
	open        map[net.Conn]struct{}
	hijacked    map[net.Conn]struct{}
	registered  int
	terminal    int
	sealed      bool
	drained     chan struct{}
	drainedDone bool
}

func newListenerFailureConnTracker(label string) *listenerFailureConnTracker {
	return &listenerFailureConnTracker{
		label:    label,
		open:     make(map[net.Conn]struct{}),
		hijacked: make(map[net.Conn]struct{}),
		drained:  make(chan struct{}),
	}
}

func (tr *listenerFailureConnTracker) onState(c net.Conn, state http.ConnState) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	switch state {
	case http.StateNew:
		tr.open[c] = struct{}{}
		tr.registered++
	case http.StateClosed:
		// The only terminal fact. It is reported by the connection's own serve
		// goroutine after the handler returned.
		if _, ok := tr.open[c]; ok {
			delete(tr.open, c)
			tr.terminal++
		}
	case http.StateHijacked:
		// Unsupported here. The entry leaves the outstanding set so the fixture does
		// not block on a fact that can never arrive, but it is recorded as unresolved
		// ownership and is never counted as terminal.
		if _, ok := tr.open[c]; ok {
			delete(tr.open, c)
		}
		tr.hijacked[c] = struct{}{}
	}
	tr.signalDrainedLocked()
}

// seal is called only after the server's Serve loop has been joined. From that instant
// net/http cannot register another connection, so the accepted set is final.
func (tr *listenerFailureConnTracker) seal() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.sealed = true
	tr.signalDrainedLocked()
}

func (tr *listenerFailureConnTracker) signalDrainedLocked() {
	if tr.sealed && len(tr.open) == 0 && !tr.drainedDone {
		tr.drainedDone = true
		close(tr.drained)
	}
}

// awaitDrained reports whether the accepted set is final and every accepted connection
// has reached its terminal ConnState fact. A false result is unresolved, not completion.
func (tr *listenerFailureConnTracker) awaitDrained(bound time.Duration) bool {
	select {
	case <-tr.drained:
		return true
	case <-time.After(bound):
		return false
	}
}

// isSealed reports whether registration was closed by an explicit, completed Serve
// join. Counts read while it is false are observations, not a final accepted set.
func (tr *listenerFailureConnTracker) isSealed() bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.sealed
}

func (tr *listenerFailureConnTracker) counts() (registered, terminal, outstanding, hijacked int) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.registered, tr.terminal, len(tr.open), len(tr.hijacked)
}

// listenerFailureHandlerProbe counts full HTTP handler invocations: entry, and the
// deferred return of every invocation, whether it was admitted to a held body or
// refused. It is never a held-body count.
type listenerFailureHandlerProbe struct {
	mu      sync.Mutex
	entries int
	returns int
}

func (p *listenerFailureHandlerProbe) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries++
}

func (p *listenerFailureHandlerProbe) exit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.returns++
}

func (p *listenerFailureHandlerProbe) snapshot() (entries, returns int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entries, p.returns
}

func (p *listenerFailureHandlerProbe) wrap(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.enter()
		defer p.exit()
		inner.ServeHTTP(w, r)
	})
}

// listenerFailureHeldWork is the guarded admission to a fixture handler's held body.
// Admission and closure take the same mutex, so an invocation racing the teardown is
// either admitted — and then waited for — or refused, deterministically. Closing
// admission does not stop an invocation from running; joining invocations is the
// connection tracker's job, not this type's.
type listenerFailureHeldWork struct {
	label string

	mu       sync.Mutex
	closed   bool
	inside   int
	admitted int
	refused  int
	idleDone bool

	entered     chan struct{}
	enteredOnce sync.Once
	idle        chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newListenerFailureHeldWork(label string) *listenerFailureHeldWork {
	return &listenerFailureHeldWork{
		label:   label,
		entered: make(chan struct{}),
		idle:    make(chan struct{}),
		release: make(chan struct{}),
	}
}

// admit reports whether this invocation may run the held body. After closeAdmission it
// always refuses, and the refusal is counted.
func (w *listenerFailureHeldWork) admit() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.refused++
		return false
	}
	w.inside++
	w.admitted++
	w.enteredOnce.Do(func() { close(w.entered) })
	return true
}

func (w *listenerFailureHeldWork) leave() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inside--
	w.signalIdleLocked()
}

func (w *listenerFailureHeldWork) signalIdleLocked() {
	if w.closed && w.inside == 0 && !w.idleDone {
		w.idleDone = true
		close(w.idle)
	}
}

// closeAdmission refuses every later admission. It is idempotent.
func (w *listenerFailureHeldWork) closeAdmission() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.signalIdleLocked()
}

// releaseAll unblocks every invocation already inside the held body. It is idempotent.
func (w *listenerFailureHeldWork) releaseAll() {
	w.releaseOnce.Do(func() { close(w.release) })
}

// awaitQuiescence reports whether admission is closed and every admitted invocation has
// left the held body. It says nothing about invocations that were refused, and nothing
// about whether any invocation has returned to net/http.
func (w *listenerFailureHeldWork) awaitQuiescence(bound time.Duration) bool {
	select {
	case <-w.idle:
		return true
	case <-time.After(bound):
		return false
	}
}

func (w *listenerFailureHeldWork) counts() (admitted, left, inside, refused int, closed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.admitted, w.admitted - w.inside, w.inside, w.refused, w.closed
}

// hold runs the held body and reports whether it was admitted.
func (w *listenerFailureHeldWork) hold() bool {
	if !w.admit() {
		return false
	}
	defer w.leave()
	select {
	case <-w.release:
	case <-time.After(listenerFailureHandlerGuard):
		// Fixture guard only, so a mistake cannot hang the package. It is never the
		// causal evidence of anything.
	}
	return true
}

// listenerFailureHeldHandler is the plain held handler used by group 2. The controls
// supply their own so they can add a control-owned return barrier.
func listenerFailureHeldHandler(held *listenerFailureHeldWork) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if held.hold() {
			_, _ = io.WriteString(w, listenerFailureBody)
			return
		}
		_, _ = io.WriteString(w, listenerFailureRejected)
	})
}

// listenerFailureServer is one fixture HTTP server: a real loopback listener kept by
// the test, its accept barrier, its connection tracker, its handler probe, its own
// Serve goroutine and that goroutine's completion.
type listenerFailureServer struct {
	srv     *http.Server
	ln      net.Listener
	addr    string
	label   string
	tracker *listenerFailureConnTracker
	probe   *listenerFailureHandlerProbe
	started chan struct{} // closed on the first Accept of this server's Serve loop
	exited  chan struct{}
	// serveErr is written before exited is closed. It must never be read unless
	// joined is true; observedServeResult is the only sanctioned reader.
	serveErr error
	// joined records that a receive from exited actually happened. A bounded wait that
	// expired never sets it: an expiration is not a join and does not make the result
	// safe to read.
	joined bool
	// checked records that the Serve sentinel has already been required once, so the
	// teardown does not report the same fact twice. It is set only where the result
	// was actually read.
	checked bool
}

// observedServeResult returns the fixture's Serve result and whether its completion was
// actually received. Every reader in this file goes through it; nothing reads serveErr
// directly.
func (f *listenerFailureServer) observedServeResult() (error, bool) {
	if !f.joined {
		return nil, false
	}
	return f.serveErr, true
}

// listenerFailureGRPCServer is one fixture grpc.Server with the same custody.
type listenerFailureGRPCServer struct {
	srv      *grpc.Server
	ln       net.Listener
	addr     string
	started  chan struct{}
	exited   chan struct{}
	serveErr error
	joined   bool
	checked  bool
}

// observedServeResult has the same contract as the HTTP fixture's.
func (g *listenerFailureGRPCServer) observedServeResult() (error, bool) {
	if !g.joined {
		return nil, false
	}
	return g.serveErr, true
}

// listenerFailureCall is one running waitAndShutdown goroutine. done is a completion
// fact, not a value handoff: it is closed, so the body and the teardown can both wait
// on it and neither consumes the other's receive.
type listenerFailureCall struct {
	label string
	done  chan struct{}
	ret   error // written before done is closed
}

// listenerFailureClient is one owned HTTP request goroutine, with the cancel and the
// transport it owns, plus its own completion fact for the same reason.
type listenerFailureClient struct {
	label     string
	done      chan struct{}
	body      string
	err       error // both written before done is closed
	cancel    context.CancelFunc
	transport *http.Transport
}

// listenerFailureConn is one raw owned connection, used where a real in-flight request
// must stop short of the handler.
type listenerFailureConn struct {
	label string
	conn  net.Conn
}

// listenerFailureHarness owns every fixture of one test and performs the whole ordered
// teardown from a single idempotent cleanup registered before any fixture is built.
type listenerFailureHarness struct {
	t             *testing.T
	fixtureCtx    context.Context
	cancelFixture context.CancelFunc
	logBuf        *listenerFailureLogBuffer

	held     []*listenerFailureHeldWork
	conns    []*listenerFailureConn
	clients  []*listenerFailureClient
	servers  []*listenerFailureServer
	grpcs    []*listenerFailureGRPCServer
	calls    []*listenerFailureCall
	teardown sync.Once

	// custodyWaitObserver, when a control sets it before launching anything, is called
	// from inside the teardown at the exact instant the connection-custody wait for one
	// server is about to begin. It is fixture-only: no product code is involved, the
	// default is nil, and it observes progress rather than producing it.
	custodyWaitObserver func(*listenerFailureServer)
}

// listenerFailureCustodyResult is the outcome of one server's connection custody. It
// separates what was observed from what was actually completed.
type listenerFailureCustodyResult struct {
	sealed      bool
	drained     bool
	registered  int
	terminal    int
	outstanding int
	hijacked    int
}

// complete reports whether this server's custody is a real join: registration sealed by
// a completed Serve join, every accepted connection terminal, and no hijacked
// connection whose ownership cannot be resolved.
func (r listenerFailureCustodyResult) complete() bool {
	return r.sealed && r.drained && r.outstanding == 0 && r.hijacked == 0
}

// newListenerFailureHarness must be the first statement of every test in this file.
func newListenerFailureHarness(t *testing.T) *listenerFailureHarness {
	t.Helper()
	h := &listenerFailureHarness{t: t, logBuf: &listenerFailureLogBuffer{}}
	h.fixtureCtx, h.cancelFixture = context.WithCancel(context.Background())
	t.Cleanup(h.runTeardown)
	return h
}

// logger returns the logger handed to waitAndShutdown. Its buffer is printed by the
// teardown, so anything the product logs while cleaning up is captured too.
func (h *listenerFailureHarness) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(h.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// join waits for a completion fact within a bound and reports an unresolved fixture if
// it does not arrive. It never calls Fatal: cleanup must not mask the original failure.
func (h *listenerFailureHarness) join(what string, done <-chan struct{}, bound time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(bound):
		h.t.Errorf("UNRESOLVED: listener-failure cleanup: %s did not finish within %s", what, bound)
		return false
	}
}

// runTeardown is the single registered cleanup and is idempotent, so a control may
// drive the real thing and the registered call then does nothing.
func (h *listenerFailureHarness) runTeardown() { h.teardown.Do(h.teardownOnce) }

func (h *listenerFailureHarness) teardownOnce() {
	t := h.t

	// 1. Release held work and close held-body admission. This is NOT a handler join:
	//    a later invocation still runs, it is only steered to its refusal branch.
	for _, w := range h.held {
		w.releaseAll()
		w.closeAdmission()
	}

	// 2. Remove the request producers: owned raw connections first, then each client
	//    goroutine, whose request and transport it owns.
	for i := len(h.conns) - 1; i >= 0; i-- {
		_ = h.conns[i].conn.Close()
	}
	for i := len(h.clients) - 1; i >= 0; i-- {
		c := h.clients[i]
		c.cancel()
		c.transport.CloseIdleConnections()
		h.join("client request "+c.label, c.done, listenerFailureJoinTimeout)
	}

	// 3. Close each owned HTTP server and join its Serve loop. Registration is sealed
	//    only when that explicit join actually completed: an expired bound is not a
	//    join, so it leaves ownership unresolved and the Serve result unread. Every
	//    server is still processed, so one failure does not skip the others.
	for i := len(h.servers) - 1; i >= 0; i-- {
		f := h.servers[i]
		_ = f.srv.Close()
		if h.join("Serve of "+f.label+" on "+f.addr, f.exited, listenerFailureJoinTimeout) {
			f.joined = true
			if !f.checked {
				f.checked = true
				if serveErr, _ := f.observedServeResult(); !errors.Is(serveErr, http.ErrServerClosed) {
					t.Errorf("listener-failure cleanup: %s Serve on %s returned %v, want http.ErrServerClosed", f.label, f.addr, serveErr)
				}
			}
			f.tracker.seal()
		} else {
			t.Errorf("UNRESOLVED: listener-failure cleanup: %s on %s keeps its connection registration unsealed and its Serve result unread, "+
				"because that explicit join did not complete", f.label, f.addr)
		}
		_ = f.ln.Close()
	}

	// 4. Stop each owned gRPC server and join its Serve loop, under the same rule.
	for i := len(h.grpcs) - 1; i >= 0; i-- {
		g := h.grpcs[i]
		g.srv.Stop()
		if h.join("gRPC Serve on "+g.addr, g.exited, listenerFailureJoinTimeout) {
			g.joined = true
			if !g.checked {
				g.checked = true
				// grpc@v1.82.1 server.go: Serve returns a non-nil error unless Stop or
				// GracefulStop was called, in which case it returns exactly nil.
				if serveErr, _ := g.observedServeResult(); serveErr != nil {
					t.Errorf("listener-failure cleanup: gRPC Serve on %s returned %v, want nil after Stop", g.addr, serveErr)
				}
			}
		} else {
			t.Errorf("UNRESOLVED: listener-failure cleanup: the gRPC fixture on %s keeps its Serve result unread, because that explicit join did not complete", g.addr)
		}
		_ = g.ln.Close()
	}

	// 5. Await the terminal ConnState fact of every accepted connection. Server.Close
	//    does not wait for connection goroutines, so this is the only join that covers
	//    a full handler invocation, admitted or refused. The per-server outcome is
	//    retained, not just its counts.
	custody := make(map[*listenerFailureServer]listenerFailureCustodyResult, len(h.servers))
	var registered, terminal, outstanding, hijacked, completed int
	for i := len(h.servers) - 1; i >= 0; i-- {
		f := h.servers[i]
		r := h.reconcileConnCustody(f)
		custody[f] = r
		registered, terminal = registered+r.registered, terminal+r.terminal
		outstanding, hijacked = outstanding+r.outstanding, hijacked+r.hijacked
		if r.complete() {
			completed++
		}
	}
	if len(h.servers) > 0 {
		t.Logf("listener-failure cleanup: connection custody completed for %d of %d owned HTTP server(s) — %d accepted, %d terminal, %d outstanding, %d hijacked",
			completed, len(h.servers), registered, terminal, outstanding, hijacked)
	}

	// 6. Held-body admission, reported as the separate fact it is.
	for _, w := range h.held {
		h.reconcileHeldWork(w)
	}

	// 7. Handler-invocation balance. The claim that a count is FINAL rests on this
	//    server's custody having completed; without that, the same numbers are only
	//    observations and are reported as such.
	for _, f := range h.servers {
		entries, returns := f.probe.snapshot()
		r := custody[f]
		if !r.complete() {
			t.Errorf("UNRESOLVED: listener-failure cleanup: %s on %s observed %d handler invocation(s) entered and %d returned, but these are NOT final: "+
				"connection custody did not complete (sealed=%v drained=%v outstanding=%d hijacked=%d)",
				f.label, f.addr, entries, returns, r.sealed, r.drained, r.outstanding, r.hijacked)
			continue
		}
		if entries != returns {
			t.Errorf("listener-failure cleanup: %s on %s entered %d handler invocation(s) and returned from %d after a completed connection-custody barrier",
				f.label, f.addr, entries, returns)
		}
	}

	// 8. The fixture context exists only for this step. Cancelling it releases a
	//    product call still parked in its select — for example when a fixture failed
	//    before the fatal cause could be injected.
	h.cancelFixture()
	for i := len(h.calls) - 1; i >= 0; i-- {
		h.join("waitAndShutdown call "+h.calls[i].label, h.calls[i].done, listenerFailureJoinTimeout)
	}

	// 9. Whatever the product said, including anything logged during this teardown.
	t.Logf("waitAndShutdown log output:\n%s", h.logBuf.String())
}

// reconcileConnCustody awaits one server's connection custody and returns whether that
// custody actually completed, along with the counts it observed.
func (h *listenerFailureHarness) reconcileConnCustody(f *listenerFailureServer) listenerFailureCustodyResult {
	var r listenerFailureCustodyResult
	r.sealed = f.tracker.isSealed()
	if !r.sealed {
		// Without a completed Serve join the accepted set is not final, so there is no
		// boundary to wait at and the counts below are observations only.
		r.registered, r.terminal, r.outstanding, r.hijacked = f.tracker.counts()
		h.t.Errorf("UNRESOLVED: listener-failure cleanup: %s on %s has unsealed connection registration; %d observed accepted connection(s) and %d terminal fact(s) "+
			"are observations, not a join", f.label, f.addr, r.registered, r.terminal)
		return r
	}
	// The actual custody wait boundary. A control may observe that the cleanup reached
	// it; the observation cannot advance or replace the wait below.
	if observe := h.custodyWaitObserver; observe != nil {
		observe(f)
	}
	r.drained = f.tracker.awaitDrained(listenerFailureJoinTimeout)
	r.registered, r.terminal, r.outstanding, r.hijacked = f.tracker.counts()
	if !r.drained {
		h.t.Errorf("UNRESOLVED: listener-failure cleanup: %s on %s — %d accepted connection(s), %d terminal, %d still without a terminal ConnState fact after %s; "+
			"this timeout does NOT establish that their handler invocations returned",
			f.label, f.addr, r.registered, r.terminal, r.outstanding, listenerFailureJoinTimeout)
	}
	if r.hijacked > 0 {
		h.t.Errorf("UNRESOLVED: listener-failure cleanup: %s on %s hijacked %d connection(s); net/http never reports StateClosed for a hijacked connection, "+
			"so their ownership is unresolved, is not counted as terminal, and leaves this custody incomplete", f.label, f.addr, r.hijacked)
	}
	return r
}

// reconcileHeldWork reports the held-body admission fact. It is deliberately not a
// handler count and is never described as one.
func (h *listenerFailureHarness) reconcileHeldWork(w *listenerFailureHeldWork) {
	if _, _, _, _, closed := w.counts(); !closed {
		h.t.Errorf("listener-failure cleanup: held work %q reached reconciliation with its admission still open", w.label)
	}
	if !w.awaitQuiescence(listenerFailureJoinTimeout) {
		admitted, left, inside, refused, _ := w.counts()
		h.t.Errorf("UNRESOLVED: listener-failure cleanup: held work %q did not quiesce within %s — %d admitted, %d left, %d still inside, %d refused; "+
			"this timeout does NOT establish that the outstanding held body completed", w.label, listenerFailureJoinTimeout, admitted, left, inside, refused)
		return
	}
	admitted, _, _, refused, _ := w.counts()
	h.t.Logf("listener-failure cleanup: held work %q — held-body admission closed, %d admitted and all left, %d refused. "+
		"These are held-body admission facts; handler invocations are joined by connection custody.", w.label, admitted, refused)
}

// newHeldWork registers one unit of held work with the harness before any handler that
// uses it can run.
func (h *listenerFailureHarness) newHeldWork(label string) *listenerFailureHeldWork {
	w := newListenerFailureHeldWork(label)
	h.held = append(h.held, w)
	return w
}

// newServer starts one owned HTTP server on 127.0.0.1:0, behind an accept barrier, and
// keeps its listener so a later closure claim can be tied to this exact generation.
func (h *listenerFailureHarness) newServer(label string, handler http.Handler) *listenerFailureServer {
	h.t.Helper()
	return h.newServerObserved(label, handler, nil)
}

// newServerObserved additionally forwards every ConnState callback to a control-owned
// observer. The tracker is always first and always present: each server owns its
// tracker and its handler probe before Serve starts.
func (h *listenerFailureHarness) newServerObserved(label string, handler http.Handler, observe func(net.Conn, http.ConnState)) *listenerFailureServer {
	h.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatalf("listener-failure: listen loopback port 0 for %s: %v", label, err)
	}
	barrier := newListenerFailureAcceptBarrier(ln)
	f := &listenerFailureServer{
		ln:      ln,
		addr:    ln.Addr().String(),
		label:   label,
		tracker: newListenerFailureConnTracker(label),
		probe:   &listenerFailureHandlerProbe{},
		started: barrier.entered,
		exited:  make(chan struct{}),
	}
	f.srv = &http.Server{
		Handler:           f.probe.wrap(handler),
		ReadHeaderTimeout: listenerFailureReadHeaderTimeout,
		ConnState: func(c net.Conn, state http.ConnState) {
			f.tracker.onState(c, state)
			if observe != nil {
				observe(c, state)
			}
		},
	}
	// Registered with the harness before the goroutine starts, so the teardown owns it
	// from the first instant it can exist.
	h.servers = append(h.servers, f)
	go func() {
		f.serveErr = f.srv.Serve(barrier)
		close(f.exited)
	}()
	return f
}

// newServerSet starts one owned HTTP server for every slot of the signature.
func (h *listenerFailureHarness) newServerSet() []*listenerFailureServer {
	h.t.Helper()
	set := make([]*listenerFailureServer, 0, len(listenerFailureHTTPSlots))
	for _, label := range listenerFailureHTTPSlots {
		set = append(set, h.newServer(label, http.HandlerFunc(listenerFailureOK)))
	}
	return set
}

// newGRPCServer starts one real grpc.Server behind the same accept barrier.
func (h *listenerFailureHarness) newGRPCServer() *listenerFailureGRPCServer {
	h.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatalf("listener-failure: listen loopback port 0 for gRPC: %v", err)
	}
	barrier := newListenerFailureAcceptBarrier(ln)
	g := &listenerFailureGRPCServer{
		srv:     grpc.NewServer(),
		ln:      ln,
		addr:    ln.Addr().String(),
		started: barrier.entered,
		exited:  make(chan struct{}),
	}
	h.grpcs = append(h.grpcs, g)
	go func() {
		g.serveErr = g.srv.Serve(barrier)
		close(g.exited)
	}()
	return g
}

// awaitServeStarted waits for the fixture's own Serve loop to enter Accept.
func (h *listenerFailureHarness) awaitServeStarted(label, addr string, started <-chan struct{}) {
	h.t.Helper()
	select {
	case <-started:
	case <-time.After(listenerFailureReturnBound):
		h.t.Fatalf("listener-failure: %s on %s did not enter its Serve accept loop within %s", label, addr, listenerFailureReturnBound)
	}
}

// pendingGRPCReadiness reports the owned gRPC fixtures whose real Accept-entry barrier
// has not been observed. It reads those barrier channels themselves; it holds no
// derived state and records nothing.
func (h *listenerFailureHarness) pendingGRPCReadiness() []string {
	var pending []string
	for _, g := range h.grpcs {
		select {
		case <-g.started:
		default:
			pending = append(pending, g.addr)
		}
	}
	return pending
}

// startCall runs one waitAndShutdown in its own goroutine. The trigger context is
// supplied by the caller so the benign canceled-context subcase can pass an already
// canceled child of the fixture context; the harness still owns the join. It is the
// single admission guard for every product trigger site in this file.
func (h *listenerFailureHarness) startCall(ctx context.Context, label string, fn func(context.Context) error) *listenerFailureCall {
	h.t.Helper()
	if pending := h.pendingGRPCReadiness(); len(pending) > 0 {
		h.t.Fatalf("listener-failure: %s would trigger the product before the gRPC fixture(s) %v entered their Serve accept loop", label, pending)
	}
	c := &listenerFailureCall{label: label, done: make(chan struct{})}
	h.calls = append(h.calls, c)
	go func() {
		c.ret = fn(ctx)
		close(c.done)
	}()
	return c
}

// awaitCall waits for the product call within a bound. A timeout is fatal for the test
// body; the teardown still releases and joins the goroutine afterwards.
func (h *listenerFailureHarness) awaitCall(c *listenerFailureCall, bound time.Duration) error {
	h.t.Helper()
	select {
	case <-c.done:
		return c.ret
	case <-time.After(bound):
		h.t.Fatalf("UNRESOLVED: listener-failure: %s did not return within %s", c.label, bound)
		return nil
	}
}

// startGet issues one owned HTTP request from its own goroutine, on a dedicated
// transport with keep-alive disabled so every request is a fresh accept.
func (h *listenerFailureHarness) startGet(label, addr string, timeout time.Duration) *listenerFailureClient {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	c := &listenerFailureClient{
		label:     label,
		done:      make(chan struct{}),
		cancel:    cancel,
		transport: &http.Transport{DisableKeepAlives: true},
	}
	h.clients = append(h.clients, c)
	go func() {
		defer close(c.done)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/ds1", nil)
		if err != nil {
			c.err = err
			return
		}
		resp, err := (&http.Client{Transport: c.transport}).Do(req)
		if err != nil {
			c.err = err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		c.body, c.err = string(body), err
	}()
	return c
}

// awaitClient waits for an owned request goroutine within a bound.
func (h *listenerFailureHarness) awaitClient(c *listenerFailureClient, bound time.Duration) (string, error) {
	h.t.Helper()
	select {
	case <-c.done:
		return c.body, c.err
	case <-time.After(bound):
		h.t.Fatalf("UNRESOLVED: listener-failure: client request %s did not finish within %s", c.label, bound)
		return "", nil
	}
}

// dialOwned opens one raw connection the harness closes in its teardown.
func (h *listenerFailureHarness) dialOwned(label, addr string) net.Conn {
	h.t.Helper()
	conn, err := net.DialTimeout("tcp", addr, listenerFailureDialTimeout)
	if err != nil {
		h.t.Fatalf("listener-failure: dial %s for %s: %v", addr, label, err)
	}
	h.conns = append(h.conns, &listenerFailureConn{label: label, conn: conn})
	return conn
}

// listenerFailureOK is the fixture handler for the servers that only have to prove
// they admit and answer.
func listenerFailureOK(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, listenerFailureBody)
}

// listenerFailureGetOnce performs one bounded synchronous request. It owns and drops
// its transport, so there is no goroutine and nothing for the teardown to join.
func listenerFailureGetOnce(addr string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listenerFailureClientTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/ds1", nil)
	if err != nil {
		return "", err
	}
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// confirmServing establishes both readiness facts for an HTTP fixture: its Serve loop
// entered Accept, and this listener generation answers a real request. The second is
// the "was admitting" premise of the closure oracle.
func (h *listenerFailureHarness) confirmServing(f *listenerFailureServer) {
	h.t.Helper()
	h.awaitServeStarted(f.label, f.addr, f.started)
	body, err := listenerFailureGetOnce(f.addr)
	if err != nil {
		h.t.Fatalf("listener-failure: fixture %s on %s did not serve its readiness request: %v", f.label, f.addr, err)
	}
	if body != listenerFailureBody {
		h.t.Fatalf("listener-failure: fixture %s on %s answered %q, want %q", f.label, f.addr, body, listenerFailureBody)
	}
}

// confirmGRPCReady establishes the same two facts for a gRPC fixture: its Serve loop
// entered Accept — which in grpc v1.82.1 happens only after the listener is registered,
// so ErrServerStopped is no longer reachable — and this listener admits a connection.
func (h *listenerFailureHarness) confirmGRPCReady(g *listenerFailureGRPCServer) {
	h.t.Helper()
	h.awaitServeStarted("gRPC", g.addr, g.started)
	conn, err := net.DialTimeout("tcp", g.addr, listenerFailureDialTimeout)
	if err != nil {
		h.t.Fatalf("listener-failure: the gRPC fixture listener %s did not admit a connection before the trigger: %v", g.addr, err)
	}
	_ = conn.Close()
}

// listenerFailureDialOutcome classifies one fresh dial. "Not admitted" is not the same
// fact as "refused": only a connection refusal is closure evidence.
type listenerFailureDialOutcome struct {
	admitted bool
	refused  bool
	err      error
}

func listenerFailureDial(addr string) listenerFailureDialOutcome {
	conn, err := net.DialTimeout("tcp", addr, listenerFailureDialTimeout)
	if err == nil {
		_ = conn.Close()
		return listenerFailureDialOutcome{admitted: true}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return listenerFailureDialOutcome{refused: true, err: err}
	}
	return listenerFailureDialOutcome{err: err}
}

// awaitServeClosed joins one fixture's own Serve call and requires the exact sentinel
// the pinned net/http returns once the server has been shut down. This is the first
// closure fact and, in group 2, the causal barrier that the product entered Shutdown.
func (h *listenerFailureHarness) awaitServeClosed(group string, f *listenerFailureServer, bound time.Duration) {
	h.t.Helper()
	select {
	case <-f.exited:
	case <-time.After(bound):
		// No join, so no result may be read and nothing is marked as checked; the
		// teardown will retry this join and report on it.
		h.t.Fatalf("UNRESOLVED: %s: Serve of %s on %s did not return within %s, so no closure is established and its Serve result must not be read",
			group, f.label, f.addr, bound)
		return
	}
	f.joined = true
	f.checked = true
	serveErr, _ := f.observedServeResult()
	if !errors.Is(serveErr, http.ErrServerClosed) {
		h.t.Errorf("%s: Serve of %s on %s returned %v, want http.ErrServerClosed", group, f.label, f.addr, serveErr)
	}
}

// requireRefused completes the closure claim for one HTTP fixture: a fresh dial must be
// refused specifically, and the listener the readiness probe used must already be
// closed. The dial always precedes that close, so this cannot manufacture its own
// refusal; when the listener still admits, a real request records what it answered.
func (h *listenerFailureHarness) requireRefused(group string, f *listenerFailureServer, elapsed time.Duration) {
	h.t.Helper()
	switch out := listenerFailureDial(f.addr); {
	case out.admitted:
		served, err := listenerFailureGetOnce(f.addr)
		h.t.Errorf("REGRESSION ORACLE FAILED (%s): HTTP slot %s on %s still admitted a fresh connection after waitAndShutdown returned in %s "+
			"(a follow-up request answered %q, err=%v); a fatal listener error must drain every transport before returning",
			group, f.label, f.addr, elapsed, served, err)
	case !out.refused:
		h.t.Errorf("%s: the fresh dial to HTTP slot %s on %s neither connected nor was refused (%v); this is an incomplete observation, not closure",
			group, f.label, f.addr, out.err)
	}
	if err := f.ln.Close(); !errors.Is(err, net.ErrClosed) {
		h.t.Errorf("%s: the listener generation %s served its readiness request from on %s was not already closed by the product (Close returned %v)",
			group, f.label, f.addr, err)
	}
}

// requireGRPCClosed is the same three-fact closure claim for the fixture grpc.Server.
func (h *listenerFailureHarness) requireGRPCClosed(group string, g *listenerFailureGRPCServer, elapsed time.Duration) {
	h.t.Helper()
	h.requireGRPCServeStopped(group, g)
	switch out := listenerFailureDial(g.addr); {
	case out.admitted:
		h.t.Errorf("REGRESSION ORACLE FAILED (%s): the gRPC listener %s still admitted a fresh connection after waitAndShutdown returned in %s; "+
			"a fatal listener error must reach grpcSrv.GracefulStop before returning", group, g.addr, elapsed)
	case !out.refused:
		h.t.Errorf("%s: the fresh dial to the gRPC listener %s neither connected nor was refused (%v); this is an incomplete observation, not closure",
			group, g.addr, out.err)
	}
	if err := g.ln.Close(); !errors.Is(err, net.ErrClosed) {
		h.t.Errorf("%s: the gRPC listener generation on %s was not already closed by the product (Close returned %v)", group, g.addr, err)
	}
}

// requireGRPCServeStopped joins the fixture's own gRPC Serve result and requires the
// exact value the pinned implementation returns after a stop. ErrServerStopped is NOT
// accepted: with the Accept-entry barrier established before every trigger, it can only
// mean the server never started, which is a fixture failure and not closure.
func (h *listenerFailureHarness) requireGRPCServeStopped(group string, g *listenerFailureGRPCServer) {
	h.t.Helper()
	select {
	case <-g.exited:
		g.joined = true
		g.checked = true
		if serveErr, _ := g.observedServeResult(); serveErr != nil {
			h.t.Errorf("%s: gRPC Serve on %s returned %v, want exactly nil after a stop that followed a proven Serve start", group, g.addr, serveErr)
		}
	case <-time.After(listenerFailureJoinTimeout):
		// Not a join: the result stays unread and unchecked, and the teardown will
		// retry the join and report on it.
		h.t.Errorf("UNRESOLVED: %s: gRPC Serve on %s did not return within %s, so no closure is established and its Serve result must not be read",
			group, g.addr, listenerFailureJoinTimeout)
	}
}

// listenerFailureInjected wraps the fixture sentinel the way a real caller would see a
// listener die, so the returned value has to carry a two-level %w chain.
func listenerFailureInjected(addr string) error {
	return fmt.Errorf("serve-listener-failure fixture injection for %s: %w", addr, errListenerFailureFixture)
}

// TestServeListenerFailureDrainsEveryOwnedTransport is the permanent regression for
// DS1. It fills waitAndShutdown's own errCh with a wrapped fatal cause while the
// trigger context can never fire, so the errCh arm is the only reachable one, and then
// requires that every transport it was given has provably closed by the time the
// function returns — with the original cause still detectable through errors.Is.
//
// Before the DS1 repair this test is red because the listeners are still admitting
// after the return, not because any fixture failed.
func TestServeListenerFailureDrainsEveryOwnedTransport(t *testing.T) {
	h := newListenerFailureHarness(t)
	log := h.logger()

	set := h.newServerSet()
	for _, f := range set {
		h.confirmServing(f)
	}
	g := h.newGRPCServer()
	h.confirmGRPCReady(g)
	t.Logf("ds1/g1: %d owned HTTP listeners serving, gRPC Serve accept loop entered on %s", len(set), g.addr)

	// waitAndShutdown's own channel, filled with a known synthetic cause. The fixture
	// context is never cancelled while the test runs, so this is the only ready arm.
	errCh := make(chan error, 1)
	errCh <- listenerFailureInjected(set[0].addr)

	start := time.Now()
	call := h.startCall(h.fixtureCtx, "waitAndShutdown(g1)", func(ctx context.Context) error {
		return waitAndShutdown(ctx, set[0].srv, g.srv,
			set[1].srv, set[2].srv, set[3].srv, set[4].srv, set[5].srv, set[6].srv, set[7].srv,
			errCh, log)
	})
	ret := h.awaitCall(call, listenerFailureReturnBound)
	elapsed := time.Since(start)
	t.Logf("ds1/g1: waitAndShutdown returned after %s with err=%v", elapsed, ret)

	if ret == nil {
		t.Fatalf("ds1/g1: waitAndShutdown returned nil; the injected fatal listener error was swallowed")
	}
	if !errors.Is(ret, errListenerFailureFixture) {
		t.Errorf("ds1/g1: the original cause is NOT reachable with errors.Is on the returned error; got %q", ret.Error())
	}

	for _, f := range set {
		h.awaitServeClosed("ds1/g1", f, listenerFailureJoinTimeout)
		h.requireRefused("ds1/g1", f, elapsed)
	}
	h.requireGRPCClosed("ds1/g1", g, elapsed)

	// The fatal branch used to be entirely silent, and the diagnostic it now emits must
	// stay a constant: no raw cause payload is copied into the log, because the caller
	// already receives the error itself.
	logged := h.logBuf.String()
	if strings.TrimSpace(logged) == "" {
		t.Errorf("ds1/g1: the fatal listener branch logged nothing; the drain it triggers must be announced like the other two arms")
	}
	if strings.Contains(logged, errListenerFailureFixture.Error()) {
		t.Errorf("ds1/g1: the fatal listener diagnostic copied the raw error payload into the log:\n%s", logged)
	}
}

// TestServeListenerFailureDrainsHeldRequestBeforeReturning proves the drain on the
// fatal path is the real one and not a token call: an owned HTTP server holds one
// active request, and waitAndShutdown must stay inside its HTTP shutdown until that
// request is released, then return the original cause.
func TestServeListenerFailureDrainsHeldRequestBeforeReturning(t *testing.T) {
	h := newListenerFailureHarness(t)
	log := h.logger()

	held := h.newHeldWork("g2 held request")
	f := h.newServer("httpSrv", listenerFailureHeldHandler(held))
	// A readiness request would itself be held here, so this fixture's readiness is the
	// accept-loop barrier alone.
	h.awaitServeStarted(f.label, f.addr, f.started)

	client := h.startGet("held request", f.addr, listenerFailureHeldClientTimeout)

	// Readiness barrier: the handler itself reports the request was admitted to the
	// held body. No sleep is used as causal evidence anywhere in this test.
	select {
	case <-held.entered:
	case <-time.After(listenerFailureReturnBound):
		t.Fatalf("UNRESOLVED: ds1/g2: no invocation was admitted to the held body within %s", listenerFailureReturnBound)
	}
	t.Logf("ds1/g2: one request is active inside the held body on %s", f.addr)

	g := h.newGRPCServer()
	h.confirmGRPCReady(g)

	errCh := make(chan error, 1)
	errCh <- listenerFailureInjected(f.addr)

	start := time.Now()
	call := h.startCall(h.fixtureCtx, "waitAndShutdown(g2)", func(ctx context.Context) error {
		return waitAndShutdown(ctx, f.srv, g.srv, nil, nil, nil, nil, nil, nil, nil, errCh, log)
	})

	// First causal barrier: the real server's own Serve call returns ErrServerClosed,
	// which is the pinned implementation's proof that the product entered
	// http.Server.Shutdown and closed the listener on the fatal path.
	select {
	case <-call.done:
		t.Fatalf("REGRESSION ORACLE FAILED (ds1/g2): waitAndShutdown returned (err=%v) without its HTTP shutdown ever closing the listener", call.ret)
	default:
	}
	h.awaitServeClosed("ds1/g2", f, listenerFailureServeExitBound)
	serveErr, observed := f.observedServeResult()
	if !observed {
		t.Fatalf("ds1/g2: the Serve completion of %s was not received, so the HTTP shutdown barrier is not established", f.label)
	}
	t.Logf("ds1/g2: the owned server's Serve returned %v — the product entered its HTTP shutdown", serveErr)

	// Only after that barrier: the function must still be pending, because one request
	// is deliberately held and the drain cannot be complete.
	select {
	case <-call.done:
		t.Fatalf("REGRESSION ORACLE FAILED (ds1/g2): waitAndShutdown returned (err=%v) while a request was still held inside the handler; "+
			"the fatal path must complete the drain before returning", call.ret)
	case <-time.After(listenerFailurePendingWindow):
		t.Logf("ds1/g2: waitAndShutdown is still pending %s after the listener closed, with the request held", listenerFailurePendingWindow)
	}

	// The single variable changed.
	held.releaseAll()

	ret := h.awaitCall(call, listenerFailureReturnBound)
	elapsed := time.Since(start)
	t.Logf("ds1/g2: after releasing the held request waitAndShutdown returned err=%v", ret)

	if ret == nil {
		t.Fatalf("ds1/g2: waitAndShutdown returned nil; the injected fatal listener error was swallowed by the drain")
	}
	if !errors.Is(ret, errListenerFailureFixture) {
		t.Errorf("ds1/g2: the original cause is NOT reachable with errors.Is after the drain; got %q", ret.Error())
	}

	body, err := h.awaitClient(client, listenerFailureJoinTimeout)
	if err != nil {
		t.Errorf("ds1/g2: the held request did not complete after release: %v", err)
	} else if body != listenerFailureBody {
		t.Errorf("ds1/g2: the held request answered %q, want %q", body, listenerFailureBody)
	}

	h.requireRefused("ds1/g2", f, elapsed)
	h.requireGRPCClosed("ds1/g2", g, elapsed)
}

// TestServeListenerFailureBenignCausesStillDrain guards the paths the repair must not
// disturb: a nil listener result, the two benign sentinels and a canceled context all
// go on performing the ordinary drain and returning nil.
func TestServeListenerFailureBenignCausesStillDrain(t *testing.T) {
	triggers := []struct {
		name string
		// prepare fills the channel and returns the trigger context. It is always a
		// child of the harness fixture context, so the teardown remains a superset.
		prepare func(h *listenerFailureHarness, errCh chan error) context.Context
	}{
		{
			name: "nil-listener-result",
			prepare: func(h *listenerFailureHarness, errCh chan error) context.Context {
				errCh <- nil
				return h.fixtureCtx
			},
		},
		{
			name: "http-err-server-closed",
			prepare: func(h *listenerFailureHarness, errCh chan error) context.Context {
				errCh <- http.ErrServerClosed
				return h.fixtureCtx
			},
		},
		{
			name: "grpc-err-server-stopped",
			prepare: func(h *listenerFailureHarness, errCh chan error) context.Context {
				errCh <- grpc.ErrServerStopped
				return h.fixtureCtx
			},
		},
		{
			name: "canceled-context",
			prepare: func(h *listenerFailureHarness, _ chan error) context.Context {
				ctx, cancel := context.WithCancel(h.fixtureCtx)
				cancel()
				return ctx
			},
		},
	}
	auxiliaries := []struct {
		name string
		full bool
	}{
		{name: "no-auxiliary-servers", full: false},
		{name: "all-auxiliary-servers", full: true},
	}

	for _, aux := range auxiliaries {
		for _, tc := range triggers {
			// Fresh fixture servers and a fresh harness per subcase; no state is
			// shared between them.
			t.Run(aux.name+"/"+tc.name, func(t *testing.T) {
				h := newListenerFailureHarness(t)
				log := h.logger()

				var set []*listenerFailureServer
				if aux.full {
					set = h.newServerSet()
				} else {
					set = []*listenerFailureServer{h.newServer("httpSrv", http.HandlerFunc(listenerFailureOK))}
				}
				for _, f := range set {
					h.confirmServing(f)
				}
				g := h.newGRPCServer()
				h.confirmGRPCReady(g)

				errCh := make(chan error, 1)
				ctx := tc.prepare(h, errCh)

				start := time.Now()
				call := h.startCall(ctx, "waitAndShutdown(g3)", func(ctx context.Context) error {
					if aux.full {
						return waitAndShutdown(ctx, set[0].srv, g.srv,
							set[1].srv, set[2].srv, set[3].srv, set[4].srv, set[5].srv, set[6].srv, set[7].srv,
							errCh, log)
					}
					return waitAndShutdown(ctx, set[0].srv, g.srv, nil, nil, nil, nil, nil, nil, nil, errCh, log)
				})
				ret := h.awaitCall(call, listenerFailureReturnBound)
				elapsed := time.Since(start)

				if ret != nil {
					t.Errorf("ds1/g3: waitAndShutdown returned %v, want nil for a benign shutdown trigger", ret)
				}
				for _, f := range set {
					h.awaitServeClosed("ds1/g3", f, listenerFailureJoinTimeout)
					h.requireRefused("ds1/g3", f, elapsed)
				}
				h.requireGRPCClosed("ds1/g3", g, elapsed)
			})
		}
	}
}

// TestServeListenerFailureControlCleanupBeforeHandlerEntry is control A. It drives the
// REAL, complete, idempotent teardown while a real owned request has reached the server
// and cannot have entered a handler, which is exactly the schedule a passing regression
// run never produces. The request is deliberately not completed first: an early cleanup
// that only happens after the work finished would not be an early cleanup.
//
// Every fact asserted afterwards is independent of the custody helpers: the server's own
// Serve result, its ConnState callbacks, the raw connection's own terminal state, and
// the handler-invocation probe.
func TestServeListenerFailureControlCleanupBeforeHandlerEntry(t *testing.T) {
	h := newListenerFailureHarness(t)

	held := h.newHeldWork("control-a")

	accepted := make(chan struct{})
	var acceptedOnce sync.Once
	f := h.newServerObserved("httpSrv", listenerFailureHeldHandler(held), func(_ net.Conn, state http.ConnState) {
		// StateNew is set at accept time, before the connection's serve goroutine
		// starts (net/http server.go:3461). It is the server-side fact that this
		// connection reached the server. StateActive is deliberately NOT used: it is
		// only set after readRequest returns (server.go:1990), which for an incomplete
		// header block means when ReadHeaderTimeout expires — a first version of this
		// control waited on it and consumed the fixture's whole 5 s header timeout.
		if state == http.StateNew {
			acceptedOnce.Do(func() { close(accepted) })
		}
	})
	h.awaitServeStarted(f.label, f.addr, f.started)

	// A real, owned, in-flight HTTP request that has reached the server and cannot
	// reach the handler: the request line and one header are written, the terminating
	// blank line is not, so net/http is still reading the header block. No handler
	// invocation can start until that blank line arrives, whatever the scheduler does.
	conn := h.dialOwned("partial request", f.addr)
	select {
	case <-accepted:
	case <-time.After(listenerFailureReturnBound):
		t.Fatalf("UNRESOLVED: control-a: the server did not accept the owned connection within %s", listenerFailureReturnBound)
	}
	if err := conn.SetDeadline(time.Now().Add(listenerFailureClientTimeout)); err != nil {
		t.Fatalf("control-a: set deadline on the owned connection: %v", err)
	}
	if _, err := io.WriteString(conn, "GET /ds1 HTTP/1.1\r\nHost: "+f.addr+"\r\n"); err != nil {
		t.Fatalf("control-a: write the partial request: %v", err)
	}
	if entries, returns := f.probe.snapshot(); entries != 0 || returns != 0 {
		t.Fatalf("control-a: a handler invocation entered before the request was complete (entries=%d returns=%d); the control's premise does not hold", entries, returns)
	}
	t.Logf("control-a: the owned connection is accepted and its request is incomplete; no handler invocation has entered")

	// The real thing, in full, at exactly this point.
	h.runTeardown()

	// Independent terminal facts. Each is claimed only from a completion the teardown
	// actually received: the Serve result is read through observedServeResult, and the
	// custody counts are final only because registration was sealed by that same join.
	serveErr, observed := f.observedServeResult()
	if !observed {
		t.Errorf("control-a: the teardown did not receive the Serve completion of %s, so its result must not be read and no closure is established", f.label)
	} else if !errors.Is(serveErr, http.ErrServerClosed) {
		t.Errorf("control-a: the server's own Serve returned %v, want http.ErrServerClosed", serveErr)
	}
	sealed := f.tracker.isSealed()
	if !sealed {
		t.Errorf("control-a: connection registration was left unsealed, so the custody counts below are observations and not a join")
	}
	registered, terminal, outstanding, hijacked := f.tracker.counts()
	if registered != 1 || terminal != 1 || outstanding != 0 || hijacked != 0 {
		t.Errorf("control-a: connection custody reports registered=%d terminal=%d outstanding=%d hijacked=%d, want 1/1/0/0",
			registered, terminal, outstanding, hijacked)
	}
	custodyComplete := sealed && outstanding == 0 && hijacked == 0
	// The raw connection is terminal from the control's own side too: the teardown owns
	// it and closed it, so a further read reports net.ErrClosed rather than any byte.
	if err := conn.SetDeadline(time.Now().Add(listenerFailureClientTimeout)); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("control-a: setting a deadline on the owned connection after teardown returned %v, want net.ErrClosed or success", err)
	}
	buf := make([]byte, 1)
	if n, err := conn.Read(buf); !errors.Is(err, net.ErrClosed) {
		t.Errorf("control-a: reading the owned connection after teardown returned n=%d err=%v, want net.ErrClosed", n, err)
	}
	entries, returns := f.probe.snapshot()
	if !custodyComplete {
		t.Errorf("control-a: the handler probe reports entries=%d returns=%d, but connection custody did not complete, so a zero count would not be final", entries, returns)
	} else if entries != 0 || returns != 0 {
		t.Errorf("control-a: after a completed connection-custody barrier the handler probe reports entries=%d returns=%d, want 0/0", entries, returns)
	}
	if admitted, _, inside, refused, closed := held.counts(); admitted != 0 || inside != 0 || refused != 0 || !closed {
		t.Errorf("control-a: held-body admission reports admitted=%d inside=%d refused=%d closed=%v, want 0/0/0/true",
			admitted, inside, refused, closed)
	}
	if custodyComplete && observed {
		t.Logf("control-a: the full teardown completed its custody with zero handler invocations and one terminal connection")
	}
}

// TestServeListenerFailureControlEntryRacingCleanup is control B. It keeps the admitted
// held-request case and adds the one the previous control could not reach: the full
// teardown must not be able to finish while a real REFUSED handler invocation is still
// inside a control-owned return barrier. Closing held-body admission does not end that
// invocation; only connection custody joins it.
//
// The control first observes, from inside the cleanup itself, that the teardown reached
// its connection-custody wait — otherwise a merely delayed teardown goroutine would
// produce the same pending window and the same final counts without the wait ever
// happening. Only after that boundary is the finite window taken, and only then is the
// return barrier released as the single changed variable.
func TestServeListenerFailureControlEntryRacingCleanup(t *testing.T) {
	h := newListenerFailureHarness(t)

	// Registered before any fixture, request or goroutine exists. It observes the
	// cleanup reaching its custody wait; it cannot advance or replace that wait.
	custodyReached := make(chan struct{})
	var custodyOnce sync.Once
	h.custodyWaitObserver = func(*listenerFailureServer) {
		custodyOnce.Do(func() { close(custodyReached) })
	}

	held := h.newHeldWork("control-b")

	rejectParked := make(chan struct{})
	rejectRelease := make(chan struct{})
	var parkedOnce, releaseOnce sync.Once
	releaseReject := func() { releaseOnce.Do(func() { close(rejectRelease) }) }

	// A control-specific handler: the same real held-body admission decision, plus a
	// control-owned return barrier on the refusal branch. The refusal answer carries an
	// explicit length and is flushed, so the client can complete while the invocation is
	// still deliberately inside the handler. No production hook is involved.
	f := h.newServer("httpSrv", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if held.hold() {
			_, _ = io.WriteString(w, listenerFailureBody)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(listenerFailureRejected)))
		_, _ = io.WriteString(w, listenerFailureRejected)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		parkedOnce.Do(func() { close(rejectParked) })
		select {
		case <-rejectRelease:
		case <-time.After(listenerFailureHandlerGuard):
		}
	}))
	h.awaitServeStarted(f.label, f.addr, f.started)

	teardownDone := make(chan struct{})
	teardownStarted := false
	// Registered after the harness cleanup, so it runs before it: the return barrier is
	// released and the control's own goroutine joined on every path, including a Fatal.
	t.Cleanup(func() {
		releaseReject()
		if !teardownStarted {
			return
		}
		select {
		case <-teardownDone:
		case <-time.After(listenerFailureJoinTimeout):
			t.Errorf("UNRESOLVED: control-b: the teardown goroutine did not finish within %s", listenerFailureJoinTimeout)
		}
	})

	// 1. The admitted held request, retained from the previous control.
	first := h.startGet("admitted held request", f.addr, listenerFailureHeldClientTimeout)
	select {
	case <-held.entered:
	case <-time.After(listenerFailureReturnBound):
		t.Fatalf("UNRESOLVED: control-b: no invocation was admitted to the held body within %s", listenerFailureReturnBound)
	}

	// 2. Admission closes underneath it, and a second real owned request is refused and
	//    parks inside the control's return barrier.
	held.closeAdmission()
	second := h.startGet("refused request", f.addr, listenerFailureClientTimeout)
	select {
	case <-rejectParked:
	case <-time.After(listenerFailureReturnBound):
		t.Fatalf("UNRESOLVED: control-b: the refused invocation did not reach the control's return barrier within %s", listenerFailureReturnBound)
	}
	if body, err := h.awaitClient(second, listenerFailureJoinTimeout); err != nil {
		t.Errorf("control-b: the refused request did not complete: %v", err)
	} else if body != listenerFailureRejected {
		t.Errorf("control-b: the refused request answered %q, want %q", body, listenerFailureRejected)
	}

	// 3. Release the admitted held body and join its own client.
	held.releaseAll()
	if body, err := h.awaitClient(first, listenerFailureJoinTimeout); err != nil {
		t.Errorf("control-b: the admitted held request did not complete after release: %v", err)
	} else if body != listenerFailureBody {
		t.Errorf("control-b: the admitted held request answered %q, want %q", body, listenerFailureBody)
	}
	if !held.awaitQuiescence(listenerFailureJoinTimeout) {
		t.Errorf("control-b: held-body admission did not quiesce after the single admitted invocation left")
	}

	// 4. The demonstration: held-body quiescence is reached, yet the full teardown must
	//    not be able to finish while the refused invocation is still inside the handler.
	teardownStarted = true
	go func() {
		h.runTeardown()
		close(teardownDone)
	}()

	//    First, observe from inside the cleanup that it actually reached its
	//    connection-custody wait. Without this, a delayed goroutine would satisfy the
	//    window below without the wait ever occurring.
	select {
	case <-custodyReached:
	case <-teardownDone:
		t.Fatalf("control-b: the full teardown completed without ever reaching its connection-custody wait")
	case <-time.After(listenerFailureReturnBound):
		t.Fatalf("UNRESOLVED: control-b: the teardown did not reach its connection-custody wait within %s", listenerFailureReturnBound)
	}

	//    At that boundary the refused invocation must still be outstanding: its
	//    connection cannot have reached StateClosed while its handler is parked, and
	//    exactly one invocation is inside. Both are facts of the server and the probe,
	//    not of the wait being observed.
	registered, terminal, outstanding, hijacked := f.tracker.counts()
	if registered != 2 || outstanding < 1 || hijacked != 0 {
		t.Errorf("control-b: at the connection-custody wait boundary custody reports registered=%d terminal=%d outstanding=%d hijacked=%d, "+
			"want 2 accepted, at least 1 outstanding and none hijacked", registered, terminal, outstanding, hijacked)
	}
	if entries, returns := f.probe.snapshot(); entries != 2 || returns != 1 {
		t.Errorf("control-b: at the connection-custody wait boundary the handler probe reports entries=%d returns=%d, want 2/1", entries, returns)
	}
	t.Logf("control-b: the teardown reached its connection-custody wait with %d of %d accepted connection(s) still outstanding", outstanding, registered)

	//    Only now is the finite window meaningful: the cleanup is provably inside the
	//    wait. The causal fact is the parked barrier, not the length of the window.
	select {
	case <-teardownDone:
		t.Errorf("control-b: the full teardown finished while a refused handler invocation was still inside the control's return barrier")
	case <-time.After(listenerFailurePendingWindow):
		t.Logf("control-b: the full teardown is still pending %s inside its connection-custody wait", listenerFailurePendingWindow)
	}

	// 5. Release that invocation; the teardown must now complete.
	releaseReject()
	select {
	case <-teardownDone:
	case <-time.After(listenerFailureReturnBound):
		t.Fatalf("UNRESOLVED: control-b: the teardown did not finish within %s of releasing the refused invocation", listenerFailureReturnBound)
	}

	// 6. Final facts. They are final because the teardown completed the same
	//    connection-custody wait this control watched it enter, and because that
	//    teardown sealed registration on its own completed Serve join.
	if !f.tracker.isSealed() {
		t.Errorf("control-b: connection registration was left unsealed, so the counts below are observations and not a join")
	}
	if entries, returns := f.probe.snapshot(); entries != 2 || returns != 2 {
		t.Errorf("control-b: the handler probe reports entries=%d returns=%d, want 2/2", entries, returns)
	}
	if admitted, left, inside, refused, closed := held.counts(); admitted != 1 || left != 1 || inside != 0 || refused != 1 || !closed {
		t.Errorf("control-b: held-body admission reports admitted=%d left=%d inside=%d refused=%d closed=%v, want 1/1/0/1/true",
			admitted, left, inside, refused, closed)
	}
	registered, terminal, outstanding, hijacked = f.tracker.counts()
	if registered != 2 || terminal != 2 || outstanding != 0 || hijacked != 0 {
		t.Errorf("control-b: connection custody reports registered=%d terminal=%d outstanding=%d hijacked=%d, want 2/2/0/0",
			registered, terminal, outstanding, hijacked)
	}
	t.Logf("control-b: one admitted invocation was joined, one refused invocation held the full teardown until it returned")
}

// TestServeListenerFailureControlGRPCReadinessPrecedesTrigger is control C. It shows
// that the accept-loop barrier is what makes the gRPC closure sentinel sound, and that
// no product trigger is issued before that real barrier.
//
// Part 1 measures one ordering only: the accept-loop barrier is observed first, and the
// stop that follows yields exactly nil. It does NOT reproduce the pre-barrier stop that
// would return ErrServerStopped; it establishes that this ordering does not. Part 2
// reads the fixture's real barrier inside the control-owned trigger function,
// immediately before calling waitAndShutdown, and keeps the exact real Serve result
// afterwards.
func TestServeListenerFailureControlGRPCReadinessPrecedesTrigger(t *testing.T) {
	h := newListenerFailureHarness(t)
	log := h.logger()

	// Part 1: fixture only, no product involved.
	early := h.newGRPCServer()
	h.awaitServeStarted("gRPC", early.addr, early.started)
	early.srv.GracefulStop()
	h.requireGRPCServeStopped("control-c/part1", early)
	earlyServeErr, earlyObserved := early.observedServeResult()
	if !earlyObserved {
		t.Errorf("control-c: the gRPC Serve completion was not received, so its result must not be read and part 1 establishes nothing")
	} else {
		if errors.Is(earlyServeErr, grpc.ErrServerStopped) {
			t.Errorf("control-c: gRPC Serve returned ErrServerStopped after an observed accept-loop entry; the barrier did not establish the start")
		}
		// Exactly what was measured, and nothing more: this ordering — barrier observed
		// first, stop issued second — yields nil. No pre-barrier stop was executed here.
		t.Logf("control-c: with the accept-loop barrier observed first, the stop that followed yielded Serve=%v, not grpc.ErrServerStopped", earlyServeErr)
	}

	// Part 2: a real product trigger over a proven-started fixture.
	f := h.newServer("httpSrv", http.HandlerFunc(listenerFailureOK))
	h.confirmServing(f)
	g := h.newGRPCServer()
	h.confirmGRPCReady(g)

	// The readiness admission read from the real barrier channels, with no product call
	// on the refused path: nothing here fabricates a pre-barrier history.
	if pending := h.pendingGRPCReadiness(); len(pending) != 0 {
		t.Fatalf("control-c: gRPC fixtures %v had not entered their accept loop before the trigger", pending)
	}

	errCh := make(chan error, 1)
	errCh <- nil // benign trigger: this control is about readiness, not the fatal path

	// barrierObservedAtTrigger is written inside the control-owned function before the
	// product call and read after the call's completion fact, so it is the real barrier
	// state at the trigger boundary and not a value any helper assigned.
	var barrierObservedAtTrigger bool
	start := time.Now()
	call := h.startCall(h.fixtureCtx, "waitAndShutdown(control-c)", func(ctx context.Context) error {
		select {
		case <-g.started:
			barrierObservedAtTrigger = true
		default:
		}
		return waitAndShutdown(ctx, f.srv, g.srv, nil, nil, nil, nil, nil, nil, nil, errCh, log)
	})
	ret := h.awaitCall(call, listenerFailureReturnBound)
	elapsed := time.Since(start)

	if !barrierObservedAtTrigger {
		t.Errorf("control-c: the gRPC fixture's own Accept-entry barrier was not observed at the product-trigger boundary")
	}
	if ret != nil {
		t.Errorf("control-c: waitAndShutdown returned %v, want nil for a benign trigger", ret)
	}
	h.awaitServeClosed("control-c/part2", f, listenerFailureJoinTimeout)
	h.requireRefused("control-c/part2", f, elapsed)
	h.requireGRPCClosed("control-c/part2", g, elapsed)
	if gServeErr, gObserved := g.observedServeResult(); !gObserved {
		t.Errorf("control-c: the gRPC Serve completion after the product stop was not received, so its result must not be read")
	} else {
		t.Logf("control-c: the trigger observed the real barrier and the product's stop left Serve=%v", gServeErr)
	}
}
