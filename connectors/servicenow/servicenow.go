// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package servicenow is the Olivares AI output connector that creates and updates
// records in ServiceNow — the ITSM/SecOps system of record an enterprise estate runs
// its incident governance in. It implements sdk.OutputConnector: each Notify turns the
// non-sensitive Notification into one ServiceNow write, over the shared reliable-
// delivery transport (internal/delivery) which rides momentary 429/5xx with backoff.
//
// It speaks four ServiceNow surfaces, selected per-notification by
// Fields["servicenow_record"] (or the connector's default record_type):
//
//   - incident — Table API POST /api/now/table/incident (the default).
//   - task     — Table API POST /api/now/table/task.
//   - sir      — Table API POST /api/now/table/sn_si_incident (Security Incident
//     Response; SOC/GRC run SIR in ServiceNow).
//   - em_event — Event Management ingestion POST /api/global/em/jsonv2, the records[]
//     web service, so an alert binds/correlates on ServiceNow's side.
//   - import   — Import Set API POST /api/now/import/{staging_table} (bulk staging +
//     transform map), for an operator who routes through a transform.
//
// Minimal-data / credential handling (docs/SECURITY-HARDENING.md-3): only the displayable Notification
// fields reach the wire. The operator credential is HTTP Basic (user+password) or an
// OAuth bearer token, declared Secret in the config, held in memory only, applied as
// the Authorization header, and NEVER logged — the delivery transport never logs
// headers, and this package never puts the credential into an error. The ServiceNow
// instance URL is not itself a secret (unlike a webhook URL), so a delivery error may
// name the host but never the credential. It imports only the SDK and the Apache
// delivery transport, never the engine.
package servicenow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// basicAuth returns the base64 of "user:password" for an HTTP Basic Authorization
// header value. The encoded credential is held in memory only and never logged.
func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// Name is the connector's globally unique identifier.
const Name = "olivares.servicenow"

// Default configuration values.
const (
	defaultMaxAttempts = 4
	defaultRecordType  = recordIncident
	defaultEventSource = "olivares-control-plane"
	maxShortDescLen    = 160 // ServiceNow short_description caps near 160 chars.
)

// Record types the connector can write. The set is closed: an unknown record type is
// a configuration error, never a guessed table.
const (
	recordIncident = "incident"
	recordTask     = "task"
	recordSIR      = "sir"
	recordEvent    = "em_event"
	recordImport   = "import"
)

// Table names for the table-API record types.
const (
	tableIncident = "incident"
	tableTask     = "task"
	tableSIR      = "sn_si_incident"
)

// Config field keys.
const (
	cfgInstanceURL  = "instance_url"
	cfgAuthMode     = "auth_mode" // "basic" (default) or "bearer"
	cfgUsername     = "username"
	cfgPassword     = "password"
	cfgToken        = "token"
	cfgRecordType   = "record_type"
	cfgStagingTable = "staging_table"
	cfgEventSource  = "event_source"
	cfgMaxAttempts  = "max_attempts"

	// fieldRecord overrides the record type per-notification (decides what/when).
	fieldRecord = "servicenow_record"
)

// Auth modes.
const (
	authBasic  = "basic"
	authBearer = "bearer"
)

// Output is the ServiceNow output connector. A single instance is opened once and
// services every Notify over a shared reliable-delivery client.
type Output struct {
	instanceURL string
	authMode    string
	authHeader  string // pre-built "Basic ..." / "Bearer ..."; in memory only, never logged
	recordType  string
	staging     string
	eventSource string
	maxAttempts int

	client *delivery.Client
	doer   delivery.Doer // optional injected transport (tests); nil => default
}

var _ sdk.OutputConnector = (*Output)(nil)

