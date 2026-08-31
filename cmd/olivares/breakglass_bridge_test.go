// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/recording"
)

// breakglass_bridge_test.go is the proof at the composition root, against the
// REAL engine: (1) the CRITICAL dual-authorization floor reaches every bridge
// consumer with zero bridge cooperation (the engine floors the threshold); (2) the
// break-glass emergency path authorizes loudly — distinct status, "breakglass:"
// reference, engine-recorded use — is time-boxed/revocable, and never overrides an
// explicit human rejection; (3) the erase gate carries real two-person evidence.

// tenantAID parses the harness's tenant A.
func tenantAID(t *testing.T, h *harness) model.TenantID {
	t.Helper()
	tid, err := model.ParseTenantID(h.tenantA)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	return tid
}

// activateBreakGlassE2E opens an emergency grant through the real governed API as
// the seeded admin human and returns the grant id.
func (h *harness) activateBreakGlassE2E(t *testing.T, matchAction, reason string) string {
	t.Helper()
	var dto struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if code := h.reqInto("POST", "/v1/m/governance/breakglass", h.adminToken, h.tenantA, map[string]any{
		"match_action": matchAction, "reason": reason,
	}, &dto); code != http.StatusCreated || dto.ID == "" || dto.Status != "active" {
		t.Fatalf("activate break-glass = %d id=%q status=%q", code, dto.ID, dto.Status)
	}
	return dto.ID
}

type httpResult struct {
	code int
	body []byte
}

// pauseBreakGlassGateRecorder is a channel barrier after the real Gate has
// reserved its exact session but before the module handler begins. Only the
// activation route is paused; every interleaving action still uses real HTTP.
type pauseBreakGlassGateRecorder struct {
	api.SessionRecorder
	gated   chan api.RecordingDecision
	release chan struct{}
}

func (p *pauseBreakGlassGateRecorder) Gate(ctx context.Context, call api.RecordedCall) (api.RecordingDecision, error) {
	dec, err := p.SessionRecorder.Gate(ctx, call)
	if err != nil || call.Namespace != "governance" || call.Method != http.MethodPost || call.Pattern != "/breakglass" {
		return dec, err
	}
	select {
	case p.gated <- dec:
	case <-ctx.Done():
		return api.RecordingDecision{}, ctx.Err()
	}
	select {
	case <-p.release:
		return dec, nil
	case <-ctx.Done():
		return api.RecordingDecision{}, ctx.Err()
	}
}

type sealFailingRecordingGate struct {
	*recording.Module
	err error
}

func (g *sealFailingRecordingGate) SealGrantInScope(context.Context, store.Scope, model.ID, auth.Principal) error {
	return g.err
}

func recordingSessionsForGrant(t *testing.T, h *harness, token, grant string) []map[string]any {
	t.Helper()
	return items(h.getJSON(token, h.tenantA, "/v1/m/recording/sessions?grant="+grant))
}

func waitRecordingDecision(t *testing.T, ch <-chan api.RecordingDecision) api.RecordingDecision {
	t.Helper()
	select {
	case dec := <-ch:
		return dec
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the activation Gate barrier")
		return api.RecordingDecision{}
	}
}

func waitHTTPResult(t *testing.T, ch <-chan httpResult) httpResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the paused activation response")
		return httpResult{}
	}
}

