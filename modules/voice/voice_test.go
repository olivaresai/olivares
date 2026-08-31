// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func (h *harness) open(token string, tenant model.TenantID, session, agent, mdl, prov, approvalRef string) resp {
	body := map[string]any{"session_ref": session, "agent_ref": agent, "model_ref": mdl, "provider_ref": prov}
	if approvalRef != "" {
		body["approval_ref"] = approvalRef
	}
	return h.do("POST", "/v1/m/voice/sessions/open", token, body, tenantHdr(tenant))
}

// TestDefaultDenyAndPolicyGate proves opening is default-DENY (no allowing policy →
// refused) and that a permitted tuple reaches the HITL gate.
func TestDefaultDenyAndPolicyGate(t *testing.T) {
	h, _ := newHarness(t) // deny gate
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	// No policy → default deny.
	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "")
	if r.code != http.StatusForbidden || r.body["policy_verdict"] != verdictDenied {
		t.Fatalf("default-deny expected, got %d %s", r.code, r.raw)
	}
	// Add a permitting policy → reaches the gate (no_gate by default).
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)
	r = h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "")
	if r.code != http.StatusAccepted || r.body["policy_verdict"] != verdictAllowed || r.body["gate_status"] != string(StatusNoGate) {
		t.Fatalf("permitted tuple should reach the gate, got %d %s", r.code, r.raw)
	}
}

// TestOpenDenyClosedNoGate proves an approved-by-policy open is still denied by
// default when no HITL gate is wired, and surfaces the governance gap.
func TestOpenDenyClosedNoGate(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	if r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", ""); r.code != http.StatusAccepted {
		t.Fatalf("phase1 = %d %s", r.code, r.raw)
	}
	if !h.waitForFinding(busUngovernedOpen) {
		t.Fatal("an un-governable open must raise an ungoverned-open finding")
	}
	if r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1"); r.code != http.StatusForbidden || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("phase2 must be blocked (deny-by-default), got %d %s", r.code, r.raw)
	}
}

// TestOpenApprovedDeclaredNotOpened proves an approved open with NO dispatcher is
// honestly "declared, not opened", marks the session governed, and is attributed to
// the real principal.
func TestOpenApprovedDeclaredNotOpened(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDeclaredNotOpened {
		t.Fatalf("approved+no-dispatcher must be declared_not_opened, got %d %s", r.code, r.raw)
	}
	s, code := h.getSession(tok, tenant, "s1")
	if code != http.StatusOK || !s.Governed || s.PrincipalRef == "" || !strings.HasPrefix(s.PrincipalRef, "user:") {
		t.Fatalf("governed open must mark the session governed + real principal, got %+v", s)
	}
}

// TestOpenDispatched proves an approved open with a wired dispatcher is dispatched.
func TestOpenDispatched(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}), WithDispatcher(fakeDispatcher{ref: "rt-7"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "rt-7" {
		t.Fatalf("approved+dispatcher must dispatch, got %d %s", r.code, r.raw)
	}
}

// TestOpenPlanHashMismatch proves an approval bound to a different plan cannot
// authorize this open (anti-TOCTOU — e.g. a silent model upgrade).
func TestOpenPlanHashMismatch(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved, planHash: "stale"}), WithDispatcher(fakeDispatcher{ref: "rt-x"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusForbidden || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("stale plan_hash must block, got %d %s", r.code, r.raw)
	}
}

// TestOpenEmptyPlanHashBlocked proves a gate that APPROVES but echoes NO plan hash
// cannot authorize a voice open (strict anti-TOCTOU binding).
func TestOpenEmptyPlanHashBlocked(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved, emptyHash: true}), WithDispatcher(fakeDispatcher{ref: "rt-x"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)
	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusForbidden || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("an approval echoing an empty plan hash must block, got %d %s", r.code, r.raw)
	}
}

