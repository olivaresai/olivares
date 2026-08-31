// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
//
// OTLP/JSON conformance and policy for the notification feed. These tests work on the RAW
// bytes, because the properties they cover cannot be seen from a decoded message:
//
//  1. severityNumber must be an UNQUOTED integer TOKEN. ProtoJSON accepts an enum as
//     either a number or its name, and OTLP/JSON requires the integer and forbids the
//     name — but both spellings decode to the same enum value, so only the raw token
//     distinguishes them. (A wrapped TIMESTAMP, by contrast, IS visible after decoding:
//     the decoded uint64 can be compared with the independently expected value. Raw bytes
//     are needed here for token TYPE and member PRESENCE, not for timestamp correctness.)
//  2. Member presence. OTLP treats an absent proto3 scalar and an explicit zero as the
//     same value, so a decoder cannot tell whether a member was emitted at all.
//
// The whole-message JSON-versus-protobuf parity check lives in connectors/otlplog, where
// the encoding is actually chosen and both real bodies exist to be compared.
package siemfmt

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// otlpRecordOf decodes the single record as a member map plus its decoded attributes, and
// asserts the envelope cardinality first so a structural change fails diagnostically
// rather than panicking on an index. It also fails on any duplicate attribute key, which
// OTLP forbids, so every test that goes through it inherits that check.
func otlpRecordOf(t *testing.T, body []byte) (rec map[string]json.RawMessage, attrs map[string]string, order []string) {
	rec, attrs, _, order = otlpRecordWithRaw(t, body)
	return rec, attrs, order
}

// otlpRecordWithRaw is otlpRecordOf plus the RAW AnyValue of each attribute, for the tests
// that assert structure (an adjustment record is an arrayValue of kvlistValues, which a
// map[string]string cannot hold).
func otlpRecordWithRaw(t *testing.T, body []byte) (rec map[string]json.RawMessage, attrs map[string]string, raw map[string]json.RawMessage, order []string) {
	t.Helper()
	var doc struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []map[string]json.RawMessage `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not valid JSON: %v\n%s", err, body)
	}
	if len(doc.ResourceLogs) != 1 || len(doc.ResourceLogs[0].ScopeLogs) != 1 ||
		len(doc.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("want exactly one resource/scope/record, got: %s", body)
	}
	rec = doc.ResourceLogs[0].ScopeLogs[0].LogRecords[0]

	var decoded []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue *string `json:"stringValue"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rec["attributes"], &decoded); err != nil {
		t.Fatalf("attributes are not decodable: %v\n%s", err, rec["attributes"])
	}
	var rawPairs []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(rec["attributes"], &rawPairs); err != nil {
		t.Fatalf("attributes are not decodable: %v", err)
	}
	attrs = make(map[string]string, len(decoded))
	raw = make(map[string]json.RawMessage, len(decoded))
	order = make([]string, 0, len(decoded))
	for i, a := range decoded {
		if _, dup := raw[a.Key]; dup {
			t.Errorf("attribute %q appears more than once; OTLP requires unique keys: %s", a.Key, body)
		}
		if a.Value.StringValue != nil {
			attrs[a.Key] = *a.Value.StringValue
		}
		raw[a.Key] = rawPairs[i].Value
		order = append(order, a.Key)
	}
	return rec, attrs, raw, order
}

// TestNotificationSeverityNumberIsAnUnquotedIntegerToken covers every severity the mapper
// can produce, INCLUDING info (which an earlier revision of this test omitted while its
// comment claimed completeness) and an unrecognized non-empty value.
//
// It requires an unquoted token explicitly. Decoding into json.Number is NOT enough:
// encoding/json accepts both `17` and `"17"` into a json.Number and Int64() succeeds for
// both, so a regression to a quoted number would have passed. The enum NAME is rejected by
// either check; the quoted number is only rejected by this one.
//
// The expected values are written out rather than read back from the mapper, so a change
// to the mapping is a deliberate edit here instead of something the test absorbs.
func TestNotificationSeverityNumberIsAnUnquotedIntegerToken(t *testing.T) {
	// Pin the enum numbers the OTLP specification fixes, so a dependency bump that
	// renumbered them fails here rather than silently changing every record's severity.
	for name, want := range map[logspb.SeverityNumber]int32{
		logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED: 0,
		logspb.SeverityNumber_SEVERITY_NUMBER_INFO:        9,
		logspb.SeverityNumber_SEVERITY_NUMBER_INFO2:       10,
		logspb.SeverityNumber_SEVERITY_NUMBER_WARN:        13,
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR:       17,
		logspb.SeverityNumber_SEVERITY_NUMBER_FATAL:       21,
	} {
		if int32(name) != want {
			t.Fatalf("OTLP %v is %d, want %d — the pinned enum numbering moved", name, int32(name), want)
		}
	}

	cases := []struct {
		severity sdkmodel.Severity
		want     int64
		wantText string
	}{
		{"", 0, "UNKNOWN"}, // the value default protojson omitted entirely
		{"not-a-severity", 0, "UNKNOWN"},
		{sdkmodel.SeverityInfo, 9, "INFO"},
		{sdkmodel.SeverityLow, 10, "LOW"},
		{sdkmodel.SeverityMedium, 13, "MEDIUM"},
		{sdkmodel.SeverityHigh, 17, "HIGH"},
		{sdkmodel.SeverityCritical, 21, "CRITICAL"},
	}
	for _, tc := range cases {
		label := string(tc.severity)
		if label == "" {
			label = "unset"
		}
		t.Run(label, func(t *testing.T) {
			n := sampleNotification()
			n.Severity = tc.severity
			body, err := OTLPLogJSON(DefaultDevice(), n)
			if err != nil {
				t.Fatalf("OTLPLogJSON: %v", err)
			}
			rec, _, _ := otlpRecordOf(t, body)

			raw, ok := rec["severityNumber"]
			if !ok {
				t.Fatalf("severityNumber is absent; a SIEM rule needs one stable numeric column: %s", body)
			}
			token := string(raw)
			if strings.HasPrefix(token, `"`) {
				t.Fatalf("severityNumber is the QUOTED token %s; OTLP/JSON requires an integer, and a quoted number is as nonconformant as the enum name", token)
			}
			got, err := strconv.ParseInt(token, 10, 32)
			if err != nil {
				t.Fatalf("severityNumber token %s is not a base-10 integer: %v", token, err)
			}
			if got != tc.want {
				t.Errorf("severityNumber = %d, want %d", got, tc.want)
			}
			var text string
			if err := json.Unmarshal(rec["severityText"], &text); err != nil {
				t.Fatalf("severityText is not decodable: %v", err)
			}
			if text != tc.wantText {
				t.Errorf("severityText = %q, want %q", text, tc.wantText)
			}
		})
	}
}

