// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// ---- the test-owned PostgreSQL wire proxy ----------------------------------
//
// WHY THIS EXISTS AND WHY IT IS NOT A MOCK. The defect C32 answers is a COMMIT
// whose acknowledgement the client never reads. No fake store can produce it:
// the whole question is what the REAL driver, pool and server do when the answer
// is lost between the server writing it and the client reading it. The only
// place that can be arranged without lying about anything is the wire.
//
// So this proxy sits in front of PostgreSQL for the APPLICATION connection only,
// forwards every byte untouched by default, and can do exactly three things on
// request: hold a COMMIT before it is delivered, hold the server's answer to a
// COMMIT after the server has produced it, or cut the connection. The server,
// the pool, the store, the router and the module are all the production ones.
//
// CUSTODY. The log records message TYPE BYTES and CommandComplete TAGS only. It
// never records startup parameters, password frames or SASL frames, and the
// controls assert that. The owner and admin DSNs are NOT proxied — only the app
// DSN is redirected, so provisioning and the cross-tenant reads the controls use
// to observe the database keep a direct connection.
//
// BOUNDS. At most 64 KiB is buffered per connection, a hold lasts at most 10 s,
// and every socket and goroutine is closed in t.Cleanup. The listener is on
// 127.0.0.1 with an ephemeral port.
//
// A deadline in this file is always a HOLD LIMIT. It is never the oracle: when a
// witness cannot be established the control fails with a named COULD_NOT_LOOK.

type commitOutcomeProxyMode int

const (
	// commitOutcomeProxyPass forwards everything. It is the default, so a
	// connection the control never arms behaves as if the proxy were not there.
	commitOutcomeProxyPass commitOutcomeProxyMode = iota
	// commitOutcomeProxyHoldAck forwards the COMMIT and holds the SERVER's answer.
	// The server has already decided; the client will never learn what it decided.
	commitOutcomeProxyHoldAck
	// commitOutcomeProxyHoldCommit does not forward the COMMIT at all. The server
	// never sees it, so nothing can have been applied.
	commitOutcomeProxyHoldCommit
)

const (
	commitOutcomeProxyBufferLimit  = 64 << 10
	commitOutcomeProxyHoldLimit    = 10 * time.Second
	commitOutcomeProxyMaxUnnamed   = 8
	commitOutcomeProxyStartupMagic = 196608
	// commitOutcomeProxyObserveLimit bounds ONE observation call. An outer loop
	// that only checks the wall clock between iterations does not bound the call
	// inside it: a QueryRow that never returns keeps the loop from ever testing
	// its own deadline, and the worker that made it cannot be joined. Every
	// observation therefore carries its own deadline, strictly inside the hold
	// limit, so a stall is reported rather than waited on forever.
	commitOutcomeProxyObserveLimit = 5 * time.Second
	// commitOutcomeProxyWriteLimit bounds ONE socket write. A peer that has
	// stopped reading can otherwise block a held write indefinitely, which is the
	// same failure in the other direction. It is also strictly inside the hold
	// limit; neither constant widens any PRODUCT timing budget, because neither
	// is a product budget.
	commitOutcomeProxyWriteLimit = 5 * time.Second
)

// commitOutcomeProxyEvent is what the control waits for instead of sleeping.
type commitOutcomeProxyEvent struct {
	// kind is "commit_held" or "ack_held".
	kind string
	// pid is the server backend that ran the transaction.
	pid int
	// tag is the CommandComplete tag the SERVER produced for the COMMIT — W in
	// the contract. It is the proof that the server answered at all.
	tag string
}

type commitOutcomeProxy struct {
	t        *testing.T
	listener net.Listener
	upstream string

	// ctx is the proxy's own cancellation owner. Every observation this proxy
	// makes derives from it, so close() can cancel in-flight SQL BEFORE joining
	// workers instead of waiting on a call nothing can interrupt.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	mode     commitOutcomeProxyMode
	identify func(ctx context.Context, pid int) bool
	// armed is the INTERCEPTION PERMIT, and it is deliberately separate from
	// held. held answers "which transaction do I currently own?"; armed answers
	// "may I take one?". Conflating them is how a settled or severed hold lets
	// the NEXT matching COMMIT be intercepted as well: clearing held would
	// silently re-open the permit, and a recovery positive would then be measuring
	// a second interception nobody asked for. The permit is consumed atomically by
	// exactly one identified transaction, including in the HoldAck window where no
	// hold is installed until the server replies.
	armed    bool
	consumed int
	// pids is every backend this proxy has seen announce itself in
	// BackendKeyData. It is the instrument's own evidence of which server
	// backends belong to connections it created, which is what lets a control
	// attribute a resend to a real backend instead of to "some other waiter".
	pids       []int
	unnamed    int
	log        []string
	held       *commitOutcomeProxyHold
	conns      []net.Conn
	closed     bool
	identified bool

	events chan commitOutcomeProxyEvent
	wg     sync.WaitGroup
}

// commitOutcomeProxyHold is one suspended COMMIT or one suspended answer.
type commitOutcomeProxyHold struct {
	pid      int
	commit   []byte   // the COMMIT bytes, for HOLD_COMMIT
	answer   [][]byte // the server messages, for HOLD_ACK
	upstream net.Conn
	client   net.Conn
	session  *commitOutcomeProxySession
	mode     commitOutcomeProxyMode
}

// newCommitOutcomeProxy starts the proxy in front of the server named by appDSN
// and returns it with the rewritten DSN the engine must dial.
func newCommitOutcomeProxy(t *testing.T, appDSN string) (*commitOutcomeProxy, string) {
	t.Helper()
	parsed, err := url.Parse(appDSN)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: the application DSN is not a URL, so it cannot be redirected: %v", err)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("COULD_NOT_LOOK: the application DSN asks for sslmode=%q; this proxy reads cleartext frames only and must never negotiate TLS on a caller's behalf",
			parsed.Query().Get("sslmode"))
	}
	host := parsed.Host
	if !strings.Contains(host, ":") {
		host += ":5432"
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: listen on an ephemeral loopback port: %v", err)
	}
	proxyCtx, proxyCancel := context.WithCancel(context.Background())
	p := &commitOutcomeProxy{
		t: t, listener: listener, upstream: host,
		ctx: proxyCtx, cancel: proxyCancel,
		events: make(chan commitOutcomeProxyEvent, 8),
	}
	redirected := *parsed
	redirected.Host = listener.Addr().String()

	p.wg.Add(1)
	go p.accept()
	t.Cleanup(p.checkRelayCustody)
	t.Cleanup(p.close)
	return p, redirected.String()
}

func (p *commitOutcomeProxy) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	// ORDER IS THE POINT. Cancel first, so an observation still inside a
	// QueryRow returns instead of pinning the worker that made it; then stop
	// accepting; then drop the sockets so every relay read fails; only then
	// join. Joining first is how a stalled observer strands cleanup.
	p.cancel()
	_ = p.listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	p.wg.Wait()
}

func (p *commitOutcomeProxy) track(c net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = c.Close()
		return
	}
	p.conns = append(p.conns, c)
}

func (p *commitOutcomeProxy) accept() {
	defer p.wg.Done()
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.track(client)
		upstream, err := net.DialTimeout("tcp", p.upstream, 10*time.Second)
		if err != nil {
			// One failed dial is not the end of the listener: the pool opens
			// connections for the whole run, and killing the accept loop here would
			// surface as an unexplained engine failure later.
			p.record("upstream_dial_failed")
			_ = client.Close()
			continue
		}
		p.track(upstream)
		p.wg.Add(2)
		session := &commitOutcomeProxySession{p: p, client: client, upstream: upstream}
		go session.clientToServer()
		go session.serverToClient()
	}
}

