// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// every export dialect carries the ledger's canonical occurred_at TEXT — the
// exact bytes canon.EventHash hashes (core/internal/store/canon/canon.go:106) — next
// to its own lossy epoch view of the same instant. These tests prove the property
// that motivated the change: a sub-millisecond difference the hash distinguishes is
// now visible in every projection, where before three of five dialects collapsed it.
package audit_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
)

// nanoEvent returns the fixture event at 123,456,789 ns past the second, so the
// canonical text exercises all nine fractional digits.
func nanoEvent() model.AuditEvent {
	ev := signedEvent()
	ev.OccurredAt = model.NewTimestamp(time.Unix(1700000000, 123456789).UTC())
	return ev
}

// nextUnescapedPipe returns the index of the next '|' that is a real CEF/LEEF
// field delimiter — i.e. not preceded by a backslash escape. Counting raw '|'
// bytes would let an escaped pipe inside a header value (e.g. an action named
// "a\|b") shift the field boundaries and smuggle fake extension text.
func nextUnescapedPipe(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // the escaped character is value content, not a delimiter
		case '|':
			return i
		}
	}
	return -1
}

// cefExtPairs parses the CEF extension — the section after the six unescaped
// header pipes that follow the "CEF:0|" prefix — into exact key=value pairs.
// The audit feed's extension values never contain spaces, so EVERY
// space-separated token must be a well-formed pair; a stray token (e.g. an
// emitter accidentally producing "x olvOccurredAt=…") is a parse error rather
// than something a lax scan would absorb.
func cefExtPairs(line string) (map[string]string, error) {
	const hdr = "CEF:0|"
	if !strings.HasPrefix(line, hdr) {
		return nil, fmt.Errorf("cef: not a CEF:0 line: %q", line)
	}
	rest := line[len(hdr):]
	// vendor | product | version | signature | name | severity |
	for i := 0; i < 6; i++ {
		j := nextUnescapedPipe(rest)
		if j < 0 {
			return nil, fmt.Errorf("cef: truncated header in %q", line)
		}
		rest = rest[j+1:]
	}
	out := map[string]string{}
	for _, tok := range strings.Split(rest, " ") {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("cef: extension token %q is not key=value", tok)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("cef: duplicate extension key %q", k)
		}
		out[k] = v
	}
	return out, nil
}

// leefAttrPairs parses the LEEF 2.0 header positionally — LEEF:2.0 | vendor |
// product | version | eventID | delimiter-spec | attributes — and requires the
// DECLARED delimiter field (the sixth) to be the audit feed's 0x09 before
// splitting the attribute section on tab. Grabbing the first literal "|0x09|"
// anywhere in the line would let a payload embedded in an attribute value pose
// as the attribute section.
func leefAttrPairs(line string) (map[string]string, error) {
	const hdr = "LEEF:2.0|"
	if !strings.HasPrefix(line, hdr) {
		return nil, fmt.Errorf("leef: not a LEEF:2.0 line: %q", line)
	}
	rest := line[len(hdr):]
	for i := 0; i < 4; i++ { // vendor | product | version | eventID
		j := nextUnescapedPipe(rest)
		if j < 0 {
			return nil, fmt.Errorf("leef: truncated header in %q", line)
		}
		rest = rest[j+1:]
	}
	j := nextUnescapedPipe(rest)
	if j < 0 {
		return nil, fmt.Errorf("leef: no delimiter field in %q", line)
	}
	if delim := rest[:j]; delim != "0x09" {
		return nil, fmt.Errorf("leef: the audit feed always declares 0x09, got %q", delim)
	}
	out := map[string]string{}
	for _, pair := range strings.Split(rest[j+1:], "\t") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("leef: attribute %q is not key=value", pair)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("leef: duplicate attribute %q", k)
		}
		out[k] = v
	}
	return out, nil
}

// validSDName reports whether s is a valid RFC 5424 SD-NAME (used for both
// SD-IDs and PARAM-NAMEs): 1..32 PRINTABLE US-ASCII bytes excluding '=', SP,
// ']' and '"' (§6.3.2). Enforcing it matters positionally: a byte sequence
// with an invalid name cannot form an SD element at all, so nothing inside it
// sits at a grammatically correct SD-PARAM position.
func validSDName(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c > '~' || c == '=' || c == ']' || c == '"' {
			return false
		}
	}
	return true
}

