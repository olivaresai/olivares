// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package wsclient

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialWriteTextReadEcho(t *testing.T) {
	srv := newWSTestServer(t, wsTestOptions{}, func(sc *serverConn) error {
		if got := sc.req.Header.Get("Authorization"); got != "Bearer test" {
			return fmt.Errorf("authorization header = %q", got)
		}
		f, err := readClientFrame(sc.br)
		if err != nil {
			return err
		}
		if !f.masked {
			return fmt.Errorf("client frame was not masked")
		}
		if f.opcode != opText || string(f.payload) != "hello" {
			return fmt.Errorf("client frame opcode=%x payload=%q", f.opcode, string(f.payload))
		}
		return writeServerFrame(sc.conn, opText, []byte("echo:hello"), true, false, 0)
	})

	ctx, cancel := testContext(t)
	defer cancel()
	c, err := Dial(ctx, srv.url, http.Header{"Authorization": []string{"Bearer test"}}, Options{})
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.WriteText(ctx, []byte("hello")))
	msg, err := c.ReadMessage(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("echo:hello"), msg)
	srv.wait(t)
}

func TestDialBadAcceptFails(t *testing.T) {
	srv := newWSTestServer(t, wsTestOptions{acceptOverride: "bad"}, nil)
	ctx, cancel := testContext(t)
	defer cancel()

	_, err := Dial(ctx, srv.url, nil, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accept")
	srv.wait(t)
}

func TestReadMessageRepliesToPingWithPong(t *testing.T) {
	srv := newWSTestServer(t, wsTestOptions{}, func(sc *serverConn) error {
		if err := writeServerFrame(sc.conn, opPing, []byte("abc"), true, false, 0); err != nil {
			return err
		}
		f, err := readClientFrame(sc.br)
		if err != nil {
			return err
		}
		if !f.masked || f.opcode != opPong || string(f.payload) != "abc" {
			return fmt.Errorf("pong frame masked=%v opcode=%x payload=%q", f.masked, f.opcode, string(f.payload))
		}
		return writeServerFrame(sc.conn, opClose, nil, true, false, 0)
	})

	ctx, cancel := testContext(t)
	defer cancel()
	c, err := Dial(ctx, srv.url, nil, Options{})
	require.NoError(t, err)

	_, err = c.ReadMessage(ctx)
	require.ErrorIs(t, err, io.EOF)
	srv.wait(t)
}

func TestReadMessageAssemblesFragments(t *testing.T) {
	srv := newWSTestServer(t, wsTestOptions{}, func(sc *serverConn) error {
		if err := writeServerFrame(sc.conn, opText, []byte("hel"), false, false, 0); err != nil {
			return err
		}
		if err := writeServerFrame(sc.conn, opContinuation, []byte("lo"), false, false, 0); err != nil {
			return err
		}
		return writeServerFrame(sc.conn, opContinuation, []byte("!"), true, false, 0)
	})

	ctx, cancel := testContext(t)
	defer cancel()
	c, err := Dial(ctx, srv.url, nil, Options{})
	require.NoError(t, err)
	defer c.Close()

	msg, err := c.ReadMessage(ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello!"), msg)
	srv.wait(t)
}

func TestReadMessageOverMaxErrors(t *testing.T) {
	srv := newWSTestServer(t, wsTestOptions{}, func(sc *serverConn) error {
		return writeServerFrame(sc.conn, opText, []byte("12345"), true, false, 0)
	})

	ctx, cancel := testContext(t)
	defer cancel()
	c, err := Dial(ctx, srv.url, nil, Options{MaxMessageBytes: 4})
	require.NoError(t, err)

	_, err = c.ReadMessage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	srv.waitAfterClientReject(t)
}

func TestReadMessageServerCloseEchoesClose(t *testing.T) {
	closePayload := []byte{0x03, 0xe8}
	srv := newWSTestServer(t, wsTestOptions{}, func(sc *serverConn) error {
		if err := writeServerFrame(sc.conn, opClose, closePayload, true, false, 0); err != nil {
			return err
		}
		f, err := readClientFrame(sc.br)
		if err != nil {
			return err
		}
		if !f.masked || f.opcode != opClose || string(f.payload) != string(closePayload) {
			return fmt.Errorf("close echo masked=%v opcode=%x payload=%v", f.masked, f.opcode, f.payload)
		}
		return nil
	})

	ctx, cancel := testContext(t)
	defer cancel()
	c, err := Dial(ctx, srv.url, nil, Options{})
	require.NoError(t, err)

	_, err = c.ReadMessage(ctx)
	require.ErrorIs(t, err, io.EOF)
	srv.wait(t)
}

func TestReadMessageRejectsRSV(t *testing.T) {
	srv := newWSTestServer(t, wsTestOptions{}, func(sc *serverConn) error {
		return writeServerFrame(sc.conn, opText, []byte("bad"), true, false, 0x40)
	})

	ctx, cancel := testContext(t)
	defer cancel()
	c, err := Dial(ctx, srv.url, nil, Options{})
	require.NoError(t, err)

	_, err = c.ReadMessage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rsv")
	srv.waitAfterClientReject(t)
}

func TestReadMessageRejectsMaskedServerFrame(t *testing.T) {
	srv := newWSTestServer(t, wsTestOptions{}, func(sc *serverConn) error {
		return writeServerFrame(sc.conn, opText, []byte("bad"), true, true, 0)
	})

	ctx, cancel := testContext(t)
	defer cancel()
	c, err := Dial(ctx, srv.url, nil, Options{})
	require.NoError(t, err)

	_, err = c.ReadMessage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "masked")
	srv.waitAfterClientReject(t)
}

func TestServerResultAllowedAfterClientReject(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "clean completion", want: true},
		{
			name: "broken pipe while writing",
			err:  &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE},
			want: true,
		},
		{
			name: "connection reset while writing",
			err:  &net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET},
			want: true,
		},
		{
			name: "connection reset while reading",
			err:  &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
		},
		{name: "unrelated server failure", err: errors.New("server handler failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, serverResultAllowedAfterClientReject(tt.err))
		})
	}
}