// TestNotificationTimestampNeverWrapsIntoAFalseInstant pins the exact raw token, because
// the defect it covers produced a perfectly valid unsigned number for the wrong instant.
func TestNotificationTimestampNeverWrapsIntoAFalseInstant(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want string // the exact JSON token expected for timeUnixNano
	}{
		{"the zero time is unknown", time.Time{}, `"0"`},
		{"a normal instant", time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), `"1781258400000000000"`},
		// Past int64 nanoseconds but well inside uint64: must be the true value.
		{"a year-2263 instant", time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC), `"9246182400000000000"`},
		// The last instant uint64 nanoseconds can express.
		{"the ceiling itself", time.Unix(18446744073, 709551615).UTC(), `"18446744073709551615"`},
		// One second further: no representation exists, so unknown — never a wrap.
		{"one second past the ceiling", time.Unix(18446744074, 0).UTC(), `"0"`},
		// Before the epoch there is no unsigned representation either.
		{"a pre-epoch instant", time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC), `"0"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := sampleNotification()
			n.Time = tc.when
			body, err := OTLPLogJSON(DefaultDevice(), n)
			if err != nil {
				t.Fatalf("OTLPLogJSON: %v", err)
			}
			rec, _, _ := otlpRecordOf(t, body)
			got, ok := rec["timeUnixNano"]
			if !ok {
				t.Fatalf("timeUnixNano is absent; this product always emits the member, with 0 for unknown: %s", body)
			}
			if string(got) != tc.want {
				t.Errorf("timeUnixNano = %s, want %s\nbody: %s", got, tc.want, body)
			}
		})
	}
}

// adjustment is one decoded entry of the structured ai.olivares.wire.adjustments value.
type adjustment struct {
	operation string
	location  string
	emitted   string
	original  []byte
}

// decodeAdjustments decodes the structured adjustment record: an arrayValue of
// kvlistValues, each with operation/location/emitted (strings) and original (bytesValue,
// base64 in ProtoJSON). Decoding it — rather than substring-matching a comma-joined
// string — is the point: a caller's own key may contain any delimiter, and only bytesValue
// can carry the original of a value that was not valid UTF-8 to begin with.
func decodeAdjustments(t *testing.T, raw json.RawMessage) []adjustment {
	t.Helper()
	if raw == nil {
		return nil
	}
	var outer struct {
		ArrayValue struct {
			Values []struct {
				KVListValue struct {
					Values []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue *string `json:"stringValue"`
							BytesValue  *[]byte `json:"bytesValue"`
						} `json:"value"`
					} `json:"values"`
				} `json:"kvlistValue"`
			} `json:"values"`
		} `json:"arrayValue"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("the adjustment record is not a decodable arrayValue: %v\n%s", err, raw)
	}
	out := make([]adjustment, 0, len(outer.ArrayValue.Values))
	for _, entry := range outer.ArrayValue.Values {
		var a adjustment
		seen := map[string]bool{}
		for _, kv := range entry.KVListValue.Values {
			if seen[kv.Key] {
				t.Errorf("adjustment entry has a duplicate key %q", kv.Key)
			}
			seen[kv.Key] = true
			switch kv.Key {
			case "operation":
				a.operation = derefString(t, kv.Key, kv.Value.StringValue)
			case "location":
				a.location = derefString(t, kv.Key, kv.Value.StringValue)
			case "emitted":
				a.emitted = derefString(t, kv.Key, kv.Value.StringValue)
			case "original":
				if kv.Value.BytesValue == nil {
					t.Errorf("adjustment original is not a bytesValue: %s", raw)
					continue
				}
				a.original = *kv.Value.BytesValue
			default:
				t.Errorf("unexpected member %q in an adjustment entry", kv.Key)
			}
		}
		for name, present := range map[string]bool{
			"operation": seen["operation"], "location": seen["location"],
			"emitted": seen["emitted"], "original": seen["original"],
		} {
			if !present {
				t.Errorf("adjustment entry is missing %q: %s", name, raw)
			}
		}
		out = append(out, a)
	}
	return out
}

func derefString(t *testing.T, key string, s *string) string {
	t.Helper()
	if s == nil {
		t.Errorf("adjustment member %q is not a stringValue", key)
		return ""
	}
	return *s
}