// parseSDElement parses ONE complete SD element, starting at the byte after
// its '[', and returns the SD-ID, the params, and the bytes following the
// closing ']'. There is deliberately NO lax "skip" path: a foreign element is
// parsed with exactly the same grammar and then discarded, so the walker can
// never be laxer than the extractor — the asymmetry earlier review rounds
// kept defeating (stray quotes activating escape state, hidden closes) cannot
// exist when skipping IS parsing.
func parseSDElement(s, line string) (string, map[string]string, string, error) {
	idEnd := strings.IndexAny(s, " ]")
	if idEnd < 0 {
		return "", nil, "", fmt.Errorf("syslog: unterminated SD-ID in %q", line)
	}
	id := s[:idEnd]
	if !validSDName(id) {
		return "", nil, "", fmt.Errorf("syslog: invalid SD-ID %q (SD-NAME: 1-32 printable US-ASCII minus '=', SP, ']', '\"') in %q", id, line)
	}
	s = s[idEnd:]
	if s[0] == ']' {
		// A zero-param element is legal: SD-ELEMENT = "[" SD-ID *(SP SD-PARAM) "]".
		return id, map[string]string{}, s[1:], nil
	}
	s = s[1:] // the SP introducing the first param
	if s == "" || s[0] == ']' {
		return "", nil, "", fmt.Errorf("syslog: trailing SP before the SD close in %q", line)
	}
	params, rest, err := sdParamsOf(s, line)
	if err != nil {
		return "", nil, "", err
	}
	return id, params, rest, nil
}

// syslogSDParams parses the audit SD element (name="value" pairs under the
// olivares@ SD-ID) with RFC 5424's own grammar, ANCHORED at the header's
// STRUCTURED-DATA position — <PRI>VERSION SP TIMESTAMP SP HOSTNAME SP APP-NAME
// SP PROCID SP MSGID SP STRUCTURED-DATA — never by substring search, which
// would also match olivares-looking text inside the free-form MSG. SD-PARAMs
// must be separated by SP (§6.3), values honor exactly the three escapes
// (\", \\, \]), and a malformed name fails the whole parse.
func syslogSDParams(line string) (map[string]string, error) {
	gt := strings.IndexByte(line, '>')
	if !strings.HasPrefix(line, "<") || gt < 0 {
		return nil, fmt.Errorf("syslog: no PRI in %q", line)
	}
	s := line[gt+1:]
	if !strings.HasPrefix(s, "1 ") {
		return nil, fmt.Errorf("syslog: not version 1 in %q", line)
	}
	s = s[2:]
	for i := 0; i < 5; i++ { // TIMESTAMP HOSTNAME APP-NAME PROCID MSGID
		j := strings.IndexByte(s, ' ')
		if j < 0 {
			return nil, fmt.Errorf("syslog: truncated header in %q", line)
		}
		s = s[j+1:]
	}
	if strings.HasPrefix(s, "-") {
		return nil, fmt.Errorf("syslog: STRUCTURED-DATA is NILVALUE in %q (bracketed text further right is MSG)", line)
	}
	for strings.HasPrefix(s, "[") {
		id, params, rest, err := parseSDElement(s[1:], line)
		if err != nil {
			// A malformed element ANYWHERE in STRUCTURED-DATA voids the position
			// claim for everything after it, foreign or not.
			return nil, err
		}
		if strings.HasPrefix(id, "olivares@") {
			// After STRUCTURED-DATA the only legal continuations are: end of
			// line, SP then MSG, or another adjacent SD element (RFC 5424 §6:
			// STRUCTURED-DATA = 1*SD-ELEMENT with no separator between
			// elements). An MSG glued onto the closing bracket is malformed.
			if rest != "" && rest[0] != ' ' && rest[0] != '[' {
				return nil, fmt.Errorf("syslog: bytes after STRUCTURED-DATA must be SP+MSG or another SD element in %q", line)
			}
			if len(params) == 0 {
				return nil, fmt.Errorf("syslog: olivares SD element has no params in %q", line)
			}
			return params, nil
		}
		s = rest
	}
	return nil, fmt.Errorf("syslog: no olivares SD element in the STRUCTURED-DATA field of %q", line)
}

