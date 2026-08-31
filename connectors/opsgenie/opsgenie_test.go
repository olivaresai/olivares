// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opsgenie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"time"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// newNoWaitClient builds a delivery client whose backoff never actually waits,
// so a retry path is exercised instantly.
func newNoWaitClient(doer delivery.Doer, attempts int) *delivery.Client {
	return delivery.New(doer, delivery.Options{
		MaxAttempts: attempts,
		Sleep:       func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
}

// recordingDoer captures every request it sees and returns queued responses in
// order, so tests assert the auth header, the marshaled payload and the
// retry/terminal behavior without a live network call. It also concatenates
// every header value it ever observed so a test can prove the API key never
// leaks anywhere unexpected.
type recordingDoer struct {
	responses  []stubResp
	calls      int
	reqs       []*http.Request
	bodies     []string
	allHeaders strings.Builder
}

type stubResp struct {
	status int
	body   string
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	var bodyStr string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyStr = string(b)
	}
	d.bodies = append(d.bodies, bodyStr)
	for k, vs := range req.Header {
		for _, v := range vs {
			d.allHeaders.WriteString(k + ":" + v + "\n")
		}
	}

	i := d.calls
	d.calls++
	if i >= len(d.responses) {
		i = len(d.responses) - 1
	}
	r := d.responses[i]
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     make(http.Header),
	}, nil
}

const testKey = "sk-opsgenie-secret-key-123456"

// openOutput builds an Output wired to a recordingDoer with the given config
// overrides applied on top of a valid baseline.
func openOutput(t *testing.T, doer *recordingDoer, overrides map[string]string) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	settings := map[string]string{
		"api_key":      testKey,
		"alerts_url":   "https://opsgenie.test/v2/alerts",
		"max_attempts": "4",
	}
	for k, v := range overrides {
		if v == "" {
			delete(settings, k)
			continue
		}
		settings[k] = v
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeOutput {
		t.Fatalf("descriptor = %+v", d)
	}
	if d.APIVersion != sdk.APIVersion || d.Version != "0.1.0" {
		t.Fatalf("descriptor version/api = %q/%q", d.Version, d.APIVersion)
	}
	var sawSecretRequired bool
	for _, f := range d.ConfigFields {
		if f.Key == "api_key" {
			if !f.Secret || !f.Required {
				t.Fatalf("api_key field must be Secret+Required: %+v", f)
			}
			sawSecretRequired = true
		}
	}
	if !sawSecretRequired {
		t.Fatal("api_key must be a declared, secret, required config field")
	}
}

func TestOpen_RequiresAPIKey(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err == nil {
		t.Fatal("Open without api_key should fail")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("error should never echo the (absent) key")
	}
}

func TestOpen_InvalidRegion(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"api_key": testKey,
		"region":  "apac",
	}})
	if err == nil {
		t.Fatal("invalid region should fail Open")
	}
}

func TestRegionSelectsDefaultURL(t *testing.T) {
	cases := []struct {
		region string
		want   string
	}{
		{"", usAlertsURL}, // default region us
		{"us", usAlertsURL},
		{"eu", euAlertsURL},
		{"EU", euAlertsURL}, // case-insensitive
	}
	for _, c := range cases {
		o := New()
		settings := map[string]string{"api_key": testKey}
		if c.region != "" {
			settings["region"] = c.region
		}
		if err := o.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
			t.Fatalf("region %q: Open: %v", c.region, err)
		}
		if o.alertsURL != c.want {
			t.Fatalf("region %q: alertsURL = %q, want %q", c.region, o.alertsURL, c.want)
		}
	}
}

func TestAlertsURLOverrideWins(t *testing.T) {
	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"api_key":    testKey,
		"region":     "eu",
		"alerts_url": "https://internal.proxy/og/alerts",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if o.alertsURL != "https://internal.proxy/og/alerts" {
		t.Fatalf("override ignored: alertsURL = %q", o.alertsURL)
	}
}

