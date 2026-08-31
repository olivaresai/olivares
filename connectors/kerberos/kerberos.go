// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package kerberos is the Olivares AI connector that OBSERVES the Kerberos/AD
// authentication plane and derives the Kerberoasting signal against the service
// accounts an AI estate runs on. It does NOT implement a KDC and
// never authenticates as one: it tails the KDC's existing auth telemetry —
// Windows Security events 4768 (TGT, AS-REQ) and 4769 (service ticket, TGS-REQ),
// or an MIT/Heimdal krb5kdc log — read-only, and emits FindingReports (docs/SECURITY-HARDENING.md
// §0: a connector observes, it does not interpose).
//
// The Kerberoasting signal (RFC 4120 + the IANA Kerberos etype registry): a
// TGS-REQ for a user service-principal answered with a WEAK ticket cipher —
// RC4-HMAC (etype 23 / 0x17) or legacy DES (etypes 1/3) — is the cipher downgrade
// an attacker forces so the service ticket can be cracked offline for the service
// account's password. A machine account (SPN ending in '$') is excluded: it
// rotates its own strong key, so a weak-cipher TGS to it is not the roasting
// pattern. The finding is tied to the service account's NHI so it converges with
// the directory roster by name.
//
// Minimal data (docs/SECURITY-HARDENING.md-3): the connector reads event METADATA only — event
// id, the service principal, the requesting account, the ticket encryption type,
// the source address. It NEVER reads, stores or transmits a TGT, a service
// ticket, a password hash or a keytab. The free-text detail is hashed (redact.Hash)
// so a finding can be de-duplicated without persisting any payload. It imports only
// the SDK and the shared Apache helpers — never the engine.
package kerberos

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/logtail"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.kerberos"

// Supported log formats.
const (
	formatWinEventJSON = "winevent-json" // one JSON object per line (Windows Security 4768/4769)
	formatMITKDC       = "mit-kdc"       // MIT/Heimdal krb5kdc.log line format
)

// Windows Security audit event ids for Kerberos authentication-service and
// ticket-granting-service requests (RFC 4120 AS-REQ / TGS-REQ).
const (
	eventTGT = 4768 // Kerberos authentication ticket (TGT) was requested (AS-REQ)
	eventTGS = 4769 // Kerberos service ticket was requested (TGS-REQ)
)

// Source tails a KDC auth log and emits Kerberoasting / weak-cipher findings. It
// satisfies sdk.SourceConnector and holds no directory roster (it observes the
// auth plane, not a directory), so it is a finding source, not a GraphProvider.
type Source struct {
	logPath string
	format  string
	follow  bool

	now func() time.Time // injectable clock (tests); nil => time.Now
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a kerberos connector with default configuration.
func New() *Source { return &Source{format: formatWinEventJSON, follow: true} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Kerberos / AD authentication telemetry",
		Description: "Observes KDC auth events (Windows 4768/4769 or MIT/Heimdal krb5kdc) read-only and derives the Kerberoasting signal. Never reads a ticket, hash or keytab.",
		ConfigFields: []sdk.ConfigField{
			{Key: "log_path", Type: sdk.FieldString, Description: "Path to the KDC auth log to tail (read-only). Empty = source does nothing (the boot warns)."},
			{Key: "format", Type: sdk.FieldString, Default: formatWinEventJSON, Description: "Log format: winevent-json (Windows Security 4768/4769) or mit-kdc (krb5kdc.log)."},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "Keep tailing appended lines (true, default) or read once to EOF and stop (false, batch)."},
		},
	}
}

// Open reads configuration and validates the format. It performs no I/O (the log
// is opened in Gather).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.logPath = strings.TrimSpace(cfg.Get("log_path"))
	s.format = strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.Get("format"), formatWinEventJSON)))
	s.follow = cfg.GetBool("follow", true)
	switch s.format {
	case formatWinEventJSON, formatMITKDC:
		return nil
	default:
		return fmt.Errorf("kerberos: unknown format %q (want %q or %q)", s.format, formatWinEventJSON, formatMITKDC)
	}
}

// Gather tails the configured KDC log read-only and emits a FindingReport for each
// Kerberoasting / weak-cipher event it derives. With no log_path it returns nil (a
// no-op the boot already warned about). In follow mode it blocks until ctx is done.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.logPath == "" {
		return nil // nothing configured; wiring emits the visible warning (12 §5)
	}
	parse := s.parserFor(s.format)
	return logtail.Tail(ctx, s.logPath, logtail.Options{Follow: s.follow}, func(line []byte) error {
		findings, err := parse(line)
		if err != nil {
			return nil // a malformed line is skipped, never fatal to the tail
		}
		for _, f := range findings {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
		return nil
	})
}

