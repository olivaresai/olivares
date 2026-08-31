// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The ledger's OTLP surface carries two wire shapes under three tokens: `otlp` and
// its exact byte-for-byte alias `otlp_envelope` are the complete, postable
// ExportLogsServiceRequest a real collector takes, while `otlp_log_record` is the bare
// LogRecord projection — pull export only, because a bare record is not a postable
// body. These tests pin the envelope, and pin the bare projection's bytes exactly
// under its own token, so the alias and the projection can never drift. Those bytes have
// moved twice, deliberately, while nothing is published: the shared timestamp
// guard corrected the encoding outside the normal timestamp domain (see
// TestOTLPEnvelopeCoversTheWholeUnsignedTimestampDomain), and froze the
// record-attribute namespace on ai.olivares.audit.* and added the canonical
// occurred_at text. From here on, byte drift is a defect again: a SIEM
// de-duplicates on these bytes.
//
// The official decoder is used AND an exact golden is pinned, because neither is
// sufficient alone: ProtoJSON accepts proto snake_case member names, accepts a
// 64-bit field as a number or a string, accepts an enum by name, and accepts any
// known member the design deliberately omitted. The golden is what actually pins
// canonical spellings, attribute order and the minimal member set.
package audit_test

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
)

// envelopeGolden is the complete expected body for signedEvent(). Everything the
// design fixed is visible here: lowerCamel member names, quoted 64-bit times, a
// numeric severity enum, an observedTimeUnixNano equal to timeUnixNano (the
// ledger's OccurredAt is store-stamped at append, so occurrence and first-party
// observation are the SAME instant — see formatOTLPEnvelope), the three ordered
// resource attributes, the named+versioned scope, the twelve ordered record
// attributes in the frozen ai.olivares.audit.* namespace — occurred_at carrying
// the exact canonical text canon.EventHash hashes, and meta_commitment carrying
// the blinded commitment that completes the preimage — and NO member the design
// excluded (no schemaUrl, droppedAttributesCount, flags, traceId, spanId,
// eventName).
const envelopeGolden = `{"resourceLogs":[{"resource":{"attributes":[` +
	`{"key":"service.name","value":{"stringValue":"ControlPlane"}},` +
	`{"key":"service.version","value":{"stringValue":"1.0"}},` +
	`{"key":"ai.olivares.tenant.id","value":{"stringValue":"22222222-2222-7222-8222-222222222222"}}]},` +
	`"scopeLogs":[{"scope":{"name":"github.com/olivaresai/olivares/core/audit","version":"1.0"},` +
	`"logRecords":[{"timeUnixNano":"1700000000000000000","observedTimeUnixNano":"1700000000000000000",` +
	`"severityNumber":9,"severityText":"INFO","body":{"stringValue":"access_edge.upsert"},"attributes":[` +
	`{"key":"ai.olivares.audit.seq","value":{"stringValue":"42"}},` +
	`{"key":"ai.olivares.audit.tenant","value":{"stringValue":"22222222-2222-7222-8222-222222222222"}},` +
	`{"key":"ai.olivares.audit.occurred_at","value":{"stringValue":"2023-11-14T22:13:20.000000000Z"}},` +
	`{"key":"ai.olivares.audit.actor","value":{"stringValue":"user:abc"}},` +
	`{"key":"ai.olivares.audit.actor_kind","value":{"stringValue":"user"}},` +
	`{"key":"ai.olivares.audit.target_kind","value":{"stringValue":"core.access_edge"}},` +
	`{"key":"ai.olivares.audit.target_id","value":{"stringValue":"33333333-3333-7333-8333-333333333333"}},` +
	`{"key":"ai.olivares.audit.meta_commitment","value":{"stringValue":"10e17bce3d607fb46863fc0ed4336d5d6e854c743a5d9c6cc3016e3cd98bfecb"}},` +
	`{"key":"ai.olivares.audit.payload_hash","value":{"stringValue":""}},` +
	`{"key":"ai.olivares.audit.prev_hash","value":{"stringValue":"0102030000000000000000000000000000000000000000000000000000000000"}},` +
	`{"key":"ai.olivares.audit.hash","value":{"stringValue":"0a0b0c0d00000000000000000000000000000000000000000000000000000000"}},` +
	`{"key":"ai.olivares.audit.sig","value":{"stringValue":"/+7dAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="}}]}]}]}]}`

