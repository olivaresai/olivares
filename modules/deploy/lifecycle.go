// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Operation verbs (the op column).
const (
	opPlan   = "plan"
	opApply  = "apply"
	opVerify = "verify"
	opRetire = "retire"
)

// Operation outcomes (the op_status column).
const (
	opStatusPlanned   = "planned"   // a dry-run diff was computed (no mutation)
	opStatusRequested = "requested" // a governed mutation requested approval; awaiting decision
	opStatusBlocked   = "blocked"   // a governed mutation was denied (no/expired/rejected approval) — deny-by-default
	opStatusApplied   = "applied"   // the target was reconciled to the desired spec
	opStatusVerified  = "verified"  // real state was checked against desired (drift reported)
	opStatusRetired   = "retired"   // the deployment was removed from the target
	opStatusFailed    = "failed"    // the executor failed
	opStatusNoop      = "noop"      // nothing to do (already in the desired state) — idempotent
)

// actionApply / actionRetire are the governed actions presented to the gate.
const (
	actionApply  = "deploy.apply"
	actionRetire = "deploy.retire"
)

// planResponse is the dry-run diff between desired and real state.
type planResponse struct {
	PlanHash    string   `json:"plan_hash"`
	FromVersion int64    `json:"from_version"`
	ToVersion   int64    `json:"to_version"`
	UpToDate    bool     `json:"up_to_date"`
	Changes     []Change `json:"changes"`
}

// mutationResponse is the result of a governed apply/retire. When the gate has
// not yet approved, RequiresApproval is true and ApprovalRef carries the handle
// the operator (or the GitOps reconciler) presents on the next call.
type mutationResponse struct {
	Op               string     `json:"op"`
	PlanHash         string     `json:"plan_hash"`
	Version          int64      `json:"version"`
	Status           string     `json:"status"` // op_status
	RequiresApproval bool       `json:"requires_approval"`
	ApprovalRef      string     `json:"approval_ref,omitempty"`
	GateStatus       GateStatus `json:"gate_status,omitempty"`
	Changes          []Change   `json:"changes,omitempty"`
	Wirings          int        `json:"wirings,omitempty"`
	Detail           string     `json:"detail,omitempty"`
}

// mutationRequest is the optional body of a governed apply/retire. An empty
// approval_ref means PHASE 1 (request approval); a present one means PHASE 2
// (present the approval to execute). This two-phase shape is the dry-run/
// plan-before-apply discipline: nothing is mutated until an explicit, approved
// reference is presented (docs/SECURITY-HARDENING.md,§2).
type mutationRequest struct {
	ApprovalRef string `json:"approval_ref,omitempty"`
}

// execContext bundles the loaded definition state a lifecycle handler needs.
type execContext struct {
	def        model.Record
	spec       deploySpec
	specHash   string
	appliedVer int64
	currentVer int64
}

// loadExec loads a definition and its current desired spec for a lifecycle op.
func loadExec(ctx context.Context, sc store.Scope, id model.ID) (execContext, bool, error) {
	defRepo, err := sc.Ext(definitionKind)
	if err != nil {
		return execContext{}, false, err
	}
	rec, err := defRepo.Get(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return execContext{}, false, nil
		}
		return execContext{}, false, err
	}
	spec, specHash, ok, err := currentSpec(ctx, sc, rec)
	if err != nil || !ok {
		return execContext{}, false, err
	}
	return execContext{
		def: rec, spec: spec, specHash: specHash,
		appliedVer: rec.Int(colAppliedVer), currentVer: rec.Int(colCurrentVer),
	}, true, nil
}

// execRequestOf builds the executor request from the loaded definition + spec.
func (ec execContext) execRequestOf(tenant model.TenantID) ExecRequest {
	return ExecRequest{
		Tenant: tenant, Target: ec.def.String(colTarget), Runtime: ec.def.String(colRuntime),
		Environment: ec.def.String(colEnvironment),
		SubjectKind: ec.def.String(colSubjectKind), SubjectRef: ec.def.String(colSubjectRef), Spec: ec.spec,
	}
}