// recordBackendPID remembers a backend this proxy's own connection announced.
func (p *commitOutcomeProxy) recordBackendPID(pid int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, seen := range p.pids {
		if seen == pid {
			return
		}
	}
	p.pids = append(p.pids, pid)
}

// observedBackendPIDs returns every backend this proxy has seen.
func (p *commitOutcomeProxy) observedBackendPIDs() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.pids...)
}

// arm puts the proxy into mode for the next identified COMMIT. identify answers
// whether a server backend pid is the transaction this control is about; the
// controls implement it as a pg_locks read on the target relation through a
// DIRECT owner connection, never through this proxy.
func (p *commitOutcomeProxy) arm(mode commitOutcomeProxyMode, identify func(ctx context.Context, pid int) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
	p.identify = identify
	p.armed = true
	p.consumed = 0
	p.unnamed = 0
	p.identified = false
}

// armConsumptions reports how many times an interception permit was taken. A
// control that expects one interception asserts this is exactly 1 AFTER the
// recovery request has been answered, which is what distinguishes "the resend
// was forwarded" from "the resend was intercepted again".
func (p *commitOutcomeProxy) armConsumptions() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.consumed
}

// isArmed reports whether a permit is still outstanding.
func (p *commitOutcomeProxy) isArmed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.armed
}

// waitForEvent blocks until the proxy reports a hold, or fails the control with a
// named COULD_NOT_LOOK. It is the handshake that replaces every sleep.
func (p *commitOutcomeProxy) waitForEvent(kind string) commitOutcomeProxyEvent {
	p.t.Helper()
	deadline := time.After(commitOutcomeProxyHoldLimit)
	for {
		select {
		case event := <-p.events:
			if event.kind == kind {
				return event
			}
		case <-deadline:
			p.mu.Lock()
			unnamed, identified := p.unnamed, p.identified
			p.mu.Unlock()
			if unnamed >= commitOutcomeProxyMaxUnnamed {
				p.t.Fatalf("COULD_NOT_LOOK: COMMIT not identified — %d COMMITs passed in this window and none was the target transaction", unnamed)
			}
			p.t.Fatalf("COULD_NOT_LOOK: the proxy never reported %q within %s (identified=%t); the schedule this control needs was not established",
				kind, commitOutcomeProxyHoldLimit, identified)
			return commitOutcomeProxyEvent{}
		}
	}
}

// forward sends a held COMMIT on to the server. It is how a control proves the
// original really was still unresolved: the resend is already waiting on its
// lock when this runs.
func (p *commitOutcomeProxy) forward() {
	p.t.Helper()
	p.mu.Lock()
	held := p.held
	p.held = nil
	p.mu.Unlock()
	if held == nil || held.mode != commitOutcomeProxyHoldCommit {
		p.t.Fatal("COULD_NOT_LOOK: no held COMMIT to forward")
	}
	if err := held.upstream.SetWriteDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		p.t.Fatalf("COULD_NOT_LOOK: bound the held COMMIT write: %v", err)
	}
	if _, err := held.upstream.Write(held.commit); err != nil {
		p.t.Fatalf("COULD_NOT_LOOK: forward the held COMMIT: %v", err)
	}
	if err := held.upstream.SetWriteDeadline(time.Time{}); err != nil {
		p.t.Fatalf("COULD_NOT_LOOK: clear the held COMMIT write deadline: %v", err)
	}
}

// release delivers a held server answer to the client — which by then may be a
// socket the driver has already abandoned. That is the point of the control: the
// answer existed and nobody read it.
func (p *commitOutcomeProxy) release() {
	p.t.Helper()
	p.mu.Lock()
	held := p.held
	p.held = nil
	p.mu.Unlock()
	if held == nil || held.mode != commitOutcomeProxyHoldAck {
		p.t.Fatal("COULD_NOT_LOOK: no held answer to release")
	}
	session := held.session
	if session == nil {
		p.t.Fatal("COULD_NOT_LOOK: the held answer names no session, so it cannot be drained in order")
	}
	// ONE operation, not two. releaseAndDrain takes the output lock BEFORE it
	// clears the holding state and keeps it until the buffered frames are on the
	// wire, so no relay can overtake them.
	drained, err := session.releaseAndDrain()
	p.record("release_drained=" + strconv.Itoa(drained))
	if err != nil {
		// A client that has gone away is a legitimate outcome here, not a fixture
		// failure: it is exactly the state the driver leaves behind when it gives
		// up on a canceled request.
		p.record("release_write_failed")
	}
}

// sever closes both halves, which is what a real reset looks like to the pool.
func (p *commitOutcomeProxy) sever() {
	p.mu.Lock()
	held := p.held
	p.held = nil
	p.mu.Unlock()
	if held != nil {
		_ = held.upstream.Close()
		_ = held.client.Close()
		return
	}
	p.mu.Lock()
	conns := append([]net.Conn(nil), p.conns...)
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *commitOutcomeProxy) record(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.log) < 4096 {
		p.log = append(p.log, line)
	}
}

// custodyLog is the whole log, for the control that asserts no credential
// material was ever written to it.
func (p *commitOutcomeProxy) custodyLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.log...)
}

type commitOutcomeProxySession struct {
	p        *commitOutcomeProxy
	client   net.Conn
	upstream net.Conn

	// writeMu owns the ORDER of everything written to the client. The release
	// drain and the ordinary relay both take it, so a frame that arrives while a
	// drain is in flight is written after the drained frames and never inside
	// them. Without it "release then resume" is not a transition at all: it is
	// two writers racing for the same socket.
	writeMu sync.Mutex

	mu  sync.Mutex
	pid int
	// holdingAnswer buffers server frames after a held COMMIT answer, so the
	// client sees nothing at all rather than a truncated exchange. It is cleared
	// by exactly one place — releaseAndDrain — together with the buffer it guards.
	holdingAnswer bool
	buffered      [][]byte
	bufferedBytes int

	// onDrainLocked, when set by an instrument, runs INSIDE releaseAndDrain with
	// writeMu held and the holding state already cleared, before any buffered
	// frame is written. It is nil in every ordinary path.
	onDrainLocked func()
	// These optional instrument callbacks distinguish frame arrival, attempting
	// the output lock, and completion of the socket write. A pre-lock attempt is
	// not evidence that a write has completed. Install and read them under mu;
	// callbacks run after mu is released and must not block the relay.
	onRelayFrame         func(byte)
	onRelayWriteAttempt  func()
	onRelayWriteComplete func(error)
	// relay is the evidence a failed write is classified from.
	relay commitOutcomeRelayState
}

// bufferedFrames reports how many frames the session is currently withholding.
// A control uses it to ACKNOWLEDGE that a frame really was buffered, instead of
// assuming it from the order in which the control sent things.
func (s *commitOutcomeProxySession) bufferedFrames() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buffered)
}

// heldSession returns the session whose answer is currently held, if any.
func (p *commitOutcomeProxy) heldSession() *commitOutcomeProxySession {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held == nil {
		return nil
	}
	return p.held.session
}

