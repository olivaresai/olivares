// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Decision ledger ops.
const (
	opOpenRequest = "open_request"
	opOpen        = "open"
	opClose       = "close"
)

// Decision op_status values.
const (
	opStatusRequested         = "requested"
	opStatusBlocked           = "blocked"
	opStatusDispatched        = "dispatched"
	opStatusDeclaredNotOpened = "declared_not_opened"
	opStatusFailed            = "failed"
	// FIN-08 budget enforcement (Denial-of-Wallet): an approved open denied because an
	// enforcing budget that scopes it is at its cap. block is a hard cap; throttle is a
	// soft cap (this period's budget exhausted, retry next period / after a top-up).
	opStatusBudgetBlocked   = "budget_blocked"
	opStatusBudgetThrottled = "budget_throttled"
)

// Policy verdicts.
const (
	verdictAllowed = "allowed"
	verdictDenied  = "denied"
)

const wildcard = "*"

// setPolicyRequest declares who may open a voice session with which model/provider.
type setPolicyRequest struct {
	AgentRef           string         `json:"agent_ref"`
	AllowedModelRef    string         `json:"allowed_model_ref"`
	AllowedProviderRef string         `json:"allowed_provider_ref"`
	MaxSessionMinutes  int64          `json:"max_session_minutes"`
	MaxLatencyMS       int64          `json:"max_latency_ms"`
	Calls              *callPolicyDTO `json:"calls,omitempty"`
}

// openRequest is the body of a two-phase voice-session open.
type openRequest struct {
	SessionRef  string `json:"session_ref"`
	AgentRef    string `json:"agent_ref"`
	ModelRef    string `json:"model_ref"`
	ProviderRef string `json:"provider_ref"`
	ApprovalRef string `json:"approval_ref"`
}

// openResponse reports the outcome of an open phase.
type openResponse struct {
	Op               string     `json:"op"`
	OpStatus         string     `json:"op_status"`
	PolicyVerdict    string     `json:"policy_verdict"`
	PlanHash         string     `json:"plan_hash,omitempty"`
	ApprovalRef      string     `json:"approval_ref,omitempty"`
	GateStatus       GateStatus `json:"gate_status,omitempty"`
	DispatchRef      string     `json:"dispatch_ref,omitempty"`
	RequiresApproval bool       `json:"requires_approval,omitempty"`
	Detail           string     `json:"detail,omitempty"`
}

// decisionRow is one row of the append-only open/close ledger.
type decisionRow struct {
	sessionRef    string
	agentRef      string
	reqModel      string
	reqProvider   string
	policyRef     string
	op            string
	policyVerdict string
	planHash      string
	approvalRef   string
	gateStatus    GateStatus
	opStatus      string
	dispatchRef   string
	actor         string
	actorKind     string
	detail        string
	result        string
}

// matchPolicy finds an allowing policy for (agent, model, provider), honoring "*"
// wildcards and preferring a specific-agent policy over a wildcard one. Default-DENY:
// no matching row → (nil, false).
func (m *Module) matchPolicy(ctx context.Context, sc store.Scope, agent, mdl, prov string) (model.Record, bool, error) {
	repo, err := sc.Ext(policyKind)
	if err != nil {
		return nil, false, err
	}
	try := func(agentKey string) (model.Record, bool, error) {
		cands, lerr := listAll(ctx, repo, eq(colPolAgentRef, agentKey))
		if lerr != nil {
			return nil, false, lerr
		}
		for _, p := range cands {
			am := p.String(colAllowedModel)
			ap := p.String(colAllowedProvi)
			if (am == mdl || am == wildcard) && (ap == prov || ap == wildcard) {
				return p, true, nil
			}
		}
		return nil, false, nil
	}
	if rec, ok, err := try(agent); err != nil || ok {
		return rec, ok, err
	}
	return try(wildcard)
}