// Close releases resources; the connector holds none beyond the tailed file.
func (s *Source) Close(context.Context) error { return nil }

// parserFor returns the line parser for a format.
func (s *Source) parserFor(format string) func(line []byte) ([]model.FindingReport, error) {
	if format == formatMITKDC {
		return s.parseMITLine
	}
	return s.parseWinEventLine
}

// ---------------------------------------------------------------------------
// Windows Security event (JSON) parser
// ---------------------------------------------------------------------------

// parseWinEventLine parses one JSON Windows Security event and derives findings.
func (s *Source) parseWinEventLine(line []byte) ([]model.FindingReport, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	flat := flatten(raw)

	eid := atoiLoose(pick(flat, "eventid", "event_id", "id"))
	if eid != eventTGT && eid != eventTGS {
		return nil, nil // not a Kerberos AS/TGS event we model
	}
	ev := kdcEvent{
		eventID:  eid,
		service:  pick(flat, "servicename", "service_name", "service"),
		account:  pick(flat, "targetusername", "target_user_name", "accountname", "account_name", "account", "user"),
		etype:    parseEtype(pick(flat, "ticketencryptiontype", "ticket_encryption_type", "encryptiontype", "etype")),
		status:   pick(flat, "status", "failurecode", "failure_code"),
		client:   pick(flat, "ipaddress", "ip_address", "clientaddress", "ip"),
		occurred: s.parseTime(pick(flat, "timecreated", "timestamp", "@timestamp", "time")),
	}
	return s.derive(ev), nil
}

// ---------------------------------------------------------------------------
// MIT/Heimdal krb5kdc.log parser
// ---------------------------------------------------------------------------

// parseMITLine parses an MIT krb5kdc.log line. The roasting-relevant line shape is:
//
//	<date> krb5kdc[..](info): TGS_REQ (N etypes {23 18 17}) <ip>: ISSUE: authtime <n>, etypes {rep=18 tkt=23 ses=18}, <client> for <service>
//
// The issued ticket cipher (tkt=<n>) is the real signal; when absent (e.g. older
// formats) the weakest requested etype is used. A weak issued cipher for a user
// service principal is the Kerberoasting pattern.
func (s *Source) parseMITLine(line []byte) ([]model.FindingReport, error) {
	text := string(line)
	isTGS := strings.Contains(text, "TGS_REQ")
	isAS := strings.Contains(text, "AS_REQ")
	if !isTGS && !isAS {
		return nil, nil
	}
	// Prefer the issued ticket cipher (tkt=N); fall back to the weakest requested.
	num, name := mitTicketEtype(text)
	if name == "" {
		if etypes := mitEtypes(text); len(etypes) > 0 {
			num, name = weakestEtype(etypes)
		}
	}
	if name == "" {
		return nil, nil // no weak cipher in play; not the roasting pattern
	}
	eid := eventTGS
	if isAS {
		eid = eventTGT
	}
	ev := kdcEvent{
		eventID:  eid,
		service:  mitFieldAfter(text, " for "),
		account:  mitFieldBefore(text, " for "),
		etype:    etypeInfo{num: num, name: name, weak: true, known: true},
		client:   mitClient(text),
		occurred: s.clock(),
	}
	return s.derive(ev), nil
}

// ---------------------------------------------------------------------------
// Signal derivation (shared by both parsers)
// ---------------------------------------------------------------------------

// kdcEvent is the normalized, minimal-data view of one KDC auth event.
type kdcEvent struct {
	eventID  int
	service  string // the service principal / SPN being requested (TGS) or krbtgt (TGS to AS)
	account  string // the requesting account
	etype    etypeInfo
	status   string // failure/status code; "0"/"0x0"/"" => success
	client   string // source network address
	occurred time.Time
}

