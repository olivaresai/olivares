// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

// This file defines the integration SEAMS this module depends on but does not own,
// each expressed in the module's own terms so the module stays decoupled from its
// neighbors' packages (the deploy/governance convention). The composition root
// injects real adapters; until it exists (the honest Fase C caveat), each seam
// defaults to a SAFE, deny-closed behavior so an un-wired deployment can declare a
// schedule but can NEVER silently fire an autonomous agent.

// ----------------------------------------------------------------------------
// ApprovalGate — the human-in-the-loop seam (same shape as deploy's gate).
// ----------------------------------------------------------------------------

// GateStatus is the effective decision the gate reports. Every value except
// StatusApproved is a DENY.
type GateStatus string

const (
	// StatusApproved is the only value that authorizes a governed fire.
	StatusApproved GateStatus = "approved"
	// StatusPending means a HITL request is open but undecided — deny, keep waiting.
	StatusPending GateStatus = "pending"
	// StatusRejected means a human rejected the request — deny.
	StatusRejected GateStatus = "rejected"
	// StatusExpired means the approval lapsed before the fire — deny.
	StatusExpired GateStatus = "expired"
	// StatusNoGate means no approval gate is wired — deny (deny-by-default).
	StatusNoGate GateStatus = "no_gate"
	// StatusNotRequired marks an action that mutates no infrastructure (a pure
	// desired-state declaration), so no approval is consumed.
	StatusNotRequired GateStatus = "not_required"
)

// ApprovalRequest describes a privileged fire that needs governance. PlanHash
// binds the request to the EXACT schedule+cadence a human saw (anti-TOCTOU): a
// re-target or re-cadence changes the hash and voids a stale approval.
type ApprovalRequest struct {
	Tenant      model.TenantID
	Action      string // e.g. "orchestration.schedule.fire"
	SubjectKind string // "schedule"
	SubjectRef  string // the schedule id
	PlanHash    string
	RequestedBy string // audit-actor string of the asking principal (provenance)
}

// GateDecision is the gate's answer for one approval reference.
type GateDecision struct {
	ApprovalRef string
	Status      GateStatus
	PlanHash    string // the plan the approval was bound to, echoed back for confirmation
}

// Allowed reports whether this decision authorizes the fire. It is true ONLY for
// an explicit approval — every other status (including the zero value) is a deny.
func (d GateDecision) Allowed() bool { return d.Status == StatusApproved }

// ApprovalGate is the governance HITL seam. The two-phase fire REQUESTS an approval
// (phase 1) and CHECKS the effective decision (phase 2) before dispatching. The
// real adapter bridges to (POST /v1/m/governance/approvals + its decision
// trail); this module never decides — it asks and consumes the result.
// ApprovalCheck is the EXPECTED scope a phase-2 caller demands an approval have
// authorized (item 2, anti-authorization-substitution). The gate MUST
// verify the stored approval (or break-glass grant) authorized THIS exact
// action + subject + plan — a low-risk approval, or a break-glass grant for a
// different scope, whose subject merely encodes the target plan hash must NOT
// authorize this fire. Without this the plan-hash match alone is satisfiable by
// a substituted, out-of-scope approval.
type ApprovalCheck struct {
	Tenant      model.TenantID
	ApprovalRef string
	PlanHash    string
	Action      string // the action the caller is about to perform (must match the approval's)
	SubjectKind string // the subject kind (must match the approval's)
	SubjectRef  string // the subject ref (bound into the plan hash; passed for the scoped grant check)
}

type ApprovalGate interface {
	Request(ctx context.Context, req ApprovalRequest) (GateDecision, error)
	// Status reports the effective decision AND verifies the approval's scope
	// (action + subject) matches chk — a scope mismatch is a DENY, never an
	// approval echoing a matching plan hash for a different action.
	Status(ctx context.Context, chk ApprovalCheck) (GateDecision, error)
}

// denyGate is the deny-closed default: every fire is denied until a real gate is
// wired. Request returns a deterministic reference (so a fire_request can be
// recorded), but Status always denies — so the fire blocks. NOT a silent no-op;
// it is the safest behavior and Start() warns once.
type denyGate struct{}

func (denyGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: StatusNoGate, PlanHash: req.PlanHash}, nil
}

func (denyGate) Status(_ context.Context, chk ApprovalCheck) (GateDecision, error) {
	return GateDecision{ApprovalRef: chk.ApprovalRef, Status: StatusNoGate, PlanHash: chk.PlanHash}, nil
}

