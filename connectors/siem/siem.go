// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package siem is the Olivares AI output connector that ships notifications to a
// SIEM as the docs/SECURITY-HARDENING.md WORM / external-copy transport. It is the last hop in
// the notify path: an alert that has already been reduced to a minimal-data
// sdk.Notification is encoded into the wire format the operator's SIEM ingests
// (raw JSON, ArcSight CEF, IBM QRadar LEEF, RFC 5424 syslog, or OpenTelemetry
// OTLP logs) and delivered to one of three destination shapes — Splunk HEC, an
// Elasticsearch _doc index, or a generic HTTP collector.
//
// Two layers do the work and this connector only wires them together:
//   - github.com/olivaresai/olivares/connectors/internal/siemfmt does ALL
//     formatting (CEF/LEEF/syslog/OTLP); this package never re-implements an
//     escaping rule, it calls siemfmt and trusts its golden-tested output.
//   - github.com/olivaresai/olivares/connectors/internal/delivery does the
//     reliable HTTP: backoff, honored Retry-After, retry only the transient
//     failures. This connector builds one delivery.Client in Open and calls Send
//     in Notify.
//
// Minimal data and credential safety (docs/SECURITY-HARDENING.md-3). A Notification already
// carries only non-sensitive, displayable fields, so the connector forwards what
// it is given and adds no enrichment. The destination credential (HEC token,
// Elastic API key, bearer) arrives via the Secret config field, is held only in
// memory on the Source, and is placed only into the outgoing Authorization
// header — never logged, never put in the body, never in an error. delivery and
// siemfmt are both documented to keep bodies/headers out of diagnostics, so a
// failed delivery surfaces a status code and a bounded body excerpt only.
package siem

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.siem"

// Destination is the shape of the target SIEM ingest API.
type destination string

const (
	destSplunk  destination = "splunk"  // Splunk HTTP Event Collector
	destElastic destination = "elastic" // Elasticsearch _doc index
	destHTTP    destination = "http"    // generic HTTP collector
)

// formatSet is this connector's slice of the sdk/siemwire format catalog: the
// notification-connector subset (json-first default, full dialect roster,
// otlp_envelope as the exact alias of otlp — one format everywhere since the
// catalog remap, ledger export included). The accepted set, the default, the
// operator-facing list and the alias resolution derive from the catalog via
// siemfmt.ResolveFormat; the private const block this replaced was one of six
// diverged hand copies. OCSF is v1.8.0 (ai_operation profile) as JSON, for a SOC
// that ACCEPTS 1.8.0 — NOT for Amazon Security Lake, whose custom sources cap at
// OCSF 1.3 in Parquet (a declared gap, not an oversight); ASIM is the Microsoft
// Sentinel ASIM Agent Event (OBS-02/07).
func formatSet() siemwire.FormatSet { return siemwire.NotificationConnectorFormats() }

const (
	defaultMaxAttempts = 4
	// sourcetype is the Splunk sourcetype every record is tagged with so an
	// operator can route the Olivares feed with a single search-time selector.
	splunkSourcetype = "olivares"
)

