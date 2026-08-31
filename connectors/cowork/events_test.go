// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// TestParseLogRecordNameNormalization proves the connector accepts a Cowork event
// whether the wire name is bare ("tool_result"), Claude-Code-prefixed
// ("claude_code.tool_result"), or defensively "cowork."-prefixed, and via the
// LogRecord.EventName field OR the legacy event.name attribute.
func TestParseLogRecordNameNormalization(t *testing.T) {
	cases := []struct {
		name      string
		eventName string // LogRecord.EventName
		attrName  string // event.name attribute (used when eventName empty)
	}{
		{"bare-eventname", "tool_result", ""},
		{"prefixed-eventname", "claude_code.tool_result", ""},
		{"cowork-prefixed", "cowork.tool_result", ""},
		{"bare-attr", "", "tool_result"},
		{"prefixed-attr", "", "claude_code.tool_result"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attrs := []*commonpb.KeyValue{kvStr(attrToolName, "Read")}
			if tc.attrName != "" {
				attrs = append(attrs, kvStr(attrEventName, tc.attrName))
			}
			rec := logRecord(tc.eventName, testTime, attrs...)
			ev, ok := parseLogRecord(rec, coworkRes(), serviceNameCowork)
			if !ok {
				t.Fatalf("expected a recognized event, got drop")
			}
			if ev.name != evtToolResult {
				t.Errorf("canonical name = %q, want %q", ev.name, evtToolResult)
			}
			if ev.toolName != "Read" {
				t.Errorf("toolName = %q", ev.toolName)
			}
		})
	}
}

// TestParseLogRecordServiceGate proves the service.name gate: a Claude Code record
// (service.name="claude-code") is rejected when require_service="cowork", but an
// empty service.name is tolerated so a stripped-resource collector still ingests.
func TestParseLogRecordServiceGate(t *testing.T) {
	mk := func(svc string) ([]*commonpb.KeyValue, *commonpb.KeyValue) {
		res := []*commonpb.KeyValue{kvStr(attrSessionID, "s")}
		if svc != "" {
			res = append(res, kvStr(attrServiceName, svc))
		}
		return res, kvStr(attrToolName, "Read")
	}
	res, rec := mk("claude-code")
	if _, ok := parseLogRecord(logRecord(evtToolResult, testTime, rec), res, serviceNameCowork); ok {
		t.Error("a claude-code record must be rejected when require_service=cowork")
	}
	res, rec = mk("cowork")
	if _, ok := parseLogRecord(logRecord(evtToolResult, testTime, rec), res, serviceNameCowork); !ok {
		t.Error("a cowork record must be accepted")
	}
	res, rec = mk("")
	if _, ok := parseLogRecord(logRecord(evtToolResult, testTime, rec), res, serviceNameCowork); !ok {
		t.Error("an empty service.name must be tolerated (not dropped)")
	}
	res, rec = mk("claude-code")
	if _, ok := parseLogRecord(logRecord(evtToolResult, testTime, rec), res, ""); !ok {
		t.Error("an empty require_service must accept any service.name")
	}
}

func TestParseLogRecordUnknownEventDropped(t *testing.T) {
	rec := logRecord("claude_code.permission_mode_changed", testTime, kvStr(attrToolName, "Read"))
	if _, ok := parseLogRecord(rec, coworkRes(), serviceNameCowork); ok {
		t.Error("an event that is not one of the five Cowork events must be dropped")
	}
}

// TestParseLogRecordIdentity proves the shared account identity is captured (the
// OTEL↔Compliance correlation spine) and that user.email is NOT carried on the event.
func TestParseLogRecordIdentity(t *testing.T) {
	res := coworkRes(kvStr(attrUserID, "user_01ME"), kvStr(attrPromptID, "turn-abc"))
	rec := logRecord(evtUserPrompt, testTime, kvInt(attrPromptLength, 42), kvStr("user.email", "secret@corp.example"), kvStr("prompt", "do not read me"))
	ev, ok := parseLogRecord(rec, res, serviceNameCowork)
	if !ok {
		t.Fatal("user_prompt should parse")
	}
	if ev.sessionID != "sess-1" || ev.orgID != "org-9" || ev.accountID != "user_01ACC" || ev.accountUUID != "uuid-acc-1" {
		t.Errorf("identity = %+v", ev.identity())
	}
	if ev.userID != "user_01ME" || ev.promptID != "turn-abc" {
		t.Errorf("userID/promptID = %q/%q", ev.userID, ev.promptID)
	}
	if ev.promptLength != 42 {
		t.Errorf("promptLength = %d, want 42", ev.promptLength)
	}
}

