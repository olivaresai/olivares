// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package snmp is the Olivares AI output connector that emits notifications as
// SNMPv3 traps (or informs) to a network management station (NMS) — the egress a
// NOC expects and that Slack/PagerDuty cannot reach. It speaks SNMPv3 with the
// User-based Security Model (USM) at securityLevel authPriv ONLY (HMAC-SHA
// authentication + AES privacy): there is no noAuth/noPriv path, secure by default
// and fail-closed (docs/SECURITY-HARDENING.md) — an unauthenticated/cleartext trap is never sent.
//
// The trap carries the RFC 3416 mandatory varbinds (sysUpTime.0, prepended by the
// SNMP layer, then snmpTrapOID.0 = the Olivares notification OID) followed by the
// notification's non-sensitive fields under a minimal private MIB rooted at the
// configured Private Enterprise Number. The DEFAULT PEN is 32473, which RFC 5612
// reserves for documentation/examples — a real deployment overrides it with its
// own registered PEN. No PEN is invented or claimed as assigned.
//
// Dependency isolation. SNMPv3 USM (engine-localized HMAC keys, AES-CFB privacy,
// ASN.1 BER message assembly) is genuinely hard to get right; this connector uses
// github.com/gosnmp/gosnmp (BSD-licensed, the de-facto Go SNMP library). The
// dependency is confined to THIS connector and never reaches /core or the default
// binary surface beyond the connectors module. Minimal data (docs/SECURITY-HARDENING.md): only the
// displayable Notification fields ride the trap; the USM passphrases are held in
// memory only and never logged.
package snmp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.snmp"

const (
	defaultPort        = 162
	defaultPEN         = 32473 // RFC 5612 documentation/example PEN; overridable
	defaultTimeout     = 5 * time.Second
	defaultRetries     = 1
	defaultEngineBoots = 1
	// snmpTrapOID is the OID whose value (the second varbind) names the notification
	// type per RFC 3416 §4.2.6.
	snmpTrapOID = "1.3.6.1.6.3.1.1.4.1.0"
)

// trapSender is the minimal SNMP capability the connector needs, so a test can
// substitute a recorder. *gosnmp.GoSNMP satisfies it.
type trapSender interface {
	Connect() error
	SendTrap(trap gosnmp.SnmpTrap) (*gosnmp.SnmpPacket, error)
	Close() error
}

// Output is the SNMPv3 trap output connector. Open builds and connects the USM
// session; Notify builds and sends one trap/inform; Close closes the socket.
type Output struct {
	base   string // private MIB base OID, "1.3.6.1.4.1.<PEN>"
	inform bool

	mu     sync.Mutex
	sender trapSender

	// newSender builds the SNMP session from the resolved config; a test overrides
	// it to inject a recorder. nil => the real gosnmp session.
	newSender func() (trapSender, error)
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns an SNMPv3 output connector. Open must be called before any Notify.
func New() *Output { return &Output{} }

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "SNMPv3 trap",
		Description: "Emits notifications as SNMPv3 USM authPriv traps/informs to an NMS, under a minimal private MIB. Auth+priv only (no noAuth/noPriv).",
		ConfigFields: []sdk.ConfigField{
			{Key: "target", Type: sdk.FieldString, Required: true, Description: "NMS host or IP."},
			{Key: "port", Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultPort), Description: "NMS trap port (default 162)."},
			{Key: "inform", Type: sdk.FieldBool, Default: "false", Description: "Send acknowledged InformRequests instead of unconfirmed traps."},
			{Key: "username", Type: sdk.FieldString, Required: true, Description: "USM security user name."},
			{Key: "auth_protocol", Type: sdk.FieldString, Default: "sha256", Description: "USM auth protocol: sha256 (default) | sha | sha224 | sha384 | sha512 | md5."},
			{Key: "auth_passphrase", Type: sdk.FieldString, Required: true, Secret: true, Description: "USM authentication passphrase. Held in memory only, never logged."},
			{Key: "priv_protocol", Type: sdk.FieldString, Default: "aes", Description: "USM privacy protocol: aes (default, AES-128) | aes192 | aes256 | des."},
			{Key: "priv_passphrase", Type: sdk.FieldString, Required: true, Secret: true, Description: "USM privacy passphrase. Held in memory only, never logged."},
			{Key: "engine_id", Type: sdk.FieldString, Description: "Authoritative engine ID (hex). Defaults to an enterprise-format ID derived from the PEN and target."},
			{Key: "engine_boots", Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultEngineBoots), Description: "USM authoritativeEngineBoots."},
			{Key: "engine_time", Type: sdk.FieldInt, Default: "0", Description: "USM authoritativeEngineTime (seconds)."},
			{Key: "pen", Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultPEN), Description: "Private Enterprise Number for the trap OIDs. Default 32473 (RFC 5612 example PEN) — set your registered PEN."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "Per-request timeout (informs wait for the ack)."},
			{Key: "retries", Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultRetries), Description: "Inform retry count (traps are unconfirmed)."},
		},
	}
}

