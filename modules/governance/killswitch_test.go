// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the estate kill switch state machine: one-click engage (graduated
// scope), queued-work revocation, dual-control re-enable with the structural
// two-human floor, forced post-review with separation of duties, and the
// incident evidence pack. The actuation-gate halves (PEP/dispatchers/eventing)
// have their own tests in their packages; these prove the SOURCE OF TRUTH.

// engage posts an engage request and returns the response.
func engage(h *harness, token string, tenant model.TenantID, scopeKind, scopeRef, reason string) resp {
	h.t.Helper()
	return h.do("POST", "/v1/m/governance/killswitch", token, map[string]any{
		"scope_kind": scopeKind, "scope_ref": scopeRef, "reason": reason,
	}, tenantHdr(tenant))
}

func TestKillSwitchEngageStateAndIdempotence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A reason is mandatory: who is reading this later — the post-review and
	// the regulator.
	if r := engage(h, admin, tenant, "estate", "", ""); r.code != http.StatusBadRequest {
		t.Fatalf("engage without reason = %d %s", r.code, r.raw)
	}
	// Scope vocabulary is closed.
	if r := engage(h, admin, tenant, "org", "", "x"); r.code != http.StatusBadRequest {
		t.Fatalf("engage with unknown scope = %d %s", r.code, r.raw)
	}
	r := engage(h, admin, tenant, "estate", "", "prompt-injection incident, SOC bridge 4411")
	if r.code != http.StatusCreated {
		t.Fatalf("engage = %d %s", r.code, r.raw)
	}
	stopID := r.body["id"].(string)
	if r.body["status"] != "active" || r.body["scope_kind"] != "estate" {
		t.Fatalf("engaged row = %v", r.body)
	}
	if r.body["engaged_aal"].(float64) < 3 {
		// The harness admin is step-up-verified; the recorded assurance must
		// reflect the REAL session (forensic attribution of the red button).
		t.Fatalf("engaged_aal = %v, want the session's AAL recorded", r.body["engaged_aal"])
	}
	if seq := r.body["engage_audit_seq"].(float64); seq < 1 {
		t.Fatalf("engage_audit_seq = %v, want a ledger anchor", seq)
	}

	// The live posture is visible (the console banner read).
	st := h.do("GET", "/v1/m/governance/killswitch/state", admin, nil, tenantHdr(tenant))
	if st.code != http.StatusOK || st.body["estate_stopped"] != true {
		t.Fatalf("state = %d %v", st.code, st.body)
	}

	// One active stop per scope: a second estate engage conflicts and names the
	// existing stop.
	if r := engage(h, admin, tenant, "estate", "", "again"); r.code != http.StatusConflict || !strings.Contains(r.raw, stopID) {
		t.Fatalf("second engage = %d %s", r.code, r.raw)
	}

	// The live consult the actuation gates use agrees.
	state, err := h.gov.KillSwitchState(context.Background(), tenant)
	if err != nil || !state.EstateStopped {
		t.Fatalf("KillSwitchState = %+v %v", state, err)
	}
	if _, stopped := state.Stopped("any-agent"); !stopped {
		t.Fatalf("estate stop must cover every agent")
	}

	// Engage is self-audited and announced on the finding rail (CRITICAL: notify
	// is exempt from the stop, so this is the alert that always flows).
	if !contains(h.auditActions(tenant), "governance.killswitch.engage") {
		t.Fatalf("missing engage self-audit: %v", h.auditActions(tenant))
	}
	foundCritical := false
	for _, f := range h.host.findings() {
		if f.Kind == "killswitch_engaged" && string(f.Severity) == "critical" {
			foundCritical = true
		}
	}
	if !foundCritical {
		t.Fatalf("missing critical killswitch_engaged finding: %+v", h.host.findings())
	}
}