// TestAnInstantOTLPCannotExpressIsCarriedAnyway is the policy test. Emitting only OTLP's 0
// would trade a wrong date for a MISSING one, and a backend that substitutes ingestion time
// for a missing timestamp would then index a 1969 event as today's — the authoritative time
// silently lost instead of silently wrong. So the instant travels as attributes with the
// reason, and ONLY in the cases where the timestamp field cannot state it.
//
// The machine-readable form is signed Unix seconds plus a nanosecond remainder, which is
// exact over the whole domain Unix() reports. It is deliberately NOT "the uint64 the instant
// would have had": outside the range no such value exists, and a modulo one would recreate
// the wrapped date this work removes.
func TestAnInstantOTLPCannotExpressIsCarriedAnyway(t *testing.T) {
	cases := []struct {
		name        string
		when        time.Time
		wantFall    bool
		wantSeconds int64
		wantNanos   int64
		wantStatus  string
		wantRFC3339 string // "" means the human attribute must be ABSENT
	}{
		{name: "a representable instant needs no fallback", when: time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)},
		{name: "the ceiling needs no fallback", when: time.Unix(18446744073, 709551615).UTC()},
		{name: "no time at all needs no fallback", when: time.Time{}},
		{
			name: "a pre-epoch instant", when: time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC),
			wantFall: true, wantSeconds: -14182980, wantNanos: 0,
			wantStatus: "before_epoch", wantRFC3339: "1969-07-20T20:17:00Z",
		},
		{
			name: "one nanosecond past the ceiling", when: time.Unix(18446744073, 709551616).UTC(),
			wantFall: true, wantSeconds: 18446744073, wantNanos: 709551616,
			wantStatus: "after_uint64_ceiling", wantRFC3339: "2554-07-21T23:34:33.709551616Z",
		},
		{
			name: "the epoch, which OTLP cannot distinguish from unknown", when: time.Unix(0, 0).UTC(),
			wantFall: true, wantSeconds: 0, wantNanos: 0,
			wantStatus: "epoch_indistinguishable_from_unknown", wantRFC3339: "1970-01-01T00:00:00Z",
		},
		{
			// Year 10000 formats as "10000-01-02T…", which NO RFC 3339 parser accepts —
			// including Go's own. The human attribute is therefore omitted rather than
			// labeled RFC 3339, while the machine form still carries the instant exactly.
			name: "a year past RFC 3339's four-digit year", when: time.Date(10000, 1, 2, 3, 4, 5, 6, time.UTC),
			// 2932898 days from the epoch to 10000-01-02 (proleptic Gregorian), plus
			// 3h04m05s. Computed independently, not read back from the encoder.
			wantFall: true, wantSeconds: 253402398245, wantNanos: 6,
			wantStatus: "after_uint64_ceiling", wantRFC3339: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := sampleNotification()
			n.Time = tc.when
			body, err := OTLPLogJSON(DefaultDevice(), n)
			if err != nil {
				t.Fatalf("OTLPLogJSON: %v", err)
			}
			_, attrs, raw, _ := otlpRecordWithRaw(t, body)

			_, hasSeconds := raw["ai.olivares.event.time.unix_seconds"]
			if !tc.wantFall {
				for _, key := range []string{
					"ai.olivares.event.time.unix_seconds", "ai.olivares.event.time.nanos",
					"ai.olivares.event.time.status", "ai.olivares.event.time.rfc3339",
				} {
					if _, ok := raw[key]; ok {
						t.Errorf("a representable or absent instant must not add %s", key)
					}
				}
				return
			}
			if !hasSeconds {
				t.Fatalf("the authoritative instant was LOST: no unix_seconds in %s", body)
			}
			// ProtoJSON renders a 64-bit integer as a decimal STRING; a bare number would
			// lose precision in a JavaScript-class consumer.
			if got := intValueOf(t, raw["ai.olivares.event.time.unix_seconds"]); got != tc.wantSeconds {
				t.Errorf("unix_seconds = %d, want %d", got, tc.wantSeconds)
			}
			if got := intValueOf(t, raw["ai.olivares.event.time.nanos"]); got != tc.wantNanos {
				t.Errorf("nanos = %d, want %d", got, tc.wantNanos)
			}
			if got := attrs["ai.olivares.event.time.status"]; got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			// Reconstructing the instant from the machine form must give back exactly it.
			rebuilt := time.Unix(tc.wantSeconds, tc.wantNanos).UTC()
			if !rebuilt.Equal(tc.when) {
				t.Errorf("seconds+nanos rebuild to %s, want %s",
					rebuilt.Format(time.RFC3339Nano), tc.when.Format(time.RFC3339Nano))
			}

			human, hasHuman := attrs["ai.olivares.event.time.rfc3339"]
			if tc.wantRFC3339 == "" {
				if hasHuman {
					t.Errorf("a value RFC 3339 cannot express was emitted as RFC 3339: %q", human)
				}
				return
			}
			if !hasHuman {
				t.Fatalf("the human form is missing for an instant RFC 3339 can express: %s", body)
			}
			if human != tc.wantRFC3339 {
				t.Errorf("rfc3339 = %q, want %q", human, tc.wantRFC3339)
			}
			// And it must actually parse — the property the old key name only asserted.
			back, err := time.Parse(time.RFC3339Nano, human)
			if err != nil {
				t.Fatalf("the emitted %q is not parseable RFC 3339: %v", human, err)
			}
			if !back.Equal(tc.when) {
				t.Errorf("the human form round-trips to %s, want %s",
					back.Format(time.RFC3339Nano), tc.when.Format(time.RFC3339Nano))
			}
		})
	}
}

