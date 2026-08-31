// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
//
// These tests are standard-library only, like the package: the SDK declares zero
// dependencies (sdk/go.mod), so the OFFICIAL OTLP decoder cannot be used here. It is
// used on the other side of the seam instead, where the protobuf types already exist
// (connectors/otlplog decodes both real encodings and compares the whole messages).
// What these tests own is the exact byte layout, the timestamp domain and the input
// validation — the byte layout because ProtoJSON accepts several spellings for the same
// message and a decoder therefore cannot see the difference.
package siemwire

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestOTLPTimeAtEveryBoundary walks the edges of the unsigned domain. Every instant here
// is reachable, and each failure mode is a WRONG DATE a decoder accepts rather than an
// error. Expected values are derived from the documented uint64 ceiling, not copied from
// the implementation's output. It is a finite boundary table, not proof of the whole
// domain — TestOTLPTimeMatchesABigIntOracle covers the interior.
func TestOTLPTimeAtEveryBoundary(t *testing.T) {
	cases := []struct {
		name       string
		in         time.Time
		want       uint64
		wantStatus OTLPTimeStatus
	}{
		{"the zero time is absent", time.Time{}, 0, OTLPTimeAbsent},
		{"the epoch is representable but indistinguishable", time.Unix(0, 0).UTC(), 0, OTLPTimeAtEpoch},
		{"one nanosecond after the epoch", time.Unix(0, 1).UTC(), 1, OTLPTimeExact},
		{"a normal instant", time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), 1781258400000000000, OTLPTimeExact},
		{"nanosecond precision survives", time.Unix(1781258400, 123456789).UTC(), 1781258400123456789, OTLPTimeExact},
		// Past int64 nanoseconds (which wraps to a negative) but inside uint64.
		{"a year-2263 instant", time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC), 9246182400000000000, OTLPTimeExact},
		{"the last representable second", time.Unix(18446744073, 0).UTC(), 18446744073000000000, OTLPTimeExact},
		{"the exact ceiling", time.Unix(18446744073, 709551615).UTC(), math.MaxUint64, OTLPTimeExact},
		// One nanosecond further there is no representation at all.
		{"one nanosecond past the ceiling", time.Unix(18446744073, 709551616).UTC(), 0, OTLPTimeAfterCeiling},
		{"one second past the ceiling", time.Unix(18446744074, 0).UTC(), 0, OTLPTimeAfterCeiling},
		{"far past the ceiling", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), 0, OTLPTimeAfterCeiling},
		// Nothing before the epoch has an unsigned representation.
		{"one nanosecond before the epoch", time.Unix(-1, 999999999).UTC(), 0, OTLPTimeBeforeEpoch},
		{"a pre-epoch instant", time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC), 0, OTLPTimeBeforeEpoch},
		{"long before the epoch", time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC), 0, OTLPTimeBeforeEpoch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, status := OTLPTime(tc.in)
			if got != tc.want {
				t.Errorf("OTLPTime(%s) = %d, want %d", tc.in.Format(time.RFC3339Nano), got, tc.want)
			}
			// The status is the whole reason this signature exists: three of these
			// inputs encode to the same 0 and only one of them loses nothing.
			if status != tc.wantStatus {
				t.Errorf("status = %v (%s), want %v (%s)", status, status, tc.wantStatus, tc.wantStatus)
			}
			if bare := OTLPTimeUnixNano(tc.in); bare != tc.want {
				t.Errorf("OTLPTimeUnixNano disagrees with OTLPTime: %d vs %d", bare, tc.want)
			}
		})
	}
}

