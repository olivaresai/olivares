// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package teams is the Olivares AI output connector that delivers notifications to
// Microsoft Teams as an Adaptive Card through a Power Automate "Workflows" incoming
// webhook (the trigger "When a Teams webhook request is received"). It implements
// sdk.OutputConnector: each Notify renders the non-sensitive Notification into an
// Adaptive Card wrapped in the Workflows message envelope and POSTs it through the
// shared reliable-delivery transport (internal/delivery).
//
// Why Workflows, not the classic connector. The legacy Office 365 "Incoming Webhook"
// connector and its MessageCard payload are RETIRED by Microsoft; new
// MessageCard cards no longer render their buttons. The only supported channel-post
// path is a Power Automate Workflow (or Microsoft Graph). This connector therefore
// targets the Workflows webhook and sends an Adaptive Card — the interface
// (OutputConnector) is unchanged; only the destination and the body shape moved.
//
// Honest interactivity constraint (§6, primary-source verified). A card
// posted through a plain Workflows incoming webhook is ONE-WAY: a button click cannot
// return to this service. A true Adaptive Card round-trip needs a registered Teams bot
// receiving the Action.Execute Invoke activity — separate infrastructure (a Bot
// Framework app + Microsoft Entra JWT validation). So for the approval round-trip this
// connector emits Action.OpenUrl buttons that deep-link the approver to a URL the
// operator controls (the governed console, or a signed callback the operator's own
// Workflow posts back to the HITL inbound receiver). It never renders an Action.Execute
// button it cannot service, which would silently do nothing.
//
// Minimal-data / credential handling (docs/SECURITY-HARDENING.md-3): the only secret is the
// Workflows webhook URL itself (it embeds an unguessable token). It is declared Secret,
// held in memory only, and never logged — the delivery transport never logs request
// bodies, headers or URLs, and this package never puts the webhook URL into an error.
// Only the non-sensitive Notification fields reach the wire. It imports only the SDK
// and the Apache delivery transport, never the engine.
package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.teams"

// Default configuration values.
const (
	defaultMaxAttempts = 4
	defaultCardVersion = "1.4"
	adaptiveCardType   = "application/vnd.microsoft.card.adaptive"
	adaptiveSchema     = "http://adaptivecards.io/schemas/adaptive-card.json"
)

// Config field keys.
const (
	cfgWebhookURL  = "webhook_url"
	cfgMaxAttempts = "max_attempts"
	cfgCardVersion = "card_version"
)

// Action-URL field keys: a notification carries an approve/deny/open deep-link in its
// non-sensitive Fields, which the connector renders as Action.OpenUrl buttons.
const (
	fieldApproveURL = "approve_url"
	fieldDenyURL    = "deny_url"
	fieldURL        = "url"
)

// containerStyle returns the Adaptive Card container style that accents a severity
// (good/warning/attention/accent are the styles every Teams client renders). Unknown/
// empty severities fall through to the calm "good" rather than guessing "attention".
func containerStyle(s model.Severity) string {
	switch {
	case s.AtLeast(model.SeverityHigh):
		return "attention"
	case s.AtLeast(model.SeverityMedium):
		return "warning"
	default:
		return "good"
	}
}

// Output is the Microsoft Teams output connector. A single instance is opened once and
// used for every Notify; it holds the resolved Workflows webhook URL (secret, in memory
// only) and the reliable-delivery transport.
type Output struct {
	webhookURL  string
	cardVersion string
	maxAttempts int
	client      *delivery.Client
	doer        delivery.Doer    // optional injected transport (tests); nil => default
	opts        delivery.Options // optional injected delivery policy (tests)
}

var _ sdk.OutputConnector = (*Output)(nil)

// New returns a Teams output connector with default configuration.
func New() *Output {
	return &Output{maxAttempts: defaultMaxAttempts, cardVersion: defaultCardVersion}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.2.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Microsoft Teams",
		Description: "Delivers notifications to a Microsoft Teams channel as an Adaptive Card via a Power Automate Workflows incoming webhook (the supported replacement for the retired Office 365 connector).",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgWebhookURL, Type: sdk.FieldString, Required: true, Secret: true, Description: "Power Automate Workflows incoming-webhook URL ('When a Teams webhook request is received'). Held in memory only; never logged or persisted."},
			{Key: cfgCardVersion, Type: sdk.FieldString, Default: defaultCardVersion, Description: "Adaptive Card schema version to emit (e.g. 1.4)."},
			{Key: cfgMaxAttempts, Type: sdk.FieldInt, Default: "4", Description: "Total delivery attempts per notification (including the first). Transient 429/5xx are retried with backoff."},
		},
	}
}

// Open resolves configuration and builds the reliable-delivery client. The webhook URL
// is required: an output connector with no destination cannot deliver.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.webhookURL = cfg.Get(cfgWebhookURL)
	if o.webhookURL == "" {
		return errors.New("teams: webhook_url is required")
	}
	if v := cfg.Get(cfgCardVersion); v != "" {
		o.cardVersion = v
	}
	o.maxAttempts = cfg.GetInt(cfgMaxAttempts, o.maxAttempts)

	opts := o.opts
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = o.maxAttempts
	}
	o.client = delivery.New(o.doer, opts)
	return nil
}