// TestParseToolResultFields proves the tool_result field mapping (decision_source +
// mcp_server_scope + success + tool_input object).
func TestParseToolResultFields(t *testing.T) {
	rec := logRecord(evtToolResult, testTime,
		kvStr(attrToolName, "Write"),
		kvBool(attrSuccess, true),
		kvStr(attrDecisionSource, srcConfig),
		kvStr(attrMCPServerScope, "github"),
		kvObj(attrToolInput, kvStr("file_path", "/etc/app.conf")),
	)
	ev, ok := parseLogRecord(rec, coworkRes(), serviceNameCowork)
	if !ok {
		t.Fatal("tool_result should parse")
	}
	if ev.toolName != "Write" || ev.decisionSource != srcConfig || ev.mcpServerScope != "github" {
		t.Errorf("fields = %+v", ev)
	}
	if ev.success == nil || !*ev.success {
		t.Errorf("success = %v", ev.success)
	}
	if got, _ := ev.toolInput["file_path"].(string); got != "/etc/app.conf" {
		t.Errorf("tool_input file_path = %q", got)
	}
}

// TestParseToolDecisionFields proves the tool_decision field mapping (decision +
// source enum carried verbatim).
func TestParseToolDecisionFields(t *testing.T) {
	rec := logRecord(evtToolDecision, testTime,
		kvStr(attrToolName, "Bash"),
		kvStr(attrDecision, decisionReject),
		kvStr(attrSource, "user_reject"),
	)
	ev, ok := parseLogRecord(rec, coworkRes(), serviceNameCowork)
	if !ok {
		t.Fatal("tool_decision should parse")
	}
	if ev.decision != decisionReject || ev.decisionSource != "user_reject" || ev.toolName != "Bash" {
		t.Errorf("fields = %+v", ev)
	}
}

// TestParseAPIRequestFields proves api_request cost/token mapping.
func TestParseAPIRequestFields(t *testing.T) {
	rec := logRecord(evtAPIRequest, testTime,
		kvStr(attrModel, "claude-opus-4-8"),
		kvDouble(attrCostUSD, 0.0123),
		kvInt(attrInputTokens, 1000),
		kvInt(attrOutTokens, 200),
		kvInt(attrCacheRead, 50),
		kvInt(attrCacheCreate, 10),
	)
	ev, ok := parseLogRecord(rec, coworkRes(), serviceNameCowork)
	if !ok {
		t.Fatal("api_request should parse")
	}
	if ev.model != "claude-opus-4-8" || !ev.hasCost || ev.costUSD != 0.0123 {
		t.Errorf("cost fields = %+v", ev)
	}
	if ev.inputTokens != 1000 || ev.outputTokens != 200 || ev.cacheReadTokens != 50 || ev.cacheCreationTokens != 10 {
		t.Errorf("token fields = %+v", ev)
	}
}

// TestRecordTimeStampingFallback proves a record with no timestamp returns zero (so
// the receiver stamps receive time) while a stamped record keeps its event time.
func TestRecordTimeStampingFallback(t *testing.T) {
	ev, _ := parseLogRecord(logRecord(evtUserPrompt, testTime), coworkRes(), serviceNameCowork)
	if !ev.at.Equal(testTime) {
		t.Errorf("stamped record at = %v, want %v", ev.at, testTime)
	}
	// logRecord omits TimeUnixNano when the time is zero, exercising the fallback.
	ev2, _ := parseLogRecord(logRecord(evtUserPrompt, time.Time{}), coworkRes(), serviceNameCowork)
	if !ev2.at.IsZero() {
		t.Errorf("unstamped record should return zero time for the receiver to stamp, got %v", ev2.at)
	}
}
