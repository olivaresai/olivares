// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package jira is the Olivares AI output connector that creates and transitions issues
// in Atlassian Jira Cloud and raises requests in Jira Service Management (JSM) — the
// other ITSM system of record an enterprise estate runs incident governance in. It
// implements sdk.OutputConnector: each Notify turns the non-sensitive Notification into
// one Jira write over the shared reliable-delivery transport (internal/delivery).
//
// It speaks three surfaces, selected per-notification by Fields["jira_action"] (or the
// connector's default record_type):
//
//   - issue (create)     — POST /rest/api/3/issue (the default): a new issue in the
//     configured project, with the description carried as Atlassian Document Format
//     (ADF), which Jira Cloud REST v3 requires for rich-text fields.
//   - issue (transition) — POST /rest/api/3/issue/{key}/transitions when
//     Fields["jira_action"]=="transition" (advances an existing issue; requires
//     Fields["issue_key"] and Fields["transition_id"]).
//   - jsm                — POST /rest/servicedeskapi/request: a JSM customer request /
//     incident in the configured service desk + request type.
//
// Minimal-data / credential handling (docs/SECURITY-HARDENING.md-3): only the displayable Notification
// fields reach the wire. The operator credential is HTTP Basic (email + API token) or
// an OAuth 2.0 bearer token, declared Secret, held in memory only, applied as the
// Authorization header, and NEVER logged — the delivery transport never logs headers,
// and this package never puts the credential into an error (the Atlassian site URL is
// not itself a secret). It imports only the SDK and the Apache delivery transport.
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.jira"

// Default configuration values.
const (
	defaultMaxAttempts = 4
	defaultIssueType   = "Task"
	defaultRecordType  = recordIssue
	maxSummaryLen      = 254 // Jira summary caps at 255 chars.
)

// Record types / actions.
const (
	recordIssue = "issue"
	recordJSM   = "jsm"

	actionCreate     = "create"
	actionTransition = "transition"
)

// Config field keys.
const (
	cfgBaseURL        = "base_url"
	cfgAuthMode       = "auth_mode" // "basic" (default) or "bearer"
	cfgEmail          = "email"
	cfgAPIToken       = "api_token"
	cfgToken          = "token"
	cfgProjectKey     = "project_key"
	cfgIssueType      = "issue_type"
	cfgRecordType     = "record_type"
	cfgServiceDeskID  = "service_desk_id"
	cfgRequestTypeID  = "request_type_id"
	cfgMaxAttempts    = "max_attempts"
	fieldJiraAction   = "jira_action"   // "create" (default) or "transition"
	fieldIssueKey     = "issue_key"     // target of a transition
	fieldTransitionID = "transition_id" // workflow-/instance-specific id
)

// Auth modes.
const (
	authBasic  = "basic"
	authBearer = "bearer"
)

// Output is the Jira/JSM output connector. A single instance is opened once and
// services every Notify over a shared reliable-delivery client.
type Output struct {
	baseURL    string
	authHeader string // pre-built "Basic ..." / "Bearer ..."; in memory only, never logged
	projectKey string
	issueType  string
	recordType string
	serviceID  string
	requestID  string
	maxAtt     int

	client *delivery.Client
	doer   delivery.Doer
}

var _ sdk.OutputConnector = (*Output)(nil)

