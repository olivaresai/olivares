// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package slack is the Olivares AI output connector that delivers notifications
// to Slack, either through an Incoming Webhook or through the Web API
// (chat.postMessage with a bot token). It satisfies sdk.OutputConnector and
// rides on the shared internal/delivery transport for backoff, jitter and
// honored Retry-After.
//
// Minimal-data (docs/SECURITY-HARDENING.md): it forwards only the non-sensitive Notification
// fields — Title, Body, Severity and the structured Fields map — never a secret.
// The operator credential (webhook_url or bot_token) is held in memory only,
// declared Secret in the descriptor, and never logged or embedded in an error:
// the delivery transport never logs bodies or headers, the webhook URL is the
// request URL (which delivery does not log), and the bot token only ever appears
// in the Authorization header. Slack's logical-error path (HTTP 200 with
// {"ok":false}) is inspected after a successful send and surfaced as an error
// carrying only the Slack error code, never the token.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.slack"

// Delivery modes.
const (
	modeWebhook = "webhook"
	modeAPI     = "api"
)

// Default configuration values.
const (
	defaultMode        = modeWebhook
	defaultMaxAttempts = 4

	postMessageURL = "https://slack.com/api/chat.postMessage"
)

// Severity attachment colors (Slack accepts the named values good/warning/danger
// and any hex). We use hex so the rendering is identical regardless of theme.
const (
	colorCritical = "#d00000" // critical/high -> danger red
	colorMedium   = "#f2c744" // medium -> warning amber
	colorLow      = "#36a64f" // low/info -> good green
)

// Output is the Slack output connector. A single instance is configured once in
// Open and then delivers one Notification per Notify call.
type Output struct {
	mode       string
	webhookURL string // Secret; incoming-webhook endpoint (webhook mode)
	botToken   string // Secret; xoxb- token (api mode)
	channel    string // api mode target channel
	username   string // optional display username (webhook payload)

	maxAttempts int
	doer        delivery.Doer    // optional injected transport (tests); nil => http.DefaultClient
	client      *delivery.Client // built in Open
}

// Compile-time proof that Output satisfies the SDK output contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a Slack output connector with default configuration.
func New() *Output {
	return &Output{
		mode:        defaultMode,
		maxAttempts: defaultMaxAttempts,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Slack",
		Description: "Delivers notifications to Slack via Incoming Webhook or the Web API (chat.postMessage).",
		ConfigFields: []sdk.ConfigField{
			{Key: "mode", Type: sdk.FieldString, Default: defaultMode, Description: "Delivery mode: webhook (Incoming Webhook) or api (Web API chat.postMessage)."},
			{Key: "webhook_url", Type: sdk.FieldString, Secret: true, Description: "Slack Incoming Webhook URL (webhook mode). Held in memory only, never logged."},
			{Key: "bot_token", Type: sdk.FieldString, Secret: true, Description: "Slack bot token (xoxb-...) for api mode. Held in memory only, never logged."},
			{Key: "channel", Type: sdk.FieldString, Description: "Target channel id or name (api mode), e.g. #alerts or C0123456789."},
			{Key: "username", Type: sdk.FieldString, Description: "Optional display username for the message (webhook mode)."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total delivery attempts including the first (1 = no retry)."},
		},
	}
}

// Open reads configuration, validates the mode's required credential and builds
// the reliable-delivery client over the (optionally injected) transport. It fails
// fast on a misconfiguration: webhook mode requires webhook_url; api mode requires
// both bot_token and channel.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	if v := cfg.Get("mode"); v != "" {
		o.mode = v
	}
	o.webhookURL = cfg.Get("webhook_url")
	o.botToken = cfg.Get("bot_token")
	o.channel = cfg.Get("channel")
	o.username = cfg.Get("username")
	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)

	switch o.mode {
	case modeWebhook:
		if o.webhookURL == "" {
			return fmt.Errorf("slack: webhook mode requires webhook_url")
		}
	case modeAPI:
		if o.botToken == "" || o.channel == "" {
			return fmt.Errorf("slack: api mode requires bot_token and channel")
		}
	default:
		return fmt.Errorf("slack: unknown mode %q (want %q or %q)", o.mode, modeWebhook, modeAPI)
	}

	o.client = delivery.New(o.doer, delivery.Options{MaxAttempts: o.maxAttempts})
	return nil
}

// Notify builds a Slack message from the notification and delivers it. In webhook
// mode it POSTs the payload to the configured webhook (Slack returns 200 with the
// body "ok"). In api mode it POSTs to chat.postMessage with a bearer token; Slack
// returns HTTP 200 with {"ok":bool,...}, so after a successful transport-level
// send the body is decoded and a logical failure (ok==false) is surfaced as an
// error carrying the Slack error code (never the token).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("slack: connector not opened")
	}
	switch o.mode {
	case modeAPI:
		return o.notifyAPI(ctx, n)
	default:
		return o.notifyWebhook(ctx, n)
	}
}