// handleListPolicies lists the voice-open policies.
func (m *Module) handleListPolicies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[policyDTO]{Items: []policyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(policyKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toPolicyDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetPolicy upserts a voice-open policy (default-deny means a policy is what
// PERMITS an open). Admin-tier, self-audited.
func (m *Module) handleSetPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in setPolicyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.AgentRef == "" || in.AllowedModelRef == "" || in.AllowedProviderRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("agent_ref, allowed_model_ref and allowed_provider_ref are required (use \"*\" for any)"))
		return
	}
	callsJSON, ok := marshalCallPolicyForStore(in.Calls)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("calls patterns must be non-empty strings"))
		return
	}
	var dto policyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(policyKind)
		if err != nil {
			return err
		}
		filters := []model.Filter{
			eq(colPolAgentRef, clamp(in.AgentRef, maxRefLen)),
			eq(colAllowedModel, clamp(in.AllowedModelRef, maxRefLen)),
			eq(colAllowedProvi, clamp(in.AllowedProviderRef, maxRefLen)),
		}
		rec, found, err := findOne(r.Context(), repo, filters...)
		if err != nil {
			return err
		}
		if !found {
			rec = model.Record{
				colPolAgentRef: clamp(in.AgentRef, maxRefLen), colAllowedModel: clamp(in.AllowedModelRef, maxRefLen),
				colAllowedProvi: clamp(in.AllowedProviderRef, maxRefLen),
			}
		}
		rec[colMaxSessionMin] = in.MaxSessionMinutes
		rec[colMaxLatencyMS] = in.MaxLatencyMS
		if callsJSON != "" {
			rec[colCallsJSON] = callsJSON
		} else {
			delete(rec, colCallsJSON)
		}
		rec[colPolicySetBy] = mc.Principal.Actor()
		var saved model.Record
		if found {
			saved, err = repo.Update(r.Context(), rec)
		} else {
			saved, err = repo.Create(r.Context(), rec)
		}
		if err != nil {
			return err
		}
		dto = toPolicyDTO(saved)
		return auditEvent(r.Context(), sc, mc, "voice.policy.set", policyKind, model.ID(saved.String(model.ColID)),
			map[string]any{"agent_ref": dto.AgentRef, "allowed_model_ref": dto.AllowedModelRef, "allowed_provider_ref": dto.AllowedProviderRef})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func marshalCallPolicyForStore(c *callPolicyDTO) (string, bool) {
	if c == nil {
		return "", true
	}
	if !validCallPatterns(c.ToPatterns) || !validCallPatterns(c.FromPatterns) {
		return "", false
	}
	cp := *c
	cp.ToPatterns = trimmedCopy(c.ToPatterns)
	cp.FromPatterns = trimmedCopy(c.FromPatterns)
	b, err := json.Marshal(cp)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func validCallPatterns(patterns []string) bool {
	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			return false
		}
	}
	return true
}

func trimmedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// handleOpen is the two-phase governed voice-session open — the ONLY
// production-affecting path. It is default-deny on policy, then HITL-gated
// (deny-closed), plan_hash-bound, audited, and actuated only through the deny-closed
// dispatcher. The module never calls a provider Realtime API.
func (m *Module) handleOpen(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in openRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.SessionRef == "" || in.AgentRef == "" || in.ModelRef == "" || in.ProviderRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session_ref, agent_ref, model_ref and provider_ref are required"))
		return
	}

	// Estate kill switch: an active stop (estate-wide, or this agent)
	// freezes BOTH phases before anything else — no new approval request queues
	// and no approved open dispatches. FAIL-CLOSED on a gate error (the inverse
	// of the budget gate): an unreadable stop state never means "go".
	if m.stopBlocksOpen(w, r, mc, in) {
		return
	}

	// Step 1: policy evaluation (default-DENY).
	var policyRef string
	matched := false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		p, ok, perr := m.matchPolicy(r.Context(), sc, in.AgentRef, in.ModelRef, in.ProviderRef)
		if perr != nil {
			return perr
		}
		matched = ok
		if ok {
			policyRef = p.String(model.ColID)
		}
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !matched {
		m.recordBlocked(r.Context(), mc, decisionRow{
			sessionRef: in.SessionRef, agentRef: in.AgentRef, reqModel: in.ModelRef, reqProvider: in.ProviderRef,
			op: opOpenRequest, policyVerdict: verdictDenied, opStatus: opStatusBlocked,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: "no allowing voice policy",
		})
		writeJSON(w, http.StatusForbidden, openResponse{
			Op: opOpenRequest, OpStatus: opStatusBlocked, PolicyVerdict: verdictDenied,
			Detail: "no voice policy permits this (agent, model, provider) — default-deny",
		})
		return
	}

	planHash := hashHex(fmt.Sprintf("%s|%s|%s|%s|%s", in.SessionRef, in.AgentRef, in.ModelRef, in.ProviderRef, policyRef))

	if in.ApprovalRef == "" {
		m.openPhaseRequest(w, r, mc, in, policyRef, planHash)
		return
	}
	m.openPhaseDecide(w, r, mc, in, policyRef, planHash)
}

