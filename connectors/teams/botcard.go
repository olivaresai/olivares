// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// botcard.go renders the REGISTERED-BOT half of the Teams Adaptive Card: a Bot
// Framework message Activity carrying an Adaptive Card whose buttons are
// `Action.Execute` Universal Actions (verb + data), as opposed to the one-way
// `Action.OpenUrl` card teams.go posts through a Power Automate Workflows webhook.
//
// Why this is separate from Notify. A card posted through a plain Workflows
// incoming webhook is ONE-WAY (a click cannot return to this service), so that
// path deliberately emits only Action.OpenUrl (teams.go). A true round-trip
// Adaptive Card needs a REGISTERED bot (Bot Framework app + Microsoft Entra JWT
// validation) that posts the card proactively and receives the click as an
// `adaptiveCard/action` Invoke. This file renders the card+activity for that bot;
// the bot's credentials, OAuth token and the proactive POST live in the closed
// add-on, not here (no engine, no secret reaches this file — Apache boundary).
//
// The button DATA is the contract the inbound receiver already parses
// (cmd/olivares/hitl.go parseTeams reads value.action.data as {decision,
// approval_id}). RenderActionExecuteActivity derives that object from the generic
// sdk.NotificationAction the originator sets (notify.ApprovalActions), so the
// Teams round-trip closes against the SAME governed decision path as Slack with
// no new wire contract.
//
// Universal Actions require Adaptive Card schema version 1.4 or greater (VERIFIED
// primary source, jun-2026, learn.microsoft.com adaptive-cards/authoring-cards/
// universal-action-model): below 1.4 an Action.Execute does not render. The card
// version defaults to 1.4 here; an empty/older version is raised to the floor so
// the bot never emits a card whose buttons silently fail to render.

package teams

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// minUniversalActionVersion is the Adaptive Card schema floor for Action.Execute.
const minUniversalActionVersion = "1.4"

// maxActionTitleLen bounds a rendered button label (non-sensitive display text).
const maxActionTitleLen = 128

// activityMessageType is the Bot Framework Activity type for a message-bearing
// activity (the one that carries card attachments).
const activityMessageType = "message"

// botActivity is the minimal Bot Framework Activity the registered bot posts to
// the Connector API ({serviceUrl}/v3/conversations/{id}/activities): a message
// with the Adaptive Card inline in attachments (NOT stringified).
type botActivity struct {
	Type        string          `json:"type"`
	Attachments []botAttachment `json:"attachments"`
}

type botAttachment struct {
	ContentType string      `json:"contentType"`
	Content     executeCard `json:"content"`
}

// executeCard is an Adaptive Card whose actions are Universal Actions
// (Action.Execute). It differs from teams.go's adaptiveCard only in the action
// element type (Execute carries verb+data, not a URL).
type executeCard struct {
	Schema  string          `json:"$schema"`
	Type    string          `json:"type"`
	Version string          `json:"version"`
	Body    []any           `json:"body"`
	Actions []executeAction `json:"actions,omitempty"`
}

