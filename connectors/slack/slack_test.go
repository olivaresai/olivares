// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// recordingDoer captures every request (method, URL, headers, body) and returns a
// canned response. It is the deterministic transport injected into the connector
// so no live Slack call is made and the payload/auth can be asserted exactly.
type recordingDoer struct {
	reqs    []recordedReq
	status  int
	body    string
	doErr   error
	doCalls int
}

type recordedReq struct {
	method string
	url    string
	header http.Header
	body   string
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	d.doCalls++
	var b string
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		b = string(raw)
	}
	// Clone the header so later attempts cannot mutate what we recorded.
	h := req.Header.Clone()
	d.reqs = append(d.reqs, recordedReq{method: req.Method, url: req.URL.String(), header: h, body: b})
	if d.doErr != nil {
		return nil, d.doErr
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     make(http.Header),
	}, nil
}

const (
	testWebhookURL = "https://hooks.slack" + ".com/services/T000/B000/secrethookpath"
	testBotToken   = "xoxb" + "-9999-supersecret-token-value"
)

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "Over-permissioned NHI detected",
		Body:     "service-account robot-7 can write to prod-secrets",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields: map[string]string{
			"resource": "prod-secrets",
			"actor":    "robot-7",
		},
		Time: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	}
}

func openWebhook(t *testing.T, doer *recordingDoer) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	cfg := sdk.Config{Settings: map[string]string{
		"mode":        "webhook",
		"webhook_url": testWebhookURL,
		"username":    "Olivares",
	}}
	if err := o.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func openAPI(t *testing.T, doer *recordingDoer) *Output {
	t.Helper()
	o := New()
	o.doer = doer
	cfg := sdk.Config{Settings: map[string]string{
		"mode":      "api",
		"bot_token": testBotToken,
		"channel":   "#alerts",
	}}
	if err := o.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name {
		t.Fatalf("name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeOutput {
		t.Fatalf("type = %q, want output", d.Type)
	}
	if d.APIVersion != sdk.APIVersion {
		t.Fatalf("api version = %q, want %q", d.APIVersion, sdk.APIVersion)
	}
	// webhook_url and bot_token MUST be declared secret.
	secret := map[string]bool{}
	for _, f := range d.ConfigFields {
		secret[f.Key] = f.Secret
	}
	for _, k := range []string{"webhook_url", "bot_token"} {
		if !secret[k] {
			t.Fatalf("config field %q must be declared Secret:true", k)
		}
	}
}

func TestOpen_ValidationErrors(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		wantErr  bool
	}{
		{"webhook missing url", map[string]string{"mode": "webhook"}, true},
		{"webhook ok", map[string]string{"mode": "webhook", "webhook_url": testWebhookURL}, false},
		{"api missing token", map[string]string{"mode": "api", "channel": "#x"}, true},
		{"api missing channel", map[string]string{"mode": "api", "bot_token": testBotToken}, true},
		{"api ok", map[string]string{"mode": "api", "bot_token": testBotToken, "channel": "#x"}, false},
		{"unknown mode", map[string]string{"mode": "carrier-pigeon", "webhook_url": testWebhookURL}, true},
		{"default mode needs webhook", map[string]string{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := New()
			o.doer = &recordingDoer{body: "ok"}
			err := o.Open(context.Background(), sdk.Config{Settings: c.settings})
			if (err != nil) != c.wantErr {
				t.Fatalf("Open err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestNotify_WebhookPayload(t *testing.T) {
	doer := &recordingDoer{status: 200, body: "ok"}
	o := openWebhook(t, doer)

	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(doer.reqs))
	}
	req := doer.reqs[0]
	if req.method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.method)
	}
	if req.url != testWebhookURL {
		t.Fatalf("url = %s, want webhook url", req.url)
	}
	if got := req.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	// Webhook mode must NOT send an Authorization header.
	if req.header.Get("Authorization") != "" {
		t.Fatalf("webhook mode leaked an Authorization header")
	}

	var msg webhookMessage
	if err := json.Unmarshal([]byte(req.body), &msg); err != nil {
		t.Fatalf("payload not valid JSON: %v\n%s", err, req.body)
	}
	if msg.Text != "Over-permissioned NHI detected" {
		t.Fatalf("text = %q, want title", msg.Text)
	}
	if msg.Username != "Olivares" {
		t.Fatalf("username = %q", msg.Username)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	a := msg.Attachments[0]
	if a.Color != colorCritical {
		t.Fatalf("high severity color = %q, want %q", a.Color, colorCritical)
	}
	if a.Text != "service-account robot-7 can write to prod-secrets" {
		t.Fatalf("attachment text = %q", a.Text)
	}
	if a.TS != sampleNotification().Time.Unix() {
		t.Fatalf("ts = %d, want %d", a.TS, sampleNotification().Time.Unix())
	}
	// Fields are sorted by key: actor before resource.
	if len(a.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(a.Fields))
	}
	if a.Fields[0].Title != "actor" || a.Fields[0].Value != "robot-7" {
		t.Fatalf("field[0] = %+v, want actor=robot-7", a.Fields[0])
	}
	if a.Fields[1].Title != "resource" || a.Fields[1].Value != "prod-secrets" {
		t.Fatalf("field[1] = %+v, want resource=prod-secrets", a.Fields[1])
	}
}

func TestNotify_APIPayloadAndAuthHeader(t *testing.T) {
	doer := &recordingDoer{status: 200, body: `{"ok":true,"channel":"C0001","ts":"1700000000.000100"}`}
	o := openAPI(t, doer)

	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(doer.reqs))
	}
	req := doer.reqs[0]
	if req.url != postMessageURL {
		t.Fatalf("url = %s, want %s", req.url, postMessageURL)
	}
	if got := req.header.Get("Authorization"); got != "Bearer "+testBotToken {
		t.Fatalf("authorization header = %q, want Bearer <token>", got)
	}
	if got := req.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}

	var msg apiMessage
	if err := json.Unmarshal([]byte(req.body), &msg); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if msg.Channel != "#alerts" {
		t.Fatalf("channel = %q, want #alerts", msg.Channel)
	}
	if msg.Text != "Over-permissioned NHI detected" {
		t.Fatalf("text = %q", msg.Text)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Color != colorCritical {
		t.Fatalf("attachment = %+v", msg.Attachments)
	}
}