// New returns a Jira output connector with default configuration.
func New() *Output {
	return &Output{
		issueType:  defaultIssueType,
		recordType: defaultRecordType,
		maxAtt:     defaultMaxAttempts,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Jira / Jira Service Management",
		Description: "Creates and transitions Jira Cloud issues (REST v3, ADF descriptions) and raises Jira Service Management requests/incidents.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgBaseURL, Type: sdk.FieldString, Required: true, Description: "Atlassian site base URL, e.g. https://acme.atlassian.net (basic auth) or https://api.atlassian.com/ex/jira/{cloudid} (bearer)."},
			{Key: cfgAuthMode, Type: sdk.FieldString, Default: authBasic, Description: "Authentication mode: basic (email + API token) or bearer (OAuth 2.0 access token)."},
			{Key: cfgEmail, Type: sdk.FieldString, Secret: true, Description: "Atlassian account email for basic auth. Held in memory only, never logged."},
			{Key: cfgAPIToken, Type: sdk.FieldString, Secret: true, Description: "Atlassian API token (basic auth). Held in memory only, never logged."},
			{Key: cfgToken, Type: sdk.FieldString, Secret: true, Description: "OAuth 2.0 bearer access token (bearer mode). Held in memory only, never logged."},
			{Key: cfgProjectKey, Type: sdk.FieldString, Description: "Default Jira project key for created issues (e.g. OPS). Required for the issue record type."},
			{Key: cfgIssueType, Type: sdk.FieldString, Default: defaultIssueType, Description: "Default issue type name for created issues (e.g. Task, Incident, Bug)."},
			{Key: cfgRecordType, Type: sdk.FieldString, Default: defaultRecordType, Description: "Default record type: issue or jsm (override per-notification with Fields[\"jira_action\"])."},
			{Key: cfgServiceDeskID, Type: sdk.FieldString, Description: "JSM service desk id (required for the jsm record type)."},
			{Key: cfgRequestTypeID, Type: sdk.FieldString, Description: "JSM request type id (required for the jsm record type)."},
			{Key: cfgMaxAttempts, Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total delivery attempts including the first (transient failures only)."},
		},
	}
}

// Open resolves configuration, validates the auth credential and the record-type
// prerequisites, pre-builds the Authorization header, and builds the delivery client.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.baseURL = strings.TrimRight(strings.TrimSpace(cfg.Get(cfgBaseURL)), "/")
	if o.baseURL == "" {
		return fmt.Errorf("jira: %s is required", cfgBaseURL)
	}
	mode := authBasic
	if v := strings.TrimSpace(cfg.Get(cfgAuthMode)); v != "" {
		mode = strings.ToLower(v)
	}
	switch mode {
	case authBasic:
		email, tok := cfg.Get(cfgEmail), cfg.Get(cfgAPIToken)
		if email == "" || tok == "" {
			return fmt.Errorf("jira: basic auth requires %s and %s", cfgEmail, cfgAPIToken)
		}
		o.authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+tok))
	case authBearer:
		tok := cfg.Get(cfgToken)
		if tok == "" {
			return fmt.Errorf("jira: bearer auth requires %s", cfgToken)
		}
		o.authHeader = "Bearer " + tok
	default:
		return fmt.Errorf("jira: unknown auth_mode %q (want %q or %q)", mode, authBasic, authBearer)
	}
	o.projectKey = strings.TrimSpace(cfg.Get(cfgProjectKey))
	if v := strings.TrimSpace(cfg.Get(cfgIssueType)); v != "" {
		o.issueType = v
	}
	if v := strings.TrimSpace(cfg.Get(cfgRecordType)); v != "" {
		o.recordType = strings.ToLower(v)
	}
	switch o.recordType {
	case recordIssue:
		if o.projectKey == "" {
			return fmt.Errorf("jira: issue record type requires %s", cfgProjectKey)
		}
	case recordJSM:
		o.serviceID = strings.TrimSpace(cfg.Get(cfgServiceDeskID))
		o.requestID = strings.TrimSpace(cfg.Get(cfgRequestTypeID))
		if o.serviceID == "" || o.requestID == "" {
			return fmt.Errorf("jira: jsm record type requires %s and %s", cfgServiceDeskID, cfgRequestTypeID)
		}
	default:
		return fmt.Errorf("jira: unknown record_type %q (want %q or %q)", o.recordType, recordIssue, recordJSM)
	}
	o.maxAtt = cfg.GetInt(cfgMaxAttempts, o.maxAtt)

	o.client = delivery.New(o.doer, delivery.Options{MaxAttempts: o.maxAtt})
	return nil
}

// Notify maps the notification to a Jira write. The default action creates a record;
// Fields["jira_action"]=="transition" advances an existing issue instead.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("jira: Notify called before Open")
	}
	if strings.ToLower(strings.TrimSpace(n.Fields[fieldJiraAction])) == actionTransition {
		return o.transition(ctx, n)
	}
	if o.recordType == recordJSM {
		return o.createJSMRequest(ctx, n)
	}
	return o.createIssue(ctx, n)
}

// Close releases resources; this connector holds none beyond the shared client.
func (o *Output) Close(context.Context) error { return nil }

// --- issue create -----------------------------------------------------------------

type issueCreate struct {
	Fields issueFields `json:"fields"`
}

