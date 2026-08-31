// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package pagerduty is the Olivares AI output connector that delivers
// notifications to PagerDuty through the Events API v2 (the /v2/enqueue
// endpoint). It turns one sdk.Notification into a single "trigger" event,
// mapping the shared severity scale onto PagerDuty's (critical/error/warning/
// info) and carrying the notification's non-sensitive Title, Body, Tenant and
// Fields as the event payload and custom_details.
//
// It is minimal-data (docs/SECURITY-HARDENING.md-3): it forwards only the displayable
// Notification fields, never a secret. The integration's routing (integration)
// key is the single operator credential — it is declared as a Secret config
// field, held in memory only, sent as a JSON body field to PagerDuty, and is
// NEVER logged or persisted. The shared delivery.Client handles within-call
// retry of transient failures (network, 429, 5xx, honoring Retry-After) and
// never logs the request body or headers, so the routing key cannot leak
// through a diagnostic. A terminal 4xx (e.g. 400 bad payload) is surfaced as an
// error without retry. PagerDuty accepts a valid event with HTTP 202.
//
// It imports only the SDK, the shared delivery transport and the redact helper —
// never the engine.
package pagerduty

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.pagerduty"

// Default configuration values.
const (
	defaultEventsURL   = "https://events.pagerduty.com/v2/enqueue"
	defaultChangeURL   = "https://events.pagerduty.com/v2/change/enqueue"
	defaultSource      = "olivares-control-plane"
	defaultMaxAttempts = 4
	maxSummaryLen      = 1024
)

// Event-lifecycle actions (Events API v2 event_action) the connector understands,
// selected per-notification via Fields["event_action"]. trigger (the default) opens
// or re-asserts an alert; acknowledge and resolve advance an existing alert and
// REQUIRE the dedup_key of the alert they act on; change records a PagerDuty Change
// Event (a non-alerting deploy/config signal) on the dedicated change endpoint.
const (
	actionTrigger     = "trigger"
	actionAcknowledge = "acknowledge"
	actionResolve     = "resolve"
	actionChange      = "change"

	// fieldEventAction selects the lifecycle action; fieldDedupKey carries the alert
	// key acknowledge/resolve act on. Both ride the non-secret Notification.Fields.
	fieldEventAction = "event_action"
	fieldDedupKey    = "dedup_key"
)

// PagerDuty severities (Events API v2 payload.severity enum).
const (
	pdCritical = "critical"
	pdError    = "error"
	pdWarning  = "warning"
	pdInfo     = "info"
)

// Output is the PagerDuty output connector. It satisfies sdk.OutputConnector:
// Open builds the reliable-delivery client from the resolved config, Notify
// turns one Notification into a trigger event and delivers it, Close releases
// nothing.
type Output struct {
	routingKey  string
	source      string
	eventsURL   string
	changeURL   string
	maxAttempts int

	client *delivery.Client
	doer   delivery.Doer // optional injected transport (tests); nil => default
}

// Compile-time proof that Output satisfies the contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a PagerDuty output connector with default configuration.
func New() *Output {
	return &Output{
		source:      defaultSource,
		eventsURL:   defaultEventsURL,
		changeURL:   defaultChangeURL,
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
		Title:       "PagerDuty",
		Description: "Delivers notifications to PagerDuty via the Events API v2 (trigger events).",
		ConfigFields: []sdk.ConfigField{
			{Key: "routing_key", Type: sdk.FieldString, Required: true, Secret: true, Description: "PagerDuty Events API v2 integration/routing key (never persisted or logged)."},
			{Key: "source", Type: sdk.FieldString, Default: defaultSource, Description: "Value for payload.source identifying the emitting system."},
			{Key: "events_url", Type: sdk.FieldString, Default: defaultEventsURL, Description: "Events API v2 enqueue endpoint (override for testing)."},
			{Key: "change_events_url", Type: sdk.FieldString, Default: defaultChangeURL, Description: "Events API v2 change-events enqueue endpoint (override for testing)."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total delivery attempts including the first (transient failures only)."},
		},
	}
}

// Open reads configuration and builds the reliable-delivery client. The routing
// key is required: without it the connector cannot deliver, so Open fails fast
// rather than deferring the error to Notify.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.routingKey = cfg.Get("routing_key")
	if o.routingKey == "" {
		return fmt.Errorf("pagerduty: routing_key is required")
	}
	if v := cfg.Get("source"); v != "" {
		o.source = v
	}
	if v := cfg.Get("events_url"); v != "" {
		o.eventsURL = v
	}
	if v := cfg.Get("change_events_url"); v != "" {
		o.changeURL = v
	}
	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)

	o.client = delivery.New(o.doer, delivery.Options{MaxAttempts: o.maxAttempts})
	return nil
}

