// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package siemsink turns an already-format-encoded event body (the OCSF/CEF/LEEF/
// syslog/JSON bytes produced upstream by core/audit.FormatEvent for the ledger, or
// connectors/internal/siemfmt for findings) into the concrete HTTP request one SIEM
// control tower expects: its envelope, its routing path and its authentication
// header. It owns ENVELOPE + AUTH only — never the transport. The durable delivery
// engine (modules/eventing) owns the POST, the SSRF guard, the retry ladder,
// the DLQ and replay; this package just shapes the request so the engine can ship a
// SIEM-native event over its existing machinery instead of a generic webhook.
//
// It is pure (stdlib only), deterministic (sorted attribute keys, declaration-order
// structs) so every shape is golden-testable, and minimal-data: it re-shapes only
// what the caller already passes (which is already redacted and integrity-bearing),
// adding no enrichment and never holding a credential beyond the single header it
// stamps. It imports neither the engine (/core) nor the connector SDK — only stdlib
// — so it sits cleanly under the Apache license boundary and is reusable by any
// connector or module.
package siemsink

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Kind selects the destination control tower's wire protocol. The zero value is
// the empty string, which is NOT a valid sink — a caller resolves a concrete kind
// from the subscription's sink_kind column (deny-closed: an unknown kind errors).
type Kind string

const (
	// KindHTTPS is a generic HTTPS log collector: the encoded body is POSTed
	// verbatim. Authentication is the engine's HMAC (added by the caller), so
	// this sink stamps no auth header of its own.
	KindHTTPS Kind = "https"
	// KindSplunkHEC is the Splunk HTTP Event Collector: JSON bodies ride the
	// /event envelope, text bodies (CEF/LEEF/syslog) ride /raw with the routing
	// metadata as query parameters; auth is "Authorization: Splunk <token>".
	KindSplunkHEC Kind = "splunk_hec"
	// KindSentinelDCR is the Microsoft Sentinel / Azure Monitor Logs Ingestion API
	// (Data Collection Rule endpoint): a JSON array POSTed to the DCR stream path
	// with an Entra bearer token. The bearer is operator-supplied (this package
	// does NOT mint client-credentials tokens — that refresh loop is a documented
	// follow-up); a static/sidecar-minted token is the supported path.
	KindSentinelDCR Kind = "sentinel_dcr"
	// KindDatadog is the Datadog Logs Intake API v2 (POST /api/v2/logs): a
	// one-element array of log objects, auth "DD-API-KEY: <key>".
	KindDatadog Kind = "datadog"
	// KindNewRelic is the New Relic Log API (POST /log/v1): the detailed
	// {common,logs[]} JSON, auth "Api-Key: <key>".
	KindNewRelic Kind = "newrelic"
)

// Valid reports whether k is a sink kind this package can render.
func (k Kind) Valid() bool {
	switch k {
	case KindHTTPS, KindSplunkHEC, KindSentinelDCR, KindDatadog, KindNewRelic:
		return true
	default:
		return false
	}
}

// Event is one event to ship, already encoded into the chosen SIEM dialect. Body
// is the canonical dialect bytes (an OCSF/ASIM/json object, or a CEF/LEEF/syslog
// line); BodyIsJSON tells the envelope whether Body is a JSON document (so it can
// be embedded as a sub-object) or opaque text (so it rides a raw/log-message slot).
// Message is a short human summary (used where a sink needs a scalar message field
// and Body is JSON). Tags are small, non-secret labels (severity/tenant/type) the
// log sinks expose for filtering. Nothing here is a secret or a raw payload — the
// caller has already enforced minimal-data.
type Event struct {
	Body       []byte
	BodyIsJSON bool
	Message    string
	Time       time.Time
	Source     string
	Tags       map[string]string
}

// Sink is the resolved destination for one delivery: the kind, the base endpoint,
// the opened credential (token/api-key/bearer — held only long enough to stamp the
// header) and the non-secret routing options (index, sourcetype, host, source,
// dcr_immutable_id, stream, service, region).
type Sink struct {
	Kind     Kind
	Endpoint string
	Cred     string
	Opts     map[string]string
}

