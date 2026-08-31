// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// This file defines the three integration SEAMS this module depends on but does
// not own, each expressed in the module's own terms so the module stays
// decoupled from its neighbors' packages (the same way governance defined a
// RosterBinding port rather than importing access-map). The composition root
// injects real adapters; until it exists (the honest Fase C caveat), each port
// defaults to a SAFE, deny-closed behavior so an un-wired deployment cannot
// silently mutate infrastructure.

// ----------------------------------------------------------------------------
// ApprovalGate — the human-in-the-loop seam.
// ----------------------------------------------------------------------------

// GateStatus is the effective decision the gate reports for a request. The
// non-"approved" values are all DENY: only StatusApproved authorizes a mutation.
type GateStatus string

const (
	// StatusApproved is the only value that authorizes a governed mutation.
	StatusApproved GateStatus = "approved"
	// StatusPending means a HITL request is open but undecided — deny, keep waiting.
	StatusPending GateStatus = "pending"
	// StatusRejected means a human rejected the request — deny.
	StatusRejected GateStatus = "rejected"
	// StatusExpired means the approval lapsed before apply — deny (deny-by-default at expiry).
	StatusExpired GateStatus = "expired"
	// StatusNoGate means no approval gate is wired — deny (deny-by-default).
	StatusNoGate GateStatus = "no_gate"
	// StatusNotRequired marks an operation that mutates nothing (an idempotent
	// no-op or a pure desired-state declaration), so no approval is consumed.
	StatusNotRequired GateStatus = "not_required"
)

// ApprovalRequest describes an infrastructure mutation that needs governance.
// PlanHash binds the request to the EXACT diff that was planned, so an approval
// can never authorize a different change than the one a human saw (anti-TOCTOU):
// a re-plan that changes the diff changes the hash and needs a fresh approval.
type ApprovalRequest struct {
	// Tenant is the business tenant the mutation targets.
	Tenant model.TenantID
	// Action is the governed action, e.g. "deploy.apply" / "deploy.retire".
	Action string
	// SubjectKind/SubjectRef name what is being mutated (a deployment).
	SubjectKind string
	SubjectRef  string
	// PlanHash is the hash of the planned transition the approval is bound to.
	PlanHash string
	// RequestedBy is the audit-actor string of the principal asking (provenance).
	RequestedBy string
}

// GateDecision is the gate's answer for one approval reference.
type GateDecision struct {
	// ApprovalRef is the governance approval id (opaque to this module).
	ApprovalRef string
	// Status is the effective decision. Allowed() is true only when approved.
	Status GateStatus
	// PlanHash is the plan the approval was bound to, echoed back so apply can
	// confirm the approved plan still matches the plan it is about to execute.
	PlanHash string
}

// Allowed reports whether this decision authorizes the mutation. It is true ONLY
// for an explicit approval — every other status (including the unset zero value)
// is a deny, which is the whole point of deny-by-default.
func (d GateDecision) Allowed() bool { return d.Status == StatusApproved }

// ApprovalGate is the governance HITL seam. plan REQUESTS an approval for a
// transition; apply/retire CHECK the effective decision before executing. The
// real adapter bridges to (POST /v1/m/governance/approvals + its decision
// trail); this module never decides — it asks and consumes the result.
type ApprovalGate interface {
	// Request opens (or finds, idempotently by plan hash) a HITL request for a
	// mutation and returns its reference.
	Request(ctx context.Context, req ApprovalRequest) (GateDecision, error)
	// Status returns the current effective decision for a previously requested
	// approval, keyed to the plan it was bound to.
	Status(ctx context.Context, tenant model.TenantID, approvalRef, planHash string) (GateDecision, error)
}

// denyGate is the deny-closed default: every governed mutation is denied until a
// real gate is wired. Request still returns a deterministic reference (so a plan
// can be recorded), but Status always denies — so apply blocks. This is NOT a
// silent no-op: it is the safest possible behavior and Start() warns once.
type denyGate struct{}

func (denyGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: StatusNoGate, PlanHash: req.PlanHash}, nil
}

func (denyGate) Status(_ context.Context, _ model.TenantID, approvalRef, planHash string) (GateDecision, error) {
	return GateDecision{ApprovalRef: approvalRef, Status: StatusNoGate, PlanHash: planHash}, nil
}

// ----------------------------------------------------------------------------
// Executor — the runtime/IaC execution seam (runtimes or a Terraform/Pulumi
// backend). Connectors DISCOVER (read-only); the action is here, behind this
// port. The default fails closed: the control plane can declare desired state,
// but cannot reconcile to real infrastructure until an executor is wired.
// ----------------------------------------------------------------------------

// Change is one element of a plan diff between desired and real state. It is a
// minimal-data summary (a kind + a short, non-sensitive description), never a
// payload, command line or secret.
type Change struct {
	// Kind is "create" | "update" | "delete" | "noop".
	Kind string `json:"kind"`
	// Resource names what changes (e.g. "container", "deployment", "wiring").
	Resource string `json:"resource"`
	// Detail is a short, non-sensitive description of the change.
	Detail string `json:"detail,omitempty"`
}

// ExecRequest is what the executor needs to reconcile one deployment: where
// (target/runtime/environment), what (the typed desired spec), and the subject.
type ExecRequest struct {
	Tenant  model.TenantID
	Target  string
	Runtime string
	// Environment is the deployment's environment ("prod" | "staging" | ...). The
	// real executor scopes its short-lived, attested credential per environment
	// (least privilege), so the seam carries it (a backward-compatible addition; the
	// in-memory mock executor ignores it).
	Environment string
	SubjectKind string
	SubjectRef  string
	Spec        deploySpec
}