// openPhaseRequest opens (phase 1) a HITL approval for a voice open and records it.
func (m *Module) openPhaseRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, in openRequest, policyRef, planHash string) {
	decision, gerr := m.gate.Request(r.Context(), ApprovalRequest{
		Tenant: mc.Tenant, Action: "voice.session.open", SubjectKind: "agent", SubjectRef: in.AgentRef,
		PlanHash: planHash, RequestedBy: mc.Principal.Actor(),
	})
	if gerr != nil {
		m.errorf("voice: approval gate request failed", "session", in.SessionRef, "err", gerr)
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
		return
	}
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return m.recordDecision(r.Context(), sc, decisionRow{
			sessionRef: in.SessionRef, agentRef: in.AgentRef, reqModel: in.ModelRef, reqProvider: in.ProviderRef,
			policyRef: policyRef, op: opOpenRequest, policyVerdict: verdictAllowed, planHash: planHash,
			approvalRef: decision.ApprovalRef, gateStatus: decision.Status, opStatus: opStatusRequested,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: "approval requested",
		})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if decision.Status == StatusNoGate {
		m.reportUngovernedOpen(r.Context(), mc, in.AgentRef, in.SessionRef)
	}
	writeJSON(w, http.StatusAccepted, openResponse{
		Op: opOpenRequest, OpStatus: opStatusRequested, PolicyVerdict: verdictAllowed, PlanHash: planHash,
		ApprovalRef: decision.ApprovalRef, GateStatus: decision.Status, RequiresApproval: true,
		Detail: "approval requested; re-POST with approval_ref to open",
	})
}