// releaseAndDrain is THE synchronized release transition, and it is one step on
// purpose.
//
// The defect it replaces cleared the proxy-level hold and left the session still
// buffering. A ReadyForQuery that arrived after the CommandComplete — which is
// the ordinary server ordering, not a rare one — then met "holding, buffer not
// empty", was appended to a buffer nobody would drain, and had nowhere to be
// published because the hold it would have been published to was already gone.
// The client was left one frame short of a usable connection, so a CORRECT
// product could hang the positive that is supposed to prove it recovered.
//
// This drains the buffer in wire order, clears the holding state and resumes
// forwarding, under one lock each, in that order. After it returns the session
// is an ordinary relay again.
// THE ONE LOCK ORDER IN THIS FILE IS writeMu BEFORE mu.
//
// Nothing may hold mu and then ask for writeMu. Every write to the client goes
// through writeLocked, which assumes writeMu is already held, so the release
// drain and the ordinary relay share one bounded low-level writer and no path
// locks recursively.
func (s *commitOutcomeProxySession) releaseAndDrain() (int, error) {
	// writeMu FIRST, and held across the state change. The defect this replaces
	// cleared holdingAnswer under mu and only then asked for writeMu: in that gap a
	// relay carrying ReadyForQuery saw no hold, won writeMu, and put Z on the wire
	// AHEAD of the buffered CommandComplete. Every individual write was serialized
	// and the wire order was still wrong, which is exactly the failure a
	// per-write mutex cannot catch.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	frames := s.buffered
	s.buffered = nil
	s.bufferedBytes = 0
	s.holdingAnswer = false
	hook := s.onDrainLocked
	s.mu.Unlock()

	if hook != nil {
		// The instrument's chance to let a competing frame reach the write path
		// while this transition still owns the output lock.
		hook()
	}
	return len(frames), s.writeLocked(frames...)
}

// writeClient is the relay's path to the client socket: take the output lock,
// then write. It is the only other caller of writeLocked.
func (s *commitOutcomeProxySession) writeClient(frames ...[]byte) error {
	s.mu.Lock()
	attempt, complete := s.onRelayWriteAttempt, s.onRelayWriteComplete
	s.mu.Unlock()
	if attempt != nil {
		attempt()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err := s.writeLocked(frames...)
	if complete != nil {
		complete(err)
	}
	return err
}

// writeLocked is the bounded low-level writer. PRECONDITION: writeMu is held.
// The deadline is the S3 half — a connected peer that stopped reading must not
// be able to pin this goroutine past the instrument's own limit.
func (s *commitOutcomeProxySession) writeLocked(frames ...[]byte) error {
	for _, frame := range frames {
		if err := s.client.SetWriteDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
			return err
		}
		if _, err := s.client.Write(frame); err != nil {
			return err
		}
	}
	// Leave no deadline behind on a socket that stays in service.
	return s.client.SetWriteDeadline(time.Time{})
}

func (s *commitOutcomeProxySession) backendPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

// clientToServer relays frontend messages. The startup message has no type byte,
// which is why it is read separately and forwarded without inspection: its
// payload is the only place credential-adjacent parameters appear, and this proxy
// does not look at it.
func (s *commitOutcomeProxySession) clientToServer() {
	defer s.p.wg.Done()
	// THE UPSTREAM CONNECTION OUTLIVES THE CLIENT WHEN A COMMIT IS HELD, and that
	// is load-bearing rather than tidy. pgx closes its side of a connection
	// asynchronously when a context is canceled while it is reading a reply
	// (pgconn asyncClose), so on every hold schedule the client socket goes away
	// FIRST. Closing the server socket with it would end the server's transaction
	// and release the very locks the resend is supposed to wait on — the schedule
	// would collapse into "no contention" and the control would report a witness it
	// never saw. While a hold is pending the server side is retained, and only
	// forward() or sever() decides its fate.
	defer s.closeUpstreamUnlessHeld()

	if err := s.relayStartup(); err != nil {
		// A CancelRequest or an SSLRequest is refused here by design; the startup
		// is inspected, so it stays within the bound.
		s.relayStopped(commitOutcomeRelayFromClient, "startup_not_relayed", nil)
		return
	}
	// buf holds one frame this relay may inspect. A larger frame is streamed
	// through it, so nothing here is ever allocated at a length a peer declared.
	buf := make([]byte, 1+commitOutcomeProxyBufferLimit)
	for {
		var header [5]byte
		kind, body, end := commitOutcomeRelayHeader(s.client, &header)
		if end != "" {
			s.relayStopped(commitOutcomeRelayFromClient, end, nil)
			return
		}
		if body+4 > commitOutcomeProxyBufferLimit {
			// Too large to be the COMMIT this proxy looks for: carried, never
			// inspected.
			if end, err := s.streamToServer(header[:], body, buf); end != "" || err != nil {
				s.relayStopped(commitOutcomeRelayFromClient, end, err)
				return
			}
			continue
		}
		frame, end := commitOutcomeRelayWhole(s.client, header[:], body, buf)
		if end != "" {
			s.relayStopped(commitOutcomeRelayFromClient, end, nil)
			return
		}
		if kind == 'Q' && commitOutcomeIsCommitQuery(frame[5:]) {
			if s.handleCommit(frame) {
				continue
			}
		}
		if err := s.writeServer(frame); err != nil {
			s.relayStopped(commitOutcomeRelayFromClient, "", err)
			return
		}
	}
}