func TestNotify_APILogicalErrorBecomesError(t *testing.T) {
	// HTTP 200 but ok:false — must surface as an error carrying the Slack code.
	doer := &recordingDoer{status: 200, body: `{"ok":false,"error":"channel_not_found"}`}
	o := openAPI(t, doer)

	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("expected error for ok:false body, got nil")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("error %q does not carry the Slack error code", err)
	}
	// A terminal logical error must NOT cause retry loops: exactly one HTTP call.
	if doer.doCalls != 1 {
		t.Fatalf("logical error retried: %d calls, want 1", doer.doCalls)
	}
}

// TestNotifyAPILogicalErrorMinimalBody proves the connector treats ok==false as a
// failure even when the response omits the "error" code entirely. A 200 with a
// bare {"ok":false} body must still surface as an error (the connector substitutes
// a generic code), not be mistaken for success because no error string was present.
func TestNotifyAPILogicalErrorMinimalBody(t *testing.T) {
	doer := &recordingDoer{status: 200, body: `{"ok":false}`}
	o := openAPI(t, doer)

	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("expected error for bare ok:false body (no error field), got nil")
	}
	// No "error" field means the connector falls back to a generic code; either way
	// it must NOT silently treat the send as a success.
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("error %q does not indicate the API rejected the message", err)
	}
	// A terminal logical error must not retry: exactly one HTTP call.
	if doer.doCalls != 1 {
		t.Fatalf("logical error retried: %d calls, want 1", doer.doCalls)
	}
}