// subjectLabel is a short, non-sensitive subject reference for the approval.
func (ec execContext) subjectLabel() string {
	return ec.def.String(colDefName) + "@" + ec.def.String(colEnvironment)
}

// execUnavailable maps the fail-closed no-executor sentinel to 503 with a clear,
// honest message; any other executor error is a 502 (the backend infra failed).
//
// IT IS A MEMBER OF THE MAPPER FAMILY BY SIGNATURE AND NOT BY SUBJECT, and the
// StoreErrorStatus call below is how it earns that without an exemption. Measured
// 2026-08-12: all five call sites pass an error returned by the executor interface
// (m.exec.Plan/Apply/Verify/Retire at lifecycle.go:163,251,310,393,514), never one
// from the store — so ok is false for everything this function receives today, and
// the 502 is still what runs. Nothing changes.
//
// It is here because the alternative was an allowlist entry, and this repository
// has already written down what those cost: an exemption whose reason has expired
// is a hole with a comment on it (scripts/test-pg-test-env.sh:2150). The reason
// here is a premise about five call sites, which is exactly the kind that expires
// quietly. Consulting the shared mapping instead means the day a store error does
// reach this writer it gets 404/409/423/503 rather than a 502 blaming an executor
// that never ran.
func execUnavailable(w http.ResponseWriter, err error) {
	if err == errNoExecutor {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(errNoExecutor.Error()))
		return
	}
	if status, msg, ok := api.StoreErrorStatus(err); ok {
		writeJSON(w, status, errorBody(msg))
		return
	}
	writeJSON(w, http.StatusBadGateway, errorBody("runtime executor error: "+err.Error()))
}

