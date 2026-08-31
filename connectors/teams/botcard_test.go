// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package teams

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// approvalActions mirrors modules/notify.ApprovalActions verbatim (the connector
// must not import the AGPL module; this documents the exact wire contract the
// originator produces and the Action.Execute card must carry).
func approvalActions(id string) []sdk.NotificationAction {
	return []sdk.NotificationAction{
		{Label: "Approve", ID: "olivares_approve", Value: "approve:" + id, Style: "primary"},
		{Label: "Deny", ID: "olivares_deny", Value: "deny:" + id, Style: "danger"},
	}
}

// renderedCard is the subset of the Bot Framework Activity a test inspects.
type renderedCard struct {
	Type        string `json:"type"`
	Attachments []struct {
		ContentType string `json:"contentType"`
		Content     struct {
			Schema  string `json:"$schema"`
			Type    string `json:"type"`
			Version string `json:"version"`
			Body    []any  `json:"body"`
			Actions []struct {
				Type  string            `json:"type"`
				Title string            `json:"title"`
				Verb  string            `json:"verb"`
				Data  map[string]string `json:"data"`
			} `json:"actions"`
		} `json:"content"`
	} `json:"attachments"`
}

func renderActivity(t *testing.T, n sdk.Notification, version string) renderedCard {
	t.Helper()
	raw, err := RenderActionExecuteActivity(n, version)
	if err != nil {
		t.Fatalf("RenderActionExecuteActivity: %v", err)
	}
	var rc renderedCard
	if err := json.Unmarshal(raw, &rc); err != nil {
		t.Fatalf("rendered activity not valid JSON: %v\n%s", err, raw)
	}
	return rc
}

func TestRenderActionExecute_ApprovalRoundTripShape(t *testing.T) {
	const id = "appr-2026-06-20-xyz"
	n := sdk.Notification{
		Title:    "Approval required: inference.content.firewall",
		Body:     "A held detection awaits your decision.",
		Severity: model.SeverityHigh,
		Tenant:   "tenant-42",
		Fields:   map[string]string{"risk_tier": "critical"},
		Actions:  approvalActions(id),
	}
	rc := renderActivity(t, n, "")

	if rc.Type != "message" {
		t.Fatalf("activity type = %q, want message", rc.Type)
	}
	if len(rc.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(rc.Attachments))
	}
	att := rc.Attachments[0]
	if att.ContentType != adaptiveCardType {
		t.Fatalf("contentType = %q, want %q", att.ContentType, adaptiveCardType)
	}
	if att.Content.Type != "AdaptiveCard" || att.Content.Schema != adaptiveSchema {
		t.Fatalf("card type/schema = %q/%q", att.Content.Type, att.Content.Schema)
	}
	if att.Content.Version != minUniversalActionVersion {
		t.Fatalf("version = %q, want %q (floor)", att.Content.Version, minUniversalActionVersion)
	}
	if len(att.Content.Body) == 0 {
		t.Fatal("card body must carry the styled container")
	}
	if len(att.Content.Actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(att.Content.Actions))
	}

	// The decisive contract: each button is an Action.Execute whose data is exactly
	// {decision, approval_id} — what cmd/olivares/hitl.go parseTeams reads.
	want := []struct{ decision, verb string }{
		{"approve", "olivares_approve"},
		{"deny", "olivares_deny"},
	}
	for i, a := range att.Content.Actions {
		if a.Type != "Action.Execute" {
			t.Fatalf("action[%d] type = %q, want Action.Execute", i, a.Type)
		}
		if a.Verb != want[i].verb {
			t.Fatalf("action[%d] verb = %q, want %q", i, a.Verb, want[i].verb)
		}
		if a.Data["decision"] != want[i].decision {
			t.Fatalf("action[%d] data.decision = %q, want %q", i, a.Data["decision"], want[i].decision)
		}
		if a.Data["approval_id"] != id {
			t.Fatalf("action[%d] data.approval_id = %q, want %q", i, a.Data["approval_id"], id)
		}
	}
}

func TestUniversalActionVersionFloor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "1.4"},
		{"1.0", "1.4"},
		{"1.3", "1.4"},
		{"1.4", "1.4"},
		{"1.5", "1.5"},
		{"1.10", "1.10"}, // numeric compare: 1.10 >= 1.4 (lexical would wrongly floor it)
		{"2.0", "2.0"},
		{"garbage", "1.4"},
	}
	for _, c := range cases {
		if got := universalActionVersion(c.in); got != c.want {
			t.Fatalf("universalActionVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExecuteActions_SkipsMalformedAndPacksValue(t *testing.T) {
	// An action with neither id nor value is skipped (never a dead button). A value
	// with no ":" leaves the decision to the id (stripped of the olivares_ prefix).
	n := sdk.Notification{Actions: []sdk.NotificationAction{
		{Label: "Empty"}, // skipped
		{Label: "Open", ID: "olivares_open", Value: "id7"}, // no ":" -> decision from id
	}}
	rc := renderActivity(t, n, "1.4")
	acts := rc.Attachments[0].Content.Actions
	if len(acts) != 1 {
		t.Fatalf("actions = %d, want 1 (malformed skipped)", len(acts))
	}
	if acts[0].Data["decision"] != "open" || acts[0].Data["approval_id"] != "id7" {
		t.Fatalf("data = %v, want decision=open approval_id=id7", acts[0].Data)
	}
}

func TestRenderActionExecute_NoActionsOmitsActions(t *testing.T) {
	rc := renderActivity(t, sdk.Notification{Title: "fyi", Severity: model.SeverityInfo}, "")
	if rc.Attachments[0].Content.Actions != nil && len(rc.Attachments[0].Content.Actions) != 0 {
		t.Fatalf("expected no actions, got %d", len(rc.Attachments[0].Content.Actions))
	}
}

func TestRenderActionExecute_TitleClamped(t *testing.T) {
	long := strings.Repeat("é", 200) // multi-byte; must clamp on a rune boundary
	n := sdk.Notification{Actions: []sdk.NotificationAction{{Label: long, ID: "x", Value: "approve:1"}}}
	rc := renderActivity(t, n, "")
	got := rc.Attachments[0].Content.Actions[0].Title
	if len([]rune(got)) != maxActionTitleLen {
		t.Fatalf("title runes = %d, want %d", len([]rune(got)), maxActionTitleLen)
	}
}