// TestOTLPTimeMatchesABigIntOracle checks the guard against arbitrary-precision arithmetic,
// so it is not merely consistent with a hand-written table. The seed is fixed: a property
// test that cannot be re-run identically is not evidence.
//
// The sampling is STRATIFIED and each stratum is named, because "random over the int64
// range" would be a false description — almost every such sample is far past the ceiling and
// the interesting states would never occur. An earlier revision sampled four strata whose
// widest cohort was [0, 2^40) and, measured, reached the epoch branch ZERO times out of
// 20 000: an assertion arm that never executes verifies nothing. The counts are therefore
// asserted at the end, so the test proves its own sampling reached what it claims.
func TestOTLPTimeMatchesABigIntOracle(t *testing.T) {
	scale := big.NewInt(int64(nsPerSecond))
	ceiling := new(big.Int).SetUint64(math.MaxUint64)
	rng := rand.New(rand.NewSource(0x0117A1E5))

	// Each stratum names the region it covers and how a second/nanosecond pair is drawn.
	strata := []struct {
		name string
		draw func() (int64, int64)
	}{
		{"around the epoch, where the AtEpoch state lives", func() (int64, int64) {
			return rng.Int63n(3) - 1, rng.Int63n(3)
		}},
		{"ordinary contemporary instants", func() (int64, int64) {
			return rng.Int63n(4_000_000_000), rng.Int63n(1_000_000_000)
		}},
		{"before the epoch", func() (int64, int64) {
			return -1 - rng.Int63n(4_000_000_000), rng.Int63n(1_000_000_000)
		}},
		{"the exact ceiling second, where the ADDITION overflows", func() (int64, int64) {
			return 18446744073, rng.Int63n(1_000_000_000)
		}},
		{"just past the ceiling second, where the MULTIPLICATION overflows", func() (int64, int64) {
			return 18446744074 + rng.Int63n(1000), rng.Int63n(1_000_000_000)
		}},
	}

	counts := map[OTLPTimeStatus]int{}
	for i := 0; i < 20000; i++ {
		stratum := strata[i%len(strata)]
		sec, nsec := stratum.draw()
		in := time.Unix(sec, nsec).UTC()

		want := new(big.Int).Mul(big.NewInt(sec), scale)
		want.Add(want, big.NewInt(nsec))

		got, status := OTLPTime(in)
		counts[status]++
		switch {
		case want.Sign() < 0:
			if got != 0 || status != OTLPTimeBeforeEpoch {
				t.Fatalf("%s: sec=%d nsec=%d is before the epoch, got %d/%s", stratum.name, sec, nsec, got, status)
			}
		case want.Cmp(ceiling) > 0:
			if got != 0 || status != OTLPTimeAfterCeiling {
				t.Fatalf("%s: sec=%d nsec=%d exceeds uint64 (%s), got %d/%s", stratum.name, sec, nsec, want, got, status)
			}
		case want.Sign() == 0:
			if got != 0 || status != OTLPTimeAtEpoch {
				t.Fatalf("%s: sec=%d nsec=%d IS the epoch, got %d/%s", stratum.name, sec, nsec, got, status)
			}
		default:
			if got != want.Uint64() || status != OTLPTimeExact {
				t.Fatalf("%s: sec=%d nsec=%d: got %d/%s, want %s/exact", stratum.name, sec, nsec, got, status, want)
			}
		}
	}

	// Every state the loop can assert must actually have occurred. Without this, a stratum
	// that stops reaching its region turns a verified branch into dead code silently.
	// OTLPTimeAbsent is NOT expected here: time.Unix never produces the Go zero time, and
	// the boundary table owns that case.
	for _, status := range []OTLPTimeStatus{
		OTLPTimeExact, OTLPTimeAtEpoch, OTLPTimeBeforeEpoch, OTLPTimeAfterCeiling,
	} {
		if counts[status] == 0 {
			t.Errorf("the sampling never produced %s, so that assertion arm verified nothing (counts: %v)", status, countsByName(counts))
		}
	}
	if counts[OTLPTimeAbsent] != 0 {
		t.Errorf("time.Unix produced the Go zero time %d times, which should be impossible", counts[OTLPTimeAbsent])
	}
}

// countsByName renders the branch tally with readable keys for a failure message.
func countsByName(counts map[OTLPTimeStatus]int) map[string]int {
	out := make(map[string]int, len(counts))
	for status, n := range counts {
		out[status.String()] = n
	}
	return out
}

// TestOTLPTimeIsIndependentOfLocationAndMonotonicReading: two Time values for the SAME
// instant must encode identically whatever their location, and a clock reading with a
// monotonic component must encode like the same instant without one. Neither property
// distinguishes this implementation from the old naive cast — they are contract tests for
// the field, not evidence that the overflow fix works, which is what the boundary table
// and the oracle are for.
func TestOTLPTimeIsIndependentOfLocationAndMonotonicReading(t *testing.T) {
	utc := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	madrid := time.FixedZone("CEST", 2*60*60)
	if a, b := OTLPTimeUnixNano(utc), OTLPTimeUnixNano(utc.In(madrid)); a != b {
		t.Errorf("same instant encoded differently: utc=%d other-zone=%d", a, b)
	}

	now := time.Now()
	if a, b := OTLPTimeUnixNano(now), OTLPTimeUnixNano(now.Round(0)); a != b {
		t.Errorf("monotonic reading changed the encoding: with=%d stripped=%d", a, b)
	}
}

