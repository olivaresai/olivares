// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package datadog is the Olivares AI output connector that ships audit/governance
// notifications to Datadog through the Logs Intake API v2 (the POST /api/v2/logs
// endpoint). It turns one sdk.Notification into a single Datadog log entry,
// projecting the notification's displayable fields onto Datadog's reserved log
// attributes (message, ddsource, service, hostname, ddtags) and carrying the
// remaining structural fields under a non-reserved "olivares" object so they stay
// searchable without colliding with Datadog's reserved names.
//
// It is minimal-data (docs/SECURITY-HARDENING.md): it forwards only the already-displayable
// Notification fields and adds no enrichment. The Datadog API key is the single
// operator credential — declared as a Secret config field, held in memory only,
// sent ONLY in the "DD-API-KEY" request header (never the body), and NEVER logged
// or wrapped into an error. The shared connectors/internal/delivery transport
// handles within-call retry of transient failures (network, 429, 5xx, honoring
// Retry-After) and never logs the request body or headers, so the key cannot leak
// through a diagnostic. A terminal 4xx (e.g. 400 bad payload, 403 bad key) is
// surfaced as an error without retry. Datadog accepts a valid batch with HTTP 202.
//
// It imports only the SDK and the shared delivery transport — never the engine.
package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.datadog"

// Default configuration values.
const (
	defaultSite        = "datadoghq.com"
	defaultSource      = "olivares"
	defaultService     = "olivares-control-plane"
	defaultMaxAttempts = 4
	// logsPath is the Logs Intake API v2 path appended to the resolved intake host.
	logsPath = "/api/v2/logs"
	// apiKeyHeader is the EXACT header Datadog authenticates the intake request with
	// (not Authorization). The key value is set here and nowhere else.
	apiKeyHeader = "DD-API-KEY"
)

// validSites is the set of Datadog intake sites the connector accepts for the
// "site" config. An explicit "endpoint" override bypasses this (testing); without
// it the intake host is built as "https://http-intake.logs."+site.
var validSites = map[string]struct{}{
	"datadoghq.com":     {},
	"datadoghq.eu":      {},
	"us3.datadoghq.com": {},
	"us5.datadoghq.com": {},
	"ap1.datadoghq.com": {},
	"ap2.datadoghq.com": {},
	"ddog-gov.com":      {},
	"us2.ddog-gov.com":  {},
}

// Output is the Datadog Logs output connector. It satisfies sdk.OutputConnector:
// Open validates config and builds the reliable-delivery client and the resolved
// /api/v2/logs target; Notify turns one Notification into a single log entry and
// delivers it; Close releases nothing.
type Output struct {
	endpoint string // resolved target URL ending in /api/v2/logs
	apiKey   string // operator credential — memory only, header only
	source   string // ddsource
	service  string // service
	hostname string // hostname (omitted when empty)
	tags     string // operator-supplied extra ddtags (comma-separated), may be empty

	maxAttempts int
	client      *delivery.Client
	doer        delivery.Doer // optional injected transport (tests); nil => default
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a Datadog Logs output connector with default configuration.
func New() *Output {
	return &Output{
		source:      defaultSource,
		service:     defaultService,
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
		Title:       "Datadog Logs",
		Description: "Delivers notifications to Datadog via the Logs Intake API v2 (POST /api/v2/logs).",
		ConfigFields: []sdk.ConfigField{
			{Key: "site", Type: sdk.FieldString, Default: defaultSite, Description: "Datadog site (e.g. datadoghq.com, datadoghq.eu, us3.datadoghq.com). The intake host is https://http-intake.logs.<site>."},
			{Key: "endpoint", Type: sdk.FieldString, Description: "Full intake URL override (e.g. for testing). When set, POST goes here instead of the site-derived host."},
			{Key: "api_key", Type: sdk.FieldString, Required: true, Secret: true, Description: "Datadog API key, sent as the 'DD-API-KEY' header. Held in memory only, never persisted or logged."},
			{Key: "source", Type: sdk.FieldString, Default: defaultSource, Description: "Value for the reserved 'ddsource' attribute."},
			{Key: "service", Type: sdk.FieldString, Default: defaultService, Description: "Value for the reserved 'service' attribute."},
			{Key: "hostname", Type: sdk.FieldString, Description: "Value for the reserved 'hostname' attribute (omitted when empty)."},
			{Key: "tags", Type: sdk.FieldString, Description: "Extra comma-separated key:value ddtags appended to the derived severity/tenant/type tags."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total delivery attempts including the first (transient failures only)."},
		},
	}
}

// Open reads and validates configuration and builds the reusable delivery client.
// The API key is required: without it the connector cannot authenticate, so Open
// fails fast rather than deferring the error to Notify. The intake endpoint is
// resolved here once (explicit override, else the site-derived host).
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.apiKey = cfg.Get("api_key")
	if o.apiKey == "" {
		return fmt.Errorf("datadog: api_key is required")
	}

	endpoint, err := resolveEndpoint(cfg.Get("endpoint"), cfg.Get("site"))
	if err != nil {
		return err
	}
	o.endpoint = endpoint

	if v := cfg.Get("source"); v != "" {
		o.source = v
	}
	if v := cfg.Get("service"); v != "" {
		o.service = v
	}
	o.hostname = strings.TrimSpace(cfg.Get("hostname"))
	o.tags = strings.TrimSpace(cfg.Get("tags"))

	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)
	o.client = delivery.New(o.doer, delivery.Options{MaxAttempts: o.maxAttempts})
	return nil
}