// Output is the SIEM output connector. One instance is configured once (Open)
// and used for every notification (Notify); it holds the destination shape, the
// chosen format, the endpoint, the (in-memory only) credential, the siemfmt
// device identity, and the reusable delivery client.
type Output struct {
	dest     destination
	format   siemwire.FormatToken // canonical encoder key, resolved at Open
	endpoint string
	token    string // secret: HEC token / Elastic API key / bearer — memory only
	index    string
	hostname string
	device   siemfmt.Device

	maxAttempts int
	doer        delivery.Doer // optional injected transport (tests); nil => default
	sleep       func(context.Context, time.Duration) error
	client      *delivery.Client
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a SIEM output connector with default configuration. The runtime
// (or a plugin main) calls Open before any Notify.
func New() *Output {
	return &Output{
		format:      siemwire.Canonical(formatSet().Default()),
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
		Title:       "SIEM",
		Description: "Ships notifications to a SIEM (Splunk HEC, Elasticsearch, HTTP) as JSON or a SIEM wire format (CEF/LEEF/syslog/OTLP/OCSF-1.8/ASIM-AgentEvent). The otlp and otlp_envelope tokens are equivalent here: this connector's OTLP body is already a complete request envelope.",
		ConfigFields: []sdk.ConfigField{
			{Key: "destination", Type: sdk.FieldString, Required: true, Description: "Target SIEM ingest shape: splunk | elastic | http."},
			{Key: "format", Type: sdk.FieldString, Default: string(formatSet().Default()), Description: "Wire format: " + strings.ReplaceAll(formatSet().List(), "|", " | ") + ". otlp_envelope is an exact alias of otlp (identical bytes)."},
			{Key: "endpoint", Type: sdk.FieldString, Required: true, Description: "Destination base URL (Splunk HEC host, Elasticsearch host, or HTTP collector URL)."},
			{Key: "token", Type: sdk.FieldString, Secret: true, Description: "Destination credential reference: Splunk HEC token, Elasticsearch API key, or HTTP bearer. Held in memory only, never logged or persisted."},
			{Key: "index", Type: sdk.FieldString, Description: "Splunk index / Elasticsearch index name (Elastic: required for the _doc path)."},
			{Key: "hostname", Type: sdk.FieldString, Description: "Syslog HOSTNAME / source host tag."},
			{Key: "vendor", Type: sdk.FieldString, Description: "siemfmt device vendor override (default Olivares.AI)."},
			{Key: "product", Type: sdk.FieldString, Description: "siemfmt device product override (default ControlPlane)."},
			{Key: "version", Type: sdk.FieldString, Description: "siemfmt device version override."},
			{Key: "max_attempts", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Maximum HTTP delivery attempts (including the first) per notification."},
		},
	}
}

// Open reads and validates configuration and builds the reusable delivery client.
// A misconfiguration (unknown destination/format, missing endpoint) is reported
// here, not deferred to Notify.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	switch destination(strings.ToLower(strings.TrimSpace(cfg.Get("destination")))) {
	case destSplunk:
		o.dest = destSplunk
	case destElastic:
		o.dest = destElastic
	case destHTTP:
		o.dest = destHTTP
	case "":
		return fmt.Errorf("siem: destination is required (splunk|elastic|http)")
	default:
		return fmt.Errorf("siem: unknown destination %q (want splunk|elastic|http)", cfg.Get("destination"))
	}

	tok, err := siemfmt.ResolveFormat(formatSet(), cfg.Get("format"))
	if err != nil {
		return fmt.Errorf("siem: %w", err)
	}
	o.format = tok

	o.endpoint = strings.TrimRight(strings.TrimSpace(cfg.Get("endpoint")), "/")
	if o.endpoint == "" {
		return fmt.Errorf("siem: endpoint is required")
	}

	o.token = cfg.Get("token")
	o.index = strings.TrimSpace(cfg.Get("index"))
	o.hostname = strings.TrimSpace(cfg.Get("hostname"))
	if o.dest == destElastic && o.index == "" {
		return fmt.Errorf("siem: elastic destination requires an index for the _doc path")
	}

	o.device = siemfmt.DefaultDevice()
	if v := cfg.Get("vendor"); v != "" {
		o.device.Vendor = v
	}
	if v := cfg.Get("product"); v != "" {
		o.device.Product = v
	}
	if v := cfg.Get("version"); v != "" {
		o.device.Version = v
	}

	o.maxAttempts = cfg.GetInt("max_attempts", o.maxAttempts)
	o.client = delivery.New(o.doer, delivery.Options{
		MaxAttempts: o.maxAttempts,
		Sleep:       o.sleep,
	})
	return nil
}