// sdParamsOf parses SP-separated name="value" pairs up to the element's closing
// bracket, returning the map and the bytes FOLLOWING the bracket so the caller
// can validate the continuation. Header-field CONTENTS (PRI digits, timestamp
// text, hostname) are deliberately NOT validated by this parser family: its job
// is to prove the extracted value sits at the correct WIRE POSITION under the
// correct grammar, which is what protects the evidence claim — not to be a full
// syslog validator.
func sdParamsOf(s, line string) (map[string]string, string, error) {
	out := map[string]string{}
	for {
		if s == "" {
			return nil, "", fmt.Errorf("syslog: unterminated SD element in %q", line)
		}
		if s[0] == ']' {
			return out, s[1:], nil
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, "", fmt.Errorf("syslog: SD content %q has no '='", s)
		}
		name := s[:eq]
		if !validSDName(name) {
			return nil, "", fmt.Errorf("syslog: malformed SD-PARAM name %q", name)
		}
		s = s[eq+1:]
		if s == "" || s[0] != '"' {
			return nil, "", fmt.Errorf("syslog: SD-PARAM %q value is not quoted", name)
		}
		s = s[1:]
		var val strings.Builder
		for {
			j := strings.IndexAny(s, "\"\\]")
			if j < 0 {
				return nil, "", fmt.Errorf("syslog: unterminated SD-PARAM %q", name)
			}
			// §6.3.3 requires ']' escaped inside PARAM-VALUE. Absorbing a bare
			// one would carry the parse ACROSS the element's real close and let
			// a later key beyond it be returned as if it were in position.
			if s[j] == ']' {
				return nil, "", fmt.Errorf("syslog: unescaped ']' inside SD-PARAM %q value", name)
			}
			val.WriteString(s[:j])
			if s[j] == '\\' {
				if j+1 >= len(s) {
					return nil, "", fmt.Errorf("syslog: dangling escape in SD-PARAM %q", name)
				}
				// RFC 5424 §6.3.3 defines escapes ONLY for '"', '\\' and ']'.
				// Any other backslash pair is two literal value bytes — an
				// unescape-everything rule would silently normalise wrong wire
				// bytes (e.g. "\\2023…") into the expected text.
				switch s[j+1] {
				case '"', '\\', ']':
					val.WriteByte(s[j+1])
				default:
					val.WriteByte('\\')
					val.WriteByte(s[j+1])
				}
				s = s[j+2:]
				continue
			}
			s = s[j+1:]
			break
		}
		if _, dup := out[name]; dup {
			return nil, "", fmt.Errorf("syslog: duplicate SD-PARAM %q", name)
		}
		// §6.3.3: PARAM-VALUE is UTF-8-STRING (RFC 3629). Octets that are not
		// well-formed UTF-8 cannot form an SD-PARAM at all, in a target OR a
		// foreign element — accepting them would return (or walk past) a value
		// that has no grammatically valid position.
		if !utf8.ValidString(val.String()) {
			return nil, "", fmt.Errorf("syslog: SD-PARAM %q value is not valid UTF-8", name)
		}
		out[name] = val.String()
		// §6.3: an SD element is "[" SD-ID *(SP SD-PARAM) "]" — every SP inside
		// the element INTRODUCES a param. So after a value the only legal bytes
		// are ']' or SP-followed-by-a-param; a missing separator AND a trailing
		// SP before the close are both malformed.
		if s == "" {
			return nil, "", fmt.Errorf("syslog: unterminated SD element in %q", line)
		}
		if s[0] != ']' {
			if s[0] != ' ' {
				return nil, "", fmt.Errorf("syslog: SD-PARAM %q not followed by SP or ']' in %q", name, line)
			}
			s = s[1:]
			if s == "" || s[0] == ']' {
				return nil, "", fmt.Errorf("syslog: trailing SP before the SD close in %q", line)
			}
		}
	}
}