// derive turns a normalized event into zero or more findings.
func (s *Source) derive(ev kdcEvent) []model.FindingReport {
	if !succeeded(ev.status) {
		return nil // a denied/failed request is not a roasting issuance
	}
	if !ev.etype.weak {
		return nil
	}
	// A TGS-REQ for a USER service principal with a weak cipher is Kerberoasting. A
	// machine account (SPN ending in '$') and krbtgt (a TGS for a TGT renewal, not a
	// service) are excluded — neither is the offline-crackable roasting target.
	if ev.eventID == eventTGS && ev.service != "" && !isMachinePrincipal(ev.service) &&
		!strings.EqualFold(serviceAccount(ev.service), "krbtgt") {
		subject := firstNonEmpty(serviceAccount(ev.service), ev.service)
		title := fmt.Sprintf("Kerberoasting: TGS-REQ for service %q answered with weak cipher %s (etype %d) — offline-crackable; rotate the service account to AES (MITRE ATT&CK T1558.003)",
			ev.service, ev.etype.label(), ev.etype.num)
		return []model.FindingReport{{
			Kind:        "kerberoasting",
			Severity:    model.SeverityHigh,
			SubjectKind: "nhi.service_account",
			SubjectRef:  subject,
			Title:       title,
			DetailHash:  redact.Hash(s.detail(ev)),
			OccurredAt:  ev.occurred,
		}}
	}
	// Any other weak-cipher Kerberos auth is a posture issue: legacy DES anywhere
	// (always broken), or an RC4 TGT request (4768) — the account/domain still
	// issues RC4 TGTs, the AS-side downgrade enabler and AS-REP-roasting precursor.
	if ev.etype.num == etypeDESCRC || ev.etype.num == etypeDESMD5 ||
		(ev.eventID == eventTGT && (ev.etype.num == etypeRC4 || ev.etype.num == etypeRC4Exp)) {
		cipher := "legacy DES"
		if ev.etype.num == etypeRC4 || ev.etype.num == etypeRC4Exp {
			cipher = "RC4"
		}
		title := fmt.Sprintf("Weak Kerberos cipher: %s event used %s (etype %d) for %q — disable weak ciphers (AES only)",
			eventName(ev.eventID), cipher, ev.etype.num, firstNonEmpty(ev.service, ev.account))
		return []model.FindingReport{{
			Kind:        "weak_kerberos_cipher",
			Severity:    model.SeverityMedium,
			SubjectKind: "identity",
			SubjectRef:  firstNonEmpty(serviceAccount(ev.service), ev.account, ev.service),
			Title:       title,
			DetailHash:  redact.Hash(s.detail(ev)),
			OccurredAt:  ev.occurred,
		}}
	}
	return nil
}

// detail builds the non-sensitive de-duplication key for a finding. It contains
// only metadata (no ticket, no hash, no keytab) and is hashed before it travels.
func (s *Source) detail(ev kdcEvent) string {
	return fmt.Sprintf("krb|%d|%s|%s|%d|%s", ev.eventID, ev.service, ev.account, ev.etype.num, redact.Clean(ev.client))
}

// ---------------------------------------------------------------------------
// Kerberos encryption types (IANA Kerberos parameters registry)
// ---------------------------------------------------------------------------

// Kerberos encryption type numbers used for the weak/strong classification.
const (
	etypeDESCRC = 1  // des-cbc-crc (weak)
	etypeDESMD5 = 3  // des-cbc-md5 (weak)
	etypeAES128 = 17 // aes128-cts-hmac-sha1-96 (strong)
	etypeAES256 = 18 // aes256-cts-hmac-sha1-96 (strong)
	etypeRC4    = 23 // rc4-hmac (weak — the Kerberoasting downgrade)
	etypeRC4Exp = 24 // rc4-hmac-exp (weak)
)

// etypeInfo is the parsed ticket encryption type.
type etypeInfo struct {
	num   int
	name  string
	weak  bool
	known bool
}

func (e etypeInfo) label() string {
	if e.name != "" {
		return e.name
	}
	return "etype-" + strconv.Itoa(e.num)
}

// parseEtype parses a Windows ticket-encryption-type value ("0x17", "23") into an
// etypeInfo with its weak/strong classification.
func parseEtype(v string) etypeInfo {
	n, ok := parseHexOrDec(v)
	if !ok {
		return etypeInfo{}
	}
	name, weak := etypeName(n)
	return etypeInfo{num: n, name: name, weak: weak, known: name != ""}
}

// etypeName returns the registry name of an etype and whether it is weak.
func etypeName(n int) (name string, weak bool) {
	switch n {
	case etypeDESCRC:
		return "des-cbc-crc", true
	case etypeDESMD5:
		return "des-cbc-md5", true
	case etypeAES128:
		return "aes128-cts-hmac-sha1-96", false
	case etypeAES256:
		return "aes256-cts-hmac-sha1-96", false
	case etypeRC4:
		return "rc4-hmac", true
	case etypeRC4Exp:
		return "rc4-hmac-exp", true
	default:
		return "", false
	}
}

// weakestEtype returns the weakest (most concerning) etype in a requested set and
// its name; name is "" when none of them is weak.
func weakestEtype(etypes []int) (num int, name string) {
	// Preference order of concern: RC4 > DES > (none).
	for _, want := range []int{etypeRC4, etypeRC4Exp, etypeDESMD5, etypeDESCRC} {
		for _, e := range etypes {
			if e == want {
				n, _ := etypeName(e)
				return e, n
			}
		}
	}
	return 0, ""
}

// ---------------------------------------------------------------------------
// Small parsing helpers
// ---------------------------------------------------------------------------

