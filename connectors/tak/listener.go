// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk/netbind"
)

// listener.go serves the CoT bearers. The wire framing follows the two rules the
// MITRE developer's guide states for the transports TAK uses:
//
//   - UDP: "a single CoT message per UDP packet" [GUIDE].
//   - TCP: "open-squirt-close" — one CoT message per connection, then the sender
//     closes; there is no streaming/length-prefixed framing at this layer [GUIDE].
//
// Everything here is deny-closed and bounded: a bearer facing an untrusted network
// must not let a hostile or buggy peer exhaust memory, pin connection slots, or
// out-run the engine. Parsing and minimal-data reduction live in cot.go/ingest.go;
// this file only frames bytes, meters them, and hands accepted events to the
// callbacks the Source supplies.

// readTimeout bounds one open-squirt-close exchange. A client that opens a TCP
// connection and never squirts (or dribbles bytes to hold a slot) is cut off here.
const readTimeout = 10 * time.Second

// runListeners starts the configured CoT bearers, blocks until ctx is done, then
// tears them down and returns. A bind failure is a hard, immediate error: an
// operator who configured a listener and did not get one must not believe ingest
// is running (deny-closed). Once bound, a bearer that stops on its own before the
// context is canceled is reported as a fault for the same reason.
func runListeners(ctx context.Context, cfg config, cb listenerCallbacks) error {
	// One shared limiter across both bearers: the rate ceiling is a property of the
	// engine we feed, not of any one socket.
	bucket := newTokenBucket(cfg.rateLimitEPS, time.Now)

	var (
		wg      sync.WaitGroup
		udpConn net.PacketConn
		tcpLn   net.Listener
	)

	// Bind every requested socket up front so a bind error surfaces here, not after
	// we have already reported a half-running listener set.
	if cfg.udpListen != "" {
		c, err := bindUDP(ctx, cfg)
		if err != nil {
			return fmt.Errorf("tak: bind CoT UDP listener %s: %w", cfg.udpListen, err)
		}
		udpConn = c
	}
	if cfg.tcpListen != "" {
		// Through the product's single admission point, not net.Listen: the bearer
		// carries CoT in the clear, so whether this address may be bound at all is
		// a policy question, and it is answered BEFORE the syscall.
		ln, err := netbind.Listen(ctx, "tcp", cfg.tcpListen, cfg.bindPolicy(cfgCoTTCPListen))
		if err != nil {
			if udpConn != nil {
				_ = udpConn.Close()
			}
			return fmt.Errorf("tak: bind CoT TCP listener %s: %w", cfg.tcpListen, err)
		}
		tcpLn = ln
	}

	if udpConn != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveUDP(ctx, cfg, udpConn, bucket, cb)
		}()
	}
	if tcpLn != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveTCP(ctx, cfg, tcpLn, bucket, cb)
		}()
	}

	wg.Wait()

	if ctx.Err() != nil {
		// Clean shutdown: the engine canceled us. serveCoT tolerates a nil return.
		return nil
	}
	// Every configured bearer stopped while the context was still live — the socket
	// was closed out from under us, or accept failed permanently. Report it so the
	// engine does not keep believing CoT ingest is live.
	return errors.New("tak: all CoT listeners stopped before shutdown")
}

// bindUDP binds the UDP CoT socket, joining a multicast group when one is
// configured. loadConfig has already validated that cfg.multicast is a multicast
// IP and that cfg.udpListen is a host:port.
func bindUDP(ctx context.Context, cfg config) (net.PacketConn, error) {
	if cfg.multicast == "" {
		return netbind.ListenPacket(ctx, "udp", cfg.udpListen, cfg.bindPolicy(cfgCoTUDPListen))
	}
	// net.ListenMulticastUDP wants the GROUP address (its IP must itself be
	// multicast). cot_multicast_group is the group; cot_udp_listen supplies the port.
	// We pass a nil interface so the system default receives the join, as configured.
	laddr, err := net.ResolveUDPAddr("udp", cfg.udpListen)
	if err != nil {
		return nil, err
	}
	group := net.ParseIP(cfg.multicast)
	if group == nil {
		return nil, fmt.Errorf("tak: %q is not an IP", cfg.multicast)
	}
	gaddr := &net.UDPAddr{IP: group, Port: laddr.Port, Zone: laddr.Zone}
	return netbind.ListenMulticastUDP("udp", nil, gaddr, cfg.bindPolicy(cfgCoTUDPListen))
}

