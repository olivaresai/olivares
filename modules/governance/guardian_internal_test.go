// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// the guardian loop, tested in-package because its trigger half is a
// bus handler (onGuardianFinding) the harness host does not route. The HTTP
// rule-authoring surface is covered by the killswitch harness tests; these
// prove the loop semantics: matching, dedup, self-loop immunity, the auto
// containments, and the approval-mode sweep.

type guardianFixture struct {
	t      *testing.T
	m      *Module
	st     store.Store
	host   *capturingHost
	tenant model.TenantID
}

func newGuardianFixture(t *testing.T) *guardianFixture {
	t.Helper()
	ctx := context.Background()
	clk := &intClock{t: intBase}
	m := New(WithClock(clk))
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = model.TenantID(org.ID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	host := &capturingHost{}
	if err := m.Init(ctx, host); err != nil {
		t.Fatal(err)
	}
	return &guardianFixture{t: t, m: m, st: st, host: host, tenant: tenant}
}

func (f *guardianFixture) createRule(name, kinds, minSev, action, mode string) model.ID {
	f.t.Helper()
	ctx := context.Background()
	var id model.ID
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colGRName: name, colGREnabled: true, colGRMatchKinds: kinds, colGRMinSeverity: minSev,
			colGRAction: action, colGRMode: mode, colGRCreatedBy: "user:test",
		})
		if err != nil {
			return err
		}
		id = model.ID(rec.String(model.ColID))
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *guardianFixture) fire(kind string, sev sdkmodel.Severity, subjectKind, subjectRef, detailHash string) {
	f.t.Helper()
	f.m.onGuardianFinding(context.Background(), event.FromObservation(f.tenant.String(), "olivares.security", sdkmodel.FindingReport{
		Kind: kind, Severity: sev, SubjectKind: subjectKind, SubjectRef: subjectRef,
		Title: "test finding", DetailHash: detailHash, OccurredAt: intBase,
	}))
}

func (f *guardianFixture) actions() []model.Record {
	f.t.Helper()
	ctx := context.Background()
	var out []model.Record
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo)
		out = recs
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *guardianFixture) stops() []model.Record {
	f.t.Helper()
	ctx := context.Background()
	var out []model.Record
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo)
		out = recs
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *guardianFixture) findingKinds() map[string]int {
	f.t.Helper()
	out := map[string]int{}
	f.host.mu.Lock()
	defer f.host.mu.Unlock()
	for _, e := range f.host.events {
		if fr, ok := event.FindingOf(e); ok {
			out[fr.Kind]++
		}
	}
	return out
}