// isMachinePrincipal reports whether a service principal is a machine/computer
// account (its sAMAccountName ends in '$'), which is excluded from roasting. In a
// Windows 4769 the ServiceName field is the service account's sAMAccountName
// (e.g. "WIN-SQL$" for a computer, "svc_mssql" for a user service account); the
// realm suffix, if present, is trimmed before the '$' test.
func isMachinePrincipal(svc string) bool {
	return strings.HasSuffix(strings.SplitN(svc, "@", 2)[0], "$")
}

// serviceAccount reduces an SPN ("MSSQLSvc/db:1433@CORP") to its account label
// ("MSSQLSvc") for convergence with the roster. A bare account name is returned
// unchanged.
func serviceAccount(svc string) string {
	s := strings.SplitN(svc, "@", 2)[0]
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

// succeeded reports whether a Windows status/failure code denotes success.
func succeeded(status string) bool {
	status = strings.TrimSpace(strings.ToLower(status))
	return status == "" || status == "0" || status == "0x0" || status == "0x00000000"
}

func eventName(eid int) string {
	switch eid {
	case eventTGT:
		return "TGT (AS-REQ)"
	case eventTGS:
		return "TGS-REQ"
	default:
		return "kerberos"
	}
}

// flatten lowercases and flattens a one-level-nested JSON object so EventData
// fields (Windows wraps them under "EventData"/"event_data") are reachable by a
// flat key lookup.
func flatten(raw map[string]json.RawMessage) map[string]string {
	out := map[string]string{}
	for k, v := range raw {
		lk := strings.ToLower(k)
		if lk == "eventdata" || lk == "event_data" || lk == "data" {
			var nested map[string]json.RawMessage
			if json.Unmarshal(v, &nested) == nil {
				for nk, nv := range nested {
					out[strings.ToLower(nk)] = jsonScalar(nv)
				}
				continue
			}
		}
		out[lk] = jsonScalar(v)
	}
	return out
}

// jsonScalar renders a JSON value as a string (string unquoted, number/bool as-is).
func jsonScalar(v json.RawMessage) string {
	s := strings.TrimSpace(string(v))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var str string
		if json.Unmarshal(v, &str) == nil {
			return str
		}
	}
	return s
}

// pick returns the first present, non-empty value among the candidate keys.
func pick(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

// parseHexOrDec parses "0x17" or "23" into an int.
func parseHexOrDec(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if strings.HasPrefix(strings.ToLower(v), "0x") {
		n, err := strconv.ParseInt(v[2:], 16, 64)
		return int(n), err == nil
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// atoiLoose parses an int, tolerating surrounding whitespace and a stray "0x".
func atoiLoose(v string) int {
	n, _ := parseHexOrDec(v)
	return n
}

// mitEtypes extracts the requested etype numbers from "(N etypes {23 18 17})".
func mitEtypes(text string) []int {
	i := strings.Index(text, "etypes {")
	if i < 0 {
		return nil
	}
	rest := text[i+len("etypes {"):]
	j := strings.IndexByte(rest, '}')
	if j < 0 {
		return nil
	}
	var out []int
	for _, tok := range strings.Fields(rest[:j]) {
		if n, err := strconv.Atoi(tok); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// mitFieldAfter returns the whitespace-delimited token following marker (trimming
// a trailing comma the krb5kdc format appends to the service principal).
func mitFieldAfter(text, marker string) string {
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(text[i+len(marker):])
	return strings.TrimRight(strings.Fields(rest + " ")[0], ",")
}

// mitFieldBefore returns the last whitespace-delimited token immediately before
// marker (the requesting client principal precedes " for <service>").
func mitFieldBefore(text, marker string) string {
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	fields := strings.Fields(text[:i])
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimRight(fields[len(fields)-1], ",")
}

// mitTicketEtype extracts the issued ticket cipher from "etypes {... tkt=<n> ...}"
// and reports its name when that cipher is weak ("" otherwise).
func mitTicketEtype(text string) (num int, name string) {
	i := strings.Index(text, "tkt=")
	if i < 0 {
		return 0, ""
	}
	rest := text[i+len("tkt="):]
	digits := ""
	for _, r := range rest {
		if r >= '0' && r <= '9' {
			digits += string(r)
		} else {
			break
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, ""
	}
	nm, weak := etypeName(n)
	if !weak {
		return n, ""
	}
	return n, nm
}

// mitClient extracts the source IP (the "<ip>:" that precedes "ISSUE"/"REQUEST").
func mitClient(text string) string {
	i := strings.Index(text, ") ")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(text[i+2:])
	tok := strings.TrimRight(strings.Fields(rest + " ")[0], ":")
	if strings.Contains(tok, ".") || strings.Contains(tok, ":") {
		return tok
	}
	return ""
}

// parseTime parses a few common timestamp encodings, falling back to the clock.
func (s *Source) parseTime(v string) time.Time {
	v = strings.TrimSpace(v)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return s.clock()
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
