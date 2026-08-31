// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pagerduty

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const testRoutingKey = "R0UT1NGK3Yverysecret000000000000"

// recordingDoer captures every request and its decoded body, and replays a
// scripted response. It lets tests assert the exact JSON shape the connector
// sends and prove the routing key never escapes via a log/error path.
type recordingDoer struct {
	status   int
	bodyFile string      // testdata file to use as the response body, or empty
	rawBody  string      // literal response body (wins over bodyFile if set)
	header   http.Header // optional response headers (e.g. Retry-After)
	reqs     []*http.Request
	bodies   [][]byte
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	var raw []byte
	if req.Body != nil {
		raw, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	d.reqs = append(d.reqs, req)
	d.bodies = append(d.bodies, raw)

	body := d.rawBody
	if body == "" && d.bodyFile != "" {
		b, err := os.ReadFile(filepath.Join("testdata", d.bodyFile))
		if err != nil {
			panic(err)
		}
		body = string(b)
	}
	h := d.header
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{StatusCode: d.status, Body: io.NopCloser(strings.NewReader(body)), Header: h}, nil
}

func (d *recordingDoer) lastEvent(t *testing.T) event {
	t.Helper()
	if len(d.bodies) == 0 {
		t.Fatal("no request was sent")
	}
	var e event
	if err := json.Unmarshal(d.bodies[len(d.bodies)-1], &e); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	return e
}

// noWait removes real backoff sleeping so retry tests run instantly.
func noWait(context.Context, time.Duration) error { return nil }

// openWith builds an opened connector wired to the given doer with no real sleep.
func openWith(t *testing.T, doer delivery.Doer) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	cfg := sdk.Config{Settings: map[string]string{
		"routing_key": testRoutingKey,
		"events_url":  "https://events.pagerduty.example/v2/enqueue",
		"source":      "olivares-test",
	}}
	if err := o.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Disable real waiting by rebuilding the delivery client with a no-op Sleep.
	o.client = delivery.New(doer, delivery.Options{MaxAttempts: o.maxAttempts, Sleep: noWait})
	return o
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeOutput {
		t.Fatalf("descriptor = %+v", d)
	}
	if d.APIVersion != sdk.APIVersion || d.Version != "0.1.0" {
		t.Fatalf("descriptor version/apiversion = %q/%q", d.Version, d.APIVersion)
	}
	var secretRequired bool
	for _, f := range d.ConfigFields {
		if f.Key == "routing_key" {
			if !f.Secret || !f.Required {
				t.Fatalf("routing_key must be Secret+Required, got %+v", f)
			}
			secretRequired = true
		}
	}
	if !secretRequired {
		t.Fatal("routing_key config field missing")
	}
}

func TestOpen_RequiresRoutingKey(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err == nil {
		t.Fatal("Open without routing_key should fail")
	}
	if strings.Contains(err.Error(), testRoutingKey) {
		t.Fatal("error must not echo the routing key")
	}
}