// serveUDP reads one CoT message per datagram until its socket is closed. It owns a
// closer goroutine that closes the socket on ctx cancellation, which unblocks the
// in-flight ReadFrom — the portable way to break out of a blocking net call.
func serveUDP(ctx context.Context, cfg config, conn net.PacketConn, bucket *tokenBucket, cb listenerCallbacks) {
	var closerWG sync.WaitGroup
	done := make(chan struct{})
	closerWG.Add(1)
	go func() {
		defer closerWG.Done()
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = conn.Close()
	}()
	defer func() {
		close(done)
		closerWG.Wait()
	}()

	// One byte over the cap so a datagram of exactly MaxEventBytes still fits and an
	// oversize one is detectably truncated. The single event carries no reference to
	// this buffer once ParseEvent returns, so it is safe to reuse across reads.
	buf := make([]byte, cfg.limits.MaxEventBytes+1)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			// Socket closed at shutdown, or a fatal read error — either way, stop.
			return
		}
		// [GUIDE] one message per datagram: a datagram larger than the cap was
		// truncated into buf and can only be a partial event. Refuse it whole rather
		// than parse a fragment.
		if n > cfg.limits.MaxEventBytes {
			cb.onReject(reasonOversize, transportUDP)
			continue
		}
		if stop := dispatch(ctx, cfg, bucket, cb, buf[:n], transportUDP); stop {
			return
		}
	}
}

// serveTCP accepts open-squirt-close connections, bounded to cfg.maxTCPConns
// concurrent handlers. The accept loop never blocks on a full pool: excess
// connections are refused and closed at once. A closer goroutine closes the
// listener (to break Accept) and every in-flight connection (to break their reads)
// on ctx cancellation.
func serveTCP(ctx context.Context, cfg config, ln net.Listener, bucket *tokenBucket, cb listenerCallbacks) {
	cs := newConnSet()

	var closerWG sync.WaitGroup
	done := make(chan struct{})
	closerWG.Add(1)
	go func() {
		defer closerWG.Done()
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = ln.Close()
		cs.closeAll()
	}()

	// A buffered channel is the connection semaphore: a slot per permitted concurrent
	// connection, never a goroutine per slot.
	sem := make(chan struct{}, cfg.maxTCPConns)
	var conns sync.WaitGroup

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener closed at shutdown, or a permanent accept fault — stop accepting.
			break
		}
		select {
		case sem <- struct{}{}:
		default:
			// [task] pool full: refuse and close now, and keep accepting. A refused
			// connection must never stall the loop behind a slow handler.
			cb.onReject(reasonConnLimit, transportTCP)
			_ = conn.Close()
			continue
		}
		if !cs.add(conn) {
			// ctx is already ending; do not start a handler we would immediately tear
			// down. Release the slot and drop the connection.
			<-sem
			_ = conn.Close()
			continue
		}
		conns.Add(1)
		go func(c net.Conn) {
			defer conns.Done()
			defer func() { <-sem }()
			defer cs.remove(c)
			handleTCPConn(ctx, cfg, c, bucket, cb)
		}(conn)
	}

	conns.Wait()
	close(done)
	closerWG.Wait()
}