// New returns a ServiceNow output connector with default configuration.
func New() *Output {
	return &Output{
		authMode:    authBasic,
		recordType:  defaultRecordType,
		eventSource: defaultEventSource,
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
		Title:       "ServiceNow",
		Description: "Creates/updates ServiceNow records (incident/task/SecOps SIR) via the Table API, pushes Event Management em_event, and supports Import Set bulk.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgInstanceURL, Type: sdk.FieldString, Required: true, Description: "ServiceNow instance base URL, e.g. https://acme.service-now.com (no trailing /api)."},
			{Key: cfgAuthMode, Type: sdk.FieldString, Default: authBasic, Description: "Authentication mode: basic (username+password) or bearer (OAuth access token)."},
			{Key: cfgUsername, Type: sdk.FieldString, Secret: true, Description: "ServiceNow integration user (basic mode). Held in memory only, never logged."},
			{Key: cfgPassword, Type: sdk.FieldString, Secret: true, Description: "ServiceNow integration password (basic mode). Held in memory only, never logged."},
			{Key: cfgToken, Type: sdk.FieldString, Secret: true, Description: "OAuth bearer access token (bearer mode). Held in memory only, never logged."},
			{Key: cfgRecordType, Type: sdk.FieldString, Default: defaultRecordType, Description: "Default record type: incident, task, sir, em_event or import (override per-notification with Fields[\"servicenow_record\"])."},
			{Key: cfgStagingTable, Type: sdk.FieldString, Description: "Import Set staging table name (required only for the import record type)."},
			{Key: cfgEventSource, Type: sdk.FieldString, Default: defaultEventSource, Description: "Value for the em_event 'source' field identifying the emitting system."},
			{Key: cfgMaxAttempts, Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total delivery attempts including the first (transient failures only)."},
		},
	}
}

// Open resolves configuration, validates the auth mode's credential, pre-builds the
// Authorization header (in memory only), and builds the reliable-delivery client. It
// fails fast on a misconfiguration (missing instance, missing credential, unknown
// auth mode or record type) rather than deferring to Notify.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.instanceURL = strings.TrimRight(strings.TrimSpace(cfg.Get(cfgInstanceURL)), "/")
	if o.instanceURL == "" {
		return fmt.Errorf("servicenow: %s is required", cfgInstanceURL)
	}
	if v := strings.TrimSpace(cfg.Get(cfgAuthMode)); v != "" {
		o.authMode = strings.ToLower(v)
	}
	switch o.authMode {
	case authBasic:
		user, pass := cfg.Get(cfgUsername), cfg.Get(cfgPassword)
		if user == "" || pass == "" {
			return fmt.Errorf("servicenow: basic auth requires %s and %s", cfgUsername, cfgPassword)
		}
		o.authHeader = "Basic " + basicAuth(user, pass)
	case authBearer:
		tok := cfg.Get(cfgToken)
		if tok == "" {
			return fmt.Errorf("servicenow: bearer auth requires %s", cfgToken)
		}
		o.authHeader = "Bearer " + tok
	default:
		return fmt.Errorf("servicenow: unknown auth_mode %q (want %q or %q)", o.authMode, authBasic, authBearer)
	}
	if v := strings.TrimSpace(cfg.Get(cfgRecordType)); v != "" {
		o.recordType = strings.ToLower(v)
	}
	if !validRecordType(o.recordType) {
		return fmt.Errorf("servicenow: unknown record_type %q", o.recordType)
	}
	o.staging = strings.TrimSpace(cfg.Get(cfgStagingTable))
	if o.recordType == recordImport && o.staging == "" {
		return fmt.Errorf("servicenow: import record_type requires %s", cfgStagingTable)
	}
	if v := strings.TrimSpace(cfg.Get(cfgEventSource)); v != "" {
		o.eventSource = v
	}
	o.maxAttempts = cfg.GetInt(cfgMaxAttempts, o.maxAttempts)

	o.client = delivery.New(o.doer, delivery.Options{MaxAttempts: o.maxAttempts})
	return nil
}

// validRecordType reports whether t is a record type the connector can write.
func validRecordType(t string) bool {
	switch t {
	case recordIncident, recordTask, recordSIR, recordEvent, recordImport:
		return true
	default:
		return false
	}
}

// Notify maps the notification to the resolved record type and writes it. The record
// type defaults to the connector's configuration and may be overridden per call by
// Fields["servicenow_record"] (decides what/when; the connector owns the how).
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("servicenow: Notify called before Open")
	}
	rt := o.recordType
	if v := strings.ToLower(strings.TrimSpace(n.Fields[fieldRecord])); v != "" {
		if !validRecordType(v) {
			return fmt.Errorf("servicenow: unknown %s %q", fieldRecord, v)
		}
		rt = v
	}
	switch rt {
	case recordEvent:
		return o.postEvent(ctx, n)
	case recordImport:
		return o.postJSON(ctx, "/api/now/import/"+o.staging, o.tableRecord(n))
	case recordSIR:
		return o.postJSON(ctx, "/api/now/table/"+tableSIR, o.tableRecord(n))
	case recordTask:
		return o.postJSON(ctx, "/api/now/table/"+tableTask, o.tableRecord(n))
	default: // recordIncident
		return o.postJSON(ctx, "/api/now/table/"+tableIncident, o.tableRecord(n))
	}
}

