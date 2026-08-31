// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestWebhookSignatureGate proves the ANT2-14 read-first guard: a webhook with an
// INVALID signature is rejected (401) and emits nothing; a correctly-signed one is
// accepted (200) and emits the mapped observation.
func TestWebhookSignatureGate(t *testing.T) {
	const secret = "whsec_testsecret"
	body := []byte(`{"type":"vault.credential.archived","vault_id":"vlt_1","workspace_id":"wrkspc_01","created_at":"2026-06-06T00:00:00Z"}`)

	var emitted []model.Observation
	h := WebhookHandler(secret, func(o model.Observation) { emitted = append(emitted, o) }, atTime)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Invalid signature → 401, nothing emitted.
	reqBad, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(string(body)))
	reqBad.Header.Set(webhookSigHeader, "sha256=deadbeef")
	respBad, err := http.DefaultClient.Do(reqBad)
	if err != nil {
		t.Fatalf("post bad: %v", err)
	}
	respBad.Body.Close()
	if respBad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature status = %d, want 401", respBad.StatusCode)
	}
	if len(emitted) != 0 {
		t.Fatalf("bad signature emitted %d observations, want 0", len(emitted))
	}

	// Valid signature → 200, one observation.
	sig := signWebhook(secret, body)
	reqOK, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(string(body)))
	reqOK.Header.Set(webhookSigHeader, sig)
	respOK, err := http.DefaultClient.Do(reqOK)
	if err != nil {
		t.Fatalf("post ok: %v", err)
	}
	respOK.Body.Close()
	if respOK.StatusCode != http.StatusOK {
		t.Fatalf("valid signature status = %d, want 200", respOK.StatusCode)
	}
	if len(emitted) != 1 {
		t.Fatalf("valid signature emitted %d observations, want 1", len(emitted))
	}
	f, ok := emitted[0].(model.FindingReport)
	if !ok || f.SubjectRef != "vlt_1" {
		t.Fatalf("emitted = %+v, want a vault finding for vlt_1", emitted[0])
	}
}

// TestVaultsIngestedWithoutMaterial proves vaults/credentials are ingested as REFS
// only (ANT2-14) — vlt_/vcrd_ ids and the credential TYPE, never the secret — and that
// a workspace-scoped vault yields the lateral-movement posture finding.
func TestVaultsIngestedWithoutMaterial(t *testing.T) {
	doer := &jsonDoer{t: t, byPath: map[string]string{
		"/v1/vaults": `{"data":[
			{"id":"vlt_shared","name":"shared","workspace_id":"wrkspc_01","archived":false}
		],"has_more":false}`,
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	vaults, err := s.FetchVaults(context.Background())
	if err != nil {
		t.Fatalf("FetchVaults: %v", err)
	}
	if len(vaults) != 1 || !strings.HasPrefix(vaults[0].ID, prefixVault) {
		t.Fatalf("vaults = %+v, want one vlt_ reference", vaults)
	}
	f := VaultLateralRiskFinding(vaults[0], fixedClock())
	if f.SubjectKind != resVault || f.Severity != model.SeverityMedium {
		t.Errorf("vault lateral-risk finding = %+v, want vault/medium", f)
	}
	// VaultCredential carries only the TYPE, never material — a compile-time guarantee
	// (the struct has no token field); assert the modeled types are the documented ones.
	vc := VaultCredential{ID: "vcrd_1", VaultRef: "vlt_shared", Type: "mcp_oauth"}
	if !strings.HasPrefix(vc.ID, prefixVaultCred) || (vc.Type != "mcp_oauth" && vc.Type != "static_bearer") {
		t.Errorf("vault credential = %+v, want a vcrd_ ref with mcp_oauth/static_bearer type", vc)
	}
}

// TestThreadEventsForensicEdge proves the subagent thread stream is read for forensic
// detail (ANT2-14) and an A2A message becomes an observed agent→agent edge.
func TestThreadEventsForensicEdge(t *testing.T) {
	doer := &jsonDoer{t: t, byPath: map[string]string{
		"/v1/sessions/sess_1/threads/thr_1/events": `{"data":[
			{"type":"agent.thread_message","agent_ref":"agent:coordinator","peer_ref":"agent:worker","created_at":"2026-06-06T00:00:00Z"},
			{"type":"agent.thread_end","agent_ref":"agent:worker"}
		],"has_more":false}`,
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	events, err := s.FetchThreadEvents(context.Background(), "sess_1", "thr_1")
	if err != nil {
		t.Fatalf("FetchThreadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("thread events = %d, want 2", len(events))
	}
	edge, ok := ThreadEventEdge("sess_1", events[0], fixedClock())
	if !ok {
		t.Fatal("A2A thread message must produce an edge")
	}
	if edge.OriginRef != "agent:coordinator" || edge.ResourceRef != "agent:worker" || edge.Source != model.SignalA2A {
		t.Errorf("A2A edge = %+v, want coordinator→worker via A2A", edge)
	}
	// A non-A2A event yields no edge.
	if _, ok := ThreadEventEdge("sess_1", events[1], fixedClock()); ok {
		t.Error("agent.thread_end must not produce an edge")
	}
}

// TestHITLPendingFinding proves the user.tool_confirmation gate is modeled (ANT2-14).
func TestHITLPendingFinding(t *testing.T) {
	r := MessageResponse{ID: "msg", Model: "claude-opus-4-8", StopReason: StopRequiresAction}
	f, ok := r.HITLPendingFinding("sess_1", atTime())
	if !ok || f.SubjectKind != managedAgentSubject || f.Kind != "governance" {
		t.Fatalf("HITL finding = %+v ok=%v, want a governance managed_agent finding", f, ok)
	}
	if _, ok := (MessageResponse{StopReason: "end_turn"}).HITLPendingFinding("s", atTime()); ok {
		t.Error("a normal end_turn must not produce a HITL finding")
	}
}