func TestNotify_SeverityColorMapping(t *testing.T) {
	cases := []struct {
		sev   model.Severity
		color string
	}{
		{model.SeverityCritical, colorCritical},
		{model.SeverityHigh, colorCritical},
		{model.SeverityMedium, colorMedium},
		{model.SeverityLow, colorLow},
		{model.SeverityInfo, colorLow},
		{model.Severity(""), colorLow}, // unknown/empty fails closed to calm green
	}
	for _, c := range cases {
		t.Run(string(c.sev), func(t *testing.T) {
			doer := &recordingDoer{status: 200, body: "ok"}
			o := openWebhook(t, doer)
			n := sampleNotification()
			n.Severity = c.sev
			if err := o.Notify(context.Background(), n); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			var msg webhookMessage
			if err := json.Unmarshal([]byte(doer.reqs[0].body), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if msg.Attachments[0].Color != c.color {
				t.Fatalf("severity %q color = %q, want %q", c.sev, msg.Attachments[0].Color, c.color)
			}
		})
	}
}

// TestNotify_NoSecretLeak is the security invariant: neither the webhook URL nor
// the bot token may ever appear in a returned error, across both the success and
// the failure paths. delivery itself does not log; this guards our own wrapping.
func TestNotify_NoSecretLeak(t *testing.T) {
	t.Run("api transport failure", func(t *testing.T) {
		// Terminal 4xx so delivery gives up immediately and returns an error.
		doer := &recordingDoer{status: http.StatusBadRequest, body: "Bad Request"}
		o := openAPI(t, doer)
		o.maxAttempts = 1
		o.client = nil
		// Re-open to apply maxAttempts=1 deterministically.
		_ = o.Open(context.Background(), sdk.Config{Settings: map[string]string{
			"mode": "api", "bot_token": testBotToken, "channel": "#alerts", "max_attempts": "1",
		}})
		err := o.Notify(context.Background(), sampleNotification())
		if err == nil {
			t.Fatal("expected error on 400")
		}
		assertNoSecret(t, err.Error())
	})

	t.Run("api logical error", func(t *testing.T) {
		doer := &recordingDoer{status: 200, body: `{"ok":false,"error":"not_in_channel"}`}
		o := openAPI(t, doer)
		err := o.Notify(context.Background(), sampleNotification())
		if err == nil {
			t.Fatal("expected error")
		}
		assertNoSecret(t, err.Error())
	})

	t.Run("webhook transport failure", func(t *testing.T) {
		doer := &recordingDoer{status: http.StatusBadRequest, body: "invalid_payload"}
		o := New()
		o.doer = doer
		_ = o.Open(context.Background(), sdk.Config{Settings: map[string]string{
			"mode": "webhook", "webhook_url": testWebhookURL, "max_attempts": "1",
		}})
		err := o.Notify(context.Background(), sampleNotification())
		if err == nil {
			t.Fatal("expected error on 400")
		}
		assertNoSecret(t, err.Error())
	})
}

func assertNoSecret(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, testBotToken) {
		t.Fatalf("bot token leaked into error: %q", s)
	}
	// The secret path of the webhook URL must not leak.
	if strings.Contains(s, "secrethookpath") {
		t.Fatalf("webhook url leaked into error: %q", s)
	}
}

func TestNotify_BeforeOpenFails(t *testing.T) {
	o := New()
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("Notify before Open must error")
	}
}

func TestNotify_EmptyFieldsOmitsFieldsArray(t *testing.T) {
	doer := &recordingDoer{status: 200, body: "ok"}
	o := openWebhook(t, doer)
	n := sampleNotification()
	n.Fields = nil
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var msg webhookMessage
	if err := json.Unmarshal([]byte(doer.reqs[0].body), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msg.Attachments[0].Fields) != 0 {
		t.Fatalf("expected no fields, got %d", len(msg.Attachments[0].Fields))
	}
}

func TestClose(t *testing.T) {
	if err := New().Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// approvalNotification is a notification carrying the interactive approve/deny
// actions, exactly as modules/notify originates one for an opened approval (the
// action_id + value the inbound HITL receiver parses on a click).
func approvalNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "approval.requested",
		Title:    "Approval needed: sessions.run.launch",
		Body:     "A critical-risk action awaits approval (2 approver(s) required).",
		Severity: model.SeverityCritical,
		Tenant:   "acme",
		Fields: map[string]string{
			"approval_id":        "appr_123",
			"action":             "sessions.run.launch",
			"risk_tier":          "critical",
			"required_approvals": "2",
		},
		Time: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
		Actions: []sdk.NotificationAction{
			{Label: "Approve", ID: "olivares_approve", Value: "approve:appr_123", Style: "primary"},
			{Label: "Deny", ID: "olivares_deny", Value: "deny:appr_123", Style: "danger"},
		},
	}
}

// findButtons returns the elements of the (single) actions block.
func findButtons(t *testing.T, blocks []block) []blockElement {
	t.Helper()
	for _, b := range blocks {
		if b.Type == "actions" {
			return b.Elements
		}
	}
	t.Fatalf("no actions block in %d blocks", len(blocks))
	return nil
}