// relayStartup forwards the untyped startup message, and the typed frames of the
// authentication exchange, without recording any of their bytes.
func (s *commitOutcomeProxySession) relayStartup() error {
	var header [4]byte
	if _, err := io.ReadFull(s.client, header[:]); err != nil {
		return err
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length < 8 || length > commitOutcomeProxyBufferLimit {
		return fmt.Errorf("startup message length %d is outside the proxy's bounds", length)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(s.client, body); err != nil {
		return err
	}
	if version := int(binary.BigEndian.Uint32(body[:4])); version != commitOutcomeProxyStartupMagic {
		// An SSLRequest or GSSENCRequest would arrive here. The proxy refuses
		// rather than negotiating anything on a caller's behalf.
		return fmt.Errorf("unexpected startup version %d: this proxy reads cleartext protocol 3.0 only", version)
	}
	s.p.record("startup_forwarded")
	if _, err := s.upstream.Write(append(header[:], body...)); err != nil {
		return err
	}
	return nil
}

// handleCommit returns true when the frame was consumed by a hold.
func (s *commitOutcomeProxySession) handleCommit(frame []byte) bool {
	p := s.p
	pid := s.backendPID()

	p.mu.Lock()
	mode, identify, armed := p.mode, p.identify, p.armed
	p.mu.Unlock()
	// THE PERMIT IS CHECKED FIRST. Once it has been consumed, a later matching
	// COMMIT — the recovery resend, on the very same route and backend — is an
	// ordinary forward. Reading held instead would re-open interception the moment
	// the first transaction was settled or severed.
	if !armed || mode == commitOutcomeProxyPass || identify == nil || pid == 0 {
		p.record("commit_forwarded_unarmed")
		return false
	}
	// The observation runs WITHOUT the proxy lock and with its own deadline, so a
	// stalled catalog read cannot freeze every other connection's relay.
	observeCtx, cancelObserve := context.WithTimeout(p.ctx, commitOutcomeProxyObserveLimit)
	identified := identify(observeCtx, pid)
	cancelObserve()
	if !identified {
		p.mu.Lock()
		p.unnamed++
		p.mu.Unlock()
		p.record("commit_forwarded_unidentified")
		return false
	}

	p.mu.Lock()
	// Re-check under the lock and CONSUME in the same critical section: two
	// connections can reach this point together, and exactly one may take the
	// permit. Consumption happens here, for HoldAck as well as HoldCommit, which
	// closes the window where HoldAck has armed the session but the server has not
	// yet replied and no hold exists to stand in for the permit.
	if !p.armed {
		p.mu.Unlock()
		p.record("commit_forwarded_permit_taken")
		return false
	}
	p.armed = false
	p.consumed++
	p.identified = true
	switch mode {
	case commitOutcomeProxyHoldCommit:
		p.held = &commitOutcomeProxyHold{
			pid: pid, commit: append([]byte(nil), frame...),
			upstream: s.upstream, client: s.client, session: s, mode: mode,
		}
		p.mu.Unlock()
		p.record("commit_held")
		p.emit(commitOutcomeProxyEvent{kind: "commit_held", pid: pid})
		return true
	case commitOutcomeProxyHoldAck:
		p.mu.Unlock()
		s.mu.Lock()
		s.holdingAnswer = true
		s.mu.Unlock()
		p.record("commit_forwarded_answer_armed")
		return false
	default:
		p.mu.Unlock()
		return false
	}
}

func (p *commitOutcomeProxy) emit(event commitOutcomeProxyEvent) {
	select {
	case p.events <- event:
	default:
		p.record("event_dropped_" + event.kind)
	}
}

// serverToClient relays backend messages, recording only the type byte and, for
// CommandComplete, the tag.
// A frame above the retention bound is streamed through streamToClient.
func (s *commitOutcomeProxySession) serverToClient() {
	defer s.p.wg.Done()
	// The client side is retained for the same reason, in the other direction:
	// release() must be able to deliver a held answer to the socket the driver
	// abandoned, because "the answer existed and nobody read it" is the fact
	// HC-1b measures.
	defer s.closeClientUnlessHeld()

	buf := make([]byte, 1+commitOutcomeProxyBufferLimit)
	for {
		var header [5]byte
		kind, body, end := commitOutcomeRelayHeader(s.upstream, &header)
		if end != "" {
			s.relayStopped(commitOutcomeRelayFromServer, end, nil)
			return
		}
		if body+4 > commitOutcomeProxyBufferLimit {
			if end, err := s.streamToClient(kind, header[:], body, buf); end != "" || err != nil {
				s.relayStopped(commitOutcomeRelayFromServer, end, err)
				return
			}
			continue
		}
		frame, end := commitOutcomeRelayWhole(s.upstream, header[:], body, buf)
		if end != "" {
			s.relayStopped(commitOutcomeRelayFromServer, end, nil)
			return
		}
		payload := frame[5:]
		s.mu.Lock()
		arrived := s.onRelayFrame
		s.mu.Unlock()
		if arrived != nil {
			arrived(kind)
		}
		switch kind {
		case 'K':
			// BackendKeyData: the backend pid is how a COMMIT is attributed to a
			// transaction. The secret key that follows is never recorded.
			if len(payload) >= 4 {
				s.mu.Lock()
				s.pid = int(binary.BigEndian.Uint32(payload[:4]))
				pid := s.pid
				s.mu.Unlock()
				s.p.recordBackendPID(pid)
				s.p.record("backend_pid=" + strconv.Itoa(pid))
			}
		case 'C':
			tag := commitOutcomeCommandTag(payload)
			s.p.record("command_complete=" + tag)
			if strings.EqualFold(tag, "COMMIT") && s.armedForAnswer() {
				s.holdAnswer(frame, tag)
				continue
			}
		default:
			s.p.record("frame=" + string(rune(kind)))
		}
		if s.bufferIfHolding(frame) {
			continue
		}
		// writeClient serializes with an in-flight release drain, so a frame that
		// arrives mid-drain lands AFTER the drained frames rather than inside them.
		if err := s.writeClient(frame); err != nil {
			s.relayStopped(commitOutcomeRelayFromServer, "", err)
			return
		}
	}
}

// closeUpstreamUnlessHeld and closeClientUnlessHeld keep a held connection open.
func (s *commitOutcomeProxySession) closeUpstreamUnlessHeld() {
	if s.p.holdsConn(s.upstream) {
		s.p.record("client_gone_upstream_retained")
		return
	}
	_ = s.upstream.Close()
}

func (s *commitOutcomeProxySession) closeClientUnlessHeld() {
	if s.p.holdsConn(s.client) {
		s.p.record("server_gone_client_retained")
		return
	}
	_ = s.client.Close()
}

func (p *commitOutcomeProxy) holdsConn(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held != nil && (p.held.upstream == c || p.held.client == c)
}

func (s *commitOutcomeProxySession) armedForAnswer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holdingAnswer && len(s.buffered) == 0
}

// holdAnswer captures the server's decision. W — the CommandComplete tag the
// server produced — is reported to the control, because it is the only proof
// that the server answered before the client stopped listening.
func (s *commitOutcomeProxySession) holdAnswer(frame []byte, tag string) {
	p := s.p
	s.mu.Lock()
	s.buffered = append(s.buffered, append([]byte(nil), frame...))
	s.bufferedBytes += len(frame)
	frames := append([][]byte(nil), s.buffered...)
	pid := s.pid
	s.mu.Unlock()

	p.mu.Lock()
	p.held = &commitOutcomeProxyHold{
		pid: pid, answer: frames, upstream: s.upstream, client: s.client,
		session: s, mode: commitOutcomeProxyHoldAck,
	}
	p.mu.Unlock()
	p.record("ack_held=" + tag)
	p.emit(commitOutcomeProxyEvent{kind: "ack_held", pid: pid, tag: tag})
}

// bufferIfHolding keeps everything after a held answer out of the client's view,
// within the proxy's byte bound. The session lock is released before the
// proxy-level hold is updated: the two are never held at once, in either order.
func (s *commitOutcomeProxySession) bufferIfHolding(frame []byte) bool {
	s.mu.Lock()
	if !s.holdingAnswer || len(s.buffered) == 0 {
		s.mu.Unlock()
		return false
	}
	if s.bufferedBytes+len(frame) > commitOutcomeProxyBufferLimit {
		s.mu.Unlock()
		s.p.record("buffer_limit_reached")
		return true
	}
	s.buffered = append(s.buffered, append([]byte(nil), frame...))
	s.bufferedBytes += len(frame)
	frames := append([][]byte(nil), s.buffered...)
	s.mu.Unlock()
	s.p.publishAnswerFrames(frames)
	return true
}

// publishAnswerFrames keeps the proxy-level hold in step with the session buffer,
// so release() writes every frame the client never saw.
func (p *commitOutcomeProxy) publishAnswerFrames(frames [][]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held != nil && p.held.mode == commitOutcomeProxyHoldAck {
		p.held.answer = frames
	}
}

// ---- relay framing --------------------------------------------------------
//
// The retention bound limits what this proxy INSPECTS or WITHHOLDS: a COMMIT
// candidate, a held answer, the frames buffered behind it. It is not a limit on
// what the proxy may CARRY. A server frame larger than the bound is ordinary —
// a catalog read of a long function body produces one — and refusing it ended
// the connection mid-exchange, which the driver reported as an unexpected EOF
// that read like a protocol failure of the server. A frame above the bound is
// streamed instead: its header, then its body in chunks through the relay's own
// bounded buffer, never allocated at the length the peer declared.

// The two relay directions, as the custody log names them.
const (
	commitOutcomeRelayFromClient = "from_client"
	commitOutcomeRelayFromServer = "from_server"
)

// commitOutcomeRelayHeader reads the next typed frame header into header and
// returns its type byte and body length. The declared length is checked BEFORE
// anything subtracts from it: a length below 4 cannot even cover itself, and it
// is named rather than turned into a body length.
func commitOutcomeRelayHeader(conn net.Conn, header *[5]byte) (kind byte, body int64, end string) {
	if n, err := io.ReadFull(conn, header[:]); err != nil {
		return 0, 0, commitOutcomeRelayReadEnd(n > 0, err)
	}
	length := int64(binary.BigEndian.Uint32(header[1:]))
	if length < 4 {
		return 0, 0, "frame_length_below_4"
	}
	return header[0], length - 4, ""
}

// commitOutcomeRelayWhole reads the body of a frame within the bound into buf
// and returns the complete frame. The frame aliases buf: every path that keeps
// a frame copies it, and every write of it finishes before the next read reuses
// buf.
func commitOutcomeRelayWhole(conn net.Conn, header []byte, body int64, buf []byte) ([]byte, string) {
	frame := buf[:5+body]
	copy(frame, header)
	if _, err := io.ReadFull(conn, frame[5:]); err != nil {
		return nil, commitOutcomeRelayReadEnd(true, err)
	}
	return frame, ""
}

// commitOutcomeRelayBody moves body bytes from src to write in chunks no larger
// than buf. Each chunk read carries the same limit the writes do, so a peer
// that stops in the middle of a frame cannot pin the relay, or the output lock
// it may hold, indefinitely. When the frame cannot be carried whole it returns
// the named read outcome, or the write error, which only relayStopped can
// classify.
func commitOutcomeRelayBody(src net.Conn, body int64, buf []byte, write func([]byte) error) (string, error) {
	for body > 0 {
		chunk := buf
		if int64(len(chunk)) > body {
			chunk = chunk[:body]
		}
		if err := src.SetReadDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
			return commitOutcomeRelayReadEnd(true, err), nil
		}
		if _, err := io.ReadFull(src, chunk); err != nil {
			return commitOutcomeRelayReadEnd(true, err), nil
		}
		if err := write(chunk); err != nil {
			return "", err
		}
		body -= int64(len(chunk))
	}
	// The frame is whole; only a socket the proxy already closed refuses this.
	if err := src.SetReadDeadline(time.Time{}); err != nil {
		return commitOutcomeRelayReadEnd(false, err), nil
	}
	return "", nil
}