// Open resolves configuration, builds the USM authPriv session and connects it.
// Missing target/username/passphrases or an unknown protocol/engine-id is reported
// here. The UDP socket is opened in Connect (UDP has no handshake, so a down NMS
// does not fail Open).
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	target := strings.TrimSpace(cfg.Get("target"))
	if target == "" {
		return fmt.Errorf("snmp: target is required")
	}
	username := strings.TrimSpace(cfg.Get("username"))
	if username == "" {
		return fmt.Errorf("snmp: username is required")
	}
	authPass := cfg.Get("auth_passphrase")
	privPass := cfg.Get("priv_passphrase")
	if authPass == "" || privPass == "" {
		return fmt.Errorf("snmp: auth_passphrase and priv_passphrase are required (authPriv only)")
	}
	authProto, err := authProtocol(cfg.Get("auth_protocol"))
	if err != nil {
		return err
	}
	privProto, err := privProtocol(cfg.Get("priv_protocol"))
	if err != nil {
		return err
	}

	pen := cfg.GetInt("pen", defaultPEN)
	o.base = fmt.Sprintf("1.3.6.1.4.1.%d", pen)
	o.inform = cfg.GetBool("inform", false)

	engineID, err := resolveEngineID(cfg.Get("engine_id"), pen, target)
	if err != nil {
		return err
	}

	sc := sessionConfig{
		target:    target,
		port:      cfg.GetInt("port", defaultPort),
		timeout:   cfg.GetDuration("timeout", defaultTimeout),
		retries:   cfg.GetInt("retries", defaultRetries),
		username:  username,
		authProto: authProto,
		authPass:  authPass,
		privProto: privProto,
		privPass:  privPass,
		engineID:  engineID,
		boots:     uint32(cfg.GetInt("engine_boots", defaultEngineBoots)), //nolint:gosec
		etime:     uint32(cfg.GetInt("engine_time", 0)),                   //nolint:gosec
	}

	if o.newSender == nil {
		o.newSender = func() (trapSender, error) {
			return &gosnmpSession{g: sc.gosnmp()}, nil
		}
	}

	sender, err := o.newSender()
	if err != nil {
		return err
	}
	if err := sender.Connect(); err != nil {
		return fmt.Errorf("snmp: connect: %w", err)
	}
	o.sender = sender
	return nil
}

// Notify builds a trap (or inform) from n and sends it. For a trap (unconfirmed)
// success means the datagram was sent; for an inform it means the NMS acknowledged.
func (o *Output) Notify(_ context.Context, n sdk.Notification) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sender == nil {
		return fmt.Errorf("snmp: connector not opened")
	}
	if _, err := o.sender.SendTrap(o.buildTrap(n)); err != nil {
		return fmt.Errorf("snmp: send %s: %w", trapKind(o.inform), err)
	}
	return nil
}

// Close closes the SNMP socket.
func (o *Output) Close(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sender != nil {
		err := o.sender.Close()
		o.sender = nil
		return err
	}
	return nil
}

// buildTrap assembles the SNMPv2-Trap varbind list. The first varbind is
// snmpTrapOID.0 (its value is the Olivares notification OID); the SNMP layer
// prepends sysUpTime.0. The remaining varbinds carry the notification's
// non-sensitive fields under the private MIB; empty values are omitted.
func (o *Output) buildTrap(n sdk.Notification) gosnmp.SnmpTrap {
	vars := []gosnmp.SnmpPDU{
		{Name: snmpTrapOID, Type: gosnmp.ObjectIdentifier, Value: o.base + ".0.1"},
	}
	label, num := severity(n.Severity)
	add := func(suffix, v string) {
		if v != "" {
			vars = append(vars, gosnmp.SnmpPDU{Name: o.base + suffix, Type: gosnmp.OctetString, Value: v})
		}
	}
	add(".1.1", n.Type)
	add(".1.2", label)
	vars = append(vars, gosnmp.SnmpPDU{Name: o.base + ".1.3", Type: gosnmp.Integer, Value: num})
	add(".1.4", n.Title)
	add(".1.5", n.Body)
	add(".1.6", n.Tenant)
	if !n.Time.IsZero() {
		add(".1.7", n.Time.UTC().Format(time.RFC3339))
	}
	if len(n.Fields) > 0 {
		if b, err := json.Marshal(n.Fields); err == nil {
			add(".1.10", string(b))
		}
	}
	return gosnmp.SnmpTrap{Variables: vars, IsInform: o.inform}
}

// severity maps the product severity onto the SNMP trap's (label, numeric) pair.
// The numeric scale is the same 0..10 the SIEM severity table uses, so an NMS rule
// and a SIEM rule agree.
func severity(s model.Severity) (string, int) {
	switch s {
	case model.SeverityInfo:
		return "info", 1
	case model.SeverityLow:
		return "low", 3
	case model.SeverityMedium:
		return "medium", 5
	case model.SeverityHigh:
		return "high", 7
	case model.SeverityCritical:
		return "critical", 10
	default:
		return "", 0
	}
}