// otlpGolden is the complete expected body. It pins what a decoder cannot: the
// lowerCamel member spellings, the quoted 64-bit strings, the NUMERIC severity enum, the
// dedicated eventName member, the attribute order, and the absence of any member this
// layout does not declare.
const otlpGolden = `{"resourceLogs":[{"resource":{"attributes":[` +
	`{"key":"service.name","value":{"stringValue":"ControlPlane"}},` +
	`{"key":"service.version","value":{"stringValue":"1"}}]},` +
	`"scopeLogs":[{"scope":{"name":"ai.olivares.test","version":"7"},` +
	`"logRecords":[{"timeUnixNano":"1781258400000000000","observedTimeUnixNano":"0",` +
	`"severityNumber":17,"severityText":"ERROR","eventName":"governance.policy.denied",` +
	`"body":{"stringValue":"a thing happened"},` +
	`"attributes":[{"key":"b","value":{"stringValue":"second"}},` +
	`{"key":"a","value":{"stringValue":"first"}}]}]}]}]}`

func sampleRequest() OTLPRequest {
	return OTLPRequest{
		ResourceAttributes: OTLPStringFields([]Field{
			{Key: "service.name", Value: "ControlPlane"},
			{Key: "service.version", Value: "1"},
		}),
		// The scope names the producing component and carries its own version, not the
		// service's — see OTLPRequest.ScopeVersion.
		ScopeName:    "ai.olivares.test",
		ScopeVersion: "7",
		Record: OTLPRecord{
			TimeUnixNano:   1781258400000000000,
			SeverityNumber: 17,
			SeverityText:   "ERROR",
			EventName:      "governance.policy.denied",
			Body:           "a thing happened",
			// Deliberately NOT alphabetical: the encoder must never sort.
			Attributes: OTLPStringFields([]Field{{Key: "b", Value: "second"}, {Key: "a", Value: "first"}}),
		},
	}
}

func TestOTLPExportRequestJSONExactBody(t *testing.T) {
	got, err := OTLPExportRequestJSON(sampleRequest())
	if err != nil {
		t.Fatalf("OTLPExportRequestJSON: %v", err)
	}
	if string(got) != otlpGolden {
		t.Errorf("body differs:\n got: %s\nwant: %s", got, otlpGolden)
	}
}

