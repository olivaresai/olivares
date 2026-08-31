// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package splunkhec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "secret write blocked",
		Body:     "claude-1 denied",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

func TestNotifyJSONEnvelope(t *testing.T) {
	var path, auth, ctype string
	var env hecEnvelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth, ctype = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &env)
		_, _ = io.WriteString(w, `{"text":"Success","code":0}`)
	}))
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "token": "TOK", "index": "main", "host": "h1", "source": "olivares",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if path != pathEvent {
		t.Errorf("path = %q, want %q", path, pathEvent)
	}
	if auth != "Splunk TOK" {
		t.Errorf("auth = %q, want 'Splunk TOK'", auth)
	}
	if ctype != "application/json" {
		t.Errorf("content-type = %q", ctype)
	}
	if env.Sourcetype != "olivares" || env.Index != "main" || env.Host != "h1" || env.Source != "olivares" {
		t.Errorf("envelope metadata wrong: %+v", env)
	}
	if env.Time == 0 {
		t.Errorf("envelope time not set")
	}
	// The event must be the canonical notification object.
	var ev map[string]any
	if err := json.Unmarshal(env.Event, &ev); err != nil {
		t.Fatalf("event not a JSON object: %v", err)
	}
	if ev["severity"] != "high" || ev["title"] != "secret write blocked" {
		t.Errorf("event payload wrong: %v", ev)
	}
}

func TestNotifyCodeNonZeroIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"text":"Incorrect index","code":7}`)
	}))
	defer srv.Close()
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": srv.URL, "token": "T"}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	err := o.Notify(context.Background(), sampleNotification())
	if err == nil || !strings.Contains(err.Error(), "code 7") {
		t.Fatalf("want code 7 error, got %v", err)
	}
}

func TestNotifyRawTextFormat(t *testing.T) {
	var path, ctype, sourcetype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ctype = r.URL.Path, r.Header.Get("Content-Type")
		sourcetype = r.URL.Query().Get("sourcetype")
		b, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(string(b), "CEF:0|") {
			t.Errorf("raw body not CEF: %q", string(b))
		}
		_, _ = io.WriteString(w, `{"text":"Success","code":0}`)
	}))
	defer srv.Close()
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "token": "T", "format": "cef",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if path != pathRaw {
		t.Errorf("path = %q, want %q", path, pathRaw)
	}
	if ctype != "text/plain" {
		t.Errorf("content-type = %q, want text/plain", ctype)
	}
	if sourcetype != "olivares" {
		t.Errorf("sourcetype query = %q", sourcetype)
	}
}

func TestNotifyWithIndexerAck(t *testing.T) {
	var submitChannel, ackChannel string
	var polls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathEvent:
			submitChannel = r.Header.Get("X-Splunk-Request-Channel")
			_, _ = io.WriteString(w, `{"text":"Success","code":0,"ackID":42}`)
		case pathAck:
			ackChannel = r.Header.Get("X-Splunk-Request-Channel")
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), `"acks":[42]`) {
				t.Errorf("ack request body wrong: %q", string(b))
			}
			// Not confirmed on the first poll, confirmed on the second.
			if atomic.AddInt32(&polls, 1) >= 2 {
				_, _ = io.WriteString(w, `{"acks":{"42":true}}`)
			} else {
				_, _ = io.WriteString(w, `{"acks":{"42":false}}`)
			}
		}
	}))
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "token": "T", "use_ack": "true",
		"ack_poll_interval": "1ms", "ack_timeout": "5s",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify with ack: %v", err)
	}
	if submitChannel == "" {
		t.Error("submit did not send X-Splunk-Request-Channel")
	}
	if submitChannel != ackChannel {
		t.Errorf("ack channel %q != submit channel %q", ackChannel, submitChannel)
	}
	if atomic.LoadInt32(&polls) < 2 {
		t.Errorf("expected to poll until confirmed, polled %d", polls)
	}
}

func TestNotifyAckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathEvent:
			_, _ = io.WriteString(w, `{"text":"Success","code":0,"ackID":7}`)
		case pathAck:
			_, _ = io.WriteString(w, `{"acks":{"7":false}}`) // never confirmed
		}
	}))
	defer srv.Close()
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "token": "T", "use_ack": "true",
		"ack_poll_interval": "1ms", "ack_timeout": "30ms",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("an unconfirmed ack must time out as an error")
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	for i, cfg := range []map[string]string{
		{"token": "T"},    // missing endpoint
		{"endpoint": "x"}, // missing token
		{"endpoint": "x", "token": "T", "format": "x"}, // bad format
	} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

// TestNotifyRefusesAnUnreadableRejection is the repro for a delivery that used to
// be reported as SUCCESS while Splunk had rejected it.
//
// The shared client reads only a bounded excerpt of the response. When HEC's
// rejection document is larger than that budget — which a real one is, because
// HEC echoes back the offending event and Splunk's own troubleshooting guidance
// is to raise limits rather than shrink payloads — the excerpt is not valid JSON,
// parseHECResponse concludes "not a HEC status document; the 2xx stands", and the
// engine records the notification as delivered. The rejection disappears.
//
// The fix does not guess at the missing bytes: an answer we could not read whole
// is refused, because "we could not tell" and "it was accepted" must not be the
// same outcome in an evidence pipeline.
func TestNotifyRefusesAnUnreadableRejection(t *testing.T) {
	// A genuine HEC rejection whose echoed event pushes it past the excerpt budget.
	padding := strings.Repeat("A", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"text":"Incorrect index","code":7,"invalid-event-number":0,"echo":"`+padding+`"}`)
	}))
	defer srv.Close()
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": srv.URL, "token": "T"}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("a rejection too large to read was reported as delivered; the engine would record Splunk's refusal as a success")
	}
	if !errors.Is(err, errIncompleteResponse) {
		t.Fatalf("the refusal must name the unreadable body, got: %v", err)
	}
}

