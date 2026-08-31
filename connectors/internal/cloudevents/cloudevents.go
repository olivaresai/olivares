// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cloudevents wraps an Olivares notification (or any event) in a CloudEvents
// 1.0 envelope (spec 1.0.2, CNCF-graduated). It is the shared, dependency-free
// packaging helper the ITSM and messaging paths share for queue/stream egress, so a
// downstream consumer can route, filter and replay events without parsing an
// Olivares-proprietary shape.
//
// It is minimal-data (docs/SECURITY-HARDENING.md): the envelope carries the same non-sensitive
// Notification fields the webhook connector delivers (type, title, body, severity,
// tenant, structured fields, time) plus the CloudEvents context attributes — never a
// secret or raw payload. It imports only the SDK and the standard library.
//
// Two wire forms are supported, both from the CloudEvents HTTP Protocol Binding:
//
//   - STRUCTURED mode — the whole event (context attributes + data) is one JSON
//     document with media type "application/cloudevents+json" (MarshalJSON /
//     StructuredHTTP). This is the default for a generic webhook/queue carrier.
//   - BINARY mode — context attributes ride as "ce-*" HTTP headers and the data is
//     the raw body with its own Content-Type (BinaryHTTP). This is what a consumer
//     that dispatches on headers (a broker, an HTTP sink) prefers.
package cloudevents

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// SpecVersion is the CloudEvents spec version this package emits. The "specversion"
// attribute value is the MAJOR.MINOR "1.0" (the patch-level 1.0.2 is the spec
// document's revision, NOT the on-the-wire value — the spec is explicit that the
// attribute carries "1.0").
const SpecVersion = "1.0"

// ContentTypeStructured is the media type of a structured-mode CloudEvents JSON
// document.
const ContentTypeStructured = "application/cloudevents+json; charset=UTF-8"

// headerPrefix is the HTTP binary-content-mode attribute header prefix.
const headerPrefix = "ce-"

// Event is a CloudEvents 1.0 event. The four required context attributes (id, source,
// type, and the implicit specversion) plus the optional ones are typed fields; Data is
// the (already-encoded) event payload; Extensions are additional context attributes a
// producer attaches (lowercase-alphanumeric names per the spec's attribute-naming
// rule). The zero value is not a valid event — Validate enforces the required set.
type Event struct {
	// ID is the producer-assigned event id, unique within the source (REQUIRED).
	ID string
	// Source identifies the producing context as a URI-reference (REQUIRED), e.g.
	// "/olivares/core" or "https://alma.example/olivares".
	Source string
	// Type is the event type in reverse-DNS-ish form (REQUIRED), e.g.
	// "ai.olivares.finding.reported".
	Type string
	// Subject is the event subject within the source (OPTIONAL).
	Subject string
	// Time is when the occurrence happened (OPTIONAL); zero omits it.
	Time time.Time
	// DataContentType is the media type of Data (OPTIONAL); for JSON data set
	// "application/json".
	DataContentType string
	// DataSchema is a URI to the schema Data adheres to (OPTIONAL).
	DataSchema string
	// Data is the already-encoded event payload (OPTIONAL). For structured JSON it
	// must be valid JSON (it is embedded verbatim under the "data" member).
	Data json.RawMessage
	// Extensions are additional context attributes. Names MUST be lowercase
	// alphanumeric and not collide with a spec attribute; values are strings.
	Extensions map[string]string
}

// reservedAttributes are the spec context-attribute names an extension may not shadow.
var reservedAttributes = map[string]bool{
	"id": true, "source": true, "specversion": true, "type": true,
	"datacontenttype": true, "dataschema": true, "subject": true, "time": true,
	"data": true, "data_base64": true,
}

// Validate reports whether e is a well-formed CloudEvent: the required attributes are
// present and every extension name is a legal (lowercase-alphanumeric, non-reserved)
// attribute name. A producer calls it implicitly via MarshalJSON/Binary*.
func (e Event) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("cloudevents: id is required")
	}
	if e.Source == "" {
		return fmt.Errorf("cloudevents: source is required")
	}
	if e.Type == "" {
		return fmt.Errorf("cloudevents: type is required")
	}
	for k := range e.Extensions {
		if reservedAttributes[k] {
			return fmt.Errorf("cloudevents: extension %q shadows a reserved attribute", k)
		}
		if !validAttributeName(k) {
			return fmt.Errorf("cloudevents: extension name %q is not lowercase alphanumeric", k)
		}
	}
	return nil
}

