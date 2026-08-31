// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/slack"
	"github.com/olivaresai/olivares/connectors/webhook"
)

// spyDecider records whether (and how) a decision reached the engine, so a test can
// prove that a rejected callback NEVER reaches it.
type spyDecider struct {
	called   int
	tenant   string
	token    string
	approval string
	decision string
	result   decisionResult
}

func (s *spyDecider) Decide(_ context.Context, tenant, token, approvalID, decision, _ string) decisionResult {
	s.called++
	s.tenant, s.token, s.approval, s.decision = tenant, token, approvalID, decision
	if s.result.HTTPStatus == 0 {
		return decisionResult{Recorded: true, HTTPStatus: 200, State: "approved"}
	}
	return s.result
}

const (
	slackSecret = "slack-signing-secret-xyz"
	hookSecret  = "olivares-shared-hmac-secret"
)

// testReceiver builds a receiver with one slack and one webhook provider, each with one
// provisioned approver, the given decider, and a fixed clock.
func testReceiver(t *testing.T, dec approvalDecider, now time.Time) *hitlReceiver {
	t.Helper()
	cfg := hitlConfig{Providers: []hitlProviderSpec{
		{Name: "corp-slack", Kind: hitlKindSlack, SigningSecret: slackSecret, Approvers: []hitlApprover{
			{ExternalID: "U-APPROVER", Tenant: "t-acme", Token: "tok-approver"},
		}},
		{Name: "corp-snow", Kind: hitlKindWebhook, SigningSecret: hookSecret, Approvers: []hitlApprover{
			{ExternalID: "alice@corp", Tenant: "t-acme", Token: "tok-alice"},
		}},
	}}
	r := newHITLReceiver(cfg, dec, discardLog())
	if r == nil {
		t.Fatal("receiver should be built from a valid config")
	}
	r.clock = func() time.Time { return now }
	return r
}