func TestGuardianAutoStopAgentDedupAndFloor(t *testing.T) {
	f := newGuardianFixture(t)
	ruleID := f.createRule("contain-prompt-injection", "guardrail_violation", "high", gaActionStopAgent, gaModeAuto)

	// Below the severity floor → nothing fires.
	f.fire("guardrail_violation", sdkmodel.SeverityMedium, "agent", "ext-agent-1", "d-low")
	if got := f.actions(); len(got) != 0 {
		t.Fatalf("below-floor finding fired %d actions", len(got))
	}
	// A kind outside the allowlist → nothing fires.
	f.fire("other_kind", sdkmodel.SeverityCritical, "agent", "ext-agent-1", "d-other")
	if got := f.actions(); len(got) != 0 {
		t.Fatalf("non-matching kind fired %d actions", len(got))
	}
	// A subject shape the action cannot contain (no agent subject) → non-event.
	f.fire("guardrail_violation", sdkmodel.SeverityCritical, "connector", "conn-1", "d-conn")
	if got := f.actions(); len(got) != 0 {
		t.Fatalf("subject-shape mismatch fired %d actions", len(got))
	}

	// The matching finding contains the agent.
	f.fire("guardrail_violation", sdkmodel.SeverityCritical, "agent", "ext-agent-1", "d-1")
	acts := f.actions()
	if len(acts) != 1 || acts[0].String(colGAStatus) != gaStatusExecuted || acts[0].String(colGARule) != ruleID.String() {
		t.Fatalf("auto action = %+v", acts)
	}
	stops := f.stops()
	if len(stops) != 1 || stops[0].String(colKSScopeKind) != ksScopeAgent ||
		stops[0].String(colKSSource) != ksSourceGuardian || stops[0].String(colKSRuleRef) != ruleID.String() {
		t.Fatalf("guardian stop = %+v", stops)
	}
	if stops[0].Int(colKSEngageSeq) < 1 {
		t.Fatalf("guardian engage must anchor the ledger too")
	}
	kinds := f.findingKinds()
	if kinds[findingKillSwitchEngaged] != 1 || kinds[findingGuardianExecuted] != 1 {
		t.Fatalf("emitted findings = %v", kinds)
	}

	// The SAME finding identity re-delivered → dedup'd, no second action.
	f.fire("guardrail_violation", sdkmodel.SeverityCritical, "agent", "ext-agent-1", "d-1")
	if got := f.actions(); len(got) != 1 {
		t.Fatalf("re-delivered finding fired again: %d actions", len(got))
	}
	// A FRESH finding for the already-stopped agent → idempotent containment
	// (an action row records "already stopped", no second stop row).
	f.fire("guardrail_violation", sdkmodel.SeverityCritical, "agent", "ext-agent-1", "d-2")
	if got := f.stops(); len(got) != 1 {
		t.Fatalf("idempotent containment created a second stop: %d", len(got))
	}

	// The live state the gates consult sees the contained agent.
	st, err := f.m.KillSwitchState(context.Background(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, stopped := st.Stopped("ext-agent-1"); !stopped {
		t.Fatalf("guardian stop must bite at the gates")
	}
}

func TestGuardianSelfLoopImmunity(t *testing.T) {
	f := newGuardianFixture(t)
	// A wildcard CRITICAL rule — the aggressive operator posture. The loop's own
	// emissions must never re-trigger it (the escalation spiral).
	f.createRule("estate-on-critical", "", "critical", gaActionStopEstate, gaModeAuto)

	f.fire(findingKillSwitchEngaged, sdkmodel.SeverityCritical, "killswitch", "ks-1", "d-ks")
	f.fire(findingGuardianExecuted, sdkmodel.SeverityCritical, "agent", "a-1", "d-ga")
	if got := f.actions(); len(got) != 0 {
		t.Fatalf("self-kinds fired %d actions (escalation spiral)", len(got))
	}

	// The control plane's OWN operational/lifecycle findings must NEVER trigger
	// containment through the any-match default — else the guardian's own
	// approval-mode escalation (governance_approval_escalated, HIGH) and every
	// routine break-glass/NHI-lifecycle event would self-amplify into an estate
	// stop. None of these fire the wildcard CRITICAL rule above.
	for _, k := range []string{
		"governance_approval_escalated", "governance_approval_expired",
		"governance_breakglass_activated", "governance_breakglass_used",
		"nhi_credential_blocked", "nhi_external_revoke_blocked",
	} {
		f.fire(k, sdkmodel.SeverityCritical, "agent", "a-1", "d-"+k)
	}
	if got := f.actions(); len(got) != 0 {
		t.Fatalf("a control-plane operational finding triggered containment: %d actions", len(got))
	}

	// A real critical finding from an EXTERNAL detector DOES stop the estate.
	f.fire("forensic_breach", sdkmodel.SeverityCritical, "agent", "a-1", "d-real")
	stops := f.stops()
	if len(stops) != 1 || stops[0].String(colKSScopeKind) != ksScopeEstate {
		t.Fatalf("estate auto-stop = %+v", stops)
	}
	// And the killswitch_engaged finding that emission produced, re-fed to the
	// loop, still does nothing.
	f.fire(findingKillSwitchEngaged, sdkmodel.SeverityCritical, "killswitch", stops[0].String(model.ColID), "d-ks2")
	if got := f.actions(); len(got) != 1 {
		t.Fatalf("feedback re-fire: %d actions", len(got))
	}
}

// A rule cannot be authored to match the control plane's own operational
// findings — the any-match default and an explicit governance_/nhi_ match are
// both refused at create time (the anti-feedback invariant is not opt-out).
func TestGuardianRuleRejectsControlPlaneKinds(t *testing.T) {
	for _, kind := range []string{"governance_approval_escalated", "nhi_credential_blocked", "killswitch_engaged", "guardian_action_executed"} {
		if msg := validateGuardianRuleFields(kind, "high", gaActionStopEstate, gaModeAuto, ""); msg == "" {
			t.Fatalf("match_kinds %q must be rejected (feedback loop)", kind)
		}
	}
	// A genuine external threat kind is accepted.
	if msg := validateGuardianRuleFields("guardrail_violation", "high", gaActionStopAgent, gaModeAuto, ""); msg != "" {
		t.Fatalf("a real threat kind must be accepted, got %q", msg)
	}
}

func TestGuardianQuarantineNHI(t *testing.T) {
	f := newGuardianFixture(t)
	ctx := context.Background()
	// An agent bound to an NHI identity (the access-map attribution bridge).
	var agentExt = "ext-agent-q"
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Create(ctx, model.Identity{Name: "svc-q", Kind: "vault_entity", ExternalID: "vault:nhi:q", Provider: "vault"})
		if err != nil {
			return err
		}
		_, err = sc.Agents().Create(ctx, model.Agent{Name: "q-bot", Kind: "claude-code", ExternalID: agentExt, Status: model.StatusActive, IdentityID: identity.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.createRule("quarantine-on-exfil", "exfil_attempt", "high", gaActionQuarantineNHI, gaModeAuto)

	f.fire("exfil_attempt", sdkmodel.SeverityHigh, "agent", agentExt, "d-q1")
	acts := f.actions()
	if len(acts) != 1 || acts[0].String(colGAStatus) != gaStatusExecuted {
		t.Fatalf("quarantine action = %+v", acts)
	}
	blocked, why, err := f.m.NHIEnforcement(ctx, f.tenant, "vault:nhi:q")
	if err != nil || !blocked {
		t.Fatalf("NHI must be blocked after quarantine: %v %q %v", blocked, why, err)
	}
	// Reversible by design: the restore path is the undo (no kill-switch
	// row was created — quarantine is identity-level containment).
	if got := f.stops(); len(got) != 0 {
		t.Fatalf("quarantine must not engage a kill switch: %+v", got)
	}

	// An agent with NO resolvable NHI records an HONEST failure, never silence.
	f.fire("exfil_attempt", sdkmodel.SeverityHigh, "agent", "ghost-agent", "d-q2")
	var failed bool
	for _, a := range f.actions() {
		if a.String(colGAStatus) == gaStatusFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("unresolvable NHI must record a failed action: %+v", f.actions())
	}
	if f.findingKinds()[findingGuardianFailed] != 1 {
		t.Fatalf("missing guardian_action_failed finding")
	}
}

func TestGuardianApprovalModeAndSweep(t *testing.T) {
	f := newGuardianFixture(t)
	ctx := context.Background()
	f.createRule("hitl-contain", "anomaly_detected", "high", gaActionStopAgent, gaModeApproval)

	f.fire("anomaly_detected", sdkmodel.SeverityHigh, "agent", "ext-agent-h", "d-h1")
	acts := f.actions()
	if len(acts) != 1 || acts[0].String(colGAStatus) != gaStatusPending {
		t.Fatalf("approval-mode action = %+v", acts)
	}
	approvalID := acts[0].String(colGAApprovalID)
	if approvalID == "" {
		t.Fatalf("pending action must bind its approval")
	}
	if f.findingKinds()[findingGuardianPending] != 1 {
		t.Fatalf("missing guardian_action_pending finding (the HITL ping)")
	}
	// Nothing is contained while the human has not decided.
	if got := f.stops(); len(got) != 0 {
		t.Fatalf("approval mode contained before the human decided: %+v", got)
	}
	res, err := f.m.GuardianSweep(ctx, f.tenant)
	if err != nil || res.Executed+res.Rejected+res.Expired+res.Failed != 0 {
		t.Fatalf("sweep before decision = %+v %v", res, err)
	}

	// The human approves (the engine's HTTP decide path is covered by the
	// harness tests; here the row flips directly to isolate sweep semantics).
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(approvalID))
		if err != nil {
			return err
		}
		rec[colStatus] = statusApproved
		rec[colApproveCount] = int64(1)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err = f.m.GuardianSweep(ctx, f.tenant)
	if err != nil || res.Executed != 1 {
		t.Fatalf("sweep after approval = %+v %v", res, err)
	}
	if got := f.stops(); len(got) != 1 || got[0].String(colKSScopeKind) != ksScopeAgent {
		t.Fatalf("approved containment must engage the stop: %+v", got)
	}
	acts = f.actions()
	if acts[0].String(colGAStatus) != gaStatusExecuted || acts[0].String(colGAKillswitchID) == "" {
		t.Fatalf("swept action = %+v", acts[0])
	}
	// Idempotent: a second sweep advances nothing.
	if res, err = f.m.GuardianSweep(ctx, f.tenant); err != nil || res.Executed != 0 {
		t.Fatalf("second sweep = %+v %v", res, err)
	}

	// A REJECTED containment never executes.
	f.fire("anomaly_detected", sdkmodel.SeverityHigh, "agent", "ext-agent-i", "d-h2")
	var pending model.Record
	for _, a := range f.actions() {
		if a.String(colGAStatus) == gaStatusPending {
			pending = a
		}
	}
	if pending == nil {
		t.Fatalf("expected a second pending action")
	}
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(pending.String(colGAApprovalID)))
		if err != nil {
			return err
		}
		rec[colStatus] = statusRejected
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if res, err = f.m.GuardianSweep(ctx, f.tenant); err != nil || res.Rejected != 1 {
		t.Fatalf("sweep after rejection = %+v %v", res, err)
	}
	if got := f.stops(); len(got) != 1 {
		t.Fatalf("a rejected containment must not engage: %d stops", len(got))
	}

	// An EXPIRED confirmation is stale intelligence — marked, never acted on.
	f.fire("anomaly_detected", sdkmodel.SeverityHigh, "agent", "ext-agent-j", "d-h3")
	for _, a := range f.actions() {
		if a.String(colGAStatus) == gaStatusPending {
			pending = a
		}
	}
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(pending.String(colGAApprovalID)))
		if err != nil {
			return err
		}
		rec[colExpiresAt] = model.NewTimestamp(intBase.Add(-time.Hour)).String()
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if res, err = f.m.GuardianSweep(ctx, f.tenant); err != nil || res.Expired != 1 {
		t.Fatalf("sweep after expiry = %+v %v", res, err)
	}
}

func TestDeleteGuardianRuleCancelsPendingAndPreservesEvidence(t *testing.T) {
	f := newGuardianFixture(t)
	ctx := context.Background()
	ruleID := f.createRule("delete-with-evidence", "anomaly_detected", "high", gaActionStopAgent, gaModeAuto)

	// First create executed evidence (including a kill-switch row bound by value
	// to this rule), then turn the same rule into approval mode and queue work.
	f.fire("anomaly_detected", sdkmodel.SeverityHigh, "agent", "already-contained", "d-executed")
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		rule, err := repo.Get(ctx, ruleID)
		if err != nil {
			return err
		}
		rule[colGRMode] = gaModeApproval
		_, err = repo.Update(ctx, rule)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.fire("anomaly_detected", sdkmodel.SeverityHigh, "agent", "queued-agent", "d-pending")

	var (
		executedID model.ID
		approvalID model.ID
	)
	for _, action := range f.actions() {
		switch action.String(colGAStatus) {
		case gaStatusExecuted:
			executedID = model.ID(action.String(model.ColID))
		case gaStatusPending:
			approvalID = model.ID(action.String(colGAApprovalID))
		}
	}
	if executedID.IsZero() || approvalID.IsZero() {
		t.Fatalf("fixture must have executed and pending evidence: %+v", f.actions())
	}

	route := chi.NewRouteContext()
	route.URLParams.Add("id", ruleID.String())
	req := httptest.NewRequest(http.MethodDelete, "/guardian/rules/"+ruleID.String(), nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	rec := httptest.NewRecorder()
	f.m.handleDeleteGuardianRule(rec, req, api.ModuleContext{
		Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()},
		Tenant:    f.tenant,
		Data:      api.NewScopedData(f.st, f.tenant),
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}

	var deleteAudit *model.AuditEvent
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		rules, err := sc.Ext(guardianRuleKind)
		if err != nil {
			return err
		}
		if _, err := rules.Get(ctx, ruleID); !isNotFound(err) {
			return fmt.Errorf("deleted rule lookup = %v, want not found", err)
		}

		approvals, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		approval, err := approvals.Get(ctx, approvalID)
		if err != nil {
			return err
		}
		if approval.String(colStatus) != statusCanceled || approval.String(colDecidedAt) == "" {
			return fmt.Errorf("bound approval = %+v, want canceled with decided_at", approval)
		}

		return sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == "governance.guardian.rule.delete" {
				copy := ev
				deleteAudit = &copy
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}

	actions := f.actions()
	if len(actions) != 2 {
		t.Fatalf("delete removed action-trail evidence: %+v", actions)
	}
	var executedPreserved, pendingCanceled bool
	for _, action := range actions {
		switch model.ID(action.String(model.ColID)) {
		case executedID:
			executedPreserved = action.String(colGAStatus) == gaStatusExecuted &&
				action.String(colGAKillswitchID) != ""
		default:
			pendingCanceled = action.String(colGAStatus) == gaStatusRejected &&
				action.String(colGADetail) == "rule deleted"
		}
	}
	if !executedPreserved || !pendingCanceled {
		t.Fatalf("trail after delete = %+v", actions)
	}
	stops := f.stops()
	if len(stops) != 1 || stops[0].String(colKSRuleRef) != ruleID.String() {
		t.Fatalf("delete cascaded into by-value kill-switch evidence: %+v", stops)
	}
	// Walk intentionally exposes the sealed ledger row without reconstructing
	// AuditDraft.Meta; action + target prove the self-audit row persisted. The
	// handler's draft supplies {name, pending_canceled}.
	if deleteAudit == nil || deleteAudit.TargetID != ruleID {
		t.Fatalf("delete audit = %+v", deleteAudit)
	}

	result, err := f.m.GuardianSweep(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if result.Executed+result.Rejected+result.Expired+result.Failed != 0 {
		t.Fatalf("deleted rule's action advanced in sweep: %+v", result)
	}
	if got := f.stops(); len(got) != 1 {
		t.Fatalf("deleted rule's queued containment executed: %+v", got)
	}
}