// streamToServer carries one client frame too large to be the COMMIT this proxy
// inspects: header first, then body chunk by body chunk.
func (s *commitOutcomeProxySession) streamToServer(header []byte, body int64, buf []byte) (string, error) {
	if err := s.writeServer(header); err != nil {
		return "", err
	}
	return commitOutcomeRelayBody(s.client, body, buf, s.writeServer)
}

// streamToClient carries one server frame too large to retain, recording only
// its type byte. While an answer is held it is refused exactly as
// bufferIfHolding refuses a frame that would take the held total past the
// bound: drained from the server and never delivered. Otherwise it goes out
// header first and then chunk by chunk, all under the output lock and the
// ordinary write deadline, so no other writer — a release drain included — can
// land inside it.
func (s *commitOutcomeProxySession) streamToClient(kind byte, header []byte, body int64, buf []byte) (string, error) {
	s.mu.Lock()
	arrived := s.onRelayFrame
	s.mu.Unlock()
	if arrived != nil {
		arrived(kind)
	}
	s.p.record("frame=" + string(rune(kind)))
	if s.withholding() {
		s.p.record("buffer_limit_reached")
		return commitOutcomeRelayBody(s.upstream, body, buf, func([]byte) error { return nil })
	}
	s.mu.Lock()
	attempt, complete := s.onRelayWriteAttempt, s.onRelayWriteComplete
	s.mu.Unlock()
	if attempt != nil {
		attempt()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var end string
	err := s.writeLocked(header)
	if err == nil {
		end, err = commitOutcomeRelayBody(s.upstream, body, buf, func(chunk []byte) error {
			return s.writeLocked(chunk)
		})
	}
	if complete != nil {
		result := err
		if result == nil && end != "" {
			result = errors.New("relay " + end)
		}
		complete(result)
	}
	return end, err
}

// withholding reports whether frames are being kept from the client behind a
// held answer, the state in which bufferIfHolding decides what is retained.
func (s *commitOutcomeProxySession) withholding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holdingAnswer && len(s.buffered) > 0
}

// writeServer is the relay's bounded path to the server, the counterpart of
// writeLocked: a server that stopped reading cannot pin the relay past the
// instrument's limit, and no deadline is left behind. Only the client-to-server
// relay calls it.
func (s *commitOutcomeProxySession) writeServer(chunk []byte) error {
	if err := s.upstream.SetWriteDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		return err
	}
	if _, err := s.upstream.Write(chunk); err != nil {
		return err
	}
	return s.upstream.SetWriteDeadline(time.Time{})
}

// commitOutcomeRelayReadEnd names a failed read from what the read itself
// returned. net.ErrClosed, and io.ErrClosedPipe on a pipe's read side, mean THIS
// proxy closed the socket: local teardown, never evidence about the peer. Any
// failure inside a frame tore it. Between frames, io.EOF is the peer's own
// orderly close and ECONNRESET its reset, both observed here; anything else is
// not evidence of either and stays an inability.
func commitOutcomeRelayReadEnd(inFrame bool, err error) string {
	switch {
	case errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe):
		return "local_close"
	case inFrame:
		return "short_read"
	case errors.Is(err, io.EOF):
		return "peer_closed"
	case errors.Is(err, syscall.ECONNRESET):
		return "peer_reset"
	default:
		return "read_failed"
	}
}

// commitOutcomeRelayWriteEnd names a failed write. THE RULE: an errno is never
// taken as proof of why a peer went away, and a local close is never taken as
// the peer's. seen is how the destination socket's read side stopped, as the
// direction reading that same socket observed it (relayStopped, writeEvidence).
//
//   - errors.Is(err, net.ErrClosed): this proxy closed the socket — "local_close",
//     an end of teardown or of sever(), not a peer closure.
//   - errors.As(err, &netErr) && netErr.Timeout(): a connected peer stopped
//     reading — "write_deadline", an inability.
//   - seen == "peer_closed": that socket had returned io.EOF at a frame boundary
//     on the proxy's own read side — "peer_closed", whatever the write returned.
//   - errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET), with
//     seen == "peer_reset" (ECONNRESET between frames on that read side): the
//     peer reset the connection — "peer_closed".
//   - anything else, EPIPE or ECONNRESET without that observation included:
//     "short_write", an inability.
//
// "peer_closed" says the peer ended its socket, as observed; it does not say
// why, so it is no evidence that a driver closed on purpose.
func commitOutcomeRelayWriteEnd(err error, seen string) string {
	var netErr net.Error
	reset := errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
	switch {
	case errors.Is(err, net.ErrClosed):
		return "local_close"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "write_deadline"
	case seen == "peer_closed":
		return "peer_closed"
	case reset && seen == "peer_reset":
		return "peer_closed"
	default:
		return "short_write"
	}
}

// commitOutcomeRelayState is the evidence one session's two directions share.
// reads[i] is how socket i's read side stopped — 0 the client socket, read by
// clientToServer; 1 the server socket, read by serverToClient — written once by
// that direction as it stops, "" when it stopped on a write; done[i] closes at
// the same moment. Guarded by the session's mu.
type commitOutcomeRelayState struct {
	reads [2]string
	done  [2]chan struct{}
}