// notifyWebhook delivers via an Incoming Webhook.
func (o *Output) notifyWebhook(ctx context.Context, n sdk.Notification) error {
	payload := o.webhookPayload(n)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack: marshal payload: %w", err)
	}
	res, err := o.client.Send(ctx, delivery.Request{
		URL:    o.webhookURL,
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   body,
	})
	if err != nil {
		// The webhook URL IS the secret credential and delivery interpolates the
		// request URL into its error string — so we must NOT wrap that error with
		// %w (it would carry the URL out of Notify). Surface only the status and
		// a bounded body excerpt, both of which are non-sensitive.
		return fmt.Errorf("slack: webhook delivery failed: status %d: %s", res.StatusCode, res.Body)
	}
	return nil
}

// notifyAPI delivers via chat.postMessage with a bot token.
func (o *Output) notifyAPI(ctx context.Context, n sdk.Notification) error {
	payload := o.apiPayload(n)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack: marshal payload: %w", err)
	}
	res, err := o.client.Send(ctx, delivery.Request{
		URL: postMessageURL,
		Header: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + o.botToken,
		},
		Body: body,
	})
	if err != nil {
		return fmt.Errorf("slack: api delivery failed: %w", err)
	}
	// Slack's Web API returns 200 even for logical errors; the truth is in the body.
	var sr slackAPIResponse
	if jerr := json.Unmarshal([]byte(res.Body), &sr); jerr != nil {
		return fmt.Errorf("slack: api returned unparseable body (status %d)", res.StatusCode)
	}
	if !sr.OK {
		errCode := sr.Error
		if errCode == "" {
			errCode = "unknown_error"
		}
		return fmt.Errorf("slack: api rejected message: %s", errCode)
	}
	return nil
}

// slackAPIResponse is the minimal shape of a chat.postMessage response. We never
// echo back the token or any operator-supplied credential — only ok/error.
type slackAPIResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// webhookMessage is the Incoming Webhook payload shape.
type webhookMessage struct {
	Text        string       `json:"text"`
	Username    string       `json:"username,omitempty"`
	Attachments []attachment `json:"attachments,omitempty"`
	Blocks      []block      `json:"blocks,omitempty"`
}

// apiMessage is the chat.postMessage request shape.
type apiMessage struct {
	Channel     string       `json:"channel"`
	Text        string       `json:"text"`
	Username    string       `json:"username,omitempty"`
	Attachments []attachment `json:"attachments,omitempty"`
	Blocks      []block      `json:"blocks,omitempty"`
}

// attachment is the (legacy but universally supported) Slack attachment we use to
// carry color-coded severity, the body text, and the structured fields.
type attachment struct {
	Color  string            `json:"color"`
	Text   string            `json:"text,omitempty"`
	Fields []attachmentField `json:"fields,omitempty"`
	TS     int64             `json:"ts,omitempty"`
}

// attachmentField is one key/value in an attachment's fields list.
type attachmentField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// webhookPayload builds the Incoming Webhook message. A notification carrying
// interactive Actions renders as Block Kit (an actions block of buttons fires to
// the app's interactivity URL); otherwise it keeps the legacy severity-colored
// attachment, unchanged.
func (o *Output) webhookPayload(n sdk.Notification) webhookMessage {
	msg := webhookMessage{Text: n.Title, Username: o.username}
	if len(n.Actions) > 0 {
		msg.Blocks = messageBlocks(n)
	} else {
		msg.Attachments = []attachment{o.attachment(n)}
	}
	return msg
}

// apiPayload builds the chat.postMessage message (same Block Kit vs attachment
// split as webhookPayload).
func (o *Output) apiPayload(n sdk.Notification) apiMessage {
	msg := apiMessage{Channel: o.channel, Text: n.Title, Username: o.username}
	if len(n.Actions) > 0 {
		msg.Blocks = messageBlocks(n)
	} else {
		msg.Attachments = []attachment{o.attachment(n)}
	}
	return msg
}

// attachment builds the single severity-colored attachment shared by both modes.
func (o *Output) attachment(n sdk.Notification) attachment {
	a := attachment{
		Color: colorFor(n.Severity),
		Text:  n.Body,
	}
	if !n.Time.IsZero() {
		a.TS = n.Time.Unix()
	}
	a.Fields = fieldsFor(n.Fields)
	return a
}

// fieldsFor turns the notification's structured map into attachment fields,
// sorted by key for a deterministic payload (Slack does not guarantee ordering
// and a stable order makes the message and the tests reproducible).
func fieldsFor(in map[string]string) []attachmentField {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]attachmentField, 0, len(keys))
	for _, k := range keys {
		out = append(out, attachmentField{Title: k, Value: in[k], Short: true})
	}
	return out
}

