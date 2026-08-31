// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import "testing"

func aosTestInput(decision *AOSDecision) OCSFInput {
	return OCSFInput{
		// The zero activity is intentionally supplied: AOS selects the verified
		// API Activity / Create mapping 6003/6/1/600301.
		SeverityID:   2,
		StatusID:     1,
		Time:         ocsfTestTime,
		Device:       Device{Vendor: "Olivares.AI", Product: "ControlPlane", Version: "1"},
		ActorAppName: "agent-ref:billing-guardian",
		SrcName:      "session-ref:sess-a4f2",
		AOS: &AOSTrace{
			Agent: AOSAgent{
				ID:      "agent-ref:billing-guardian",
				Name:    "billing-guardian",
				Version: "2026.07",
			},
			SessionID: "session-ref:sess-a4f2",
			Step: AOSStep{
				ID:            "step-ref:stp-07",
				Type:          "toolCall",
				TurnID:        "turn-ref:turn-03",
				OperationType: "policy_enforcement",
			},
			Decision: decision,
		},
		Unmapped: map[string]any{
			"ai.olivares.trace_ref": "sha256:2a2b",
			"aos":                   map[string]any{"profile_revision": "0.1-public-preview"},
		},
	}
}

func requireObject(t *testing.T, value any, path string) map[string]any {
	t.Helper()
	obj, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s must be an object, got %T", path, value)
	}
	return obj
}

func requireExactKeys(t *testing.T, obj map[string]any, path string, keys ...string) {
	t.Helper()
	want := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		want[key] = struct{}{}
	}
	for key := range obj {
		if _, ok := want[key]; !ok {
			t.Errorf("%s contains unexpected key %q", path, key)
		}
	}
	for key := range want {
		if _, ok := obj[key]; !ok {
			t.Errorf("%s is missing key %q", path, key)
		}
	}
}

func assertNoAOSPayloadKeys(t *testing.T, value any, path string) {
	t.Helper()
	forbidden := map[string]struct{}{
		"api_key": {}, "arguments": {}, "content": {}, "credentials": {},
		"inputs": {}, "outputs": {}, "payload": {}, "secret": {}, "user": {},
	}
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if _, found := forbidden[key]; found {
				t.Errorf("%s contains forbidden payload/PII key %q", path, key)
			}
			assertNoAOSPayloadKeys(t, child, path+"."+key)
		}
	case []any:
		for _, child := range v {
			assertNoAOSPayloadKeys(t, child, path+"[]")
		}
	}
}