// intValueOf decodes an OTLP intValue, which ProtoJSON spells as a decimal STRING.
func intValueOf(t *testing.T, raw json.RawMessage) int64 {
	t.Helper()
	var v struct {
		IntValue *string `json:"intValue"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not a decodable AnyValue: %v\n%s", err, raw)
	}
	if v.IntValue == nil {
		t.Fatalf("value is not an intValue: %s", raw)
	}
	n, err := strconv.ParseInt(*v.IntValue, 10, 64)
	if err != nil {
		t.Fatalf("intValue %q is not a decimal integer: %v", *v.IntValue, err)
	}
	return n
}

// TestTheNotificationTypeTravelsInEventName: OTLP has a dedicated member for the event type
// (LogRecord.event_name, field 12 of the pinned proto), whose presence identifies the record
// as an Event. An earlier revision synthesized an "eventType" ATTRIBUTE instead, which both
// ignored the dedicated member and could be shadowed by a caller field of the same name — a
// duplicate key, which OTLP forbids and whose handling a receiver is free to decide.
func TestTheNotificationTypeTravelsInEventName(t *testing.T) {
	n := sampleNotification()
	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	rec, attrs, _ := otlpRecordOf(t, body)

	var eventName string
	if err := json.Unmarshal(rec["eventName"], &eventName); err != nil {
		t.Fatalf("eventName is absent or not decodable: %v\n%s", err, body)
	}
	if eventName != n.Type {
		t.Errorf("eventName = %q, want %q", eventName, n.Type)
	}
	if v, ok := attrs["eventType"]; ok {
		t.Errorf("the synthetic eventType attribute is still emitted (%q); the type belongs in eventName", v)
	}

	// An empty type means "not an event", and the member is still present as "" so the
	// column keeps one shape.
	n.Type = ""
	body, err = OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	rec, _, _ = otlpRecordOf(t, body)
	if got := string(rec["eventName"]); got != `""` {
		t.Errorf("eventName for an untyped notification = %s, want an empty string", got)
	}
}

// TestTheAuthoritativeTenantCannotBeReplacedByACallerField: the text formats let a caller's
// "tenant" field win, because there the field vocabulary is the caller's. OTLP has a
// reserved namespace, so the authoritative Notification.Tenant travels as the product key
// ai.olivares.tenant.id and a caller field named "tenant" stays a separate caller field.
func TestTheAuthoritativeTenantCannotBeReplacedByACallerField(t *testing.T) {
	n := sampleNotification()
	n.Tenant = "authoritative"
	n.Fields = map[string]string{"tenant": "caller-supplied"}
	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	_, attrs, _ := otlpRecordOf(t, body)
	if got := attrs["ai.olivares.tenant.id"]; got != "authoritative" {
		t.Errorf("ai.olivares.tenant.id = %q, want the authoritative tenant", got)
	}
	if got := attrs["tenant"]; got != "caller-supplied" {
		t.Errorf("the caller's own tenant field was lost: %q", got)
	}
}

// TestACallerFieldCannotShadowAProductAttribute: the ai.olivares.* attribute namespace is
// the product's. A caller field there is re-homed rather than allowed to overwrite a product
// key or duplicate it — and the move is recorded structurally, so nothing is silently
// renamed.
func TestACallerFieldCannotShadowAProductAttribute(t *testing.T) {
	n := sampleNotification()
	// A pre-epoch time, so the product attributes exist to be shadowed.
	n.Time = time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC)
	n.Fields = map[string]string{
		"ai.olivares.event.time.status": "exact",
		"ai.olivares.tenant.id":         "attacker supplied",
		"ordinary":                      "kept",
	}
	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	_, attrs, raw, order := otlpRecordWithRaw(t, body)

	if got := attrs["ai.olivares.event.time.status"]; got != "before_epoch" {
		t.Errorf("the product status was shadowed: %q", got)
	}
	if got := attrs["ai.olivares.tenant.id"]; got != n.Tenant {
		t.Errorf("the authoritative tenant was shadowed: %q", got)
	}
	// Nothing is dropped: the caller's values are still there, under caller.*.
	if got := attrs["caller.ai.olivares.event.time.status"]; got != "exact" {
		t.Errorf("the caller's value was lost (have %v): %q", order, got)
	}
	if got := attrs["caller.ai.olivares.tenant.id"]; got != "attacker supplied" {
		t.Errorf("the caller's value was lost: %q", got)
	}
	if got := attrs["ordinary"]; got != "kept" {
		t.Errorf("an ordinary field was disturbed: %q", got)
	}

	// The renames are recorded with the exact original key in bytesValue.
	got := map[string]adjustment{}
	for _, a := range decodeAdjustments(t, raw["ai.olivares.wire.adjustments"]) {
		if a.operation != "rename" {
			t.Errorf("unexpected adjustment %q for valid input", a.operation)
		}
		got[string(a.original)] = a
	}
	for original, wantEmitted := range map[string]string{
		"ai.olivares.event.time.status": "caller.ai.olivares.event.time.status",
		"ai.olivares.tenant.id":         "caller.ai.olivares.tenant.id",
	} {
		a, ok := got[original]
		if !ok {
			t.Errorf("no adjustment records the rename of %q; the move is invisible", original)
			continue
		}
		if a.emitted != wantEmitted {
			t.Errorf("rename of %q emitted %q, want %q", original, a.emitted, wantEmitted)
		}
		if !strings.HasPrefix(a.location, "record.attributes[") || !strings.HasSuffix(a.location, "].key") {
			t.Errorf("rename of %q has location %q, which does not name an attribute key", original, a.location)
		}
	}
	if n := intValueOf(t, raw["ai.olivares.wire.adjustment.count"]); n != int64(len(got)) {
		t.Errorf("adjustment.count = %d, want %d", n, len(got))
	}
}

// TestAttributeKeysStayUniqueThroughAThreeWayCollision is the witness that requires the
// LOOPING suffix. Two distinct map keys can never collide by themselves, so `dup` next to
// `dup#1` proves nothing — a single-shot `#1` disambiguator passes that. The state that
// breaks a single shot is three originals normalising onto ONE base whose `#1` is already
// taken: the third then needs `#2`.
//
// Here `a\xff` and `a\xfe` both sanitize to `a<U+FFFD>`, and the caller also supplies
// `a<U+FFFD>#1` literally. A single-shot implementation emits `a<U+FFFD>#1` twice, the SDK
// encoder rejects the duplicate, and the whole governance record is dropped.
func TestAttributeKeysStayUniqueThroughAThreeWayCollision(t *testing.T) {
	replacement := string(utf8.RuneError)
	n := sampleNotification()
	n.Fields = map[string]string{
		"a\xff":                  "first",
		"a\xfe":                  "second",
		"a" + replacement + "#1": "literal",
	}
	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("a colliding key set must not fail the encoding: %v", err)
	}
	// otlpRecordWithRaw fails on any duplicate key, which is the primary assertion.
	_, attrs, raw, order := otlpRecordWithRaw(t, body)
	if len(raw) != len(order) {
		t.Fatalf("duplicate keys survived: %v", order)
	}

	base := "a" + replacement
	// All three values survive, under three distinct names, and the base plus BOTH ordinals
	// are used — which is only reachable with a loop.
	wantKeys := []string{base, base + "#1", base + "#2"}
	for _, k := range wantKeys {
		if _, ok := attrs[k]; !ok {
			t.Errorf("expected emitted key %q; got %v", k, order)
		}
	}
	values := map[string]bool{}
	for _, k := range wantKeys {
		values[attrs[k]] = true
	}
	for _, want := range []string{"first", "second", "literal"} {
		if !values[want] {
			t.Errorf("value %q was lost; emitted values were %v", want, values)
		}
	}

	// The two sanitized keys are recorded with their exact original bytes, which is the
	// only way a consumer can tell which of them became which emitted key.
	originals := map[string]string{}
	for _, a := range decodeAdjustments(t, raw["ai.olivares.wire.adjustments"]) {
		originals[string(a.original)] = a.emitted
	}
	for _, original := range []string{"a\xff", "a\xfe"} {
		if _, ok := originals[original]; !ok {
			t.Errorf("no adjustment carries the original bytes %q", original)
		}
	}
}

// TestInvalidUTF8IsSubstitutedAndTheOriginalIsPreserved: JSON cannot carry arbitrary bytes,
// so encoding/json would turn every invalid sequence into U+FFFD and two different values
// would serialize identically. The SDK encoder refuses such input outright; this layer
// substitutes, records the exact location, and carries the ORIGINAL bytes — because dropping
// a governance event from the OTLP feed while the CEF and syslog feeds still deliver it is
// the worse outcome, and a marker that cannot reproduce the original is not evidence.
//
// The substitution is strings.ToValidUTF8, which collapses each RUN of invalid bytes to one
// U+FFFD rather than one per byte. That is not reversible on its own, which is exactly why
// the original travels.
func TestInvalidUTF8IsSubstitutedAndTheOriginalIsPreserved(t *testing.T) {
	n := sampleNotification()
	n.Body = "payload \xff\xfe ends"
	n.Fields = map[string]string{"path": "na\xffme", "clean": "fine", "bad\xffkey": "value"}

	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("a record with invalid UTF-8 must still be deliverable, got: %v", err)
	}
	_, attrs, raw, order := otlpRecordWithRaw(t, body)

	entries := decodeAdjustments(t, raw["ai.olivares.wire.adjustments"])
	if len(entries) == 0 {
		t.Fatalf("the substitution was NOT recorded; an altered record must not look intact: %s", body)
	}
	byOriginal := map[string]adjustment{}
	for _, a := range entries {
		byOriginal[string(a.original)] = a
	}
	// Every altered input is recoverable byte for byte from the record itself.
	composedBody := "least-privilege drift — payload \xff\xfe ends"
	for original, wantLocation := range map[string]string{
		// The recorded original is the COMPOSED body ("Title — Body"), which is the value
		// that would have gone on the wire — not the raw Body field.
		composedBody: "record.body",
		"na\xffme":   "record.attributes[",
		"bad\xffkey": "record.attributes[",
	} {
		a, ok := byOriginal[original]
		if !ok {
			t.Errorf("no adjustment carries the original bytes %q; the evidence is gone", original)
			continue
		}
		if !strings.HasPrefix(a.location, wantLocation) {
			t.Errorf("adjustment for %q has location %q, want a location starting %q", original, a.location, wantLocation)
		}
		if a.operation != "utf8_replace" {
			t.Errorf("adjustment for %q has operation %q, want utf8_replace", original, a.operation)
		}
		if utf8.ValidString(string(a.original)) {
			t.Errorf("the original for %q came back as valid UTF-8; base64 round trip failed", original)
		}
	}
	if got := attrs["clean"]; got != "fine" {
		t.Errorf("a valid field was disturbed: %q", got)
	}
	if _, ok := byOriginal["fine"]; ok {
		t.Error("an unaltered value was recorded as adjusted")
	}
	if n := intValueOf(t, raw["ai.olivares.wire.adjustment.count"]); n != int64(len(entries)) {
		t.Errorf("adjustment.count = %d, want %d (have %v)", n, len(entries), order)
	}

	// A wholly valid notification carries no adjustment record at all.
	body, err = OTLPLogJSON(DefaultDevice(), sampleNotification())
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	_, _, raw, _ = otlpRecordWithRaw(t, body)
	for _, key := range []string{"ai.olivares.wire.adjustments", "ai.olivares.wire.adjustment.count"} {
		if _, ok := raw[key]; ok {
			t.Errorf("valid input was marked as adjusted (%s)", key)
		}
	}
}