// commitOutcomeRelaySides returns the socket a direction reads and the socket it
// writes: 0 is the client socket, 1 the server socket.
func commitOutcomeRelaySides(direction string) (reads, writes int) {
	if direction == commitOutcomeRelayFromClient {
		return 0, 1
	}
	return 1, 0
}

// relayDoneLocked returns the channel that closes when side's read side has
// stopped. PRECONDITION: s.mu is held.
func (s *commitOutcomeProxySession) relayDoneLocked(side int) chan struct{} {
	if s.relay.done[side] == nil {
		s.relay.done[side] = make(chan struct{})
	}
	return s.relay.done[side]
}

// relayStopped is the one exit of a relay direction. It first publishes how
// this direction's own read side stopped, which is the evidence the other
// direction's failed write is classified from, and only then names its own
// stop: a read outcome as it is, a failed write by commitOutcomeRelayWriteEnd.
// Publishing before waiting means two directions that both failed a write
// never wait on each other.
func (s *commitOutcomeProxySession) relayStopped(direction, readEnd string, writeErr error) {
	own, dst := commitOutcomeRelaySides(direction)
	s.mu.Lock()
	s.relay.reads[own] = readEnd
	close(s.relayDoneLocked(own))
	s.mu.Unlock()
	if writeErr != nil {
		readEnd = commitOutcomeRelayWriteEnd(writeErr, s.writeEvidence(dst, writeErr))
	}
	s.relayEnded(direction, readEnd)
}

// writeEvidence returns how side's read side stopped, as the direction reading
// that socket observed it. A write refused with EPIPE or ECONNRESET can come a
// moment before that direction observes the same peer, so for those two errors
// only it waits for that read side to stop — never past the instrument's write
// limit, and never past the proxy's own close — before answering. Every other
// write error is classified from what was already observed.
func (s *commitOutcomeProxySession) writeEvidence(side int, err error) string {
	s.mu.Lock()
	seen, done := s.relay.reads[side], s.relayDoneLocked(side)
	s.mu.Unlock()
	if seen != "" || !(errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)) {
		return seen
	}
	timer := time.NewTimer(commitOutcomeProxyWriteLimit)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-s.p.ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.relay.reads[side]
}

// relayEnded names why one relay direction stopped; the direction then closes
// as it always has. Only a direction and a reason are recorded, never frame
// bytes, and past the log's ordinary cap, because the end-of-test custody check
// reads them. A peer observed ending its socket, the proxy's own close, and a
// refused startup are ends. Any other reason means a frame could not be carried
// whole, so the connection loss that follows is this instrument's inability and
// not a protocol verdict about either peer: the owning test's log says
// COULD_NOT_LOOK beside the same custody entry, and checkRelayCustody fails that
// test unless it declared exactly this inability.
func (s *commitOutcomeProxySession) relayEnded(direction, reason string) {
	switch reason {
	case "peer_closed", "peer_reset", "local_close", "startup_not_relayed":
		s.p.recordRelay("relay_end=" + direction + ":" + reason)
	default:
		entry := "relay_error=" + direction + ":" + reason
		s.p.recordRelay(entry)
		s.p.t.Logf("COULD_NOT_LOOK: %s: the test proxy could not carry a frame whole and closed the connection", entry)
	}
}

// commitOutcomeRelayExpected prefixes a declaration in the custody log.
const commitOutcomeRelayExpected = "expect_relay_error="

// recordRelay appends a relay end, a relay inability or a declaration to the
// custody log even past its ordinary cap: checkRelayCustody reads these, so
// none may be dropped. There are at most two relay entries per connection.
func (p *commitOutcomeProxy) recordRelay(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = append(p.log, line)
}

// expectRelayError declares that this test causes exactly one relay inability
// named entry, "<direction>:<reason>" as it appears after "relay_error=". It is
// for controls whose purpose is to cause that inability; declare BEFORE causing
// it. Each declaration is consumed by one later occurrence of exactly the same
// entry.
func (p *commitOutcomeProxy) expectRelayError(entry string) {
	p.recordRelay(commitOutcomeRelayExpected + entry)
}

// checkRelayCustody is the end-of-test custody check. newCommitOutcomeProxy
// registers it immediately BEFORE close, and cleanup runs last-in first-out, so
// it runs after close() has joined every relay goroutine: nothing can record a
// relay inability after it has looked. Walking the log in order, each
// relay_error entry consumes one earlier declaration of exactly the same entry.
// An undeclared occurrence, an occurrence beyond the declared count, and a
// declaration nothing consumed each fail the owning test.
func (p *commitOutcomeProxy) checkRelayCustody() {
	var declared []string
	pending := make(map[string]int)
	for _, line := range p.custodyLog() {
		if entry, ok := strings.CutPrefix(line, commitOutcomeRelayExpected); ok {
			declared = append(declared, entry)
			pending[entry]++
			continue
		}
		entry, ok := strings.CutPrefix(line, "relay_error=")
		if !ok {
			continue
		}
		if pending[entry] > 0 {
			pending[entry]--
			continue
		}
		p.t.Errorf("COULD_NOT_LOOK: relay_error=%s was recorded and this test declared no such inability, or declared fewer: the test proxy could not carry a frame whole, so this result is not a verdict about the product",
			entry)
	}
	for _, entry := range declared {
		if pending[entry] > 0 {
			pending[entry]--
			p.t.Errorf("COULD_NOT_LOOK: relay_error=%s was declared and never recorded: the inability this test exists to cause did not occur",
				entry)
		}
	}
}

// commitOutcomeReadTypedFrame reads one typed protocol message and returns the
// complete frame, its type byte and its payload. The fake server and client
// use it; the relay itself reads through commitOutcomeRelayHeader.
func commitOutcomeReadTypedFrame(conn net.Conn) (frame []byte, kind byte, payload []byte, err error) {
	var header [5]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, 0, nil, err
	}
	length := int(binary.BigEndian.Uint32(header[1:]))
	if length < 4 || length > commitOutcomeProxyBufferLimit {
		return nil, 0, nil, fmt.Errorf("frame length %d is outside the proxy's 64 KiB bound", length)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, 0, nil, err
	}
	frame = append(append([]byte(nil), header[:]...), body...)
	return frame, header[0], body, nil
}

// commitOutcomeIsCommitQuery answers whether a simple Query frame is the COMMIT
// this boundary is about. pgx issues it as a simple query, so the text is the
// whole payload up to its terminator.
func commitOutcomeIsCommitQuery(payload []byte) bool {
	text := strings.TrimSpace(strings.TrimRight(string(payload), "\x00"))
	text = strings.TrimSuffix(strings.TrimSpace(text), ";")
	return strings.EqualFold(strings.TrimSpace(text), "commit")
}

func commitOutcomeCommandTag(payload []byte) string {
	return strings.TrimRight(string(payload), "\x00")
}

// ---- estate wiring --------------------------------------------------------

