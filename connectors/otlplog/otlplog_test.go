// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package otlplog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "governance.policy.denied",
		Title:    "policy denied tool call",
		Body:     "agent claude-1 blocked from write",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1", "decision": "deny"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

type capture struct {
	method      string
	path        string
	contentType string
	auth        string
	body        []byte
}

func newServer(t *testing.T, status int, respBody string, cap *capture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.contentType = r.Header.Get("Content-Type")
		cap.auth = r.Header.Get("Authorization")
		cap.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
}

func TestNotifyJSONRoundTrip(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK, `{"partialSuccess":{}}`, &cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "token": "sekret",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/v1/logs" {
		t.Errorf("path = %q, want /v1/logs", cap.path)
	}
	if cap.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", cap.contentType)
	}
	if cap.auth != "Bearer sekret" {
		t.Errorf("auth = %q, want Bearer sekret", cap.auth)
	}

	var req collogspb.ExportLogsServiceRequest
	if err := protojson.Unmarshal(cap.body, &req); err != nil {
		t.Fatalf("body is not a valid OTLP/JSON ExportLogsServiceRequest: %v", err)
	}
	rec := singleRecord(t, &req)
	if rec.GetSeverityNumber() != logspb.SeverityNumber_SEVERITY_NUMBER_ERROR {
		t.Errorf("severity = %v, want ERROR (high)", rec.GetSeverityNumber())
	}
	if rec.GetBody().GetStringValue() == "" {
		t.Errorf("log record has empty body")
	}
}

func TestNotifyProtobuf(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK, "", &cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL + "/v1/logs", "encoding": "protobuf",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if cap.contentType != "application/x-protobuf" {
		t.Errorf("content-type = %q, want application/x-protobuf", cap.contentType)
	}
	if cap.path != "/v1/logs" { // endpoint already had the path; must not double it
		t.Errorf("path = %q, want /v1/logs (no doubling)", cap.path)
	}
	var req collogspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(cap.body, &req); err != nil {
		t.Fatalf("body is not a valid OTLP/protobuf request: %v", err)
	}
	if _, err := getRecord(&req); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyPartialSuccessIsError(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK, `{"partialSuccess":{"rejectedLogRecords":"1","errorMessage":"bad attr"}}`, &cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": srv.URL}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("partial-success rejection must surface as an error")
	}
}

func TestNotifyJSONPartialSuccessWithUnknownField(t *testing.T) {
	// A collector/proxy that adds an unrecognized top-level field must NOT turn a
	// real rejection into a false success (DiscardUnknown).
	var cap capture
	srv := newServer(t, http.StatusOK,
		`{"partialSuccess":{"rejectedLogRecords":"1","errorMessage":"bad attr"},"x-proxy":"foo"}`, &cap)
	defer srv.Close()
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": srv.URL}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("partial-success rejection must surface even with an unknown response field")
	}
}

