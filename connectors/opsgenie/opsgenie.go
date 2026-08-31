// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package opsgenie is the Olivares AI output connector that delivers
// notifications to Atlassian Opsgenie through the Alerts API. It satisfies
// sdk.OutputConnector and rides on the shared reliable-delivery transport
// (internal/delivery): a single Notify becomes a bounded sequence of HTTP
// attempts that retries transient failures (network, 408/425/429, 5xx, honoring
// Retry-After) and gives up immediately on a terminal 4xx such as 422 (a
// malformed payload that will fail identically on retry).
//
// Alert lifecycle. The default Notify opens an alert (POST /v2/alerts). A
// notification may instead advance an EXISTING alert by setting
// Fields["action"]: "close" (POST /v2/alerts/{identifier}/close) or
// "acknowledge" (POST /v2/alerts/{identifier}/acknowledge). A lifecycle action
// acts on the alert named by Fields["alert_id"] (identifierType=id) or the
// de-dup Fields["alias"] (identifierType=alias); without either it is a terminal
// configuration error (closing "some alert" is never guessed — deny-closed). The
// alias is the de-dup key Opsgenie keeps unique among OPEN alerts, so a
// close-by-alias resolves the currently-open alert bearing that alias. Closing
// is processed asynchronously (HTTP 202 Accepted): a 202 is acceptance, not a
// proof of closure.
//
// It is minimal-data (docs/SECURITY-HARDENING.md-3): it forwards only the non-sensitive
// Notification fields (Title, Body, Severity, Tenant, Type and the Fields kv)
// as the alert message/description/priority/details. The operator's Opsgenie
// API key is declared as a Secret config field, is held in memory only on the
// Authorization header, and is NEVER logged or persisted — the delivery layer
// never logs request headers or bodies, and an error from Send carries only a
// status code and a bounded body excerpt, never the credential.
//
// It imports only the SDK and the internal delivery transport, never the
// engine, so it ships under Apache-2.0.
package opsgenie

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
const Name = "olivares.opsgenie"

// Default configuration values and protocol constants.
const (
	defaultRegion      = "us"
	usAlertsURL        = "https://api.opsgenie.com/v2/alerts"
	euAlertsURL        = "https://api.eu.opsgenie.com/v2/alerts"
	defaultMaxAttempts = 4

	// alertSource tags every alert with its originating system; it is a constant,
	// non-sensitive label, not operator data.
	alertSource = "olivares-control-plane"

	// maxMessageLen is the Opsgenie hard limit for the alert "message" field.
	// Longer titles are truncated so the API never rejects the payload.
	maxMessageLen = 130
)

// Output is the Opsgenie output connector. It holds the resolved alerts URL,
// the in-memory API key, and a reusable delivery client built in Open.
type Output struct {
	apiKey    string
	alertsURL string
	region    string
	attempts  int

	deliver *delivery.Client
	doer    delivery.Doer // optional injected transport (tests); nil => http.DefaultClient
}

// Compile-time proof that Output satisfies the SDK output contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns an Opsgenie output connector with default configuration.
func New() *Output {
	return &Output{
		region:   defaultRegion,
		attempts: defaultMaxAttempts,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Opsgenie",
		Description: "Delivers notifications to Atlassian Opsgenie via the Alerts API (POST /v2/alerts), and advances an existing alert with Fields[\"action\"]=close|acknowledge by its alias or id.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Required: true, Secret: true, Description: "Opsgenie API integration key (GenieKey). Held in memory only; never logged or persisted."},
			{Key: "region", Type: sdk.FieldString, Default: defaultRegion, Description: "Opsgenie region: us or eu. Selects the default Alerts API endpoint."},
			{Key: "alerts_url", Type: sdk.FieldString, Description: "Override for the Alerts API endpoint (defaults to the region's URL)."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total delivery attempts including the first (transient failures are retried with backoff)."},
		},
	}
}

// Open reads configuration, resolves the alerts URL from region (unless an
// explicit alerts_url override is given) and builds the reusable delivery
// client. A missing api_key is a configuration error reported here, not
// deferred to Notify.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.apiKey = cfg.Get("api_key")
	if o.apiKey == "" {
		return fmt.Errorf("opsgenie: api_key is required")
	}

	if v := cfg.Get("region"); v != "" {
		o.region = strings.ToLower(v)
	}
	switch o.region {
	case "us", "eu":
		// ok
	default:
		return fmt.Errorf("opsgenie: region %q invalid (want us or eu)", o.region)
	}

	o.alertsURL = cfg.Get("alerts_url")
	if o.alertsURL == "" {
		o.alertsURL = regionURL(o.region)
	}

	o.attempts = cfg.GetInt("max_attempts", o.attempts)

	o.deliver = delivery.New(o.doer, delivery.Options{MaxAttempts: o.attempts})
	return nil
}

