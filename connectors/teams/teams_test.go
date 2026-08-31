// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package teams

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

// testWebhook is an opaque, secret-bearing Workflows URL (the connector never parses
// it; it is the credential and must never leak into a log, body or error).
const testWebhook = "https://prod-1.westus.logic.azure.com/workflows/secret-token-abc123/triggers/manual/paths/invoke"

// recordingDoer captures every request (URL, headers, body) and returns queued
// responses in order so a test can assert what reached the wire — and prove the
// webhook secret only ever travels in the URL, never in a log or error.
type recordingDoer struct {
	responses []stubResp
	calls     int
	reqs      []recordedReq
}

type stubResp struct {
	status int
	body   string
}

type recordedReq struct {
	url     string
	headers http.Header
	body    string
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	d.reqs = append(d.reqs, recordedReq{url: req.URL.String(), headers: req.Header.Clone(), body: body})
	i := d.calls
	d.calls++
	if i >= len(d.responses) {
		i = len(d.responses) - 1
	}
	r := d.responses[i]
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     http.Header{},
	}, nil
}

func open(t *testing.T, doer delivery.Doer, opts delivery.Options) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	o.opts = opts
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{cfgWebhookURL: testWebhook}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

// envelope is the Workflows message envelope the connector posts.
type envelope struct {
	Type        string `json:"type"`
	Attachments []struct {
		ContentType string         `json:"contentType"`
		ContentURL  *string        `json:"contentUrl"`
		Content     map[string]any `json:"content"`
	} `json:"attachments"`
}

func decodeEnvelope(t *testing.T, body string) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("posted body is not valid JSON: %v\nbody=%q", err, body)
	}
	return e
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != "olivares.teams" || Name != "olivares.teams" {
		t.Errorf("Name = %q (const Name = %q)", d.Name, Name)
	}
	if d.Type != sdk.TypeOutput {
		t.Errorf("Type = %q, want output", d.Type)
	}
	if d.APIVersion != sdk.APIVersion {
		t.Errorf("APIVersion = %q", d.APIVersion)
	}
	var found bool
	for _, f := range d.ConfigFields {
		if f.Key == cfgWebhookURL {
			found = true
			if !f.Required || !f.Secret {
				t.Errorf("webhook_url field = %+v, want Required+Secret", f)
			}
		}
	}
	if !found {
		t.Error("descriptor missing webhook_url config field")
	}
}

func TestOpenRequiresWebhookURL(t *testing.T) {
	o := New()
	err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{}})
	if err == nil {
		t.Fatal("expected error for empty webhook_url")
	}
	if strings.Contains(err.Error(), testWebhook) {
		t.Error("error must not contain a webhook URL")
	}
	if err := o.Notify(context.Background(), sdk.Notification{Title: "x"}); err == nil {
		t.Error("Notify before successful Open should error")
	}
}