// extractOccurredAt pulls the canonical occurred_at text out of one rendered line
// via each dialect's OWN grammar — parsed key=value pairs with exact key lookup,
// never a substring scan. TestDialectParsersRejectMalformedFieldBoundaries keeps
// the parsers honest with crafted-bad-line fixtures.
func extractOccurredAt(t *testing.T, f audit.Format, line string) string {
	t.Helper()
	switch f {
	case audit.FormatCEF:
		pairs, err := cefExtPairs(line)
		if err != nil {
			t.Fatalf("cef: %v", err)
		}
		v, ok := pairs["olvOccurredAt"]
		if !ok {
			t.Fatalf("cef: no olvOccurredAt key in %q", line)
		}
		return v
	case audit.FormatLEEF:
		pairs, err := leefAttrPairs(line)
		if err != nil {
			t.Fatalf("leef: %v", err)
		}
		v, ok := pairs["olvOccurredAt"]
		if !ok {
			t.Fatalf("leef: no olvOccurredAt key in %q", line)
		}
		return v
	case audit.FormatSyslog:
		params, err := syslogSDParams(line)
		if err != nil {
			t.Fatalf("syslog: %v", err)
		}
		v, ok := params["occurred_at"]
		if !ok {
			t.Fatalf("syslog: no occurred_at param in %q", line)
		}
		return v
	case audit.FormatOTLP, audit.FormatOTLPEnvelope, audit.FormatOTLPLogRecord:
		// Walk the decoded JSON to the attribute list rather than substring-matching.
		// Since the catalog remap, otlp and its alias are the request envelope and
		// only otlp_log_record is the bare single-record shape.
		var attrs []struct {
			Key   string `json:"key"`
			Value struct {
				StringValue string `json:"stringValue"`
			} `json:"value"`
		}
		if f == audit.FormatOTLPLogRecord {
			var rec struct {
				Attributes json.RawMessage `json:"attributes"`
			}
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("otlp: %v", err)
			}
			if err := json.Unmarshal(rec.Attributes, &attrs); err != nil {
				t.Fatalf("otlp attributes: %v", err)
			}
		} else {
			var req struct {
				ResourceLogs []struct {
					ScopeLogs []struct {
						LogRecords []struct {
							Attributes json.RawMessage `json:"attributes"`
						} `json:"logRecords"`
					} `json:"scopeLogs"`
				} `json:"resourceLogs"`
			}
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				t.Fatalf("otlp_envelope: %v", err)
			}
			raw := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes
			if err := json.Unmarshal(raw, &attrs); err != nil {
				t.Fatalf("otlp_envelope attributes: %v", err)
			}
		}
		for _, kv := range attrs {
			if kv.Key == "ai.olivares.audit.occurred_at" {
				return kv.Value.StringValue
			}
		}
		t.Fatalf("%s: no ai.olivares.audit.occurred_at attribute in %q", f, line)
	case audit.FormatOCSF:
		var doc struct {
			Unmapped map[string]any `json:"unmapped"`
		}
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("ocsf: %v", err)
		}
		v, ok := doc.Unmapped["ai.olivares.audit.occurred_at"].(string)
		if !ok {
			t.Fatalf("ocsf: no ai.olivares.audit.occurred_at in unmapped: %q", line)
		}
		return v
	}
	t.Fatalf("unhandled format %q", f)
	return ""
}