// ExecResult is the outcome of an apply/verify/retire.
type ExecResult struct {
	// Changes is the set of changes applied (apply) or observed-as-drift (verify).
	Changes []Change
	// Detail is a short, non-sensitive summary.
	Detail string
}

// Executor reconciles a desired spec onto a runtime target. Every method is
// expected to be IDEMPOTENT: applying a spec already in effect yields a noop
// plan and changes nothing. The real adapter wraps Docker/K8s (writing through
// the same clients reads with, under least privilege) or an IaC backend.
type Executor interface {
	// Plan computes the diff between the desired spec and the current real state
	// on the target. It mutates nothing (dry-run).
	Plan(ctx context.Context, req ExecRequest) ([]Change, error)
	// Apply reconciles the target to the desired spec. It is idempotent.
	Apply(ctx context.Context, req ExecRequest) (ExecResult, error)
	// Verify reports the real state's drift from the desired spec (no mutation).
	Verify(ctx context.Context, req ExecRequest) (ExecResult, error)
	// Retire removes the deployment from the target. It is idempotent.
	Retire(ctx context.Context, req ExecRequest) (ExecResult, error)
}

// errNoExecutor is the fail-closed error every executor method returns when no
// runtime executor is wired. It is a deliberate, explicit failure — never a
// pretend success that would let the control plane believe it mutated infra.
type unwiredExecutor struct{}

func (unwiredExecutor) Plan(context.Context, ExecRequest) ([]Change, error) {
	return nil, errNoExecutor
}
func (unwiredExecutor) Apply(context.Context, ExecRequest) (ExecResult, error) {
	return ExecResult{}, errNoExecutor
}
func (unwiredExecutor) Verify(context.Context, ExecRequest) (ExecResult, error) {
	return ExecResult{}, errNoExecutor
}
func (unwiredExecutor) Retire(context.Context, ExecRequest) (ExecResult, error) {
	return ExecResult{}, errNoExecutor
}

// ----------------------------------------------------------------------------
// IdentityBinder — the per-agent-identity seam. At wiring time the module
// asserts/mints the agent's unique NHI identity through binding contract,
// so the deployment is BORN with attribution (closing the bridge needs). If
// binding is unavailable the wiring is marked DEGRADED — honest, never faked.
// ----------------------------------------------------------------------------

// BoundIdentity is the result of ensuring an agent's identity for a wiring.
type BoundIdentity struct {
	// IdentityRef is the NHI identity reference now bound to the agent (empty when
	// attribution is degraded).
	IdentityRef string
	// Firm is true when a single, unambiguous per-agent identity was bound; false
	// when binding was unavailable (attribution degraded) — never silently faked.
	Firm bool
	// Reason is a short, non-sensitive explanation surfaced on the wiring.
	Reason string
}

// IdentityBinder ensures an agent runs as its own NHI identity. The real adapter
// bridges to (POST /v1/m/governance/agents/{id}/identity), which enforces the
// type gate and emits the shared-identity finding; this module never sets
// Agent.IdentityID directly (that would re-implement bridge).
type IdentityBinder interface {
	// EnsureAgentIdentity binds (find-or-mint) the agent's per-agent identity for a
	// wiring and returns the bound reference, or a degraded result if unavailable.
	EnsureAgentIdentity(ctx context.Context, tenant model.TenantID, agentRef, identityRef string, mint bool) (BoundIdentity, error)
}

// unwiredBinder is the honest default: it never fakes attribution. It returns a
// degraded result so the wiring records that per-agent identity could not be
// asserted, rather than pretending the agent has a firm identity.
type unwiredBinder struct{}

func (unwiredBinder) EnsureAgentIdentity(_ context.Context, _ model.TenantID, _, identityRef string, _ bool) (BoundIdentity, error) {
	return BoundIdentity{IdentityRef: "", Firm: false, Reason: "no identity binder wired; per-agent attribution degraded"}, nil
}

// ----------------------------------------------------------------------------
// StopGate — the estate kill-switch pre-flight. Runs FIRST on apply and
// retire: while an emergency stop scopes the mutation (estate-wide, or the
// subject agent), no infrastructure mutation proceeds — not even an approved
// one. Expressed in this module's own terms; the composition root injects a
// governance-backed adapter.
// ----------------------------------------------------------------------------

// StopDims is the attribution the stop check scopes on (references only).
type StopDims struct {
	// AgentRef is the deployed agent subject (empty for a non-agent subject,
	// e.g. an mcp_server — an estate-wide stop still freezes it).
	AgentRef string
}

// StopDecision is the gate's verdict.
type StopDecision struct {
	Stopped bool
	// StopRef is the stop row's id (the operator-facing pointer to who/why).
	StopRef string
	// Scope is the matching stop's graduation: estate | agent.
	Scope string
}

// StopGate reports whether an active emergency stop freezes this mutation.
// DENY-CLOSED BY CONTRACT: callers treat a gate ERROR as stopped — an
// unreadable stop state must never mean "go".
type StopGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims StopDims) (StopDecision, error)
}

// allowStopGate is the unwired default (no stop state exists); the composition
// root always wires the real, in-process governance-backed gate.
type allowStopGate struct{}

func (allowStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	return StopDecision{}, nil
}
