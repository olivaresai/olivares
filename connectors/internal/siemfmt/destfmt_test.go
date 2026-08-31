// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func destNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "agent blocked from secret",
		Body:     "claude-1 denied write to vault.path/db",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields: map[string]string{
			"agent": "claude-1", "actor": "svc-bot", "resource": "vault.path/db",
			"tool": "vault.write", "decision": "deny", "owasp_llm": "LLM06:2025",
		},
		Time: time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

func TestECSShape(t *testing.T) {
	b, err := ECS(DefaultDevice(), destNotification())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("ECS output is not valid JSON: %v", err)
	}
	if doc["@timestamp"] != "2026-06-06T10:00:00Z" {
		t.Errorf("@timestamp = %v", doc["@timestamp"])
	}
	ecs, _ := doc["ecs"].(map[string]any)
	if ecs["version"] != ECSVersion {
		t.Errorf("ecs.version = %v, want %s", ecs["version"], ECSVersion)
	}
	ev, _ := doc["event"].(map[string]any)
	if ev["kind"] != "alert" {
		t.Errorf("event.kind = %v, want alert (finding)", ev["kind"])
	}
	// event.severity is an ECS long; high => 7. JSON numbers decode as float64.
	if got, _ := ev["severity"].(float64); got != 7 {
		t.Errorf("event.severity = %v, want 7 (high)", ev["severity"])
	}
	labels, _ := doc["labels"].(map[string]any)
	if labels["owasp_llm"] != "LLM06:2025" {
		t.Errorf("labels.owasp_llm = %v", labels["owasp_llm"])
	}
	if labels["tenant"] != "acme" {
		t.Errorf("labels.tenant = %v, want acme (appended)", labels["tenant"])
	}
	// event.category/type must be ABSENT (not fabricated).
	if _, ok := ev["category"]; ok {
		t.Errorf("event.category should be omitted, not fabricated")
	}
}

func TestECSDeterministic(t *testing.T) {
	n := destNotification()
	a, _ := ECS(DefaultDevice(), n)
	b, _ := ECS(DefaultDevice(), n)
	if string(a) != string(b) {
		t.Errorf("ECS output not byte-deterministic")
	}
}

func TestUDMShape(t *testing.T) {
	b, err := UDM(DefaultDevice(), UDMOptions{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, destNotification())
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("UDM output is not valid JSON: %v", err)
	}
	meta, _ := ev["metadata"].(map[string]any)
	if meta["eventTimestamp"] != "2026-06-06T10:00:00Z" {
		t.Errorf("metadata.eventTimestamp = %v", meta["eventTimestamp"])
	}
	if meta["eventType"] != UDMEventType {
		t.Errorf("metadata.eventType = %v, want %s", meta["eventType"], UDMEventType)
	}
	if meta["vendorName"] != "Olivares.AI" {
		t.Errorf("metadata.vendorName = %v", meta["vendorName"])
	}
	principal, _ := ev["principal"].(map[string]any)
	if principal["application"] != "claude-1" {
		t.Errorf("principal.application = %v, want claude-1", principal["application"])
	}
	if u, _ := principal["user"].(map[string]any); u["userid"] != "svc-bot" {
		t.Errorf("principal.user.userid = %v, want svc-bot", principal["user"])
	}
	target, _ := ev["target"].(map[string]any)
	if r, _ := target["resource"].(map[string]any); r["name"] != "vault.path/db" {
		t.Errorf("target.resource.name = %v", target["resource"])
	}
	sr, _ := ev["securityResult"].([]any)
	if len(sr) != 1 {
		t.Fatalf("securityResult should be a 1-element array, got %v", ev["securityResult"])
	}
	sr0, _ := sr[0].(map[string]any)
	if sr0["severity"] != "HIGH" {
		t.Errorf("securityResult[0].severity = %v, want HIGH", sr0["severity"])
	}
	action, _ := sr0["action"].([]any)
	if len(action) != 1 || action[0] != "BLOCK" {
		t.Errorf("securityResult[0].action = %v, want [BLOCK]", sr0["action"])
	}
	add, _ := ev["additional"].(map[string]any)
	if add["owasp_llm"] != "LLM06:2025" {
		t.Errorf("additional.owasp_llm = %v", add["owasp_llm"])
	}
	if add["tenant"] != "acme" {
		t.Errorf("additional.tenant = %v", add["tenant"])
	}
}

func TestUDMTimestampFallback(t *testing.T) {
	n := destNotification()
	n.Time = time.Time{} // no event time
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	b, _ := UDM(DefaultDevice(), UDMOptions{Now: now}, n)
	var ev map[string]any
	_ = json.Unmarshal(b, &ev)
	meta, _ := ev["metadata"].(map[string]any)
	if meta["eventTimestamp"] != "2026-06-06T12:00:00Z" {
		t.Errorf("fallback eventTimestamp = %v, want the opts.Now value", meta["eventTimestamp"])
	}
}

func TestUDMSeverityEnum(t *testing.T) {
	cases := map[model.Severity]string{
		model.SeverityInfo:     "INFORMATIONAL",
		model.SeverityCritical: "CRITICAL",
	}
	for sev, want := range cases {
		if got := mapSeverity(sev).udm; got != want {
			t.Errorf("udm severity for %v = %q, want %q", sev, got, want)
		}
	}
}