// TestOCSFAOSDenyDecisionShape pins the v0.1 public-preview AOS
// session/decision subset while also proving that the OCSF 1.8.0 envelope still
// validates and the historical actor marker remains under unmapped.
func TestOCSFAOSDenyDecisionShape(t *testing.T) {
	in := aosTestInput(&AOSDecision{
		Decision:   AOSDecisionDeny,
		Reasoning:  "PEP policy disallows the requested write scope",
		ReasonCode: "policy.scope.write_denied",
		Message:    "Request denied by policy",
		// A caller cannot make a deny event leak a modified request: the
		// encoder emits this field only for the modify verdict.
		ModifiedRequest: &AOSRequestReference{
			Ref:    "request-ref:req-42",
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	})
	ev := mustValidate(t, "api_activity", in)
	if ev["class_uid"] != float64(6003) || ev["category_uid"] != float64(6) ||
		ev["activity_id"] != float64(1) || ev["type_uid"] != float64(600301) {
		t.Fatalf("AOS base mapping must be 6003/6/1/600301, got %v/%v/%v/%v",
			ev["class_uid"], ev["category_uid"], ev["activity_id"], ev["type_uid"])
	}

	actor := requireObject(t, ev["actor"], "actor")
	if _, moved := actor["user"]; moved {
		t.Fatal("AOS actor.type_id=99 marker must not move to actor.user")
	}
	unmapped := requireObject(t, ev["unmapped"], "unmapped")
	if unmapped["actor.type_id"] != float64(99) || unmapped["actor.type"] != "AI Agent" {
		t.Fatalf("historical AOS actor marker changed: %v", unmapped)
	}
	if unmapped["ai.olivares.trace_ref"] != "sha256:2a2b" {
		t.Fatalf("caller unmapped entry was not merged: %v", unmapped)
	}

	aos := requireObject(t, unmapped["aos"], "unmapped.aos")
	requireExactKeys(t, aos, "unmapped.aos", "context", "step", "decision", "profile_revision")
	if aos["profile_revision"] != "0.1-public-preview" {
		t.Fatalf("caller AOS extension was not merged: %v", aos)
	}
	context := requireObject(t, aos["context"], "unmapped.aos.context")
	requireExactKeys(t, context, "unmapped.aos.context", "agent", "session")
	agent := requireObject(t, context["agent"], "unmapped.aos.context.agent")
	requireExactKeys(t, agent, "unmapped.aos.context.agent", "id", "name", "version")
	session := requireObject(t, context["session"], "unmapped.aos.context.session")
	requireExactKeys(t, session, "unmapped.aos.context.session", "id")
	if agent["id"] != "agent-ref:billing-guardian" || agent["name"] != "billing-guardian" ||
		agent["version"] != "2026.07" || session["id"] != "session-ref:sess-a4f2" {
		t.Fatalf("AOS agent/session context changed: agent=%v session=%v", agent, session)
	}

	step := requireObject(t, aos["step"], "unmapped.aos.step")
	requireExactKeys(t, step, "unmapped.aos.step", "id", "type", "turn_id", "operation")
	operation := requireObject(t, step["operation"], "unmapped.aos.step.operation")
	requireExactKeys(t, operation, "unmapped.aos.step.operation", "type")
	if step["id"] != "step-ref:stp-07" || step["type"] != "toolCall" ||
		step["turn_id"] != "turn-ref:turn-03" || operation["type"] != "policy_enforcement" {
		t.Fatalf("AOS step context changed: step=%v operation=%v", step, operation)
	}

	decision := requireObject(t, aos["decision"], "unmapped.aos.decision")
	requireExactKeys(t, decision, "unmapped.aos.decision", "decision", "reasoning", "reasonCode", "message")
	if decision["decision"] != "deny" ||
		decision["reasoning"] != "PEP policy disallows the requested write scope" ||
		decision["reasonCode"] != "policy.scope.write_denied" ||
		decision["message"] != "Request denied by policy" {
		t.Fatalf("deny decision attributes changed: %v", decision)
	}
	if _, leaked := decision["modifiedRequest"]; leaked {
		t.Fatal("modifiedRequest must not appear on a deny decision")
	}
	assertNoAOSPayloadKeys(t, aos, "unmapped.aos")
}

func TestOCSFAOSModifyIncludesOnlyRequestReference(t *testing.T) {
	in := aosTestInput(&AOSDecision{
		Decision:   AOSDecisionModify,
		Reasoning:  "PEP policy narrowed the requested scope",
		ReasonCode: "policy.scope.narrowed",
		Message:    "Request modified by policy",
		ModifiedRequest: &AOSRequestReference{
			Ref:    "request-ref:req-43",
			SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	})
	ev := mustValidate(t, "api_activity", in)
	aos := requireObject(t, requireObject(t, ev["unmapped"], "unmapped")["aos"], "unmapped.aos")
	decision := requireObject(t, aos["decision"], "unmapped.aos.decision")
	requireExactKeys(t, decision, "unmapped.aos.decision",
		"decision", "reasoning", "reasonCode", "message", "modifiedRequest")
	modified := requireObject(t, decision["modifiedRequest"], "unmapped.aos.decision.modifiedRequest")
	requireExactKeys(t, modified, "unmapped.aos.decision.modifiedRequest", "ref", "sha256")
	if modified["ref"] != "request-ref:req-43" ||
		modified["sha256"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("modified request must contain only its opaque reference/fingerprint: %v", modified)
	}
	assertNoAOSPayloadKeys(t, aos, "unmapped.aos")
}

func TestOCSFAOSSessionShapeWithoutDecision(t *testing.T) {
	ev := mustValidate(t, "api_activity", aosTestInput(nil))
	if ev["activity_name"] != "Agent Session" {
		t.Fatalf("session event default activity_name changed: %v", ev["activity_name"])
	}
	aos := requireObject(t, requireObject(t, ev["unmapped"], "unmapped")["aos"], "unmapped.aos")
	if _, hasDecision := aos["decision"]; hasDecision {
		t.Fatal("session-only AOS event must not fabricate a decision")
	}
	assertNoAOSPayloadKeys(t, aos, "unmapped.aos")
}

func TestOCSFAOSRejectsUnknownDecisionVerdict(t *testing.T) {
	in := aosTestInput(&AOSDecision{
		Decision:   AOSDecisionVerdict("permit"),
		Reasoning:  "policy evaluation completed",
		ReasonCode: "policy.invalid_test",
		Message:    "invalid test verdict",
	})
	if _, err := OCSF(in); err == nil {
		t.Fatal("an AOS decision outside allow|deny|modify must fail closed")
	}
}