func TestNotifyAdaptiveCardShape(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202, body: ""}}}
	o := open(t, doer, delivery.Options{})

	n := sdk.Notification{
		Type:     "finding.reported",
		Title:    "Over-permissioned NHI",
		Body:     "service-account/ci can write to prod-secrets",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"resource": "prod-secrets", "principal": "ci", "approve_url": "https://cp/approve", "deny_url": "https://cp/deny"},
		Time:     time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
	}
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1", doer.calls)
	}

	req := doer.reqs[0]
	if req.url != testWebhook {
		t.Errorf("posted to %q, want webhook URL", req.url)
	}
	if got := req.headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	e := decodeEnvelope(t, req.body)
	if e.Type != "message" {
		t.Errorf("envelope type = %q, want message", e.Type)
	}
	if len(e.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(e.Attachments))
	}
	att := e.Attachments[0]
	if att.ContentType != adaptiveCardType {
		t.Errorf("contentType = %q, want %q", att.ContentType, adaptiveCardType)
	}
	if att.ContentURL != nil {
		t.Errorf("contentUrl = %v, want null", *att.ContentURL)
	}
	card := att.Content
	if card["type"] != "AdaptiveCard" {
		t.Errorf("card type = %v", card["type"])
	}
	if card["$schema"] != adaptiveSchema {
		t.Errorf("schema = %v", card["$schema"])
	}
	if card["version"] != defaultCardVersion {
		t.Errorf("version = %v, want %s", card["version"], defaultCardVersion)
	}
	// Body is a single styled container; high severity => attention.
	body, ok := card["body"].([]any)
	if !ok || len(body) != 1 {
		t.Fatalf("body = %v, want one container", card["body"])
	}
	container := body[0].(map[string]any)
	if container["type"] != "Container" || container["style"] != "attention" {
		t.Errorf("container = %v, want Container/attention", container)
	}
	items := container["items"].([]any)
	// title TextBlock + body TextBlock + FactSet
	if len(items) != 3 {
		t.Fatalf("container items = %d, want 3 (title, body, factset)", len(items))
	}
	factset := items[2].(map[string]any)
	if factset["type"] != "FactSet" {
		t.Fatalf("third item = %v, want FactSet", factset)
	}
	facts := factset["facts"].([]any)
	// Fields (sorted, action URLs excluded): principal, resource, then severity, tenant.
	wantFacts := [][2]string{{"principal", "ci"}, {"resource", "prod-secrets"}, {"severity", "high"}, {"tenant", "acme"}}
	if len(facts) != len(wantFacts) {
		t.Fatalf("facts = %d, want %d: %v", len(facts), len(wantFacts), facts)
	}
	for i, w := range wantFacts {
		f := facts[i].(map[string]any)
		if f["title"] != w[0] || f["value"] != w[1] {
			t.Errorf("fact[%d] = {%v,%v}, want %v", i, f["title"], f["value"], w)
		}
	}
	// Two Action.OpenUrl buttons (Approve, Deny) from the action URLs.
	actions := card["actions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2 (approve, deny)", len(actions))
	}
	a0 := actions[0].(map[string]any)
	if a0["type"] != "Action.OpenUrl" || a0["title"] != "Approve" || a0["url"] != "https://cp/approve" {
		t.Errorf("action[0] = %v", a0)
	}
}

func TestContainerStyleMapping(t *testing.T) {
	cases := map[model.Severity]string{
		model.SeverityCritical:  "attention",
		model.SeverityHigh:      "attention",
		model.SeverityMedium:    "warning",
		model.SeverityLow:       "good",
		model.SeverityInfo:      "good",
		model.Severity(""):      "good",
		model.Severity("weird"): "good",
	}
	for sev, want := range cases {
		if got := containerStyle(sev); got != want {
			t.Errorf("containerStyle(%q) = %q, want %q", sev, got, want)
		}
	}
}

func TestDeterministicRender(t *testing.T) {
	o := New()
	n := sdk.Notification{Title: "t", Fields: map[string]string{"zeta": "1", "alpha": "2", "mu": "3", "beta": "4"}}
	a, err := o.renderEnvelope(n)
	if err != nil {
		t.Fatal(err)
	}
	b, err := o.renderEnvelope(n)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("non-deterministic render:\n a=%s\n b=%s", a, b)
	}
	s := string(a)
	idx := func(sub string) int { return strings.Index(s, sub) }
	if idx(`"alpha"`) > idx(`"beta"`) || idx(`"beta"`) > idx(`"mu"`) || idx(`"mu"`) > idx(`"zeta"`) {
		t.Errorf("facts not in sorted key order: %s", s)
	}
}

func TestNotifyNoTitleNoActions(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 202, body: ""}}}
	o := open(t, doer, delivery.Options{})
	if err := o.Notify(context.Background(), sdk.Notification{Body: "detail only"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	e := decodeEnvelope(t, doer.reqs[0].body)
	card := e.Attachments[0].Content
	if _, ok := card["actions"]; ok {
		t.Errorf("no action URLs => actions must be omitted, got %v", card["actions"])
	}
	container := card["body"].([]any)[0].(map[string]any)
	items := container["items"].([]any)
	// Only the body TextBlock (no title, no fields).
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (body only)", len(items))
	}
}

