// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package wsclient is a minimal RFC 6455 client for connector-side control
// channels. Production callers should use wss:// URLs; ws:// is supported only
// so offline tests can run against local listeners without TLS fixtures.
package wsclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6455 mandates SHA-1 for the Sec-WebSocket-Accept handshake (not a security primitive)
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxMessageBytes = 1 << 20
	defaultHandshake       = 10 * time.Second
	websocketGUID          = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa
)

// Options tunes Dial and message assembly. The zero value is usable.
type Options struct {
	MaxMessageBytes  int64
	TLSConfig        *tls.Config
	HandshakeTimeout time.Duration
}

func (o Options) maxMessageBytes() int64 {
	if o.MaxMessageBytes <= 0 {
		return defaultMaxMessageBytes
	}
	return o.MaxMessageBytes
}

func (o Options) handshakeTimeout() time.Duration {
	if o.HandshakeTimeout <= 0 {
		return defaultHandshake
	}
	return o.HandshakeTimeout
}

// Conn is one WebSocket client connection. ReadMessage callers must serialize;
// writes are guarded internally so WriteText and automatic PONG replies can
// share the socket safely.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	maxMessageBytes int64
	writeMu         sync.Mutex
	closeOnce       sync.Once
}

// Dial opens rawURL as a WebSocket client connection, performs the HTTP/1.1
// Upgrade handshake, and verifies the server's accept key. Only ws:// and wss://
// are supported; production use should pass wss://.
func Dial(ctx context.Context, rawURL string, header http.Header, opts Options) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("wsclient: parse url: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("wsclient: unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("wsclient: missing host")
	}

	handshakeCtx, cancel := context.WithTimeout(ctx, opts.handshakeTimeout())
	defer cancel()

	nc, err := dialNet(handshakeCtx, u, opts)
	if err != nil {
		return nil, err
	}
	if deadline, ok := handshakeCtx.Deadline(); ok {
		_ = nc.SetDeadline(deadline)
	}
	defer func() {
		if err != nil {
			_ = nc.Close()
		}
	}()

	key, err := randomKey()
	if err != nil {
		return nil, err
	}
	req, err := handshakeRequest(u, key, header)
	if err != nil {
		return nil, err
	}
	if err = req.Write(nc); err != nil {
		return nil, fmt.Errorf("wsclient: write handshake: %w", err)
	}

	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("wsclient: read handshake: %w", err)
	}
	if err = verifyHandshake(resp, key); err != nil {
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})

	return &Conn{
		conn:            nc,
		br:              br,
		maxMessageBytes: opts.maxMessageBytes(),
	}, nil
}

func dialNet(ctx context.Context, u *url.URL, opts Options) (net.Conn, error) {
	address := dialAddress(u)
	if u.Scheme == "wss" {
		cfg := opts.TLSConfig
		if cfg == nil {
			cfg = &tls.Config{}
		} else {
			cfg = cfg.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		d := tls.Dialer{NetDialer: &net.Dialer{}, Config: cfg}
		c, err := d.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, fmt.Errorf("wsclient: dial wss: %w", err)
		}
		return c, nil
	}
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("wsclient: dial ws: %w", err)
	}
	return c, nil
}

func dialAddress(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	port := "80"
	if u.Scheme == "wss" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func randomKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("wsclient: random key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

func handshakeRequest(u *url.URL, key string, extra http.Header) (*http.Request, error) {
	scheme := "http"
	if u.Scheme == "wss" {
		scheme = "https"
	}
	httpURL := *u
	httpURL.Scheme = scheme
	req, err := http.NewRequest(http.MethodGet, httpURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("wsclient: build handshake: %w", err)
	}
	copyHeaders(req.Header, extra)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Del("Sec-WebSocket-Protocol")
	req.Header.Del("Sec-WebSocket-Extensions")
	return req, nil
}

func copyHeaders(dst, src http.Header) {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.EqualFold(k, "Sec-WebSocket-Protocol") ||
			strings.EqualFold(k, "Sec-WebSocket-Extensions") {
			continue
		}
		for _, v := range src[k] {
			dst.Add(k, v)
		}
	}
}

func verifyHandshake(resp *http.Response, key string) error {
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("wsclient: handshake status %d", resp.StatusCode)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		return fmt.Errorf("wsclient: handshake missing websocket upgrade")
	}
	if !headerToken(resp.Header.Get("Connection"), "upgrade") {
		return fmt.Errorf("wsclient: handshake missing connection upgrade")
	}
	if resp.Header.Get("Sec-WebSocket-Extensions") != "" {
		return fmt.Errorf("wsclient: unexpected websocket extension")
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), acceptKey(key); got != want {
		return fmt.Errorf("wsclient: bad websocket accept")
	}
	return nil
}

