// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package syslog is the Olivares AI output connector that ships notifications to a
// syslog collector over its real transport — UDP (RFC 5426), TCP with RFC 6587
// framing (octet-counting or non-transparent), or TLS on port 6514 (RFC 5425)
// with optional mutual TLS. It is the missing last hop the SIEM connector
// did not have: Produced the RFC 5424 string but could only POST it to an
// HTTP collector; a real SOC forwards syslog to the relay/collector it already
// runs (rsyslog, syslog-ng, an ArcSight Syslog SmartConnector, a QRadar event
// collector), not to a bespoke webhook.
//
// The record itself is built by connectors/internal/siemfmt (RFC 5424 grammar,
// PRI/severity, structured data, escaping — all golden-tested in); this
// package adds only the TWO things siemfmt cannot: the wire FRAMING and the
// TRANSPORT. It also carries ArcSight CEF:0 and IBM QRadar LEEF 2.0 (04) as the MSG of a spec-correct RFC 5424 frame, which is exactly how those
// products ingest those formats over syslog.
//
// Secure by default (docs/SECURITY-HARDENING.md, fail-closed). The default transport is TLS: an
// operator who wants cleartext UDP/TCP must select it EXPLICITLY (transport=udp|
// tcp). There is no code path that downgrades a TLS destination to cleartext on
// error — a TLS dial that fails is reported, never silently retried in the clear.
// TLS verifies the server certificate against the system roots or an operator CA;
// a client certificate (mTLS) is loaded when configured. RFC 5425 requires
// octet-counting framing over TLS, so the connector forces it there regardless of
// the framing setting.
//
// Minimal data (docs/SECURITY-HARDENING.md): a Notification already carries only non-sensitive,
// displayable fields; this connector forwards what siemfmt encodes and adds no
// enrichment. It imports only the SDK and the Apache siemfmt helper, never the
// engine.
package syslog

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.syslog"

// transport is the wire transport for the syslog stream.
type transport string

const (
	transportTLS transport = "tls" // TLS over TCP, RFC 5425 (default; secure)
	transportTCP transport = "tcp" // plain TCP, RFC 6587 framing
	transportUDP transport = "udp" // plain UDP datagrams, RFC 5426
)

// framing is the RFC 6587 message framing used on a stream transport (TCP/TLS).
// UDP is unframed (one datagram per message, RFC 5426).
type framing string

const (
	// framingOctet is RFC 6587 §3.4.1 octet-counting: "MSG-LEN SP SYSLOG-MSG",
	// where MSG-LEN is the octet count of the syslog message. It is unambiguous
	// (the receiver reads exactly MSG-LEN bytes) and is REQUIRED over TLS by RFC
	// 5425, so it is the default.
	framingOctet framing = "octet-counting"
	// framingTransparent is RFC 6587 §3.4.2 non-transparent-framing: the message
	// is terminated by a trailer, here LF (0x0A), the most widely accepted one. A
	// record must therefore not contain a bare LF; siemwire already collapses CR/LF
	// in the record so this holds.
	framingTransparent framing = "non-transparent"
)

// formatSet is the connector's selection surface in the sdk/siemwire catalog:
// the deliberate line-oriented subset (native RFC 5424, plus CEF:0 / LEEF 2.0
// carried as the MSG of a 5424 frame). The local wireFormat const block this
// replaced was one of six diverged hand copies; the accepted set, the default,
// the operator-facing list and the alias resolution all derive from the
// catalog now via siemfmt.ResolveFormat.
func formatSet() siemwire.FormatSet { return siemwire.SyslogConnectorFormats() }

const (
	defaultTLSPort     = "6514" // RFC 5425 registered port for syslog-over-TLS
	defaultPlainPort   = "514"  // RFC 5424/5426 registered port for syslog
	defaultDialTimeout = 10 * time.Second
)