// validAttributeName reports whether s is a legal CloudEvents attribute name: a
// non-empty run of lowercase ASCII letters and digits (the spec's strict rule;
// producers are advised to stay within it for maximum interoperability).
func validAttributeName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// MarshalJSON renders the event in CloudEvents structured content mode: one JSON
// object with the context attributes and the data as top-level members. Map-key
// ordering is deterministic (encoding/json sorts object keys), so the same event
// always serializes byte-for-byte identically — important for signing and tests.
func (e Event) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	m := map[string]any{
		"specversion": SpecVersion,
		"id":          e.ID,
		"source":      e.Source,
		"type":        e.Type,
	}
	if e.Subject != "" {
		m["subject"] = e.Subject
	}
	if !e.Time.IsZero() {
		m["time"] = e.Time.UTC().Format(time.RFC3339)
	}
	if e.DataContentType != "" {
		m["datacontenttype"] = e.DataContentType
	}
	if e.DataSchema != "" {
		m["dataschema"] = e.DataSchema
	}
	if len(e.Data) > 0 {
		m["data"] = e.Data // json.RawMessage embeds verbatim
	}
	for k, v := range e.Extensions {
		m[k] = v
	}
	return json.Marshal(m)
}

// StructuredHTTP returns the media type and body for an HTTP structured-mode delivery:
// the whole event as one "application/cloudevents+json" document.
func (e Event) StructuredHTTP() (contentType string, body []byte, err error) {
	body, err = e.MarshalJSON()
	if err != nil {
		return "", nil, err
	}
	return ContentTypeStructured, body, nil
}

// BinaryHTTP returns the headers and body for an HTTP binary-mode delivery: each
// context attribute as a "ce-<name>" header, the data as the raw body, and the data's
// own media type as Content-Type. A consumer that dispatches on headers (a broker, an
// HTTP sink) prefers this form. The returned header map is safe for the caller to
// merge into a request.
func (e Event) BinaryHTTP() (header map[string]string, body []byte, err error) {
	if err := e.Validate(); err != nil {
		return nil, nil, err
	}
	h := map[string]string{
		headerPrefix + "id":          e.ID,
		headerPrefix + "source":      e.Source,
		headerPrefix + "specversion": SpecVersion,
		headerPrefix + "type":        e.Type,
	}
	if e.Subject != "" {
		h[headerPrefix+"subject"] = e.Subject
	}
	if !e.Time.IsZero() {
		h[headerPrefix+"time"] = e.Time.UTC().Format(time.RFC3339)
	}
	if e.DataSchema != "" {
		h[headerPrefix+"dataschema"] = e.DataSchema
	}
	for _, k := range sortedKeys(e.Extensions) {
		h[headerPrefix+k] = e.Extensions[k]
	}
	if e.DataContentType != "" {
		h["Content-Type"] = e.DataContentType
	}
	return h, e.Data, nil
}

// sortedKeys returns the map keys in deterministic order.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// notificationData is the minimal-data JSON payload a Notification contributes as the
// CloudEvent's data member: the same non-sensitive fields the webhook connector
// delivers. Field order fixes the JSON key order; omitempty keeps absent values out.
type notificationData struct {
	Type     string            `json:"type,omitempty"`
	Title    string            `json:"title,omitempty"`
	Body     string            `json:"body,omitempty"`
	Severity string            `json:"severity,omitempty"`
	Tenant   string            `json:"tenant,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}

// FromNotification builds a CloudEvent from an sdk.Notification. id is the
// producer-assigned unique event id (the caller owns uniqueness — a Notification has
// no id of its own); source is the producing context URI. The event type defaults to
// the notification's Type (prefixed into the Olivares type namespace) and the
// occurrence time to the notification's Time. Severity and tenant are also surfaced as
// CloudEvents extension attributes so a consumer can filter on them without decoding
// the data member.
func FromNotification(id, source string, n sdk.Notification) (Event, error) {
	data, err := json.Marshal(notificationData{
		Type:     n.Type,
		Title:    n.Title,
		Body:     n.Body,
		Severity: string(n.Severity),
		Tenant:   n.Tenant,
		Fields:   n.Fields,
	})
	if err != nil {
		return Event{}, fmt.Errorf("cloudevents: marshal notification data: %w", err)
	}
	ext := map[string]string{}
	if n.Severity != "" {
		ext["severity"] = string(n.Severity)
	}
	if n.Tenant != "" {
		// CloudEvents attribute names are lowercase-alphanumeric; "tenantref" stays
		// within that rule (an underscore/dash would not).
		ext["tenantref"] = n.Tenant
	}
	ev := Event{
		ID:              id,
		Source:          source,
		Type:            eventType(n.Type),
		Time:            n.Time,
		DataContentType: "application/json",
		Data:            data,
		Extensions:      ext,
	}
	return ev, ev.Validate()
}

// eventType maps a Notification.Type (an event.Type string like "finding.reported")
// onto a stable CloudEvents type in the Olivares namespace. An empty type falls back
// to a generic notification type so the required attribute is never blank.
func eventType(t string) string {
	if t == "" {
		return "ai.olivares.notification"
	}
	return "ai.olivares." + t
}