// Request is the HTTP request the durable engine will POST. The engine owns the
// transport (SSRF-guarded dial, TLS, retry/DLQ/replay); this is purely the shaped
// URL, headers and body.
type Request struct {
	URL    string
	Header map[string]string
	Body   []byte
}

// Render shapes one Event into the HTTP Request for the given Sink. It fails closed:
// an invalid kind, a missing required credential, or a missing required routing
// option returns an error (the caller treats it as a config failure that retries
// and dead-letters honestly, never sending unauthenticated or to the wrong place).
func Render(s Sink, e Event) (Request, error) {
	switch s.Kind {
	case KindHTTPS:
		return renderHTTPS(s, e)
	case KindSplunkHEC:
		return renderSplunkHEC(s, e)
	case KindSentinelDCR:
		return renderSentinelDCR(s, e)
	case KindDatadog:
		return renderDatadog(s, e)
	case KindNewRelic:
		return renderNewRelic(s, e)
	default:
		return Request{}, fmt.Errorf("siemsink: unknown sink kind %q", s.Kind)
	}
}

// contentType returns the body Content-Type for a JSON-vs-text body.
func contentType(jsonBody bool) string {
	if jsonBody {
		return "application/json"
	}
	return "text/plain; charset=utf-8"
}

// renderHTTPS POSTs the encoded body verbatim to the endpoint. No sink auth header:
// the generic-HTTPS sink is authenticated by the engine's HMAC, which the
// caller stamps over this exact body.
func renderHTTPS(s Sink, e Event) (Request, error) {
	if s.Endpoint == "" {
		return Request{}, fmt.Errorf("siemsink: https sink requires an endpoint")
	}
	return Request{
		URL:    s.Endpoint,
		Header: map[string]string{"Content-Type": contentType(e.BodyIsJSON)},
		Body:   e.Body,
	}, nil
}

// HEC endpoint paths (the standard documented HEC contract; see connectors/splunkhec).
const (
	hecPathEvent = "/services/collector/event"
	hecPathRaw   = "/services/collector/raw"
)

// hecEnvelope is the Splunk HEC /event JSON envelope. Event is the caller's encoded
// JSON document carried verbatim (json.RawMessage), so the integrity fields a ledger
// record embeds are never reshaped. Time is epoch seconds with fractional millis.
type hecEnvelope struct {
	Event      json.RawMessage `json:"event"`
	Time       float64         `json:"time,omitempty"`
	Host       string          `json:"host,omitempty"`
	Source     string          `json:"source,omitempty"`
	Sourcetype string          `json:"sourcetype,omitempty"`
	Index      string          `json:"index,omitempty"`
}

// renderSplunkHEC builds the HEC submit request: JSON bodies are wrapped in the
// /event envelope; text bodies (CEF/LEEF/syslog) go to /raw with the routing
// metadata as query parameters (a raw text event cannot carry an envelope). Auth is
// the "Authorization: Splunk <token>" scheme.
func renderSplunkHEC(s Sink, e Event) (Request, error) {
	if s.Endpoint == "" {
		return Request{}, fmt.Errorf("siemsink: splunk_hec sink requires an endpoint")
	}
	if s.Cred == "" {
		return Request{}, fmt.Errorf("siemsink: splunk_hec sink requires a token credential")
	}
	base := strings.TrimRight(s.Endpoint, "/")
	hdr := map[string]string{"Authorization": "Splunk " + s.Cred}
	sourcetype := optOr(s.Opts, "sourcetype", "olivares")
	source := firstNonEmpty(s.Opts["source"], e.Source)

	if !e.BodyIsJSON {
		q := url.Values{}
		q.Set("sourcetype", sourcetype)
		if v := s.Opts["index"]; v != "" {
			q.Set("index", v)
		}
		if v := s.Opts["host"]; v != "" {
			q.Set("host", v)
		}
		if source != "" {
			q.Set("source", source)
		}
		hdr["Content-Type"] = "text/plain"
		return Request{URL: base + hecPathRaw + "?" + q.Encode(), Header: hdr, Body: e.Body}, nil
	}

	env := hecEnvelope{
		Event:      json.RawMessage(e.Body),
		Source:     source,
		Sourcetype: sourcetype,
		Index:      s.Opts["index"],
		Host:       s.Opts["host"],
	}
	if !e.Time.IsZero() {
		env.Time = float64(e.Time.UTC().UnixNano()) / 1e9
	}
	body, err := json.Marshal(env)
	if err != nil {
		return Request{}, fmt.Errorf("siemsink: marshal HEC envelope: %w", err)
	}
	hdr["Content-Type"] = "application/json"
	return Request{URL: base + hecPathEvent, Header: hdr, Body: body}, nil
}