// handlePlan is a DRY-RUN: it computes the diff between the desired spec and the
// real state on the target and mutates nothing. Write-tier; self-audited. The
// returned plan_hash is the value the governed apply binds its approval to.
func (m *Module) handlePlan(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	// Load (read), call the executor (external, no mutation), then record the plan.
	var (
		ec      execContext
		found   bool
		loadErr error
	)
	if loadErr = mc.Data.View(r.Context(), func(sc store.Scope) error {
		ec, found, loadErr = loadExec(r.Context(), sc, id)
		return loadErr
	}); loadErr != nil {
		writeStoreError(w, loadErr)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	changes, err := m.exec.Plan(r.Context(), ec.execRequestOf(mc.Tenant))
	if err != nil {
		execUnavailable(w, err)
		return
	}
	if changes == nil {
		changes = []Change{}
	}
	planHash := planHashOf(id.String(), ec.appliedVer, ec.currentVer, ec.specHash)
	out := planResponse{PlanHash: planHash, FromVersion: ec.appliedVer, ToVersion: ec.currentVer, UpToDate: len(changes) == 0, Changes: changes}
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := recordOperation(r.Context(), sc, m.clock, id, opPlan, ec.appliedVer, ec.currentVer, planHash, "", StatusNotRequired, opStatusPlanned, mc.Principal.Actor(), changeSummary(changes)); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "deploy.plan", definitionKind, id, map[string]any{"plan_hash": planHash, "changes": len(changes)})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApply reconciles the deployment to its desired spec — a GOVERNED
// INFRASTRUCTURE MUTATION. Admin-tier AND gated:
//
//   - If the executor reports NO changes, apply is an idempotent no-op: it needs
//     no approval, records a noop operation and returns.
//   - PHASE 1 (no approval_ref): it requests a HITL approval bound to the plan
//     hash and returns it WITHOUT mutating anything (the dry-run/plan-before-apply
//     discipline).
//   - PHASE 2 (approval_ref present): it checks the gate. ONLY an explicit
//     approval bound to the current plan hash proceeds; anything else
//     (pending/expired/rejected/no-gate/stale-plan) is DENIED — deny-by-default.
//
// On apply it materializes the declared wirings (the PERMITTED feed + the
// per-agent identity bridge), updates the core Deployment snapshot, advances the
// applied version and records the operation + a ledger self-audit (what/version/
// who-approved/result). The wiring edges are published only AFTER commit.
func (m *Module) handleApply(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !requireStepUp(w, mc) {
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in mutationRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	in.ApprovalRef = strings.TrimSpace(in.ApprovalRef)

	var (
		ec      execContext
		found   bool
		retired bool
		loadErr error
	)
	if loadErr = mc.Data.View(r.Context(), func(sc store.Scope) error {
		ec, found, loadErr = loadExec(r.Context(), sc, id)
		if loadErr == nil && found {
			retired = ec.def.String(colDesiredStatus) == desiredRetired
		}
		return loadErr
	}); loadErr != nil {
		writeStoreError(w, loadErr)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if retired {
		writeJSON(w, http.StatusConflict, errorBody("definition is retired; roll back or update it before applying"))
		return
	}

	// Estate kill switch: an active stop (estate-wide, or this subject
	// agent) freezes BOTH phases of apply — even an already-approved one. Note
	// the asymmetry this creates on purpose: a post-incident ROLLBACK is
	// "declare prior revision (ungated) + governed apply", so the apply leg
	// runs only AFTER the dual-control re-enable lifts the stop.
	if m.stopBlocksMutation(w, r, mc, id, opApply, ec) {
		return
	}

	req := ec.execRequestOf(mc.Tenant)
	planHash := planHashOf(id.String(), ec.appliedVer, ec.currentVer, ec.specHash)

	// Idempotent no-op: the real state already matches the desired spec.
	changes, err := m.exec.Plan(r.Context(), req)
	if err != nil {
		execUnavailable(w, err)
		return
	}
	if len(changes) == 0 {
		m.finishApply(w, r, mc, id, ec, planHash, "", StatusNotRequired, opStatusNoop, ExecResult{Detail: "already in desired state"}, true)
		return
	}

	// PHASE 1 — request approval, mutate nothing.
	if in.ApprovalRef == "" {
		decision, gerr := m.gate.Request(r.Context(), ApprovalRequest{
			Tenant: mc.Tenant, Action: actionApply, SubjectKind: "deployment", SubjectRef: ec.subjectLabel(),
			PlanHash: planHash, RequestedBy: mc.Principal.Actor(),
		})
		if gerr != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("approval gate error: "+gerr.Error()))
			return
		}
		if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			if err := recordOperation(r.Context(), sc, m.clock, id, opApply, ec.appliedVer, ec.currentVer, planHash, decision.ApprovalRef, decision.Status, opStatusRequested, mc.Principal.Actor(), "approval requested"); err != nil {
				return err
			}
			return auditEvent(r.Context(), sc, mc, "deploy.apply.request", definitionKind, id, map[string]any{"plan_hash": planHash, "approval_ref": decision.ApprovalRef})
		}); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, mutationResponse{
			Op: opApply, PlanHash: planHash, Version: ec.currentVer, Status: opStatusRequested,
			RequiresApproval: true, ApprovalRef: decision.ApprovalRef, GateStatus: decision.Status, Changes: changes,
		})
		return
	}

	// PHASE 2 — check the gate. Deny-by-default unless an explicit approval bound
	// to THIS plan is present.
	decision, gerr := m.gate.Status(r.Context(), mc.Tenant, in.ApprovalRef, planHash)
	if gerr != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate error: "+gerr.Error()))
		return
	}
	if !decision.Allowed() || decision.PlanHash != planHash {
		m.recordBlocked(r.Context(), mc, id, opApply, ec, planHash, in.ApprovalRef, decision)
		writeJSON(w, http.StatusForbidden, mutationResponse{
			Op: opApply, PlanHash: planHash, Status: opStatusBlocked, RequiresApproval: true,
			ApprovalRef: in.ApprovalRef, GateStatus: decision.Status,
			Detail: "denied by default: governance approval is " + string(decision.Status),
		})
		return
	}

	// Approved — execute the external mutation, then record the outcome.
	result, aerr := m.exec.Apply(r.Context(), req)
	if aerr != nil {
		if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			return recordOperation(r.Context(), sc, m.clock, id, opApply, ec.appliedVer, ec.currentVer, planHash, in.ApprovalRef, decision.Status, opStatusFailed, mc.Principal.Actor(), "apply failed")
		}); err != nil {
			m.errorf("deploy: failed to record apply-failure operation", "definition", id.String(), "err", err)
		}
		execUnavailable(w, aerr)
		return
	}
	m.finishApply(w, r, mc, id, ec, planHash, in.ApprovalRef, decision.Status, opStatusApplied, result, false)
}

