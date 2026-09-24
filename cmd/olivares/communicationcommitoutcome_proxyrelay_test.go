// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// These controls own the test proxy's relay and nothing else. A frame larger
// than the proxy's retention bound is ordinary PostgreSQL — a catalog read of a
// long function body produces one — so the relay must carry it byte for byte in
// both directions, while the bytes it inspects or withholds stay bounded. A
// frame it cannot carry whole must end the connection with a named reason,
// never with a silence the client would report as a protocol failure of the
// server.
//
// No database and no product run here. The test plays both ends of the wire,
// which is what makes an oversized, malformed or torn frame reachable on
// purpose. A subtest whose purpose is one such inability declares exactly that
// entry before causing it; the proxy's end-of-test custody check fails every
// test that records an inability it did not declare, the positives here
// included.

// commitOutcomeRelayOversized is a payload larger than any frame the proxy may
// retain or inspect.
const commitOutcomeRelayOversized = 120000

// commitOutcomeRelayMarker leads every payload this file sends. The proxy's
// custody log must never contain it: the relay records reasons, not bytes.
const commitOutcomeRelayMarker = "relay-payload-witness"

func TestCommunicationCommitOutcomeProxyRelay(t *testing.T) {
	t.Run("oversized_server_frame_passes_through", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		oversized := commitOutcomeFrame('D', commitOutcomeRelayPayload(commitOutcomeRelayOversized))
		ready := commitOutcomeFrame('Z', []byte{'I'})
		wait := commitOutcomeRelaySend(t, rig.upstream, oversized, ready)
		got, err := commitOutcomeRelayReadFrame(rig.client)
		if err != nil {
			t.Fatalf("the %d-byte server frame did not pass through the test proxy: %v", len(oversized), err)
		}
		if !bytes.Equal(got, oversized) {
			t.Fatalf("the %d-byte server frame arrived altered, as %d bytes", len(oversized), len(got))
		}
		next, err := commitOutcomeRelayReadFrame(rig.client)
		if err != nil || !bytes.Equal(next, ready) {
			t.Fatalf("the frame after the oversized one did not arrive intact (%q, %v): the relay lost its framing", next, err)
		}
		if err := wait(); err != nil {
			t.Fatalf("COULD_NOT_LOOK: the scripted server could not write its frames: %v", err)
		}
		commitOutcomeRelayAssertNoPayload(t, rig.proxy)
	})

	t.Run("oversized_client_frame_passes_through", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		oversized := commitOutcomeFrame('d', commitOutcomeRelayPayload(commitOutcomeRelayOversized))
		done := commitOutcomeFrame('c', nil)
		wait := commitOutcomeRelaySend(t, rig.client, oversized, done)
		got, err := commitOutcomeRelayReadFrame(rig.upstream)
		if err != nil {
			t.Fatalf("the %d-byte client frame did not pass through the test proxy: %v", len(oversized), err)
		}
		if !bytes.Equal(got, oversized) {
			t.Fatalf("the %d-byte client frame arrived altered, as %d bytes", len(oversized), len(got))
		}
		next, err := commitOutcomeRelayReadFrame(rig.upstream)
		if err != nil || !bytes.Equal(next, done) {
			t.Fatalf("the frame after the oversized one did not arrive intact (%q, %v): the relay lost its framing", next, err)
		}
		if err := wait(); err != nil {
			t.Fatalf("COULD_NOT_LOOK: the client could not write its frames: %v", err)
		}
		commitOutcomeRelayAssertNoPayload(t, rig.proxy)
	})

	t.Run("held_total_stays_within_the_bound", func(t *testing.T) {
		if commitOutcomeProxyBufferLimit != 64<<10 {
			t.Fatalf("the retention bound is %d bytes, want 65536: it bounds what the proxy inspects and withholds, and larger frames are streamed rather than retained",
				commitOutcomeProxyBufferLimit)
		}
		rig := newCommitOutcomeRelayRig(t)
		session := rig.holdAnswer(t)
		rows := [][]byte{
			commitOutcomeFrame('D', commitOutcomeRelayPayload(30000)),
			commitOutcomeFrame('D', commitOutcomeRelayPayload(30001)),
			commitOutcomeFrame('D', commitOutcomeRelayPayload(30002)),
		}
		wait := commitOutcomeRelaySend(t, rig.upstream, rows...)
		if !commitOutcomeRelayAwaitCustody(rig.proxy, "buffer_limit_reached") {
			t.Fatal("the third withheld frame would take the held total past the bound and was not refused by name")
		}
		commitOutcomeAwaitBufferedFrames(t, rig.proxy, 3)
		session.mu.Lock()
		frames, held := len(session.buffered), session.bufferedBytes
		session.mu.Unlock()
		// The held COMMIT answer is 12 bytes: kind, length and "COMMIT\x00".
		if want := 12 + len(rows[0]) + len(rows[1]); frames != 3 || held != want {
			t.Fatalf("the proxy withholds %d frames of %d bytes, want 3 frames of %d: the answer and the two rows that fit",
				frames, held, want)
		}
		if held > commitOutcomeProxyBufferLimit {
			t.Fatalf("the proxy withholds %d bytes, past its %d-byte bound", held, commitOutcomeProxyBufferLimit)
		}
		if err := wait(); err != nil {
			t.Fatalf("COULD_NOT_LOOK: the scripted server could not write its frames: %v", err)
		}
		delivered := rig.release(t, 4)
		if kinds := commitOutcomeRelayKinds(delivered); kinds != "CDDZ" {
			t.Fatalf("the release delivered %q, want CDDZ: the refused frame must never reach the client", kinds)
		}
		if !bytes.Equal(delivered[1], rows[0]) || !bytes.Equal(delivered[2], rows[1]) {
			t.Fatal("the withheld rows arrived altered after the release")
		}
	})

	t.Run("oversized_frame_while_holding_is_refused", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		session := rig.holdAnswer(t)
		oversized := commitOutcomeFrame('D', commitOutcomeRelayPayload(commitOutcomeRelayOversized))
		ready := commitOutcomeFrame('Z', []byte{'I'})
		wait := commitOutcomeRelaySend(t, rig.upstream, oversized, ready)
		if !commitOutcomeRelayAwaitCustody(rig.proxy, "buffer_limit_reached") {
			t.Fatal("the oversized server frame that arrived while an answer was held was not refused by name")
		}
		// The ReadyForQuery behind it is withheld as usual, which is what shows the
		// relay kept its framing after refusing a frame it could not retain.
		commitOutcomeAwaitBufferedFrames(t, rig.proxy, 2)
		session.mu.Lock()
		frames, held := len(session.buffered), session.bufferedBytes
		session.mu.Unlock()
		if want := 12 + len(ready); frames != 2 || held != want {
			t.Fatalf("the proxy withholds %d frames of %d bytes, want 2 frames of %d: the answer and the ReadyForQuery",
				frames, held, want)
		}
		if err := wait(); err != nil {
			t.Fatalf("COULD_NOT_LOOK: the scripted server could not write its frames: %v", err)
		}
		delivered := rig.release(t, 3)
		if kinds := commitOutcomeRelayKinds(delivered); kinds != "CZZ" {
			t.Fatalf("the release delivered %q, want CZZ: the refused frame must never reach the client", kinds)
		}
		commitOutcomeRelayAssertNoPayload(t, rig.proxy)
	})

	t.Run("frame_length_below_4_from_server_is_named", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		rig.proxy.expectRelayError("from_server:frame_length_below_4")
		rig.serverWrite(t, []byte{'D', 0, 0, 0, 3})
		commitOutcomeRelayExpectNamed(t, rig.proxy, "relay_error=from_server:frame_length_below_4")
		commitOutcomeRelayExpectClosed(t, rig.client, "client")
	})

	t.Run("frame_length_below_4_from_client_is_named", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		rig.proxy.expectRelayError("from_client:frame_length_below_4")
		if err := rig.client.SetWriteDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
			t.Fatalf("COULD_NOT_LOOK: bound the client's write: %v", err)
		}
		if _, err := rig.client.Write([]byte{'Q', 0, 0, 0, 2}); err != nil {
			t.Fatalf("COULD_NOT_LOOK: the client could not write: %v", err)
		}
		commitOutcomeRelayExpectNamed(t, rig.proxy, "relay_error=from_client:frame_length_below_4")
		commitOutcomeRelayExpectClosed(t, rig.upstream, "server")
	})

	t.Run("truncated_frame_is_named", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		rig.proxy.expectRelayError("from_server:short_read")
		frame := commitOutcomeFrame('D', commitOutcomeRelayPayload(1000))
		rig.serverWrite(t, frame[:5+400])
		_ = rig.upstream.Close()
		commitOutcomeRelayExpectNamed(t, rig.proxy, "relay_error=from_server:short_read")
		commitOutcomeRelayExpectClosed(t, rig.client, "client")
	})

	t.Run("truncated_oversized_frame_is_named", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		rig.proxy.expectRelayError("from_server:short_read")
		frame := commitOutcomeFrame('D', commitOutcomeRelayPayload(commitOutcomeRelayOversized))
		cut := 5 + 50000
		// The send result is read only after the naming assertion: a relay that
		// stops reading early may reset the connection under the writer, and that
		// must not stand in for the verdict this control is about.
		sendErr := commitOutcomeRelaySend(t, rig.upstream, frame[:cut])()
		_ = rig.upstream.Close()
		commitOutcomeRelayExpectNamed(t, rig.proxy, "relay_error=from_server:short_read")
		if sendErr != nil {
			t.Fatalf("COULD_NOT_LOOK: the scripted server could not write the first %d bytes: %v", cut, sendErr)
		}
		if got, err := commitOutcomeRelayReadFrame(rig.client); err == nil {
			t.Fatalf("the client received a complete %d-byte frame from a server that sent %d bytes of it", len(got), cut)
		}
	})

	t.Run("closed_peer_is_named", func(t *testing.T) {
		rig := newCommitOutcomeRelayRig(t)
		ready := commitOutcomeFrame('Z', []byte{'I'})
		rig.serverWrite(t, ready)
		_ = rig.upstream.Close()
		got, err := commitOutcomeRelayReadFrame(rig.client)
		if err != nil || !bytes.Equal(got, ready) {
			t.Fatalf("the last complete frame before the server closed did not arrive (%q, %v)", got, err)
		}
		commitOutcomeRelayExpectNamed(t, rig.proxy, "relay_end=from_server:peer_closed")
		commitOutcomeRelayExpectClosed(t, rig.client, "client")
	})

	t.Run("short_write_is_named", func(t *testing.T) {
		// A pipe has no kernel buffer, so a client that goes away in the middle of
		// a frame is observed exactly where it stopped reading.
		proxy, _ := newCommitOutcomeProxy(t, "postgres://u:p@127.0.0.1:9/db?sslmode=disable")
		proxy.expectRelayError("from_server:short_write")
		clientEnd, proxyClient := net.Pipe()
		serverEnd, proxyUpstream := net.Pipe()
		for _, c := range []net.Conn{clientEnd, proxyClient, serverEnd, proxyUpstream} {
			proxy.track(c)
		}
		session := &commitOutcomeProxySession{p: proxy, client: proxyClient, upstream: proxyUpstream}
		proxy.wg.Add(1)
		go session.serverToClient()
		oversized := commitOutcomeFrame('D', commitOutcomeRelayPayload(commitOutcomeRelayOversized))
		commitOutcomeRelaySend(t, serverEnd, oversized)
		if err := clientEnd.SetReadDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
			t.Fatalf("COULD_NOT_LOOK: bound the client's read: %v", err)
		}
		part := make([]byte, 32<<10)
		if _, err := io.ReadFull(clientEnd, part); err != nil {
			t.Fatalf("the client received less than %d bytes of the %d-byte frame: %v", len(part), len(oversized), err)
		}
		if !bytes.Equal(part, oversized[:len(part)]) {
			t.Fatal("the first part of the oversized frame arrived altered")
		}
		_ = clientEnd.Close()
		commitOutcomeRelayExpectNamed(t, proxy, "relay_error=from_server:short_write")
		commitOutcomeRelayAssertNoPayload(t, proxy)
	})
}

