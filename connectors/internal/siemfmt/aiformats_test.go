// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func agentNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "agent read a restricted table",
		Body:     "claude session touched public.customers",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields: map[string]string{
			"model":         "claude-opus-4-8",
			"provider":      "anthropic",
			"agent":         "claude-code",
			"agent_id":      "ag-123",
			"session":       "sess-abc",
			"tool":          "Read",
			"mode":          "read",
			"input_tokens":  "1200",
			"output_tokens": "340",
			"resource":      "public.customers", // unrecognized -> preserved
		},
		Time: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC),
	}
}

func TestOCSFMapping(t *testing.T) {
	b, err := OCSF(DefaultDevice(), agentNotification())
	if err != nil {
		t.Fatalf("OCSF: %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("OCSF not valid JSON: %v\n%s", err, b)
	}
	num := func(k string) float64 {
		v, ok := ev[k].(float64)
		if !ok {
			t.Fatalf("OCSF[%q] not a number: %v", k, ev[k])
		}
		return v
	}
	if num("class_uid") != 6003 || num("category_uid") != 6 {
		t.Errorf("class/category wrong: %v / %v", ev["class_uid"], ev["category_uid"])
	}
	// type_uid = class_uid*100 + activity_id; mode=read -> activity_id 2.
	if num("activity_id") != 2 || num("type_uid") != 600302 {
		t.Errorf("activity/type_uid wrong: %v / %v", ev["activity_id"], ev["type_uid"])
	}
	// severity High -> severity_id 4.
	if num("severity_id") != 4 {
		t.Errorf("severity_id = %v, want 4", ev["severity_id"])
	}
	meta := ev["metadata"].(map[string]any)
	if meta["version"] != "1.8.0" {
		t.Errorf("metadata.version = %v, want 1.8.0", meta["version"])
	}
	aim := ev["ai_model"].(map[string]any)
	if aim["name"] != "claude-opus-4-8" || aim["ai_provider"] != "anthropic" {
		t.Errorf("ai_model wrong: %v", aim)
	}
	mc := ev["message_context"].(map[string]any)
	if mc["ai_role_id"].(float64) != 4 {
		t.Errorf("ai_role_id = %v, want 4 (Agent)", mc["ai_role_id"])
	}
	if mc["prompt_tokens"].(float64) != 1200 {
		t.Errorf("prompt_tokens = %v, want 1200", mc["prompt_tokens"])
	}
	// the 1.8.0 schema requires at_least_one of application/service inside
	// message_context — application = the initiating agent, service = the AI
	// provider endpoint.
	if app, ok := mc["application"].(map[string]any); !ok || app["name"] != "claude-code" {
		t.Errorf("message_context.application wrong: %v", mc["application"])
	}
	if svc, ok := mc["service"].(map[string]any); !ok || svc["name"] != "anthropic" {
		t.Errorf("message_context.service wrong: %v", mc["service"])
	}
	// cloud belonged to the (unapplied) cloud profile and must be gone;
	// the applied profile is declared instead.
	if _, ok := ev["cloud"]; ok {
		t.Error("cloud must not be emitted (unapplied profile)")
	}
	profs, _ := meta["profiles"].([]any)
	if len(profs) != 1 || profs[0] != "ai_operation" {
		t.Errorf("metadata.profiles must declare ai_operation: %v", meta["profiles"])
	}
	// OWASP AOS marker lives under unmapped, NOT on the native actor object.
	um := ev["unmapped"].(map[string]any)
	if um["actor.type_id"].(float64) != 99 || um["actor.type"] != "AI Agent" {
		t.Errorf("AOS marker missing/wrong in unmapped: %v", um)
	}
	// The unrecognized field is preserved, not dropped — parked under the caller
	// prefix, because unmapped is one flat map shared with encoder-owned markers
	// (actor.type_id, aos) that an unprefixed caller key could clobber.
	if um["caller.resource"] != "public.customers" {
		t.Errorf("unrecognized field not preserved: %v", um)
	}
	// Namespace freeze: the product keys use the reserved reverse-DNS
	// spelling, and no key in the container carries the retired bare one.
	if um["ai.olivares.event_type"] != "finding.reported" {
		t.Errorf("ai.olivares.event_type = %v, want the notification type", um["ai.olivares.event_type"])
	}
	if um["ai.olivares.tenant.id"] != "acme" {
		t.Errorf("ai.olivares.tenant.id = %v, want the authoritative tenant", um["ai.olivares.tenant.id"])
	}
	for key := range um {
		if strings.HasPrefix(key, "olivares.") {
			t.Errorf("bare pre-freeze key %q in OCSF unmapped", key)
		}
	}
	if _, ok := ev["actor"].(map[string]any)["type_id"]; ok {
		t.Errorf("type_id must NOT be on the native OCSF actor object")
	}
}

// TestOCSFPreservationContract pins the recognized-key contract on the OCSF
// path: a recognized field that cannot land on a schema column must
// surface under unmapped — provider without model/session/tokens, and
// model_version without a model name (carried via the shared encoder's
// invalid-ai_model parking).
func TestOCSFPreservationContract(t *testing.T) {
	encode := func(fields map[string]string) map[string]any {
		t.Helper()
		b, err := OCSF(DefaultDevice(), sdk.Notification{
			Type: "finding.reported", Title: "t", Severity: model.SeverityInfo,
			Fields: fields, Time: time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("OCSF: %v", err)
		}
		var ev map[string]any
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		return ev
	}

	// provider only: no ai_model (no name), no message_context (no session/
	// tokens) — the provider must ride unmapped, never vanish.
	ev := encode(map[string]string{"provider": "anthropic"})
	if _, has := ev["ai_model"]; has {
		t.Fatal("no model name -> no ai_model")
	}
	if un := ev["unmapped"].(map[string]any); un["caller.provider"] != "anthropic" {
		t.Fatalf("a provider with nowhere to land must ride unmapped: %v", un)
	}

	// model_version only: the version must be preserved (parked ai_model.version
	// via the shared encoder), not silently lost.
	ev = encode(map[string]string{"model_version": "2026-01"})
	if _, has := ev["ai_model"]; has {
		t.Fatal("a version-only ai_model violates 1.8.0 and must not be emitted")
	}
	if un := ev["unmapped"].(map[string]any); un["ai_model.version"] != "2026-01" {
		t.Fatalf("model_version without model must be parked, not lost: %v", un)
	}
}

// TestOCSFOperationFallsBackToType pins the api.operation fallback (fix):
// a tool-less notification must carry its TYPE as the operation, not the
// activity label.
func TestOCSFOperationFallsBackToType(t *testing.T) {
	b, err := OCSF(DefaultDevice(), sdk.Notification{
		Type: "finding.reported", Title: "t", Severity: model.SeverityInfo,
		Time: time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	if op := ev["api"].(map[string]any)["operation"]; op != "finding.reported" {
		t.Fatalf("api.operation = %v, want the notification type", op)
	}
}

func TestASIMMapping(t *testing.T) {
	b, err := ASIMAgentEvent(DefaultDevice(), agentNotification())
	if err != nil {
		t.Fatalf("ASIM: %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("ASIM not valid JSON: %v\n%s", err, b)
	}
	if ev["EventSchema"] != "AgentEvent" || ev["EventSchemaVersion"] != "0.1.0" {
		t.Errorf("schema/version wrong: %v / %v", ev["EventSchema"], ev["EventSchemaVersion"])
	}
	if ev["EventCount"].(float64) != 1 {
		t.Errorf("EventCount = %v, want 1", ev["EventCount"])
	}
	if ev["ModelName"] != "claude-opus-4-8" || ev["ModelProviderName"] != "anthropic" {
		t.Errorf("model columns wrong: %v / %v", ev["ModelName"], ev["ModelProviderName"])
	}
	if ev["InputTokensUsed"].(float64) != 1200 || ev["OutputTokensUsed"].(float64) != 340 {
		t.Errorf("token columns wrong: %v / %v", ev["InputTokensUsed"], ev["OutputTokensUsed"])
	}
	if ev["EventSessionId"] != "sess-abc" || ev["SrcAgentName"] != "claude-code" || ev["ToolName"] != "Read" {
		t.Errorf("agent/session/tool columns wrong: %v", ev)
	}
	add := ev["AdditionalFields"].(map[string]any)
	if add["Severity"] != "high" || add["resource"] != "public.customers" {
		t.Errorf("AdditionalFields wrong: %v", add)
	}
	// Columns NOT in the verified v0.1.0 schema must be ABSENT (no fabrication).
	for _, forbidden := range []string{"EventResult", "DvcAction", "ToolType", "ToolInvocationId", "EventStopReason"} {
		if _, ok := ev[forbidden]; ok {
			t.Errorf("ASIM must NOT emit %q (not in v0.1.0)", forbidden)
		}
	}
}

func TestOCSFOmitsAIModelWhenNoModel(t *testing.T) {
	n := sdk.Notification{Type: "x", Severity: model.SeverityInfo, Fields: map[string]string{}}
	b, _ := OCSF(DefaultDevice(), n)
	var ev map[string]any
	_ = json.Unmarshal(b, &ev)
	if _, ok := ev["ai_model"]; ok {
		t.Errorf("ai_model must be omitted when no model name (OCSF constraint)")
	}
	// Base structure still valid.
	if ev["class_uid"].(float64) != 6003 {
		t.Errorf("class_uid wrong")
	}
}