// finishApply commits the post-apply state: it materializes wirings, updates the
// core Deployment snapshot, advances the applied version and records the
// operation + a ledger self-audit. Wiring edges are published AFTER commit so a
// rolled-back transaction never signals a permitted edge to the access map.
func (m *Module) finishApply(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, ec execContext, planHash, approvalRef string, gateStatus GateStatus, opStatus string, result ExecResult, noop bool) {
	var (
		pending []permittedEdge
		wires   int
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		edges, n, err := m.materializeWirings(r.Context(), sc, mc.Tenant, ec)
		if err != nil {
			return err
		}
		pending, wires = edges, n
		if err := m.upsertDeploymentSnapshot(r.Context(), sc, id, ec); err != nil {
			return err
		}
		action := "deploy.apply"
		if noop {
			action = "deploy.apply.noop"
		}
		if err := recordOperation(r.Context(), sc, m.clock, id, opApply, ec.appliedVer, ec.currentVer, planHash, approvalRef, gateStatus, opStatus, mc.Principal.Actor(), result.Detail); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, action, definitionKind, id, map[string]any{
			"version": ec.currentVer, "approval_ref": approvalRef, "plan_hash": planHash, "wirings": wires, "gate_status": string(gateStatus),
		})
	})
	if err != nil {
		// Honesty gap (docs/SECURITY-HARDENING.md): for a real apply the executor ALREADY mutated
		// infrastructure; if the state/ledger commit then fails, the real state and
		// our record diverge. We cannot fix it here, but we must not hide it — log it
		// loudly as an integrity event so a verify/reconcile can recover it.
		if !noop {
			m.errorf("deploy: infrastructure was mutated but the state/ledger commit failed — real state and record may diverge; reconcile with verify",
				"definition", id.String(), "version", ec.currentVer, "approval_ref", approvalRef, "err", err)
		}
		writeStoreError(w, err)
		return
	}
	// Publish the declared PERMITTED wiring edges so (sole AccessEdge writer)
	// reconciles them into the permitted side (SignalPolicy → permitted=true).
	m.publishPermittedEdges(r.Context(), mc.Tenant, pending)
	writeJSON(w, http.StatusOK, mutationResponse{
		Op: opApply, PlanHash: planHash, Version: ec.currentVer, Status: opStatus,
		ApprovalRef: approvalRef, GateStatus: gateStatus, Changes: result.Changes, Wirings: wires, Detail: result.Detail,
	})
}