// commitOutcomeProxiedPostgresEstate boots the ordinary PostgreSQL HTTP estate
// with ONE change: the application DSN points at the proxy. The owner and admin
// DSNs stay direct, so provisioning, the posture check and every observation the
// controls make reach the server without passing through the instrument.
func commitOutcomeProxiedPostgresEstate(
	t *testing.T,
) (communicationHTTPTestStore, *commitOutcomeProxy, enginetest.DSNs) {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		required, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("OLIVARES_TEST_POSTGRES_REQUIRED")))
		if err == nil && required {
			t.Fatal("the PostgreSQL commit-outcome journey is required for this run and no isolated server is available")
		}
		t.Skipf("set %s to run the PostgreSQL commit-outcome journey", enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	proxy, proxiedApp := newCommitOutcomeProxy(t, pg.App)
	dataDir := t.TempDir()
	estate := communicationHTTPTestStore{
		dataDir:      dataDir,
		engine:       "postgres",
		dsnFile:      writeCommunicationHTTPTestDSNFile(t, dataDir, "app.dsn", proxiedApp),
		ownerDSNFile: writeCommunicationHTTPTestDSNFile(t, dataDir, "owner.dsn", pg.Owner),
		adminDSNFile: writeCommunicationHTTPTestDSNFile(t, dataDir, "admin.dsn", pg.Admin),
	}
	// The posture check runs against the same three references the engine will
	// use, so a proxy that broke the application role's posture is caught here
	// rather than surfacing as an unexplained control failure later.
	assertCommunicationHTTPTestPostgresPosture(t, estate, pg.Database)
	return estate, proxy, pg
}