// event is the Events API v2 alert event. Only non-sensitive Notification fields
// populate it; the routing key authenticates the request as a body field. Payload is
// a pointer so an acknowledge/resolve event — which carries only the dedup_key and no
// payload — omits it entirely rather than sending an empty object.
type event struct {
	RoutingKey  string   `json:"routing_key"`
	EventAction string   `json:"event_action"`
	DedupKey    string   `json:"dedup_key,omitempty"`
	Payload     *payload `json:"payload,omitempty"`
}

type payload struct {
	Summary       string            `json:"summary"`
	Source        string            `json:"source"`
	Severity      string            `json:"severity"`
	Timestamp     string            `json:"timestamp,omitempty"`
	CustomDetails map[string]string `json:"custom_details,omitempty"`
}

// Notify delivers the notification to PagerDuty. The Events API v2 lifecycle action
// is selected by Fields["event_action"] (default trigger): trigger opens/re-asserts
// an alert; acknowledge and resolve advance an existing alert and require its
// dedup_key; change records a Change Event on the dedicated change endpoint. It
// returns nil on a 2xx (202 Accepted) and an error on a terminal 4xx or after
// exhausting transient retries. The routing key never appears in a log or the error.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("pagerduty: Notify called before Open")
	}
	switch eventAction(n) {
	case actionChange:
		return o.notifyChange(ctx, n)
	case actionAcknowledge:
		return o.notifyLifecycle(ctx, n, actionAcknowledge)
	case actionResolve:
		return o.notifyLifecycle(ctx, n, actionResolve)
	default:
		return o.notifyTrigger(ctx, n)
	}
}

// notifyTrigger sends a trigger event (opens or re-asserts an alert).
func (o *Output) notifyTrigger(ctx context.Context, n sdk.Notification) error {
	return o.enqueue(ctx, o.eventsURL, event{
		RoutingKey:  o.routingKey,
		EventAction: actionTrigger,
		DedupKey:    dedupKey(n),
		Payload: &payload{
			Summary:       summary(n),
			Source:        o.source,
			Severity:      pdSeverity(n.Severity),
			Timestamp:     timestamp(n.Time),
			CustomDetails: customDetails(n),
		},
	})
}

// notifyLifecycle sends an acknowledge or resolve event. These advance an EXISTING
// alert, so a dedup_key is mandatory — without it PagerDuty has no alert to act on,
// and silently triggering a new one would be wrong. A missing key is a terminal
// configuration error (the caller must thread the alert's dedup_key through Fields).
func (o *Output) notifyLifecycle(ctx context.Context, n sdk.Notification, action string) error {
	key := n.Fields[fieldDedupKey]
	if key == "" {
		return fmt.Errorf("pagerduty: %s requires a dedup_key (Fields[%q]) identifying the alert to act on", action, fieldDedupKey)
	}
	return o.enqueue(ctx, o.eventsURL, event{
		RoutingKey:  o.routingKey,
		EventAction: action,
		DedupKey:    key,
	})
}

// enqueue marshals and delivers an alert event to the Events API v2 enqueue endpoint,
// inspecting the 200-with-logical-error body. The routing key never reaches a log or
// the returned error.
func (o *Output) enqueue(ctx context.Context, url string, ev event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("pagerduty: marshal event: %w", err)
	}
	res, err := o.client.Send(ctx, delivery.Request{
		URL:    url,
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   body,
	})
	if err != nil {
		// delivery already redacts: its error carries only status + a bounded body
		// excerpt, never the request body (which holds the routing key).
		return fmt.Errorf("pagerduty: deliver event: %w", err)
	}
	if logicalErr := pagerDutyResultError(res); logicalErr != nil {
		return fmt.Errorf("pagerduty: event rejected: %w", logicalErr)
	}
	return nil
}

// changeEvent is the Events API v2 Change Event body (a non-alerting deploy/config
// signal on the dedicated /v2/change/enqueue endpoint). It carries no event_action and
// never opens an incident; only routing_key + payload.summary are required.
type changeEvent struct {
	RoutingKey string        `json:"routing_key"`
	Payload    changePayload `json:"payload"`
}

type changePayload struct {
	Summary       string            `json:"summary"`
	Source        string            `json:"source,omitempty"`
	Timestamp     string            `json:"timestamp,omitempty"`
	CustomDetails map[string]string `json:"custom_details,omitempty"`
}