func TestNotifyRetriesTransientThenSucceeds(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{
		{status: 503, body: "service unavailable"},
		{status: 202, body: ""},
	}}
	var slept []time.Duration
	o := open(t, doer, delivery.Options{
		MaxAttempts: 4, BaseDelay: time.Second,
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
	})
	if err := o.Notify(context.Background(), sdk.Notification{Title: "flaky", Severity: model.SeverityLow}); err != nil {
		t.Fatalf("Notify should succeed after a retry: %v", err)
	}
	if doer.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", doer.calls)
	}
	if len(slept) != 1 {
		t.Fatalf("slept %v, want exactly one backoff before retry", slept)
	}
	if doer.reqs[0].body != doer.reqs[1].body {
		t.Errorf("retry body differs from first attempt")
	}
}

func TestNotifyTerminalErrorReported(t *testing.T) {
	doer := &recordingDoer{responses: []stubResp{{status: 400, body: "Bad payload"}}}
	o := open(t, doer, delivery.Options{MaxAttempts: 5, Sleep: func(context.Context, time.Duration) error { return nil }})
	err := o.Notify(context.Background(), sdk.Notification{Title: "bad"})
	if err == nil {
		t.Fatal("expected a delivery error on terminal 400")
	}
	if doer.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", doer.calls)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status: %v", err)
	}
	if strings.Contains(err.Error(), "secret-token-abc123") || strings.Contains(err.Error(), testWebhook) {
		t.Errorf("SECURITY: webhook secret leaked into error: %v", err)
	}
}

// TestWebhookSecretNeverLeaks is the security invariant: across descriptor, render
// output and delivery error, the webhook secret token never appears. The card payload
// carries only Notification fields; the secret lives solely in the request URL.
func TestWebhookSecretNeverLeaks(t *testing.T) {
	const secret = "secret-token-abc123"
	for _, f := range New().Descriptor().ConfigFields {
		if strings.Contains(f.Default, secret) {
			t.Errorf("descriptor default leaks secret: %+v", f)
		}
	}
	o := New()
	body, err := o.renderEnvelope(sdk.Notification{Title: "anything", Body: "body", Fields: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Errorf("SECURITY: rendered card contains the webhook secret: %s", body)
	}
	doer := &recordingDoer{responses: []stubResp{{status: 403, body: "forbidden"}}}
	opened := open(t, doer, delivery.Options{MaxAttempts: 1, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err := opened.Notify(context.Background(), sdk.Notification{Title: "x"}); err != nil {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("SECURITY: error leaks secret: %v", err)
		}
	}
	if !strings.Contains(doer.reqs[0].url, secret) {
		t.Fatal("expected the webhook secret in the request URL (sanity)")
	}
}

// erroringDoer always fails at the transport layer (no HTTP response). Its error names
// the host + a secret-looking token, which Notify must not propagate.
type erroringDoer struct{ calls int }

func (d *erroringDoer) Do(*http.Request) (*http.Response, error) {
	d.calls++
	return nil, &leakyErr{}
}

type leakyErr struct{}

func (*leakyErr) Error() string {
	return "dial tcp prod-1.westus.logic.azure.com: connection refused (secret-token-abc123 in path)"
}

func TestNotifyPersistentTransportErrorNoLeak(t *testing.T) {
	doer := &erroringDoer{}
	o := open(t, doer, delivery.Options{MaxAttempts: 2, Sleep: func(context.Context, time.Duration) error { return nil }})
	err := o.Notify(context.Background(), sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatal("expected error when transport never returns a response")
	}
	if strings.Contains(err.Error(), "secret-token-abc123") || strings.Contains(err.Error(), "azure.com") {
		t.Errorf("SECURITY: transport error leaked into Notify error: %v", err)
	}
	if !strings.Contains(err.Error(), "no response") {
		t.Errorf("error = %v, want a generic no-response message", err)
	}
	if doer.calls != 2 {
		t.Errorf("calls = %d, want 2 (retried transient transport error)", doer.calls)
	}
}

func TestCloseIsSafe(t *testing.T) {
	o := New()
	if err := o.Close(context.Background()); err != nil {
		t.Errorf("Close before Open should be safe: %v", err)
	}
	doer := &recordingDoer{responses: []stubResp{{status: 202, body: ""}}}
	opened := open(t, doer, delivery.Options{})
	if err := opened.Close(context.Background()); err != nil {
		t.Errorf("Close after Open: %v", err)
	}
}