// recordOf decodes the single log record as a member map, so a test can assert exact
// presence and exact raw tokens rather than searching the document for a substring. It
// also asserts the envelope cardinality, so a structural change fails here diagnostically
// instead of panicking on an index.
func recordOf(t *testing.T, body []byte) (resourceAttrs, recordAttrs json.RawMessage, record map[string]json.RawMessage) {
	t.Helper()
	var doc struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes json.RawMessage `json:"attributes"`
			} `json:"resource"`
			ScopeLogs []struct {
				LogRecords []map[string]json.RawMessage `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not valid JSON: %v\n%s", err, body)
	}
	if len(doc.ResourceLogs) != 1 {
		t.Fatalf("resourceLogs has %d entries, want 1: %s", len(doc.ResourceLogs), body)
	}
	if len(doc.ResourceLogs[0].ScopeLogs) != 1 {
		t.Fatalf("scopeLogs has %d entries, want 1: %s", len(doc.ResourceLogs[0].ScopeLogs), body)
	}
	if n := len(doc.ResourceLogs[0].ScopeLogs[0].LogRecords); n != 1 {
		t.Fatalf("logRecords has %d entries, want 1: %s", n, body)
	}
	rec := doc.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	return doc.ResourceLogs[0].Resource.Attributes, rec["attributes"], rec
}

// TestOTLPExportRequestJSONDeclaresExactlyItsProfile: the reason this encoder exists
// instead of default protojson is that a declared value must have ONE byte shape. An
// omitted severityNumber gives a raw-JSON consumer two presence shapes for one column.
//
// It asserts BOTH directions, on a decoded member map rather than with substring
// searches: every declared member is present with its exact zero token, and no member
// outside the declared profile appears. A substring check would be satisfied by either of
// the two attributes arrays and could not see a member that should not be there at all.
func TestOTLPExportRequestJSONDeclaresExactlyItsProfile(t *testing.T) {
	got, err := OTLPExportRequestJSON(OTLPRequest{})
	if err != nil {
		t.Fatalf("OTLPExportRequestJSON: %v", err)
	}
	resourceAttrs, recordAttrs, rec := recordOf(t, got)

	want := map[string]string{
		"timeUnixNano":         `"0"`,
		"observedTimeUnixNano": `"0"`,
		"severityNumber":       `0`,
		"severityText":         `""`,
		"eventName":            `""`,
		"body":                 `{"stringValue":""}`,
		"attributes":           `[]`,
	}
	for member, token := range want {
		raw, ok := rec[member]
		if !ok {
			t.Errorf("declared member %q is absent: %s", member, got)
			continue
		}
		if string(raw) != token {
			t.Errorf("member %q = %s, want %s", member, raw, token)
		}
	}
	if len(rec) != len(want) {
		t.Errorf("record has %d members, want exactly %d (the declared profile); got %v", len(rec), len(want), rec)
	}
	// Both attribute arrays are [] and never null: `null` and `[]` are two byte forms
	// for "no attributes" and a consumer indexing the array should not meet both.
	if string(resourceAttrs) != `[]` {
		t.Errorf("resource attributes = %s, want []", resourceAttrs)
	}
	if string(recordAttrs) != `[]` {
		t.Errorf("record attributes = %s, want []", recordAttrs)
	}
	if strings.Contains(string(got), "null") {
		t.Errorf("no member may render as null:\n%s", got)
	}
}

// TestOTLPExportRequestJSONNeverSortsAttributes: siemwire's contract across every encoder
// is that the CALLER owns field order (the findings encoder sorts, the audit encoder uses
// a fixed semantic order). Sorting here would silently override both. Asserted on the
// decoded key sequence, so it cannot be satisfied by a coincidental substring position.
func TestOTLPExportRequestJSONNeverSortsAttributes(t *testing.T) {
	req := sampleRequest()
	req.Record.Attributes = OTLPStringFields([]Field{{Key: "z", Value: "1"}, {Key: "a", Value: "2"}, {Key: "m", Value: "3"}})
	got, err := OTLPExportRequestJSON(req)
	if err != nil {
		t.Fatalf("OTLPExportRequestJSON: %v", err)
	}
	_, recordAttrs, _ := recordOf(t, got)
	var decoded []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	}
	if err := json.Unmarshal(recordAttrs, &decoded); err != nil {
		t.Fatalf("attributes are not decodable: %v\n%s", err, recordAttrs)
	}
	gotKeys := make([]string, 0, len(decoded))
	for _, a := range decoded {
		gotKeys = append(gotKeys, a.Key)
	}
	if strings.Join(gotKeys, ",") != "z,a,m" {
		t.Errorf("attribute order = %v, want [z a m] exactly as given", gotKeys)
	}
}

// TestOTLPExportRequestJSONIsRepeatableWithinABuild: encoding the same input twice must
// give the same bytes. This is the WEAK half of the byte-stability property — it proves
// repeatability inside one process, not stability across library versions, which is what
// protojson declines to promise. The golden above is what actually guards that, because
// it fails when the shape changes for any reason at all.
func TestOTLPExportRequestJSONIsRepeatableWithinABuild(t *testing.T) {
	req := sampleRequest()
	first, err := OTLPExportRequestJSON(req)
	if err != nil {
		t.Fatalf("OTLPExportRequestJSON: %v", err)
	}
	for i := 0; i < 50; i++ {
		again, err := OTLPExportRequestJSON(req)
		if err != nil {
			t.Fatalf("OTLPExportRequestJSON: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("render %d differs:\n first: %s\nsecond: %s", i, first, again)
		}
	}
}

// TestOTLPExportRequestJSONEscapesHostileValues: a value carrying a quote, a brace or a
// newline must not be able to break out of its JSON string. encoding/json does that, so
// what is pinned is the exact escaped WIRE form — the form a consumer sees — AND the
// decoded value, so the record cannot pass by having the right escapes around the wrong
// content.
func TestOTLPExportRequestJSONEscapesHostileValues(t *testing.T) {
	req := sampleRequest()
	req.Record.Body = "quote\" brace} newline\n tab\t"
	req.Record.Attributes = OTLPStringFields([]Field{{Key: `k"ey`, Value: "<script>alert(1)</script>"}})
	got, err := OTLPExportRequestJSON(req)
	if err != nil {
		t.Fatalf("OTLPExportRequestJSON: %v", err)
	}
	body := string(got)
	if !strings.Contains(body, `"stringValue":"quote\" brace} newline\n tab\t"`) {
		t.Errorf("body value is not escaped as expected: %s", body)
	}
	if !strings.Contains(body, `"key":"k\"ey"`) {
		t.Errorf("attribute key is not escaped: %s", body)
	}
	// And it decodes back to exactly what went in — one record, one attribute.
	_, recordAttrs, rec := recordOf(t, got)
	var decodedBody struct {
		StringValue string `json:"stringValue"`
	}
	if err := json.Unmarshal(rec["body"], &decodedBody); err != nil {
		t.Fatalf("body is not decodable: %v", err)
	}
	if decodedBody.StringValue != req.Record.Body {
		t.Errorf("decoded body = %q, want %q", decodedBody.StringValue, req.Record.Body)
	}
	var decodedAttrs []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	}
	if err := json.Unmarshal(recordAttrs, &decodedAttrs); err != nil {
		t.Fatalf("attributes are not decodable: %v", err)
	}
	if len(decodedAttrs) != 1 {
		t.Fatalf("hostile input changed the attribute count: %v", decodedAttrs)
	}
	if decodedAttrs[0].Key != `k"ey` || decodedAttrs[0].Value.StringValue != "<script>alert(1)</script>" {
		t.Errorf("attribute round-trip = %+v, want the exact input back", decodedAttrs[0])
	}
	// encoding/json escapes < and > by default (HTML-safe), so on the WIRE they are
	// \uXXXX escapes and the raw characters must not appear at all. The expected escape
	// is BUILT from the rune rather than typed: a source line holding the escape
	// sequence and one holding the raw character are nearly indistinguishable in review,
	// and an expectation accidentally written against the raw form would assert the
	// opposite of the property. The decode above already proved the value survives.
	lt := fmt.Sprintf("\\u%04x", '<')
	gt := fmt.Sprintf("\\u%04x", '>')
	wantWire := lt + "script" + gt + "alert(1)" + lt + "/script" + gt
	if !strings.Contains(body, wantWire) {
		t.Errorf("angle brackets are not escaped as %s on the wire: %s", wantWire, body)
	}
	if strings.ContainsAny(body, "<>") {
		t.Errorf("a raw angle bracket reached the wire: %s", body)
	}
}

