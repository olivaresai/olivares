// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk/event"
)

// govPath is the module route prefix.
const govPath = "/v1/m/governance"

func (h *harness) createApproval(token string, tenant model.TenantID, body map[string]any) resp {
	h.t.Helper()
	return h.do("POST", govPath+"/approvals", token, body, tenantHdr(tenant))
}

func (h *harness) decide(token string, tenant model.TenantID, id, decision string) resp {
	h.t.Helper()
	return h.do("POST", govPath+"/approvals/"+id+"/decisions", token, map[string]any{"decision": decision}, tenantHdr(tenant))
}

func resolvedPayloads(t *testing.T, h *harness) []event.ApprovalResolution {
	t.Helper()
	evs := h.host.ofType(event.TypeApprovalResolved)
	out := make([]event.ApprovalResolution, 0, len(evs))
	for _, e := range evs {
		res, ok := event.ApprovalResolutionOf(e)
		if !ok {
			t.Fatalf("approval.resolved payload has type %T", e.Payload)
		}
		out = append(out, res)
	}
	return out
}

func TestApprovalSingleApprovalLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "editor@x.io", "editor")

	r := h.createApproval(editor, tenant, map[string]any{"subject_kind": "deployment", "subject_ref": "deploy-1", "action": "deploy"})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r.body["status"] != "pending" {
		t.Fatalf("new request should be pending, got %v", r.body["status"])
	}
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("approve = %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	if got := resolved[0]; got.ApprovalID != id || got.Outcome != "approved" || got.ApproveCount != 1 || got.RejectCount != 0 || got.RequiredApprovals != 1 {
		t.Fatalf("approval.resolved payload = %+v", got)
	}
	if resolved[0].DecidedAt.IsZero() {
		t.Fatal("approval.resolved DecidedAt should be set")
	}
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("duplicate decision after terminal status must be 409, got %d %s", rr.code, rr.raw)
	}
	if got := len(h.host.ofType(event.TypeApprovalResolved)); got != 1 {
		t.Fatalf("terminal duplicate decision must not emit, got %d approval.resolved events", got)
	}
}

func TestApprovalSeparationOfDuty(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// A second admin who both requests and tries to decide their own request.
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	r := h.createApproval(a2, tenant, map[string]any{"action": "deploy"})
	id := r.body["id"].(string)
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusForbidden {
		t.Fatalf("requester deciding own request must be 403 (SoD), got %d %s", rr.code, rr.raw)
	}
	// A different admin can decide it.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK {
		t.Fatalf("a different admin should decide: %d %s", rr.code, rr.raw)
	}
}

func TestApprovalRequesterFieldsAreEngineOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	spoofed := model.NewID()
	r := h.createApproval(a2, tenant, map[string]any{"action": "deploy", "requestedby": "user:" + spoofed.String(), "requestedbyuser": spoofed.String()})
	if r.code != http.StatusBadRequest {
		t.Fatalf("wire input overrode the authenticated requester = %d %s, want 400", r.code, r.raw)
	}
	for _, field := range []string{"requested_by", "requested_by_user"} {
		r = h.createApproval(a2, tenant, map[string]any{"action": "deploy", field: spoofed.String()})
		if r.code != http.StatusBadRequest {
			t.Fatalf("%s wire input = %d %s, want 400", field, r.code, r.raw)
		}
	}
	r = h.createApproval(a2, tenant, map[string]any{"action": "deploy"})
	if r.code != http.StatusCreated {
		t.Fatalf("ordinary requester create = %d %s", r.code, r.raw)
	}
	if rr := h.decide(a2, tenant, r.body["id"].(string), "approve"); rr.code != http.StatusForbidden {
		t.Fatalf("creator approved their own request = %d %s, want 403", rr.code, rr.raw)
	}
}