// Notify encodes n into the configured format and delivers it to the configured
// destination, returning an error if delivery failed or the destination reported
// a logical failure in an otherwise-2xx response (Splunk HEC code!=0).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("siem: connector not opened")
	}
	req, err := o.buildRequest(n)
	if err != nil {
		return err
	}
	res, err := o.client.Send(ctx, req)
	if err != nil {
		return fmt.Errorf("siem: deliver to %s: %w", o.dest, err)
	}
	// Some destinations return 200 with a logical error in the body. Splunk HEC
	// is the one we must inspect: a successful HTTP 200 still carries {"code":N}
	// where N!=0 means the event was rejected (bad token, disabled index, ...).
	if o.dest == destSplunk {
		if err := checkSplunkBody(res.Body); err != nil {
			return fmt.Errorf("siem: splunk HEC rejected event: %w", err)
		}
	}
	return nil
}

// Close releases resources; this connector holds none beyond the delivery client,
// which is stateless. Safe to call even if Open failed.
func (o *Output) Close(context.Context) error { return nil }

// buildRequest formats n and assembles the destination-specific HTTP request
// (path, auth header, content type, body).
func (o *Output) buildRequest(n sdk.Notification) (delivery.Request, error) {
	switch o.dest {
	case destSplunk:
		return o.splunkRequest(n)
	case destElastic:
		return o.elasticRequest(n)
	case destHTTP:
		return o.httpRequest(n)
	default:
		return delivery.Request{}, fmt.Errorf("siem: unknown destination %q", o.dest)
	}
}

// formatBody encodes n in the connector's configured wire format and returns the
// bytes plus the matching Content-Type. JSON and OTLP are application/json; the
// raw SIEM text formats are text/plain.
func (o *Output) formatBody(n sdk.Notification) ([]byte, string, error) {
	switch o.format {
	case siemwire.TokenJSON:
		b, err := json.Marshal(notificationJSON(n))
		if err != nil {
			return nil, "", fmt.Errorf("siem: marshal notification json: %w", err)
		}
		return b, "application/json", nil
	case siemwire.TokenCEF:
		return []byte(siemfmt.CEF(o.device, n)), "text/plain", nil
	case siemwire.TokenLEEF:
		return []byte(siemfmt.LEEF(o.device, n)), "text/plain", nil
	case siemwire.TokenSyslog:
		return []byte(siemfmt.Syslog5424(o.device, siemfmt.SyslogOptions{Hostname: o.hostname}, n)), "text/plain", nil
	case siemwire.TokenOTLP:
		b, err := siemfmt.OTLPLogJSON(o.device, n)
		if err != nil {
			return nil, "", err
		}
		return b, "application/json", nil
	case siemwire.TokenOCSF:
		b, err := siemfmt.OCSF(o.device, n)
		if err != nil {
			return nil, "", err
		}
		return b, "application/json", nil
	case siemwire.TokenASIM:
		b, err := siemfmt.ASIMAgentEvent(o.device, n)
		if err != nil {
			return nil, "", err
		}
		return b, "application/json", nil
	default:
		return nil, "", fmt.Errorf("siem: unknown format %q", o.format)
	}
}

// isTextFormat reports whether the configured format is a raw SIEM text format
// (CEF/LEEF/syslog) rather than a structured JSON document (json/otlp). The
// distinction drives Splunk's /raw-vs-/event endpoint and Elastic's
// message-vs-document body shape.
func (o *Output) isTextFormat() bool {
	switch o.format {
	case siemwire.TokenCEF, siemwire.TokenLEEF, siemwire.TokenSyslog:
		return true
	default:
		return false
	}
}

// --- Splunk HEC --------------------------------------------------------------