// openPhaseDecide consumes (phase 2) the decision and, if approved and still matching
// the plan, opens through the deny-closed dispatcher.
func (m *Module) openPhaseDecide(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, in openRequest, policyRef, planHash string) {
	decision, gerr := m.gate.Status(r.Context(), mc.Tenant, in.ApprovalRef, planHash)
	if gerr != nil {
		m.errorf("voice: approval gate status failed", "session", in.SessionRef, "err", gerr)
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
		return
	}
	// Strict plan binding (anti-TOCTOU): the recomputed planHash is always a non-empty
	// SHA-256 over the (session|agent|model|provider|policy) tuple, so an approved
	// decision echoing an EMPTY or different plan hash is a non-match and is blocked —
	// an approval cannot be silently upgraded to a stronger model (contract).
	if !decision.Allowed() || decision.PlanHash != planHash {
		m.recordBlocked(r.Context(), mc, decisionRow{
			sessionRef: in.SessionRef, agentRef: in.AgentRef, reqModel: in.ModelRef, reqProvider: in.ProviderRef,
			policyRef: policyRef, op: opOpen, policyVerdict: verdictAllowed, planHash: planHash,
			approvalRef: in.ApprovalRef, gateStatus: decision.Status, opStatus: opStatusBlocked,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: "denied: " + string(decision.Status),
		})
		if decision.Status == StatusNoGate {
			m.reportUngovernedOpen(r.Context(), mc, in.AgentRef, in.SessionRef)
		}
		writeJSON(w, http.StatusForbidden, openResponse{
			Op: opOpen, OpStatus: opStatusBlocked, PolicyVerdict: verdictAllowed, PlanHash: planHash,
			ApprovalRef: in.ApprovalRef, GateStatus: decision.Status, RequiresApproval: true, Detail: "open denied (deny-by-default)",
		})
		return
	}

	// FIN-08 budget pre-flight: the SECOND, orthogonal gate. An open a human approved is
	// still denied when an enforcing budget that scopes it is at its cap (Denial-of-
	// Wallet). It runs AFTER approval (a rejected open never reaches it) and BEFORE
	// dispatch (capped spend never leaves). The denial is audited in the same ledger as
	// a distinct op_status; a budget-gate error fails OPEN (the open proceeds).
	if m.budgetBlocksOpen(w, r, mc, in, policyRef, planHash, decision.Status) {
		return
	}

	result, derr := m.dispatch.Open(r.Context(), OpenRequest{
		Tenant: mc.Tenant, SessionRef: in.SessionRef, AgentRef: in.AgentRef, ModelRef: in.ModelRef,
		ProviderRef: in.ProviderRef, PlanHash: planHash,
	})
	opStatus, dispatchRef, detail := opStatusDispatched, result.Ref, "dispatched"
	httpStatus := http.StatusOK
	if derr != nil {
		if errors.Is(derr, errNoDispatcher) {
			opStatus, dispatchRef, detail = opStatusDeclaredNotOpened, "", "approved; no dispatcher wired (declared, not opened)"
		} else {
			m.errorf("voice: dispatcher failed", "session", in.SessionRef, "err", derr)
			opStatus, dispatchRef, detail, httpStatus = opStatusFailed, "", "dispatcher error", http.StatusBadGateway
		}
	}

	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if opStatus != opStatusFailed {
			if err := m.markGovernedOpen(r.Context(), sc, in, policyRef, mc.Principal.Actor()); err != nil {
				return err
			}
		}
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			sessionRef: in.SessionRef, agentRef: in.AgentRef, reqModel: in.ModelRef, reqProvider: in.ProviderRef,
			policyRef: policyRef, op: opOpen, policyVerdict: verdictAllowed, planHash: planHash,
			approvalRef: in.ApprovalRef, gateStatus: decision.Status, opStatus: opStatus, dispatchRef: dispatchRef,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: detail,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "voice.session.open", sessionKind, parseIDOrZero(in.SessionRef),
			map[string]any{"session_ref": clamp(in.SessionRef, maxRefLen), "approval_ref": in.ApprovalRef, "plan_hash": planHash, "op_status": opStatus})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, httpStatus, openResponse{
		Op: opOpen, OpStatus: opStatus, PolicyVerdict: verdictAllowed, PlanHash: planHash,
		ApprovalRef: in.ApprovalRef, GateStatus: decision.Status, DispatchRef: dispatchRef, Detail: detail,
	})
}