// notifyChange records a PagerDuty Change Event on the dedicated change endpoint. A
// change event is informational (a deploy, a config change, a governance decision
// landing) and never pages anyone; it appears on the service timeline for context.
func (o *Output) notifyChange(ctx context.Context, n sdk.Notification) error {
	ev := changeEvent{
		RoutingKey: o.routingKey,
		Payload: changePayload{
			Summary:       summary(n),
			Source:        o.source,
			Timestamp:     timestamp(n.Time),
			CustomDetails: customDetails(n),
		},
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("pagerduty: marshal change event: %w", err)
	}
	res, err := o.client.Send(ctx, delivery.Request{
		URL:    o.changeURL,
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("pagerduty: deliver change event: %w", err)
	}
	if logicalErr := pagerDutyResultError(res); logicalErr != nil {
		return fmt.Errorf("pagerduty: change event rejected: %w", logicalErr)
	}
	return nil
}

// Close releases resources; this connector holds none.
func (o *Output) Close(context.Context) error { return nil }

// eventAction resolves the lifecycle action from Fields["event_action"], defaulting
// to trigger. An unrecognized value also defaults to trigger (a notification is, by
// nature, an alert) so a typo never silently drops the event.
func eventAction(n sdk.Notification) string {
	switch n.Fields[fieldEventAction] {
	case actionAcknowledge:
		return actionAcknowledge
	case actionResolve:
		return actionResolve
	case actionChange:
		return actionChange
	default:
		return actionTrigger
	}
}

// summary returns the alert summary: the Title, falling back to the Body when the
// Title is empty, truncated to maxSummaryLen runes (PagerDuty caps summary at
// 1024 chars). A wholly empty notification yields a stable placeholder so the
// event is never rejected for an empty summary.
func summary(n sdk.Notification) string {
	s := n.Title
	if s == "" {
		s = n.Body
	}
	if s == "" {
		s = "olivares notification"
	}
	return truncate(s, maxSummaryLen)
}

// truncate caps s at limit runes (not bytes) so multi-byte text is not split.
func truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}

// pdSeverity maps the shared severity scale onto PagerDuty's payload.severity
// enum. Anything not high/critical/medium (including empty/unknown) is "info".
func pdSeverity(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return pdCritical
	case model.SeverityHigh:
		return pdError
	case model.SeverityMedium:
		return pdWarning
	default: // SeverityLow, SeverityInfo, empty, or any unknown value
		return pdInfo
	}
}

// timestamp formats t as RFC3339 when set; an empty string omits the field so
// PagerDuty stamps the event with its receive time.
func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// dedupKey returns a stable de-duplication key so repeated triggers for the same
// underlying condition collapse onto one PagerDuty alert. It prefers an explicit
// caller-supplied n.Fields["dedup_key"]; otherwise it derives a stable hash from
// the event type and the summary line so two identical notifications dedup while
// distinct ones do not.
func dedupKey(n sdk.Notification) string {
	if k := n.Fields["dedup_key"]; k != "" {
		return k
	}
	title := n.Title
	if title == "" {
		title = n.Body
	}
	if n.Type == "" && title == "" {
		return "" // nothing stable to key on; let PagerDuty assign one
	}
	return redact.Hash(n.Type + "\x00" + title)
}

// customDetails copies the non-sensitive structured fields and enriches them with
// the tenant and body so the receiving on-call sees the full non-sensitive
// context. The explicit dedup_key field is dropped — it is conveyed at the event
// level, not as a detail. The input map is never mutated.
func customDetails(n sdk.Notification) map[string]string {
	out := make(map[string]string, len(n.Fields)+2)
	for k, v := range n.Fields {
		// dedup_key and event_action are connector-control metadata conveyed at the
		// event level, not customer-visible detail — drop them from custom_details.
		if k == fieldDedupKey || k == fieldEventAction {
			continue
		}
		out[k] = v
	}
	if n.Tenant != "" {
		out["tenant"] = n.Tenant
	}
	if n.Body != "" {
		out["body"] = n.Body
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pdResult is the success shape PagerDuty returns (HTTP 202): a status of
// "success" plus the assigned dedup_key. A logical rejection at HTTP 2xx (rare
// for Events API v2, but defended against) carries a non-"success" status and a
// message, which we surface as an error.
type pdResult struct {
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Errors   []string `json:"errors"`
	DedupKey string   `json:"dedup_key"`
}

// pagerDutyResultError inspects a successful (2xx) delivery body for a logical
// failure. PagerDuty's Events API v2 returns 202 with {"status":"success",...}
// on acceptance; an empty or unparseable body on a 2xx is treated as success
// (some proxies strip the body), so only an explicit non-"success" status is an
// error. The returned error never contains the routing key (the response body
// does not echo it).
func pagerDutyResultError(res delivery.Result) error {
	if res.Body == "" {
		return nil
	}
	var r pdResult
	if err := json.Unmarshal([]byte(res.Body), &r); err != nil {
		// Not JSON we recognize; the HTTP status already said 2xx, so accept it.
		return nil
	}
	if r.Status == "" || r.Status == "success" {
		return nil
	}
	msg := r.Message
	if msg == "" {
		msg = r.Status
	}
	if len(r.Errors) > 0 {
		return fmt.Errorf("status %q: %s: %v", r.Status, msg, r.Errors)
	}
	return fmt.Errorf("status %q: %s", r.Status, msg)
}
