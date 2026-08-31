// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/slack"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/sdk"
)

// This is the proof that the HITL chat round-trip closes END TO END across
// both license planes: notify (AGPL) originates the approve/deny action pair, the
// Slack connector (Apache) renders it as interactive Block Kit buttons, and the
// inbound receiver (AGPL, hitl.go) parses a click of those exact buttons back into
// a governed decision. It deliberately uses the REAL connector render and the REAL
// signature-verified receiver — not hand-built shapes — so a drift on either side
// fails here.

// slackButton is one rendered interactive button extracted from a posted card.
type slackButton struct {
	actionID string
	value    string
}

// extractSlackButtons parses a captured Slack message payload (webhook or API
// shape, both carry top-level "blocks") and returns the buttons in its single
// actions block — exactly what Slack copies into the interactivity callback on a
// click.
func extractSlackButtons(t *testing.T, payload []byte) []slackButton {
	t.Helper()
	var msg struct {
		Blocks []struct {
			Type     string `json:"type"`
			Elements []struct {
				Type     string `json:"type"`
				ActionID string `json:"action_id"`
				Value    string `json:"value"`
			} `json:"elements"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("rendered card is not valid JSON: %v\n%s", err, payload)
	}
	var out []slackButton
	for _, b := range msg.Blocks {
		if b.Type != "actions" {
			continue
		}
		for _, e := range b.Elements {
			if e.Type == "button" {
				out = append(out, slackButton{actionID: e.ActionID, value: e.Value})
			}
		}
	}
	return out
}

func TestHITLApprovalOriginationRoundTrip(t *testing.T) {
	const approvalID = "appr_round_7f3"

	// 1. ORIGINATE: render the approval card through the real Slack connector in
	//    webhook mode, pointed at a capture server (the only public way to observe
	//    the rendered payload). The actions are notify's canonical pair.
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	out := slack.New()
	if err := out.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode": "webhook", "webhook_url": srv.URL,
	}}); err != nil {
		t.Fatalf("open slack: %v", err)
	}
	card := sdk.Notification{
		Type:    "approval.requested",
		Title:   "Approval needed: sessions.run.launch",
		Body:    "A critical-risk action awaits 2 approvals.",
		Fields:  map[string]string{"approval_id": approvalID, "risk_tier": "critical"},
		Actions: notify.ApprovalActions(approvalID),
	}
	if err := out.Notify(context.Background(), card); err != nil {
		t.Fatalf("notify slack: %v", err)
	}

	buttons := extractSlackButtons(t, captured)
	if len(buttons) != 2 {
		t.Fatalf("rendered card carries %d buttons, want approve+deny", len(buttons))
	}

	// 2. ROUND-TRIP: drive a click of each rendered button through the FULL inbound
	//    receiver (Slack HMAC verified) and assert the governed engine receives the
	//    right decision on the right approval — the loop is closed.
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	wantDecision := map[string]string{"olivares_approve": "approve", "olivares_deny": "reject"}
	seen := map[string]bool{}
	for _, b := range buttons {
		spy := &spyDecider{}
		r := testReceiver(t, spy, now)
		ts := strconv.FormatInt(now.Unix(), 10)
		req := slackRequest(t, slackSecret, ts, blockActions("U-APPROVER", b.actionID, b.value))
		rec := httptest.NewRecorder()
		r.handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("button %q: receiver status = %d, want 200", b.actionID, rec.Code)
		}
		if spy.called != 1 {
			t.Fatalf("button %q: engine reached %d times, want 1", b.actionID, spy.called)
		}
		if spy.approval != approvalID {
			t.Fatalf("button %q: approval id = %q, want %q (lost across the round-trip)", b.actionID, spy.approval, approvalID)
		}
		want, ok := wantDecision[b.actionID]
		if !ok {
			t.Fatalf("unexpected rendered action_id %q (not the inbound contract)", b.actionID)
		}
		if spy.decision != want {
			t.Fatalf("button %q: engine decision = %q, want %q", b.actionID, spy.decision, want)
		}
		seen[want] = true
	}
	if !seen["approve"] || !seen["reject"] {
		t.Fatalf("round-trip did not cover both approve and reject: %v", seen)
	}
}