// Output is the syslog output connector. One instance is configured once (Open)
// and used for every notification (Notify). It holds the transport choice, the
// framing, the destination address, the device identity, an optional TLS config,
// and a lazily-dialed connection guarded by mu so concurrent Notify calls are
// safe (the runtime drives one output from a single goroutine, but the connector
// stays safe if shared).
type Output struct {
	transport transport
	address   string               // host:port (port defaulted per transport)
	format    siemwire.FormatToken // canonical encoder key, resolved at Open
	framing   framing
	hostname  string
	facility  int
	device    siemfmt.Device

	// maxPayloadBytes is the operator-declared receiver budget for one syslog
	// record, in bytes. 0 (the default) disables the check: the connector never
	// invents a limit the operator did not declare.
	maxPayloadBytes int

	tlsConfig   *tls.Config
	dialTimeout time.Duration

	// dialOverride lets a test substitute the transport with an in-memory or
	// loopback connection. nil in production, where dial builds the real
	// UDP/TCP/TLS connection from the configured transport and tlsConfig.
	dialOverride func(ctx context.Context) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a syslog output connector with secure defaults (TLS transport,
// octet-counting framing, native RFC 5424 format). Open must be called before any
// Notify.
func New() *Output {
	return &Output{
		transport:   transportTLS,
		format:      siemwire.Canonical(formatSet().Default()),
		framing:     framingOctet,
		dialTimeout: defaultDialTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Syslog",
		Description: "Ships notifications to a syslog collector over UDP (RFC 5426), TCP (RFC 6587 framing) or TLS 6514 (RFC 5425, mTLS optional). Carries RFC 5424, CEF:0 or LEEF 2.0. TLS by default, no cleartext fallback.",
		ConfigFields: []sdk.ConfigField{
			{Key: "transport", Type: sdk.FieldString, Default: string(transportTLS), Description: "Wire transport: tls (default, secure) | tcp | udp. Cleartext (tcp/udp) is an explicit opt-out."},
			{Key: "address", Type: sdk.FieldString, Required: true, Description: "Destination host:port (port defaults to 6514 for tls, 514 for tcp/udp)."},
			{Key: "format", Type: sdk.FieldString, Default: string(formatSet().Default()), Description: "Payload format (" + formatSet().List() + "): syslog is the native RFC 5424 record; cef and leef are CEF:0 / LEEF 2.0 carried as the MSG of a 5424 frame."},
			{Key: "framing", Type: sdk.FieldString, Default: string(framingOctet), Description: "Stream framing for tcp/tls: octet-counting (default; forced on tls per RFC 5425) | non-transparent (LF-terminated). Ignored for udp."},
			{Key: "hostname", Type: sdk.FieldString, Description: "Syslog HOSTNAME field; defaults to the NILVALUE '-' when empty."},
			{Key: "facility", Type: sdk.FieldInt, Description: "Syslog facility 0..23; defaults to local0 (16)."},
			{Key: "ca_file", Type: sdk.FieldString, Description: "PEM CA bundle to verify the server certificate (tls); empty uses the system roots."},
			{Key: "cert_file", Type: sdk.FieldString, Description: "PEM client certificate for mutual TLS (tls); requires key_file."},
			{Key: "key_file", Type: sdk.FieldString, Secret: true, Description: "PEM private key for the mTLS client certificate. Held in memory only, never logged."},
			{Key: "server_name", Type: sdk.FieldString, Description: "TLS server name (SNI / certificate verification); defaults to the host in address."},
			{Key: "tls_insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Disable server-certificate verification (discouraged; for a self-signed collector without a provided CA). Never disables TLS itself."},
			{Key: "vendor", Type: sdk.FieldString, Description: "siemfmt device vendor override (default Olivares.AI)."},
			{Key: "product", Type: sdk.FieldString, Description: "siemfmt device product override (default ControlPlane)."},
			{Key: "version", Type: sdk.FieldString, Description: "siemfmt device version override."},
			{Key: "dial_timeout", Type: sdk.FieldDuration, Default: defaultDialTimeout.String(), Description: "Connection dial and per-write deadline."},
			{Key: "max_payload_bytes", Type: sdk.FieldInt, Default: "0", Description: "Receiver budget for one record, in bytes; 0 (default) disables the check. A record over the budget fails the delivery (retried, then dead-lettered) instead of being silently split by the receiver. Known receiver figures: ArcSight's guides say a syslog-daemon message MIGHT be split past 1024 (not the file/pipe paths); QRadar TCP defaults to 4096, raisable (IBM documents 8192, ceiling 32000); RFC 5424 receivers must accept 480 and should accept 2048."},
		},
	}
}

// Open reads and validates configuration and, for TLS, builds the verified TLS
// config (loading the CA and any mTLS client certificate). A misconfiguration —
// unknown transport/format/framing, missing address, an unreadable CA or client
// keypair — is reported here, not deferred to Notify. The network connection is
// dialed lazily on first Notify (and re-dialed on a broken stream), so Open does
// not couple boot to the collector being reachable.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	switch transport(strings.ToLower(strings.TrimSpace(cfg.Get("transport")))) {
	case transportTLS, "":
		o.transport = transportTLS
	case transportTCP:
		o.transport = transportTCP
	case transportUDP:
		o.transport = transportUDP
	default:
		return fmt.Errorf("syslog: unknown transport %q (want tls|tcp|udp)", cfg.Get("transport"))
	}

	tok, err := siemfmt.ResolveFormat(formatSet(), cfg.Get("format"))
	if err != nil {
		return fmt.Errorf("syslog: %w", err)
	}
	o.format = tok

	switch framing(strings.ToLower(strings.TrimSpace(cfg.Get("framing")))) {
	case framingOctet, "":
		o.framing = framingOctet
	case framingTransparent:
		o.framing = framingTransparent
	default:
		return fmt.Errorf("syslog: unknown framing %q (want octet-counting|non-transparent)", cfg.Get("framing"))
	}
	// RFC 5425 §4.3: a syslog/TLS transport sender MUST use octet-counting framing.
	if o.transport == transportTLS {
		o.framing = framingOctet
	}

	addr := strings.TrimSpace(cfg.Get("address"))
	if addr == "" {
		return fmt.Errorf("syslog: address is required (host:port)")
	}
	o.address = withDefaultPort(addr, o.transport)

	o.hostname = strings.TrimSpace(cfg.Get("hostname"))
	o.facility = cfg.GetInt("facility", 0)
	o.dialTimeout = cfg.GetDuration("dial_timeout", defaultDialTimeout)

	o.maxPayloadBytes = cfg.GetInt("max_payload_bytes", 0)
	if o.maxPayloadBytes < 0 {
		return fmt.Errorf("syslog: max_payload_bytes must be >= 0 (0 disables the check), got %d", o.maxPayloadBytes)
	}

	o.device = siemfmt.DefaultDevice()
	if v := cfg.Get("vendor"); v != "" {
		o.device.Vendor = v
	}
	if v := cfg.Get("product"); v != "" {
		o.device.Product = v
	}
	if v := cfg.Get("version"); v != "" {
		o.device.Version = v
	}

	if o.transport == transportTLS {
		tc, err := buildTLSConfig(cfg, o.address)
		if err != nil {
			return err
		}
		o.tlsConfig = tc
	}
	return nil
}