// TestEveryDialectCarriesTheCanonicalHashedText: for each registered format, the
// extracted occurred_at is byte-identical to model.Timestamp.String() — the exact
// hash input — and parses back to the same instant via the canonical parser.
func TestEveryDialectCarriesTheCanonicalHashedText(t *testing.T) {
	ev := nanoEvent()
	want := ev.OccurredAt.String()
	if want != "2023-11-14T22:13:20.123456789Z" {
		t.Fatalf("fixture text = %q; the fixture must exercise all nine fractional digits", want)
	}
	for _, f := range audit.Formats() {
		line, err := audit.FormatEvent(ev, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		got := extractOccurredAt(t, f, line)
		if got != want {
			t.Errorf("%s: occurred_at = %q, want the canonical text %q", f, got, want)
			continue
		}
		parsed, err := model.ParseTimestamp(got)
		if err != nil {
			t.Errorf("%s: the carried text does not parse canonically: %v", f, err)
			continue
		}
		if !parsed.Time().Equal(ev.OccurredAt.Time()) {
			t.Errorf("%s: round-trip drifted: %v != %v", f, parsed.Time(), ev.OccurredAt.Time())
		}
	}
}

// TestSubMillisecondTamperIsVisibleInEveryDialect is the property the unit exists
// for. Two instants one nanosecond apart within the SAME millisecond produce
// different chain hashes — canon.EventHash covers the nine-digit text — yet before
// The CEF/LEEF/OCSF projections rendered them identically (millisecond epoch)
// and syslog's header truncates to microseconds. Now every dialect distinguishes
// what the hash distinguishes, via the verbatim canonical text.
func TestSubMillisecondTamperIsVisibleInEveryDialect(t *testing.T) {
	evA := signedEvent()
	evB := signedEvent()
	evA.OccurredAt = model.NewTimestamp(time.Unix(1700000000, 123000000).UTC())
	evB.OccurredAt = model.NewTimestamp(time.Unix(1700000000, 123000001).UTC())

	toCanon := func(ev model.AuditEvent) canon.Event {
		return canon.Event{
			TenantID:       ev.TenantID.String(),
			Seq:            ev.Seq,
			OccurredAt:     ev.OccurredAt.String(),
			Actor:          ev.Actor,
			ActorKind:      ev.ActorKind,
			Action:         ev.Action,
			TargetKind:     string(ev.TargetKind),
			TargetID:       ev.TargetID.String(),
			MetaCommitment: canon.MetaDigest("{}"),
			PrevHash:       ev.PrevHash,
		}
	}
	hashA := mustLineHash(t, toCanon(evA))
	hashB := mustLineHash(t, toCanon(evB))
	if string(hashA) == string(hashB) {
		t.Fatal("canon.EventHash does not cover the nanosecond — the premise of this test is wrong")
	}

	// The two instants are indistinguishable at millisecond resolution, which is
	// what CEF rt / LEEF devTime / OCSF time carry.
	if evA.OccurredAt.Time().UnixMilli() != evB.OccurredAt.Time().UnixMilli() {
		t.Fatal("fixture instants must share a millisecond")
	}

	for _, f := range audit.Formats() {
		lineA, err := audit.FormatEvent(evA, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		lineB, err := audit.FormatEvent(evB, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		a, b := extractOccurredAt(t, f, lineA), extractOccurredAt(t, f, lineB)
		if a == b {
			t.Errorf("%s: a 1ns difference the hash distinguishes is invisible: both render %q", f, a)
		}
		if a != evA.OccurredAt.String() || b != evB.OccurredAt.String() {
			t.Errorf("%s: carried text drifted from the hash input: %q / %q", f, a, b)
		}
	}
}

// TestFrozenNamespaceHasNoBareOlivaresKeys is the regression guard: every
// product attribute key in the OTLP record set and the OCSF unmapped container
// lives under the reserved reverse-DNS namespace ai.olivares.* — never under the
// pre-freeze bare olivares.* spelling, which read as reverse DNS claims the TLD
// "audit". (The hash domain separators like olivares.audit.v1 are NOT attribute
// keys and are deliberately out of scope; see otlpEventAttributes.)
func TestFrozenNamespaceHasNoBareOlivaresKeys(t *testing.T) {
	ev := signedEvent()

	assertFrozen := func(f audit.Format, key string) {
		t.Helper()
		if strings.HasPrefix(key, "olivares.") {
			t.Errorf("%s: bare pre-freeze key %q survived the namespace freeze", f, key)
		}
	}

	// OTLP: both projections share otlpEventAttributes; walk the envelope's
	// resource attributes too.
	line, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var req struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []struct {
					Key string `json:"key"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeLogs []struct {
				LogRecords []struct {
					Attributes []struct {
						Key string `json:"key"`
					} `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	for _, kv := range req.ResourceLogs[0].Resource.Attributes {
		assertFrozen(audit.FormatOTLPEnvelope, kv.Key)
	}
	for _, kv := range req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes {
		assertFrozen(audit.FormatOTLPEnvelope, kv.Key)
	}

	// OCSF: every unmapped key.
	line, err = audit.FormatEvent(ev, audit.FormatOCSF)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var doc struct {
		Unmapped map[string]any `json:"unmapped"`
	}
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatalf("decode ocsf: %v", err)
	}
	if len(doc.Unmapped) == 0 {
		t.Fatal("ocsf unmapped is empty; the guard is not exercising anything")
	}
	for key := range doc.Unmapped {
		assertFrozen(audit.FormatOCSF, key)
	}
}

// TestExtremeYearsStayFramingSafeInEveryDialect: model.NewTimestamp range-checks
// nothing, and Go's formatter treats the layout width as a MINIMUM — a negative
// year renders with a leading '-' and year 10000+ renders five digits. Neither is
// the fixed-width four-digit form, so the carried text is documented as "the
// canonical layout text" rather than as RFC 3339; what this test proves is that
// every reachable rendering stays FRAMING-safe (its alphabet is digits plus
// '-', 'T', ':', '.', 'Z' — nothing any dialect escapes or delimits on) and is
// still carried verbatim. Parse-back is NOT asserted here: the canonical parser
// itself rejects wide years, a pre-existing ledger property documented in.
func TestExtremeYearsStayFramingSafeInEveryDialect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		when     time.Time
		wantText string
	}{
		{"year -1", time.Date(-1, 7, 20, 1, 2, 3, 4, time.UTC), "-0001-07-20T01:02:03.000000004Z"},
		{"year 0", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), "0000-01-01T00:00:00.000000000Z"},
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "10000-01-01T00:00:00.000000000Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := signedEvent()
			ev.OccurredAt = model.NewTimestamp(tc.when)
			// The expected text is an independent LITERAL: deriving it from
			// Timestamp.String() would bless whatever the formatter does — a
			// clamp of extreme years to some four-digit form would pass unseen.
			want := tc.wantText
			if got := ev.OccurredAt.String(); got != want {
				t.Fatalf("Timestamp.String() = %q, want the literal %q", got, want)
			}
			for _, r := range want {
				if !((r >= '0' && r <= '9') || r == '-' || r == ':' || r == '.' || r == 'T' || r == 'Z') {
					t.Fatalf("canonical text %q contains %q — outside the framing-safe alphabet", want, r)
				}
			}
			for _, f := range audit.Formats() {
				line, err := audit.FormatEvent(ev, f)
				if err != nil {
					t.Fatalf("format %s: %v", f, err)
				}
				if strings.ContainsAny(line, "\n\r") {
					t.Errorf("%s: multi-line record for %s", f, tc.name)
				}
				if got := extractOccurredAt(t, f, line); got != want {
					t.Errorf("%s: occurred_at = %q, want %q carried verbatim", f, got, want)
				}
			}
		})
	}
}