// TestAnEmptyCallerKeyIsPreservedUnderAGeneratedName: nothing can query an attribute with no
// key, so the SDK encoder rejects one — but the VALUE is still evidence. The text formats
// drop it (they have no way to express it); the OTLP path gives it a generated name and says
// so, which preserves the value instead of discarding it.
func TestAnEmptyCallerKeyIsPreservedUnderAGeneratedName(t *testing.T) {
	n := sampleNotification()
	n.Fields = map[string]string{"": "value with no key", "ok": "1"}
	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	_, attrs, raw, order := otlpRecordWithRaw(t, body)

	var found string
	for k, v := range attrs {
		if v == "value with no key" {
			found = k
		}
	}
	if found == "" {
		t.Fatalf("the value of the empty key was DROPPED: %v", order)
	}
	if !strings.HasPrefix(found, "ai.olivares.caller.field.") {
		t.Errorf("the generated key %q is not in the product namespace", found)
	}
	var recorded bool
	for _, a := range decodeAdjustments(t, raw["ai.olivares.wire.adjustments"]) {
		if a.operation == "generated_key" && a.emitted == found && len(a.original) == 0 {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("the generated key was not recorded as an adjustment: %s", raw["ai.olivares.wire.adjustments"])
	}
	if got := attrs["ok"]; got != "1" {
		t.Errorf("an ordinary field was disturbed: %q", got)
	}
}

// TestProductKeysAreDistinct: every product-owned key is a distinct constant. put() panics on
// a duplicate — a collision there can only be a bug in the encoder, never something an input
// causes — so this test is what makes that panic unreachable rather than merely unlikely.
func TestProductKeysAreDistinct(t *testing.T) {
	keys := map[string]string{
		"otlpAttrTenant":           otlpAttrTenant,
		"otlpAttrEventTimeSeconds": otlpAttrEventTimeSeconds,
		"otlpAttrEventTimeNanos":   otlpAttrEventTimeNanos,
		"otlpAttrEventTimeRFC3339": otlpAttrEventTimeRFC3339,
		"otlpAttrEventTimeStatus":  otlpAttrEventTimeStatus,
		"otlpAttrAdjustments":      otlpAttrAdjustments,
		"otlpAttrAdjustmentCount":  otlpAttrAdjustmentCount,
		"otlpAttrDeviceVendor":     otlpAttrDeviceVendor,
		"otlpAttrDeviceVersion":    otlpAttrDeviceVersion,
	}
	seen := map[string]string{}
	for name, key := range keys {
		if prev, dup := seen[key]; dup {
			t.Errorf("%s and %s are both %q", prev, name, key)
		}
		seen[key] = name
		if !strings.HasPrefix(key, otlpReservedPrefix) {
			t.Errorf("%s = %q is outside the reserved namespace, so a caller field could shadow it", name, key)
		}
	}
	// The generated-key prefix must also be reserved, or an empty caller key could land on
	// a name a later caller field could claim.
	if !strings.HasPrefix(otlpReservedPrefix+"caller.field.", otlpReservedPrefix) {
		t.Error("the generated-key prefix is outside the reserved namespace")
	}
}

// TestResourceIdentityAndScope pins the resource and scope exactly, and is the regression
// test for two pieces of FALSE metadata.
//
// The scope identifies the component that PRODUCES the records — this shared encoder, which
// several connectors emit through — so it must not be named after one transport, and its
// version must be the encoder's own.
//
// service.version is the harder one. The OpenTelemetry semantic conventions define it as the
// service's API or implementation version, while Device.Version is the CEF/LEEF header
// revision an operator may set to a reseller's branding. An earlier revision of this very
// test set Device.Version to a reseller value, correctly refused it as the scope version,
// and then asserted it AS service.version — blessing the exact semantic error the scope fix
// exists to prevent. Now the device header travels under its own product key, and
// service.version is emitted only when the running service's version is actually known.
func TestResourceIdentityAndScope(t *testing.T) {
	resourceOf := func(t *testing.T, d Device) (map[string]string, []string, struct{ Name, Version string }) {
		t.Helper()
		body, err := OTLPLogJSON(d, sampleNotification())
		if err != nil {
			t.Fatalf("OTLPLogJSON: %v", err)
		}
		var doc struct {
			ResourceLogs []struct {
				Resource struct {
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue string `json:"stringValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"resource"`
				ScopeLogs []struct {
					Scope struct {
						Name    string `json:"name"`
						Version string `json:"version"`
					} `json:"scope"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		got := map[string]string{}
		var order []string
		for _, a := range doc.ResourceLogs[0].Resource.Attributes {
			got[a.Key] = a.Value.StringValue
			order = append(order, a.Key)
		}
		s := doc.ResourceLogs[0].ScopeLogs[0].Scope
		return got, order, struct{ Name, Version string }{s.Name, s.Version}
	}

	t.Run("an unknown service version is omitted, not invented", func(t *testing.T) {
		d := DefaultDevice()
		d.Version = "9" // as a reseller might set the CEF header
		got, order, scope := resourceOf(t, d)

		want := map[string]string{
			"service.name":               d.Product,
			"ai.olivares.device.vendor":  d.Vendor,
			"ai.olivares.device.version": "9",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("resource attribute %q = %q, want %q", k, got[k], v)
			}
		}
		if len(got) != len(want) {
			t.Errorf("resource has %d attributes, want exactly %d: %v", len(got), len(want), order)
		}
		if v, ok := got["service.version"]; ok {
			t.Errorf("service.version = %q was asserted from the device header; an unknown service version must be OMITTED", v)
		}
		if _, ok := got["vendor"]; ok {
			t.Error(`the un-namespaced "vendor" resource key is still emitted`)
		}
		if scope.Name != otlpScopeName {
			t.Errorf("scope name = %q, want %q", scope.Name, otlpScopeName)
		}
		if scope.Version != otlpScopeVersion {
			t.Errorf("scope version = %q, want the encoder's own %q", scope.Version, otlpScopeVersion)
		}
		if scope.Version == d.Version {
			t.Error("the scope version is the device header revision; it must identify the emitting code")
		}
	})

	t.Run("a known service version is emitted and is independent of the device header", func(t *testing.T) {
		d := DefaultDevice()
		d.Version = "9"
		d.ServiceVersion = "1.4.2"
		got, _, scope := resourceOf(t, d)

		if got["service.version"] != "1.4.2" {
			t.Errorf("service.version = %q, want the running service version 1.4.2", got["service.version"])
		}
		if got["ai.olivares.device.version"] != "9" {
			t.Errorf("device version = %q, want the header revision 9", got["ai.olivares.device.version"])
		}
		if got["service.version"] == got["ai.olivares.device.version"] {
			t.Error("the service version and the device header revision are the same value; they are different concepts")
		}
		if scope.Version == got["service.version"] {
			t.Error("the scope version is the service version; it must identify the encoder")
		}
	})
}

// TestOTLPJSONIsRepeatableAcrossMapIterationOrders: Notification.Fields is a Go map whose
// iteration order is randomized per run, so the ONLY thing that makes the output stable is
// the explicit sort in orderedFields. Rendering the same content from maps built in
// different insertion orders is what exercises that; repeating one render in a loop mostly
// re-reads the same map layout.
func TestOTLPJSONIsRepeatableAcrossMapIterationOrders(t *testing.T) {
	keys := []string{"z", "a", "m", "k", "b", "y", "c", "x"}
	var want string
	for round := 0; round < 8; round++ {
		n := sampleNotification()
		n.Fields = make(map[string]string, len(keys))
		for i := range keys { // a different insertion order every round
			k := keys[(i+round)%len(keys)]
			n.Fields[k] = "v-" + k
		}
		body, err := OTLPLogJSON(DefaultDevice(), n)
		if err != nil {
			t.Fatalf("OTLPLogJSON: %v", err)
		}
		if round == 0 {
			want = string(body)
			continue
		}
		if string(body) != want {
			t.Fatalf("insertion order %d changed the bytes:\n first: %s\n  this: %s", round, want, body)
		}
	}
	// And the emitted order is the sorted one, not any insertion order.
	n := sampleNotification()
	n.Fields = map[string]string{"z": "1", "a": "2", "m": "3"}
	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	_, _, order := otlpRecordOf(t, body)
	if len(order) < 3 || order[0] != "a" || order[1] != "m" || order[2] != "z" {
		t.Errorf("attribute order = %v, want the sorted a,m,z first", order)
	}
}

// TestOTLPRequestForIsTheOneResolution asserts the seam: the typed projection and the JSON
// body must both come from one resolved request, so a caller cannot accidentally resolve
// twice and get two answers. The whole-message comparison of the two real encodings lives
// in connectors/otlplog, where the encoding is chosen.
func TestOTLPRequestForIsTheOneResolution(t *testing.T) {
	n := sampleNotification()
	resolved := OTLPRequestFor(DefaultDevice(), n)

	fromResolved, err := OTLPLogsDataFrom(resolved)
	if err != nil {
		t.Fatalf("OTLPLogsDataFrom: %v", err)
	}
	fromNotification, err := OTLPLogsData(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogsData: %v", err)
	}
	a := fromResolved.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
	b := fromNotification.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
	if a.GetEventName() != b.GetEventName() || a.GetTimeUnixNano() != b.GetTimeUnixNano() ||
		a.GetSeverityNumber() != b.GetSeverityNumber() ||
		a.GetBody().GetStringValue() != b.GetBody().GetStringValue() {
		t.Error("OTLPLogsData and OTLPLogsDataFrom disagree for the same notification")
	}

	viaResolved, err := siemwire.OTLPExportRequestJSON(resolved)
	if err != nil {
		t.Fatalf("OTLPExportRequestJSON: %v", err)
	}
	viaHelper, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	if string(viaResolved) != string(viaHelper) {
		t.Errorf("OTLPLogJSON is not just the resolved request encoded:\n resolved: %s\n   helper: %s", viaResolved, viaHelper)
	}
}

// TestARetiredNamespaceKeyCannotImpersonateLegacyProductData: Retired the bare
// olivares.* spelling of the product namespace. A caller field spelled that way would
// read as legacy product data to any receiver still mapping the pre-freeze names, so
// the retired prefix is quarantined exactly like the live one — re-homed under
// caller., with the rename recorded and the original bytes preserved.
func TestARetiredNamespaceKeyCannotImpersonateLegacyProductData(t *testing.T) {
	n := sampleNotification()
	n.Fields = map[string]string{
		"olivares.audit.hash": "spoof",
		"ordinary":            "kept",
	}
	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	_, attrs, raw, _ := otlpRecordWithRaw(t, body)

	if _, present := attrs["olivares.audit.hash"]; present {
		t.Error("a caller field in the RETIRED product namespace was emitted verbatim")
	}
	if got := attrs["caller.olivares.audit.hash"]; got != "spoof" {
		t.Errorf("the re-homed caller value was lost: %q", got)
	}
	if got := attrs["ordinary"]; got != "kept" {
		t.Errorf("an ordinary field was disturbed: %q", got)
	}
	// No attribute key may carry the retired spelling, and the rename is recorded.
	for key := range attrs {
		if strings.HasPrefix(key, "olivares.") {
			t.Errorf("bare retired-namespace key %q reached the wire", key)
		}
	}
	renamed := false
	for _, a := range decodeAdjustments(t, raw["ai.olivares.wire.adjustments"]) {
		if a.operation == "rename" && string(a.original) == "olivares.audit.hash" &&
			a.emitted == "caller.olivares.audit.hash" {
			renamed = true
		}
	}
	if !renamed {
		t.Error("the quarantine of the retired-namespace key was not recorded as an adjustment")
	}
}

// TestCallerFieldSpellingAcrossOCSFAndOTLP pins the ACTUAL per-schema policy for
// preserved caller fields, so the difference is a tested contract rather than drift.
// OTLP preserves an ordinary caller key's natural spelling (its attribute list has the
// reserved-namespace machinery to protect product keys), while OCSF parks EVERY
// preserved caller field under caller. — its `unmapped` container is one flat map
// shared with encoder-owned markers (actor.type_id, aos), so unprefixed caller keys
// could collide with and silently clobber them. Keys in the reserved (ai.olivares.*)
// or retired (olivares.*) product namespace are quarantined under caller. in BOTH.
// The policy is documented in the SIEM-egress contract.
func TestCallerFieldSpellingAcrossOCSFAndOTLP(t *testing.T) {
	n := sampleNotification()
	n.Fields = map[string]string{
		"resource":              "public.customers",
		"ai.olivares.tenant.id": "spoof-live",
		"olivares.legacy":       "spoof-retired",
	}

	body, err := OTLPLogJSON(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OTLPLogJSON: %v", err)
	}
	_, attrs, _ := otlpRecordOf(t, body)
	if got := attrs["resource"]; got != "public.customers" {
		t.Errorf("OTLP: ordinary caller key must keep its natural spelling, got %q", got)
	}
	if got := attrs["caller.ai.olivares.tenant.id"]; got != "spoof-live" {
		t.Errorf("OTLP: live-namespace spoof not quarantined: %q", got)
	}
	if got := attrs["caller.olivares.legacy"]; got != "spoof-retired" {
		t.Errorf("OTLP: retired-namespace spoof not quarantined: %q", got)
	}
	// The EXACT caller-derived key set — presence alone would tolerate the same
	// value emitted under a second, wrong spelling next to the right one.
	var otlpCallerKeys []string
	for key := range attrs {
		if !strings.HasPrefix(key, "ai.olivares.") {
			otlpCallerKeys = append(otlpCallerKeys, key)
		}
	}
	sort.Strings(otlpCallerKeys)
	wantOTLP := []string{"caller.ai.olivares.tenant.id", "caller.olivares.legacy", "resource"}
	if !reflect.DeepEqual(otlpCallerKeys, wantOTLP) {
		t.Errorf("OTLP caller-derived keys = %v, want exactly %v", otlpCallerKeys, wantOTLP)
	}

	ocsfBytes, err := OCSF(DefaultDevice(), n)
	if err != nil {
		t.Fatalf("OCSF: %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal(ocsfBytes, &ev); err != nil {
		t.Fatalf("decode OCSF: %v", err)
	}
	um, _ := ev["unmapped"].(map[string]any)
	if um == nil {
		t.Fatal("OCSF unmapped missing")
	}
	for key, want := range map[string]string{
		"caller.resource":              "public.customers",
		"caller.ai.olivares.tenant.id": "spoof-live",
		"caller.olivares.legacy":       "spoof-retired",
	} {
		if got, _ := um[key].(string); got != want {
			t.Errorf("OCSF: unmapped[%q] = %q, want %q", key, got, want)
		}
	}
	// The product keys stay authoritative and no bare pre-freeze key survives.
	if got, _ := um["ai.olivares.tenant.id"].(string); got != n.Tenant {
		t.Errorf("OCSF: authoritative tenant = %q, want %q", got, n.Tenant)
	}
	// Exact caller-derived key set for OCSF too: in particular, the ordinary
	// key must NOT also appear under its natural spelling next to caller.<k>.
	var ocsfCallerKeys []string
	for key := range um {
		if strings.HasPrefix(key, "caller.") || key == "resource" || strings.HasPrefix(key, "olivares.") {
			ocsfCallerKeys = append(ocsfCallerKeys, key)
		}
	}
	sort.Strings(ocsfCallerKeys)
	wantOCSF := []string{"caller.ai.olivares.tenant.id", "caller.olivares.legacy", "caller.resource"}
	if !reflect.DeepEqual(ocsfCallerKeys, wantOCSF) {
		t.Errorf("OCSF caller-derived keys = %v, want exactly %v", ocsfCallerKeys, wantOCSF)
	}
}
