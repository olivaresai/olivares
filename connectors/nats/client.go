// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// defaultClientFactory dials the real NATS wire client (overridden by a fake in
// tests). It connects to the first reachable server, performs the handshake and
// attaches the durable PULL consumer's inbox subscription.
func defaultClientFactory(c config) (jsClient, error) {
	return dial(c)
}

// natsConn is the real hand-rolled NATS/JetStream client over net.Conn / tls.Conn.
// It owns the connection, a buffered reader for frame parsing and the random inbox the
// JetStream pull responses are delivered to. It is STDLIB-ONLY — no third-party NATS
// client.
type natsConn struct {
	cfg     config
	conn    net.Conn
	r       *bufio.Reader
	inbox   string // _INBOX.<random> the pull responses arrive on
	apiSubj string // $JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>
}

// serverInfo is the subset of the server's INFO line the client needs: whether the
// server requires/offers TLS and (for completeness) auth. Unknown fields are ignored.
type serverInfo struct {
	TLSRequired  bool `json:"tls_required"`
	TLSVerify    bool `json:"tls_verify"`
	AuthRequired bool `json:"auth_required"`
	Headers      bool `json:"headers"`
}

// connectOpts is the client's CONNECT line payload (the fields the NATS server reads
// from a client's CONNECT). Secrets ride here only on the wire to the server; they are
// never logged.
type connectOpts struct {
	Verbose     bool   `json:"verbose"`
	Pedantic    bool   `json:"pedantic"`
	TLSRequired bool   `json:"tls_required"`
	Name        string `json:"name"`
	Lang        string `json:"lang"`
	Version     string `json:"version"`
	Headers     bool   `json:"headers"`
	User        string `json:"user,omitempty"`
	Pass        string `json:"pass,omitempty"`
	AuthToken   string `json:"auth_token,omitempty"`
}

// dial connects to the first server that accepts the handshake and returns a ready
// natsConn whose inbox is SUBscribed for JetStream pull responses.
func dial(c config) (jsClient, error) {
	var lastErr error
	for _, server := range c.servers {
		nc, err := dialOne(c, server)
		if err != nil {
			lastErr = err
			continue
		}
		return nc, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nats: no servers configured")
	}
	return nil, fmt.Errorf("nats: dial failed: %w", lastErr)
}

// hostPort extracts host:port from a nats[s]://host:port server string, defaulting the
// port to 4222 and the scheme to nats://.
func hostPort(server string) (string, bool, error) {
	s := server
	tlsScheme := false
	if !strings.Contains(s, "://") {
		s = "nats://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", false, fmt.Errorf("nats: bad server %q: %w", server, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "natss", "tls":
		tlsScheme = true
	}
	host := u.Host
	if host == "" {
		return "", false, fmt.Errorf("nats: server %q has no host", server)
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "4222")
	}
	return host, tlsScheme, nil
}

// dialOne performs the full handshake against one server: TCP connect, read INFO,
// optionally upgrade to TLS, send CONNECT + a PING, await PONG, then create the inbox
// subscription. The connect handshake is bounded by cfg.timeout.
func dialOne(c config, server string) (*natsConn, error) {
	addr, tlsScheme, err := hostPort(server)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: c.timeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if c.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(c.timeout))
	}
	r := bufio.NewReader(conn)

	info, err := readInfo(r)
	if err != nil {
		conn.Close()
		return nil, err
	}

	wantTLS := tlsScheme || info.TLSRequired || c.tls != nil
	if wantTLS {
		tlsCfg := c.tls
		if tlsCfg == nil {
			tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			tlsCfg = tlsCfg.Clone()
		}
		if tlsCfg.ServerName == "" {
			if host, _, e := net.SplitHostPort(addr); e == nil {
				tlsCfg.ServerName = host
			}
		}
		tconn := tls.Client(conn, tlsCfg)
		if e := tconn.HandshakeContext(deadlineCtx(c.timeout)); e != nil {
			conn.Close()
			return nil, fmt.Errorf("nats: TLS handshake: %w", e)
		}
		conn = tconn
		r = bufio.NewReader(conn)
	}

	opts := connectOpts{
		Verbose:     false,
		Pedantic:    false,
		TLSRequired: wantTLS,
		Name:        "olivares-observer",
		Lang:        "go",
		Version:     sourceVersion,
		Headers:     true,
		User:        c.user,
		Pass:        c.password,
		AuthToken:   c.token,
	}
	payload, err := json.Marshal(opts)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Write([]byte("CONNECT " + string(payload) + "\r\nPING\r\n")); err != nil {
		conn.Close()
		return nil, err
	}
	// Await PONG (skip any +OK), surfacing a -ERR as an auth/protocol failure.
	if err := awaitPong(r); err != nil {
		conn.Close()
		return nil, err
	}

	inbox, err := newInbox()
	if err != nil {
		conn.Close()
		return nil, err
	}
	nc := &natsConn{
		cfg:     c,
		conn:    conn,
		r:       r,
		inbox:   inbox,
		apiSubj: "$JS.API.CONSUMER.MSG.NEXT." + c.stream + "." + c.consumer,
	}
	// Subscribe the inbox (sid 1) so JetStream pull responses are delivered to us.
	if _, err := conn.Write([]byte("SUB " + inbox + " 1\r\n")); err != nil {
		conn.Close()
		return nil, err
	}
	return nc, nil
}

