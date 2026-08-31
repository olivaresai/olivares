// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"
)

func TestBuildDurableTaskGetParamsUsesFinalTasksExtension(t *testing.T) {
	params, err := BuildDurableTaskGetParams("task-final-1", revision20260728)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		TaskID string `json:"taskId"`
		Meta   struct {
			Version string `json:"io.modelcontextprotocol/protocolVersion"`
			Caps    struct {
				Extensions map[string]json.RawMessage `json:"extensions"`
			} `json:"io.modelcontextprotocol/clientCapabilities"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TaskID != "task-final-1" || decoded.Meta.Version != revision20260728 {
		t.Fatalf("unexpected tasks/get params: %s", params)
	}
	if _, ok := decoded.Meta.Caps.Extensions[extensionTasks]; !ok {
		t.Fatalf("tasks extension is not declared: %s", params)
	}
	headers := UpstreamRoutingHeaders(methodTasksGet, params)
	if headers[headerMCPProtocolVersion] != revision20260728 ||
		headers[headerMcpMethod] != methodTasksGet || headers[headerMcpName] != "task-final-1" {
		t.Fatalf("unexpected routing headers: %#v", headers)
	}
}

func TestBuildDurableTaskGetParamsRefusesUnimplementedRevision(t *testing.T) {
	if _, err := BuildDurableTaskGetParams("task-final-1", revision20251125); err == nil {
		t.Fatal("expected legacy Tasks wire shape to be refused")
	}
}

func TestParseDurableTaskGetResultReturnsStrictProjection(t *testing.T) {
	result := json.RawMessage(`{
		"resultType":"complete",
		"taskId":"task-final-1",
		"status":"input_required",
		"statusMessage":"approval required",
		"createdAt":"2026-08-20T10:00:00Z",
		"lastUpdatedAt":"2026-08-20T10:00:01Z",
		"ttlMs":60000,
		"pollIntervalMs":1000,
		"inputRequests":{"approval":{"type":"boolean"}}
	}`)
	report, err := ParseDurableTaskGetResult("task-final-1", result)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != taskStatusInputRequired || report.StatusReason != "approval required" ||
		report.Terminal || report.TTLMs == nil || *report.TTLMs != 60000 ||
		report.PollIntervalMs == nil || *report.PollIntervalMs != 1000 ||
		len(report.InputRequests) != 1 || report.ResultDigest == "" {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestParseDurableTaskGetResultKeepsPeerTextOutOfDefectClass(t *testing.T) {
	const marker = "peer-private-member"
	_, err := ParseDurableTaskGetResult("task-final-1", json.RawMessage(
		`{"resultType":"complete","taskId":"task-final-1","status":"working",`+
			`"createdAt":"2026-08-20T10:00:00Z","lastUpdatedAt":"2026-08-20T10:00:01Z",`+
			`"ttlMs":1000,"Status":"completed","`+marker+`":true}`,
	))
	if err == nil {
		t.Fatal("expected strict result rejection")
	}
	if got := DurableTaskResultDefectClass(err); got != defectGetResultAlias {
		t.Fatalf("defect class = %q, want %q", got, defectGetResultAlias)
	}
}
