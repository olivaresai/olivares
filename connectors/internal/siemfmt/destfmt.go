// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// This file maps an sdk.Notification onto the two modern destination schemas whose
// SIEM connectors adds: Elastic Common Schema (ECS, for Elasticsearch _bulk) and
// Google Chronicle / Google SecOps UDM (Unified Data Model). Both are pinned and
// verified against their primary sources (ECS 9.4.0; UDM v1alpha events:import). The
// classic four formats stay in siemfmt.go; OCSF/ASIM stay in aiformats.go. Severity
// for both lands through the single mapSeverity table (siemfmt.go) — ECS reuses the
// numeric 0..10 scale, UDM uses its security_result.severity enum — so a rule author
// never sees two severity dialects.
//
// Field order is deterministic: a Go struct serializes in declaration order, and a
// map[string]string serializes with json.Marshal's sorted keys, so the same
// notification always produces byte-identical output (golden-tested). Minimal data
// (docs/SECURITY-HARDENING.md): a Notification already carries only non-sensitive structural fields.

// fieldResource is the recognized Notification.Fields key for the accessed resource
// (mapped onto ECS-/UDM-native target fields). The rest of the recognized keys live
// in aiformats.go (shared with OCSF/ASIM).
const fieldResource = "resource"

// --- Elastic Common Schema (ECS) ---------------------------------------------

// ECSVersion is the Elastic Common Schema version this encoder targets, verified
// "current" on elastic.co (ecs.version is a required field that must exist on every
// event).
const ECSVersion = "9.4.0"

type ecsVersion struct {
	Version string `json:"version"`
}

type ecsEvent struct {
	Kind     string `json:"kind,omitempty"`     // alert | event (ECS event.kind keyword set)
	Action   string `json:"action,omitempty"`   // the notification type
	Severity int    `json:"severity,omitempty"` // numeric (ECS event.severity is a long)
	Provider string `json:"provider,omitempty"`
}

type ecsObserver struct {
	Vendor  string `json:"vendor,omitempty"`
	Product string `json:"product,omitempty"`
}

type ecsService struct {
	Name string `json:"name,omitempty"`
}

// ecsDoc is one ECS document. Field order is the declaration order; labels (a flat
// keyword map) serialize with sorted keys. event.category/event.type are
// deliberately NOT emitted: their ECS allowed-value sets are semantic and a generic
// governance notification cannot be classified into them without guessing — omitting
// is honest (an operator adds an ingest pipeline if they need them) rather than
// fabricating a category that does not fit.
type ecsDoc struct {
	Timestamp string            `json:"@timestamp,omitempty"`
	Message   string            `json:"message,omitempty"`
	ECS       ecsVersion        `json:"ecs"`
	Event     ecsEvent          `json:"event"`
	Labels    map[string]string `json:"labels,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Observer  ecsObserver       `json:"observer"`
	Service   ecsService        `json:"service,omitempty"`
}

// ECS encodes n as an Elastic Common Schema (9.4.0) JSON document for the
// Elasticsearch _bulk API. @timestamp is the event time (omitted when zero — never
// fabricated); message is "Title — Body"; the ordered fields become ECS labels; the
// severity label and "olivares" become tags.
func ECS(d Device, n sdk.Notification) ([]byte, error) {
	dev := d.orDefault()
	sev := mapSeverity(n.Severity)

	kind := "event"
	if n.Type == "finding.reported" {
		kind = "alert"
	}

	doc := ecsDoc{
		Message: joinTitleBody(n),
		ECS:     ecsVersion{Version: ECSVersion},
		Event: ecsEvent{
			Kind:     kind,
			Action:   n.Type,
			Severity: sev.ecs,
			Provider: dev.Vendor,
		},
		Labels:   fieldsMap(n),
		Tags:     ecsTags(n),
		Observer: ecsObserver{Vendor: dev.Vendor, Product: dev.Product},
		Service:  ecsService{Name: dev.Product},
	}
	if !n.Time.IsZero() {
		doc.Timestamp = n.Time.UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("siemfmt: marshal ECS document: %w", err)
	}
	return b, nil
}

// ecsTags returns the ECS tags array: the severity label (when known) and a stable
// "olivares" source tag, so a SOC can filter the feed.
func ecsTags(n sdk.Notification) []string {
	tags := []string{"olivares"}
	if s := severityLabel(n.Severity); s != "" {
		tags = append(tags, s)
	}
	return tags
}

// --- Google Chronicle / SecOps UDM -------------------------------------------

// UDMEventType is the default UDM metadata.event_type when the caller does not
// override it. GENERIC_EVENT is the always-valid type that imposes no mandatory
// noun sub-fields, which suits a generic governance/finding notification.
const UDMEventType = "GENERIC_EVENT"

// UDMOptions tunes the UDM event beyond the device identity.
type UDMOptions struct {
	// EventType overrides metadata.event_type (default GENERIC_EVENT). The caller is
	// responsible for passing a UDM-valid enum value.
	EventType string
	// Now is the fallback event timestamp used when the notification has no time.
	// UDM requires metadata.event_timestamp, so the connector passes time.Now();
	// siemfmt never reads the clock itself (determinism).
	Now time.Time
}

type udmMetadata struct {
	EventTimestamp string `json:"eventTimestamp"`
	EventType      string `json:"eventType"`
	ProductName    string `json:"productName,omitempty"`
	VendorName     string `json:"vendorName,omitempty"`
	Description    string `json:"description,omitempty"`
}

type udmUser struct {
	UserID string `json:"userid,omitempty"`
}

type udmResource struct {
	Name string `json:"name,omitempty"`
}

type udmNoun struct {
	Hostname    string       `json:"hostname,omitempty"`
	Application string       `json:"application,omitempty"`
	User        *udmUser     `json:"user,omitempty"`
	Resource    *udmResource `json:"resource,omitempty"`
}

type udmSecurityResult struct {
	Action      []string `json:"action,omitempty"`
	Severity    string   `json:"severity,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
}