// TestTelemetryMetadataOnly proves the telemetry fold stores METADATA only: counts,
// latency, language and a HASH of an external transcript locator — and that a
// telemetry event carrying a forbidden content key is DROPPED whole, never stored.
func TestTelemetryMetadataOnly(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	// A valid telemetry event (allow-listed keys only).
	h.publishTelemetry(tenant, map[string]any{
		"session_ref": "s1", "agent_ref": "voice-agent", "model_ref": "gpt-realtime", "provider_ref": "openai",
		"language_code": "es-ES", "role": "user", "turn_delta": 2, "latency_ms": 120, "duration_ms": 5000,
		"transcript_locator_ref": "r2://bucket/transcripts/s1.json",
	})
	s := h.waitForSession(tok, tenant, "s1")
	if s.UserTurns != 2 || s.LanguageCode != "es-ES" || s.LatencyMaxMS != 120 || s.DurationMS != 5000 {
		t.Fatalf("telemetry metadata not folded: %+v", s)
	}
	if len(s.TranscriptRefHash) != 64 || s.TranscriptRefHash == "r2://bucket/transcripts/s1.json" {
		t.Fatalf("transcript locator must be hashed (64 hex), got %q", s.TranscriptRefHash)
	}
	raw, _ := json.Marshal(s)
	for _, banned := range []string{"transcript_text", "audio", "\"text\"", "speaker", "prompt"} {
		if strings.Contains(strings.ToLower(string(raw)), banned) {
			t.Fatalf("session JSON leaked a %q field: %s", banned, raw)
		}
	}

	// A telemetry event with a forbidden content key is rejected WHOLE.
	h.publishTelemetry(tenant, map[string]any{"session_ref": "s2", "transcript_text": "hello world"})
	// Give the bus a moment; s2 must never materialize.
	for i := 0; i < 40; i++ {
		if _, code := h.getSession(tok, tenant, "s2"); code == http.StatusOK {
			t.Fatal("a telemetry event with a forbidden key must be dropped, not stored")
		}
	}
}

// TestPolicyViolationFinding proves telemetry for an (agent,model,provider) that no
// policy permits raises a voice_policy_violation.
func TestPolicyViolationFinding(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// No policy at all → any telemetry is a violation.
	h.publishTelemetry(tenant, map[string]any{
		"session_ref": "s1", "agent_ref": "rogue", "model_ref": "x", "provider_ref": "y", "role": "user", "turn_delta": 1,
	})
	if !h.waitForFinding(busPolicyViolation) {
		t.Fatal("un-permitted voice usage must raise a policy-violation finding")
	}
}

// TestLatencyDegradedFinding proves telemetry crossing the policy SLA bound raises a
// voice_latency_degraded.
func TestLatencyDegradedFinding(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 100) // SLA 100ms

	h.publishTelemetry(tenant, map[string]any{
		"session_ref": "s1", "agent_ref": "voice-agent", "model_ref": "gpt-realtime", "provider_ref": "openai",
		"role": "agent", "turn_delta": 1, "latency_ms": 500,
	})
	if !h.waitForFinding(busLatencyDegraded) {
		t.Fatal("latency over the policy SLA must raise a latency-degraded finding")
	}
}

// TestDecisionLedgerAppendOnly proves the open/close evidence ledger is immutable.
func TestDecisionLedgerAppendOnly(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)
	h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "") // produces an open_request row

	ctx := context.Background()
	err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(decisionKind)
		if e != nil {
			return e
		}
		recs, _, e := repo.List(ctx, model.Query{Limit: 1})
		if e != nil {
			return e
		}
		if len(recs) == 0 {
			t.Fatal("expected a decision row")
		}
		_, e = repo.Update(ctx, recs[0])
		return e
	})
	if err != store.ErrAppendOnly {
		t.Fatalf("decision ledger must be append-only, update got %v", err)
	}
}

// TestTenantIsolationSessions proves one tenant never sees another's voice sessions.
func TestTenantIsolationSessions(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	a := h.createOrg(admin, "tenant-a")
	b := h.createOrg(admin, "tenant-b")
	tokA := h.roleToken(admin, a, "a@x.io", "admin")
	tokB := h.roleToken(admin, b, "b@x.io", "admin")

	h.publishTelemetry(a, map[string]any{"session_ref": "s1", "agent_ref": "voice-agent", "model_ref": "m", "provider_ref": "p", "role": "user", "turn_delta": 1})
	h.waitForSession(tokA, a, "s1")

	if _, code := h.getSession(tokB, b, "s1"); code != http.StatusNotFound {
		t.Fatalf("tenant B must not see tenant A's session, got %d", code)
	}
}

// TestPermissionTiers proves the verb-tier gating: a viewer reads but cannot set a
// policy (policy:admin) or open a session (session:admin).
func TestPermissionTiers(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.io", "viewer")

	if r := h.do("GET", "/v1/m/voice/sessions", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer should read sessions, got %d %s", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/m/voice/policies", viewer, map[string]any{"agent_ref": "a", "allowed_model_ref": "m", "allowed_provider_ref": "p"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer must not set a policy, got %d %s", r.code, r.raw)
	}
	if r := h.open(viewer, tenant, "s1", "a", "m", "p", ""); r.code != http.StatusForbidden {
		t.Fatalf("viewer must not open a session, got %d %s", r.code, r.raw)
	}
}