// budgetBlocksOpen consults the FinOps budget gate before an open dispatches (FIN-08).
// It returns true when the open was DENIED — the response is already written and the
// caller must return: an enforcing budget (action=throttle|block) that scopes this
// (agent, model, provider) is at its cap. The denial is recorded in the append-only
// ledger as a distinct op_status (budget_blocked|budget_throttled) and audited
// (docs/SECURITY-HARDENING.md: minimal data — refs + action, never audio/transcript or a USD figure).
// It FAILS OPEN: a budget-gate error never blocks an approved open (the
// finops_budget_cap finding is the backstop), per finops.CheckBudget's contract.
func (m *Module) budgetBlocksOpen(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, in openRequest, policyRef, planHash string, gateStatus GateStatus) bool {
	verdict, err := m.budgetGate.Check(r.Context(), mc.Tenant, BudgetDims{
		AgentRef: in.AgentRef, SessionRef: in.SessionRef, ModelRef: in.ModelRef, ProviderRef: in.ProviderRef,
	})
	if err != nil {
		// Fail open: a FinOps outage must not take down an approved open.
		m.errorf("voice: budget gate error; failing open (approved open proceeds)", "session", in.SessionRef, "err", err)
		return false
	}
	if verdict.Allowed {
		return false
	}
	opStatus, httpStatus := opStatusBudgetBlocked, http.StatusPaymentRequired
	if verdict.Action == budgetActionThrottle {
		opStatus, httpStatus = opStatusBudgetThrottled, http.StatusTooManyRequests
	}
	detail := verdict.Reason
	if detail == "" {
		detail = "open denied: budget cap reached"
	}
	// Best-effort evidence (mirrors recordBlocked): the budget denial is AUTHORITATIVE
	// even if its ledger/audit write fails — a lost record is logged (docs/SECURITY-HARDENING.md), never
	// turned into a 5xx that would mask the denial. Spend is prevented regardless (the
	// dispatcher is never reached).
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			sessionRef: in.SessionRef, agentRef: in.AgentRef, reqModel: in.ModelRef, reqProvider: in.ProviderRef,
			policyRef: policyRef, op: opOpen, policyVerdict: verdictAllowed, planHash: planHash,
			approvalRef: in.ApprovalRef, gateStatus: gateStatus, opStatus: opStatus,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: detail,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "voice.session.open.budget_denied", sessionKind, parseIDOrZero(in.SessionRef),
			map[string]any{"session_ref": clamp(in.SessionRef, maxRefLen), "approval_ref": in.ApprovalRef, "plan_hash": planHash,
				"op_status": opStatus, "budget_ref": verdict.BudgetRef, "budget_action": verdict.Action})
	}); err != nil {
		m.errorf("voice: failed to record budget-denied open evidence", "session", in.SessionRef, "err", err)
	}
	writeJSON(w, httpStatus, openResponse{
		Op: opOpen, OpStatus: opStatus, PolicyVerdict: verdictAllowed, PlanHash: planHash,
		ApprovalRef: in.ApprovalRef, GateStatus: gateStatus, Detail: detail,
	})
	return true
}

// stopBlocksOpen consults the kill-switch gate before any open work. It
// returns true when the open was DENIED — the response is already written. The
// denial is recorded best-effort (the denial is authoritative even if the
// evidence write fails). It FAILS CLOSED: a gate error denies the open.
func (m *Module) stopBlocksOpen(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, in openRequest) bool {
	op := opOpenRequest
	if in.ApprovalRef != "" {
		op = opOpen
	}
	verdict, err := m.stopGate.Check(r.Context(), mc.Tenant, StopDims{AgentRef: in.AgentRef})
	if err != nil {
		m.errorf("voice: kill-switch gate error; failing CLOSED (open denied)", "session", in.SessionRef, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("kill-switch state unreadable; open denied (deny-closed)"))
		return true
	}
	if !verdict.Stopped {
		return false
	}
	detail := "denied: emergency stop active (" + verdict.Scope + " kill switch " + verdict.StopRef + "); re-enable requires dual-control"
	// Best-effort evidence with the DEDICATED audit action (mirroring the
	// orchestration/deploy siblings): the denial is authoritative even if the
	// write fails, and the stop reference rides the ledger Meta.
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			sessionRef: in.SessionRef, agentRef: in.AgentRef, reqModel: in.ModelRef, reqProvider: in.ProviderRef,
			op: op, opStatus: opStatusBlocked, approvalRef: in.ApprovalRef,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: detail,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "voice.session.open.killswitch_denied", sessionKind, parseIDOrZero(in.SessionRef),
			map[string]any{"session_ref": clamp(in.SessionRef, maxRefLen), "stop_ref": verdict.StopRef, "stop_scope": verdict.Scope})
	}); err != nil {
		m.errorf("voice: failed to record kill-switch-denied open evidence", "session", in.SessionRef, "err", err)
	}
	writeJSON(w, http.StatusLocked, openResponse{
		Op: op, OpStatus: opStatusBlocked, ApprovalRef: in.ApprovalRef, Detail: detail,
	})
	return true
}

