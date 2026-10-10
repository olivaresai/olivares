// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemsink

import "time"

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