// TestOTLPExportRequestJSONRejectsInvalidUTF8: encoding/json substitutes U+FFFD for every
// invalid UTF-8 sequence, so "invalid\xff" and "invalid\xfe" would serialize to identical
// bytes and neither the emitter nor the consumer could tell which value was supplied. For
// an audit feed that silent collapse is worse than an error — the same event exported as
// CEF or syslog carries the original bytes untouched — so this layer refuses instead of
// repairing. A caller that would rather deliver a marked, altered record does the
// substitution in its own policy layer.
func TestOTLPExportRequestJSONRejectsInvalidUTF8(t *testing.T) {
	bad := "invalid\xff"
	cases := []struct {
		name string
		mut  func(*OTLPRequest)
		want string
	}{
		{"scope name", func(r *OTLPRequest) { r.ScopeName = bad }, "scope name"},
		{"scope version", func(r *OTLPRequest) { r.ScopeVersion = bad }, "scope version"},
		{"severity text", func(r *OTLPRequest) { r.Record.SeverityText = bad }, "severity text"},
		{"event name", func(r *OTLPRequest) { r.Record.EventName = bad }, "event name"},
		{"body", func(r *OTLPRequest) { r.Record.Body = bad }, "body"},
		{"a record attribute value", func(r *OTLPRequest) {
			r.Record.Attributes = OTLPStringFields([]Field{{Key: "k", Value: bad}})
		}, `record attribute value for "k"`},
		{"a record attribute key", func(r *OTLPRequest) {
			r.Record.Attributes = OTLPStringFields([]Field{{Key: bad, Value: "v"}})
		}, "record attribute[0] key"},
		{"a resource attribute value", func(r *OTLPRequest) {
			r.ResourceAttributes = OTLPStringFields([]Field{{Key: "k", Value: bad}})
		}, `resource attribute value for "k"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := sampleRequest()
			tc.mut(&req)
			got, err := OTLPExportRequestJSON(req)
			if err == nil {
				t.Fatalf("invalid UTF-8 in the %s was accepted: %s", tc.name, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the offending field (%q)", err, tc.want)
			}
			if got != nil {
				t.Errorf("bytes returned alongside an error: %s", got)
			}
		})
	}

	// The replacement character itself is valid UTF-8 and must pass: a caller that has
	// already substituted must not be rejected for it.
	req := sampleRequest()
	req.Record.Body = "invalid�"
	if _, err := OTLPExportRequestJSON(req); err != nil {
		t.Errorf("an already-substituted value was rejected: %v", err)
	}
}

// TestOTLPExportRequestJSONRejectsUnusableAttributeKeys: OTLP requires attribute keys to
// be unique within a set and states that a receiver's handling of duplicates is
// unpredictable, so a duplicate is an encoding error here rather than something to pass on
// and hope about. An empty key is rejected for the plainer reason that nothing can query
// it.
func TestOTLPExportRequestJSONRejectsUnusableAttributeKeys(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*OTLPRequest)
		want string
	}{
		{"duplicate record keys", func(r *OTLPRequest) {
			r.Record.Attributes = OTLPStringFields([]Field{{Key: "k", Value: "1"}, {Key: "k", Value: "2"}})
		}, `record attribute key "k" appears more than once`},
		{"duplicate resource keys", func(r *OTLPRequest) {
			r.ResourceAttributes = OTLPStringFields([]Field{{Key: "service.name", Value: "a"}, {Key: "service.name", Value: "b"}})
		}, `resource attribute key "service.name" appears more than once`},
		{"an empty record key", func(r *OTLPRequest) {
			r.Record.Attributes = OTLPStringFields([]Field{{Key: "", Value: "1"}})
		}, "record attribute[0] has an empty key"},
		{"an empty resource key", func(r *OTLPRequest) {
			r.ResourceAttributes = OTLPStringFields([]Field{{Key: "", Value: "1"}})
		}, "resource attribute[0] has an empty key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := sampleRequest()
			tc.mut(&req)
			got, err := OTLPExportRequestJSON(req)
			if err == nil {
				t.Fatalf("%s was accepted: %s", tc.name, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say why (%q)", err, tc.want)
			}
		})
	}

	// Same key in DIFFERENT sets is fine: uniqueness is per attribute set.
	req := sampleRequest()
	req.ResourceAttributes = OTLPStringFields([]Field{{Key: "k", Value: "resource"}})
	req.Record.Attributes = OTLPStringFields([]Field{{Key: "k", Value: "record"}})
	if _, err := OTLPExportRequestJSON(req); err != nil {
		t.Errorf("the same key in two different sets was rejected: %v", err)
	}
}

// TestOTLPTimeStatusTokensAreStable: the tokens travel on the wire in an attribute value,
// so they are a contract, not a debug string. Pinned literally, and every declared status
// must have a distinct token.
func TestOTLPTimeStatusTokensAreStable(t *testing.T) {
	want := map[OTLPTimeStatus]string{
		OTLPTimeAbsent:       "absent",
		OTLPTimeExact:        "exact",
		OTLPTimeAtEpoch:      "epoch_indistinguishable_from_unknown",
		OTLPTimeBeforeEpoch:  "before_epoch",
		OTLPTimeAfterCeiling: "after_uint64_ceiling",
	}
	seen := make(map[string]OTLPTimeStatus, len(want))
	for status, token := range want {
		if got := status.String(); got != token {
			t.Errorf("status %d token = %q, want %q", status, got, token)
		}
		if prev, dup := seen[token]; dup {
			t.Errorf("token %q is shared by status %d and %d", token, prev, status)
		}
		seen[token] = status
	}
	// An unnamed value must not silently read as a real state.
	if got := OTLPTimeStatus(200).String(); got != "unspecified" {
		t.Errorf("an unknown status reads as %q, want \"unspecified\"", got)
	}
}

// TestOTLPTimestampsAreQuotedDecimalStrings: ProtoJSON encodes 64-bit fields as strings,
// and a bare JSON number also loses precision in JavaScript-class consumers. The ceiling
// value is where a float would visibly break, so it is the one asserted.
func TestOTLPTimestampsAreQuotedDecimalStrings(t *testing.T) {
	req := sampleRequest()
	req.Record.TimeUnixNano = math.MaxUint64
	req.Record.ObservedTimeUnixNano = math.MaxUint64 - 1
	got, err := OTLPExportRequestJSON(req)
	if err != nil {
		t.Fatalf("OTLPExportRequestJSON: %v", err)
	}
	_, _, rec := recordOf(t, got)
	for member, want := range map[string]uint64{
		"timeUnixNano":         math.MaxUint64,
		"observedTimeUnixNano": math.MaxUint64 - 1,
	} {
		raw := string(rec[member])
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			t.Fatalf("%s = %s, want a QUOTED decimal string", member, raw)
		}
		n, err := strconv.ParseUint(strings.Trim(raw, `"`), 10, 64)
		if err != nil {
			t.Fatalf("%s = %s, not a decimal uint64: %v", member, raw, err)
		}
		if n != want {
			t.Errorf("%s = %d, want %d (full precision preserved)", member, n, want)
		}
	}
}

// TestDeeplyNestedValuesAreRejectedNotFatal is the regression test for a crash, not a bug:
// both the validator and the encoder walk the AnyValue union recursively, and Go's stack
// overflow is a FATAL ERROR that no recover() can catch — a sufficiently nested value took
// the whole process down. This package is the public SDK third-party connectors build
// against, so the depth a caller reaches is not something this repository controls.
//
// The test builds a value well past the declared limit and requires an ERROR. If the bound
// regresses, this test does not fail: the test BINARY dies, which is itself unmistakable.
func TestDeeplyNestedValuesAreRejectedNotFatal(t *testing.T) {
	nest := func(depth int) OTLPValue {
		v := OTLPString("leaf")
		for i := 0; i < depth; i++ {
			v = OTLPArray(v)
		}
		return v
	}

	t.Run("an array nested just past the limit", func(t *testing.T) {
		_, err := OTLPValueJSON(nest(maxOTLPValueDepth + 5))
		if err == nil {
			t.Fatal("a value nested past the limit was accepted")
		}
		if !strings.Contains(err.Error(), "nests deeper than") {
			t.Errorf("error %q does not say why", err)
		}
	})

	// The depth that actually matters. A few levels past the limit only exercises the
	// bound; this one is deep enough that an UNBOUNDED walk exhausts the goroutine stack,
	// which in Go is a fatal error no recover() can catch. Measured: without the bound the
	// walk dies at roughly eight thousand levels, so 200 000 is unambiguous. The bounded
	// version must return an error immediately instead — and it never touches level 33.
	t.Run("a depth that would exhaust the stack", func(t *testing.T) {
		_, err := OTLPValueJSON(nest(200_000))
		if err == nil {
			t.Fatal("a pathologically nested value was accepted")
		}
		if !strings.Contains(err.Error(), "nests deeper than") {
			t.Errorf("error %q does not say why", err)
		}
	})

	t.Run("a request whose attribute nests past the limit", func(t *testing.T) {
		req := sampleRequest()
		req.Record.Attributes = []OTLPKeyValue{{Key: "deep", Value: nest(maxOTLPValueDepth + 5)}}
		got, err := OTLPExportRequestJSON(req)
		if err == nil {
			t.Fatalf("a request nested past the limit was encoded: %s", got)
		}
		if !strings.Contains(err.Error(), "nests deeper than") {
			t.Errorf("error %q does not say why", err)
		}
	})

	t.Run("alternating array and kvlist nesting is bounded too", func(t *testing.T) {
		v := OTLPString("leaf")
		for i := 0; i < maxOTLPValueDepth+5; i++ {
			if i%2 == 0 {
				v = OTLPArray(v)
				continue
			}
			v = OTLPKVList(OTLPKeyValue{Key: "k", Value: v})
		}
		if _, err := OTLPValueJSON(v); err == nil {
			t.Fatal("alternating nesting past the limit was accepted")
		}
	})

	t.Run("nesting within the limit still works", func(t *testing.T) {
		// The bound must not reject shapes a caller legitimately builds. Three levels is
		// already more than any real telemetry attribute.
		got, err := OTLPValueJSON(nest(3))
		if err != nil {
			t.Fatalf("a three-level value was rejected: %v", err)
		}
		if !strings.Contains(string(got), `"leaf"`) {
			t.Errorf("the leaf value did not survive: %s", got)
		}
	})
}