func TestApprovalMultiApprovalAndDuplicateDecider(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	_, a3 := h.roleUser(admin, tenant, "a3@x.io", "admin")

	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2})
	id := r.body["id"].(string)
	if got := r.body["required_approvals"].(float64); got != 2 {
		t.Fatalf("required_approvals = %v, want 2", got)
	}
	// First approval keeps it pending.
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "pending" {
		t.Fatalf("first of two approvals should stay pending: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	// Same admin deciding again is rejected (duplicate decider keyed on user id).
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("duplicate decider must be 409, got %d %s", rr.code, rr.raw)
	}
	// A different admin crosses the threshold.
	if rr := h.decide(a3, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("second distinct approval should approve: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
}

func TestApprovalRejection(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2})
	id := r.body["id"].(string)
	if rr := h.decide(admin, tenant, id, "reject"); rr.code != http.StatusOK || rr.body["status"] != "rejected" {
		t.Fatalf("a single rejection should reject: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	if got := resolved[0]; got.ApprovalID != id || got.Outcome != "rejected" || got.ApproveCount != 0 || got.RejectCount != 1 {
		t.Fatalf("approval.resolved rejection payload = %+v", got)
	}
}

func TestApprovalLazyExpiryBlocksDecision(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "expires_in_seconds": 60})
	id := r.body["id"].(string)

	h.clk.advance(61 * time.Second)
	// Read reports the EFFECTIVE status (expired) even before a sweep persists it.
	if rr := h.do("GET", govPath+"/approvals/"+id, admin, nil, tenantHdr(tenant)); rr.body["status"] != "expired" {
		t.Fatalf("lazy expiry should report expired, got %v", rr.body["status"])
	}
	// A decision on a logically-expired request is refused.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("deciding an expired request must be 409, got %d %s", rr.code, rr.raw)
	}
}

func TestApprovalSweepEscalatesThenExpiresOnce(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "escalate_in_seconds": 30, "expires_in_seconds": 60})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	sweep := func() resp { return h.do("POST", govPath+"/approvals/sweep", admin, nil, tenantHdr(tenant)) }

	h.clk.advance(31 * time.Second) // past escalate, before expiry
	if rr := sweep(); rr.body["escalated"].(float64) != 1 || rr.body["expired"].(float64) != 0 {
		t.Fatalf("first sweep should escalate 1, expire 0: %s", rr.raw)
	}
	// A repeated sweep at the same time must NOT re-escalate (gated on escalated_at).
	if rr := sweep(); rr.body["escalated"].(float64) != 0 {
		t.Fatalf("repeated sweep must not double-escalate: %s", rr.raw)
	}
	h.clk.advance(30 * time.Second) // now past expiry
	if rr := sweep(); rr.body["expired"].(float64) != 1 {
		t.Fatalf("sweep past expiry should expire 1: %s", rr.raw)
	}
	// The now-expired request leaves the pending set; a further sweep is a no-op.
	if rr := sweep(); rr.body["scanned"].(float64) != 0 {
		t.Fatalf("expired request must not be re-scanned: %s", rr.raw)
	}

	fs := h.host.findings()
	esc, exp := 0, 0
	for _, f := range fs {
		switch f.Kind {
		case "governance_approval_escalated":
			esc++
		case "governance_approval_expired":
			exp++
		}
	}
	if esc != 1 || exp != 1 {
		t.Fatalf("expected exactly one escalation and one expiry finding, got esc=%d exp=%d", esc, exp)
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 || resolved[0].Outcome != "expired" || resolved[0].ApprovalID != r.body["id"].(string) {
		t.Fatalf("sweep must emit exactly one expired approval.resolved, got %+v", resolved)
	}
}

func TestApprovalSweepEscalationOnlyDoesNotResolve(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "escalate_in_seconds": 30, "expires_in_seconds": 60})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}

	h.clk.advance(31 * time.Second)
	rr := h.do("POST", govPath+"/approvals/sweep", admin, nil, tenantHdr(tenant))
	if rr.code != http.StatusOK || rr.body["escalated"].(float64) != 1 || rr.body["expired"].(float64) != 0 {
		t.Fatalf("sweep should escalate only: %d %s", rr.code, rr.raw)
	}
	if got := len(h.host.ofType(event.TypeApprovalResolved)); got != 0 {
		t.Fatalf("escalation-only sweep must not emit approval.resolved, got %d", got)
	}
}

func TestApprovalPolicyIsAuthoritativeOverRequest(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// An approval policy requiring two approvals for "deploy" actions.
	h.createApprovalPolicy(admin, tenant, "deploys-need-two", map[string]any{"required_approvals": 2, "match": map[string]any{"action": "deploy"}})
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	// The request tries to lower the bar to 1; the policy wins.
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 1})
	if got := r.body["required_approvals"].(float64); got != 2 {
		t.Fatalf("policy should force required_approvals=2, got %v", got)
	}
	if r.body["policy_ref"] == "" || r.body["policy_ref"] == nil {
		t.Fatal("the matched policy should be recorded on the request")
	}
	governance.WithPolicyResolver(tenantPolicyFallback{})(h.gov)
	fallback := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 1})
	if fallback.code != http.StatusCreated || fallback.body["required_approvals"] != r.body["required_approvals"] || fallback.body["policy_ref"] != r.body["policy_ref"] {
		t.Fatalf("delegating resolver changed tenant policy: %d %s", fallback.code, fallback.raw)
	}
}