// Notify encodes n in the configured format, frames it, and writes it to the
// destination, dialing (or re-dialing) the connection as needed. It returns an
// error if the connection cannot be established or the write fails. There is no
// destination-side acknowledgement for a TRAP-style syslog stream (UDP has none;
// a TCP/TLS write confirms the bytes left the socket), so success means the frame
// was written, consistent with how syslog forwarding works.
//
// When the operator declares the receiver's payload budget (max_payload_bytes),
// a record over that budget fails the delivery rather than being handed to a
// receiver that would split it: a split record is two unparseable halves of one
// auditable event, so the honest outcome is the engine's retry/dead-letter path.
// The record itself is never truncated, and the error carries byte counts only.
// The budget is measured on the RECORD (what the receiver reassembles), not on
// the octet-counting frame prefix, which is transport overhead.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.address == "" {
		return fmt.Errorf("syslog: connector not opened")
	}
	record, err := o.encode(n)
	if err != nil {
		return err
	}
	if o.maxPayloadBytes > 0 && siemwire.ExceedsTransportBudget(record, o.maxPayloadBytes) {
		return fmt.Errorf("syslog: record is %d bytes, over the declared %d-byte receiver budget for %s (not sent: a receiver that splits it would corrupt the event)",
			len(record), o.maxPayloadBytes, o.transport)
	}
	return o.send(ctx, o.frame(record))
}

// Close closes the underlying connection if one is open. Safe to call even if Open
// failed or no Notify ever ran.
func (o *Output) Close(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn != nil {
		err := o.conn.Close()
		o.conn = nil
		return err
	}
	return nil
}

// encode renders n as the configured payload: a native RFC 5424 record, or a
// CEF:0/LEEF 2.0 record carried as the MSG of an RFC 5424 frame. A format
// outside the catalog subset is corrupted internal state (Open resolves every
// stored spelling) and errors instead of silently relabelling as syslog.
func (o *Output) encode(n sdk.Notification) (string, error) {
	opts := siemfmt.SyslogOptions{Hostname: o.hostname, Facility: o.facility}
	switch o.format {
	case siemwire.TokenCEF:
		return siemfmt.SyslogWithMsg(o.device, opts, n, siemfmt.CEF(o.device, n)), nil
	case siemwire.TokenLEEF:
		return siemfmt.SyslogWithMsg(o.device, opts, n, siemfmt.LEEF(o.device, n)), nil
	case siemwire.TokenSyslog:
		return siemfmt.Syslog5424(o.device, opts, n), nil
	default:
		return "", fmt.Errorf("syslog: unrecognized stored format %q", o.format)
	}
}

// frame applies the wire framing for the transport: UDP sends the record as one
// datagram (no framing, RFC 5426); a stream transport uses octet-counting
// ("LEN SP MSG", RFC 6587 §3.4.1) or non-transparent LF-termination (§3.4.2).
func (o *Output) frame(record string) []byte {
	if o.transport == transportUDP {
		return []byte(record)
	}
	if o.framing == framingTransparent {
		return []byte(record + "\n")
	}
	return []byte(strconv.Itoa(len(record)) + " " + record)
}