// renderSentinelDCR builds the Logs Ingestion API request: a JSON array POSTed to
// the DCR stream path on the data collection endpoint, with an Entra bearer token.
// A JSON body is the array element; a text body is wrapped in a minimal object so
// the stream always receives a JSON object. The bearer is operator-supplied.
func renderSentinelDCR(s Sink, e Event) (Request, error) {
	dce := strings.TrimRight(s.Endpoint, "/")
	if dce == "" {
		return Request{}, fmt.Errorf("siemsink: sentinel_dcr sink requires a data collection endpoint")
	}
	if s.Cred == "" {
		return Request{}, fmt.Errorf("siemsink: sentinel_dcr sink requires a bearer token")
	}
	dcr := s.Opts["dcr_immutable_id"]
	stream := s.Opts["stream"]
	if dcr == "" || stream == "" {
		return Request{}, fmt.Errorf("siemsink: sentinel_dcr sink requires dcr_immutable_id and stream options")
	}
	var elem json.RawMessage
	if e.BodyIsJSON {
		elem = json.RawMessage(e.Body)
	} else {
		obj := map[string]string{"Message": string(e.Body)}
		if !e.Time.IsZero() {
			obj["TimeGenerated"] = e.Time.UTC().Format(time.RFC3339)
		}
		enc, err := json.Marshal(obj)
		if err != nil {
			return Request{}, fmt.Errorf("siemsink: marshal sentinel element: %w", err)
		}
		elem = enc
	}
	body, err := json.Marshal([]json.RawMessage{elem})
	if err != nil {
		return Request{}, fmt.Errorf("siemsink: marshal sentinel array: %w", err)
	}
	u := dce + "/dataCollectionRules/" + url.PathEscape(dcr) + "/streams/" + url.PathEscape(stream) + "?api-version=2023-01-01"
	return Request{
		URL:    u,
		Header: map[string]string{"Authorization": "Bearer " + s.Cred, "Content-Type": "application/json"},
		Body:   body,
	}, nil
}

// ddPathLogs is the Datadog Logs Intake v2 path appended to the resolved host.
const ddPathLogs = "/api/v2/logs"

// ddEntry is one Datadog Logs Intake v2 entry. The encoded event is the reserved
// "message"; the routing/severity labels ride ddtags and the non-reserved
// "olivares" object so they stay searchable without colliding with reserved names.
type ddEntry struct {
	Message  string            `json:"message"`
	DDSource string            `json:"ddsource,omitempty"`
	Service  string            `json:"service,omitempty"`
	Hostname string            `json:"hostname,omitempty"`
	DDTags   string            `json:"ddtags,omitempty"`
	Olivares map[string]string `json:"olivares,omitempty"`
}

// renderDatadog builds the one-element /api/v2/logs array. The intake host is the
// caller-resolved endpoint (e.g. https://http-intake.logs.datadoghq.com); auth is
// the DD-API-KEY header. The encoded event is the log message; tags become ddtags.
func renderDatadog(s Sink, e Event) (Request, error) {
	if s.Endpoint == "" {
		return Request{}, fmt.Errorf("siemsink: datadog sink requires an endpoint")
	}
	if s.Cred == "" {
		return Request{}, fmt.Errorf("siemsink: datadog sink requires an api key")
	}
	entry := ddEntry{
		Message:  messageOf(e),
		DDSource: optOr(s.Opts, "source", "olivares"),
		Service:  optOr(s.Opts, "service", "olivares-control-plane"),
		Hostname: s.Opts["host"],
		DDTags:   ddTags(e.Tags),
		Olivares: sortedCopy(e.Tags),
	}
	body, err := json.Marshal([]ddEntry{entry})
	if err != nil {
		return Request{}, fmt.Errorf("siemsink: marshal datadog entry: %w", err)
	}
	return Request{
		URL:    resolvePath(s.Endpoint, ddPathLogs),
		Header: map[string]string{"Content-Type": "application/json", "DD-API-KEY": s.Cred},
		Body:   body,
	}, nil
}