// TestDialectParsersRejectMalformedFieldBoundaries keeps the extraction helpers
// honest with the exact crafted lines a round-2 review used to defeat their
// predecessors: an emitter accidentally producing a key with an embedded space
// ("x olvOccurredAt=…") must be a PARSE FAILURE or an absent key — never
// silently re-split into the contracted key.
func TestDialectParsersRejectMalformedFieldBoundaries(t *testing.T) {
	if _, err := cefExtPairs("CEF:0|V|P|1|sig|name|3|rt=1 x olvOccurredAt=v"); err == nil {
		t.Error("cef: a stray non-pair token must fail the extension parse")
	}
	pairs, err := leefAttrPairs("LEEF:2.0|V|P|1|evt|0x09|sev=3\tx olvOccurredAt=v")
	if err != nil {
		t.Fatalf("leef: %v", err)
	}
	if _, ok := pairs["olvOccurredAt"]; ok {
		t.Error("leef: a key with an embedded space must not resolve to the contracted key")
	}
	if _, ok := pairs["x olvOccurredAt"]; !ok {
		t.Error("leef: the malformed key must surface AS malformed")
	}
	if _, err := syslogSDParams(`<134>1 - - - - - [olivares@32473 x occurred_at="v"] msg`); err == nil {
		t.Error("syslog: an SD-PARAM name containing a space must fail the parse")
	}
	// Round-3 probes: wrong wire bytes that lax parsers previously accepted.
	// CEF header injection — escaped pipes inside the name field must not shift
	// the extension boundary, so the smuggled key stays header content.
	pairs2, err := cefExtPairs(`CEF:0|V|P|1|sig|na\|\|fake=1 olvOccurredAt=w|3|x=1`)
	if err != nil {
		t.Fatalf("cef: header-injection line must still parse (the ext is x=1): %v", err)
	}
	if _, ok := pairs2["olvOccurredAt"]; ok {
		t.Error("cef: a key smuggled inside an escaped header value leaked into the extension")
	}
	// LEEF with a different DECLARED delimiter: a literal |0x09| later in the
	// payload must not be mistaken for the attribute section.
	if _, err := leefAttrPairs("LEEF:2.0|V|P|1|evt|^|sev=3^payload=|0x09|olvOccurredAt=w"); err == nil {
		t.Error("leef: a non-0x09 declared delimiter must be rejected, not bypassed")
	}
	// Syslog: a backslash pair outside RFC 5424's three escapes is literal value
	// content — it must NOT be normalised into the expected text.
	params2, err := syslogSDParams(`<134>1 - - - - - [olivares@32473 occurred_at="\2023-x"] msg`)
	if err != nil {
		t.Fatalf("syslog: non-escape backslash line must parse: %v", err)
	}
	if got := params2["occurred_at"]; got != `\2023-x` {
		t.Errorf(`syslog: value = %q, want the literal \2023-x (backslash preserved)`, got)
	}

	// Round-4 probes: valid syslog whose STRUCTURED-DATA is NILVALUE and whose
	// olivares-looking text lives only in the free-form MSG must NOT yield a
	// value — extraction is anchored at the header's STRUCTURED-DATA position.
	if _, err := syslogSDParams(`<134>1 - - - - - - [olivares@32473 occurred_at="w"]`); err == nil {
		t.Error("syslog: extracted occurred_at from MSG instead of STRUCTURED-DATA")
	}
	// And adjacent SD-PARAMs without the required SP separator are malformed.
	if _, err := syslogSDParams(`<134>1 - - - - - [olivares@32473 occurred_at="w"x="1"] msg`); err == nil {
		t.Error("syslog: accepted adjacent SD-PARAMs with no required separator")
	}

	// Round-5 probes: a trailing SP before the SD close, and an MSG glued onto
	// the closing bracket without its required SP — both malformed per §6.
	if _, err := syslogSDParams(`<134>1 - - - - - [olivares@32473 occurred_at="w" ] msg`); err == nil {
		t.Error("syslog: accepted trailing SP with no following SD-PARAM")
	}
	if _, err := syslogSDParams(`<134>1 - - - - - [olivares@32473 occurred_at="w"]msg`); err == nil {
		t.Error("syslog: accepted MSG without the required separator")
	}
	// Adjacent SD elements carry NO separator (STRUCTURED-DATA = 1*SD-ELEMENT):
	// a foreign element before the olivares one must still resolve.
	params3, err := syslogSDParams(`<134>1 - - - - - [other@1 a="b"][olivares@32473 occurred_at="v"] msg`)
	if err != nil || params3["occurred_at"] != "v" {
		t.Errorf("syslog: adjacent-element form failed: %v %v", params3, err)
	}
	// Round-6 probes. A bare ']' inside a PARAM-VALUE is malformed (§6.3.3
	// requires it escaped); absorbing it would carry the parse across the SD
	// close and surface a key that sits BEYOND the element.
	if _, err := syslogSDParams(`<134>1 - - - - - [olivares@32473 x="bad] ignored" occurred_at="w"]`); err == nil {
		t.Error("syslog: accepted occurred_at beyond a bare SD close inside a value")
	}
	// Escapes exist only inside PARAM-VALUEs: a backslash before a foreign
	// element's real close must not hide that close and let MSG text be read
	// as an adjacent SD element.
	if _, err := syslogSDParams(`<134>1 - - - - - [other@1 a="b"\] msg marker][olivares@32473 occurred_at="w"]`); err == nil {
		t.Error("syslog: accepted an MSG-resident key after a backslash-hidden foreign SD close")
	}
	// And the legal form of the same shape: ']' escaped INSIDE the foreign
	// value is content, the element closes where the RFC says, and the real
	// adjacent olivares element resolves.
	params4, err := syslogSDParams(`<134>1 - - - - - [other@1 a="x\]y"][olivares@32473 occurred_at="v"] msg`)
	if err != nil || params4["occurred_at"] != "v" {
		t.Errorf("syslog: escaped-']'-in-value adjacent form failed: %v %v", params4, err)
	}
	// Round-7 probes. A quote can only follow PARAM-NAME= — a stray one after
	// the SD-ID must not activate escape state and let a backslash hide the
	// element's real close (the text after it is MSG, not an adjacent element).
	if _, err := syslogSDParams(`<134>1 - - - - - [other@1 "\] msg"][olivares@32473 occurred_at="w"]`); err == nil {
		t.Error("syslog: a stray quote outside PARAM-VALUE carried parsing into MSG")
	}
	// SD-ID is SD-NAME: 1-32 printable US-ASCII. A non-ASCII byte means the
	// sequence cannot form an SD element, so nothing in it is in position.
	if _, err := syslogSDParams(`<134>1 - - - - - [olivares@32473é occurred_at="w"]`); err == nil {
		t.Error("syslog: accepted a non-ASCII SD-ID")
	}
	// Round-8 probes: PARAM-VALUE is UTF-8-STRING (§6.3.3, RFC 3629) — a raw
	// 0xff octet cannot form an SD-PARAM, directly in the target element…
	if _, err := syslogSDParams("<134>1 - - - - - [olivares@32473 occurred_at=\"\xff\"]"); err == nil {
		t.Error("syslog: returned a value from an invalid-UTF-8 PARAM-VALUE")
	}
	// …or in a FOREIGN element, whose malformedness voids the position claim
	// for everything after it.
	if _, err := syslogSDParams("<134>1 - - - - - [other@1 a=\"\xff\"][olivares@32473 occurred_at=\"w\"]"); err == nil {
		t.Error("syslog: walked past a malformed foreign element to a later value")
	}

	// And the well-formed baseline still parses, so the rejections above are
	// discriminating rather than a parser that fails everything.
	params, err := syslogSDParams(`<134>1 - - - - - [olivares@32473 occurred_at="v"] msg`)
	if err != nil || params["occurred_at"] != "v" {
		t.Errorf("syslog: well-formed baseline failed: %v %v", params, err)
	}
}

