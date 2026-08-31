// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
)

// record is the TOLERANT decoded shape of one egress-proxy verdict line. It is NOT a
// vendor standard (there is none — see doc.go): it is a documented expected shape
// that accepts the field-name ALIASES real proxies emit (Squid, an Envoy RBAC log, a
// bespoke MITM sidecar, a sandbox egress filter). Every field is a pointer-free
// string/json.Number-tolerant union resolved by rawRecord.UnmarshalJSON below, so a
// missing field is simply empty and never fails the decode.
//
// Deliberately ABSENT: any request/response body, header value, URL query or
// credential — the connector records WHO reached WHERE and the allow/deny verdict,
// never the payload (docs/SECURITY-HARDENING.md).
type record struct {
	timestamp string
	identity  string
	host      string
	port      int
	method    string
	decision  string
	reason    string
}

// rawRecord is the on-the-wire JSON object. Each logical field lists its accepted
// aliases; firstNonEmpty picks the first present spelling. Aliases are taken from the
// field names commonly seen across egress proxies — they are a documented ingest, not
// an invented schema. Numbers (port) arrive as json.Number so an int or a quoted
// string both decode.
type rawRecord struct {
	// timestamp aliases.
	TS        string `json:"ts"`
	Time      string `json:"time"`
	Timestamp string `json:"timestamp"`
	AtTime    string `json:"@timestamp"`
	EventTime string `json:"eventTime"`
	// identity aliases (source workload / agent / principal).
	Identity  string `json:"identity"`
	SourceID  string `json:"source"`
	Principal string `json:"principal"`
	Workload  string `json:"workload"`
	Agent     string `json:"agent"`
	Client    string `json:"client"`
	// destination FQDN aliases.
	FQDN        string `json:"fqdn"`
	Host        string `json:"host"`
	Destination string `json:"destination"`
	Dest        string `json:"dest"`
	SNI         string `json:"sni"`
	Authority   string `json:"authority"`
	// port aliases (numeric or string).
	Port     json.Number `json:"port"`
	DestPort json.Number `json:"dest_port"`
	DestPrt2 json.Number `json:"destination_port"`
	// method (optional; drives R/RW when present).
	Method string `json:"method"`
	// decision/verdict aliases.
	Decision string `json:"decision"`
	Verdict  string `json:"verdict"`
	Action   string `json:"action"`
	Result   string `json:"result"`
	// reason aliases.
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Policy  string `json:"policy"`
}

// resolve flattens the alias union into a record, choosing the first present spelling
// per logical field. Field precedence is most-specific-first so a precise alias
// (fqdn) beats a generic one (host) when both appear.
func (r rawRecord) resolve() record {
	return record{
		timestamp: firstNonEmpty(r.TS, r.Time, r.Timestamp, r.AtTime, r.EventTime),
		identity:  firstNonEmpty(r.Identity, r.SourceID, r.Principal, r.Workload, r.Agent, r.Client),
		host:      firstNonEmpty(r.FQDN, r.Host, r.Destination, r.Dest, r.SNI, r.Authority),
		port:      firstPort(r.Port, r.DestPort, r.DestPrt2),
		method:    strings.TrimSpace(r.Method),
		decision:  firstNonEmpty(r.Decision, r.Verdict, r.Action, r.Result),
		reason:    firstNonEmpty(r.Reason, r.Message, r.Detail, r.Policy),
	}
}

// parseLine decodes one JSON-lines verdict into a record. ok=false for a blank line,
// a non-object, malformed JSON, or a record that names no destination — the caller
// tolerates and skips it without failing the run (docs/SECURITY-HARDENING.md: a garbage line must not
// break the data path).
func parseLine(line []byte) (record, bool) {
	line = trimSpaceBytes(line)
	if len(line) == 0 || line[0] != '{' {
		return record{}, false
	}
	var raw rawRecord
	if err := json.Unmarshal(line, &raw); err != nil {
		return record{}, false
	}
	rec := raw.resolve()
	if strings.TrimSpace(rec.host) == "" {
		return record{}, false
	}
	return rec, true
}

// allowDecisions and denyDecisions are the case-folded tokens this connector maps to
// a verdict. A token in neither set yields VerdictUnknown and the record is skipped:
// the mode/verdict is taken VERBATIM from the source, never guessed (ARCHITECTURE.md).
var (
	allowDecisions = map[string]bool{
		"allow": true, "allowed": true, "permit": true, "permitted": true,
		"pass": true, "passed": true, "ok": true, "accept": true, "accepted": true,
	}
	denyDecisions = map[string]bool{
		"deny": true, "denied": true, "block": true, "blocked": true,
		"drop": true, "dropped": true, "reject": true, "rejected": true, "forbidden": true,
	}
)

// classifyVerdict maps a record's decision token to a meshobs verdict. An empty or
// unrecognized decision is VerdictUnknown (ok=false) — never coerced to allow, since
// an unclassifiable verdict must not silently become an observed edge.
func classifyVerdict(decision string) (meshobs.Verdict, bool) {
	d := strings.ToLower(strings.TrimSpace(decision))
	switch {
	case allowDecisions[d]:
		return meshobs.VerdictAllowed, true
	case denyDecisions[d]:
		return meshobs.VerdictDenied, true
	default:
		return meshobs.VerdictUnknown, false
	}
}

// proxyTimeLayouts are the timestamp formats egress logs commonly emit. RFC3339(Nano)
// covers structured JSON loggers; a bare date-time and a Unix-ish space form cover
// older proxies. An unparseable timestamp falls back to the connector clock (handled
// by the caller), never to a fabricated time.
var proxyTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.000",
}

// parseTime parses a record timestamp, returning ok=false when absent/unparseable so
// the caller substitutes its clock.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range proxyTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// firstNonEmpty returns the first non-blank (after trim) of its arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// firstPort returns the first parseable, positive port from its arguments, or 0.
func firstPort(vals ...json.Number) int {
	for _, v := range vals {
		s := strings.TrimSpace(string(v))
		if s == "" {
			continue
		}
		if n, err := v.Int64(); err == nil && n > 0 && n <= 65535 {
			return int(n)
		}
	}
	return 0
}

// trimSpaceBytes trims ASCII whitespace from a byte slice without allocating.
func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && isSpace(b[0]) {
		b = b[1:]
	}
	for len(b) > 0 && isSpace(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return b
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