// commitOutcomeRelayRig is one test proxy between a raw client and a scripted
// server. Neither end is a database: the test writes each side of the wire
// itself.
type commitOutcomeRelayRig struct {
	proxy    *commitOutcomeProxy
	client   net.Conn
	upstream net.Conn
}

func newCommitOutcomeRelayRig(t *testing.T) *commitOutcomeRelayRig {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: listen for the scripted server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	proxy, redirected := newCommitOutcomeProxy(t, "postgres://u:p@"+listener.Addr().String()+"/db?sslmode=disable")
	address := strings.TrimPrefix(redirected, "postgres://u:p@")
	address = strings.TrimSuffix(address, "/db?sslmode=disable")
	client := newCommitOutcomeProxyClient(t, address)
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		t.Fatalf("COULD_NOT_LOOK: the scripted server's listener is a %T, not TCP", listener)
	}
	if err := tcpListener.SetDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: bound the scripted server's accept: %v", err)
	}
	upstream, err := listener.Accept()
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: the test proxy never dialed the scripted server: %v", err)
	}
	t.Cleanup(func() { _ = upstream.Close() })

	// The startup message is untyped: a length, then the protocol version.
	if err := upstream.SetReadDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: bound the scripted server's read: %v", err)
	}
	var length [4]byte
	if _, err := io.ReadFull(upstream, length[:]); err != nil {
		t.Fatalf("COULD_NOT_LOOK: the startup message never reached the scripted server: %v", err)
	}
	size := binary.BigEndian.Uint32(length[:])
	if size < 8 || size > 1024 {
		t.Fatalf("COULD_NOT_LOOK: the startup message declares %d bytes", size)
	}
	startup := make([]byte, size-4)
	if _, err := io.ReadFull(upstream, startup); err != nil {
		t.Fatalf("COULD_NOT_LOOK: the startup message reached the scripted server torn: %v", err)
	}
	if version := binary.BigEndian.Uint32(startup[:4]); version != commitOutcomeProxyStartupMagic {
		t.Fatalf("COULD_NOT_LOOK: the startup message names protocol %d", version)
	}
	return &commitOutcomeRelayRig{proxy: proxy, client: client.conn, upstream: upstream}
}