// splunkRequest builds a Splunk HTTP Event Collector request. JSON and OTLP go to
// /services/collector/event wrapped in a HEC envelope; the raw text formats go to
// /services/collector/raw with index/sourcetype carried as query parameters (the
// raw endpoint cannot take a JSON envelope). Auth is "Authorization: Splunk <token>".
func (o *Output) splunkRequest(n sdk.Notification) (delivery.Request, error) {
	body, _, err := o.formatBody(n)
	if err != nil {
		return delivery.Request{}, err
	}
	hdr := map[string]string{}
	if o.token != "" {
		hdr["Authorization"] = "Splunk " + o.token
	}

	if o.isTextFormat() {
		// Raw endpoint: the body is the formatted text verbatim; metadata rides as
		// query parameters because /raw has no JSON envelope.
		q := url.Values{}
		q.Set("sourcetype", splunkSourcetype)
		if o.index != "" {
			q.Set("index", o.index)
		}
		if o.hostname != "" {
			q.Set("host", o.hostname)
		}
		hdr["Content-Type"] = "text/plain"
		return delivery.Request{
			URL:    o.endpoint + "/services/collector/raw?" + q.Encode(),
			Header: hdr,
			Body:   body,
		}, nil
	}

	// Event endpoint: a HEC envelope around the event. For JSON the event is the
	// notification object itself; for OTLP it is the OTLP/JSON document as a string
	// (Splunk HEC has no native OTLP-logs endpoint, so it is carried as the event
	// payload — an operator wanting native OTLP points a generic http destination
	// at an OTLP collector instead).
	env := splunkEnvelope{Sourcetype: splunkSourcetype}
	if o.index != "" {
		env.Index = o.index
	}
	if o.hostname != "" {
		env.Host = o.hostname
	}
	if !n.Time.IsZero() {
		env.Time = n.Time.UTC().Unix()
	}
	if o.format == siemwire.TokenOTLP {
		env.Event = json.RawMessage(strconvQuote(string(body)))
	} else {
		env.Event = json.RawMessage(body)
	}
	enc, err := json.Marshal(env)
	if err != nil {
		return delivery.Request{}, fmt.Errorf("siem: marshal splunk envelope: %w", err)
	}
	hdr["Content-Type"] = "application/json"
	return delivery.Request{
		URL:    o.endpoint + "/services/collector/event",
		Header: hdr,
		Body:   enc,
	}, nil
}

// splunkEnvelope is the Splunk HEC /event request body. Time is omitted when
// zero (omitempty) so Splunk applies its own receipt time. Event is raw JSON so
// the notification object (or an OTLP-as-string) is embedded without re-encoding.
type splunkEnvelope struct {
	Event      json.RawMessage `json:"event"`
	Sourcetype string          `json:"sourcetype,omitempty"`
	Index      string          `json:"index,omitempty"`
	Host       string          `json:"host,omitempty"`
	Time       int64           `json:"time,omitempty"`
}

// splunkResponse is the subset of the HEC acknowledgement we inspect: code 0 is
// success; any other code is a logical rejection even under HTTP 200.
type splunkResponse struct {
	Text string `json:"text"`
	Code int    `json:"code"`
}

// checkSplunkBody parses a HEC response body and returns an error when the
// reported code is non-zero (a logical rejection delivered under HTTP 200). An
// empty or unparseable body is treated as success: delivery already confirmed a
// 2xx, and HEC variants (raw endpoint, ack mode) may answer with no JSON body.
func checkSplunkBody(body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var r splunkResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil // not a HEC status document; the 2xx stands
	}
	if r.Code != 0 {
		msg := r.Text
		if msg == "" {
			msg = "non-zero status"
		}
		return fmt.Errorf("code %d: %s", r.Code, msg)
	}
	return nil
}

// --- Elasticsearch -----------------------------------------------------------

