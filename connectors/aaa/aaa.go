// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package aaa is the Olivares AI connector that ingests AAA (Authentication,
// Authorization, Accounting) telemetry from RADIUS and TACACS+ — the pre-AI
// device-admin / 802.1X control plane a real SOC/NOC runs on, which the cloud-IdP
// connectors do not see. It OBSERVES (docs/SECURITY-HARDENING.md): it tails the
// NAS / AAA-server logs the operator already writes, or runs a hardened inbound
// RADIUS accounting receiver, and emits FindingReports for the security-relevant
// signals — a failed authentication and a privileged device-admin session — tied
// to the principal so they converge with the directory roster by name.
//
// Protocols and authorities:
//   - RADIUS — RFC 2865 (auth, Draft Standard, 2000) + RFC 2866 (accounting,
//     Informational, 2000). Packet codes and attribute types are read verbatim
//     from the registries.
//   - TACACS+ — RFC 8907 (the canonical TACACS+ protocol, Informational, 2020);
//     RFC 9887 (TACACS+ over TLS 1.3, Proposed Standard, 2025) is the modern
//     hardened transport that UPDATES 8907 (it does not replace it). The text
//     accounting log this connector parses is the on-disk record of those packets.
//
// Minimal data (docs/SECURITY-HARDENING.md-3): the connector reads identity/accounting METADATA
// only. It NEVER reads the RADIUS shared secret (which is never on the wire —
// RFC 2865 §3) and NEVER reads the User-Password attribute (the obfuscated
// credential). A TACACS+ command (cmd=) can carry a secret, so it is redacted and
// hashed into the finding's de-dup detail and never displayed. It imports only the
// SDK and the shared Apache helpers — never the engine.
//
// Security posture: the inbound RADIUS receiver is the hardened exception to
// the read-first/no-listener default (docs/SECURITY-HARDENING.md). It binds LOOPBACK by default and
// REFUSES a non-loopback bind unless the operator explicitly accepts the risk — it
// does not weaken the secure default; it makes the one inbound surface opt-in and
// loud, exactly as the claude OTLP receiver does.
package aaa

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/logtail"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.aaa"

// Ingestion modes.
const (
	modeRADIUSDetail = "radius-detail" // tail a FreeRADIUS "detail" accounting/auth file
	modeTACACS       = "tacacs"        // tail a tac_plus accounting log
	modeRADIUSUDP    = "radius-udp"    // hardened inbound RADIUS accounting receiver (loopback default)
)

// defaultRADIUSAcctAddr is the loopback default for the inbound receiver. 1813 is
// the IANA RADIUS accounting port; the bind is loopback so it is opt-in to expose.
const defaultRADIUSAcctAddr = "127.0.0.1:1813"

// Event proto + kind vocabulary shared by the RADIUS and TACACS+ decoders.
const (
	protoRADIUS = "radius"
	protoTACACS = "tacacs"

	kindAuthReject   = "auth-reject"
	kindAuthAccept   = "auth-accept"
	kindAcctStart    = "acct-start"
	kindAcctStop     = "acct-stop"
	kindAcctInterim  = "acct-interim"
	kindAcctNASState = "acct-nas-state"
	kindAccounting   = "accounting"
	kindOther        = "other"
)

// aaaEvent is the normalized, minimal-data view of one AAA record (no shared
// secret, no password, no raw command).
type aaaEvent struct {
	proto       string
	kind        string
	user        string
	nas         string
	device      string
	service     string
	privLvl     string
	deviceAdmin bool
	occurred    time.Time
	detail      string // non-sensitive; redacted before it is hashed
}