// Next issues one JetStream pull request for up to batch messages and reads the
// MSG/HMSG frames that arrive on the inbox until the batch is satisfied or the
// request's server-side expiry returns a status frame. Status frames (404/408/409…)
// are filtered out — the caller gets only real stream messages, and an empty slice
// means "no messages this window", so Gather re-requests.
func (nc *natsConn) Next(ctx context.Context, batch int) ([]Msg, error) {
	if nc.conn == nil {
		return nil, fmt.Errorf("nats: client closed")
	}
	// Bound this pull read by ctx and the per-pull deadline (expiry + slack).
	deadline := time.Now().Add(nc.cfg.expires + 2*time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = nc.conn.SetReadDeadline(deadline)

	req := fmt.Sprintf(`{"batch":%d,"expires":%d,"no_wait":false}`, batch, nc.cfg.expires.Nanoseconds())
	pub := fmt.Sprintf("PUB %s %s %d\r\n%s\r\n", nc.apiSubj, nc.inbox, len(req), req)
	if _, err := nc.conn.Write([]byte(pub)); err != nil {
		return nil, err
	}

	var out []Msg
	for len(out) < batch {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		m, ok, err := nc.readFrame()
		if err != nil {
			if isTimeout(err) {
				// The pull window elapsed with no (further) delivery; return what we have.
				return out, nil
			}
			return out, err
		}
		if !ok {
			continue // PING/+OK/-ERR control frame, not a message
		}
		if m.Status != "" && isStatusCode(m.Status) {
			// A "no messages"/expiry/conflict status terminates this batch.
			return out, nil
		}
		// A status message with an empty subject and no status code is still control;
		// only deliver frames that carry a real subject.
		if m.Subject != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

// readFrame reads one server control line and, for a MSG/HMSG, its body. It answers a
// server PING with PONG inline. ok is false for non-message control frames.
func (nc *natsConn) readFrame() (Msg, bool, error) {
	line, err := nc.r.ReadString('\n')
	if err != nil {
		return Msg{}, false, err
	}
	verb, args := parseControlLine(line)
	switch verb {
	case "MSG":
		m, err := readMsg(nc.r, args)
		return m, err == nil, err
	case "HMSG":
		m, err := readHMsg(nc.r, args)
		return m, err == nil, err
	case "PING":
		_, _ = nc.conn.Write([]byte("PONG\r\n"))
		return Msg{}, false, nil
	case "PONG", "+OK", "INFO":
		return Msg{}, false, nil
	case "-ERR":
		return Msg{}, false, fmt.Errorf("nats: server error: %s", args)
	default:
		return Msg{}, false, nil
	}
}

// Close tears down the connection.
func (nc *natsConn) Close() error {
	if nc.conn == nil {
		return nil
	}
	err := nc.conn.Close()
	nc.conn = nil
	return err
}

// readInfo reads the server's opening INFO line and decodes its JSON.
func readInfo(r *bufio.Reader) (serverInfo, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return serverInfo{}, fmt.Errorf("nats: read INFO: %w", err)
	}
	verb, args := parseControlLine(line)
	if verb != "INFO" {
		return serverInfo{}, fmt.Errorf("nats: expected INFO, got %q", verb)
	}
	var info serverInfo
	if err := json.Unmarshal([]byte(args), &info); err != nil {
		return serverInfo{}, fmt.Errorf("nats: parse INFO json: %w", err)
	}
	return info, nil
}

// awaitPong reads control frames until a PONG, answering a server PING and surfacing a
// -ERR (an auth or protocol rejection) as an error.
func awaitPong(r *bufio.Reader) error {
	for i := 0; i < 8; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("nats: await PONG: %w", err)
		}
		verb, args := parseControlLine(line)
		switch verb {
		case "PONG":
			return nil
		case "-ERR":
			return fmt.Errorf("nats: connect rejected: %s", args)
		case "PING", "+OK", "INFO":
			// keep reading
		default:
			// ignore unknown control during handshake
		}
	}
	return fmt.Errorf("nats: no PONG after handshake")
}

// newInbox returns a unique _INBOX.<random> subject for JetStream pull replies.
func newInbox() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("nats: inbox id: %w", err)
	}
	return "_INBOX." + hex.EncodeToString(b[:]), nil
}

// deadlineCtx returns a context bounded by d (or Background when d<=0) for the TLS
// handshake.
func deadlineCtx(d time.Duration) context.Context {
	if d <= 0 {
		return context.Background()
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	// The handshake completes well within d; leaking the cancel until GC is acceptable
	// here, but cancel on a timer to satisfy the vet/lint contract.
	time.AfterFunc(d, cancel)
	return ctx
}

// isTimeout reports whether err is an i/o timeout (the pull-window read deadline).
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}