// markGovernedOpen records the governance facts on the session row (created here if
// telemetry has not yet arrived): the real opener, the matched policy, the agreed
// model/provider, and governed=true.
func (m *Module) markGovernedOpen(ctx context.Context, sc store.Scope, in openRequest, policyRef, principal string) error {
	repo, err := sc.Ext(sessionKind)
	if err != nil {
		return err
	}
	rec, found, err := findOne(ctx, repo, eq(colSessionRef, clamp(in.SessionRef, maxRefLen)))
	if err != nil {
		return err
	}
	if !found {
		atTS := m.clock.Now().String()
		rec = model.Record{
			colSessionRef: clamp(in.SessionRef, maxRefLen),
			colUserTurns:  int64(0), colAgentTurns: int64(0), colDurationMS: int64(0),
			colLatencyCount: int64(0), colLatencySumMS: int64(0), colLatencyMaxMS: int64(0),
			colFirstEventAt: atTS, colLastEventAt: atTS,
		}
	}
	rec[colAgentRef] = clamp(in.AgentRef, maxRefLen)
	rec[colModelRef] = clamp(in.ModelRef, maxRefLen)
	rec[colProviderRef] = clamp(in.ProviderRef, maxRefLen)
	rec[colPrincipalRef] = principal
	rec[colPolicyRef] = policyRef
	rec[colGoverned] = true
	if found {
		_, err = repo.Update(ctx, rec)
	} else {
		_, err = repo.Create(ctx, rec)
	}
	return err
}

// recordDecision appends one immutable open/close ledger row.
func (m *Module) recordDecision(ctx context.Context, sc store.Scope, d decisionRow) error {
	repo, err := sc.Ext(decisionKind)
	if err != nil {
		return err
	}
	rec := model.Record{
		colDecSessionRef: clamp(d.sessionRef, maxRefLen), colDecAgentRef: clamp(d.agentRef, maxRefLen),
		colReqModelRef: clamp(d.reqModel, maxRefLen), colReqProviRef: clamp(d.reqProvider, maxRefLen),
		colOp: d.op, colPolicyVerdict: d.policyVerdict, colGateStatus: string(d.gateStatus), colOpStatus: d.opStatus,
		colActor: d.actor, colActorKind: d.actorKind, colOccurredAt: m.clock.Now().String(),
	}
	setIf(rec, colDecPolicyRef, d.policyRef)
	setIf(rec, colPlanHash, d.planHash)
	setIf(rec, colApprovalRef, d.approvalRef)
	setIf(rec, colDispatchRef, d.dispatchRef)
	if d.detail != "" {
		rec[colDetailHash] = hashHex(d.detail)
	}
	if d.result != "" {
		rec[colResult] = clamp(d.result, maxNameLen)
	}
	_, err = repo.Create(ctx, rec)
	return err
}

// recordBlocked records a denied open to the append-only ledger in its own
// transaction (best-effort), so the denial persists regardless of the response.
func (m *Module) recordBlocked(ctx context.Context, mc api.ModuleContext, d decisionRow) {
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		if err := m.recordDecision(ctx, sc, d); err != nil {
			return err
		}
		return auditEvent(ctx, sc, mc, "voice.session.open.blocked", sessionKind, parseIDOrZero(d.sessionRef),
			map[string]any{"session_ref": clamp(d.sessionRef, maxRefLen), "policy_verdict": d.policyVerdict, "gate_status": string(d.gateStatus)})
	}); err != nil {
		m.errorf("voice: failed to record blocked-open evidence", "session", d.sessionRef, "err", err)
	}
}

// reportUngovernedOpen records and emits a Finding that an open could not be governed
// because no approval gate is wired — an operator-visible gap (the open is still
// denied by default).
func (m *Module) reportUngovernedOpen(ctx context.Context, mc api.ModuleContext, agentRef, sessionRef string) {
	detail := fmt.Sprintf("session:%s open attempted with no approval gate wired", sessionRef)
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return m.persistFinding(ctx, sc, finding{
			kind: busUngovernedOpen, severity: sdkmodel.SeverityMedium, subjectKind: "agent", subjectRef: agentRef,
			title: "voice open blocked: no approval gate wired", detail: detail,
			meta: map[string]any{"session_ref": clamp(sessionRef, maxRefLen)},
		})
	}); err != nil {
		m.errorf("voice: failed to persist ungoverned-open finding", "session", sessionRef, "err", err)
	}
	m.emitFinding(ctx, mc.Tenant, busUngovernedOpen, sdkmodel.SeverityMedium, "agent", agentRef,
		"voice open blocked: no approval gate wired", detail)
}