// Notify renders n as an Adaptive Card in the Workflows message envelope and delivers
// it. A successful Workflows post answers with HTTP 202 Accepted; the delivery
// transport treats any 2xx as success. A non-2xx (or a persistent transport failure)
// surfaces as an error that never echoes the secret webhook URL.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return errors.New("teams: Notify called before Open")
	}
	body, err := o.renderEnvelope(n)
	if err != nil {
		return fmt.Errorf("teams: render card: %w", err)
	}
	res, err := o.client.Send(ctx, delivery.Request{
		URL:    o.webhookURL,
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   body,
	})
	if err != nil {
		// SECURITY: do NOT wrap the delivery error verbatim — for Teams the webhook URL
		// is itself the secret credential and delivery's error string embeds the request
		// URL. Surface only the non-sensitive outcome (status, attempt count, bounded
		// body excerpt) so the webhook secret never reaches a log or the engine.
		if res.StatusCode != 0 {
			return fmt.Errorf("teams: delivery failed after %d attempt(s): status %d: %s", res.Attempts, res.StatusCode, res.Body)
		}
		return fmt.Errorf("teams: delivery failed after %d attempt(s): no response from webhook", res.Attempts)
	}
	return nil
}

// Close releases resources; this connector holds none beyond the in-memory URL.
func (o *Output) Close(context.Context) error { return nil }

// --- Workflows message envelope + Adaptive Card ------------------------------------

// workflowsMessage is the body a Workflows incoming webhook expects: a message with an
// attachments array carrying the Adaptive Card inline (NOT stringified, unlike Graph).
type workflowsMessage struct {
	Type        string       `json:"type"`
	Attachments []attachment `json:"attachments"`
}

type attachment struct {
	ContentType string       `json:"contentType"`
	ContentURL  *string      `json:"contentUrl"`
	Content     adaptiveCard `json:"content"`
}

// adaptiveCard is the minimal Adaptive Card we emit.
type adaptiveCard struct {
	Schema  string       `json:"$schema"`
	Type    string       `json:"type"`
	Version string       `json:"version"`
	Body    []any        `json:"body"`
	Actions []cardAction `json:"actions,omitempty"`
}

type cardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// renderEnvelope builds the Workflows message envelope wrapping an Adaptive Card. The
// card carries the title, the body, a deterministic FactSet of the structured fields
// (plus severity and tenant), and any approve/deny/open deep-links as Action.OpenUrl
// buttons.
func (o *Output) renderEnvelope(n sdk.Notification) ([]byte, error) {
	card := adaptiveCard{
		Schema:  adaptiveSchema,
		Type:    "AdaptiveCard",
		Version: o.cardVersion,
		Body:    []any{cardContainer(n)},
		Actions: actions(n),
	}
	msg := workflowsMessage{
		Type:        "message",
		Attachments: []attachment{{ContentType: adaptiveCardType, ContentURL: nil, Content: card}},
	}
	return json.Marshal(msg)
}

// cardContainer builds the styled Adaptive Card content container shared by the
// Workflows path (renderEnvelope) and the registered-bot Action.Execute path
// (botcard.go): the title, the body, and a deterministic FactSet of the
// structured fields, wrapped in a severity-accented Container.
func cardContainer(n sdk.Notification) map[string]any {
	body := []any{}
	if n.Title != "" {
		body = append(body, map[string]any{
			"type": "TextBlock", "text": n.Title, "weight": "bolder", "size": "large", "wrap": true,
		})
	}
	if n.Body != "" {
		body = append(body, map[string]any{
			"type": "TextBlock", "text": n.Body, "wrap": true,
		})
	}
	if facts := factSet(n); len(facts) > 0 {
		body = append(body, map[string]any{"type": "FactSet", "facts": facts})
	}
	return map[string]any{
		"type": "Container", "style": containerStyle(n.Severity), "bleed": true, "items": body,
	}
}

// factSet builds a deterministic Adaptive Card FactSet: the caller-supplied Fields in
// sorted key order (excluding the action-URL keys, which become buttons), followed by
// severity and tenant when present.
func factSet(n sdk.Notification) []map[string]string {
	actionKeys := map[string]bool{fieldApproveURL: true, fieldDenyURL: true, fieldURL: true}
	keys := make([]string, 0, len(n.Fields))
	for k := range n.Fields {
		if actionKeys[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	facts := make([]map[string]string, 0, len(keys)+2)
	for _, k := range keys {
		facts = append(facts, map[string]string{"title": k, "value": n.Fields[k]})
	}
	if n.Severity != "" {
		facts = append(facts, map[string]string{"title": "severity", "value": string(n.Severity)})
	}
	if n.Tenant != "" {
		facts = append(facts, map[string]string{"title": "tenant", "value": n.Tenant})
	}
	return facts
}

// actions builds the Action.OpenUrl buttons from the notification's action-URL fields.
// Only Action.OpenUrl is used: a Workflows-posted card cannot service an Action.Execute
// (no bot is registered), so emitting one would render a button that silently does
// nothing — instead the approver is deep-linked to a URL the operator controls.
func actions(n sdk.Notification) []cardAction {
	var out []cardAction
	if u := n.Fields[fieldApproveURL]; u != "" {
		out = append(out, cardAction{Type: "Action.OpenUrl", Title: "Approve", URL: u})
	}
	if u := n.Fields[fieldDenyURL]; u != "" {
		out = append(out, cardAction{Type: "Action.OpenUrl", Title: "Deny", URL: u})
	}
	if u := n.Fields[fieldURL]; u != "" {
		out = append(out, cardAction{Type: "Action.OpenUrl", Title: "Open", URL: u})
	}
	return out
}