// ----------------------------------------------------------------------------
// Dispatcher — the deny-closed ACTUATION seam. Running an agent is NOT this
// module's job: the act of dispatching a scheduled run leaves through this port to
// a future adapter (deploy / the core runtime). The default fails closed: the
// control plane can declare and approve a fire, but cannot actuate one until a
// dispatcher is wired. The module NEVER spawns a process.
// ----------------------------------------------------------------------------

// FireRequest is what the dispatcher needs to run one scheduled subject. It is a
// minimal-data reference set — no command line, no payload, no secret.
type FireRequest struct {
	Tenant      model.TenantID
	SubjectKind string
	SubjectRef  string
	ScheduleRef string
	PlanHash    string
	// OperationID is the single-use identity of this governed effect. The
	// dispatcher MUST propagate it end to end so the receiver can dedup a
	// duplicate delivery by OperationID (e.g. A2A messageId = OperationID). An
	// ambiguous dispatch is never blindly re-emitted; reconciliation keys on it.
	OperationID string
}

// DispatchResult is the outcome of a real dispatch.
type DispatchResult struct {
	// Ref is the opaque external run/dispatch reference (e.g. a deploy operation id).
	Ref string
}

// Dispatcher actuates a scheduled fire on a runtime/deploy backend. The real
// adapter wraps (which owns the only plane that acts on customer infra) or the
// core runtime; this module only asks, after governance approves.
type Dispatcher interface {
	Fire(ctx context.Context, req FireRequest) (DispatchResult, error)
}

// errNoDispatcher is the fail-closed error the unwired dispatcher returns — a
// deliberate, explicit failure, never a pretend success that would let the control
// plane believe it ran an agent.
var errNoDispatcher = errors.New("orchestration: no dispatcher wired; fire declared, not actuated")

// ErrDispatchAmbiguous is the sentinel a Dispatcher returns when the effect MAY
// have taken place but the outcome could not be confirmed (hole c5): e.g.
// an A2A response-read error AFTER the message was transmitted, or a transport
// timeout past the write. The operation is settled as UNKNOWN — never re-emitted
// automatically (at-most-once past the ambiguous point) and never mislabeled as a
// definitive "failed" (which would falsely assert the effect did not happen).
var ErrDispatchAmbiguous = errors.New("orchestration: dispatch outcome ambiguous; may have actuated")

// unwiredDispatcher is the deny-closed default.
type unwiredDispatcher struct{}

func (unwiredDispatcher) Fire(context.Context, FireRequest) (DispatchResult, error) {
	return DispatchResult{}, errNoDispatcher
}

// ----------------------------------------------------------------------------
// BudgetGate — the FinOps (module XI) pre-flight admission seam (FIN-08). It is
// ORTHOGONAL to the ApprovalGate: a fire a human approved is still denied when an
// enforcing budget that scopes it is at its cap. That is the Denial-of-Wallet
// control (OWASP LLM10:2025 Unbounded Consumption) — an exhausted budget must STOP
// the spend, not merely annotate it. Expressed in this module's own terms; the
// composition root injects a finops-backed adapter (modules never import finops).
// ----------------------------------------------------------------------------

// Budget enforcement actions, mirroring the FinOps budget spec (alert never reaches
// this seam — alert-only is showback and never denies).
const (
	budgetActionThrottle = "throttle"
	budgetActionBlock    = "block"
)

// BudgetDims is the provider-neutral attribution of a prospective fire the budget
// check scopes on. MINIMAL DATA (docs/SECURITY-HARDENING.md): references only — never a payload,
// command line or secret. A fire knows the subject; a global/agent-scoped enforcing
// budget is what can cap it.
type BudgetDims struct {
	// AgentRef is the fired agent subject (empty for a non-agent subject, e.g. a
	// swarm — a global budget still scopes it).
	AgentRef string
	// RoutineRef is the Claude Code Routine (trigger) id that originated
	// the fire, if any. Enables per-routine enforcing budgets.
	RoutineRef string
}

// BudgetDecision is the gate's admission answer for a prospective fire.
type BudgetDecision struct {
	// Allowed is true when no enforcing budget caps this fire. It is the OPT-IN
	// default: budget enforcement applies ONLY when an action=throttle|block budget
	// that scopes the fire is at its limit.
	Allowed bool
	// Action is the most restrictive enforcing action when denied: throttle | block.
	Action string
	// BudgetRef is the capping budget's id (audit provenance). Reason is a short,
	// money-free explanation (docs/SECURITY-HARDENING.md: never a USD amount in user-facing text).
	BudgetRef string
	Reason    string
}

// BudgetGate is the FinOps pre-flight seam. Check is consulted AFTER governance
// approves and BEFORE the dispatcher actuates, so an exhausted budget denies an
// otherwise-approved fire.
type BudgetGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims BudgetDims) (BudgetDecision, error)
}