func TestNotifyProtobufPartialSuccessIsError(t *testing.T) {
	// Build a real protobuf ExportLogsServiceResponse reporting a rejection.
	resp := &collogspb.ExportLogsServiceResponse{
		PartialSuccess: &collogspb.ExportLogsPartialSuccess{RejectedLogRecords: 2, ErrorMessage: "schema"},
	}
	pb, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var cap capture
	srv := newServer(t, http.StatusOK, string(pb), &cap)
	defer srv.Close()
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "encoding": "protobuf",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("protobuf-mode partial-success rejection must surface as an error")
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	for i, cfg := range []map[string]string{
		{},                                    // missing endpoint
		{"endpoint": "x", "encoding": "yaml"}, // bad encoding
	} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

func singleRecord(t *testing.T, req *collogspb.ExportLogsServiceRequest) *logspb.LogRecord {
	t.Helper()
	rec, err := getRecord(req)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func getRecord(req *collogspb.ExportLogsServiceRequest) (*logspb.LogRecord, error) {
	rl := req.GetResourceLogs()
	if len(rl) == 0 || len(rl[0].GetScopeLogs()) == 0 || len(rl[0].GetScopeLogs()[0].GetLogRecords()) == 0 {
		return nil, fmt.Errorf("request has no log record")
	}
	return rl[0].GetScopeLogs()[0].GetLogRecords()[0], nil
}

// TestPartialSuccessIsTerminalNotRetryable is a specification-conformance repro.
//
// The OpenTelemetry specification states: "The client MUST NOT retry the request
// when it receives a partial success response where the partial_success is
// populated." Before this contract existed the rejection surfaced as a plain
// error, which the outbox reads as a transient failure and puts back on the retry
// ladder — 30s, 2m, 10m, 30m — re-sending records the collector had explicitly
// refused, and only then dead-lettering them. That is a MUST NOT, not an
// inefficiency.
func TestPartialSuccessIsTerminalNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"partialSuccess":{"rejectedLogRecords":"1","errorMessage":"bad record"}}`)
	}))
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": srv.URL}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("a populated partial_success must not be reported as a clean delivery")
	}
	report := sdk.ReportFor(err)
	if report.Outcome != sdk.OutcomeRejected {
		t.Fatalf("outcome = %s, want rejected: one notification is one record, so a populated rejection is a total refusal", report.Outcome)
	}
	if report.Outcome.Retryable() {
		t.Fatal("the OTLP specification says the client MUST NOT retry a populated partial success")
	}
	if report.Rejected != 1 {
		t.Fatalf("Rejected = %d, want the count the collector reported", report.Rejected)
	}
	if report.Locator != sdk.LocatorAggregateCount {
		t.Fatalf("Locator = %v: OTLP reports a count without identities, and claiming more precision would promise a selective resubmit we cannot perform", report.Locator)
	}
}

// TestOnlyTheSpecifiedStatusesAreRetried pins the retry set to the one the OTLP
// specification names. It lists 429, 502, 503 and 504, and says that for any other
// failure "the client MUST NOT retry sending the same telemetry data" — so an
// unlisted 5xx is not a transient condition to back off from just because it looks
// like a server problem. Treating it as one is how a permanently misconfigured
// collector keeps receiving the same rejected batch.
func TestOnlyTheSpecifiedStatusesAreRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   sdk.DeliveryOutcome
	}{
		{429, sdk.OutcomeUnavailable},
		{502, sdk.OutcomeUnavailable},
		{503, sdk.OutcomeUnavailable},
		{504, sdk.OutcomeUnavailable},
		{400, sdk.OutcomeRejected},
		{404, sdk.OutcomeRejected},
		{500, sdk.OutcomeRejected}, // NOT in the specification's retryable set
		{501, sdk.OutcomeRejected},
		{0, sdk.OutcomeIndeterminate}, // no response ever arrived: a transport fault
	} {
		got := classifyOTLPStatus(tc.status)
		if got != tc.want {
			t.Fatalf("status %d classified %s, want %s", tc.status, got, tc.want)
		}
		if tc.want == sdk.OutcomeRejected && got.Retryable() {
			t.Fatalf("status %d must not be retryable: the specification forbids re-sending the same data", tc.status)
		}
	}
}

// TestEmptyBodyIsAcceptanceOnlyForProtobuf. A zero-byte body is the valid protobuf
// serialization of an ExportLogsServiceResponse with no fields set, so protobuf must
// keep reading it as a clean acceptance. JSON has no such document — the success
// response is an object and "{}" is how a collector spells the empty one — so an
// empty JSON body means something that is not the collector answered.
//
// The exemption used to be tested BEFORE the encoding was consulted, which extended
// the protobuf rule to JSON silently.
func TestEmptyBodyIsAcceptanceOnlyForProtobuf(t *testing.T) {
	for _, tc := range []struct {
		name, encoding, body string
		wantErr              bool
	}{
		{name: "protobuf empty is the default message", encoding: "protobuf", body: "", wantErr: false},
		{name: "json empty is no document at all", encoding: "json", body: "", wantErr: true},
		{name: "json whitespace is no document at all", encoding: "json", body: "  \n\t ", wantErr: true},
		{name: "json {} is the empty response", encoding: "json", body: "{}", wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cap capture
			srv := newServer(t, http.StatusOK, tc.body, &cap)
			defer srv.Close()

			o := New()
			if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
				"endpoint": srv.URL + "/v1/logs", "encoding": tc.encoding,
			}}); err != nil {
				t.Fatal(err)
			}
			defer o.Close(context.Background())

			err := o.Notify(context.Background(), sampleNotification())
			if tc.wantErr && err == nil {
				t.Fatal("an HTTP success with no response document was read as a delivery")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("a valid empty response was refused: %v", err)
			}
		})
	}
}