// A CRITICAL action opened through the bridge (which sends NO required_approvals)
// is floored at two distinct approvers BY THE ENGINE — one human cannot release
// it, two distinct humans can.
func TestBridgeCriticalActionFlooredAtTwoApprovers(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "crit-b@bridge.test")
	_, approverC := h.createApprover(t, "crit-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()

	ref, st, _, err := br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-crit-1", "prod deploy", "tester")
	if err != nil || st != nbPending {
		t.Fatalf("gateOnce = %q err=%v", st, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+ref)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("engine must floor a bridge-opened critical action at 2: required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	if code, body := h.decide(t, approverB, ref, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if _, st, _, _ = br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-crit-1", "prod deploy", "tester"); st != nbPending {
		t.Fatalf("one approver must NOT release a critical action, got %q", st)
	}
	if code, body := h.decide(t, approverC, ref, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	if _, st, _, _ = br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-crit-1", "prod deploy", "tester"); st != nbApproved {
		t.Fatalf("two distinct approvers must release it, got %q", st)
	}
}

// The emergency path end to end: a pending CRITICAL action proceeds under an
// active grant with a distinct status + "breakglass:" reference, the engine
// records the use, the underlying approval stays pending for the humans, and a
// revoke (or expiry) closes the valve for the persisted reference too.
func TestBridgeBreakGlassEmergencyPath(t *testing.T) {
	h := newHarness(t)
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()

	ref0, st, _, err := br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-bg-7", "incident", "tester")
	if err != nil || st != nbPending {
		t.Fatalf("gateOnce = %q err=%v", st, err)
	}

	grant := h.activateBreakGlassE2E(t, "deploy.*", "second approver unreachable, prod down (INC-42)")

	bgRef, st, bound, err := br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-bg-7", "incident", "tester")
	if err != nil || st != nbBreakGlass {
		t.Fatalf("under an active grant gateOnce must return break_glass, got %q err=%v", st, err)
	}
	if !strings.HasPrefix(bgRef, breakGlassRefPrefix+grant) || bound != "plan-bg-7" {
		t.Fatalf("break-glass ref must name the grant and bind the plan: ref=%q bound=%q", bgRef, bound)
	}
	if deployGateStatus(st) != deploy.StatusApproved {
		t.Fatal("break_glass must map to the module's approved status (the actuation proceeds)")
	}

	// The engine recorded the use against the exact action/subject/plan.
	uses := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/breakglass/"+grant+"/uses")
	items, _ := uses["items"].([]any)
	if len(items) == 0 {
		t.Fatal("a break-glass authorization must leave a use record")
	}
	use := items[0].(map[string]any)
	if use["action"] != "deploy.apply" || !strings.Contains(use["subject_ref"].(string), planBindingMarker+"plan-bg-7") {
		t.Fatalf("use record must carry the exact identity: %v", use)
	}

	// The underlying approval STAYS pending — the emergency never erases the
	// humans' queue.
	if got := h.approvalStatus(t, h.adminToken, ref0); got != "pending" {
		t.Fatalf("underlying approval = %q, want pending", got)
	}

	// status() on the persisted break-glass reference keeps authorizing while the
	// grant lives, with the plan hash decoded from the REFERENCE...
	if st, bound, err = br.status(ctx, tid, bgRef, "plan-bg-7"); err != nil || st != nbBreakGlass || bound != "plan-bg-7" {
		t.Fatalf("status(bgRef) = %q bound=%q err=%v", st, bound, err)
	}
	// ...and a revoke turns the same reference into a deny.
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+grant+"/revoke", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", code, body)
	}
	if st, _, _ = br.status(ctx, tid, bgRef, "plan-bg-7"); st != nbExpired {
		t.Fatalf("a revoked grant's reference must deny, got %q", st)
	}
	if _, st, _, _ = br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-bg-7", "incident", "tester"); st != nbPending {
		t.Fatalf("after revoke the gate must be back to pending, got %q", st)
	}
}

// Two-phase consumers (deploy/orchestration/voice) reach break-glass through
// status(): the approval they persisted stays pending, but the apply-time check
// proceeds under the grant — with the bound hash read from STORAGE.
func TestBridgeBreakGlassTwoPhaseStatusPath(t *testing.T) {
	h := newHarness(t)
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()

	ref, st, _, err := br.request(ctx, tid, "deploy.retire", "deployment", "svc/old", "plan-2p-3", "retire", "tester")
	if err != nil || st != nbPending {
		t.Fatalf("request = %q err=%v", st, err)
	}
	if st, _, _ = br.status(ctx, tid, ref, "plan-2p-3"); st != nbPending {
		t.Fatalf("status before grant = %q, want pending", st)
	}

	h.activateBreakGlassE2E(t, "", "unscoped emergency")

	st, bound, err := br.status(ctx, tid, ref, "plan-2p-3")
	if err != nil || st != nbBreakGlass || bound != "plan-2p-3" {
		t.Fatalf("status under grant = %q bound=%q err=%v (want break_glass with the stored plan)", st, bound, err)
	}
}