// Source tails an AAA log or runs the inbound receiver and emits findings. It
// satisfies sdk.SourceConnector and holds no directory roster (AAA is an event
// stream, not a directory), so it is a finding source, not a GraphProvider.
type Source struct {
	mode            string
	logPath         string
	follow          bool
	listenAddr      string
	allowPublicBind bool

	conn net.PacketConn   // bound in Open for radius-udp (so a bind error surfaces early)
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns an aaa connector with default configuration (tail a RADIUS detail file).
func New() *Source { return &Source{mode: modeRADIUSDetail, follow: true} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AAA — RADIUS / TACACS+",
		Description: "Observes RADIUS (RFC 2865/2866) and TACACS+ (RFC 8907 / 9887-over-TLS) AAA telemetry and emits device-admin and auth-failure findings. Never the shared secret or a password.",
		ConfigFields: []sdk.ConfigField{
			{Key: "mode", Type: sdk.FieldString, Default: modeRADIUSDetail, Description: "radius-detail | tacacs (tail a log), or radius-udp (hardened inbound accounting receiver, loopback default)."},
			{Key: "log_path", Type: sdk.FieldString, Description: "Path to the AAA log to tail (read-only). Empty in a tail mode = source does nothing (the boot warns)."},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "Keep tailing appended lines (true) or read once to EOF and stop (false, batch)."},
			{Key: "listen_addr", Type: sdk.FieldString, Default: defaultRADIUSAcctAddr, Description: "radius-udp bind address. Loopback by default; a non-loopback bind is refused unless allow_public_bind=true."},
			{Key: "allow_public_bind", Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the inbound RADIUS receiver to a non-loopback address. Keep loopback (secure default)."},
		},
	}
}

// Open reads configuration and, for the inbound receiver, binds the loopback
// listener now so a bind/permission error surfaces here, not mid-Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.mode = strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.Get("mode"), modeRADIUSDetail)))
	s.logPath = strings.TrimSpace(cfg.Get("log_path"))
	s.follow = cfg.GetBool("follow", true)
	s.listenAddr = firstNonEmpty(strings.TrimSpace(cfg.Get("listen_addr")), defaultRADIUSAcctAddr)
	s.allowPublicBind = cfg.GetBool("allow_public_bind", false)

	switch s.mode {
	case modeRADIUSDetail, modeTACACS:
		return nil
	case modeRADIUSUDP:
		// One admission point for every socket this product opens. RADIUS
		// accounting is UDP in the clear, with a shared secret that only covers
		// part of the packet.
		conn, err := netbind.ListenPacket(context.Background(), "udp", s.listenAddr, netbind.Policy{
			Component:   "aaa",
			Purpose:     "RADIUS accounting receiver",
			AllowPublic: s.allowPublicBind,
			OptIn:       "allow_public_bind",
		})
		if err != nil {
			return fmt.Errorf("aaa: bind RADIUS receiver %s: %w", s.listenAddr, err)
		}
		s.conn = conn
		return nil
	default:
		return fmt.Errorf("aaa: unknown mode %q (want %q, %q or %q)", s.mode, modeRADIUSDetail, modeTACACS, modeRADIUSUDP)
	}
}

// Gather runs the configured ingestion until ctx is done (or, in batch tail mode,
// until EOF). It emits a FindingReport for each security-relevant AAA signal.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	switch s.mode {
	case modeRADIUSUDP:
		return s.gatherUDP(ctx, sink)
	case modeTACACS:
		return s.tail(ctx, sink, func(line string) []aaaEvent {
			if ev, ok := parseTACACSLine(line); ok {
				ev.occurred = s.clock()
				return []aaaEvent{ev}
			}
			return nil
		})
	case modeRADIUSDetail:
		return s.tailRADIUSDetail(ctx, sink)
	default:
		return nil
	}
}

// Close releases the inbound listener.
func (s *Source) Close(context.Context) error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// gatherUDP reads RADIUS accounting packets from the loopback listener and emits
// findings. It polls with a short read deadline so it returns promptly on ctx
// cancellation WITHOUT closing the listener itself — the runtime owns the single
// Close after Gather returns (the SourceConnector lifecycle), avoiding a
// double-close.
func (s *Source) gatherUDP(ctx context.Context, sink sdk.Sink) error {
	if s.conn == nil {
		return nil
	}
	buf := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, _, err := s.conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // deadline tick; re-check ctx and read again
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil // listener closed; stop cleanly
		}
		ev, derr := decodeRADIUS(buf[:n])
		if derr != nil {
			continue // a malformed packet is dropped, never fatal
		}
		ev.occurred = s.clock()
		for _, f := range s.derive(ev) {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}
}