// executeAction is an Action.Execute button. Only `type` is required by the
// schema; `verb` is kept for bot-side routing/identification and `data` is the
// payload echoed back in the inbound Invoke (value.action.data).
type executeAction struct {
	Type  string            `json:"type"`
	Title string            `json:"title"`
	Verb  string            `json:"verb,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
}

// RenderActionExecuteActivity builds the Bot Framework message Activity (JSON)
// the registered bot posts proactively: an Adaptive Card (schema ≥ 1.4) whose
// buttons are Action.Execute Universal Actions derived from n.Actions, wrapped in
// the same severity-accented container the Workflows card uses. cardVersion
// defaults to (and is floored at) 1.4 so Action.Execute always renders. It
// returns the marshaled Activity ready to POST to the Connector API; it holds no
// secret and reaches no engine.
func RenderActionExecuteActivity(n sdk.Notification, cardVersion string) ([]byte, error) {
	card := executeCard{
		Schema:  adaptiveSchema,
		Type:    "AdaptiveCard",
		Version: universalActionVersion(cardVersion),
		Body:    []any{cardContainer(n)},
		Actions: executeActions(n),
	}
	act := botActivity{
		Type:        activityMessageType,
		Attachments: []botAttachment{{ContentType: adaptiveCardType, Content: card}},
	}
	return json.Marshal(act)
}

// universalActionVersion returns a card version safe for Action.Execute: the
// requested version when it parses to at least 1.4, otherwise the 1.4 floor. The
// comparison is numeric (major, minor) — a lexical compare would wrongly rank
// "1.10" below "1.4". An empty or unparseable version is raised to the floor so a
// button never silently fails to render.
func universalActionVersion(v string) string {
	v = strings.TrimSpace(v)
	if atLeastVersion(v, 1, 4) {
		return v
	}
	return minUniversalActionVersion
}

// atLeastVersion reports whether the "major.minor" version v is >= (major,minor),
// comparing numerically. A missing minor is treated as 0; an unparseable version
// is below everything (false).
func atLeastVersion(v string, major, minor int) bool {
	maj, min, ok := parseVersion(v)
	if !ok {
		return false
	}
	if maj != major {
		return maj > major
	}
	return min >= minor
}

// parseVersion parses a "major" or "major.minor" version into integers.
func parseVersion(v string) (maj, min int, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 2)
	maj, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	if len(parts) == 2 {
		if min, err = strconv.Atoi(strings.TrimSpace(parts[1])); err != nil {
			return 0, 0, false
		}
	}
	return maj, min, true
}

// executeActions maps the notification's generic actions onto Action.Execute
// buttons. Each button's data is the {decision, approval_id} object the inbound
// receiver (hitl.go parseTeams) reads — derived from the action's ID/Value with
// the same "decision:approval_id" packing the Slack originator uses, so both
// channels feed the identical governed decision. A malformed action (no id and no
// value) is skipped rather than rendered as a button that does nothing.
func executeActions(n sdk.Notification) []executeAction {
	out := make([]executeAction, 0, len(n.Actions))
	for _, a := range n.Actions {
		if a.ID == "" && a.Value == "" {
			continue
		}
		out = append(out, executeAction{
			Type:  "Action.Execute",
			Title: clampTitle(a.Label),
			Verb:  actionVerb(a),
			Data:  decisionData(a),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decisionData derives the Action.Execute data object the inbound receiver
// parses: {decision, approval_id}. It mirrors the receiver's decisionAndID — the
// value is "decision:approval_id" (the decision wins, the suffix is the id); a
// value with no ":" leaves the decision to the action id. The "olivares_" prefix
// is stripped so the data is clean (the receiver normalizes either way).
func decisionData(a sdk.NotificationAction) map[string]string {
	decision := a.ID
	approvalID := a.Value
	if i := strings.Index(a.Value, ":"); i >= 0 {
		decision = a.Value[:i]
		approvalID = a.Value[i+1:]
	}
	decision = strings.TrimPrefix(strings.TrimSpace(decision), "olivares_")
	data := map[string]string{}
	if decision != "" {
		data["decision"] = decision
	}
	if approvalID != "" {
		data["approval_id"] = strings.TrimSpace(approvalID)
	}
	if len(data) == 0 {
		return nil
	}
	return data
}

// actionVerb returns a non-empty verb for the Action.Execute (Teams identifies
// actions by verb; the receiver routes by data, not verb). It uses the action id,
// falling back to a stable default so the card is always well-formed.
func actionVerb(a sdk.NotificationAction) string {
	if v := strings.TrimSpace(a.ID); v != "" {
		return v
	}
	return "olivares_decision"
}

// clampTitle caps a button label at maxActionTitleLen runes (not bytes) so
// multi-byte text is never split; an over-long label is truncated, never dropped.
func clampTitle(s string) string {
	r := []rune(s)
	if len(r) <= maxActionTitleLen {
		return s
	}
	return string(r[:maxActionTitleLen])
}