// An explicit human REJECTION is never overridden: the persisted reference stays
// rejected, and the one-shot gate refuses to consult break-glass for the exact
// identity two humans already refused.
func TestBridgeBreakGlassNeverOverridesRejection(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "rej-b@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()

	ref, st, _, err := br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-rej-9", "risky", "tester")
	if err != nil || st != nbPending {
		t.Fatalf("gateOnce = %q err=%v", st, err)
	}
	if code, body := h.decide(t, approverB, ref, "reject"); code != http.StatusOK {
		t.Fatalf("reject = %d: %s", code, body)
	}

	h.activateBreakGlassE2E(t, "", "emergency after rejection")

	// The persisted reference: rejected stands (status() never consumes for it).
	if st, _, _ = br.status(ctx, tid, ref, "plan-rej-9"); st != nbRejected {
		t.Fatalf("status of a rejected approval under a grant = %q, want rejected", st)
	}
	// The one-shot path: a fresh pending opens, but break-glass is NOT consulted
	// for an identity with an explicit rejection on record.
	if _, st, _, _ = br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-rej-9", "risky", "tester"); st != nbPending {
		t.Fatalf("break-glass must not override an explicit rejection, got %q", st)
	}
	// A DIFFERENT plan is a new question: the grant covers it.
	if _, st, _, _ = br.gateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-new-1", "re-planned", "tester"); st != nbBreakGlass {
		t.Fatalf("a re-planned change under an active grant should proceed, got %q", st)
	}
}

// The two-phase gates (deploy/orchestration/voice) re-open a FRESH pending
// approval whenever they re-enter phase 1 without a ref. Break-glass must NOT
// launder a rejection through that fresh row: status() of the new pending
// approval must still refuse to consume because THIS exact plan was rejected on
// an earlier (terminal) row.
func TestBridgeBreakGlassTwoPhaseNeverLaundersRejection(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "tp-rej-b@bridge.test")
	_, approverC := h.createApprover(t, "tp-rej-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()

	// Phase 1: open A1, then TWO humans reject it (deploy.retire is CRITICAL; a
	// single reject already terminates it, but two proves a deliberate refusal).
	refA1, st, _, err := br.request(ctx, tid, "deploy.retire", "deployment", "svc/old", "plan-tp-1", "retire", "tester")
	if err != nil || st != nbPending {
		t.Fatalf("request A1 = %q err=%v", st, err)
	}
	if code, body := h.decide(t, approverB, refA1, "reject"); code != http.StatusOK {
		t.Fatalf("reject A1 = %d: %s", code, body)
	}

	// An admin opens an emergency window covering the action.
	h.activateBreakGlassE2E(t, "deploy.*", "incident after the refusal")

	// The caller re-enters phase 1 with NO ref → a FRESH pending A2 with the
	// identical encoded subject/plan is minted.
	refA2, st, _, err := br.request(ctx, tid, "deploy.retire", "deployment", "svc/old", "plan-tp-1", "retire", "tester")
	if err != nil || st != nbPending {
		t.Fatalf("request A2 = %q err=%v", st, err)
	}
	if refA2 == refA1 {
		t.Fatalf("a rejected approval must not be reused; A2 should be a fresh row")
	}

	// Phase 2 on A2 must STILL deny: this exact plan was rejected, so break-glass
	// is not consulted even though A2 itself is only pending.
	st, _, err = br.status(ctx, tid, refA2, "plan-tp-1")
	if err != nil || st != nbPending {
		t.Fatalf("break-glass must not launder a rejection through a fresh two-phase approval, got %q err=%v", st, err)
	}
	_ = approverC
}