// TestBothOTLPTimeFieldsAcrossTheWholeDomain pins timeUnixNano AND
// observedTimeUnixNano with INDEPENDENT literals per input class — neither field
// is the oracle for the other, so a future special-case on just one of them
// (e.g. "negative years report observed=1") cannot pass unseen. The equality of
// the two fields is the contract (store-stamped occurrence IS the first-party
// observation); the literals are what make the assertion non-tautological.
func TestBothOTLPTimeFieldsAcrossTheWholeDomain(t *testing.T) {
	cases := []struct {
		name         string
		when         time.Time
		wantTime     string
		wantObserved string
	}{
		{"zero timestamp", time.Time{}, "0", "0"},
		{"unix epoch", time.Unix(0, 0).UTC(), "0", "0"},
		{"year -1", time.Date(-1, 7, 20, 1, 2, 3, 4, time.UTC), "0", "0"},
		{"year 0", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), "0", "0"},
		{"a normal instant", time.Unix(1700000000, 0).UTC(), "1700000000000000000", "1700000000000000000"},
		{"year 2263", time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC), "9246182400000000000", "9246182400000000000"},
		{"the uint64 ceiling", time.Unix(18446744073, 709551615).UTC(), "18446744073709551615", "18446744073709551615"},
		{"past the ceiling", time.Unix(18446744074, 0).UTC(), "0", "0"},
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "0", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := signedEvent()
			ev.OccurredAt = model.NewTimestamp(tc.when)
			line, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
			if err != nil {
				t.Fatalf("format: %v", err)
			}
			var req struct {
				ResourceLogs []struct {
					ScopeLogs []struct {
						LogRecords []struct {
							TimeUnixNano         string `json:"timeUnixNano"`
							ObservedTimeUnixNano string `json:"observedTimeUnixNano"`
						} `json:"logRecords"`
					} `json:"scopeLogs"`
				} `json:"resourceLogs"`
			}
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			rec := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
			if rec.TimeUnixNano != tc.wantTime {
				t.Errorf("timeUnixNano = %q, want %q", rec.TimeUnixNano, tc.wantTime)
			}
			if rec.ObservedTimeUnixNano != tc.wantObserved {
				t.Errorf("observedTimeUnixNano = %q, want %q", rec.ObservedTimeUnixNano, tc.wantObserved)
			}
		})
	}
}