func TestNotify_PayloadShapeAndAccepted(t *testing.T) {
	doer := &recordingDoer{status: http.StatusAccepted, bodyFile: "accepted.json"}
	o := openWith(t, doer)

	n := sdk.Notification{
		Type:     "finding.reported",
		Title:    "CPU saturated on srv-01",
		Body:     "host srv-01 sustained 98% CPU for 10m",
		Severity: model.SeverityHigh,
		Tenant:   "tenant-acme",
		Fields:   map[string]string{"host": "srv-01", "link": "https://console.example/findings/42"},
		Time:     time.Date(2026, 6, 3, 9, 30, 0, 0, time.UTC),
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if len(doer.reqs) != 1 {
		t.Fatalf("attempts = %d, want 1 (202 is success, no retry)", len(doer.reqs))
	}
	req := doer.reqs[0]
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if req.URL.String() != "https://events.pagerduty.example/v2/enqueue" {
		t.Fatalf("url = %s", req.URL.String())
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}

	e := doer.lastEvent(t)
	if e.RoutingKey != testRoutingKey {
		t.Fatalf("routing_key not sent in body, got %q", e.RoutingKey)
	}
	if e.EventAction != "trigger" {
		t.Fatalf("event_action = %q, want trigger", e.EventAction)
	}
	if e.Payload.Summary != "CPU saturated on srv-01" {
		t.Fatalf("summary = %q", e.Payload.Summary)
	}
	if e.Payload.Source != "olivares-test" {
		t.Fatalf("source = %q", e.Payload.Source)
	}
	if e.Payload.Severity != pdError {
		t.Fatalf("severity = %q, want %q for High", e.Payload.Severity, pdError)
	}
	if e.Payload.Timestamp != "2026-06-03T09:30:00Z" {
		t.Fatalf("timestamp = %q", e.Payload.Timestamp)
	}
	if e.Payload.CustomDetails["tenant"] != "tenant-acme" {
		t.Fatalf("custom_details.tenant = %q", e.Payload.CustomDetails["tenant"])
	}
	if e.Payload.CustomDetails["body"] != n.Body {
		t.Fatalf("custom_details.body = %q", e.Payload.CustomDetails["body"])
	}
	if e.Payload.CustomDetails["host"] != "srv-01" {
		t.Fatalf("custom_details.host = %q", e.Payload.CustomDetails["host"])
	}
	if e.Payload.CustomDetails["link"] == "" {
		t.Fatalf("custom_details.link missing")
	}
	// No explicit dedup_key supplied → a stable derived hash is present.
	if e.DedupKey == "" || len(e.DedupKey) != 64 {
		t.Fatalf("derived dedup_key = %q (want 64-hex sha256)", e.DedupKey)
	}
}

func TestNotify_SeverityMapping(t *testing.T) {
	cases := []struct {
		in   model.Severity
		want string
	}{
		{model.SeverityCritical, pdCritical},
		{model.SeverityHigh, pdError},
		{model.SeverityMedium, pdWarning},
		{model.SeverityLow, pdInfo},
		{model.SeverityInfo, pdInfo},
		{model.Severity(""), pdInfo},
		{model.Severity("bogus"), pdInfo},
	}
	for _, c := range cases {
		doer := &recordingDoer{status: http.StatusAccepted, bodyFile: "accepted.json"}
		o := openWith(t, doer)
		n := sdk.Notification{Type: "t", Title: "x", Severity: c.in}
		if err := o.Notify(context.Background(), n); err != nil {
			t.Fatalf("Notify(%s): %v", c.in, err)
		}
		if got := doer.lastEvent(t).Payload.Severity; got != c.want {
			t.Fatalf("severity %q mapped to %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNotify_SummaryFallbackAndTruncation(t *testing.T) {
	// Empty Title falls back to Body.
	doer := &recordingDoer{status: http.StatusAccepted, bodyFile: "accepted.json"}
	o := openWith(t, doer)
	if err := o.Notify(context.Background(), sdk.Notification{Type: "t", Body: "body becomes summary"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := doer.lastEvent(t).Payload.Summary; got != "body becomes summary" {
		t.Fatalf("fallback summary = %q", got)
	}

	// Wholly empty notification yields a stable placeholder, never empty.
	doer2 := &recordingDoer{status: http.StatusAccepted, bodyFile: "accepted.json"}
	o2 := openWith(t, doer2)
	if err := o2.Notify(context.Background(), sdk.Notification{}); err != nil {
		t.Fatalf("Notify(empty): %v", err)
	}
	if got := doer2.lastEvent(t).Payload.Summary; got == "" {
		t.Fatalf("empty notification produced empty summary")
	}

	// Over-length title is truncated to 1024 runes (PagerDuty's cap).
	doer3 := &recordingDoer{status: http.StatusAccepted, bodyFile: "accepted.json"}
	o3 := openWith(t, doer3)
	long := strings.Repeat("á", 2000) // multi-byte, 2000 runes
	if err := o3.Notify(context.Background(), sdk.Notification{Type: "t", Title: long}); err != nil {
		t.Fatalf("Notify(long): %v", err)
	}
	gotSummary := doer3.lastEvent(t).Payload.Summary
	if n := len([]rune(gotSummary)); n != maxSummaryLen {
		t.Fatalf("truncated summary = %d runes, want %d", n, maxSummaryLen)
	}
}

func TestNotify_DedupKeyBehavior(t *testing.T) {
	// Explicit dedup_key is used verbatim and not duplicated into custom_details.
	doer := &recordingDoer{status: http.StatusAccepted, bodyFile: "accepted.json"}
	o := openWith(t, doer)
	n := sdk.Notification{
		Type:   "finding.reported",
		Title:  "x",
		Fields: map[string]string{"dedup_key": "stable-incident-7", "k": "v"},
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	e := doer.lastEvent(t)
	if e.DedupKey != "stable-incident-7" {
		t.Fatalf("explicit dedup_key = %q", e.DedupKey)
	}
	if _, dup := e.Payload.CustomDetails["dedup_key"]; dup {
		t.Fatal("dedup_key leaked into custom_details")
	}
	if e.Payload.CustomDetails["k"] != "v" {
		t.Fatal("other field dropped")
	}

	// Two identical (type+title) notifications derive the same key; a different
	// title derives a different key.
	keyOf := func(n sdk.Notification) string {
		d := &recordingDoer{status: http.StatusAccepted, bodyFile: "accepted.json"}
		oo := openWith(t, d)
		if err := oo.Notify(context.Background(), n); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		return d.lastEvent(t).DedupKey
	}
	a := keyOf(sdk.Notification{Type: "finding.reported", Title: "same"})
	b := keyOf(sdk.Notification{Type: "finding.reported", Title: "same"})
	c := keyOf(sdk.Notification{Type: "finding.reported", Title: "different"})
	if a != b {
		t.Fatalf("identical notifications produced different dedup keys: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("distinct titles produced the same dedup key")
	}
}

func TestNotify_BadRequestIsTerminalNoRetry(t *testing.T) {
	doer := &recordingDoer{status: http.StatusBadRequest, bodyFile: "bad_request.json"}
	o := openWith(t, doer)
	err := o.Notify(context.Background(), sdk.Notification{Type: "t", Title: "x", Severity: model.SeverityCritical})
	if err == nil {
		t.Fatal("400 should surface as an error")
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("400 retried: %d attempts, want 1", len(doer.reqs))
	}
	assertNoRoutingKeyLeak(t, err)
}

func TestNotify_TransientThenSuccess(t *testing.T) {
	// First a 503 (retryable), then a 202: delivery should retry and succeed.
	doer := &sequenceDoer{steps: []step{
		{status: http.StatusServiceUnavailable, body: "upstream down"},
		{status: http.StatusAccepted, body: `{"status":"success","dedup_key":"k"}`},
	}}
	o := New()
	o.doer = doer
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"routing_key": testRoutingKey, "events_url": "https://events.pagerduty.example/v2/enqueue",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	o.client = delivery.New(doer, delivery.Options{MaxAttempts: 4, Sleep: noWait})

	if err := o.Notify(context.Background(), sdk.Notification{Type: "t", Title: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if doer.calls != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry)", doer.calls)
	}
}

func TestNotify_LogicalFailureAt2xx(t *testing.T) {
	// Defensive: a 2xx whose body reports a non-success status surfaces as an error.
	doer := &recordingDoer{status: http.StatusOK, rawBody: `{"status":"invalid event","message":"missing payload"}`}
	o := openWith(t, doer)
	err := o.Notify(context.Background(), sdk.Notification{Type: "t", Title: "x"})
	if err == nil {
		t.Fatal("logical failure body at 2xx should surface as an error")
	}
	if !strings.Contains(err.Error(), "missing payload") {
		t.Fatalf("error should carry the API message: %v", err)
	}
	assertNoRoutingKeyLeak(t, err)
}

func TestNotify_2xxEmptyBodyIsSuccess(t *testing.T) {
	// A 2xx with no body (e.g. a proxy stripped it) is accepted, not an error. The
	// success test must hold for ANY 2xx, not just PagerDuty's documented 202: a 200
	// with an empty body (a gateway that normalized the status) is also success.
	cases := []struct {
		name   string
		status int
	}{
		{"202 accepted", http.StatusAccepted},
		{"200 ok", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doer := &recordingDoer{status: c.status, rawBody: ""}
			o := openWith(t, doer)
			if err := o.Notify(context.Background(), sdk.Notification{Type: "t", Title: "x"}); err != nil {
				t.Fatalf("empty %d body should be success: %v", c.status, err)
			}
		})
	}
}

func TestNotify_NoRoutingKeyInErrorOrResponsePath(t *testing.T) {
	// Even a verbose terminal error must never echo the secret routing key.
	doer := &recordingDoer{status: http.StatusUnprocessableEntity, rawBody: `{"status":"invalid event","message":"bad","errors":["x"]}`}
	o := openWith(t, doer)
	err := o.Notify(context.Background(), sdk.Notification{Type: "t", Title: "x"})
	if err == nil {
		t.Fatal("422 should be terminal error")
	}
	assertNoRoutingKeyLeak(t, err)

	// And the key only ever appears in the request body, never the URL or headers.
	req := doer.reqs[0]
	if strings.Contains(req.URL.String(), testRoutingKey) {
		t.Fatal("routing key leaked into URL")
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			if strings.Contains(v, testRoutingKey) {
				t.Fatalf("routing key leaked into header %s", k)
			}
		}
	}
}

func assertNoRoutingKeyLeak(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), testRoutingKey) {
		t.Fatalf("routing key leaked in error: %v", err)
	}
}

// sequenceDoer returns a scripted sequence of responses, one per call.
type sequenceDoer struct {
	steps []step
	calls int
}

type step struct {
	status int
	body   string
}

func (d *sequenceDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	i := d.calls
	if i >= len(d.steps) {
		i = len(d.steps) - 1
	}
	d.calls++
	s := d.steps[i]
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(bytes.NewReader([]byte(s.body))), Header: make(http.Header)}, nil
}