// The DB backstop: at most one unreviewed grant per tenant. The app-level check
// returns the friendly 409, and the (tenant_id, active_guard) unique index is the
// hard guarantee under concurrency; once the prior grant is reviewed, the guard
// clears and a new activation succeeds.
func TestBreakGlassActiveGuardBacksSingleUnreviewed(t *testing.T) {
	h := newHarness(t)
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor)) // unused beyond harness wiring
	_ = br
	g1 := h.activateBreakGlassE2E(t, "", "first")
	// A second activation while the first is unreviewed is refused.
	if code, body := h.req("POST", "/v1/m/governance/breakglass", h.adminToken, h.tenantA, map[string]any{"reason": "second"}); code != http.StatusConflict {
		t.Fatalf("second activation must 409 while one is unreviewed, got %d: %s", code, body)
	}
	// Revoke + post-review (a distinct admin) clears the guard.
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+g1+"/revoke", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", code, body)
	}
	_, reviewerTok := h.createApprover(t, "guard-rev@bridge.test")
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+g1+"/review", reviewerTok, h.tenantA, map[string]any{"note": "closed; reviewed"}); code != http.StatusOK {
		t.Fatalf("review = %d: %s", code, body)
	}
	// Now a fresh activation succeeds (the guard was cleared to NULL).
	if code, body := h.req("POST", "/v1/m/governance/breakglass", h.adminToken, h.tenantA, map[string]any{"reason": "third"}); code != http.StatusCreated {
		t.Fatalf("activation after review must succeed, got %d: %s", code, body)
	}
}

// A 200 post-review is the commit boundary for BOTH facts: the grant guard is
// released and its exact recording is sealed. Disable finding delivery entirely
// to prove the bus only announces that committed fact; then activate g2
// immediately, with no polling or sleep.
func TestBreakGlassReviewCommitsSealBeforeNotification(t *testing.T) {
	h := newHarness(t)
	g1 := h.activateBreakGlassE2E(t, "", "first incident")
	g1Sessions := recordingSessionsForGrant(t, h, h.adminToken, g1)
	if len(g1Sessions) != 1 {
		t.Fatalf("g1 must have exactly one bound recording session, got %v", g1Sessions)
	}
	s1 := g1Sessions[0]["id"].(string)

	if err := h.set.recorder.Stop(context.Background()); err != nil {
		t.Fatalf("stop reviewed-finding subscriber: %v", err)
	}
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+g1+"/revoke", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("revoke g1 = %d: %s", code, body)
	}
	_, reviewer := h.createApprover(t, "atomic-review@bridge.test")
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+g1+"/review", reviewer, h.tenantA,
		map[string]any{"note": "closed and independently reviewed"}); code != http.StatusOK {
		t.Fatalf("review g1 = %d: %s", code, body)
	}

	sealed := h.getJSON(reviewer, h.tenantA, "/v1/m/recording/sessions/"+s1)
	if sealed["status"] != "sealed" || sealed["seal_reason"] != "breakglass_review" {
		t.Fatalf("review returned 200 without a committed recording seal: status=%q reason=%q",
			sealed["status"], sealed["seal_reason"])
	}

	code, raw := h.req("POST", "/v1/m/governance/breakglass", h.adminToken, h.tenantA,
		map[string]any{"reason": "second incident"})
	if code != http.StatusCreated {
		t.Fatalf("activation after a 200 review must not depend on finding delivery, got %d: %s", code, raw)
	}
	var g2DTO struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &g2DTO); err != nil || g2DTO.ID == "" {
		t.Fatalf("decode g2 activation: id=%q err=%v body=%s", g2DTO.ID, err, raw)
	}

	g1Sessions = recordingSessionsForGrant(t, h, reviewer, g1)
	g2Sessions := recordingSessionsForGrant(t, h, reviewer, g2DTO.ID)
	if len(g1Sessions) != 1 || g1Sessions[0]["id"] != s1 || g1Sessions[0]["status"] != "sealed" {
		t.Fatalf("reviewed grant must retain its exact sealed recording: got %v", g1Sessions)
	}
	if len(g2Sessions) != 1 || g2Sessions[0]["status"] != "active" || g2Sessions[0]["id"] == s1 {
		t.Fatalf("new grant must bind a distinct active recording: g1=%v g2=%v", g1Sessions, g2Sessions)
	}
}