// handleVerify reconciles the real state against the desired spec and updates the
// Deployment snapshot's status (drift is reported, not mutated). Write-tier, NOT
// gated (it touches no infrastructure). Self-audited.
func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		ec      execContext
		found   bool
		loadErr error
	)
	if loadErr = mc.Data.View(r.Context(), func(sc store.Scope) error {
		ec, found, loadErr = loadExec(r.Context(), sc, id)
		return loadErr
	}); loadErr != nil {
		writeStoreError(w, loadErr)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	result, err := m.exec.Verify(r.Context(), ec.execRequestOf(mc.Tenant))
	if err != nil {
		execUnavailable(w, err)
		return
	}
	if result.Changes == nil {
		result.Changes = []Change{}
	}
	inSync := len(result.Changes) == 0
	planHash := planHashOf(id.String(), ec.appliedVer, ec.currentVer, ec.specHash)
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if depID := model.ID(ec.def.String(colDeploymentID)); !depID.IsZero() {
			if dep, err := sc.Deployments().Get(r.Context(), depID); err == nil {
				dep.Status = deploymentStatusFor(inSync)
				if _, err := sc.Deployments().Update(r.Context(), dep); err != nil {
					return err
				}
			} else if !isNotFound(err) {
				return err
			}
		}
		if err := recordOperation(r.Context(), sc, m.clock, id, opVerify, ec.appliedVer, ec.currentVer, planHash, "", StatusNotRequired, opStatusVerified, mc.Principal.Actor(), changeSummary(result.Changes)); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "deploy.verify", definitionKind, id, map[string]any{"in_sync": inSync, "drift": len(result.Changes)})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"in_sync": inSync, "drift": result.Changes})
}

// handleRetire removes the deployment from the target — a GOVERNED MUTATION with
// the same two-phase, deny-by-default gating as apply. On success it marks the
// definition retired, revokes its wirings and updates the snapshot. Admin-tier.
func (m *Module) handleRetire(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !requireStepUp(w, mc) {
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in mutationRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	in.ApprovalRef = strings.TrimSpace(in.ApprovalRef)

	var (
		ec      execContext
		found   bool
		loadErr error
	)
	if loadErr = mc.Data.View(r.Context(), func(sc store.Scope) error {
		ec, found, loadErr = loadExec(r.Context(), sc, id)
		return loadErr
	}); loadErr != nil {
		writeStoreError(w, loadErr)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}

	// Estate kill switch: a stop freezes retire too (a teardown is an
	// infrastructure mutation; containment is the stop itself, not a destroy).
	if m.stopBlocksMutation(w, r, mc, id, opRetire, ec) {
		return
	}

	req := ec.execRequestOf(mc.Tenant)
	// The retire plan hash is bound to the applied version being torn down.
	planHash := planHashOf(id.String(), ec.appliedVer, 0, "retire:"+ec.specHash)

	// PHASE 1 — request approval, mutate nothing.
	if in.ApprovalRef == "" {
		decision, gerr := m.gate.Request(r.Context(), ApprovalRequest{
			Tenant: mc.Tenant, Action: actionRetire, SubjectKind: "deployment", SubjectRef: ec.subjectLabel(),
			PlanHash: planHash, RequestedBy: mc.Principal.Actor(),
		})
		if gerr != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("approval gate error: "+gerr.Error()))
			return
		}
		if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			if err := recordOperation(r.Context(), sc, m.clock, id, opRetire, ec.appliedVer, 0, planHash, decision.ApprovalRef, decision.Status, opStatusRequested, mc.Principal.Actor(), "approval requested"); err != nil {
				return err
			}
			return auditEvent(r.Context(), sc, mc, "deploy.retire.request", definitionKind, id, map[string]any{"plan_hash": planHash, "approval_ref": decision.ApprovalRef})
		}); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, mutationResponse{
			Op: opRetire, PlanHash: planHash, Status: opStatusRequested, RequiresApproval: true,
			ApprovalRef: decision.ApprovalRef, GateStatus: decision.Status,
		})
		return
	}

	// PHASE 2 — check the gate (deny-by-default).
	decision, gerr := m.gate.Status(r.Context(), mc.Tenant, in.ApprovalRef, planHash)
	if gerr != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate error: "+gerr.Error()))
		return
	}
	if !decision.Allowed() || decision.PlanHash != planHash {
		m.recordBlocked(r.Context(), mc, id, opRetire, ec, planHash, in.ApprovalRef, decision)
		writeJSON(w, http.StatusForbidden, mutationResponse{
			Op: opRetire, PlanHash: planHash, Status: opStatusBlocked, RequiresApproval: true,
			ApprovalRef: in.ApprovalRef, GateStatus: decision.Status,
			Detail: "denied by default: governance approval is " + string(decision.Status),
		})
		return
	}

	result, rerr := m.exec.Retire(r.Context(), req)
	if rerr != nil {
		if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			return recordOperation(r.Context(), sc, m.clock, id, opRetire, ec.appliedVer, 0, planHash, in.ApprovalRef, decision.Status, opStatusFailed, mc.Principal.Actor(), "retire failed")
		}); err != nil {
			m.errorf("deploy: failed to record retire-failure operation", "definition", id.String(), "err", err)
		}
		execUnavailable(w, rerr)
		return
	}
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		defRepo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		rec, err := defRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		rec[colDesiredStatus] = desiredRetired
		rec[colAppliedVer] = int64(0)
		if _, err := defRepo.Update(r.Context(), rec); err != nil {
			return err
		}
		if err := m.revokeWirings(r.Context(), sc, id); err != nil {
			return err
		}
		if depID := model.ID(rec.String(colDeploymentID)); !depID.IsZero() {
			if dep, err := sc.Deployments().Get(r.Context(), depID); err == nil {
				dep.Status = "retired"
				if _, err := sc.Deployments().Update(r.Context(), dep); err != nil {
					return err
				}
			} else if !isNotFound(err) {
				return err
			}
		}
		if err := recordOperation(r.Context(), sc, m.clock, id, opRetire, ec.appliedVer, 0, planHash, in.ApprovalRef, decision.Status, opStatusRetired, mc.Principal.Actor(), result.Detail); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "deploy.retire", definitionKind, id, map[string]any{"approval_ref": in.ApprovalRef, "plan_hash": planHash})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mutationResponse{Op: opRetire, PlanHash: planHash, Status: opStatusRetired, ApprovalRef: in.ApprovalRef, GateStatus: decision.Status, Detail: result.Detail})
}