// serverWrite sends small frames from the scripted server, bounded by the
// instrument's write limit.
func (r *commitOutcomeRelayRig) serverWrite(t *testing.T, frames ...[]byte) {
	t.Helper()
	if err := r.upstream.SetWriteDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: bound the scripted server's write: %v", err)
	}
	for _, frame := range frames {
		if _, err := r.upstream.Write(frame); err != nil {
			t.Fatalf("COULD_NOT_LOOK: the scripted server could not write: %v", err)
		}
	}
}

// holdAnswer opens the connection with backend pid 4242, arms the proxy to hold
// the answer to the next COMMIT, and has the server answer one. It returns the
// session that now withholds that answer.
func (r *commitOutcomeRelayRig) holdAnswer(t *testing.T) *commitOutcomeProxySession {
	t.Helper()
	key := make([]byte, 8)
	binary.BigEndian.PutUint32(key, 4242)
	r.serverWrite(t,
		commitOutcomeFrame('R', []byte{0, 0, 0, 0}),
		commitOutcomeFrame('K', key),
		commitOutcomeFrame('Z', []byte{'I'}),
	)
	for _, want := range []byte("RKZ") {
		frame, err := commitOutcomeRelayReadFrame(r.client)
		if err != nil || frame[0] != want {
			t.Fatalf("COULD_NOT_LOOK: the opening exchange did not pass through (%q, %v)", frame, err)
		}
	}
	r.proxy.arm(commitOutcomeProxyHoldAck, func(context.Context, int) bool { return true })
	if err := r.client.SetWriteDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: bound the client's write: %v", err)
	}
	if _, err := r.client.Write(commitOutcomeFrame('Q', commitOutcomeStringPayload("commit"))); err != nil {
		t.Fatalf("COULD_NOT_LOOK: the client could not send COMMIT: %v", err)
	}
	query, err := commitOutcomeRelayReadFrame(r.upstream)
	if err != nil || query[0] != 'Q' {
		t.Fatalf("COULD_NOT_LOOK: the COMMIT did not reach the scripted server (%q, %v)", query, err)
	}
	r.serverWrite(t, commitOutcomeFrame('C', commitOutcomeStringPayload("COMMIT")))
	r.proxy.waitForEvent("ack_held")
	session := r.proxy.heldSession()
	if session == nil {
		t.Fatal("COULD_NOT_LOOK: no session owns the held answer")
	}
	return session
}