// A session proposes its own launch approval, so no person requests it: policy
// selection asks for the person who launched the session, on open and on decide.
func TestSessionLaunchApprovalSelectsTheLaunchersPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "launch-policy")
	launcher, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &launcherPolicy{user: launcher.UserID.String(), action: "sessions.run.launch"}
	governance.WithPolicyResolver(resolver)(h.gov)
	session := cancellationSession(t, h, tenant, admin, "launch")
	a, err := h.gov.EngineApprovals().Request(context.Background(), tenant, session, governance.ApprovalRequest{
		Action: "sessions.run.launch", SubjectKind: "sessions.run", SubjectRef: "launch", SessionRef: session.SessionIdentity, LaunchedBy: launcher.Actor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.RequiredApprovals != 2 {
		t.Fatalf("required_approvals = %d, want the launcher's policy (2)", a.RequiredApprovals)
	}
	if r := h.decide(admin, tenant, a.ID, "approve"); r.code != http.StatusOK || r.body["status"] != "pending" {
		t.Fatalf("first of two approvals = %d %s", r.code, r.raw)
	}
	for _, u := range resolver.asked {
		if u != resolver.user {
			t.Fatalf("policy selection asked for %q, want the launcher %q (all asks: %q)", u, resolver.user, resolver.asked)
		}
	}
}

// A tool permission a session asks for, through the engine or over HTTP, selects the
// policy of the person who launched the session, like its launch does.
func TestSessionToolApprovalSelectsTheLaunchersPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tool-policy")
	launcher, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	governance.WithPolicyResolver(&launcherPolicy{user: launcher.UserID.String(), action: "sessions.provider.approval"})(h.gov)
	session := cancellationSession(t, h, tenant, admin, "tool")
	a, err := h.gov.EngineApprovals().Request(context.Background(), tenant, session, governance.ApprovalRequest{
		Action: "sessions.provider.approval", SubjectKind: "session_run", SubjectRef: "tool", SessionRef: session.SessionIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.RequiredApprovals != 2 {
		t.Fatalf("engine: required_approvals = %d, want the launcher's policy (2)", a.RequiredApprovals)
	}
	route := &approvalCreateRoute{}
	h.gov.APIRoutes(route)
	req := httptest.NewRequest(http.MethodPost, "/approvals", strings.NewReader(`{"action":"sessions.provider.approval","subject_kind":"session_run"}`))
	rec := httptest.NewRecorder()
	route.handler(rec, req, api.ModuleContext{Principal: session, Tenant: tenant, Data: api.NewScopedData(h.st, tenant)})
	var got governance.Approval
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusCreated || err != nil {
		t.Fatalf("http: create = %d %s", rec.Code, rec.Body.String())
	}
	if got.RequiredApprovals != 2 {
		t.Fatalf("http: required_approvals = %d, want the launcher's policy (2)", got.RequiredApprovals)
	}
}

type launcherPolicy struct {
	user, action string
	asked        []string
}

func (r *launcherPolicy) ApprovalPolicies(_ context.Context, _ store.Scope, user string) ([]model.Policy, bool, error) {
	r.asked = append(r.asked, user)
	if user != r.user {
		return nil, false, nil
	}
	spec := map[string]any{"required_approvals": 2, "match": map[string]any{"action": r.action}}
	return []model.Policy{{BaseFields: model.BaseFields{ID: model.NewID()}, Name: "launcher two", Kind: "approval", Spec: spec, Enabled: true}}, true, nil
}

func (*launcherPolicy) DLPRules(context.Context, store.Scope, string) ([]model.Record, bool, error) {
	return nil, false, nil
}

func TestApprovalDecisionTrail(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	if rr := h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{"decision": "approve", "note": "looks fine"}, tenantHdr(tenant)); rr.code != http.StatusOK {
		t.Fatalf("decide = %d %s", rr.code, rr.raw)
	}
	r := h.do("GET", govPath+"/approvals/"+id+"/decisions", admin, nil, tenantHdr(tenant))
	trail := items(r)
	if len(trail) != 1 {
		t.Fatalf("decision trail should have one entry, got %d (%s)", len(trail), r.raw)
	}
	entry := trail[0].(map[string]any)
	if entry["decision"] != "approve" || entry["note"] != "looks fine" {
		t.Fatalf("trail entry wrong: %v", entry)
	}
	if d, _ := entry["decider"].(string); len(d) < 5 || d[:5] != "user:" {
		t.Fatalf("decider should be a user actor id, got %q", entry["decider"])
	}
}

func TestApprovalRejectsCredentialInAction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	// action/subject_kind ride the immutable audit ledger Meta; a credential-shaped
	// value must be refused (docs/SECURITY-HARDENING.md), never sealed into the chain.
	if r := h.createApproval(editor, tenant, map[string]any{"action": "api_key=sk-leak", "subject_ref": "x"}); r.code != http.StatusBadRequest {
		t.Fatalf("a credential in action must be rejected, got %d %s", r.code, r.raw)
	}
}