func TestNotify_BlockKitInteractiveAPI(t *testing.T) {
	doer := &recordingDoer{status: 200, body: `{"ok":true}`}
	o := openAPI(t, doer)
	if err := o.Notify(context.Background(), approvalNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var msg apiMessage
	if err := json.Unmarshal([]byte(doer.reqs[0].body), &msg); err != nil {
		t.Fatalf("payload not valid JSON: %v\n%s", err, doer.reqs[0].body)
	}
	// An interactive card renders blocks, NOT a legacy attachment, and keeps a
	// non-empty top-level text as the notification/accessibility fallback.
	if len(msg.Attachments) != 0 {
		t.Fatalf("interactive card must not also send attachments, got %d", len(msg.Attachments))
	}
	if msg.Text == "" {
		t.Fatalf("interactive card must keep a fallback text")
	}
	if msg.Channel != "#alerts" {
		t.Fatalf("channel = %q", msg.Channel)
	}
	btns := findButtons(t, msg.Blocks)
	if len(btns) != 2 {
		t.Fatalf("buttons = %d, want 2", len(btns))
	}
	// action_id + value are copied VERBATIM — this is the inbound receiver's contract.
	want := []blockElement{
		{Type: "button", ActionID: "olivares_approve", Value: "approve:appr_123", Style: "primary"},
		{Type: "button", ActionID: "olivares_deny", Value: "deny:appr_123", Style: "danger"},
	}
	for i, w := range want {
		if btns[i].Type != w.Type || btns[i].ActionID != w.ActionID || btns[i].Value != w.Value || btns[i].Style != w.Style {
			t.Fatalf("button[%d] = %+v, want id=%s value=%s style=%s", i, btns[i], w.ActionID, w.Value, w.Style)
		}
		if btns[i].Text == nil || btns[i].Text.Type != "plain_text" || btns[i].Text.Text == "" {
			t.Fatalf("button[%d] label = %+v, want non-empty plain_text", i, btns[i].Text)
		}
	}
}

func TestNotify_BlockKitInteractiveWebhook(t *testing.T) {
	doer := &recordingDoer{status: 200, body: "ok"}
	o := openWebhook(t, doer)
	if err := o.Notify(context.Background(), approvalNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var msg webhookMessage
	if err := json.Unmarshal([]byte(doer.reqs[0].body), &msg); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if len(msg.Attachments) != 0 {
		t.Fatalf("webhook interactive card must not send attachments, got %d", len(msg.Attachments))
	}
	if len(findButtons(t, msg.Blocks)) != 2 {
		t.Fatalf("webhook card must carry 2 buttons")
	}
}

// TestNotify_NoActionsStaysLegacy proves the absence of Actions is fully
// backward-compatible: a finding still renders as one severity-colored attachment
// and emits no blocks.
func TestNotify_NoActionsStaysLegacy(t *testing.T) {
	doer := &recordingDoer{status: 200, body: "ok"}
	o := openWebhook(t, doer)
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	var msg webhookMessage
	if err := json.Unmarshal([]byte(doer.reqs[0].body), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msg.Blocks) != 0 {
		t.Fatalf("a finding (no actions) must not render blocks, got %d", len(msg.Blocks))
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("a finding must render one attachment, got %d", len(msg.Attachments))
	}
}

func TestButtonElements_SkipsMalformedAndUnknownStyle(t *testing.T) {
	els := buttonElements([]sdk.NotificationAction{
		{Label: "", ID: "x", Value: "v"},                      // no label -> skipped
		{Label: "y", ID: "", Value: "v"},                      // no id -> skipped
		{Label: "OK", ID: "ok", Value: "v", Style: "rainbow"}, // unknown style -> dropped
	})
	if len(els) != 1 {
		t.Fatalf("elements = %d, want 1 (two malformed skipped)", len(els))
	}
	if els[0].Style != "" {
		t.Fatalf("unknown style must be dropped, got %q", els[0].Style)
	}
}

func TestBlockFields_CapsAndMarksOverflow(t *testing.T) {
	in := map[string]string{}
	for i := 0; i < 15; i++ {
		in[fmt.Sprintf("k%02d", i)] = "v"
	}
	fs := blockFields(in)
	if len(fs) != maxSectionFields {
		t.Fatalf("fields = %d, want capped at %d", len(fs), maxSectionFields)
	}
	last := fs[len(fs)-1].Text
	if !strings.Contains(last, "more") {
		t.Fatalf("overflow must be marked, last field = %q", last)
	}
}

func TestEscapeMrkdwn(t *testing.T) {
	if got := escapeMrkdwn("a & b < c > d"); got != "a &amp; b &lt; c &gt; d" {
		t.Fatalf("escapeMrkdwn = %q", got)
	}
}
