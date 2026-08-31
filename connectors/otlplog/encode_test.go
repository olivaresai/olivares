// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
//
// encode() is where the connector CHOOSES between two independent layouts of the same
// notification: the binary path marshals the generated protobuf types, the JSON path goes
// through the SDK's declared field layout. Resolving once (siemfmt.OTLPRequestFor) makes
// them share their source VALUES, but each still decides which members it lays out — so
// nothing about the architecture alone prevents one from carrying a member the other does
// not. That is what these tests close, by comparing the WHOLE decoded messages rather than
// a hand-listed set of fields: a selected-field comparison passes happily while a new
// member exists on one side only, which is exactly how the two would drift.
package otlplog

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// encodeCases exercise the values that used to differ between the two paths, plus the ones
// the shared timestamp guard exists for: each of these instants once produced a plausible
// WRONG date that a decoder accepts.
var encodeCases = []struct {
	name string
	mut  func(*sdk.Notification)
	// device, when set, overrides the connector's device identity. An earlier revision
	// called a case "a device override" while mutating the NOTIFICATION's tenant, so a
	// projection bug conditional on the device identity had no parity coverage at all.
	device *siemfmt.Device
}{
	{name: "the sample notification", mut: func(*sdk.Notification) {}},
	{name: "an unknown severity", mut: func(n *sdk.Notification) { n.Severity = "" }},
	{name: "an info severity", mut: func(n *sdk.Notification) { n.Severity = model.SeverityInfo }},
	{name: "a low severity", mut: func(n *sdk.Notification) { n.Severity = model.SeverityLow }},
	{name: "a medium severity", mut: func(n *sdk.Notification) { n.Severity = model.SeverityMedium }},
	{name: "a high severity", mut: func(n *sdk.Notification) { n.Severity = model.SeverityHigh }},
	{name: "a critical severity", mut: func(n *sdk.Notification) { n.Severity = model.SeverityCritical }},
	{name: "the zero time", mut: func(n *sdk.Notification) { n.Time = time.Time{} }},
	{name: "the epoch", mut: func(n *sdk.Notification) { n.Time = time.Unix(0, 0).UTC() }},
	{name: "a pre-epoch time", mut: func(n *sdk.Notification) {
		n.Time = time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC)
	}},
	{name: "a year-2263 time", mut: func(n *sdk.Notification) {
		n.Time = time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC)
	}},
	{name: "the exact uint64 nanosecond ceiling", mut: func(n *sdk.Notification) {
		n.Time = time.Unix(18446744073, 709551615).UTC()
	}},
	{name: "one second past the ceiling", mut: func(n *sdk.Notification) {
		n.Time = time.Unix(18446744074, 0).UTC()
	}},
	{name: "no fields at all", mut: func(n *sdk.Notification) { n.Fields = nil }},
	{name: "an empty type", mut: func(n *sdk.Notification) { n.Type = "" }},
	{name: "an empty title and body", mut: func(n *sdk.Notification) { n.Title, n.Body = "", "" }},
	{name: "a caller field in the reserved namespace", mut: func(n *sdk.Notification) {
		n.Fields = map[string]string{"ai.olivares.event.time": "supplied", "ok": "1"}
	}},
	{name: "invalid UTF-8 in a value", mut: func(n *sdk.Notification) {
		n.Fields = map[string]string{"path": "na\xffme"}
	}},
	{name: "invalid UTF-8 in a KEY, which used to drop the record entirely", mut: func(n *sdk.Notification) {
		n.Fields = map[string]string{"bad\xffkey": "value"}
	}},
	{name: "an empty caller key", mut: func(n *sdk.Notification) {
		n.Fields = map[string]string{"": "value with no key"}
	}},
	{name: "a device override", mut: func(*sdk.Notification) {}, device: &siemfmt.Device{
		Vendor: "Reseller", Product: "Rebranded", Version: "9", ServiceVersion: "1.4.2",
	}},
	{name: "another tenant", mut: func(n *sdk.Notification) { n.Tenant = "other-tenant" }},
}

