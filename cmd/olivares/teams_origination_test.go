// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/connectors/teams"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestTeamsActionExecuteOriginationRoundTrip proves the Teams Action.Execute
// origination contract closes end-to-end against the SAME inbound parser the
// receiver uses (cmd/olivares/hitl.go parseTeams), exactly as the Slack round-trip
// does. The originator builds the card from notify.ApprovalActions (the one
// source of truth); the registered bot posts teams.RenderActionExecuteActivity
// verbatim (verified in enterprise/incidentloop). Here we take that rendered card,
// wrap each button's data in the adaptiveCard/action Invoke envelope Teams sends on
// a click, and confirm parseTeams extracts the SAME decision + approval id — so the
// click resolves the correct governed decision on the correct approval.
func TestTeamsActionExecuteOriginationRoundTrip(t *testing.T) {
	const approvalID = "appr:2026-06-20:weird:id" // contains ':' to stress the split
	n := sdk.Notification{
		Title:    "Approval required",
		Severity: model.SeverityHigh,
		Actions:  notify.ApprovalActions(approvalID),
	}

	raw, err := teams.RenderActionExecuteActivity(n, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Parse the rendered activity down to its Action.Execute buttons.
	var act struct {
		Attachments []struct {
			Content struct {
				Actions []struct {
					Verb string          `json:"verb"`
					Data json.RawMessage `json:"data"`
				} `json:"actions"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(raw, &act); err != nil {
		t.Fatalf("unmarshal activity: %v", err)
	}
	if len(act.Attachments) != 1 || len(act.Attachments[0].Content.Actions) != 2 {
		t.Fatalf("expected 2 buttons, got %s", raw)
	}

	want := map[string]string{"olivares_approve": "approve", "olivares_deny": "reject"}
	for _, btn := range act.Attachments[0].Content.Actions {
		// Build the inbound Invoke exactly as Teams delivers a click: the button's
		// verb + data echoed inside value.action, with the approver's stable Entra id.
		invoke := map[string]any{
			"type":       "invoke",
			"name":       "adaptiveCard/action",
			"serviceUrl": "https://smba.test/teams",
			"from":       map[string]any{"aadObjectId": "entra-user-1"},
			"value": map[string]any{
				"action": map[string]any{"verb": btn.Verb, "data": btn.Data},
			},
		}
		body, _ := json.Marshal(invoke)

		dec, err := parseTeams(body)
		if err != nil {
			t.Fatalf("parseTeams(%s): %v", btn.Verb, err)
		}
		if dec.externalID != "entra-user-1" {
			t.Fatalf("externalID = %q", dec.externalID)
		}
		if dec.approvalID != approvalID {
			t.Fatalf("approvalID = %q, want %q (the ':' in the id must survive)", dec.approvalID, approvalID)
		}
		gotDecision := normalizeDecision(dec.decision)
		if gotDecision != want[btn.Verb] {
			t.Fatalf("button %q normalized to %q, want %q", btn.Verb, gotDecision, want[btn.Verb])
		}
	}
}