// Close releases resources; this connector holds none beyond the shared client.
func (o *Output) Close(context.Context) error { return nil }

// tableRecord builds the flat column map for a Table API / Import Set write. It maps
// the Notification onto ServiceNow's standard columns (short_description, description,
// urgency, impact) and folds the non-sensitive structured Fields in as columns — the
// SDK documents Fields as "non-sensitive structured key/values (links, ids) for the
// target", and ServiceNow ignores a column it does not recognize. Connector-control
// keys (the record selector) are dropped.
func (o *Output) tableRecord(n sdk.Notification) map[string]string {
	rec := map[string]string{
		"short_description": shortDescription(n),
	}
	if n.Body != "" {
		rec["description"] = n.Body
	}
	if u, i := urgencyImpact(n.Severity); u != "" {
		rec["urgency"], rec["impact"] = u, i
	}
	for k, v := range n.Fields {
		if k == fieldRecord {
			continue
		}
		rec[k] = v
	}
	return rec
}

// postJSON sends a JSON record to a Table API / Import Set path and inspects the
// result for a logical failure (ServiceNow answers a logical error as a non-2xx with
// {"error":{...}}, which delivery already surfaces; a 2xx error is handled here).
func (o *Output) postJSON(ctx context.Context, path string, record map[string]string) error {
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("servicenow: marshal record: %w", err)
	}
	res, err := o.client.Send(ctx, delivery.Request{
		URL:    o.instanceURL + path,
		Header: o.headers(),
		Body:   body,
	})
	if err != nil {
		// The instance URL is not a secret; delivery's error carries only host+status+
		// a bounded body excerpt, never the Authorization header. Safe to wrap.
		return fmt.Errorf("servicenow: write %s: %w", path, err)
	}
	if logicalErr := resultError(res); logicalErr != nil {
		return fmt.Errorf("servicenow: %s rejected: %w", path, logicalErr)
	}
	return nil
}

// emEvent is one Event Management event row (em_event), wrapped in the jsonv2
// records[] envelope the global web service expects.
type emEventEnvelope struct {
	Records []emEvent `json:"records"`
}

type emEvent struct {
	Source         string `json:"source"`
	Node           string `json:"node,omitempty"`
	Type           string `json:"type,omitempty"`
	Resource       string `json:"resource,omitempty"`
	MetricName     string `json:"metric_name,omitempty"`
	Severity       string `json:"severity"`
	Description    string `json:"description,omitempty"`
	MessageKey     string `json:"message_key,omitempty"`
	EventClass     string `json:"event_class,omitempty"`
	TimeOfEvent    string `json:"time_of_event,omitempty"`
	AdditionalInfo string `json:"additional_info,omitempty"`
}

// postEvent pushes an Event Management em_event via the jsonv2 web service.
func (o *Output) postEvent(ctx context.Context, n sdk.Notification) error {
	ev := emEvent{
		Source:      o.eventSource,
		Node:        n.Fields["node"],
		Type:        n.Type,
		Resource:    n.Fields["resource"],
		MetricName:  n.Fields["metric_name"],
		Severity:    emSeverity(n.Severity),
		Description: shortDescription(n),
		MessageKey:  messageKey(n),
		EventClass:  n.Fields["event_class"],
		TimeOfEvent: eventTime(n.Time),
	}
	if info := additionalInfo(n); info != "" {
		ev.AdditionalInfo = info
	}
	body, err := json.Marshal(emEventEnvelope{Records: []emEvent{ev}})
	if err != nil {
		return fmt.Errorf("servicenow: marshal em_event: %w", err)
	}
	res, err := o.client.Send(ctx, delivery.Request{
		URL:    o.instanceURL + "/api/global/em/jsonv2",
		Header: o.headers(),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("servicenow: push em_event: %w", err)
	}
	if logicalErr := resultError(res); logicalErr != nil {
		return fmt.Errorf("servicenow: em_event rejected: %w", logicalErr)
	}
	return nil
}

// headers returns the request headers: JSON content/accept and the in-memory
// Authorization header. The map is freshly built per call so the transport cannot
// retain a reference.
func (o *Output) headers() map[string]string {
	return map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"Authorization": o.authHeader,
	}
}