// TestHECCapacityWarningIsAnAcceptance is the repro for a duplication bug.
//
// Splunk codes 24 and 25 arrive under HTTP 200 and mean the event WAS indexed;
// they warn that a queue is filling. Treating "code != 0" as a rejection made the
// engine report a failure, which the outbox retries — re-sending an event Splunk
// already holds and duplicating it in the operator's index. Duplicated evidence
// is not a cosmetic problem: it inflates every count drawn from that index.
func TestHECCapacityWarningIsAnAcceptance(t *testing.T) {
	for _, code := range []int{24, 25} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"text":"HEC queue is approaching its capacity limit","code":`+strconv.Itoa(code)+`}`)
		}))
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": srv.URL, "token": "T"}}); err != nil {
			t.Fatal(err)
		}
		if err := o.Notify(context.Background(), sampleNotification()); err != nil {
			t.Fatalf("code %d is a capacity warning over an ACCEPTED event; reporting it as a failure makes the engine retry and duplicate: %v", code, err)
		}
		_ = o.Close(context.Background())
		srv.Close()
	}
}

// TestHECCodesAreClassifiedNotLumped pins the rest of the table. The distinction
// that matters operationally is transient versus deterministic: a busy indexer
// must be retried, and a wrong index must not be — retrying the latter burns the
// whole ladder and then dead-letters anyway, having re-sent bytes Splunk refused.
func TestHECCodesAreClassifiedNotLumped(t *testing.T) {
	for _, tc := range []struct {
		code int
		want sdk.DeliveryOutcome
	}{
		{0, sdk.OutcomeDelivered},
		{7, sdk.OutcomeRejected},     // Incorrect index
		{6, sdk.OutcomeRejected},     // Invalid data format
		{9, sdk.OutcomeUnavailable},  // Server is busy
		{26, sdk.OutcomeUnavailable}, // queue at capacity (429)
		{24, sdk.OutcomeDeliveredWithWarning},
		{999, sdk.OutcomeIndeterminate}, // a code Splunk has not published
	} {
		if got := ClassifyHECCode(tc.code); got != tc.want {
			t.Fatalf("code %d classified %s, want %s", tc.code, got, tc.want)
		}
	}
	// The retry policy follows from the classification, and this is the property the
	// outbox depends on: only a transient condition may be retried.
	if !ClassifyHECCode(9).Retryable() {
		t.Fatal("a busy indexer must be retryable")
	}
	if ClassifyHECCode(7).Retryable() {
		t.Fatal("a wrong index can never succeed on a retry of the same bytes")
	}
	if ClassifyHECCode(24).Retryable() {
		t.Fatal("retrying an accepted-with-warning event duplicates it")
	}
}

// TestAckIDDecodesInBothShapes guards a failure that was worse than the missing
// ack it looks like. The field was typed *int64, so a quoted value made the WHOLE
// response fail to unmarshal — which sent the caller into "not a HEC status
// document, the 2xx stands" and reported a delivery nobody had confirmed. An
// optional field must never be able to void the verdict of the fields that are not.
func TestAckIDDecodesInBothShapes(t *testing.T) {
	for _, body := range []string{
		`{"text":"Success","code":0,"ackID":42}`,
		`{"text":"Success","code":0,"ackID":"42"}`,
		`{"text":"Success","code":0,"ackId":42}`,
		`{"text":"Success","code":0,"ackId":"42"}`,
	} {
		resp, err := parseHECResponse(body)
		if err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if resp.code() != 0 {
			t.Fatalf("body %s: the code must still decode, got %d", body, resp.code())
		}
		id := resp.ackID()
		if id == nil || *id != 42 {
			t.Fatalf("body %s: ackID = %v, want 42", body, id)
		}
	}
	// And a code still decodes even when the ack value is a shape we cannot use, so
	// an unusable ack degrades to "no ack" instead of erasing the status.
	resp, err := parseHECResponse(`{"text":"Success","code":0,"ackID":"not-a-number"}`)
	if err != nil {
		t.Fatalf("an unusable ack must not void the response: %v", err)
	}
	if resp.code() != 0 {
		t.Fatalf("code = %d, want the status to survive an unusable ack", resp.code())
	}
	if resp.ackID() != nil {
		t.Fatal("an unparseable ack must read as absent, not as a fabricated id")
	}
}

// TestHECCodesArriveWithTheirRealHTTPStatus is the repro for a classification that
// was almost entirely unreachable.
//
// Splunk sends most of its codes with a NON-2xx status: code 7 "Incorrect index"
// is HTTP 400, code 9 "Server is busy" is 503. The shared client returns an error
// for any non-2xx, and the connector used to return that error unclassified — so
// only the codes riding a 200 (0, 17, 24, 25) ever reached the table. Every real
// refusal fell to the engine's default, which retries: a wrong index burned the
// whole ladder before dead-lettering, re-sending bytes Splunk had refused.
//
// The earlier test hid this by answering 200 to a code 7, a combination Splunk
// does not produce.
func TestHECCodesArriveWithTheirRealHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   sdk.DeliveryOutcome
	}{
		{"incorrect index is 400 and terminal", 400, `{"text":"Incorrect index","code":7}`, sdk.OutcomeRejected},
		{"invalid data format is 400 and terminal", 400, `{"text":"Invalid data format","code":6}`, sdk.OutcomeRejected},
		{"server busy is 503 and transient", 503, `{"text":"Server is busy","code":9}`, sdk.OutcomeUnavailable},
		{"queue at capacity is 429 and transient", 429, `{"text":"HEC queue is at capacity","code":26}`, sdk.OutcomeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			o := New()
			if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
				"endpoint": srv.URL, "token": "T", "max_attempts": "1",
			}}); err != nil {
				t.Fatal(err)
			}
			defer o.Close(context.Background())

			err := o.Notify(context.Background(), sampleNotification())
			if err == nil {
				t.Fatal("a non-2xx refusal must not report success")
			}
			report := sdk.ReportFor(err)
			if report.Outcome != tc.want {
				t.Fatalf("HTTP %d code %s classified %s, want %s — an unclassified refusal falls to the engine default and is retried",
					tc.status, tc.body, report.Outcome, tc.want)
			}
			if report.Code == 0 {
				t.Fatal("the report must carry Splunk's own code so an operator can see which refusal this was")
			}
		})
	}
}

// TestAnAckOnlyResponseIsWellFormed guards a regression the well-formedness check
// introduced. Splunk documents the acknowledgement-enabled submit response as
// carrying an ackID; demanding text AND code rejected that legitimate answer
// before the ack could even be polled, turning a working ack-mode destination into
// one whose deliveries could never be confirmed.
func TestAnAckOnlyResponseIsWellFormed(t *testing.T) {
	for _, body := range []string{
		`{"ackID":"2"}`,
		`{"ackId":2}`,
		`{"text":"Success","code":0,"ackID":2}`,
	} {
		resp, err := parseHECResponse(body)
		if err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if !resp.wellFormed {
			t.Fatalf("body %s is a documented HEC answer and must be accepted as one", body)
		}
	}
	// What must still be refused: a document that parses but says nothing HEC says.
	for _, body := range []string{`{}`, `{"error":"gateway timeout"}`, `{"status":"ok"}`} {
		resp, _ := parseHECResponse(body)
		if resp.wellFormed {
			t.Fatalf("body %s carries no HEC member and must not be read as a verdict", body)
		}
	}
}