// TestOTLPEnvelopeExactBody pins every byte. This is the assertion that catches
// what the official decoder tolerates: a snake_case member name, a timestamp
// emitted as a number, an enum emitted by name, a reordered or duplicated
// attribute, and any extra known field.
func TestOTLPEnvelopeExactBody(t *testing.T) {
	got, err := audit.FormatEvent(signedEvent(), audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if got != envelopeGolden {
		t.Fatalf("envelope body changed:\n got: %s\nwant: %s", got, envelopeGolden)
	}
}

// TestOTLPEnvelopeIsAcceptedByTheOfficialDecoder complements the golden: the golden
// says "these exact bytes", this says "and the spec's own decoder agrees they are a
// valid request", with unknown members REJECTED.
func TestOTLPEnvelopeIsAcceptedByTheOfficialDecoder(t *testing.T) {
	line, err := audit.FormatEvent(signedEvent(), audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}

	var req collogspb.ExportLogsServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("the official OTLP decoder rejected the body: %v\n%s", err, line)
	}
	if n := len(req.GetResourceLogs()); n != 1 {
		t.Fatalf("resourceLogs = %d, want exactly 1", n)
	}
	rl := req.GetResourceLogs()[0]
	if n := len(rl.GetScopeLogs()); n != 1 {
		t.Fatalf("scopeLogs = %d, want exactly 1", n)
	}
	sl := rl.GetScopeLogs()[0]
	if n := len(sl.GetLogRecords()); n != 1 {
		t.Fatalf("logRecords = %d, want exactly 1", n)
	}

	// Resource identity, in order and without duplicates. Semconv's `tenant.id`
	// means an Active Directory tenant, so using it here would be a semantic lie.
	var keys []string
	for _, kv := range rl.GetResource().GetAttributes() {
		keys = append(keys, kv.GetKey())
	}
	if want := []string{"service.name", "service.version", "ai.olivares.tenant.id"}; !equalStrings(keys, want) {
		t.Errorf("resource attributes = %v, want exactly %v in order", keys, want)
	}

	rec := sl.GetLogRecords()[0]
	if rec.GetSeverityNumber() != 9 || rec.GetSeverityText() != "INFO" {
		t.Errorf("severity = %v/%q, want 9/INFO", rec.GetSeverityNumber(), rec.GetSeverityText())
	}
	// observed_time is what the COLLECTING system stamps — and for the ledger
	// that is the SAME instant as the occurrence, verified: OccurredAt is
	// server-assigned at append (core/model/audit.go:25-27; AuditDraft has no
	// caller time field), so the store both created and observed the event then.
	// An draft asserted 0 ("unknown") here instead; the adversarial review
	// established that the stock Collector receiver does NOT backfill a zero
	// observed time, so emitting 0 would DELETE a known true value. Both fields
	// are still asserted (equality alone would pass on 0 == 0).
	if rec.GetTimeUnixNano() == 0 {
		t.Error("timeUnixNano = 0 for an event with a real occurrence instant")
	}
	if got := rec.GetObservedTimeUnixNano(); got != rec.GetTimeUnixNano() {
		t.Errorf("observedTimeUnixNano = %d, want the store-stamped occurrence instant %d",
			got, rec.GetTimeUnixNano())
	}
	// All eleven ledger attributes survive, in order. Note what that does and does
	// not buy a consumer: the integrity fields ride verbatim, so the CHAIN LINKAGE
	// (prev_hash[n+1] == hash[n]) and a checkpoint signature over hash can both be
	// checked offline, and since occurred_at carries the exact canonical TEXT
	// the hash covers. Re-deriving hash from the content still cannot be done:
	// canon.EventHash takes twelve inputs (core/internal/store/canon/canon.go:99-117)
	// and this projection carries eleven — the stored MetaDigest is absent. The
	// archive path carries all of them (core/audit/archive.go:46-60); the SIEM
	// projection does not yet, and no comment here should imply otherwise.
	var recKeys []string
	for _, kv := range rec.GetAttributes() {
		recKeys = append(recKeys, kv.GetKey())
	}
	want := []string{
		"ai.olivares.audit.seq", "ai.olivares.audit.tenant", "ai.olivares.audit.occurred_at",
		"ai.olivares.audit.actor", "ai.olivares.audit.actor_kind", "ai.olivares.audit.target_kind",
		"ai.olivares.audit.target_id", "ai.olivares.audit.meta_commitment",
		"ai.olivares.audit.payload_hash",
		"ai.olivares.audit.prev_hash", "ai.olivares.audit.hash", "ai.olivares.audit.sig",
	}
	if !equalStrings(recKeys, want) {
		t.Errorf("record attributes = %v, want exactly %v in order", recKeys, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestOTLPEnvelopeUsesCanonicalMemberNames walks the RAW JSON: ProtoJSON would also
// accept `scope_logs`, `log_records` and `string_value`, so the decoder cannot pin
// the canonical spelling — only reading the bytes can.
func TestOTLPEnvelopeUsesCanonicalMemberNames(t *testing.T) {
	line, err := audit.FormatEvent(signedEvent(), audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	for _, snake := range []string{
		"resource_logs", "scope_logs", "log_records", "string_value",
		"time_unix_nano", "observed_time_unix_nano", "severity_number", "severity_text",
	} {
		if strings.Contains(line, `"`+snake+`"`) {
			t.Errorf("non-canonical member %q on the wire: %s", snake, line)
		}
	}
	for _, canonical := range []string{
		"resourceLogs", "scopeLogs", "logRecords", "stringValue",
		"timeUnixNano", "observedTimeUnixNano", "severityNumber", "severityText",
	} {
		if !strings.Contains(line, `"`+canonical+`"`) {
			t.Errorf("canonical member %q missing: %s", canonical, line)
		}
	}
	// Members the design deliberately excluded until the ledger has real values.
	for _, undesigned := range []string{
		"schemaUrl", "droppedAttributesCount", "flags", "traceId", "spanId", "eventName",
	} {
		if strings.Contains(line, `"`+undesigned+`"`) {
			t.Errorf("undesigned member %q appeared: %s", undesigned, line)
		}
	}
}

// TestOTLPEnvelopeEncodes64BitFieldsAsStrings pins the ProtoJSON rule the decoder
// tolerates either way: 64-bit fields are quoted, enums are numeric.
func TestOTLPEnvelopeEncodes64BitFieldsAsStrings(t *testing.T) {
	line, err := audit.FormatEvent(signedEvent(), audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if !strings.Contains(line, `"timeUnixNano":"1700000000000000000"`) ||
		!strings.Contains(line, `"observedTimeUnixNano":"1700000000000000000"`) {
		t.Errorf("64-bit times must be QUOTED strings: %s", line)
	}
	if !strings.Contains(line, `"severityNumber":9`) || strings.Contains(line, `"severityNumber":"`) {
		t.Errorf("severityNumber must be the numeric enum value, not a name: %s", line)
	}
}

// TestOTLPFormatsNeverFabricateATimeForAZeroTimestamp covers BOTH formats: Go's
// zero time has no representable UnixNano, and OTLP spells "no timestamp" as 0.
func TestOTLPFormatsNeverFabricateATimeForAZeroTimestamp(t *testing.T) {
	ev := signedEvent()
	ev.OccurredAt = model.Timestamp{}

	envelope, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if !strings.Contains(envelope, `"timeUnixNano":"0"`) ||
		!strings.Contains(envelope, `"observedTimeUnixNano":"0"`) {
		t.Errorf("an unset time must encode as OTLP's 0: %s", envelope)
	}
	// The member is lowerCamel on the wire, so this is the spelling a negative
	// epoch would actually appear under.
	if strings.Contains(envelope, `"timeUnixNano":"-`) {
		t.Errorf("negative epoch reached the wire: %s", envelope)
	}
	// The envelope is still a valid request with an unset time.
	var req collogspb.ExportLogsServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(envelope), &req); err != nil {
		t.Fatalf("zero-time envelope is not decodable: %v", err)
	}

	// And the bare projection's zero-time bytes are pinned too (under its
	// post-remap token, otlp_log_record — same bytes), so the shared helper
	// cannot change one format's behavior without the other's noticing.
	bare, err := audit.FormatEvent(ev, audit.FormatOTLPLogRecord)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if !strings.HasPrefix(bare, `{"timeUnixNano":"0","severityText":"INFO"`) {
		t.Errorf("bare-projection zero-time bytes changed: %s", bare)
	}
}

// TestOTLPEnvelopeIsByteDeterministicUnderHostileInput: the push path delivers
// these bytes and a SIEM de-duplicates on them. The input carries the characters
// encoding/json escapes, a newline (which must not survive into an NDJSON line) and
// invalid UTF-8 (which encoding/json replaces deterministically).
func TestOTLPEnvelopeIsByteDeterministicUnderHostileInput(t *testing.T) {
	ev := signedEvent()
	ev.Actor = "user:<a>&\"b\"\nsecond\xff"
	first, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	for i := 0; i < 50; i++ {
		again, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
		if err != nil {
			t.Fatalf("format: %v", err)
		}
		if again != first {
			t.Fatalf("render %d differs:\n got: %s\nwant: %s", i, again, first)
		}
	}
	// encoding/json HTML-escapes these; an Encoder with SetEscapeHTML(false) would
	// silently change every delivered byte, so the exact escaping is pinned.
	// The exact WIRE form: HTML escapes as \u00XX, the literal newline as \n (two
	// characters, never a raw break), and the invalid byte as U+FFFD.
	if !strings.Contains(first, `user:\u003ca\u003e\u0026\"b\"\nsecond\ufffd`) {
		t.Errorf("hostile-string encoding drifted from encoding/json's: %s", first)
	}
	// A literal newline would split one NDJSON record into two.
	if strings.ContainsAny(first, "\n\r") {
		t.Errorf("an NDJSON line must contain no raw CR/LF: %q", first)
	}
	var req collogspb.ExportLogsServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(first), &req); err != nil {
		t.Fatalf("hostile-input envelope is not decodable: %v", err)
	}
}

// TestBareOTLPProjectionExactBytes pins the bare projection to the byte. Until
// this test guarded byte identity with the PRE-refactor output deliberately
// retired that guarantee in the one direction a pre-publication product can — the
// attribute namespace froze on ai.olivares.audit.* and the canonical occurred_at
// text joined the attributes, because renaming a published vocabulary is a breaking
// change and nothing is published yet. This golden is written from the DESIGN (the
// frozen key set, in order, with the canonical timestamp text), not blessed from
// the code's output; from here on, byte drift is a defect again. The catalog
// remap then moved the shape's TOKEN — otlp came to mean the request envelope
// everywhere and these bytes moved to otlp_log_record — and this golden came
// along UNCHANGED: the token moved, the bytes did not.
func TestBareOTLPProjectionExactBytes(t *testing.T) {
	const want = `{"timeUnixNano":"1700000000000000000","severityText":"INFO",` +
		`"body":{"stringValue":"access_edge.upsert"},"attributes":[` +
		`{"key":"ai.olivares.audit.seq","value":{"stringValue":"42"}},` +
		`{"key":"ai.olivares.audit.tenant","value":{"stringValue":"22222222-2222-7222-8222-222222222222"}},` +
		`{"key":"ai.olivares.audit.occurred_at","value":{"stringValue":"2023-11-14T22:13:20.000000000Z"}},` +
		`{"key":"ai.olivares.audit.actor","value":{"stringValue":"user:abc"}},` +
		`{"key":"ai.olivares.audit.actor_kind","value":{"stringValue":"user"}},` +
		`{"key":"ai.olivares.audit.target_kind","value":{"stringValue":"core.access_edge"}},` +
		`{"key":"ai.olivares.audit.target_id","value":{"stringValue":"33333333-3333-7333-8333-333333333333"}},` +
		`{"key":"ai.olivares.audit.meta_commitment","value":{"stringValue":"10e17bce3d607fb46863fc0ed4336d5d6e854c743a5d9c6cc3016e3cd98bfecb"}},` +
		`{"key":"ai.olivares.audit.payload_hash","value":{"stringValue":""}},` +
		`{"key":"ai.olivares.audit.prev_hash","value":{"stringValue":"0102030000000000000000000000000000000000000000000000000000000000"}},` +
		`{"key":"ai.olivares.audit.hash","value":{"stringValue":"0a0b0c0d00000000000000000000000000000000000000000000000000000000"}},` +
		`{"key":"ai.olivares.audit.sig","value":{"stringValue":"/+7dAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="}}]}`

	got, err := audit.FormatEvent(signedEvent(), audit.FormatOTLPLogRecord)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if got != want {
		t.Fatalf("the bare OTLP projection changed:\n got: %s\nwant: %s", got, want)
	}
	// And it is still NOT an OTLP request — that is why it is its own token.
	var req collogspb.ExportLogsServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(got), &req); err == nil {
		t.Error("otlp_log_record decoded as an ExportLogsServiceRequest; the shapes have converged")
	}
}

// TestOTLPEnvelopeAliasIsExactBytes holds the catalog's alias contract at this
// surface: otlp and otlp_envelope are ONE format with two accepted spellings —
// byte equality, not just same-shape — and the bare projection is a genuinely
// different wire form under its own token. Hostile input rides along so escaping
// cannot diverge between the spellings either.
func TestOTLPEnvelopeAliasIsExactBytes(t *testing.T) {
	ev := signedEvent()
	ev.Actor = "user:<a>&\"b\"\nsecond\xff"
	canonical, err := audit.FormatEvent(ev, audit.FormatOTLP)
	if err != nil {
		t.Fatalf("format otlp: %v", err)
	}
	alias, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format otlp_envelope: %v", err)
	}
	if canonical != alias {
		t.Fatalf("otlp and otlp_envelope diverged:\n otlp: %s\nalias: %s", canonical, alias)
	}
	bare, err := audit.FormatEvent(ev, audit.FormatOTLPLogRecord)
	if err != nil {
		t.Fatalf("format otlp_log_record: %v", err)
	}
	if bare == canonical {
		t.Fatal("otlp_log_record rendered the envelope; the bare projection must stay its own wire form")
	}
}

// TestEveryFormatInTheRegistryRenders is the cross-surface guard the previous
// "…Everywhere" test falsely claimed: a format in the registry that FormatEvent
// cannot render, or a JSON format that does not parse as one object per line.
func TestEveryFormatInTheRegistryRenders(t *testing.T) {
	formats := audit.Formats()
	if len(formats) == 0 {
		t.Fatal("the format registry is empty")
	}
	seen := map[audit.Format]bool{}
	for _, f := range formats {
		if seen[f] {
			t.Errorf("format %q appears twice in the registry", f)
		}
		seen[f] = true
		line, err := audit.FormatEvent(signedEvent(), f)
		if err != nil {
			t.Errorf("format %q is in the registry but does not render: %v", f, err)
			continue
		}
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("format %q emitted a multi-line record", f)
		}
		if f == audit.FormatOTLP || f == audit.FormatOTLPEnvelope ||
			f == audit.FormatOTLPLogRecord || f == audit.FormatOCSF {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				t.Errorf("format %q must be one JSON object per line: %v", f, err)
			}
		}
	}
	// ValidFormat reads the same slice, so asserting it accepts the registry would be
	// tautological. What is worth pinning is that it stays CLOSED.
	if audit.ValidFormat("bogus") || audit.ValidFormat("") {
		t.Error("ValidFormat must reject anything not in the registry")
	}
	if got, want := audit.FormatList(), "cef|leef|syslog|otlp|otlp_envelope|otlp_log_record|ocsf"; got != want {
		t.Errorf("FormatList() = %q, want %q — operator-facing lists derive from this", got, want)
	}
}