// snErrorBody is the shape ServiceNow returns on a logical failure.
type snErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	} `json:"error"`
	Status string `json:"status"`
}

// resultError inspects a 2xx body for a ServiceNow logical failure. ServiceNow
// normally signals errors with a non-2xx (handled by delivery); this defends the rare
// 2xx-with-error-body. The returned error never contains the credential (the body
// does not echo it).
func resultError(res delivery.Result) error {
	if res.Body == "" {
		return nil
	}
	var e snErrorBody
	if err := json.Unmarshal([]byte(res.Body), &e); err != nil {
		return nil // not a recognized error shape; the HTTP status already said 2xx
	}
	if e.Status != "failure" && e.Error.Message == "" {
		return nil
	}
	msg := e.Error.Message
	if msg == "" {
		msg = e.Status
	}
	if e.Error.Detail != "" {
		return fmt.Errorf("%s: %s", msg, e.Error.Detail)
	}
	return fmt.Errorf("%s", msg)
}

// shortDescription returns the Title (falling back to the Body), truncated to the
// ServiceNow short_description limit. A wholly empty notification yields a stable
// placeholder so the record is never rejected for an empty short_description.
func shortDescription(n sdk.Notification) string {
	s := n.Title
	if s == "" {
		s = n.Body
	}
	if s == "" {
		s = "olivares notification"
	}
	return truncate(s, maxShortDescLen)
}

// urgencyImpact maps the shared severity onto ServiceNow's urgency/impact (1=High,
// 2=Medium, 3=Low). An empty severity yields empty strings so the connector does not
// override the instance's defaults.
func urgencyImpact(s model.Severity) (urgency, impact string) {
	switch s {
	case model.SeverityCritical, model.SeverityHigh:
		return "1", "1"
	case model.SeverityMedium:
		return "2", "2"
	case model.SeverityLow:
		return "3", "3"
	default: // info / empty / unknown
		return "", ""
	}
}

// emSeverity maps the shared severity onto the ServiceNow Event Management severity
// scale (1=Critical, 2=Major, 3=Minor, 4=Warning, 5=Info; 0=Clear is reserved for an
// explicit clear and is never produced from a notification's severity).
func emSeverity(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return "1"
	case model.SeverityHigh:
		return "2"
	case model.SeverityMedium:
		return "3"
	case model.SeverityLow:
		return "4"
	default: // info / empty / unknown
		return "5"
	}
}

// messageKey returns the em_event correlation key: an explicit Fields["message_key"]
// or Fields["dedup_key"], else empty (ServiceNow assigns its own binding).
func messageKey(n sdk.Notification) string {
	if k := n.Fields["message_key"]; k != "" {
		return k
	}
	return n.Fields["dedup_key"]
}

// additionalInfo encodes the non-sensitive Fields as a JSON STRING (ServiceNow's
// em_event additional_info expects a JSON-encoded string, not a nested object, to
// avoid an "[object Object]" display). The connector-control and already-mapped keys
// are dropped. It returns "" when there is nothing to add.
func additionalInfo(n sdk.Notification) string {
	skip := map[string]bool{
		fieldRecord: true, "node": true, "resource": true, "metric_name": true,
		"event_class": true, "message_key": true, "dedup_key": true,
	}
	extra := make(map[string]string, len(n.Fields))
	for k, v := range n.Fields {
		if skip[k] {
			continue
		}
		extra[k] = v
	}
	if n.Tenant != "" {
		extra["tenant"] = n.Tenant
	}
	if len(extra) == 0 {
		return ""
	}
	b, err := json.Marshal(extra) // map keys are sorted -> deterministic
	if err != nil {
		return ""
	}
	return string(b)
}

// eventTime formats t as ServiceNow's "YYYY-MM-DD HH:MM:SS" (UTC) when set; empty
// omits it so ServiceNow stamps the event with its receive time.
func eventTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
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