func TestNotify_SuccessMappingAndAuthHeader(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202, body: `{"requestId":"abc"}`}}}
	o := openOutput(t, doer, nil)

	n := sdk.Notification{
		Type:     "finding.reported",
		Title:    "Public S3 bucket detected",
		Body:     "Bucket acme-logs is world-readable in us-east-1.",
		Severity: model.SeverityHigh,
		Tenant:   "tenant-42",
		Fields: map[string]string{
			"alias":       "s3-acme-logs-public",
			"resourceRef": "arn:aws:s3:::acme-logs",
		},
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1", doer.calls)
	}

	req := doer.reqs[0]
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if got := req.Header.Get("Authorization"); got != "GenieKey "+testKey {
		t.Fatalf("Authorization = %q, want GenieKey + key", got)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}

	var p alertPayload
	if err := json.Unmarshal([]byte(doer.bodies[0]), &p); err != nil {
		t.Fatalf("payload not valid JSON: %v\n%s", err, doer.bodies[0])
	}
	if p.Message != "Public S3 bucket detected" {
		t.Fatalf("message = %q", p.Message)
	}
	if p.Description != n.Body {
		t.Fatalf("description = %q", p.Description)
	}
	if p.Priority != "P2" {
		t.Fatalf("priority = %q, want P2 for high", p.Priority)
	}
	if p.Alias != "s3-acme-logs-public" {
		t.Fatalf("alias = %q", p.Alias)
	}
	if p.Source != alertSource {
		t.Fatalf("source = %q, want %q", p.Source, alertSource)
	}
	// Details merge Fields + tenant + eventType.
	if p.Details["tenant"] != "tenant-42" {
		t.Fatalf("details.tenant = %q", p.Details["tenant"])
	}
	if p.Details["eventType"] != "finding.reported" {
		t.Fatalf("details.eventType = %q", p.Details["eventType"])
	}
	if p.Details["resourceRef"] != "arn:aws:s3:::acme-logs" {
		t.Fatalf("details.resourceRef = %q", p.Details["resourceRef"])
	}
	if p.Details["alias"] != "s3-acme-logs-public" {
		t.Fatalf("details should still carry the alias field: %q", p.Details["alias"])
	}
}

func TestNotify_PriorityMapping(t *testing.T) {
	cases := []struct {
		sev  model.Severity
		want string
	}{
		{model.SeverityCritical, "P1"},
		{model.SeverityHigh, "P2"},
		{model.SeverityMedium, "P3"},
		{model.SeverityLow, "P4"},
		{model.SeverityInfo, "P5"},
		{model.Severity(""), "P5"}, // unknown/empty falls to P5
		{model.Severity("bogus"), "P5"},
	}
	for _, c := range cases {
		doer := &recordingDoer{responses: []stubResp{{status: 202}}}
		o := openOutput(t, doer, nil)
		if err := o.Notify(context.Background(), sdk.Notification{Title: "x", Severity: c.sev}); err != nil {
			t.Fatalf("sev %q: Notify: %v", c.sev, err)
		}
		var p alertPayload
		if err := json.Unmarshal([]byte(doer.bodies[0]), &p); err != nil {
			t.Fatalf("sev %q: bad json: %v", c.sev, err)
		}
		if p.Priority != c.want {
			t.Fatalf("sev %q: priority = %q, want %q", c.sev, p.Priority, c.want)
		}
	}
}