// tail tails a single-line-record log, applying parse to each line.
func (s *Source) tail(ctx context.Context, sink sdk.Sink, parse func(line string) []aaaEvent) error {
	if s.logPath == "" {
		return nil // nothing configured; wiring emits the visible warning (12 §5)
	}
	return logtail.Tail(ctx, s.logPath, logtail.Options{Follow: s.follow}, func(line []byte) error {
		for _, ev := range parse(string(line)) {
			for _, f := range s.derive(ev) {
				if err := sink.Emit(ctx, f); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// tailRADIUSDetail tails a FreeRADIUS detail file, assembling multi-line records
// (a non-indented header line begins a record; tab/space-indented lines are its
// attributes) and flushing the final record at EOF in batch mode.
func (s *Source) tailRADIUSDetail(ctx context.Context, sink sdk.Sink) error {
	if s.logPath == "" {
		return nil
	}
	var header string
	var attrs []string
	flush := func() error {
		if header == "" && len(attrs) == 0 {
			return nil
		}
		if ev, ok := parseRADIUSDetailRecord(header, attrs); ok {
			ev.occurred = s.clock()
			for _, f := range s.derive(ev) {
				if err := sink.Emit(ctx, f); err != nil {
					return err
				}
			}
		}
		header, attrs = "", nil
		return nil
	}
	err := logtail.Tail(ctx, s.logPath, logtail.Options{Follow: s.follow}, func(line []byte) error {
		l := string(line)
		if len(l) > 0 && (l[0] == '\t' || l[0] == ' ') {
			attrs = append(attrs, l)
			return nil
		}
		// A new non-indented line starts a new record: flush the previous one first.
		if err := flush(); err != nil {
			return err
		}
		header = l
		return nil
	})
	if err != nil {
		return err
	}
	return flush() // EOF (batch): emit the trailing record
}

// derive turns a normalized AAA event into zero or more findings: a failed
// authentication (Medium) and a privileged device-admin session (Info). Ordinary
// network-access accounting is high-volume telemetry, not a finding — it is left
// to SIEM export; a stateful brute-force aggregation is a documented seam.
func (s *Source) derive(ev aaaEvent) []model.FindingReport {
	switch {
	case ev.kind == kindAuthReject:
		return []model.FindingReport{{
			Kind:        "aaa_auth_reject",
			Severity:    model.SeverityMedium,
			SubjectKind: "identity",
			SubjectRef:  ev.user,
			Title:       fmt.Sprintf("%s authentication rejected for %q at NAS %q", strings.ToUpper(ev.proto), ev.user, redact.Clean(ev.nas)),
			DetailHash:  redact.Hash(ev.detail),
			OccurredAt:  ev.occurred,
		}}
	case ev.deviceAdmin && (ev.kind == kindAcctStart || ev.kind == kindAcctStop || ev.kind == kindAuthAccept):
		title := fmt.Sprintf("Device-admin session (%s/%s) by %q on NAS %q", ev.proto, ev.kind, ev.user, redact.Clean(ev.nas))
		if ev.service != "" || ev.privLvl != "" {
			title += fmt.Sprintf(" [service=%s priv-lvl=%s]", ev.service, ev.privLvl)
		}
		return []model.FindingReport{{
			Kind:        "aaa_device_admin",
			Severity:    model.SeverityInfo,
			SubjectKind: "identity",
			SubjectRef:  ev.user,
			Title:       title,
			DetailHash:  redact.Hash(ev.detail),
			OccurredAt:  ev.occurred,
		}}
	default:
		return nil
	}
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