// Pause an activation after Gate returns S1. Seal S1 and open S2 for the same
// credential through public HTTP before the handler resumes. The activation
// must validate S1 itself: accepting S1 because BindGrant ignores state, or
// re-resolving to S2, would both return a frame-losing 201.
func TestBreakGlassActivationValidatesExactGateSession(t *testing.T) {
	pause := &pauseBreakGlassGateRecorder{
		gated:   make(chan api.RecordingDecision),
		release: make(chan struct{}),
	}
	h := newHarnessWithRecorder(t, func(delegate api.SessionRecorder) api.SessionRecorder {
		pause.SessionRecorder = delegate
		return pause
	})
	defer func() {
		select {
		case <-pause.release:
		default:
			close(pause.release)
		}
	}()
	_, reviewer := h.createApprover(t, "exact-session@bridge.test")

	result := make(chan httpResult, 1)
	go func() {
		code, body := h.req("POST", "/v1/m/governance/breakglass", h.adminToken, h.tenantA,
			map[string]any{"reason": "paused activation"})
		result <- httpResult{code: code, body: body}
	}()

	decision := waitRecordingDecision(t, pause.gated)
	if decision.Session.IsZero() {
		t.Fatal("Gate barrier returned a zero session")
	}
	s1 := decision.Session.String()
	if code, body := h.req("POST", "/v1/m/recording/sessions/"+s1+"/seal", reviewer, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("seal exact Gate session S1 = %d: %s", code, body)
	}
	s1DTO := h.getJSON(reviewer, h.tenantA, "/v1/m/recording/sessions/"+s1)
	if s1DTO["status"] != "sealed" {
		t.Fatalf("S1 must be sealed before activation resumes: %v", s1DTO)
	}

	// Open S2 with the SAME activator credential. This makes a mutant that
	// re-resolves by cred+open_guard observably choose the wrong session.
	if code, body := h.req("GET", "/v1/m/governance/breakglass", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("open replacement recording S2 = %d: %s", code, body)
	}
	active := items(h.getJSON(reviewer, h.tenantA, "/v1/m/recording/sessions?status=active"))
	replacementFound := false
	for _, sess := range active {
		if sess["cred"] == s1DTO["cred"] && sess["id"] != s1 {
			replacementFound = true
		}
	}
	if !replacementFound {
		t.Fatalf("test precondition: no active S2 for S1 credential %q in %v", s1DTO["cred"], active)
	}

	close(pause.release)
	res := waitHTTPResult(t, result)
	if res.code != http.StatusPreconditionFailed {
		t.Fatalf("activation must validate the exact session returned by Gate; got %d after S1 was sealed and S2 opened: %s",
			res.code, res.body)
	}
	grants := items(h.getJSON(reviewer, h.tenantA, "/v1/m/governance/breakglass"))
	if len(grants) != 0 {
		t.Fatalf("failed exact-session validation must roll back grant creation, got %v", grants)
	}
	s1DTO = h.getJSON(reviewer, h.tenantA, "/v1/m/recording/sessions/"+s1)
	if s1DTO["status"] != "sealed" || s1DTO["breakglass_grant"] != nil {
		t.Fatalf("sealed S1 must remain unbound after denied activation: %v", s1DTO)
	}
}

// If the recording seal cannot be committed, review must not acknowledge the
// loop as closed. The grant update, active_guard release and recording seal are
// one transaction, so every observable fact remains pre-review.
func TestBreakGlassReviewRollsBackWhenSealFails(t *testing.T) {
	h := newHarness(t)
	g1 := h.activateBreakGlassE2E(t, "", "seal failure incident")
	g1Sessions := recordingSessionsForGrant(t, h, h.adminToken, g1)
	if len(g1Sessions) != 1 {
		t.Fatalf("g1 must have one recording before review, got %v", g1Sessions)
	}
	s1 := g1Sessions[0]["id"].(string)
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+g1+"/revoke", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("revoke g1 = %d: %s", code, body)
	}
	_, reviewer := h.createApprover(t, "seal-fail@bridge.test")
	h.set.gov.UseRecordingGate(&sealFailingRecordingGate{
		Module: h.set.recorder,
		err:    store.ErrAuditSpoolFull,
	})

	code, body := h.req("POST", "/v1/m/governance/breakglass/"+g1+"/review", reviewer, h.tenantA,
		map[string]any{"note": "must roll back"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("review with an uncommittable recording seal = %d, want 503: %s", code, body)
	}
	grant := h.getJSON(reviewer, h.tenantA, "/v1/m/governance/breakglass/"+g1)
	if grant["reviewed"] != false {
		t.Fatalf("failed seal must roll back reviewed=true: %v", grant)
	}
	session := h.getJSON(reviewer, h.tenantA, "/v1/m/recording/sessions/"+s1)
	if session["status"] != "active" {
		t.Fatalf("failed seal must leave the recording active: %v", session)
	}
	if code, body := h.req("POST", "/v1/m/governance/breakglass", h.adminToken, h.tenantA,
		map[string]any{"reason": "must remain blocked"}); code != http.StatusConflict {
		t.Fatalf("failed review must retain active_guard and block another activation, got %d: %s", code, body)
	}
}