func trapKind(inform bool) string {
	if inform {
		return "inform"
	}
	return "trap"
}

// authProtocol maps a config string onto a gosnmp USM auth protocol (SHA-2 family
// preferred per RFC 7860). An empty value defaults to SHA-256.
func authProtocol(s string) (gosnmp.SnmpV3AuthProtocol, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "sha256":
		return gosnmp.SHA256, nil
	case "sha", "sha1":
		return gosnmp.SHA, nil
	case "sha224":
		return gosnmp.SHA224, nil
	case "sha384":
		return gosnmp.SHA384, nil
	case "sha512":
		return gosnmp.SHA512, nil
	case "md5":
		return gosnmp.MD5, nil
	default:
		return 0, fmt.Errorf("snmp: unknown auth_protocol %q", s)
	}
}

// privProtocol maps a config string onto a gosnmp USM privacy protocol (AES-128 per
// RFC 3826 default). An empty value defaults to AES.
func privProtocol(s string) (gosnmp.SnmpV3PrivProtocol, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "aes", "aes128":
		return gosnmp.AES, nil
	case "aes192":
		return gosnmp.AES192, nil
	case "aes256":
		return gosnmp.AES256, nil
	case "des":
		return gosnmp.DES, nil
	default:
		return 0, fmt.Errorf("snmp: unknown priv_protocol %q", s)
	}
}

// resolveEngineID returns the authoritative engine ID bytes (as a string) from an
// operator hex value, or derives a valid RFC 3411 enterprise-format engine ID from
// the PEN and a stable seed when none is configured.
func resolveEngineID(hexID string, pen int, seed string) (string, error) {
	hexID = strings.TrimSpace(hexID)
	if hexID != "" {
		b, err := hex.DecodeString(strings.TrimPrefix(hexID, "0x"))
		if err != nil {
			return "", fmt.Errorf("snmp: engine_id must be hex: %w", err)
		}
		return string(b), nil
	}
	// RFC 3411 §4: enterprise format — 4 octets PEN with the high bit of the field
	// set, a format octet (0x04 = administratively assigned text), then a stable id.
	b := []byte{
		0x80 | byte(uint32(pen)>>24), byte(uint32(pen) >> 16), byte(uint32(pen) >> 8), byte(uint32(pen)), //nolint:gosec
		0x04,
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	sum := h.Sum32()
	b = append(b, byte(sum>>24), byte(sum>>16), byte(sum>>8), byte(sum))
	return string(b), nil
}

// sessionConfig is the resolved set of parameters for one SNMPv3 USM session,
// kept as a value so the gosnmp construction is a single, separately-testable
// function (a test asserts the secure-by-default authPriv configuration without a
// live NMS).
type sessionConfig struct {
	target    string
	port      int
	timeout   time.Duration
	retries   int
	username  string
	authProto gosnmp.SnmpV3AuthProtocol
	authPass  string
	privProto gosnmp.SnmpV3PrivProtocol
	privPass  string
	engineID  string
	boots     uint32
	etime     uint32
}

// gosnmp builds the *gosnmp.GoSNMP session: SNMPv3, USM, securityLevel authPriv —
// always (there is no code path that produces a noAuth/noPriv session).
func (sc sessionConfig) gosnmp() *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{
		Target:        sc.target,
		Port:          uint16(sc.port), //nolint:gosec
		Transport:     "udp",
		Version:       gosnmp.Version3,
		Timeout:       sc.timeout,
		Retries:       sc.retries,
		MaxOids:       gosnmp.MaxOids,
		SecurityModel: gosnmp.UserSecurityModel,
		MsgFlags:      gosnmp.AuthPriv,
		SecurityParameters: &gosnmp.UsmSecurityParameters{
			UserName:                 sc.username,
			AuthenticationProtocol:   sc.authProto,
			AuthenticationPassphrase: sc.authPass,
			PrivacyProtocol:          sc.privProto,
			PrivacyPassphrase:        sc.privPass,
			AuthoritativeEngineID:    sc.engineID,
			AuthoritativeEngineBoots: sc.boots,
			AuthoritativeEngineTime:  sc.etime,
		},
	}
}

// gosnmpSession adapts *gosnmp.GoSNMP to trapSender (Close closes its Conn).
type gosnmpSession struct{ g *gosnmp.GoSNMP }

func (s *gosnmpSession) Connect() error { return s.g.Connect() }
func (s *gosnmpSession) SendTrap(trap gosnmp.SnmpTrap) (*gosnmp.SnmpPacket, error) {
	return s.g.SendTrap(trap)
}
func (s *gosnmpSession) Close() error {
	if s.g.Conn != nil {
		return s.g.Conn.Close()
	}
	return nil
}