// handleTCPConn reads exactly one CoT message from an open-squirt-close connection
// and dispatches it. One message per connection: it always closes the socket and
// returns when done [GUIDE].
func handleTCPConn(ctx context.Context, cfg config, conn net.Conn, bucket *tokenBucket, cb listenerCallbacks) {
	defer conn.Close()

	// Bound the read so a peer that opens and stalls cannot hold a slot indefinitely.
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))

	// One byte over the cap distinguishes an at-limit message from an oversize one.
	// The sender closes after squirting, which yields the io.EOF that ends ReadAll.
	raw, err := io.ReadAll(io.LimitReader(conn, int64(cfg.limits.MaxEventBytes)+1))
	if err != nil {
		// A read the caller torn down during shutdown is not the peer's fault — do not
		// count it as malformed traffic.
		if ctx.Err() != nil {
			return
		}
		cb.onReject(reasonMalformed, transportTCP)
		return
	}
	if len(raw) > cfg.limits.MaxEventBytes {
		cb.onReject(reasonOversize, transportTCP)
		return
	}
	// The return value (stop) is meaningful only to a streaming reader; a TCP
	// connection carries a single message, so we return regardless.
	_ = dispatch(ctx, cfg, bucket, cb, raw, transportTCP)
}

// dispatch applies the shared rate limit, then parses and delivers one raw CoT
// message. It returns stop=true when the reader loop should end because the
// engine's context is going away (onEvent, which is the backpressure seam, failed).
func dispatch(ctx context.Context, cfg config, bucket *tokenBucket, cb listenerCallbacks, raw []byte, transport string) (stop bool) {
	// [task] Cheapest rejection first: a rate-limited message costs no parser cycles.
	if !bucket.allow() {
		cb.onReject(reasonRateLimited, transport)
		return false
	}
	ev, err := ParseEvent(raw, cfg.limits)
	if err != nil {
		// A size-class refusal is reported as oversize; every other parse failure is
		// malformed. The offending bytes are never echoed (see rejectionFinding).
		if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrDetailTooLarge) {
			cb.onReject(reasonOversize, transport)
		} else {
			cb.onReject(reasonMalformed, transport)
		}
		return false
	}
	if err := cb.onEvent(ctx, ev, transport); err != nil {
		// onEvent only fails when the bounded queue is abandoned on ctx cancellation.
		return true
	}
	return false
}

// tokenBucket is a rate limiter: up to `rate` events per second with a burst of
// `rate`. It refills lazily from elapsed wall time on each check — no timer, no
// goroutine per token — and is safe for concurrent use across both bearers. The
// clock is injectable so a test can advance time without sleeping.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	capacity float64
	rate     float64 // tokens added per second
	last     time.Time
	now      func() time.Time
}

// newTokenBucket returns a bucket that starts full (a burst of eps is allowed
// immediately), refilling at eps tokens per second. eps is validated > 0 in
// loadConfig, so capacity is always positive.
func newTokenBucket(eps int, now func() time.Time) *tokenBucket {
	return &tokenBucket{
		tokens:   float64(eps),
		capacity: float64(eps),
		rate:     float64(eps),
		last:     now(),
		now:      now,
	}
}

// allow consumes one token if available, returning whether the event is admitted.
func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	t := b.now()
	if elapsed := t.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = t
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// connSet tracks the live TCP connections so the closer goroutine can break their
// blocked reads on shutdown. It also records "closed" so a connection accepted in
// the race with cancellation is dropped instead of handled.
type connSet struct {
	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

func newConnSet() *connSet { return &connSet{conns: map[net.Conn]struct{}{}} }

// add registers a connection, returning false if the set is already closing.
func (cs *connSet) add(c net.Conn) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closed {
		return false
	}
	cs.conns[c] = struct{}{}
	return true
}

func (cs *connSet) remove(c net.Conn) {
	cs.mu.Lock()
	delete(cs.conns, c)
	cs.mu.Unlock()
}

// closeAll marks the set closed and closes every live connection. Closing a socket
// that a handler is also closing is harmless; the second close just returns an error.
func (cs *connSet) closeAll() {
	cs.mu.Lock()
	cs.closed = true
	for c := range cs.conns {
		_ = c.Close()
	}
	cs.conns = nil
	cs.mu.Unlock()
}