// TestEncodeBothEncodingsAgree decodes each body to the SAME generated type and compares
// the complete messages with proto.Equal. Comparing bytes would be wrong — the two
// encodings are legitimately different byte sequences; what must be identical is the
// message. The JSON body is decoded with unknown members REJECTED, so a misspelled member
// fails here instead of being discarded and compared against our own mistake.
func TestEncodeBothEncodingsAgree(t *testing.T) {
	for _, tc := range encodeCases {
		t.Run(tc.name, func(t *testing.T) {
			n := sampleNotification()
			tc.mut(&n)
			device := siemfmt.Device{}
			if tc.device != nil {
				device = *tc.device
			}

			jsonOut := &Output{encoding: encodingJSON, maxAttempts: defaultMaxAttempts, device: device}
			jb, jct, err := jsonOut.encode(n)
			if err != nil {
				t.Fatalf("encode json: %v", err)
			}
			if jct != "application/json" {
				t.Errorf("json content-type = %q, want application/json", jct)
			}

			protoOut := &Output{encoding: encodingProto, maxAttempts: defaultMaxAttempts, device: device}
			pb, pct, err := protoOut.encode(n)
			if err != nil {
				t.Fatalf("encode protobuf: %v", err)
			}
			if pct != "application/x-protobuf" {
				t.Errorf("protobuf content-type = %q, want application/x-protobuf", pct)
			}

			var fromJSON collogspb.ExportLogsServiceRequest
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(jb, &fromJSON); err != nil {
				t.Fatalf("the JSON body is not a valid ExportLogsServiceRequest: %v\n%s", err, jb)
			}
			var fromProto collogspb.ExportLogsServiceRequest
			if err := proto.Unmarshal(pb, &fromProto); err != nil {
				t.Fatalf("the protobuf body is not a valid ExportLogsServiceRequest: %v", err)
			}

			// The whole message, not a field list: this is what would catch a member added
			// to one projection and forgotten in the other.
			if !proto.Equal(&fromJSON, &fromProto) {
				t.Errorf("the two encodings describe different messages:\n json: %v\nproto: %v", &fromJSON, &fromProto)
			}
		})
	}
}

// TestEncodeBinaryTimestampIsGuardedToo pins the ABSOLUTE value the binary projection
// carries at each edge. The parity test above compares the two encodings with each other,
// so it would stay green if BOTH regressed together; only an independently derived
// expectation catches that. These values come from the documented uint64 ceiling.
//
// It is also the regression test for a release-note claim: the binary MECHANISM is
// unchanged (still the generated types), but the VALUE it carries for an instant outside
// OTLP's representable range changed from a wrapped number to 0 — which proto3 then omits
// from the wire entirely.
func TestEncodeBinaryTimestampIsGuardedToo(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want uint64
	}{
		{"a normal instant", time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), 1781258400000000000},
		{"a year-2263 instant", time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC), 9246182400000000000},
		{"the exact ceiling", time.Unix(18446744073, 709551615).UTC(), 18446744073709551615},
		// Previously 18432561093709551616, which reads as 2554-02-07T19:51:33.709551616Z.
		{"a pre-epoch instant", time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC), 0},
		// Previously 290448384, i.e. 1970-01-01T00:00:00.290448384Z.
		{"one second past the ceiling", time.Unix(18446744074, 0).UTC(), 0},
		{"the zero time", time.Time{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := sampleNotification()
			n.Time = tc.when
			o := &Output{encoding: encodingProto, maxAttempts: defaultMaxAttempts}
			body, _, err := o.encode(n)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var req collogspb.ExportLogsServiceRequest
			if err := proto.Unmarshal(body, &req); err != nil {
				t.Fatalf("body is not a valid OTLP/protobuf request: %v", err)
			}
			rec, err := getRecord(&req)
			if err != nil {
				t.Fatal(err)
			}
			if rec.GetTimeUnixNano() != tc.want {
				t.Errorf("binary timeUnixNano = %d, want %d", rec.GetTimeUnixNano(), tc.want)
			}
		})
	}
}

// TestEncodeJSONSeverityAndTimestampAreRawTokens asserts on the RAW bytes, which is the one
// thing decoding cannot check: ProtoJSON accepts an enum as a number or as its name and
// both decode identically, and an absent proto3 scalar decodes the same as an explicit
// zero. OTLP/JSON requires the integer token; this product additionally always emits the
// member, so a SIEM rule has one column with one shape.
func TestEncodeJSONSeverityAndTimestampAreRawTokens(t *testing.T) {
	for _, tc := range encodeCases {
		t.Run(tc.name, func(t *testing.T) {
			n := sampleNotification()
			tc.mut(&n)
			device := siemfmt.Device{}
			if tc.device != nil {
				device = *tc.device
			}
			o := &Output{encoding: encodingJSON, maxAttempts: defaultMaxAttempts, device: device}
			body, _, err := o.encode(n)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
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
				t.Fatalf("want exactly one resource/scope/record: %s", body)
			}
			rec := doc.ResourceLogs[0].ScopeLogs[0].LogRecords[0]

			sev, ok := rec["severityNumber"]
			if !ok {
				t.Fatalf("severityNumber is absent: %s", body)
			}
			if strings.HasPrefix(string(sev), `"`) {
				t.Fatalf("severityNumber is the quoted token %s; OTLP/JSON requires an integer", sev)
			}
			if _, err := strconv.ParseInt(string(sev), 10, 32); err != nil {
				t.Fatalf("severityNumber %s is not a base-10 integer: %v", sev, err)
			}

			stamp, ok := rec["timeUnixNano"]
			if !ok {
				t.Fatalf("timeUnixNano is absent; this product always emits it, with 0 for unknown: %s", body)
			}
			raw := string(stamp)
			if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
				t.Fatalf("timeUnixNano = %s, want a quoted decimal string", raw)
			}
			if _, err := strconv.ParseUint(strings.Trim(raw, `"`), 10, 64); err != nil {
				t.Fatalf("timeUnixNano %s is not a decimal uint64: %v", raw, err)
			}
		})
	}
}