// release lets the held answer go and returns the next want frames the client
// receives, the last of them a ReadyForQuery the server sends only after the
// release. Reading runs alongside the release, so a large drain never waits on a
// client that has not started reading.
func (r *commitOutcomeRelayRig) release(t *testing.T, want int) [][]byte {
	t.Helper()
	type result struct {
		frames [][]byte
		err    error
	}
	read := make(chan result, 1)
	go func() {
		var got result
		for len(got.frames) < want && got.err == nil {
			var frame []byte
			frame, got.err = commitOutcomeRelayReadFrame(r.client)
			if got.err == nil {
				got.frames = append(got.frames, frame)
			}
		}
		read <- got
	}()
	r.proxy.release()
	r.serverWrite(t, commitOutcomeFrame('Z', []byte{'I'}))
	select {
	case got := <-read:
		if got.err != nil {
			t.Fatalf("after the release the client received %d of %d frames (%q): %v",
				len(got.frames), want, commitOutcomeRelayKinds(got.frames), got.err)
		}
		return got.frames
	case <-time.After(commitOutcomeProxyHoldLimit):
		t.Fatalf("COULD_NOT_LOOK: the client was still reading %d frames after %s", want, commitOutcomeProxyHoldLimit)
		return nil
	}
}

// commitOutcomeRelayPayload is size bytes led by the custody marker.
func commitOutcomeRelayPayload(size int) []byte {
	out := make([]byte, size)
	copy(out, commitOutcomeRelayMarker)
	for i := len(commitOutcomeRelayMarker); i < size; i++ {
		out[i] = byte(i % 251)
	}
	return out
}

