// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// This file defines the integration SEAMS this module depends on but does not own.
// The composition root injects real adapters; until then each defaults to a SAFE,
// deny-closed behavior so an un-wired deployment can declare a voice policy but can
// NEVER silently open a voice/realtime session.

// ----------------------------------------------------------------------------
// ApprovalGate — the human-in-the-loop seam (same shape as deploy's gate).
// ----------------------------------------------------------------------------

// GateStatus is the effective decision the gate reports. Every value except
// StatusApproved is a DENY.
type GateStatus string

const (
	StatusApproved    GateStatus = "approved"
	StatusPending     GateStatus = "pending"
	StatusRejected    GateStatus = "rejected"
	StatusExpired     GateStatus = "expired"
	StatusNoGate      GateStatus = "no_gate"
	StatusNotRequired GateStatus = "not_required"
)

// ApprovalRequest describes a privileged voice-session open that needs governance.
// PlanHash binds the request to the EXACT (session, agent, model, provider, policy)
// tuple a human saw, so an approval cannot be silently upgraded to a stronger model
// (anti-TOCTOU).
type ApprovalRequest struct {
	Tenant      model.TenantID
	Action      string // "voice.session.open"
	SubjectKind string // "agent"
	SubjectRef  string // the agent ref
	PlanHash    string
	RequestedBy string
}

// GateDecision is the gate's answer for one approval reference.
type GateDecision struct {
	ApprovalRef string
	Status      GateStatus
	PlanHash    string
}

// Allowed reports whether this decision authorizes the open. True ONLY for approved.
func (d GateDecision) Allowed() bool { return d.Status == StatusApproved }

// ApprovalGate is the governance HITL seam for opening a voice session.
type ApprovalGate interface {
	Request(ctx context.Context, req ApprovalRequest) (GateDecision, error)
	Status(ctx context.Context, tenant model.TenantID, approvalRef, planHash string) (GateDecision, error)
}

// denyGate is the deny-closed default: every open is denied until a real gate is
// wired. NOT a silent no-op; Start() warns once.
type denyGate struct{}

func (denyGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: StatusNoGate, PlanHash: req.PlanHash}, nil
}

func (denyGate) Status(_ context.Context, _ model.TenantID, approvalRef, planHash string) (GateDecision, error) {
	return GateDecision{ApprovalRef: approvalRef, Status: StatusNoGate, PlanHash: planHash}, nil
}

// ----------------------------------------------------------------------------
// Dispatcher — the deny-closed ACTUATION seam. Calling a provider Realtime API
// is NOT this module's job: an approved open leaves through this port to a future
// adapter (model-provider / the core runtime). The default fails closed: the
// control plane can declare and approve an open, but cannot actuate one until a
// dispatcher is wired. The module NEVER calls a provider.
// ----------------------------------------------------------------------------

// OpenRequest is what the dispatcher needs to open one voice session. It is a
// minimal-data reference set — no audio, no transcript, no secret.
type OpenRequest struct {
	Tenant      model.TenantID
	SessionRef  string
	AgentRef    string
	ModelRef    string
	ProviderRef string
	PlanHash    string
}

// OpenResult is the outcome of a real open.
type OpenResult struct {
	// Ref is the opaque external session/dispatch reference from the provider.
	Ref string
}

// Dispatcher actuates an approved open on a provider Realtime backend.
type Dispatcher interface {
	Open(ctx context.Context, req OpenRequest) (OpenResult, error)
}

// errNoDispatcher is the fail-closed error the unwired dispatcher returns.
var errNoDispatcher = errors.New("voice: no dispatcher wired; open declared, not actuated")

// unwiredDispatcher is the deny-closed default.
type unwiredDispatcher struct{}

func (unwiredDispatcher) Open(context.Context, OpenRequest) (OpenResult, error) {
	return OpenResult{}, errNoDispatcher
}

// ----------------------------------------------------------------------------
// BudgetGate — the FinOps (module XI) pre-flight admission seam (FIN-08). It is
// ORTHOGONAL to the ApprovalGate: a voice-session open a human approved is still
// denied when an enforcing budget that scopes it is at its cap. That is the
// Denial-of-Wallet control (OWASP LLM10:2025 Unbounded Consumption) — an exhausted
// budget must STOP the spend, not merely annotate it. Expressed in this module's own
// terms; the composition root injects a finops-backed adapter (modules never import
// finops).
// ----------------------------------------------------------------------------

// Budget enforcement actions, mirroring the FinOps budget spec (alert never reaches
// this seam — alert-only is showback and never denies).
const (
	budgetActionThrottle = "throttle"
	budgetActionBlock    = "block"
)