// allowBudgetGate is the OPT-IN default: with no FinOps gate wired, no budget ever
// denies a fire. This is deliberately NOT deny-closed like the approval gate — an
// absent enforcing budget is the NORMAL state, not a safety gap, and it mirrors
// finops.CheckBudget's fail-open contract (a FinOps gap must not take down
// actuation; the emitted finops_budget_cap finding remains the backstop). The
// composition root always wires the real, in-process finops-backed gate, so this
// default is only the honest behavior for an isolated module / a test.
type allowBudgetGate struct{}

func (allowBudgetGate) Check(context.Context, model.TenantID, BudgetDims) (BudgetDecision, error) {
	return BudgetDecision{Allowed: true}, nil
}

// ----------------------------------------------------------------------------
// StopGate — the estate kill-switch pre-flight. It is ORTHOGONAL to both
// the ApprovalGate and the BudgetGate and runs BEFORE them: while an emergency
// stop scopes this fire (estate-wide, or the fired agent), NOTHING proceeds —
// no new fire request queues and no already-approved fire dispatches.
// Expressed in this module's own terms; the composition root injects a
// governance-backed adapter (modules never import each other).
// ----------------------------------------------------------------------------

// StopDims is the attribution the stop check scopes on. MINIMAL DATA: a
// reference only.
type StopDims struct {
	// AgentRef is the fired agent subject (empty for a non-agent subject — an
	// estate-wide stop still freezes it).
	AgentRef string
}

// StopDecision is the gate's verdict.
type StopDecision struct {
	// Stopped is true when an active emergency stop scopes this fire.
	Stopped bool
	// StopRef is the stop row's id (the operator-facing pointer to who/why).
	StopRef string
	// Scope is the matching stop's graduation: estate | agent.
	Scope string
}

// StopGate reports whether an active emergency stop freezes this actuation.
// DENY-CLOSED BY CONTRACT: callers treat a gate ERROR as stopped — an
// unreadable stop state must never mean "go". This is the exact inverse of the
// budget gate's documented fail-open posture.
type StopGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims StopDims) (StopDecision, error)
}

// allowStopGate is the unwired default: no stop state exists, nothing is
// frozen. The composition root always wires the real, in-process
// governance-backed gate; this default is only the honest behavior for an
// isolated module / a test.
type allowStopGate struct{}

func (allowStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	return StopDecision{}, nil
}

// ----------------------------------------------------------------------------
// RoutinePolicyGate — the routine-governance seam. A schedule IS
// what calls a routine: the budget gate already passes this module's
// schedule id as the RoutineRef dimension (BudgetDims.RoutineRef above, and
// the routines-governance contract). The policy itself lives in
// modules/governance; this port states it in THIS module's terms and the
// composition root injects the governance-backed adapter.
//
// It is ORTHOGONAL to the other three seams and is the operator's standing
// answer to "what routines may exist at all, and how often may they run" —
// checked when a routine is DECLARED or ACTIVATED, and again when one FIRES
// (a policy authored or tightened after a routine was created must bite, or
// every pre-existing routine is grandfathered out of governance).
//
// DENY-CLOSED BY CONTRACT, like StopGate and unlike BudgetGate: routine policy
// is positive enforcement, so a gate ERROR denies. An unreadable policy must
// never mean "unconstrained".
// ----------------------------------------------------------------------------

// RoutineScope is the governance scope of one routine: the identity of the
// principal that DECLARED it, persisted on the schedule. It is deliberately not
// the identity of whoever is calling now — an admin (or a token) can patch or
// fire someone else's schedule, so resolving user/workspace policy from the
// live caller would let any such principal step outside the owner's policy.
type RoutineScope struct {
	// UserRef is the owning user's stable id ("" when the declaring principal
	// had no user identity).
	UserRef string
	// UserKnown distinguishes "this routine definitively has no owning user"
	// (declared by a token) from "this routine cannot answer for the user axis".
	// Only the second is indeterminate; treating the first as unknown refuses
	// every token-declared routine as soon as one user-scoped policy exists.
	UserKnown bool
	// WorkspaceRef is the owning workspace id ("" when unknown/unconfined).
	WorkspaceRef string
}

// RoutineActiveCap is one scope's cap on ACTIVE routine declarations. Each cap
// constrains a DIFFERENT population (a tenant cap of 100 and a user cap of 2
// are not comparable), so admission must count and check each one separately —
// never fold them into a single number and a single count.
type RoutineActiveCap struct {
	ScopeKind string // tenant | workspace | user
	ScopeRef  string // "" for tenant
	Max       int64  // always > 0 (a zero cap means "no cap" and is not emitted)
}