func TestKillSwitchAgentScopeMatchesEveryIdentifier(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.createAgent(tenant, "billing-bot", "ext-billing-1")

	// Engage by EXTERNAL id; the stop must also match the resolved UUID.
	r := engage(h, admin, tenant, "agent", "ext-billing-1", "agent compromised")
	if r.code != http.StatusCreated {
		t.Fatalf("agent engage = %d %s", r.code, r.raw)
	}
	if r.body["agent_id"] != agent.ID.String() || r.body["agent_external_id"] != "ext-billing-1" {
		t.Fatalf("agent resolution = %v", r.body)
	}
	state, err := h.gov.KillSwitchState(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, stopped := state.Stopped(agent.ID.String()); !stopped {
		t.Fatalf("stop must match the agent UUID")
	}
	if _, stopped := state.Stopped("ext-billing-1"); !stopped {
		t.Fatalf("stop must match the external id")
	}
	if _, stopped := state.Stopped("other-agent"); stopped {
		t.Fatalf("an agent stop must not cover other agents")
	}
	if state.EstateStopped {
		t.Fatalf("an agent stop is not an estate stop")
	}

	// Engaging the SAME agent by UUID collides on the normalized scope key.
	if r := engage(h, admin, tenant, "agent", agent.ID.String(), "dup"); r.code != http.StatusConflict {
		t.Fatalf("same-agent engage by UUID = %d %s", r.code, r.raw)
	}
	// An UNKNOWN ref still engages (a rogue agent outside the inventory must be
	// stoppable by the ref the PEP sees) — honest string-only matching.
	if r := engage(h, admin, tenant, "agent", "ghost-agent", "rogue"); r.code != http.StatusCreated {
		t.Fatalf("unknown-agent engage = %d %s", r.code, r.raw)
	}
}

func TestKillSwitchEngageRevokesPendingActuationApprovals(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	open := func(action, subjectKind, subjectRef string) string {
		r := h.do("POST", "/v1/m/governance/approvals", admin, map[string]any{
			"action": action, "subject_kind": subjectKind, "subject_ref": subjectRef,
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("open approval %s = %d %s", action, r.code, r.raw)
		}
		return r.body["id"].(string)
	}
	fire := open("orchestration.schedule.fire", "schedule", "sched-1")
	voiceA := open("voice.session.open", "agent", "agent-a#plan=abc")
	voiceB := open("voice.session.open", "agent", "agent-b#plan=def")
	rotate := open("nhi.rotate", "identity", "vault:nhi:x") // governance work — never revoked

	// An AGENT-scoped engage revokes only that agent's queued actuation.
	if r := engage(h, admin, tenant, "agent", "agent-a", "contain agent-a"); r.code != http.StatusCreated {
		t.Fatalf("agent engage = %d %s", r.code, r.raw)
	} else if int(r.body["revoked_approvals"].(float64)) != 1 {
		t.Fatalf("agent engage revoked = %v, want 1", r.body["revoked_approvals"])
	}
	status := func(id string) string {
		r := h.do("GET", "/v1/m/governance/approvals/"+id, admin, nil, tenantHdr(tenant))
		return r.body["status"].(string)
	}
	if status(voiceA) != "canceled" || status(voiceB) != "pending" || status(fire) != "pending" {
		t.Fatalf("agent-scope revocation: voiceA=%s voiceB=%s fire=%s", status(voiceA), status(voiceB), status(fire))
	}

	// An ESTATE engage revokes every pending actuation approval — and NEVER the
	// governance work that runs the controls themselves.
	r := engage(h, admin, tenant, "estate", "", "estate stop")
	if r.code != http.StatusCreated {
		t.Fatalf("estate engage = %d %s", r.code, r.raw)
	}
	if int(r.body["revoked_approvals"].(float64)) != 2 {
		t.Fatalf("estate engage revoked = %v, want 2 (fire + voiceB)", r.body["revoked_approvals"])
	}
	if status(fire) != "canceled" || status(voiceB) != "canceled" || status(rotate) != "pending" {
		t.Fatalf("estate revocation: fire=%s voiceB=%s rotate=%s", status(fire), status(voiceB), status(rotate))
	}
}

// reenable posts a re-enable request.
func reenable(h *harness, token string, tenant model.TenantID, id string) resp {
	h.t.Helper()
	return h.do("POST", "/v1/m/governance/killswitch/"+id+"/reenable", token, map[string]any{}, tenantHdr(tenant))
}

// decide records one human decision on an approval.
func decide(h *harness, token string, tenant model.TenantID, approvalID, decision string) resp {
	h.t.Helper()
	return h.do("POST", "/v1/m/governance/approvals/"+approvalID+"/decisions", token,
		map[string]any{"decision": decision}, tenantHdr(tenant))
}

func TestKillSwitchReenableDualControl(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, approver1 := h.roleUser(admin, tenant, "ana@x.io", "admin")
	_, approver2 := h.roleUser(admin, tenant, "bea@x.io", "admin")

	r := engage(h, admin, tenant, "estate", "", "incident")
	if r.code != http.StatusCreated {
		t.Fatalf("engage = %d %s", r.code, r.raw)
	}
	stopID := r.body["id"].(string)

	// First call opens the CRITICAL dual-control request (the reserved
	// security.killswitch.* family: floor 2, AAL3 per decision — the engine's
	// controls, inherited with zero new code).
	r = reenable(h, admin, tenant, stopID)
	if r.code != http.StatusAccepted {
		t.Fatalf("reenable phase 1 = %d %s", r.code, r.raw)
	}
	appr := r.body["approval"].(map[string]any)
	approvalID := appr["id"].(string)
	if appr["action"] != "security.killswitch.reenable" || appr["risk_tier"] != "critical" || appr["required_approvals"].(float64) != 2 {
		t.Fatalf("re-enable approval = %v", appr)
	}

	// The requester cannot decide their own re-enable (engine SoD).
	if d := decide(h, admin, tenant, approvalID, "approve"); d.code != http.StatusForbidden {
		t.Fatalf("self-approval = %d %s", d.code, d.raw)
	}
	// One human is not enough — the stop stays active and the call reports 202.
	if d := decide(h, approver1, tenant, approvalID, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver1 = %d %s", d.code, d.raw)
	}
	if r := reenable(h, admin, tenant, stopID); r.code != http.StatusAccepted {
		t.Fatalf("reenable with 1/2 approvals = %d %s", r.code, r.raw)
	}
	st := h.do("GET", "/v1/m/governance/killswitch/state", admin, nil, tenantHdr(tenant))
	if st.body["estate_stopped"] != true {
		t.Fatalf("estate must stay stopped with 1/2 approvals")
	}
	// The second distinct human crosses the threshold; the flip verifies the
	// floor structurally in the same transaction and lifts the stop.
	if d := decide(h, approver2, tenant, approvalID, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver2 = %d %s", d.code, d.raw)
	}
	r = reenable(h, admin, tenant, stopID)
	if r.code != http.StatusOK || r.body["status"] != "reenabled" {
		t.Fatalf("reenable flip = %d %s", r.code, r.raw)
	}
	st = h.do("GET", "/v1/m/governance/killswitch/state", admin, nil, tenantHdr(tenant))
	if st.body["estate_stopped"] != false {
		t.Fatalf("estate must resume after the dual-control re-enable")
	}
	// A re-enabled stop is terminal for re-enable.
	if r := reenable(h, admin, tenant, stopID); r.code != http.StatusConflict {
		t.Fatalf("re-reenable = %d %s", r.code, r.raw)
	}
	if !contains(h.auditActions(tenant), "governance.killswitch.reenable") {
		t.Fatalf("missing reenable self-audit")
	}
}

func TestKillSwitchReenableFloorSurvivesDowngradedPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, approver1 := h.roleUser(admin, tenant, "ana@x.io", "admin")
	_, approver2 := h.roleUser(admin, tenant, "bea@x.io", "admin")

	// An operator policy explicitly DOWNGRADES the re-enable tier to high with a
	// single approver — resolveRiskTier honors it (configurable by policy), so
	// the ENGINE will let the approval cross with one human. The handler's
	// structural floor is what keeps "never unilateral" true anyway.
	if r := h.do("POST", "/v1/m/governance/policies", admin, map[string]any{
		"name": "downgrade-reenable", "kind": "approval", "enabled": true,
		"spec": map[string]any{
			"required_approvals": 1, "risk_tier": "high",
			"match": map[string]any{"action": "security.killswitch.reenable"},
		},
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("create downgrade policy = %d %s", r.code, r.raw)
	}

	r := engage(h, admin, tenant, "estate", "", "incident")
	stopID := r.body["id"].(string)
	r = reenable(h, admin, tenant, stopID)
	if r.code != http.StatusAccepted {
		t.Fatalf("reenable phase 1 = %d %s", r.code, r.raw)
	}
	appr := r.body["approval"].(map[string]any)
	approvalID := appr["id"].(string)
	// The downgrade DOES retune the tier (and with it the AAL3 decision bar —
	// that is what "configurable by policy" means)... but the re-enable opens at
	// two distinct humans regardless: the quorum is not policy-tunable.
	if appr["risk_tier"] != "high" {
		t.Fatalf("risk_tier = %v, want the operator's downgrade honored", appr["risk_tier"])
	}
	if appr["required_approvals"].(float64) != 2 {
		t.Fatalf("required_approvals = %v, want the non-negotiable 2", appr["required_approvals"])
	}
	if d := decide(h, approver1, tenant, approvalID, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver1 = %d %s", d.code, d.raw)
	}
	if r := reenable(h, admin, tenant, stopID); r.code != http.StatusAccepted {
		t.Fatalf("flip with 1/2 under downgraded policy = %d %s", r.code, r.raw)
	}
	if d := decide(h, approver2, tenant, approvalID, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver2 = %d %s", d.code, d.raw)
	}
	if r := reenable(h, admin, tenant, stopID); r.code != http.StatusOK {
		t.Fatalf("flip after real quorum = %d %s", r.code, r.raw)
	}
}

func TestKillSwitchStructuralFloorRefusesCorruptApproval(t *testing.T) {
	// Defense in depth: even if an approval row somehow reads APPROVED with a
	// single distinct approver (legacy data, a raced policy change, manual DB
	// surgery), the flip refuses, marks the request spent, and the next attempt
	// opens a fresh dual-control request — "never unilateral" holds structurally.
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := engage(h, admin, tenant, "estate", "", "incident")
	stopID := r.body["id"].(string)
	r = reenable(h, admin, tenant, stopID)
	firstApproval := r.body["approval"].(map[string]any)["id"].(string)

	// Corrupt the bound approval directly in the store: approved, one decision.
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.approval")
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(firstApproval))
		if err != nil {
			return err
		}
		rec["status"] = "approved"
		rec["required_approvals"] = int64(1)
		rec["approve_count"] = int64(1)
		if _, err := repo.Update(ctx, rec); err != nil {
			return err
		}
		decRepo, err := sc.Ext("governance.approval_decision")
		if err != nil {
			return err
		}
		_, err = decRepo.Create(ctx, model.Record{
			"approval_id": firstApproval, "decision": "approve",
			"decider": "user:ghost", "decider_user": "ghost", "decided_at": h.clk.Now().String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	r = reenable(h, admin, tenant, stopID)
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "dual_control_required") {
		t.Fatalf("corrupt-approval flip = %d %s, want dual_control_required", r.code, r.raw)
	}
	st := h.do("GET", "/v1/m/governance/killswitch/state", admin, nil, tenantHdr(tenant))
	if st.body["estate_stopped"] != true {
		t.Fatalf("the estate must stay stopped after a floor refusal")
	}
	// The spent request is unbound: the next attempt opens a FRESH one.
	r = reenable(h, admin, tenant, stopID)
	if r.code != http.StatusAccepted {
		t.Fatalf("fresh request after spent = %d %s", r.code, r.raw)
	}
	if second := r.body["approval"].(map[string]any)["id"].(string); second == firstApproval {
		t.Fatalf("a spent request must not be reused")
	}
}

func TestKillSwitchReenableRejectedOpensFreshQuorum(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, approver1 := h.roleUser(admin, tenant, "ana@x.io", "admin")
	_, approver2 := h.roleUser(admin, tenant, "bea@x.io", "admin")

	r := engage(h, admin, tenant, "estate", "", "incident")
	stopID := r.body["id"].(string)
	r = reenable(h, admin, tenant, stopID)
	first := r.body["approval"].(map[string]any)["id"].(string)
	// One human REJECTS — the request is spent; nothing flips.
	if d := decide(h, approver1, tenant, first, "reject"); d.code != http.StatusOK {
		t.Fatalf("reject = %d %s", d.code, d.raw)
	}
	// The next attempt opens a FRESH request (a new quorum of two humans — a
	// rejection is never laundered, it stays in the rows and the ledger).
	r = reenable(h, admin, tenant, stopID)
	if r.code != http.StatusAccepted {
		t.Fatalf("post-rejection reenable = %d %s", r.code, r.raw)
	}
	second := r.body["approval"].(map[string]any)["id"].(string)
	if second == first {
		t.Fatalf("rejected request must not be reused")
	}
	if d := decide(h, approver1, tenant, second, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver1 = %d %s", d.code, d.raw)
	}
	if d := decide(h, approver2, tenant, second, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver2 = %d %s", d.code, d.raw)
	}
	if r := reenable(h, admin, tenant, stopID); r.code != http.StatusOK {
		t.Fatalf("flip = %d %s", r.code, r.raw)
	}
}

// liftStop runs the full dual-control re-enable of stopID with two approvers.
func liftStop(h *harness, admin string, tenant model.TenantID, stopID string, approver1, approver2 string) {
	h.t.Helper()
	r := reenable(h, admin, tenant, stopID)
	if r.code != http.StatusAccepted {
		h.t.Fatalf("liftStop phase 1 = %d %s", r.code, r.raw)
	}
	approvalID := r.body["approval"].(map[string]any)["id"].(string)
	if d := decide(h, approver1, tenant, approvalID, "approve"); d.code != http.StatusOK {
		h.t.Fatalf("liftStop approver1 = %d %s", d.code, d.raw)
	}
	if d := decide(h, approver2, tenant, approvalID, "approve"); d.code != http.StatusOK {
		h.t.Fatalf("liftStop approver2 = %d %s", d.code, d.raw)
	}
	if r := reenable(h, admin, tenant, stopID); r.code != http.StatusOK {
		h.t.Fatalf("liftStop flip = %d %s", r.code, r.raw)
	}
}

func TestKillSwitchPostReviewSoDAndBackpressure(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, approver1 := h.roleUser(admin, tenant, "ana@x.io", "admin")
	_, approver2 := h.roleUser(admin, tenant, "bea@x.io", "admin")
	_, reviewer := h.roleUser(admin, tenant, "carla@x.io", "admin")

	r := engage(h, admin, tenant, "estate", "", "incident 1")
	stop1 := r.body["id"].(string)
	liftStop(h, admin, tenant, stop1, approver1, approver2)

	review := func(token, id, note string) resp {
		return h.do("POST", "/v1/m/governance/killswitch/"+id+"/review", token, map[string]any{"note": note}, tenantHdr(tenant))
	}
	// The note is mandatory; the engager/requester/re-enabler cannot sign off
	// their own incident (separation of duties).
	if rr := review(reviewer, stop1, ""); rr.code != http.StatusBadRequest {
		t.Fatalf("review without note = %d %s", rr.code, rr.raw)
	}
	if rr := review(admin, stop1, "looks fine"); rr.code != http.StatusForbidden {
		t.Fatalf("involved review = %d %s", rr.code, rr.raw)
	}

	// Backpressure: a NEW engage of the same scope is never blocked (an
	// emergency must not queue behind paperwork)...
	r = engage(h, admin, tenant, "estate", "", "incident 2")
	if r.code != http.StatusCreated {
		t.Fatalf("engage 2 with unreviewed prior = %d %s", r.code, r.raw)
	}
	stop2 := r.body["id"].(string)
	// ...but RE-ENABLING it is blocked until incident 1's post-review lands.
	r = reenable(h, admin, tenant, stop2)
	approvalID := r.body["approval"].(map[string]any)["id"].(string)
	if d := decide(h, approver1, tenant, approvalID, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver1 = %d", d.code)
	}
	if d := decide(h, approver2, tenant, approvalID, "approve"); d.code != http.StatusOK {
		t.Fatalf("approver2 = %d", d.code)
	}
	r = reenable(h, admin, tenant, stop2)
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "post-review") {
		t.Fatalf("flip with unreviewed prior = %d %s, want post-review backpressure", r.code, r.raw)
	}
	if rr := review(reviewer, stop1, "stop justified; agent rotated; policies tightened"); rr.code != http.StatusOK {
		t.Fatalf("review = %d %s", rr.code, rr.raw)
	}
	if r := reenable(h, admin, tenant, stop2); r.code != http.StatusOK {
		t.Fatalf("flip after review = %d %s", r.code, r.raw)
	}
	// One-shot: a reviewed incident cannot be re-reviewed.
	if rr := review(reviewer, stop1, "again"); rr.code != http.StatusConflict {
		t.Fatalf("double review = %d %s", rr.code, rr.raw)
	}
	if !contains(h.auditActions(tenant), "governance.killswitch.review") {
		t.Fatalf("missing review self-audit")
	}
}

func TestKillSwitchEvidencePack(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, approver1 := h.roleUser(admin, tenant, "ana@x.io", "admin")
	_, approver2 := h.roleUser(admin, tenant, "bea@x.io", "admin")
	_, reviewer := h.roleUser(admin, tenant, "carla@x.io", "admin")

	// Queued actuation work that the engage revokes (evidence of revocation).
	if r := h.do("POST", "/v1/m/governance/approvals", admin, map[string]any{
		"action": "orchestration.schedule.fire", "subject_kind": "schedule", "subject_ref": "sched-1",
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("open fire approval = %d %s", r.code, r.raw)
	}

	r := engage(h, admin, tenant, "estate", "", "regulator drill")
	stopID := r.body["id"].(string)
	liftStop(h, admin, tenant, stopID, approver1, approver2)
	if rr := h.do("POST", "/v1/m/governance/killswitch/"+stopID+"/review", reviewer,
		map[string]any{"note": "drill complete"}, tenantHdr(tenant)); rr.code != http.StatusOK {
		t.Fatalf("review = %d %s", rr.code, rr.raw)
	}

	r = h.do("GET", "/v1/m/governance/killswitch/"+stopID+"/evidence", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("evidence = %d %s", r.code, r.raw)
	}
	ks := r.body["killswitch"].(map[string]any)
	if ks["id"] != stopID || ks["reviewed"] != true {
		t.Fatalf("pack killswitch = %v", ks)
	}
	integ := r.body["integrity"].(map[string]any)
	if integ["chain_verified"] != true || integ["anchor_seq"].(float64) < 1 {
		t.Fatalf("pack integrity = %v (the chain proof is the point)", integ)
	}
	if integ["canonical_meta"] != true {
		t.Fatalf("pack must export canonical ledger meta, got %v", integ)
	}
	appr := r.body["reenable_approval"].(map[string]any)
	if appr["action"] != "security.killswitch.reenable" {
		t.Fatalf("pack approval = %v", appr)
	}
	if decs := r.body["reenable_decisions"].([]any); len(decs) != 2 {
		t.Fatalf("pack decisions = %d, want the two-human proof", len(decs))
	}
	if revoked := r.body["revoked_approval_ids"].([]any); len(revoked) != 1 {
		t.Fatalf("pack revoked approvals = %v, want the canceled fire", r.body["revoked_approval_ids"])
	}
	timeline := r.body["timeline"].([]any)
	wantActions := map[string]bool{
		"governance.killswitch.engage": false, "governance.killswitch.reenable": false,
		"governance.killswitch.review": false, "governance.approval.create": false,
	}
	for _, e := range timeline {
		if a, ok := e.(map[string]any)["action"].(string); ok {
			if _, tracked := wantActions[a]; tracked {
				wantActions[a] = true
			}
		}
	}
	for a, seen := range wantActions {
		if !seen {
			t.Fatalf("timeline missing %s: %v", a, timeline)
		}
	}
	rb := r.body["rollback"].(map[string]any)
	if nr := rb["non_reversible_domains"].([]any); len(nr) == 0 {
		t.Fatalf("the pack must document where rollback does NOT apply")
	}
	if r.body["pack_sha256"].(string) == "" {
		t.Fatalf("pack must carry its own content hash")
	}
	// Exporting evidence is itself privileged and lands on the chain.
	if !contains(h.auditActions(tenant), "governance.killswitch.evidence_export") {
		t.Fatalf("missing evidence-export self-audit")
	}

	// The pack is admin-tier: a viewer cannot pull incident history.
	_, viewer := h.roleUser(admin, tenant, "viewer@x.io", "viewer")
	if r := h.do("GET", "/v1/m/governance/killswitch/"+stopID+"/evidence", viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer evidence = %d, want 403", r.code)
	}
}

// TestKillSwitchFindingTitlesCarrySubjectAndReason fija que dos paradas DISTINTAS producen títulos
// DISTINTOS.
//
// ⛔ EL DEFECTO QUE CIERRA, y no se ve desde el verde. Los títulos eran CONSTANTES: seis paradas
// distintas daban seis filas IDÉNTICAS en Security > Findings, indistinguibles sin abrir cada una.
// Lo midió sobre datos reales — no era el seed. El operador que mira esa lista durante un
// incidente es justo quien no puede permitirse abrir seis filas para saber a quién se paró.
//
// Se comprueba por el CAMINO REAL —engage HTTP y el finding que emite— y no llamando a los
// ayudantes: un título que sólo es correcto en la función que lo arma no ha probado que llegue.
func TestKillSwitchFindingTitlesCarrySubjectAndReason(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	if r := engage(h, admin, tenant, "agent", "billing-bot", "leaked key in prompt"); r.code != http.StatusCreated {
		t.Fatalf("engage agent = %d %s", r.code, r.raw)
	}
	if r := engage(h, admin, tenant, "agent", "deploy-bot", "runaway spend"); r.code != http.StatusCreated {
		t.Fatalf("engage second agent = %d %s", r.code, r.raw)
	}
	if r := engage(h, admin, tenant, "estate", "", "SOC bridge 4411"); r.code != http.StatusCreated {
		t.Fatalf("engage estate = %d %s", r.code, r.raw)
	}

	var titles []string
	for _, f := range h.host.findings() {
		if f.Kind == "killswitch_engaged" {
			titles = append(titles, f.Title)
		}
	}
	if len(titles) != 3 {
		t.Fatalf("engaged findings = %d, want 3: %v", len(titles), titles)
	}

	// LO QUE DE VERDAD SE AFIRMA: las tres son distintas entre sí. Un título constante suspende
	// aquí aunque contenga las palabras correctas.
	seen := map[string]bool{}
	for _, ti := range titles {
		if seen[ti] {
			t.Fatalf("dos paradas distintas produjeron el MISMO título — es el defecto que esto cierra:\n  %s", ti)
		}
		seen[ti] = true
	}

	// Y cada una nombra a SU sujeto: sin esto, tres títulos distintos por accidente (un id, un
	// reloj) pasarían igual.
	for _, want := range []string{"billing-bot", "deploy-bot", "the whole estate"} {
		found := false
		for _, ti := range titles {
			if strings.Contains(ti, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ningún título nombra %q; títulos: %v", want, titles)
		}
	}
	// Y la razón viaja: es la mitad que responde «por qué» sin abrir la fila.
	for _, want := range []string{"leaked key in prompt", "runaway spend", "SOC bridge 4411"} {
		found := false
		for _, ti := range titles {
			if strings.Contains(ti, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ningún título lleva la razón %q; títulos: %v", want, titles)
		}
	}
}

// TestKillSwitchFindingTitleFoldsAHostileReason fija las DOS acotaciones del título, que no son
// cosméticas: la razón es texto libre acotado sólo por `maxNoteLen`.
//
// Un salto de línea parte la fila que el título existe para informar, y un párrafo largo la
// desborda. El recorte va por RUNAS: cortar por bytes publicaría UTF-8 inválido dentro del finding.
func TestKillSwitchFindingTitleFoldsAHostileReason(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	reason := "línea uno\nlínea dos\tcon tabulador " + strings.Repeat("ñ", 200)
	if r := engage(h, admin, tenant, "agent", "verbose-bot", reason); r.code != http.StatusCreated {
		t.Fatalf("engage = %d %s", r.code, r.raw)
	}
	var title string
	for _, f := range h.host.findings() {
		if f.Kind == "killswitch_engaged" {
			title = f.Title
		}
	}
	if title == "" {
		t.Fatal("sin finding de parada")
	}
	if strings.ContainsAny(title, "\n\t") {
		t.Fatalf("el título conserva un salto o tabulador y rompe la fila:\n%q", title)
	}
	if !utf8.ValidString(title) {
		t.Fatalf("el título no es UTF-8 válido: el recorte cortó a mitad de una runa:\n%q", title)
	}
	if n := utf8.RuneCountInString(title); n > 300 {
		t.Fatalf("el título mide %d runas: desborda la fila", n)
	}
	if !strings.Contains(title, "verbose-bot") {
		t.Fatalf("el recorte se llevó el sujeto: %q", title)
	}
}