func TestApprovalCancel(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_ = uid
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	// The requester can cancel their own pending request.
	if rr := h.do("POST", govPath+"/approvals/"+id+"/cancel", editor, nil, tenantHdr(tenant)); rr.code != http.StatusOK || rr.body["status"] != "canceled" {
		t.Fatalf("requester cancel = %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	if got := resolved[0]; got.ApprovalID != id || got.Outcome != "canceled" {
		t.Fatalf("approval.resolved cancel payload = %+v", got)
	}
	// A decision on a canceled request is refused.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("deciding a canceled request must be 409, got %d %s", rr.code, rr.raw)
	}
}

func TestApprovalResolvedPayloadIsMinimalData(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{
		"subject_kind": "deployment", "subject_ref": "deploy-7", "action": "deploy",
		"reason": "ship the hotfix",
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create approval = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	rr := h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{
		"decision": "approve", "note": "looks good",
	}, tenantHdr(tenant))
	if rr.code != http.StatusOK {
		t.Fatalf("decide = %d %s", rr.code, rr.raw)
	}

	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	wire, err := json.Marshal(resolved[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"ship the hotfix", "looks good", "deploy-7", "SubjectRef", "Reason", "Note"} {
		if strings.Contains(string(wire), leak) {
			t.Fatalf("approval.resolved wire payload leaks %q: %s", leak, wire)
		}
	}
}

func TestEngineApprovalReviewPolicyUsesEnabledScopedAuthoredMatch(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "review-policy")
	other := h.createOrg(admin, "other-review-policy")
	service := h.gov.EngineApprovals()
	check := func(t model.TenantID, action, kind, want string) {
		policy, err := service.ReviewPolicy(context.Background(), t, action, kind)
		if err != nil || policy != want {
			h.t.Fatalf("review(%s,%s)=%q %v want %q", action, kind, policy, err, want)
		}
	}
	check(tenant, "claude.tool.use", "claude.tool", "")
	create := func(name, action, kind string, enabled bool) string {
		r := h.do("POST", govPath+"/policies", admin, map[string]any{"name": name, "kind": "approval", "enabled": enabled, "spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": action, "subject_kind": kind}}}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			h.t.Fatalf("policy=%d %s", r.code, r.raw)
		}
		return r.body["id"].(string)
	}
	create("disabled ask", "claude.tool.use", "claude.tool", false)
	check(tenant, "claude.tool.use", "claude.tool", "")
	codex := create("Codex ask", "sessions.provider.approval", "session_run", true)
	check(tenant, "sessions.provider.approval", "session_run", codex)
	check(tenant, "sessions.provider.approval", "claude.tool", "")
	check(other, "sessions.provider.approval", "session_run", "")
	claude := create("Claude ask", "claude.tool.use", "claude.tool", true)
	check(tenant, "claude.tool.use", "claude.tool", claude)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReviewPolicy(canceled, tenant, "claude.tool.use", "claude.tool"); err == nil {
		t.Fatal("unavailable policy state became allow")
	}
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(context.Background(), model.Policy{Name: "unreadable", Kind: "approval", Enabled: true, Spec: map[string]any{"required_approvals": "invalid integer"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewPolicy(context.Background(), tenant, "claude.tool.use", "claude.tool"); err == nil {
		t.Fatal("unreadable enabled policy became allow")
	}
}

// The in-process service reads the same decision trail the REST route answers,
// and only inside the tenant it is asked about: another tenant's approval id
// yields an empty trail, never its rows.
func TestEngineApprovalsDecisionsMatchRESTTrailAndStayInTenant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	other := h.createOrg(admin, "other")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	if rr := h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{"decision": "approve", "note": "looks fine"}, tenantHdr(tenant)); rr.code != http.StatusOK {
		t.Fatalf("decide = %d %s", rr.code, rr.raw)
	}
	service := h.gov.EngineApprovals()
	got, err := service.Decisions(t.Context(), tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	rest := items(h.do("GET", govPath+"/approvals/"+id+"/decisions", admin, nil, tenantHdr(tenant)))
	if len(got) != 1 || len(rest) != 1 {
		t.Fatalf("service trail=%v rest trail=%v, want one entry each", got, rest)
	}
	entry := rest[0].(map[string]any)
	if got[0].Decision != "approve" || got[0].Note != "looks fine" || got[0].Decider != entry["decider"] || got[0].DeciderUser != entry["decider_user"] {
		t.Fatalf("service entry %+v differs from REST entry %v", got[0], entry)
	}
	foreign, err := service.Decisions(t.Context(), other, id)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("another tenant read %d decisions (err=%v), want none", len(foreign), err)
	}
	if rows, err := service.Decisions(t.Context(), "", id); err == nil || len(rows) != 0 {
		t.Fatalf("zero tenant read %d decisions (err=%v), want an error and none", len(rows), err)
	}
}