func headerToken(v, want string) bool {
	for _, tok := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(tok), want) {
			return true
		}
	}
	return false
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID)) // #nosec G401 -- RFC 6455 mandates SHA-1 for the Sec-WebSocket-Accept handshake, not confidentiality
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ReadMessage returns the next complete TEXT or BINARY message. Control frames
// are handled internally; a received CLOSE is echoed and returned as an
// io.EOF-wrapped error.
func (c *Conn) ReadMessage(ctx context.Context) ([]byte, error) {
	cleanup := applyDeadline(ctx, c.conn.SetReadDeadline)
	defer cleanup()

	var (
		assembling bool
		messageOp  byte
		out        []byte
	)
	for {
		f, err := c.readFrame()
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("wsclient: read message: %w", ctx.Err())
			}
			return nil, err
		}

		switch f.opcode {
		case opText, opBinary:
			if assembling {
				_ = c.Close()
				return nil, fmt.Errorf("wsclient: data frame while fragmented message open")
			}
			if int64(len(f.payload)) > c.maxMessageBytes {
				_ = c.Close()
				return nil, fmt.Errorf("wsclient: message exceeds %d bytes", c.maxMessageBytes)
			}
			if f.fin {
				return f.payload, nil
			}
			assembling = true
			messageOp = f.opcode
			out = append(out[:0], f.payload...)
		case opContinuation:
			if !assembling || messageOp == 0 {
				_ = c.Close()
				return nil, fmt.Errorf("wsclient: unexpected continuation")
			}
			if int64(len(out)+len(f.payload)) > c.maxMessageBytes {
				_ = c.Close()
				return nil, fmt.Errorf("wsclient: message exceeds %d bytes", c.maxMessageBytes)
			}
			out = append(out, f.payload...)
			if f.fin {
				return out, nil
			}
		case opPing:
			if err := c.writeFrame(opPong, f.payload); err != nil {
				return nil, err
			}
		case opPong:
			continue
		case opClose:
			_ = c.closeWithPayload(f.payload)
			return nil, fmt.Errorf("wsclient: close received: %w", io.EOF)
		default:
			_ = c.Close()
			return nil, fmt.Errorf("wsclient: unsupported opcode 0x%x", f.opcode)
		}
	}
}

// WriteText writes one complete TEXT message. Client frames are always masked.
func (c *Conn) WriteText(ctx context.Context, p []byte) error {
	cleanup := applyDeadline(ctx, c.conn.SetWriteDeadline)
	defer cleanup()
	if err := c.writeFrame(opText, p); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("wsclient: write text: %w", ctx.Err())
		}
		return err
	}
	return nil
}

// Close sends a normal WebSocket close frame once, then closes the TCP
// connection. It is safe to call repeatedly.
func (c *Conn) Close() error {
	return c.closeWithPayload([]byte{0x03, 0xe8})
}

func (c *Conn) closeWithPayload(payload []byte) error {
	var err error
	c.closeOnce.Do(func() {
		if len(payload) > 125 {
			payload = payload[:125]
		}
		err = c.writeFrame(opClose, payload)
		if closeErr := c.conn.Close(); err == nil {
			err = closeErr
		}
	})
	return err
}

type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

func (c *Conn) readFrame() (frame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return frame{}, fmt.Errorf("wsclient: read frame header: %w", err)
	}

	fin := hdr[0]&0x80 != 0
	if hdr[0]&0x70 != 0 {
		_ = c.Close()
		return frame{}, fmt.Errorf("wsclient: rsv bits set")
	}
	opcode := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	if masked {
		_ = c.Close()
		return frame{}, fmt.Errorf("wsclient: masked server frame")
	}

	payloadLen, err := c.readPayloadLen(hdr[1] & 0x7f)
	if err != nil {
		_ = c.Close()
		return frame{}, err
	}
	if isControl(opcode) {
		if !fin {
			_ = c.Close()
			return frame{}, fmt.Errorf("wsclient: fragmented control frame")
		}
		if payloadLen > 125 {
			_ = c.Close()
			return frame{}, fmt.Errorf("wsclient: oversized control frame")
		}
	} else if payloadLen > uint64(c.maxMessageBytes) {
		_ = c.Close()
		return frame{}, fmt.Errorf("wsclient: message exceeds %d bytes", c.maxMessageBytes)
	}

	payload := make([]byte, int(payloadLen))
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return frame{}, fmt.Errorf("wsclient: read payload: %w", err)
	}
	return frame{fin: fin, opcode: opcode, payload: payload}, nil
}

func (c *Conn) readPayloadLen(code byte) (uint64, error) {
	switch code {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return 0, fmt.Errorf("wsclient: read extended length: %w", err)
		}
		return uint64(binary.BigEndian.Uint16(b[:])), nil
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return 0, fmt.Errorf("wsclient: read extended length: %w", err)
		}
		n := binary.BigEndian.Uint64(b[:])
		if n > uint64(^uint(0)>>1) {
			return 0, fmt.Errorf("wsclient: payload too large")
		}
		return n, nil
	default:
		return uint64(code), nil
	}
}

func isControl(opcode byte) bool {
	return opcode == opClose || opcode == opPing || opcode == opPong
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	if isControl(opcode) && len(payload) > 125 {
		return fmt.Errorf("wsclient: control payload too large")
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("wsclient: random mask: %w", err)
	}

	header := make([]byte, 0, 14)
	header = append(header, 0x80|opcode)
	n := len(payload)
	switch {
	case n <= 125:
		header = append(header, 0x80|byte(n))
	case n <= 0xffff:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		header = append(header, b[:]...)
	}
	header = append(header, mask[:]...)

	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.conn.Write(header); err != nil {
		return fmt.Errorf("wsclient: write frame header: %w", err)
	}
	if len(masked) > 0 {
		if _, err := c.conn.Write(masked); err != nil {
			return fmt.Errorf("wsclient: write frame payload: %w", err)
		}
	}
	return nil
}

func applyDeadline(ctx context.Context, set func(time.Time) error) func() {
	if deadline, ok := ctx.Deadline(); ok {
		_ = set(deadline)
	} else {
		_ = set(time.Time{})
	}
	if ctx.Done() == nil {
		return func() { _ = set(time.Time{}) }
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = set(time.Now())
		case <-done:
		}
	}()
	return func() {
		close(done)
		_ = set(time.Time{})
	}
}