// The MCP destructive-tool gate is one-shot: a human approval must take effect on
// the retry of the same call (gateOnce reuse). Before it used the two-phase
// request(), which never reuses an approved grant — deny-forever after approval.
// mcp.tool.call only ever gates DESTRUCTIVE tools, so it is CRITICAL — the engine
// floors it at two distinct approvers.
func TestMCPToolGateApprovalTakesEffect(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "mcp-b@bridge.test")
	_, approverC := h.createApprover(t, "mcp-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	gate := mcpToolGate{bridge: br, tenant: tenantAID(t, h)}
	ctx := context.Background()

	req := mcpc.ToolApprovalRequest{Tenant: h.tenantA, Tool: "db.drop_table", PlanHash: "plan-mcp-1", RequestedBy: "agent"}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != mcpc.StatusPending {
		t.Fatalf("first authorize = %v err=%v", d.Status, err)
	}
	// A destructive MCP tool-call is CRITICAL: one approver leaves it pending.
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+d.ApprovalRef)
	if m["risk_tier"] != "critical" || m["required_approvals"] != float64(2) {
		t.Fatalf("mcp.tool.call must be critical/floored: tier=%v required=%v", m["risk_tier"], m["required_approvals"])
	}
	if code, body := h.decide(t, approverB, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d, _ = gate.Authorize(ctx, req); d.Status != mcpc.StatusPending {
		t.Fatalf("one approver must not release a critical destructive tool-call, got %v", d.Status)
	}
	if code, body := h.decide(t, approverC, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d, err = gate.Authorize(ctx, req)
	if err != nil || d.Status != mcpc.StatusApproved || !d.Allowed() {
		t.Fatalf("two approvers must take effect on the retry, got %v err=%v", d.Status, err)
	}
}

// The erase gate: the irreversible RTBF deletion demands the engine-floored
// two-person quorum AND carries the distinct-approver evidence the connector
// re-verifies. No break-glass shortcut exists on this path.
func TestEraseGateDualControlEvidence(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "erase-b@bridge.test")
	_, approverC := h.createApprover(t, "erase-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	gate := br.eraseGate(tid)
	ctx := context.Background()

	req := claudecompliance.EraseRequest{
		Tenant: h.tenantA, Target: claudecompliance.EraseChat, SubjectRef: "chat-123",
		CaseRef: "RTBF-77", PlanHash: "plan-erase-1", RequestedBy: "dpo@x.test",
	}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != claudecompliance.ErasePending || d.Allowed() {
		t.Fatalf("first authorize = %v err=%v", d.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+d.ApprovalRef)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("compliance.content.erase must be critical/floored: required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	// Even an ACTIVE emergency grant cannot release an erasure: the erase gate
	// never consults break-glass, and a break-glass decision would carry no
	// approvers anyway (the connector's quorum re-check is independent).
	h.activateBreakGlassE2E(t, "", "emergency during an RTBF case")
	if d, _ = gate.Authorize(ctx, req); d.Status != claudecompliance.ErasePending || d.Allowed() {
		t.Fatalf("break-glass must not touch the erase path, got %v", d.Status)
	}

	if code, body := h.decide(t, approverB, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d, _ = gate.Authorize(ctx, req); d.Status != claudecompliance.ErasePending || d.HasDualControl() {
		t.Fatalf("one approver must not satisfy the erase quorum: %v approvers=%v", d.Status, d.Approvers)
	}
	if code, body := h.decide(t, approverC, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d, err = gate.Authorize(ctx, req)
	if err != nil || d.Status != claudecompliance.EraseApproved {
		t.Fatalf("final authorize = %v err=%v", d.Status, err)
	}
	if !d.HasDualControl() || !d.Allowed() {
		t.Fatalf("the decision must carry ≥2 distinct approver principals, got %v", d.Approvers)
	}
}