// recordBlocked records a denied governed mutation to the append-only operation
// log (the evidence that a mutation was attempted and denied), in its own
// transaction so it persists regardless of the request outcome.
func (m *Module) recordBlocked(ctx context.Context, mc api.ModuleContext, id model.ID, op string, ec execContext, planHash, approvalRef string, decision GateDecision) {
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		if err := recordOperation(ctx, sc, m.clock, id, op, ec.appliedVer, ec.currentVer, planHash, approvalRef, decision.Status, opStatusBlocked, mc.Principal.Actor(), "denied: "+string(decision.Status)); err != nil {
			return err
		}
		return auditEvent(ctx, sc, mc, "deploy."+op+".blocked", definitionKind, id, map[string]any{"gate_status": string(decision.Status), "approval_ref": approvalRef})
	}); err != nil {
		// Best-effort: the denial (403) still returns; surface a lost denial-record.
		m.errorf("deploy: failed to record blocked-mutation evidence", "op", op, "definition", id.String(), "err", err)
	}
}

// stopBlocksMutation consults the kill-switch gate before any apply/retire
// work. It returns true when the mutation was DENIED — the response is already
// written. The denial is recorded best-effort in the operation log (the denial
// is authoritative even if the evidence write fails). It FAILS CLOSED: a gate
// error denies the mutation — an unreadable stop state never means "go".
func (m *Module) stopBlocksMutation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, op string, ec execContext) bool {
	dims := StopDims{}
	if ec.def.String(colSubjectKind) == "agent" {
		dims.AgentRef = ec.def.String(colSubjectRef)
	}
	verdict, err := m.stopGate.Check(r.Context(), mc.Tenant, dims)
	if err != nil {
		m.errorf("deploy: kill-switch gate error; failing CLOSED (mutation denied)", "op", op, "definition", id.String(), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("kill-switch state unreadable; "+op+" denied (deny-closed)"))
		return true
	}
	if !verdict.Stopped {
		return false
	}
	detail := "denied: emergency stop active (" + verdict.Scope + " kill switch " + verdict.StopRef + "); re-enable requires dual-control"
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := recordOperation(r.Context(), sc, m.clock, id, op, ec.appliedVer, ec.currentVer, "", "", "", opStatusBlocked, mc.Principal.Actor(), detail); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "deploy."+op+".killswitch_denied", definitionKind, id,
			map[string]any{"stop_ref": verdict.StopRef, "stop_scope": verdict.Scope})
	}); err != nil {
		m.errorf("deploy: failed to record kill-switch-denied mutation evidence", "op", op, "definition", id.String(), "err", err)
	}
	writeJSON(w, http.StatusLocked, mutationResponse{Op: op, Status: opStatusBlocked, Detail: detail})
	return true
}