// nrPathLogs is the New Relic Log API path appended to the resolved host.
const nrPathLogs = "/log/v1"

// nrPayload is the detailed New Relic Log API shape: a single common block plus a
// logs array. The encoded event is the log message; tags become per-log attributes.
type nrPayload struct {
	Common nrCommon `json:"common"`
	Logs   []nrLog  `json:"logs"`
}
type nrCommon struct {
	Attributes map[string]string `json:"attributes,omitempty"`
}
type nrLog struct {
	Message    string            `json:"message"`
	Timestamp  int64             `json:"timestamp,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// renderNewRelic builds the New Relic Log API request: the detailed {common,logs[]}
// array form, auth "Api-Key: <key>". The encoded event is the log message; tags
// become per-log attributes; timestamp is epoch millis when known.
func renderNewRelic(s Sink, e Event) (Request, error) {
	if s.Endpoint == "" {
		return Request{}, fmt.Errorf("siemsink: newrelic sink requires an endpoint")
	}
	if s.Cred == "" {
		return Request{}, fmt.Errorf("siemsink: newrelic sink requires an ingest key")
	}
	logEntry := nrLog{Message: messageOf(e), Attributes: sortedCopy(e.Tags)}
	if !e.Time.IsZero() {
		logEntry.Timestamp = e.Time.UTC().UnixMilli()
	}
	common := nrCommon{}
	if src := firstNonEmpty(s.Opts["source"], e.Source); src != "" {
		common.Attributes = map[string]string{"logtype": "olivares", "source": src}
	} else {
		common.Attributes = map[string]string{"logtype": "olivares"}
	}
	body, err := json.Marshal([]nrPayload{{Common: common, Logs: []nrLog{logEntry}}})
	if err != nil {
		return Request{}, fmt.Errorf("siemsink: marshal newrelic payload: %w", err)
	}
	return Request{
		URL:    resolvePath(s.Endpoint, nrPathLogs),
		Header: map[string]string{"Content-Type": "application/json", "Api-Key": s.Cred},
		Body:   body,
	}, nil
}

// messageOf returns the scalar message a log sink carries: the encoded body (so the
// full canonical SIEM dialect is the log line), falling back to the human summary,
// then to a stable placeholder so the message is never empty.
func messageOf(e Event) string {
	if len(e.Body) > 0 {
		return string(e.Body)
	}
	if e.Message != "" {
		return e.Message
	}
	return "olivares event"
}

// ddTags renders tags as Datadog's comma-separated "key:value" ddtags string, in
// deterministic (sorted) key order.
func ddTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for _, k := range sortedKeys(tags) {
		parts = append(parts, k+":"+tags[k])
	}
	return strings.Join(parts, ",")
}

// resolvePath appends path to host unless host already carries a path (an explicit
// full-URL override wins verbatim, e.g. an httptest target).
func resolvePath(host, path string) string {
	h := strings.TrimRight(host, "/")
	if u, err := url.Parse(h); err == nil && u.Path != "" && u.Path != "/" {
		return h
	}
	return h + path
}

// optOr returns opts[key] when non-empty, else def.
func optOr(opts map[string]string, key, def string) string {
	if v := opts[key]; v != "" {
		return v
	}
	return def
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// sortedKeys returns a map's non-empty keys in deterministic order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// sortedCopy returns a copy of m with empty keys dropped (nil when empty), so the
// emitted object is deterministic and never mutates the caller's map.
func sortedCopy(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for _, k := range sortedKeys(m) {
		out[k] = m[k]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