// elasticRequest builds an Elasticsearch index request: POST {endpoint}/{index}/_doc
// with "Authorization: ApiKey <token>" when a token is set. For the json format
// the body is the notification document directly; for a text format it is a small
// envelope carrying the formatted line under "message" with an "@timestamp" and
// the structured notification under "olivares".
func (o *Output) elasticRequest(n sdk.Notification) (delivery.Request, error) {
	hdr := map[string]string{"Content-Type": "application/json"}
	if o.token != "" {
		hdr["Authorization"] = "ApiKey " + o.token
	}
	target := o.endpoint + "/" + url.PathEscape(o.index) + "/_doc"

	var body []byte
	if o.isTextFormat() || o.format == siemwire.TokenOTLP {
		formatted, _, err := o.formatBody(n)
		if err != nil {
			return delivery.Request{}, err
		}
		doc := elasticTextDoc{
			Message:   string(formatted),
			Timestamp: elasticTimestamp(n.Time),
			Olivares:  notificationJSON(n),
		}
		body, err = json.Marshal(doc)
		if err != nil {
			return delivery.Request{}, fmt.Errorf("siem: marshal elastic doc: %w", err)
		}
	} else {
		// json format: the notification object IS the indexed document.
		var err error
		body, err = json.Marshal(notificationJSON(n))
		if err != nil {
			return delivery.Request{}, fmt.Errorf("siem: marshal elastic doc: %w", err)
		}
	}
	return delivery.Request{URL: target, Header: hdr, Body: body}, nil
}

// elasticTextDoc wraps a formatted text record (CEF/LEEF/syslog) or an OTLP
// document so Elasticsearch indexes a searchable "message" plus the structured
// notification, with an ECS-style "@timestamp".
type elasticTextDoc struct {
	Message   string           `json:"message"`
	Timestamp string           `json:"@timestamp,omitempty"`
	Olivares  notificationView `json:"olivares"`
}

// elasticTimestamp renders n.Time as RFC3339 (Elasticsearch's default
// date format), or "" when the time is zero so omitempty drops the field.
func elasticTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// --- generic HTTP ------------------------------------------------------------

// httpRequest builds a generic HTTP collector request: POST {endpoint} with the
// formatted bytes as the body and the matching Content-Type, plus an optional
// "Authorization: Bearer <token>".
func (o *Output) httpRequest(n sdk.Notification) (delivery.Request, error) {
	body, contentType, err := o.formatBody(n)
	if err != nil {
		return delivery.Request{}, err
	}
	hdr := map[string]string{"Content-Type": contentType}
	if o.token != "" {
		hdr["Authorization"] = "Bearer " + o.token
	}
	return delivery.Request{URL: o.endpoint, Header: hdr, Body: body}, nil
}

// --- notification JSON shape -------------------------------------------------

// notificationView is the JSON shape of a notification, identical to what the
// webhook output connector ships: a flat, non-sensitive object. Severity is the
// string form of the model scale. omitempty keeps empty optional fields out.
type notificationView struct {
	Type     string            `json:"type,omitempty"`
	Title    string            `json:"title,omitempty"`
	Body     string            `json:"body,omitempty"`
	Severity string            `json:"severity,omitempty"`
	Tenant   string            `json:"tenant,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	Time     string            `json:"time,omitempty"`
}

// notificationJSON projects an sdk.Notification onto the wire view. The time is
// rendered RFC3339 (UTC) and dropped when zero.
func notificationJSON(n sdk.Notification) notificationView {
	v := notificationView{
		Type:     n.Type,
		Title:    n.Title,
		Body:     n.Body,
		Severity: severityString(n.Severity),
		Tenant:   n.Tenant,
		Fields:   n.Fields,
	}
	if !n.Time.IsZero() {
		v.Time = n.Time.UTC().Format(time.RFC3339)
	}
	return v
}

// severityString renders the model severity as its lowercase label, or "" for an
// empty/unknown severity so it is omitted from the JSON.
func severityString(s model.Severity) string {
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

// strconvQuote returns s as a JSON string literal (the OTLP document embedded as
// a Splunk event payload string). It uses strconv.Quote, which produces a valid
// JSON-compatible double-quoted Go string for UTF-8 input.
func strconvQuote(s string) string { return strconv.Quote(s) }
