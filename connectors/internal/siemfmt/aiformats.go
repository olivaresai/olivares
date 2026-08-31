// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// This file maps an sdk.Notification onto the two AI-aware SIEM schemas an
// enterprise SOC expects for agent telemetry: OCSF v1.8.0 (ai_operation profile,
// JSON, for a SOC that ACCEPTS 1.8.0 — NOT Amazon Security Lake, whose custom
// sources cap at OCSF 1.3 in Parquet; a declared gap, not an oversight, OBS-02)
// and Microsoft Sentinel ASIM Agent Event v0.1.0 (for Sentinel/Defender,
// OBS-07). Both are pinned and verified against
// their primary sources re-fetched the official OCSF 1.8.0 ai_operation
// class exports on 2026-07-05 and found them byte-identical to the vendored
// schemas. ASIM is pre-1.0 (v0.1.0), and OCSF's ai_operation is new in 1.8.0 —
// neither is treated as a stable contract.
//
// Mapping contract (documented for): the encoders read a fixed set of
// well-known Notification.Fields keys and map them onto schema columns; any field
// NOT recognized is preserved (OCSF `unmapped`, ASIM `AdditionalFields`) so a SOC
// never silently loses a dimension. Minimal-data still holds: a Notification
// already carries only non-sensitive structural fields (docs/SECURITY-HARDENING.md).

// Recognized Notification.Fields keys (the documented mapping surface).
const (
	fieldModel         = "model"
	fieldModelVersion  = "model_version"
	fieldProvider      = "provider"
	fieldAgent         = "agent"
	fieldAgentID       = "agent_id"
	fieldSession       = "session"
	fieldTool          = "tool"
	fieldToolID        = "tool_id"
	fieldActor         = "actor"
	fieldMode          = "mode"
	fieldDecision      = "decision"
	fieldInputTokens   = "input_tokens"
	fieldOutputTokens  = "output_tokens"
	fieldThoughtDetail = "thought_process"
)