// TestNeitherOTLPFormatEmitsANegativeTimestamp: OTLP's timestamp fields are UNSIGNED
// nanoseconds and the spec defines 0 as "unknown", so a signed negative value is not
// a valid request at all — the official decoder rejects it. The ledger does not
// range-check what a caller records (model.NewTimestamp has no epoch floor), so a
// pre-1970 date or a badly set clock could otherwise emit a body a collector refuses,
// which reads as a transport fault.
//
// BOTH formats are asserted. An earlier revision of this test pinned the legacy
// projection's negative output as intended behavior, on the reasoning that its bytes
// are already shipped. That was pinning a defect: the official decoder rejects
// "-14182980000000000" in a fixed64 field, and no first-party parser of those bytes exists
// in this repository — first-party code creates and transports them but never reads them
// back, and whether any third party parses them is unverified — while the SAME
// wrap, one domain further out, silently
// produces a plausible WRONG date (see
// TestOTLPEnvelopeCoversTheWholeUnsignedTimestampDomain). The projection's bytes
// are pinned for a normal in-domain instant by TestBareOTLPProjectionExactBytes.
func TestNeitherOTLPFormatEmitsANegativeTimestamp(t *testing.T) {
	ev := signedEvent()
	ev.OccurredAt = model.NewTimestamp(time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC))

	// All three OTLP spellings — the canonical envelope, its alias, and the bare
	// projection — cover both wire shapes.
	for _, f := range []audit.Format{audit.FormatOTLPEnvelope, audit.FormatOTLP, audit.FormatOTLPLogRecord} {
		line, err := audit.FormatEvent(ev, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		if strings.Contains(line, `":"-`) {
			t.Errorf("%s: a negative epoch cannot be an unsigned OTLP timestamp: %s", f, line)
		}
		if !strings.Contains(line, `"timeUnixNano":"0"`) {
			t.Errorf("%s: want the pre-epoch time reported as OTLP's unknown (0): %s", f, line)
		}
	}

	// The envelope must additionally remain a decodable request; "unknown" is the
	// honest encoding, not a reason to emit something unparseable.
	line, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var req collogspb.ExportLogsServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("pre-epoch envelope is not a valid OTLP request: %v\n%s", err, line)
	}
	if got := req.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0].GetTimeUnixNano(); got != 0 {
		t.Errorf("timeUnixNano = %d, want 0 (OTLP's unknown) for an out-of-domain time", got)
	}
}