type wsTestOptions struct {
	acceptOverride string
}

type wsTestServer struct {
	url  string
	ln   net.Listener
	done chan error
}

type serverConn struct {
	conn net.Conn
	br   *bufio.Reader
	req  *http.Request
}

func newWSTestServer(t *testing.T, opts wsTestOptions, handler func(*serverConn) error) *wsTestServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &wsTestServer{
		url:  "ws://" + ln.Addr().String(),
		ln:   ln,
		done: make(chan error, 1),
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		srv.done <- serveOneWS(ln, opts, handler)
	}()
	return srv
}

func serveOneWS(ln net.Listener, opts wsTestOptions, handler func(*serverConn) error) error {
	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return err
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	accept := testAcceptKey(key)
	if opts.acceptOverride != "" {
		accept = opts.acceptOverride
	}
	if _, err := fmt.Fprintf(conn,
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		return err
	}
	if handler == nil {
		return nil
	}
	return handler(&serverConn{conn: conn, br: br, req: req})
}

func (s *wsTestServer) wait(t *testing.T) {
	t.Helper()
	require.NoError(t, s.waitError(t))
}

func (s *wsTestServer) waitAfterClientReject(t *testing.T) {
	t.Helper()
	err := s.waitError(t)
	// A client that rejects a frame closes before the server necessarily finishes
	// writing it. Only the two write errors caused by that peer close are benign.
	if serverResultAllowedAfterClientReject(err) {
		return
	}
	require.NoError(t, err)
}

func (s *wsTestServer) waitError(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.done:
		return err
	case <-time.After(2 * time.Second):
		_ = s.ln.Close()
		t.Fatal("test websocket server did not finish")
		return nil
	}
}

func serverResultAllowedAfterClientReject(err error) bool {
	if err == nil {
		return true
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "write" {
		return false
	}
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

type testFrame struct {
	fin     bool
	opcode  byte
	masked  bool
	payload []byte
}

func readClientFrame(br *bufio.Reader) (testFrame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return testFrame{}, err
	}
	f := testFrame{
		fin:    hdr[0]&0x80 != 0,
		opcode: hdr[0] & 0x0f,
		masked: hdr[1]&0x80 != 0,
	}
	n, err := readTestPayloadLen(br, hdr[1]&0x7f)
	if err != nil {
		return testFrame{}, err
	}
	var mask [4]byte
	if f.masked {
		if _, err := io.ReadFull(br, mask[:]); err != nil {
			return testFrame{}, err
		}
	}
	f.payload = make([]byte, int(n))
	if _, err := io.ReadFull(br, f.payload); err != nil {
		return testFrame{}, err
	}
	if f.masked {
		for i := range f.payload {
			f.payload[i] ^= mask[i%4]
		}
	}
	return f, nil
}

func readTestPayloadLen(br *bufio.Reader, code byte) (uint64, error) {
	switch code {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(b[:])), nil
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint64(b[:]), nil
	default:
		return uint64(code), nil
	}
}

func writeServerFrame(w io.Writer, opcode byte, payload []byte, fin, masked bool, rsv byte) error {
	first := opcode | (rsv & 0x70)
	if fin {
		first |= 0x80
	}
	header := []byte{first}
	n := len(payload)
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case n <= 125:
		header = append(header, maskBit|byte(n))
	case n <= 0xffff:
		header = append(header, maskBit|126, byte(n>>8), byte(n))
	default:
		header = append(header, maskBit|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		header = append(header, b[:]...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	out := payload
	if masked {
		mask := [4]byte{1, 2, 3, 4}
		if _, err := w.Write(mask[:]); err != nil {
			return err
		}
		out = append([]byte(nil), payload...)
		for i := range out {
			out[i] ^= mask[i%4]
		}
	}
	if len(out) == 0 {
		return nil
	}
	_, err := w.Write(out)
	return err
}

func testAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func testContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Second)
}