func TestApprovalHTTPSessionRefRequiresSessionIdentity(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "approval-session-binding")
	r := h.createApproval(admin, tenant, map[string]any{"action": "github.write", "session_ref": "another-session"})
	if r.code != http.StatusForbidden {
		t.Fatalf("unbound session_ref = %d %s, want 403", r.code, r.raw)
	}
	r = h.createApproval(admin, tenant, map[string]any{"action": "github.write"})
	if r.code != http.StatusCreated || r.body["session_ref"] != nil {
		t.Fatalf("human request outside a session = %d %s", r.code, r.raw)
	}
	principal := cancellationSession(t, h, tenant, admin, "http-own")
	route := &approvalCreateRoute{}
	h.gov.APIRoutes(route)
	if route.handler == nil {
		t.Fatal("approval create route is missing")
	}
	for _, tc := range []struct {
		name, ref string
		tenant    model.TenantID
		want      int
	}{
		{"matching", principal.SessionIdentity, tenant, http.StatusCreated},
		{"omitted", "", tenant, http.StatusCreated},
		{"foreign session", "another-session", tenant, http.StatusForbidden},
		{"foreign tenant", principal.SessionIdentity, model.NewTenantID(), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/approvals", strings.NewReader(`{"action":"github.write","session_ref":"`+tc.ref+`"}`))
			rec := httptest.NewRecorder()
			route.handler(rec, req, api.ModuleContext{Principal: principal, Tenant: tc.tenant, Data: api.NewScopedData(h.st, tc.tenant)})
			if rec.Code != tc.want {
				t.Fatalf("create = %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if rec.Code == http.StatusCreated {
				var got governance.Approval
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.SessionRef != principal.SessionIdentity || got.RequestedBy != "session:"+principal.SessionIdentity {
					t.Fatalf("unbound session request: %+v", got)
				}
			}
		})
	}

}

// Capture the registered handler to exercise session identities even though the
// HTTP admission layer currently grants no session token approval-write access.
type approvalCreateRoute struct{ handler api.ModuleHandler }

func (c *approvalCreateRoute) Handle(method, path string, _ auth.Permission, handler api.ModuleHandler) {
	if method == http.MethodPost && path == "/approvals" {
		c.handler = handler
	}
}
func (c *approvalCreateRoute) HandleEntity(method, path string, perm auth.Permission, _ api.EntityRef, handler api.ModuleHandler) {
	c.Handle(method, path, perm, handler)
}

func (h *harness) engineSessionApproval(tenant model.TenantID, action, kind, subject, session string) string {
	h.t.Helper()
	out, err := h.gov.EngineApprovals().Request(context.Background(), tenant, auth.Principal{SessionIdentity: session}, governance.ApprovalRequest{Action: action, SubjectKind: kind, SubjectRef: subject, SessionRef: session})
	if err != nil {
		h.t.Fatal(err)
	}
	return out.ID
}

type tenantPolicyFallback struct{}

func (tenantPolicyFallback) ApprovalPolicies(context.Context, store.Scope, string) ([]model.Policy, bool, error) {
	return nil, false, nil
}
func (tenantPolicyFallback) DLPRules(context.Context, store.Scope, string) ([]model.Record, bool, error) {
	return nil, false, nil
}