// TestOTLPEnvelopeCoversTheWholeUnsignedTimestampDomain: guarding the floor is only
// half the domain. OTLP's timestamp is uint64 nanoseconds, which runs to
// 2554-07-21T23:34:33.709551615Z, but Go's UnixNano() is int64 and wraps silently
// past 2262-04-11 — 2263-01-01 comes back as -9200561673709551616. The ledger's
// canonical text layout parses any 4-digit year and range-checks nothing
// (core/model/time.go:16,27), so such an instant is reachable through
// model.NewTimestamp and through ParseTimestamp on a stored row. Between 2262 and
// 2554 the value IS representable, so the honest encoding is the true unsigned
// number — neither a wrapped negative (which the decoder rejects outright) nor
// "unknown" (which would throw away a time the wire format can carry). Past the
// ceiling nothing is representable and 0 is all that is left.
//
// Every case runs through BOTH OTLP formats. Asserting only the envelope would let a
// regression through that kept the pre-epoch floor but restored UnixNano() for
// positive legacy timestamps — the floor test alone cannot see that.
//
// The last two cases look redundant and are not. A true +1 ns past the ceiling is the
// honest boundary, but UNGUARDED uint64 addition at exactly MaxUint64+1 wraps to zero,
// which coincidentally equals the "unknown" sentinel — so that case would pass against
// a broken encoder. The second-boundary witness wraps to a NON-zero value (290448384,
// i.e. 1970-01-01T00:00:00.290448384Z) and is therefore the case that actually bites. Both
// stay: one names the boundary, the other detects the defect.
func TestOTLPEnvelopeCoversTheWholeUnsignedTimestampDomain(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want uint64
	}{
		// Past the int64 nanosecond wrap, still far inside uint64.
		{"a year-2263 instant", time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC), 9246182400000000000},
		// The very last instant uint64 nanoseconds can express.
		{"the ceiling itself", time.Unix(18446744073, 709551615).UTC(), math.MaxUint64},
		// Exactly one nanosecond further: no representation exists, so "unknown".
		{"one nanosecond past the ceiling", time.Unix(18446744073, 709551616).UTC(), 0},
		// One SECOND past the ceiling's second — 290,448,385 ns beyond it. Unguarded,
		// this is the input that produces a plausible false 1970 date.
		{"the positive-wrap witness", time.Unix(18446744074, 0).UTC(), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := signedEvent()
			ev.OccurredAt = model.NewTimestamp(tc.when)

			// --- the request envelope ---
			line, err := audit.FormatEvent(ev, audit.FormatOTLPEnvelope)
			if err != nil {
				t.Fatalf("format envelope: %v", err)
			}
			// The official decoder is the arbiter: a wrapped negative fails here with
			// "invalid value for uint64 field", so this catches the defect even if the
			// numeric assertion below were ever loosened.
			var req collogspb.ExportLogsServiceRequest
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(line), &req); err != nil {
				t.Fatalf("not a valid OTLP request: %v\n%s", err, line)
			}
			rec := req.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
			if got := rec.GetTimeUnixNano(); got != tc.want {
				t.Errorf("envelope timeUnixNano = %d, want %d", got, tc.want)
			}
			if got := rec.GetObservedTimeUnixNano(); got != tc.want {
				t.Errorf("envelope observedTimeUnixNano = %d, want %d (store-stamped occurrence = observation)", got, tc.want)
			}
			// And pin the bytes, not just the decoded number: ProtoJSON would accept a
			// bare number too, and the design requires the quoted string form.
			wireForm := `"timeUnixNano":"` + strconv.FormatUint(tc.want, 10) + `"`
			if !strings.Contains(line, wireForm) {
				t.Errorf("envelope wire form missing %s in:\n%s", wireForm, line)
			}

			// --- the bare LogRecord projection, which shares the encoder ---
			// It is not an ExportLogsServiceRequest, but it IS a LogRecord, so the
			// official generated type decodes it — a stronger check than reading the
			// member as a substring, because the decoder also rejects a value that does
			// not fit the unsigned field at all.
			bare, err := audit.FormatEvent(ev, audit.FormatOTLPLogRecord)
			if err != nil {
				t.Fatalf("format bare projection: %v", err)
			}
			var rawRec logspb.LogRecord
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(bare), &rawRec); err != nil {
				t.Fatalf("bare projection is not a valid OTLP LogRecord: %v\n%s", err, bare)
			}
			if got := rawRec.GetTimeUnixNano(); got != tc.want {
				t.Errorf("bare projection timeUnixNano = %d, want %d", got, tc.want)
			}
			// And the wire form, since ProtoJSON would also accept a bare number.
			if !strings.Contains(bare, wireForm) {
				t.Errorf("bare projection wire form missing %s in:\n%s", wireForm, bare)
			}
		})
	}
}