// RoutinePolicy is the composed, most-restrictive posture for one RoutineScope.
type RoutinePolicy struct {
	// InForce is true when at least one enabled policy matched.
	InForce bool
	// Indeterminate marks a resolution that could not be completed (an enabled
	// policy scopes an axis this routine cannot supply). The consumer DENIES:
	// treating it as "no policy matched" is precisely the silent bypass this
	// seam exists to close. IndeterminateAxis names the axis for the operator.
	Indeterminate     bool
	IndeterminateAxis string
	// MinIntervalSec is the cadence FLOOR in seconds (0 = none).
	MinIntervalSec int64
	// RequireApproval demands a HITL approval before a routine becomes active.
	RequireApproval bool
	// CronAllowed is the set of admissible cron patterns in canonical spelling.
	// CronAllowlistInForce distinguishes "no allowlist" from an authored EMPTY
	// one (which denies every cron).
	CronAllowed          []string
	CronAllowlistInForce bool
	// BlockedEnvs are environments a routine may not actuate in.
	BlockedEnvs []string
	// ActiveCaps is the per-scope vector of active-declaration caps.
	ActiveCaps []RoutineActiveCap
	// EffectiveUserRef / EffectiveWorkspaceRef are the axis values the policy
	// MATCH was made on, and DefaultWorkspaceRef the tenant default used to
	// normalise an absent workspace. An active-cap population MUST be selected
	// with these, never with a candidate row's raw column: matching on a derived
	// value while counting on a raw one lets a row be GOVERNED by a cap and be
	// INVISIBLE to its count, so the cap silently admits past its limit.
	EffectiveUserRef      string
	EffectiveWorkspaceRef string
	DefaultWorkspaceRef   string

	// PolicyRefs and Digest are evidence: which policies decided, and a stable
	// fingerprint of the composed posture. Never the policy body.
	PolicyRefs []string
	Digest     string
}

// RoutinePolicyGate resolves the effective routine policy for a scope.
type RoutinePolicyGate interface {
	Resolve(ctx context.Context, tenant model.TenantID, scope RoutineScope) (RoutinePolicy, error)
}

// openRoutinePolicyGate is the unwired default: no policy exists, nothing is
// constrained. The composition root always wires the real, in-process
// governance-backed gate; this default is the honest behavior for an isolated
// module or a test, and it is NOT a security hole — with no gate there is no
// policy store to read, so there is nothing to enforce.
type openRoutinePolicyGate struct{}

func (openRoutinePolicyGate) Resolve(context.Context, model.TenantID, RoutineScope) (RoutinePolicy, error) {
	return RoutinePolicy{}, nil
}

// ----------------------------------------------------------------------------
// TargetEnvironmentResolver — the AUTHORITATIVE environment of a routine's
// actuation target. The blocked_environments control needs to know
// which environment a routine really runs in, and the only truthful source is
// the operator dispatcher configuration the fire path actually selects: it maps
// (subject_kind, subject_ref) to a runtime target whose Environment is handed to
// the executor (cmd/olivares/orchdispatch.go fireRuntime).
//
// It is deliberately NOT a caller-declared field on the schedule. A declared
// environment="dev" is forgeable while the dispatcher still executes the same
// subject in prod — the policy would admit the declaration and the fire would
// actuate the blocked target. The adapter is therefore built from the SAME
// filtered snapshot and precedence as the dispatcher itself (the rule
// newDispatcherGeneration already follows), so what is evaluated is what Fire
// picks.
// ----------------------------------------------------------------------------

// TargetEnvironment is the resolved actuation context of one schedule subject.
type TargetEnvironment struct {
	// RouteFound reports that the dispatcher has an actuation route for this
	// subject at all.
	RouteFound bool
	// Environment is the environment that route actuates in. It is EMPTY for a
	// route that carries none (the A2A delegation route has no environment
	// dimension) — which is an absence, never an implicit "safe" environment.
	Environment string
}

// TargetEnvironmentResolver answers the environment question for a subject.
type TargetEnvironmentResolver interface {
	Resolve(ctx context.Context, subjectKind, subjectRef string) (TargetEnvironment, error)
}

// unwiredTargetEnvironment is the default: nothing is resolvable. It is only
// consulted when a blocked-environment list is IN FORCE, so an un-wired
// deployment with no such policy is unaffected; when one IS authored, an
// unresolvable target denies closed rather than actuating into an environment
// the plane cannot name.
type unwiredTargetEnvironment struct{}

func (unwiredTargetEnvironment) Resolve(context.Context, string, string) (TargetEnvironment, error) {
	return TargetEnvironment{}, nil
}