// TestEncodeProjectsTheResolvedRequest: the body encode() produces must be exactly the
// projection of siemfmt.OTLPRequestFor's output — no second, divergent resolution path.
//
// The name is deliberately NOT "resolves once". OTLPRequestFor is pure and deterministic, so
// calling it twice yields the same value and NO comparison of outputs can distinguish one
// call from two; a mutant that resolves twice passes this test, as it should. Resolving once
// is an efficiency and structure property of the source, asserted by review, not here.
func TestEncodeProjectsTheResolvedRequest(t *testing.T) {
	n := sampleNotification()
	resolved := siemfmt.OTLPRequestFor(siemfmt.Device{}, n)

	o := &Output{encoding: encodingProto, maxAttempts: defaultMaxAttempts}
	got, _, err := o.encode(n)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	data, err := siemfmt.OTLPLogsDataFrom(resolved)
	if err != nil {
		t.Fatalf("OTLPLogsDataFrom: %v", err)
	}
	want, err := proto.Marshal(&collogspb.ExportLogsServiceRequest{ResourceLogs: data.ResourceLogs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var a, b collogspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(got, &a); err != nil {
		t.Fatalf("unmarshal encoded: %v", err)
	}
	if err := proto.Unmarshal(want, &b); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	if !proto.Equal(&a, &b) {
		t.Errorf("encode does not project the single resolved request:\n got: %v\nwant: %v", &a, &b)
	}
}

// TestServiceVersionIsConfigurableAndSeparateFromTheDeviceHeader: adding
// Device.ServiceVersion without a way to SET it would have left a field that can never be
// populated in production — a half-built fix that reads as a complete one. This asserts the
// config key exists, is wired, and is independent of the device header revision, which an
// operator may set to a reseller's branding.
func TestServiceVersionIsConfigurableAndSeparateFromTheDeviceHeader(t *testing.T) {
	var declared bool
	for _, f := range New().Descriptor().ConfigFields {
		if f.Key == "service_version" {
			declared = true
			if strings.Contains(strings.ToLower(f.Description), "device") &&
				!strings.Contains(strings.ToLower(f.Description), "service.version") {
				t.Errorf("the service_version description talks about the device: %q", f.Description)
			}
		}
		if f.Key == "version" && !strings.Contains(f.Description, "NOT service.version") {
			t.Errorf("the version field does not say it is not service.version: %q", f.Description)
		}
	}
	if !declared {
		t.Fatal("service_version is not a declared config field, so Device.ServiceVersion can never be set")
	}

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": "https://collector:4318", "version": "9", "service_version": "1.4.2",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if o.device.ServiceVersion != "1.4.2" {
		t.Errorf("ServiceVersion = %q, want 1.4.2", o.device.ServiceVersion)
	}
	if o.device.Version != "9" {
		t.Errorf("device header Version = %q, want 9", o.device.Version)
	}

	// Unset, service.version must be ABSENT from the resource rather than filled from the
	// header — an absent attribute is honest, a wrong one is not.
	bare := New()
	if err := bare.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": "https://collector:4318", "version": "9",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	body, _, err := bare.encode(sampleNotification())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var req collogspb.ExportLogsServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range req.GetResourceLogs()[0].GetResource().GetAttributes() {
		if a.GetKey() == "service.version" {
			t.Errorf("service.version = %q was emitted with no configured service version",
				a.GetValue().GetStringValue())
		}
		if a.GetKey() == "ai.olivares.device.version" && a.GetValue().GetStringValue() != "9" {
			t.Errorf("device version = %q, want 9", a.GetValue().GetStringValue())
		}
	}
}