// regionURL returns the default Alerts API endpoint for a region.
func regionURL(region string) string {
	if region == "eu" {
		return euAlertsURL
	}
	return usAlertsURL
}

// alertPayload is the JSON body of an Opsgenie create-alert request. Only
// non-sensitive Notification fields are populated.
type alertPayload struct {
	Message     string            `json:"message"`
	Description string            `json:"description,omitempty"`
	Priority    string            `json:"priority"`
	Alias       string            `json:"alias,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
	Source      string            `json:"source"`
}

// Notify delivers one notification to Opsgenie. The lifecycle action is selected
// by Fields["action"] (default create): create opens an alert; close and
// acknowledge advance an existing alert (by its alias or id) and require one. It
// returns nil on a 2xx (202 Accepted) and an error on a terminal 4xx or after
// exhausting transient retries. The api key never appears in a log or the error.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.deliver == nil {
		return fmt.Errorf("opsgenie: Notify called before Open")
	}
	switch alertAction(n) {
	case actionClose:
		return o.notifyLifecycle(ctx, n, actionClose)
	case actionAcknowledge:
		return o.notifyLifecycle(ctx, n, actionAcknowledge)
	default:
		return o.notifyCreate(ctx, n)
	}
}

// notifyCreate opens an alert (POST /v2/alerts). It builds the JSON payload from
// the non-sensitive Notification fields, POSTs it with the GenieKey Authorization
// header, and relies on the delivery client for retry. Opsgenie returns 202
// Accepted on success; a terminal 4xx (e.g. 422) is not retried and surfaces as
// an error.
func (o *Output) notifyCreate(ctx context.Context, n sdk.Notification) error {
	body, err := json.Marshal(o.buildPayload(n))
	if err != nil {
		return fmt.Errorf("opsgenie: marshal alert: %w", err)
	}

	res, err := o.deliver.Send(ctx, delivery.Request{
		URL: o.alertsURL,
		Header: map[string]string{
			"Authorization": "GenieKey " + o.apiKey,
			"Content-Type":  "application/json",
		},
		Body: body,
	})
	if err != nil {
		// The delivery error carries a status code and a bounded body excerpt,
		// never the credential; wrap it with the connector name for context.
		return fmt.Errorf("opsgenie: deliver alert (status %d): %w", res.StatusCode, err)
	}
	return nil
}

// buildPayload maps a Notification onto an Opsgenie alert. The message falls
// back to the Body when the Title is empty and is truncated to the Opsgenie
// limit; the priority is derived from the severity; the details merge the
// caller's Fields with the tenant and event type. Only non-sensitive fields are
// forwarded.
func (o *Output) buildPayload(n sdk.Notification) alertPayload {
	message := truncate(firstNonEmpty(n.Title, n.Body), maxMessageLen)

	details := make(map[string]string, len(n.Fields)+2)
	for k, v := range n.Fields {
		details[k] = v
	}
	if n.Tenant != "" {
		details["tenant"] = n.Tenant
	}
	if n.Type != "" {
		details["eventType"] = n.Type
	}
	if len(details) == 0 {
		details = nil
	}

	return alertPayload{
		Message:     message,
		Description: n.Body,
		Priority:    priorityFor(n.Severity),
		Alias:       n.Fields["alias"],
		Details:     details,
		Source:      alertSource,
	}
}

// Close releases resources; this connector holds none beyond the delivery
// client, which needs no teardown.
func (o *Output) Close(context.Context) error { return nil }

// priorityFor maps the shared severity scale onto Opsgenie's P1..P5 priorities.
// An empty/unknown severity is the lowest priority (P5) rather than a guess.
func priorityFor(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return "P1"
	case model.SeverityHigh:
		return "P2"
	case model.SeverityMedium:
		return "P3"
	case model.SeverityLow:
		return "P4"
	default:
		return "P5"
	}
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// truncate caps s to at most n bytes, preserving valid UTF-8 by trimming at a
// rune boundary. Opsgenie counts the message in characters; trimming on a rune
// boundary keeps the payload well-formed and within the limit.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Walk back to a rune boundary at or before n.
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// utf8RuneStart reports whether b is the first byte of a UTF-8 rune (i.e. not a
// continuation byte 0b10xxxxxx).
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