// BudgetDims is the provider-neutral attribution of a prospective open the budget
// check scopes on. MINIMAL DATA (docs/SECURITY-HARDENING.md): references only — never audio, a
// transcript or a secret. A voice open knows the agent, model and provider it would
// run on, so model/provider/agent-scoped (and global) enforcing budgets can cap it.
type BudgetDims struct {
	AgentRef    string
	SessionRef  string
	ModelRef    string
	ProviderRef string
}

// BudgetDecision is the gate's admission answer for a prospective open.
type BudgetDecision struct {
	// Allowed is true when no enforcing budget caps this open. It is the OPT-IN
	// default: budget enforcement applies ONLY when an action=throttle|block budget
	// that scopes the open is at its limit.
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
// otherwise-approved open.
type BudgetGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims BudgetDims) (BudgetDecision, error)
}

// allowBudgetGate is the OPT-IN default: with no FinOps gate wired, no budget ever
// denies an open. This is deliberately NOT deny-closed like the approval gate — an
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
// StopGate — the estate kill-switch pre-flight. Runs FIRST in handleOpen:
// while an emergency stop scopes this open (estate-wide, or the agent), nothing
// proceeds — no new approval request queues and no approved open dispatches.
// Expressed in this module's own terms; the composition root injects a
// governance-backed adapter.
// ----------------------------------------------------------------------------

// StopDims is the attribution the stop check scopes on (a reference only).
type StopDims struct {
	// AgentRef is the agent the session would embody.
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

// StopGate reports whether an active emergency stop freezes this actuation.
// DENY-CLOSED BY CONTRACT: callers treat a gate ERROR as stopped — an
// unreadable stop state must never mean "go" (the inverse of the budget gate).
type StopGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims StopDims) (StopDecision, error)
}

// allowStopGate is the unwired default (no stop state exists); the composition
// root always wires the real, in-process governance-backed gate.
type allowStopGate struct{}

func (allowStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	return StopDecision{}, nil
}

// ----------------------------------------------------------------------------
// Realtime SIP call plane — OpenAI Realtime call control and sideband seams.
// ----------------------------------------------------------------------------

// CallAccept is the governed session object subset needed to accept one SIP call.
// It carries only policy-authored strings: never SIP headers, audio, transcript
// text, or provider credentials.
type CallAccept struct {
	Model        string
	Instructions string
}

// CallController actuates OpenAI Realtime SIP call control. The module owns this
// port and the composition root adapts it to the connector. The default refuses
// every operation: an unwired call plane cannot silently answer calls.
type CallController interface {
	Accept(ctx context.Context, callID string, cfg CallAccept) error
	Reject(ctx context.Context, callID string, statusCode int) error
	Hangup(ctx context.Context, callID string) error
}

// errNoCallController is the fail-closed error returned by the unwired controller.
var errNoCallController = errors.New("voice: no call controller wired; realtime call refused")

// unwiredCallController is the deny-closed default.
type unwiredCallController struct{}

func (unwiredCallController) Accept(context.Context, string, CallAccept) error {
	return errNoCallController
}

func (unwiredCallController) Reject(context.Context, string, int) error {
	return errNoCallController
}

func (unwiredCallController) Hangup(context.Context, string) error {
	return errNoCallController
}

// SidebandAttacher attaches to a live call's server-side sideband stream.
type SidebandAttacher func(ctx context.Context, callID string) (CallSideband, error)

// CallSideband is the minimal websocket-like channel the observer consumes.
type CallSideband interface {
	ReadMessage(ctx context.Context) ([]byte, error)
	WriteText(ctx context.Context, p []byte) error
	Close() error
}

// SensitivityHit is one DLP classifier result over transcript text. It carries
// only class labels, rules, counts and severity — never the matched text.
type SensitivityHit struct {
	Class    string `json:"class"`
	Rule     string `json:"rule,omitempty"`
	Count    int    `json:"count,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// TranscriptClassifier classifies transcript text in memory. Production wires the
// deterministic security catalog; nil means transcript events are unclassified and
// surfaced as a finding.
type TranscriptClassifier interface {
	Classify(text string) ([]SensitivityHit, error)
}

// TranscriptClassifierFunc adapts a function to TranscriptClassifier.
type TranscriptClassifierFunc func(text string) ([]SensitivityHit, error)

// Classify calls the wrapped function.
func (f TranscriptClassifierFunc) Classify(text string) ([]SensitivityHit, error) {
	return f(text)
}

// CallConfig is the operator-supplied attribution and lifecycle config for the
// inbound SIP call plane. Tenant is required before the webhook can do useful
// policy/store work; WorkspaceRef attributes runtime cost to the OpenAI project.
type CallConfig struct {
	Tenant            model.TenantID
	WorkspaceRef      string
	MaxObservers      int
	StopSweepInterval time.Duration
}