// send writes frame to the connection, dialing on first use and re-dialing once
// on a write error (a TCP/TLS stream a collector restarted, an idle connection a
// middlebox dropped). It holds mu so a shared connector serializes writes and
// connection swaps.
func (o *Output) send(ctx context.Context, frame []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.conn == nil {
		c, err := o.dial(ctx)
		if err != nil {
			return err
		}
		o.conn = c
	}

	if err := o.writeAll(ctx, o.conn, frame); err != nil {
		// The stream is suspect: drop it and try one fresh connection. This never
		// changes transport — a TLS destination re-dials TLS, never cleartext.
		_ = o.conn.Close()
		o.conn = nil
		c, derr := o.dial(ctx)
		if derr != nil {
			return fmt.Errorf("syslog: re-dial after write error: %w", derr)
		}
		o.conn = c
		if werr := o.writeAll(ctx, o.conn, frame); werr != nil {
			_ = o.conn.Close()
			o.conn = nil
			return werr
		}
	}
	return nil
}

// writeAll writes the full frame, applying a write deadline derived from ctx (or
// the dial timeout) so a stuck collector cannot block the runtime's delivery
// goroutine indefinitely.
func (o *Output) writeAll(ctx context.Context, conn net.Conn, frame []byte) error {
	deadline := time.Now().Add(o.dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetWriteDeadline(deadline)
	n, err := conn.Write(frame)
	if err != nil {
		return fmt.Errorf("syslog: write to %s: %w", o.transport, err)
	}
	if n < len(frame) {
		return fmt.Errorf("syslog: short write to %s (%d/%d bytes)", o.transport, n, len(frame))
	}
	return nil
}

// dial establishes the transport connection. A test may inject dialOverride; in
// production it builds a UDP, TCP, or TLS connection honoring ctx and the dial
// timeout. The TLS dial uses the verified config built in Open.
func (o *Output) dial(ctx context.Context) (net.Conn, error) {
	if o.dialOverride != nil {
		return o.dialOverride(ctx)
	}
	dctx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		dctx, cancel = context.WithTimeout(ctx, o.dialTimeout)
		defer cancel()
	}
	nd := &net.Dialer{Timeout: o.dialTimeout}
	switch o.transport {
	case transportUDP:
		c, err := nd.DialContext(dctx, "udp", o.address)
		if err != nil {
			return nil, fmt.Errorf("syslog: dial udp: %w", err)
		}
		return c, nil
	case transportTCP:
		c, err := nd.DialContext(dctx, "tcp", o.address)
		if err != nil {
			return nil, fmt.Errorf("syslog: dial tcp: %w", err)
		}
		return c, nil
	case transportTLS:
		td := &tls.Dialer{NetDialer: nd, Config: o.tlsConfig}
		c, err := td.DialContext(dctx, "tcp", o.address)
		if err != nil {
			// Fail closed: a TLS handshake/verification failure is reported, never
			// downgraded to a cleartext connection.
			return nil, fmt.Errorf("syslog: dial tls: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("syslog: unknown transport %q", o.transport)
	}
}

// buildTLSConfig assembles the verified TLS config for the TLS transport: TLS 1.2+,
// the server name (operator override or the host in address), an optional operator
// CA bundle (else system roots), an optional mTLS client certificate, and the
// explicit (default-off) skip-verify opt-out. A bad CA or keypair is a
// configuration error returned from Open.
func buildTLSConfig(cfg sdk.Config, address string) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12}

	if sn := strings.TrimSpace(cfg.Get("server_name")); sn != "" {
		tc.ServerName = sn
	} else if host, _, err := net.SplitHostPort(address); err == nil {
		tc.ServerName = host
	}

	if caFile := strings.TrimSpace(cfg.Get("ca_file")); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("syslog: read ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("syslog: ca_file %q contains no valid PEM certificate", caFile)
		}
		tc.RootCAs = pool
	}

	certFile := strings.TrimSpace(cfg.Get("cert_file"))
	keyFile := strings.TrimSpace(cfg.Get("key_file"))
	switch {
	case certFile != "" && keyFile != "":
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("syslog: load mTLS client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{pair}
	case certFile != "" || keyFile != "":
		return nil, fmt.Errorf("syslog: cert_file and key_file must be set together for mTLS")
	}

	tc.InsecureSkipVerify = cfg.GetBool("tls_insecure_skip_verify", false) //nolint:gosec // explicit, default-off operator opt-out
	return tc, nil
}

// withDefaultPort returns addr unchanged if it already has a port, otherwise it
// appends the transport's default syslog port (6514 for TLS, 514 for cleartext).
func withDefaultPort(addr string, t transport) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	port := defaultPlainPort
	if t == transportTLS {
		port = defaultTLSPort
	}
	return net.JoinHostPort(addr, port)
}