// commitOutcomeDirectConn opens a DIRECT observation channel that does not pass
// through the proxy.
//
// WHICH ROLE OBSERVES WHAT, because getting this wrong would manufacture a
// witness rather than read one:
//
//   - pg_locks and the EXISTENCE of a pg_stat_activity row are visible to any
//     role, so lock waits and backend settlement are read through the OWNER DSN.
//   - tenant rows and audit_events carry FORCE row-level security, which applies
//     to the table owner too, so xmin and row presence are read through the ADMIN
//     DSN (the BYPASSRLS cross-tenant read role).
//
// A per-backend column of pg_stat_activity such as xact_start is NULL for a
// backend owned by another role, so no control here reads one: settlement is the
// disappearance of the row, which is a fact every role can see.
func commitOutcomeDirectConn(t *testing.T, dsn, role string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: open the direct %s connection: %v", role, err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// commitOutcomeIdentifyByRelation is the COMMIT attribution the contract
// specifies: a backend is the target transaction when it holds RowExclusiveLock
// on the route's own relation. It reads through the direct owner connection.
func commitOutcomeIdentifyByRelation(
	t *testing.T, owner *sql.DB, relation string,
) func(ctx context.Context, pid int) bool {
	t.Helper()
	return func(ctx context.Context, pid int) bool {
		var held int
		err := owner.QueryRowContext(ctx, `
			SELECT count(*) FROM pg_locks l
			JOIN pg_class c ON c.oid = l.relation
			WHERE l.pid = $1 AND l.mode = 'RowExclusiveLock' AND c.relname = $2`,
			pid, relation).Scan(&held)
		return err == nil && held > 0
	}
}

// commitOutcomeObserve runs ONE bounded observation.
//
// It exists because a polling loop that only consults the wall clock between
// iterations bounds nothing: the call inside it can block past the loop's own
// deadline, and then the deadline is never even evaluated. Every observation in
// these controls goes through here, so a stall is an answer — a named inability
// with the worker released — rather than a hang.
func commitOutcomeObserve(
	ctx context.Context, scan func(context.Context) error,
) error {
	return commitOutcomeObserveBy(ctx, time.Now().Add(commitOutcomeProxyObserveLimit), scan)
}

// commitOutcomeObserveBy bounds one observation by the NEARER of the per-call
// ceiling and an outer deadline. A fresh full-length timeout on the last call of
// a polling loop can otherwise push it past the window that loop promised.
func commitOutcomeObserveBy(
	ctx context.Context, outer time.Time, scan func(context.Context) error,
) error {
	limit := time.Now().Add(commitOutcomeProxyObserveLimit)
	if outer.Before(limit) {
		limit = outer
	}
	callCtx, cancel := context.WithDeadline(ctx, limit)
	defer cancel()
	return scan(callCtx)
}

// commitOutcomePoll repeats a bounded observation until it answers true or the
// owned deadline expires. It returns false rather than failing, so each caller
// names its own inability in its own words.
func commitOutcomePoll(
	ctx context.Context, limit time.Duration, step func(context.Context) (bool, error),
) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		var done bool
		err := commitOutcomeObserveBy(ctx, deadline, func(callCtx context.Context) error {
			var innerErr error
			done, innerErr = step(callCtx)
			return innerErr
		})
		if err == nil && done {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// commitOutcomeWaitForBackendSettlement is the settlement barrier. A read taken
// before it cannot prove absence, which is the whole reason the controls do not
// census immediately after ServeHTTP returns.
func commitOutcomeWaitForBackendSettlement(t *testing.T, owner *sql.DB, pid int) {
	t.Helper()
	settled := commitOutcomePoll(context.Background(), commitOutcomeProxyHoldLimit,
		func(callCtx context.Context) (bool, error) {
			var open int
			if err := owner.QueryRowContext(callCtx,
				`SELECT count(*) FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&open); err != nil {
				return false, err
			}
			return open == 0, nil
		})
	if !settled {
		t.Fatalf("COULD_NOT_LOOK: settlement not observed for backend %d within %s, so no census after it is conclusive",
			pid, commitOutcomeProxyHoldLimit)
	}
}

// commitOutcomeFatal is the slice of *testing.T a witness needs. It exists so the
// witness's own REFUSAL can be controlled — a guard that cannot be exercised is a
// guard nobody knows is still there.
type commitOutcomeFatal interface {
	Helper()
	Fatalf(format string, args ...any)
}

// commitOutcomeFenceKey is ONE route's intended transaction fence, named and
// keyed exactly.
//
// name is the literal string the route passes to tx.lockTransaction. It is
// carried alongside the halves so a failure says WHICH fence was not observed
// rather than printing two integers, and so a control cannot quietly assert a
// key it never named.
type commitOutcomeFenceKey struct {
	name    string
	classID int64
	objID   int64
	// known guards against an unnamed key reaching an assertion. Every one of the
	// six routes IS derivable from request intent at this pin, so in practice it is
	// always true for a route; it stays because an assertion on a key nobody named
	// must fail loudly rather than compare zeroes.
	known bool
}

// commitOutcomeNamedFenceKey derives the exact key for a route whose lock NAME
// this package can construct in full.
func commitOutcomeNamedFenceKey(t *testing.T, owner *sql.DB, name string) commitOutcomeFenceKey {
	t.Helper()
	classID, objID := commitOutcomeAdvisoryKeyHalves(commitOutcomeAdvisoryKey(t, owner, name))
	return commitOutcomeFenceKey{name: name, classID: classID, objID: objID, known: true}
}

// commitOutcomeWaitForExactFence is the R2 witness: the holder HOLDS and the
// identified resend AWAITS the INTENDED (classid, objid, objsubid).
//
// WHY EXACTNESS IS THE WHOLE CONTROL. The previous form accepted any advisory
// key that was not the tenant audit key. That is not the route's fence: at this
// head the ack route also takes a transaction-scoped lock on
// directNoticeMessageLockKey (communication_ack_apply.go:423) and the cursor
// route one on directNoticeCursorIdentityLockKey (communication_cursor_service.go:1296),
// and the send route one on directNoticeMessageLockKey (communication_service.go:493).
// Each of those is IDENTICAL for an original and its resend, so deleting the
// route's own tx.lockTransaction could leave the observation green and M11 would
// not be established for those routes. A witness that can be satisfied by a lock
// the mutant does not remove is not a witness.
//
// shared is the set of keys that are known to be contended for reasons other
// than this route's fence — the tenant audit key, and any secondary key the
// caller can name. Passing one of them AS the intended fence is refused outright,
// so a control cannot be quietly rewritten into the weaker form it replaced.
//
// It returns false rather than failing, so each caller names its own inability.
func commitOutcomeWaitForExactFence(
	t commitOutcomeFatal,
	owner *sql.DB,
	holderPID int,
	want commitOutcomeFenceKey,
	shared []commitOutcomeFenceKey,
) (objSubID int64, observed bool) {
	t.Helper()
	if !want.known {
		return 0, false
	}
	for _, other := range shared {
		if other.known && other.classID == want.classID && other.objID == want.objID {
			t.Fatalf("the fence witness for %q was handed a key that is also %q: a key both the original and its resend take for another reason cannot stand in for the route's own fence",
				want.name, other.name)
		}
	}
	found := commitOutcomePoll(context.Background(), commitOutcomeProxyHoldLimit,
		func(callCtx context.Context) (bool, error) {
			err := owner.QueryRowContext(callCtx, `
				SELECT waiter.objsubid::bigint
				FROM pg_locks waiter
				JOIN pg_locks holder
				  ON holder.locktype = 'advisory'
				 AND holder.classid = waiter.classid
				 AND holder.objid = waiter.objid
				 AND holder.objsubid = waiter.objsubid
				 AND holder.granted
				WHERE waiter.locktype = 'advisory'
				  AND NOT waiter.granted
				  AND holder.pid = $1
				  AND waiter.pid <> $1
				  AND waiter.classid::bigint = $2
				  AND waiter.objid::bigint = $3
				LIMIT 1`, holderPID, want.classID, want.objID).Scan(&objSubID)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return true, nil
		})
	return objSubID, found
}

// commitOutcomeAssertExactHolder is the R2 OWNERSHIP ORACLE.
//
// It requires the real original transaction to hold the route's own advisory
// lock as ONE exact tuple:
//
//	current database OID · the causally identified original PID ·
//	locktype='advisory' · classid/objid of hashtextextended(<expected name>,0) ·
//	objsubid=1 · mode='ExclusiveLock' · granted=true
//
// objsubid=1 is not decoration. sqlstore/scope.go:120-135 calls the ONE-BIGINT
// form pg_advisory_xact_lock(hashtextextended($1,0)); PostgreSQL records the
// two-argument namespace form with objsubid=2. Requiring 1 is what stops a
// two-int advisory lock from ever satisfying this assertion.
//
// The database OID and the exact PID matter for the same reason: without them an
// unrelated backend, or the same key in another database on the same cluster,
// could stand in for the transaction under test.
//
// A tenant-audit lock, a message lock and a cursor-identity lock all fail this
// assertion, because the expected name is derived from the request's own intent
// BEFORE the request runs — never read back from whatever locks appeared.
func commitOutcomeAssertExactHolder(
	t *testing.T, owner *sql.DB, holderPID int, want commitOutcomeFenceKey,
) {
	t.Helper()
	if !want.known {
		t.Fatalf("COULD_NOT_LOOK: no expected fence name was derived for %q, so ownership cannot be asserted", want.name)
	}
	var rows int
	held := commitOutcomePoll(context.Background(), commitOutcomeProxyHoldLimit,
		func(callCtx context.Context) (bool, error) {
			if err := owner.QueryRowContext(callCtx, `
				SELECT count(*) FROM pg_locks
				WHERE locktype = 'advisory'
				  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
				  AND pid = $1
				  AND classid::bigint = $2
				  AND objid::bigint = $3
				  AND objsubid = 1
				  AND mode = 'ExclusiveLock'
				  AND granted`,
				holderPID, want.classID, want.objID).Scan(&rows); err != nil {
				return false, err
			}
			return rows > 0, nil
		})
	if !held {
		t.Fatalf("backend %d does not hold the exact advisory tuple for %q (classid=%d objid=%d objsubid=1 ExclusiveLock granted, current database): the route's own transaction lock is absent",
			holderPID, want.name, want.classID, want.objID)
	}
}

// commitOutcomeBlockingPIDsOnce asks PostgreSQL once, under one bounded call,
// which backends block the given one. It returns an empty set rather than
// failing, so a caller probing several candidates is not derailed by the ones
// that are simply not blocked.
func commitOutcomeBlockingPIDsOnce(t *testing.T, owner *sql.DB, waiterPID int) []int64 {
	t.Helper()
	var blockers []int64
	err := commitOutcomeObserve(context.Background(), func(callCtx context.Context) error {
		rows, err := owner.QueryContext(callCtx,
			`SELECT unnest(pg_blocking_pids($1))::bigint`, waiterPID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var current []int64
		for rows.Next() {
			var pid int64
			if err := rows.Scan(&pid); err != nil {
				return err
			}
			current = append(current, pid)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		blockers = current
		return nil
	})
	if err != nil {
		return nil
	}
	return blockers
}

// commitOutcomeBlockingPIDs reports which backends PostgreSQL says are blocking
// the given one. It is the real earlier-blocker evidence: no synthetic contender
// and no guess about which lock the resend is parked on.
func commitOutcomeBlockingPIDs(t *testing.T, owner *sql.DB, waiterPID int) []int64 {
	t.Helper()
	var blockers []int64
	found := commitOutcomePoll(context.Background(), commitOutcomeProxyHoldLimit,
		func(callCtx context.Context) (bool, error) {
			rows, err := owner.QueryContext(callCtx,
				`SELECT unnest(pg_blocking_pids($1))::bigint`, waiterPID)
			if err != nil {
				return false, err
			}
			defer func() { _ = rows.Close() }()
			var current []int64
			for rows.Next() {
				var pid int64
				if err := rows.Scan(&pid); err != nil {
					return false, err
				}
				current = append(current, pid)
			}
			if err := rows.Err(); err != nil {
				return false, err
			}
			blockers = current
			return len(current) > 0, nil
		})
	if !found {
		return nil
	}
	return blockers
}

// commitOutcomeAdvisoryKeyHalves splits a 64-bit advisory key the way PostgreSQL
// stores it: the high 32 bits become classid and the low 32 become objid, both
// read back as unsigned oid values.
func commitOutcomeAdvisoryKeyHalves(key int64) (classID, objID int64) {
	unsigned := uint64(key)
	return int64(uint32(unsigned >> 32)), int64(uint32(unsigned))
}

// commitOutcomeAdvisoryKey computes the advisory key the store derives for a
// lock name, so a control can exclude the tenant audit key by value instead of
// by guesswork.
func commitOutcomeAdvisoryKey(t *testing.T, owner *sql.DB, name string) int64 {
	t.Helper()
	var key int64
	if err := commitOutcomeObserve(context.Background(), func(callCtx context.Context) error {
		return owner.QueryRowContext(callCtx,
			`SELECT pg_catalog.hashtextextended($1, 0)`, name).Scan(&key)
	}); err != nil {
		t.Fatalf("COULD_NOT_LOOK: derive the advisory key for %q: %v", name, err)
	}
	return key
}

// commitOutcomeAssertProxyCustody is the custody control on the instrument
// itself: the log carries frame types and command tags, and never a byte of
// credential material.
func commitOutcomeAssertProxyCustody(t *testing.T, proxy *commitOutcomeProxy) {
	t.Helper()
	for _, line := range proxy.custodyLog() {
		lower := strings.ToLower(line)
		for _, forbidden := range []string{"password", "scram", "sasl", "md5"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("the proxy log recorded credential-adjacent material (%q); the instrument must never write startup or authentication bytes", line)
			}
		}
	}
}