// udmEvent is one UDM event object (the value placed under the "udm" key by the
// Chronicle connector). securityResult is an ARRAY in UDM; additional is a free-form
// struct (sorted-key map) carrying the unmapped fields so nothing is dropped.
type udmEvent struct {
	Metadata       udmMetadata         `json:"metadata"`
	Principal      *udmNoun            `json:"principal,omitempty"`
	Target         *udmNoun            `json:"target,omitempty"`
	SecurityResult []udmSecurityResult `json:"securityResult,omitempty"`
	Additional     map[string]string   `json:"additional,omitempty"`
}

// UDM encodes n as a Google Chronicle / SecOps UDM event object (the value of the
// "udm" key in an events:import inline_source). metadata.event_timestamp is the
// event time (or opts.Now when the notification has none); event_type defaults to
// GENERIC_EVENT. The agent/actor map onto principal, the resource/tool onto target,
// and decision/severity onto a securityResult; unmapped fields ride under additional.
func UDM(d Device, opts UDMOptions, n sdk.Notification) ([]byte, error) {
	dev := d.orDefault()
	sev := mapSeverity(n.Severity)

	eventType := opts.EventType
	if eventType == "" {
		eventType = UDMEventType
	}
	ts := n.Time
	if ts.IsZero() {
		ts = opts.Now
	}

	ev := udmEvent{
		Metadata: udmMetadata{
			EventType:   eventType,
			ProductName: dev.Product,
			VendorName:  dev.Vendor,
			Description: n.Title,
		},
		Principal:      udmPrincipal(n),
		Target:         udmTarget(n),
		SecurityResult: udmSecResults(n, sev.udm),
		Additional:     udmAdditional(n),
	}
	if !ts.IsZero() {
		ev.Metadata.EventTimestamp = ts.UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("siemfmt: marshal UDM event: %w", err)
	}
	return b, nil
}

// udmPrincipal maps the acting agent/actor onto the UDM principal noun (nil to omit).
func udmPrincipal(n sdk.Notification) *udmNoun {
	app := firstField(n, fieldAgent)
	user := firstField(n, fieldActor)
	if app == "" && user == "" {
		return nil
	}
	p := &udmNoun{Application: app}
	if user != "" {
		p.User = &udmUser{UserID: user}
	}
	return p
}

// udmTarget maps the accessed resource/tool onto the UDM target noun (nil to omit).
func udmTarget(n sdk.Notification) *udmNoun {
	res := firstField(n, fieldResource)
	tool := firstField(n, fieldTool)
	if res == "" && tool == "" {
		return nil
	}
	t := &udmNoun{Application: tool}
	if res != "" {
		t.Resource = &udmResource{Name: res}
	}
	return t
}

// udmSecResults builds the (single-element) UDM securityResult array from the
// decision and severity, or nil when there is nothing to report.
func udmSecResults(n sdk.Notification, sev string) []udmSecurityResult {
	sr := udmSecurityResult{Severity: sev, Summary: n.Title, Description: n.Body}
	if a := udmAction(n.Fields[fieldDecision]); a != "" {
		sr.Action = []string{a}
	}
	if len(sr.Action) == 0 && sr.Severity == "" && sr.Summary == "" && sr.Description == "" {
		return nil
	}
	return []udmSecurityResult{sr}
}

// udmAction maps a decision/outcome onto the UDM security_result.action enum
// (ALLOW/BLOCK); an unrecognized/empty decision yields "" (omit) rather than a
// fabricated action.
func udmAction(decision string) string {
	switch decision {
	case "allow", "accept", "success", "succeeded":
		return "ALLOW"
	case "deny", "denied", "reject", "blocked", "block", "failure", "error":
		return "BLOCK"
	default:
		return ""
	}
}

// udmAdditional collects the notification's non-noun fields (plus type/tenant) into
// the UDM additional struct so nothing the principal/target/securityResult mapping
// did not consume is lost.
func udmAdditional(n sdk.Notification) map[string]string {
	consumed := map[string]bool{
		fieldAgent: true, fieldActor: true, fieldResource: true, fieldTool: true, fieldDecision: true,
	}
	out := map[string]string{}
	if n.Type != "" {
		out["event_type"] = n.Type
	}
	if n.Tenant != "" {
		out["tenant"] = n.Tenant
	}
	for k, v := range n.Fields {
		if k == "" || consumed[k] {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- shared helpers ----------------------------------------------------------

// joinTitleBody renders the human message as "Title — Body" (the same projection
// the syslog and OTLP encoders use).
func joinTitleBody(n sdk.Notification) string {
	msg := n.Title
	if n.Body != "" {
		if msg != "" {
			msg += " — "
		}
		msg += n.Body
	}
	return msg
}

// fieldsMap returns the ordered fields as a flat map (tenant appended by
// orderedFields when not already present). json.Marshal emits map keys sorted, so
// the result is deterministic.
func fieldsMap(n sdk.Notification) map[string]string {
	of := orderedFields(n)
	if len(of) == 0 {
		return nil
	}
	out := make(map[string]string, len(of))
	for _, f := range of {
		out[f.k] = f.v
	}
	return out
}