// commitOutcomeRelayReadFrame reads one typed frame of any declared length,
// bounded by the instrument's limit. The proxy refuses to retain frames above
// its bound; this reader exists so a test sees exactly what the proxy put on
// the wire.
func commitOutcomeRelayReadFrame(conn net.Conn) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		return nil, err
	}
	var header [5]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length < 4 {
		return nil, fmt.Errorf("a frame declares %d bytes, fewer than its own length field", length)
	}
	frame := make([]byte, 1+int(length))
	copy(frame, header[:])
	if _, err := io.ReadFull(conn, frame[5:]); err != nil {
		return nil, err
	}
	return frame, nil
}

func commitOutcomeRelayKinds(frames [][]byte) string {
	kinds := make([]byte, 0, len(frames))
	for _, frame := range frames {
		kinds = append(kinds, frame[0])
	}
	return string(kinds)
}

// commitOutcomeRelaySend writes data on its own goroutine, so a proxy that has
// stopped reading can never pin the test. The returned wait reports how the
// write ended, within the hold limit; cleanup closes conn, which releases a
// write nobody waited for, and joins the writer.
func commitOutcomeRelaySend(t *testing.T, conn net.Conn, data ...[]byte) func() error {
	t.Helper()
	done := make(chan error, 1)
	stopped := make(chan struct{})
	t.Cleanup(func() {
		_ = conn.Close()
		timer := time.NewTimer(commitOutcomeProxyWriteLimit)
		defer timer.Stop()
		select {
		case <-stopped:
		case <-timer.C:
			t.Error("COULD_NOT_LOOK: the scripted writer did not stop after its connection closed")
		}
	})
	go func() {
		defer close(stopped)
		done <- commitOutcomeRelayWriteAll(conn, data)
	}()
	return func() error {
		select {
		case err := <-done:
			return err
		case <-time.After(commitOutcomeProxyHoldLimit):
			return fmt.Errorf("still writing after %s", commitOutcomeProxyHoldLimit)
		}
	}
}

func commitOutcomeRelayWriteAll(conn net.Conn, data [][]byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		return err
	}
	for _, chunk := range data {
		if _, err := conn.Write(chunk); err != nil {
			return err
		}
	}
	return nil
}

// commitOutcomeRelayAwaitCustody waits, within the instrument's write limit, for
// the proxy to record exactly want.
func commitOutcomeRelayAwaitCustody(proxy *commitOutcomeProxy, want string) bool {
	deadline := time.Now().Add(commitOutcomeProxyWriteLimit)
	for {
		for _, line := range proxy.custodyLog() {
			if line == want {
				return true
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// commitOutcomeRelayExpectNamed requires the custody log to name how a relay
// direction ended.
func commitOutcomeRelayExpectNamed(t *testing.T, proxy *commitOutcomeProxy, want string) {
	t.Helper()
	if !commitOutcomeRelayAwaitCustody(proxy, want) {
		t.Logf("custody log: %q", proxy.custodyLog())
		t.Fatalf("the test proxy did not name how the relay ended: no %q in its custody log", want)
	}
}

// commitOutcomeRelayExpectClosed requires the proxy to have closed conn's peer
// without delivering one more byte: a frame the relay could not carry whole
// must not reach the other side as something a driver would parse.
func commitOutcomeRelayExpectClosed(t *testing.T, conn net.Conn, side string) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: bound the %s's read: %v", side, err)
	}
	var one [1]byte
	n, err := conn.Read(one[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("the %s read %d byte(s) and %v, want the connection closed with nothing delivered", side, n, err)
	}
}

// commitOutcomeRelayAssertNoPayload requires the custody log to hold no frame
// bytes and no credential-adjacent material.
func commitOutcomeRelayAssertNoPayload(t *testing.T, proxy *commitOutcomeProxy) {
	t.Helper()
	commitOutcomeAssertProxyCustody(t, proxy)
	for _, line := range proxy.custodyLog() {
		if strings.Contains(line, commitOutcomeRelayMarker) {
			t.Fatalf("the proxy log recorded frame bytes (%q); the relay records reasons only", line)
		}
	}
}