type issueFields struct {
	Project     map[string]string `json:"project"`
	Summary     string            `json:"summary"`
	IssueType   map[string]string `json:"issuetype"`
	Description json.RawMessage   `json:"description,omitempty"`
}

func (o *Output) createIssue(ctx context.Context, n sdk.Notification) error {
	body, err := json.Marshal(issueCreate{Fields: issueFields{
		Project:     map[string]string{"key": o.projectKey},
		Summary:     summary(n),
		IssueType:   map[string]string{"name": o.issueType},
		Description: adf(n.Body),
	}})
	if err != nil {
		return fmt.Errorf("jira: marshal issue: %w", err)
	}
	return o.send(ctx, "POST", "/rest/api/3/issue", body, "create issue")
}

// --- issue transition --------------------------------------------------------------

type transitionBody struct {
	Transition map[string]string `json:"transition"`
}

func (o *Output) transition(ctx context.Context, n sdk.Notification) error {
	key := strings.TrimSpace(n.Fields[fieldIssueKey])
	tid := strings.TrimSpace(n.Fields[fieldTransitionID])
	if key == "" || tid == "" {
		return fmt.Errorf("jira: transition requires Fields[%q] and Fields[%q]", fieldIssueKey, fieldTransitionID)
	}
	body, err := json.Marshal(transitionBody{Transition: map[string]string{"id": tid}})
	if err != nil {
		return fmt.Errorf("jira: marshal transition: %w", err)
	}
	// Jira fails a concurrent transition with 409 (moving from a legacy 400); the
	// delivery layer retries a 409? No — 409 is terminal there. A concurrent
	// transition is rare for a notification path; surface it for the engine to retry.
	return o.send(ctx, "POST", "/rest/api/3/issue/"+pathEscape(key)+"/transitions", body, "transition issue")
}

// --- JSM request -------------------------------------------------------------------

type jsmRequest struct {
	ServiceDeskID     string            `json:"serviceDeskId"`
	RequestTypeID     string            `json:"requestTypeId"`
	RequestFieldValue map[string]string `json:"requestFieldValues"`
}

func (o *Output) createJSMRequest(ctx context.Context, n sdk.Notification) error {
	fields := map[string]string{"summary": summary(n)}
	if n.Body != "" {
		fields["description"] = n.Body
	}
	body, err := json.Marshal(jsmRequest{
		ServiceDeskID:     o.serviceID,
		RequestTypeID:     o.requestID,
		RequestFieldValue: fields,
	})
	if err != nil {
		return fmt.Errorf("jira: marshal jsm request: %w", err)
	}
	return o.send(ctx, "POST", "/rest/servicedeskapi/request", body, "create jsm request")
}

// --- transport ---------------------------------------------------------------------

// send delivers a request and maps the outcome. Jira/JSM signal errors with proper
// status codes (a non-2xx is surfaced by delivery), so there is no 200-with-logical-
// error path. The site URL is not a secret; delivery's error carries only host +
// status + a bounded body excerpt, never the Authorization header.
func (o *Output) send(ctx context.Context, method, path string, body []byte, what string) error {
	res, err := o.client.Send(ctx, delivery.Request{
		Method: method,
		URL:    o.baseURL + path,
		Header: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "application/json",
			"Authorization": o.authHeader,
		},
		Body: body,
	})
	if err != nil {
		return fmt.Errorf("jira: %s: %w", what, err)
	}
	_ = res // 2xx; create=201, transition=204, jsm=201 — nothing further to inspect
	return nil
}

// summary returns the Title (falling back to the Body), truncated to the Jira summary
// limit. A wholly empty notification yields a stable placeholder.
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

// adf renders plain text as a minimal Atlassian Document Format document (Jira Cloud
// REST v3 requires ADF for rich-text fields like description; a plain string is
// rejected or coerced). An empty body yields nil so the description is omitted (an ADF
// text node may not be empty).
func adf(text string) json.RawMessage {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	doc := map[string]any{
		"version": 1,
		"type":    "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": text},
				},
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return b
}

// pathEscape escapes a path segment (an issue key is well-formed, but a defensive
// escape keeps a malformed key from breaking the URL).
func pathEscape(s string) string {
	return strings.ReplaceAll(s, "/", "%2F")
}

// truncate caps s at limit runes (not bytes).
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