func slackRequest(t *testing.T, secret, ts string, payload map[string]any) *http.Request {
	t.Helper()
	pj, _ := json.Marshal(payload)
	body := []byte("payload=" + url.QueryEscape(string(pj)))
	req := httptest.NewRequest(http.MethodPost, "/hitl/corp-slack", strings.NewReader(string(body)))
	req.Header.Set(slack.HeaderTimestamp, ts)
	req.Header.Set(slack.HeaderSignature, slack.SignRequest(secret, ts, body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func blockActions(userID, actionID, value string) map[string]any {
	return map[string]any{
		"type": "block_actions",
		"user": map[string]any{"id": userID},
		"actions": []map[string]any{
			{"action_id": actionID, "value": value},
		},
	}
}

func TestSlackValidCallbackRecordsDecision(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	spy := &spyDecider{}
	r := testReceiver(t, spy, now)

	ts := strconv.FormatInt(now.Unix(), 10)
	req := slackRequest(t, slackSecret, ts, blockActions("U-APPROVER", "approve", "appr-123"))
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if spy.called != 1 {
		t.Fatalf("decider called %d times, want 1", spy.called)
	}
	if spy.token != "tok-approver" || spy.tenant != "t-acme" {
		t.Fatalf("acted as %q/%q, want the mapped approver token/tenant", spy.token, spy.tenant)
	}
	if spy.approval != "appr-123" || spy.decision != "approve" {
		t.Fatalf("decision = %q on %q", spy.decision, spy.approval)
	}
}

func TestSlackBadSignatureNeverTouchesEngine(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	spy := &spyDecider{}
	r := testReceiver(t, spy, now)

	ts := strconv.FormatInt(now.Unix(), 10)
	req := slackRequest(t, slackSecret, ts, blockActions("U-APPROVER", "approve", "appr-123"))
	// Tamper the signature.
	req.Header.Set(slack.HeaderSignature, "v0=deadbeef")
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if spy.called != 0 {
		t.Fatal("SECURITY: a callback with an invalid signature reached the engine")
	}
}

func TestSlackReplayRejected(t *testing.T) {
	signedAt := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	now := signedAt.Add(10 * time.Minute) // outside the 5-min window
	spy := &spyDecider{}
	r := testReceiver(t, spy, now)

	ts := strconv.FormatInt(signedAt.Unix(), 10)
	req := slackRequest(t, slackSecret, ts, blockActions("U-APPROVER", "approve", "appr-123"))
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (replayed)", rec.Code)
	}
	if spy.called != 0 {
		t.Fatal("SECURITY: a replayed callback reached the engine")
	}
}

func TestSlackUnmappedApproverRejected(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	spy := &spyDecider{}
	r := testReceiver(t, spy, now)
	ts := strconv.FormatInt(now.Unix(), 10)
	// A correctly-signed callback from a user with no provisioned approver mapping.
	req := slackRequest(t, slackSecret, ts, blockActions("U-STRANGER", "approve", "appr-123"))
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (unmapped approver)", rec.Code)
	}
	if spy.called != 0 {
		t.Fatal("an unmapped approver must not reach the engine")
	}
}

func webhookRequest(t *testing.T, secret, ts string, c webhookCallback) *http.Request {
	t.Helper()
	body, _ := json.Marshal(c)
	req := httptest.NewRequest(http.MethodPost, "/hitl/corp-snow", strings.NewReader(string(body)))
	sig := "t=" + ts + ",v1=" + webhook.Sign(secret, ts, body)
	req.Header.Set(hdrOlivaresTimestamp, ts)
	req.Header.Set(hdrOlivaresSignature, sig)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestWebhookValidCallbackNormalizesDecision(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	spy := &spyDecider{}
	r := testReceiver(t, spy, now)
	ts := strconv.FormatInt(now.Unix(), 10)
	req := webhookRequest(t, hookSecret, ts, webhookCallback{ApprovalID: "appr-9", Decision: "decline", ExternalID: "alice@corp"})
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)

	if spy.called != 1 {
		t.Fatalf("decider called %d times, want 1", spy.called)
	}
	if spy.decision != "reject" { // "decline" -> reject
		t.Fatalf("decision = %q, want reject", spy.decision)
	}
	if spy.token != "tok-alice" {
		t.Fatalf("acted as %q, want tok-alice", spy.token)
	}
}

func TestWebhookBadSignatureNeverTouchesEngine(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	spy := &spyDecider{}
	r := testReceiver(t, spy, now)
	ts := strconv.FormatInt(now.Unix(), 10)
	req := webhookRequest(t, "wrong-secret", ts, webhookCallback{ApprovalID: "a", Decision: "approve", ExternalID: "alice@corp"})
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if spy.called != 0 {
		t.Fatal("SECURITY: a bad-HMAC webhook callback reached the engine")
	}
}

// TestEngineDeclineReflected proves the receiver honestly reflects an decline
// (e.g. a separation-of-duty rejection): the webhook provider mirrors the engine's
// status, while the slack provider 200-acks (Slack retries non-2xx) but carries the
// decline in its body.
func TestEngineDeclineReflected(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	spy := &spyDecider{result: decisionResult{Recorded: false, HTTPStatus: http.StatusForbidden, Message: "separation of duty"}}
	r := testReceiver(t, spy, now)
	ts := strconv.FormatInt(now.Unix(), 10)

	req := webhookRequest(t, hookSecret, ts, webhookCallback{ApprovalID: "a", Decision: "approve", ExternalID: "alice@corp"})
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("webhook decline status = %d, want 403 mirrored from engine", rec.Code)
	}

	// Slack: 200-ack even on decline (so Slack does not retry), decline in the body.
	sreq := slackRequest(t, slackSecret, ts, blockActions("U-APPROVER", "approve", "appr-1"))
	srec := httptest.NewRecorder()
	r.handler().ServeHTTP(srec, sreq)
	if srec.Code != http.StatusOK {
		t.Fatalf("slack decline status = %d, want 200-ack", srec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(srec.Body.Bytes(), &body)
	if ok, _ := body["ok"].(bool); ok {
		t.Fatalf("slack body should report not-ok on a decline: %v", body)
	}
}

func TestUnknownProvider404(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	spy := &spyDecider{}
	r := testReceiver(t, spy, now)
	req := httptest.NewRequest(http.MethodPost, "/hitl/nope", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	r.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestNormalizeDecision(t *testing.T) {
	approve := []string{"approve", "Approved", "YES", "olivares_approve", "accept"}
	reject := []string{"reject", "deny", "Declined", "no", "olivares_deny"}
	for _, s := range approve {
		if normalizeDecision(s) != "approve" {
			t.Errorf("normalizeDecision(%q) != approve", s)
		}
	}
	for _, s := range reject {
		if normalizeDecision(s) != "reject" {
			t.Errorf("normalizeDecision(%q) != reject", s)
		}
	}
	for _, s := range []string{"maybe", "", "approveish"} {
		if normalizeDecision(s) != "" {
			t.Errorf("normalizeDecision(%q) should be empty (unrecognized)", s)
		}
	}
}

func TestReceiverNotBuiltWithoutSecret(t *testing.T) {
	cfg := hitlConfig{Providers: []hitlProviderSpec{{Name: "x", Kind: hitlKindSlack}}}
	if newHITLReceiver(cfg, &spyDecider{}, discardLog()) != nil {
		t.Fatal("a provider with no signing secret must be skipped (no usable provider => nil receiver)")
	}
}