func TestNotify_MessageFallbackToBody(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, nil)
	if err := o.Notify(context.Background(), sdk.Notification{
		Body:     "No title, only a body line.",
		Severity: model.SeverityMedium,
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var p alertPayload
	if err := json.Unmarshal([]byte(doer.bodies[0]), &p); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if p.Message != "No title, only a body line." {
		t.Fatalf("message should fall back to body, got %q", p.Message)
	}
}

func TestNotify_MessageTruncatedTo130(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, nil)

	long := strings.Repeat("A", 200)
	if err := o.Notify(context.Background(), sdk.Notification{Title: long, Severity: model.SeverityLow}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var p alertPayload
	if err := json.Unmarshal([]byte(doer.bodies[0]), &p); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(p.Message) != maxMessageLen {
		t.Fatalf("message len = %d, want %d", len(p.Message), maxMessageLen)
	}
}

func TestTruncate_RuneBoundary(t *testing.T) {
	// A string whose byte at the limit lands mid-rune must be trimmed back to a
	// rune boundary, never split into invalid UTF-8.
	s := strings.Repeat("é", 100) // each 'é' is 2 bytes
	got := truncate(s, 5)         // 5 bytes -> trim back to 4 (two full runes)
	if len(got) != 4 {
		t.Fatalf("truncate len = %d, want 4 (rune boundary)", len(got))
	}
	if !json.Valid([]byte(`"` + got + `"`)) {
		t.Fatalf("truncated string is not valid UTF-8: %q", got)
	}
}

func TestNotify_202IsSuccess(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202, body: `{"result":"Request will be processed"}`}}}
	o := openOutput(t, doer, nil)
	if err := o.Notify(context.Background(), sdk.Notification{Title: "ok", Severity: model.SeverityInfo}); err != nil {
		t.Fatalf("202 should be success, got: %v", err)
	}
}

func TestNotify_422TerminalNoRetry(t *testing.T) {
	// 422 Unprocessable Entity is a terminal client error: it must not be retried
	// and must surface as an error, without ever leaking the key in the message.
	doer := &recordingDoer{responses: []stubResp{
		{status: 422, body: `{"message":"Message can not be empty.","took":0.0}`},
		{status: 202}, // would succeed if (incorrectly) retried
	}}
	o := openOutput(t, doer, nil)
	err := o.Notify(context.Background(), sdk.Notification{Title: "x", Severity: model.SeverityHigh})
	if err == nil {
		t.Fatal("422 must produce an error")
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on terminal 422)", doer.calls)
	}
	if !strings.Contains(err.Error(), "422") {
		t.Fatalf("error should carry the status: %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error must never contain the api key: %v", err)
	}
}

func TestNotify_RetriesTransientThenSucceeds(t *testing.T) {
	// A 5xx then 202: the delivery layer retries and the connector reports success.
	doer := &recordingDoer{responses: []stubResp{
		{status: 503, body: "upstream down"},
		{status: 202},
	}}
	o := openOutput(t, doer, nil)
	// Drive backoff to zero by swapping in a no-wait delivery client.
	o.deliver = newNoWaitClient(doer, o.attempts)
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x", Severity: model.SeverityMedium}); err != nil {
		t.Fatalf("Notify after transient retry: %v", err)
	}
	if doer.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", doer.calls)
	}
}

// TestNoSecretLeak proves the api key only ever appears on the Authorization
// header and nowhere else: not in the body, not in any other header.
func TestNoSecretLeak(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202}}}
	o := openOutput(t, doer, nil)
	if err := o.Notify(context.Background(), sdk.Notification{
		Title:    "secret check",
		Body:     "no credential here",
		Severity: model.SeverityCritical,
		Fields:   map[string]string{"alias": "a1"},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// The body must not contain the key.
	if strings.Contains(doer.bodies[0], testKey) {
		t.Fatalf("api key leaked into request body: %s", doer.bodies[0])
	}
	// The key must appear exactly once across all headers, and only on Authorization.
	headers := doer.allHeaders.String()
	if strings.Count(headers, testKey) != 1 {
		t.Fatalf("api key should appear exactly once in headers, got:\n%s", headers)
	}
	if !strings.Contains(headers, "Authorization:GenieKey "+testKey) {
		t.Fatalf("api key not on the Authorization header:\n%s", headers)
	}
}
