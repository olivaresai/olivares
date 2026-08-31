// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

var testTime = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

func TestParseToolResult(t *testing.T) {
	rec := logRecord(evtToolResult, testTime,
		kvStr(attrToolName, "Read"),
		kvStr(attrToolUseID, "toolu_123"),
		kvBool(attrSuccess, true),
		kvObj(attrToolInput, kvStr("file_path", "/etc/passwd")),
	)
	res := []*commonpb.KeyValue{kvStr(attrSessionID, "sess-1"), kvStr(attrAccountUUID, "acct-9")}
	ev, ok := parseLogRecord(rec, res)
	if !ok {
		t.Fatal("parse failed")
	}
	if ev.name != evtToolResult || ev.sessionID != "sess-1" || ev.toolName != "Read" || ev.toolUseID != "toolu_123" {
		t.Errorf("bad parse: %+v", ev)
	}
	if ev.success == nil || !*ev.success {
		t.Error("success not parsed")
	}
	if ev.toolInput["file_path"] != "/etc/passwd" {
		t.Errorf("tool_input not parsed: %v", ev.toolInput)
	}
	if !ev.at.Equal(testTime) {
		t.Errorf("time = %v", ev.at)
	}
	if ev.originRef() != "sess-1" {
		t.Errorf("originRef = %q", ev.originRef())
	}
}

func TestParseToolResultViaToolParameters(t *testing.T) {
	// When tool_input is absent, the tool_parameters attribute is the fallback
	// carrier for the structured input.
	rec := logRecord(evtToolResult, testTime,
		kvStr(attrToolName, "Read"),
		kvObj(attrToolParams, kvStr("file_path", "/x")),
	)
	ev, ok := parseLogRecord(rec, nil)
	if !ok || ev.toolInput == nil || ev.toolInput["file_path"] != "/x" {
		t.Errorf("tool_parameters fallback not parsed: %+v", ev.toolInput)
	}
}

func TestParseEventNameAttributeFallback(t *testing.T) {
	// No first-class EventName; only the legacy event.name attribute.
	rec := &logspb.LogRecord{
		TimeUnixNano: uint64(testTime.UnixNano()),
		Attributes: []*commonpb.KeyValue{
			kvStr(attrEventName, evtToolResult),
			kvStr(attrToolName, "Write"),
		},
	}
	ev, ok := parseLogRecord(rec, nil)
	if !ok || ev.name != evtToolResult || ev.toolName != "Write" {
		t.Errorf("event.name fallback failed: %+v ok=%v", ev, ok)
	}
}

func TestParseAPIRequest(t *testing.T) {
	rec := logRecord(evtAPIRequest, testTime,
		kvStr(attrModel, "claude-opus-4-8"),
		kvDouble(attrCostUSD, 0.0123),
		kvInt(attrInputTokens, 1200),
		kvInt(attrOutputTokens, 340),
	)
	ev, _ := parseLogRecord(rec, []*commonpb.KeyValue{kvStr(attrSessionID, "s")})
	if ev.model != "claude-opus-4-8" || !ev.hasCost || ev.costUSD != 0.0123 {
		t.Errorf("api_request cost parse: %+v", ev)
	}
	if ev.inputTokens != 1200 || ev.outputTokens != 340 {
		t.Errorf("token parse: %+v", ev)
	}
}

func TestParseMCPConnection(t *testing.T) {
	rec := logRecord(evtMCPConnection, testTime,
		kvStr(attrStatus, "failed"),
		kvStr(attrTransportType, "stdio"),
		kvStr(attrServerScope, "project"),
		kvStr(attrErrorCode, "ECONN"),
	)
	ev, _ := parseLogRecord(rec, nil)
	if ev.mcpStatus != "failed" || ev.mcpTransport != "stdio" || ev.mcpScope != "project" || ev.mcpErrorCode != "ECONN" {
		t.Errorf("mcp parse: %+v", ev)
	}
}

func TestParseNoEventName(t *testing.T) {
	rec := &logspb.LogRecord{Attributes: []*commonpb.KeyValue{kvStr("x", "y")}}
	if _, ok := parseLogRecord(rec, nil); ok {
		t.Error("record with no event name must not parse")
	}
	if _, ok := parseLogRecord(nil, nil); ok {
		t.Error("nil record must not parse")
	}
}

func TestRecordTimeFallback(t *testing.T) {
	obs := &logspb.LogRecord{ObservedTimeUnixNano: uint64(testTime.UnixNano())}
	if got := recordTime(obs); !got.Equal(testTime) {
		t.Errorf("observed-time fallback = %v", got)
	}
	if got := recordTime(&logspb.LogRecord{}); !got.IsZero() {
		t.Errorf("zero record should yield zero time, got %v", got)
	}
}