// sortStrings is a tiny insertion sort to avoid importing sort for one slice
// (stdlib sort is allowed; this keeps the dependency surface minimal and the
// behavior obvious for the small Fields maps a notification carries).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// --- Block Kit (interactive) rendering -------------------------------------
//
// When a Notification carries Actions, the connector renders Block Kit blocks
// instead of the legacy attachment: a section for the title/body, an optional
// section of the structured Fields, and an `actions` block of buttons. Each
// button's action_id and value are copied VERBATIM from the NotificationAction
// (ID and Value) — they are the exact shape the inbound receiver parses on a
// click, so the connector must never rewrite them. Buttons fire in messages
// posted either way: a click goes to the Slack app's configured interactivity
// Request URL, not to the message's origin (so webhook- and API-posted cards are
// equally interactive).

// block is the minimal Block Kit block subset we emit (section, actions).
type block struct {
	Type     string         `json:"type"`
	BlockID  string         `json:"block_id,omitempty"`
	Text     *textObject    `json:"text,omitempty"`     // section: primary text
	Fields   []textObject   `json:"fields,omitempty"`   // section: 2-column fields
	Elements []blockElement `json:"elements,omitempty"` // actions: the buttons
}

// textObject is a Block Kit composition text object ("mrkdwn" or "plain_text").
type textObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// blockElement is one interactive element; we only emit buttons.
type blockElement struct {
	Type     string      `json:"type"` // "button"
	Text     *textObject `json:"text,omitempty"`
	ActionID string      `json:"action_id,omitempty"`
	Value    string      `json:"value,omitempty"`
	Style    string      `json:"style,omitempty"` // "primary" | "danger"
}

// Slack hard limits we render within (Slack rejects the whole message otherwise).
const (
	maxSectionFields = 10 // a section's fields array
	maxButtons       = 25 // an actions block's elements
	maxButtonLabel   = 75 // a button's plain_text label, in runes
)

// messageBlocks renders a notification as Block Kit: a header section, an
// optional fields section, and the actions block of buttons.
func messageBlocks(n sdk.Notification) []block {
	var bs []block

	header := "*" + escapeMrkdwn(n.Title) + "*"
	if n.Body != "" {
		header += "\n" + escapeMrkdwn(n.Body)
	}
	bs = append(bs, block{Type: "section", Text: &textObject{Type: "mrkdwn", Text: header}})

	if fs := blockFields(n.Fields); len(fs) > 0 {
		bs = append(bs, block{Type: "section", Fields: fs})
	}
	if els := buttonElements(n.Actions); len(els) > 0 {
		bs = append(bs, block{Type: "actions", BlockID: "olivares_actions", Elements: els})
	}
	return bs
}

// blockFields renders the structured Fields as 2-column mrkdwn section fields,
// sorted by key for a deterministic payload. Slack caps a section at 10 fields;
// beyond that the connector renders the first nine and a VISIBLE "+N more" marker
// rather than silently dropping the rest (or letting Slack reject all ten-plus).
func blockFields(in map[string]string) []textObject {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]textObject, 0, len(keys))
	for _, k := range keys {
		if len(keys) > maxSectionFields && len(out) == maxSectionFields-1 {
			out = append(out, textObject{Type: "mrkdwn", Text: fmt.Sprintf("_+%d more_", len(keys)-(maxSectionFields-1))})
			break
		}
		out = append(out, textObject{Type: "mrkdwn", Text: "*" + escapeMrkdwn(k) + "*\n" + escapeMrkdwn(in[k])})
	}
	return out
}

// buttonElements maps NotificationActions to Block Kit buttons. action_id and
// value are copied verbatim (the inbound receiver's contract); a malformed action
// (no label or id) is skipped, and only Slack's two recognized styles survive.
func buttonElements(actions []sdk.NotificationAction) []blockElement {
	if len(actions) == 0 {
		return nil
	}
	out := make([]blockElement, 0, len(actions))
	for _, a := range actions {
		if a.Label == "" || a.ID == "" {
			continue
		}
		if len(out) == maxButtons {
			break
		}
		el := blockElement{
			Type:     "button",
			Text:     &textObject{Type: "plain_text", Text: clampRunes(a.Label, maxButtonLabel)},
			ActionID: a.ID,
			Value:    a.Value,
		}
		if a.Style == "primary" || a.Style == "danger" {
			el.Style = a.Style
		}
		out = append(out, el)
	}
	return out
}

// escapeMrkdwn escapes the three characters Slack reserves in mrkdwn text so a
// value containing them renders literally (Slack docs: only &, < and >).
func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// clampRunes bounds s to n runes without splitting a multi-byte rune.
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// colorFor maps a severity to an attachment color. Critical and high map to the
// red danger color; medium to amber; low and info (and an empty/unknown severity)
// to green. The mapping fails closed to a calm color rather than guessing red.
func colorFor(s model.Severity) string {
	switch {
	case s.AtLeast(model.SeverityHigh):
		return colorCritical
	case s.AtLeast(model.SeverityMedium):
		return colorMedium
	default:
		return colorLow
	}
}

// Close releases resources; this connector holds none.
func (o *Output) Close(context.Context) error { return nil }