// upsertDeploymentSnapshot creates or updates the canonical core Deployment row
// (the real/applied state the Terraform provider and read) and advances
// the definition's applied version, all version-checked within the caller's tx.
func (m *Module) upsertDeploymentSnapshot(ctx context.Context, sc store.Scope, id model.ID, ec execContext) error {
	defRepo, err := sc.Ext(definitionKind)
	if err != nil {
		return err
	}
	rec, err := defRepo.Get(ctx, id) // reload inside the tx for a fresh version
	if err != nil {
		return err
	}
	now := m.clock.Now()
	depID := model.ID(rec.String(colDeploymentID))
	meta := map[string]any{"definition_id": id.String(), "name": rec.String(colDefName), "subject_ref": rec.String(colSubjectRef)}
	if depID.IsZero() {
		dep, err := sc.Deployments().Create(ctx, model.Deployment{
			SubjectKind: rec.String(colSubjectKind), Target: rec.String(colTarget), Environment: rec.String(colEnvironment),
			Status: "active", Version: itoa(ec.currentVer), DeployedAt: now, ConfigHash: []byte(ec.specHash), Metadata: meta,
		})
		if err != nil {
			return err
		}
		rec[colDeploymentID] = dep.ID.String()
	} else {
		dep, err := sc.Deployments().Get(ctx, depID)
		if err != nil {
			if !isNotFound(err) {
				return err
			}
		} else {
			dep.Status, dep.Version, dep.DeployedAt, dep.ConfigHash = "active", itoa(ec.currentVer), now, []byte(ec.specHash)
			if _, err := sc.Deployments().Update(ctx, dep); err != nil {
				return err
			}
		}
	}
	rec[colAppliedVer] = ec.currentVer
	_, err = defRepo.Update(ctx, rec)
	return err
}

// recordOperation appends one immutable operation row (append-only change ledger).
func recordOperation(ctx context.Context, sc store.Scope, clock model.Clock, defID model.ID, op string, fromVer, toVer int64, planHash, approvalRef string, gateStatus GateStatus, opStatus, actor, result string) error {
	repo, err := sc.Ext(operationKind)
	if err != nil {
		return err
	}
	if len(result) > maxNoteLen {
		result = result[:maxNoteLen]
	}
	_, err = repo.Create(ctx, model.Record{
		colDefinitionRef: defID.String(), colOp: op, colFromVersion: fromVer, colToVersion: toVer,
		colPlanHash: planHash, colApprovalRef: approvalRef, colGateStatus: string(gateStatus),
		colOpStatus: opStatus, colActor: actor, colResult: result, colOccurredAt: clock.Now().String(),
	})
	return err
}

// changeSummary is a short, non-sensitive textual summary of a plan diff.
func changeSummary(changes []Change) string {
	if len(changes) == 0 {
		return "no changes (up to date)"
	}
	return itoa(int64(len(changes))) + " change(s)"
}

// deploymentStatusFor maps the verify result to a core Deployment status.
func deploymentStatusFor(inSync bool) string {
	if inSync {
		return "active"
	}
	return "drifted"
}