// firstField returns the first present, non-empty value among keys.
func firstField(n sdk.Notification, keys ...string) string {
	for _, k := range keys {
		if v, ok := n.Fields[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

func intField(n sdk.Notification, key string) *int64 {
	v, ok := n.Fields[key]
	if !ok || v == "" {
		return nil
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &i
}

// ocsfSeverityID maps the product severity onto the OCSF severity_id enum (1.8.0).
func ocsfSeverityID(s model.Severity) int {
	switch s {
	case model.SeverityInfo:
		return 1 // Informational
	case model.SeverityLow:
		return 2
	case model.SeverityMedium:
		return 3
	case model.SeverityHigh:
		return 4
	case model.SeverityCritical:
		return 5
	default:
		return 0 // Unknown
	}
}

// ocsfActivity maps an access mode to the OCSF 6003 activity_id (and label).
func ocsfActivity(mode string) (int, string) {
	switch mode {
	case "read":
		return 2, "Read"
	case "write", "readwrite", "update":
		return 3, "Update"
	case "create":
		return 1, "Create"
	case "delete":
		return 4, "Delete"
	default:
		return 99, "Other"
	}
}

// ocsfStatus maps a decision/outcome to the OCSF status_id (0 = omit).
func ocsfStatus(decision string) int {
	switch decision {
	case "allow", "accept", "success", "succeeded":
		return 1 // Success
	case "deny", "reject", "blocked", "block", "failure", "error", "denied":
		return 2 // Failure
	default:
		return 0
	}
}

// OCSF encodes n as an OCSF v1.8.0 API Activity (6003) event with the ai_operation
// profile, via the shared sdk/siemwire encoder (so the findings feed and the audit
// ledger emit identical OCSF). 6003 REGISTERS the profile in 1.8.0 (verified
// 2026-07-05, alongside process_activity 1007 and datastore_activity
// 6005) and is the class that models these events (agent/tool/API operations).
// Unrecognized fields ride under `unmapped`; a provider with no model rides there
// too (ai_model requires name + ai_provider in 1.8.0, never emitted incomplete).
func OCSF(d Device, n sdk.Notification) ([]byte, error) {
	dev := d.orDefault()
	mode := n.Fields[fieldMode]
	actID, actName := ocsfActivity(mode)
	session := firstField(n, fieldSession)
	provider := firstField(n, fieldProvider)
	agent := firstField(n, fieldAgent, fieldActor)

	// api.operation: the tool when named, else the notification TYPE — n.Type
	// is a VALUE fallback, not a Fields key (fix: it was passed to
	// firstField as a key, so the type fallback never fired and a tool-less
	// notification degraded to the activity label).
	op := firstField(n, fieldTool)
	if op == "" {
		op = n.Type
	}

	in := siemwire.OCSFInput{
		ActivityID:   actID,
		ActivityName: actName,
		SeverityID:   ocsfSeverityID(n.Severity),
		StatusID:     ocsfStatus(n.Fields[fieldDecision]),
		Time:         n.Time,
		Message:      n.Title,
		Device:       siemwire.Device{Vendor: dev.Vendor, Product: dev.Product, Version: dev.Version},
		Operation:    op,
		ActorAppName: agent,
		SrcName:      firstField(n, fieldAgent, fieldSession),
	}

	// The ai_model is built whenever the notification carries ANY model fact —
	// name OR version (fix: version-only used to vanish, since the version
	// is recognized, excluded from unmapped, and was read only under a non-empty
	// name). A version-only model violates the 1.8.0 constraint, so the shared
	// encoder parks it under `unmapped` — preserved, never emitted invalid.
	if name, ver := firstField(n, fieldModel), firstField(n, fieldModelVersion); name != "" || ver != "" {
		in.AIModel = &siemwire.OCSFAIModel{
			Name:       name,
			AIProvider: provider,
			Version:    ver,
		}
	}
	// message_context carries the OCSF-native Agent role + token accounting when
	// known. The 1.8.0 schema requires at_least_one of application/service inside
	// it (verified): application is the initiating agent/framework, service the
	// AI provider endpoint handling the request. When neither is nameable the
	// shared encoder parks the context under `unmapped` (never emitted invalid).
	if session != "" || n.Fields[fieldInputTokens] != "" || n.Fields[fieldOutputTokens] != "" {
		mc := &siemwire.OCSFMessageContext{UID: session, AIRoleID: siemwire.OCSFRoleAgent, AIRole: "Agent"}
		if agent != "" {
			mc.Application = &siemwire.OCSFApplication{Name: agent}
		}
		if provider != "" {
			mc.Service = &siemwire.OCSFService{Name: provider}
		}
		mc.PromptTokens = intField(n, fieldInputTokens)
		mc.CompletionTokens = intField(n, fieldOutputTokens)
		in.MessageContext = mc
	}

	in.Unmapped = ocsfUnmapped(n)
	// A provider that ended up neither in ai_model nor in message_context.service
	// must not vanish (the recognized-key contract: nothing is silently lost). It
	// is the CALLER's value, so it parks under the caller prefix like any other
	// preserved field — never under the reserved product namespace.
	if provider != "" && in.AIModel == nil && in.MessageContext == nil {
		in.Unmapped[otlpCallerPrefix+fieldProvider] = provider
	}
	return siemwire.OCSF(in)
}

// ocsfUnmapped collects the notification's non-schema fields (plus type/tenant) so
// the OCSF event preserves everything the recognized-key mapping did not consume.
func ocsfUnmapped(n sdk.Notification) map[string]any {
	recognized := map[string]bool{
		fieldModel: true, fieldModelVersion: true, fieldProvider: true, fieldAgent: true,
		fieldAgentID: true, fieldSession: true, fieldTool: true, fieldToolID: true,
		fieldActor: true, fieldMode: true, fieldDecision: true, fieldInputTokens: true,
		fieldOutputTokens: true, fieldThoughtDetail: true,
	}
	// Namespace freeze: product-authored keys live under the reserved
	// ai.olivares.* reverse-DNS namespace (the pre-freeze bare olivares.* spelling,
	// read as reverse DNS, claimed the TLD "audit"/"tenant"). Caller-supplied
	// fields follow a DELIBERATE per-schema policy (TestCallerFieldSpellingAcrossOCSFAndOTLP): here EVERY preserved caller
	// field parks under the caller prefix, because unmapped is one flat map
	// shared with encoder-owned markers (actor.type_id, aos) that an unprefixed
	// caller key could silently clobber — while the OTLP projection keeps an
	// ordinary caller key's natural spelling and quarantines only the live and
	// retired product namespaces. The tenant key reuses otlpAttrTenant — one
	// product-wide spelling for the authoritative tenant (it also names the OTLP
	// resource attribute; the constant's name is historical, the value is
	// format-neutral).
	out := map[string]any{}
	if n.Type != "" {
		out["ai.olivares.event_type"] = n.Type
	}
	if n.Tenant != "" {
		out[otlpAttrTenant] = n.Tenant
	}
	for k, v := range n.Fields {
		if k == "" || recognized[k] {
			continue
		}
		out[otlpCallerPrefix+k] = v
	}
	return out
}

// --- Microsoft Sentinel ASIM Agent Event (v0.1.0) ----------------------------

// ASIMSchema / ASIMSchemaVersion pin the Microsoft Sentinel ASIM Agent Event schema
// this emitter targets. The schema is pre-1.0 (v0.1.0), verified against
// https://learn.microsoft.com/en-us/azure/sentinel/normalization-schema-agent; the
// union parser is _Im_AgentEvent. EventType is intentionally NOT enumerated by the
// schema, so we pass the notification type through verbatim.
const (
	ASIMSchema        = "AgentEvent"
	ASIMSchemaVersion = "0.1.0"
)

// asimAgentEvent is the JSON shape of one ASIM Agent Event row. Only columns that
// exist in the verified v0.1.0 schema are present — EventResult, DvcAction,
// ToolType, ToolInvocationId and conversation/turn columns are deliberately ABSENT
// because they are not in v0.1.0 (verified). Field order is the declaration order,
// so json.Marshal is deterministic.
type asimAgentEvent struct {
	EventCount                 int               `json:"EventCount"`
	EventStartTime             string            `json:"EventStartTime,omitempty"`
	EventEndTime               string            `json:"EventEndTime,omitempty"`
	TimeGenerated              string            `json:"TimeGenerated,omitempty"`
	EventType                  string            `json:"EventType,omitempty"`
	EventOriginalType          string            `json:"EventOriginalType,omitempty"`
	EventProduct               string            `json:"EventProduct"`
	EventVendor                string            `json:"EventVendor"`
	EventSchema                string            `json:"EventSchema"`
	EventSchemaVersion         string            `json:"EventSchemaVersion"`
	EventSessionID             string            `json:"EventSessionId,omitempty"`
	SrcAgentID                 string            `json:"SrcAgentId,omitempty"`
	SrcAgentName               string            `json:"SrcAgentName,omitempty"`
	ActorUsername              string            `json:"ActorUsername,omitempty"`
	ModelProviderName          string            `json:"ModelProviderName,omitempty"`
	ModelName                  string            `json:"ModelName,omitempty"`
	InputTokensUsed            *int64            `json:"InputTokensUsed,omitempty"`
	OutputTokensUsed           *int64            `json:"OutputTokensUsed,omitempty"`
	ToolID                     string            `json:"ToolId,omitempty"`
	ToolName                   string            `json:"ToolName,omitempty"`
	EventThoughtProcessDetails string            `json:"EventThoughtProcessDetails,omitempty"`
	AdditionalFields           map[string]string `json:"AdditionalFields,omitempty"`
}

// ASIMAgentEvent encodes n as a Microsoft Sentinel ASIM Agent Event (v0.1.0) JSON
// row mapping onto _Im_AgentEvent. Severity (which v0.1.0 has no column for) and any
// unrecognized field ride in AdditionalFields, so nothing is dropped.
func ASIMAgentEvent(d Device, n sdk.Notification) ([]byte, error) {
	dev := d.orDefault()
	ts := ""
	if !n.Time.IsZero() {
		ts = n.Time.UTC().Format(time.RFC3339)
	}
	ev := asimAgentEvent{
		EventCount:                 1,
		EventStartTime:             ts,
		EventEndTime:               ts,
		TimeGenerated:              ts,
		EventType:                  n.Type,
		EventOriginalType:          n.Type,
		EventProduct:               dev.Product,
		EventVendor:                dev.Vendor,
		EventSchema:                ASIMSchema,
		EventSchemaVersion:         ASIMSchemaVersion,
		EventSessionID:             firstField(n, fieldSession),
		SrcAgentID:                 firstField(n, fieldAgentID),
		SrcAgentName:               firstField(n, fieldAgent),
		ActorUsername:              firstField(n, fieldActor),
		ModelProviderName:          firstField(n, fieldProvider),
		ModelName:                  firstField(n, fieldModel),
		InputTokensUsed:            intField(n, fieldInputTokens),
		OutputTokensUsed:           intField(n, fieldOutputTokens),
		ToolID:                     firstField(n, fieldToolID),
		ToolName:                   firstField(n, fieldTool),
		EventThoughtProcessDetails: firstField(n, fieldThoughtDetail),
		AdditionalFields:           asimAdditional(n),
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("siemfmt: marshal ASIM agent event: %w", err)
	}
	return b, nil
}

// asimAdditional collects severity, title/body, tenant and any unrecognized fields
// into the ASIM AdditionalFields bag.
func asimAdditional(n sdk.Notification) map[string]string {
	recognized := map[string]bool{
		fieldAgent: true, fieldAgentID: true, fieldSession: true, fieldActor: true,
		fieldProvider: true, fieldModel: true, fieldInputTokens: true, fieldOutputTokens: true,
		fieldTool: true, fieldToolID: true, fieldThoughtDetail: true,
	}
	out := map[string]string{}
	if s := severityLabel(n.Severity); s != "" {
		out["Severity"] = s
	}
	if n.Title != "" {
		out["Title"] = n.Title
	}
	if n.Body != "" {
		out["Body"] = n.Body
	}
	if n.Tenant != "" {
		out["Tenant"] = n.Tenant
	}
	for k, v := range n.Fields {
		if k == "" || recognized[k] {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// severityLabel renders the model severity as a lowercase label (or "" if empty).
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