// resolveEndpoint determines the POST target. An explicit endpoint override wins
// verbatim (its /api/v2/logs path is the caller's responsibility — the test sets it
// to <httptest URL>+"/api/v2/logs"). Otherwise the intake host is built from a
// validated site as "https://http-intake.logs.<site>"+logsPath.
func resolveEndpoint(endpoint, site string) (string, error) {
	if e := strings.TrimSpace(endpoint); e != "" {
		return e, nil
	}
	site = strings.TrimSpace(site)
	if site == "" {
		site = defaultSite
	}
	if _, ok := validSites[site]; !ok {
		return "", fmt.Errorf("datadog: unknown site %q", site)
	}
	return "https://http-intake.logs." + site + logsPath, nil
}

// logEntry is one Datadog Logs Intake v2 entry. Only the five reserved attributes
// named by the API are populated; the notification's structural fields ride under
// the non-reserved "olivares" object so they are searchable without colliding with
// a reserved name. omitempty drops Hostname when unset (the only optional reserved
// attribute); Message is required and is guaranteed non-empty by message().
type logEntry struct {
	Message  string            `json:"message"`
	DDSource string            `json:"ddsource,omitempty"`
	Service  string            `json:"service,omitempty"`
	Hostname string            `json:"hostname,omitempty"`
	DDTags   string            `json:"ddtags,omitempty"`
	Olivares map[string]string `json:"olivares,omitempty"`
}

// Notify builds a single log entry from n and delivers it as a one-element JSON
// array. It returns nil on a 2xx (Datadog returns 202 Accepted on success) and an
// error on a terminal 4xx (surfaced by delivery, e.g. 400/403) or after exhausting
// transient retries. The API key never appears in a log or in the returned error.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("datadog: Notify called before Open")
	}

	// The intake accepts an array of log objects; we send exactly one.
	body, err := json.Marshal([]logEntry{o.buildEntry(n)})
	if err != nil {
		// Marshaling strings + a map[string]string cannot realistically fail; surface
		// it rather than panic, and do not retry — it is deterministic.
		return fmt.Errorf("datadog: marshal log entry: %w", err)
	}

	_, err = o.client.Send(ctx, delivery.Request{
		URL: o.endpoint,
		Header: map[string]string{
			"Content-Type": "application/json",
			apiKeyHeader:   o.apiKey,
		},
		Body: body,
	})
	if err != nil {
		// delivery already redacts: its error carries only status + a bounded body
		// excerpt, never the request headers (which hold the API key).
		return fmt.Errorf("datadog: deliver log: %w", err)
	}
	// A 2xx is the only success signal Datadog gives for the intake; there is no
	// logical-error body to inspect on success. A 4xx/5xx is already surfaced above.
	return nil
}

// Close releases resources; this connector holds none beyond the stateless
// delivery client.
func (o *Output) Close(context.Context) error { return nil }

// buildEntry assembles the Datadog log entry from a Notification: the reserved
// attributes plus the "olivares" object of structural fields.
func (o *Output) buildEntry(n sdk.Notification) logEntry {
	return logEntry{
		Message:  message(n),
		DDSource: o.source,
		Service:  o.service,
		Hostname: o.hostname,
		DDTags:   o.ddTags(n),
		Olivares: olivaresFields(n),
	}
}

// message returns the reserved (required, non-empty) "message" attribute: the
// Title joined to the Body as "Title — Body", falling back to the Title alone, then
// to the Body alone, then to a stable placeholder — so the message is never empty
// (Datadog rejects an empty message).
func message(n sdk.Notification) string {
	title := strings.TrimSpace(n.Title)
	body := strings.TrimSpace(n.Body)
	switch {
	case title != "" && body != "":
		return title + " — " + body
	case title != "":
		return title
	case body != "":
		return body
	default:
		return "olivares notification"
	}
}

// ddTags builds the comma-separated "ddtags" string: "severity:<label>" when the
// severity is known, "tenant:<tenant>" when set, "type:<Type>" when set, then any
// operator-supplied extra tags appended verbatim. Order is deterministic.
func (o *Output) ddTags(n sdk.Notification) string {
	var tags []string
	if label := severityLabel(n.Severity); label != "" {
		tags = append(tags, "severity:"+label)
	}
	if n.Tenant != "" {
		tags = append(tags, "tenant:"+n.Tenant)
	}
	if n.Type != "" {
		tags = append(tags, "type:"+n.Type)
	}
	if o.tags != "" {
		tags = append(tags, o.tags)
	}
	return strings.Join(tags, ",")
}

// olivaresFields returns the structural fields placed under the non-reserved
// "olivares" object: the notification's Fields plus the severity label when known.
// Keys are copied (the input map is never mutated); an empty result yields nil so
// the object is omitted. A field named "severity" wins over the derived label.
func olivaresFields(n sdk.Notification) map[string]string {
	out := make(map[string]string, len(n.Fields)+1)
	if label := severityLabel(n.Severity); label != "" {
		out["severity"] = label
	}
	for _, k := range sortedKeys(n.Fields) {
		out[k] = n.Fields[k]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortedKeys returns the map's keys in deterministic order (empty keys dropped) so
// the emitted object is stable regardless of Go's map iteration order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// severityLabel maps the shared severity scale onto the product's lowercase label
// (info/low/medium/high/critical), returning "" for an empty/unknown severity.
// model.Severity already carries these exact lowercase values; this switch is an
// explicit allow-list (not a competing scale) so an unknown value yields "" rather
// than leaking a raw, unvalidated string into a tag or attribute.
func severityLabel(s model.Severity) string {
	switch s {
	case model.SeverityInfo:
		return "info"
	case model.SeverityLow:
		return "low"
	case model.SeverityMedium:
		return "medium"
	case model.SeverityHigh:
		return "high"
	case model.SeverityCritical:
		return "critical"
	default:
		return ""
	}
}
