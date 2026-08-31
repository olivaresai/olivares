// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// budget.go defines the FinOps (module XI) pre-flight admission seam this module
// consults BEFORE returning a routing decision (FIN-08). A routing resolve is a pure
// selection — it performs no inference — but it is the precursor to spend: it tells
// the gateway WHICH governed model to call. So when an enforcing budget that
// scopes the selected model is at its cap, the router must DENY the resolution
// (Denial-of-Wallet / OWASP LLM10:2025 Unbounded Consumption): an exhausted budget
// must STOP the spend, not merely annotate it.
//
// The seam is expressed in this module's own terms; the composition root injects a
// finops-backed adapter (this module never imports finops — the seam convention).

// Budget enforcement actions, mirroring the FinOps budget spec (alert never reaches
// this seam — alert-only is showback and never denies).
const (
	budgetActionThrottle = "throttle"
	budgetActionBlock    = "block"
)

// BudgetDims is the provider-neutral attribution a routing resolve scopes the budget
// check on. MINIMAL DATA (docs/SECURITY-HARDENING.md): references only. The router knows the model and
// provider it would route to, so global, provider- and model-scoped enforcing budgets
// can cap it. Finer scopes (workspace/api_key/team) are enforced at the actuation
// seams (fire/open) and the gateway, which know those dimensions.
type BudgetDims struct {
	ProviderRef string
	ModelRef    string
	// SessionRef is the acting session's external id, when known (the /execute path
	// carries it; /resolve does not). It lets an IDENTITY-scoped Claude budget
	// resolve its firm identity and cap the routed spend at selection too — the budget
	// tie-in of model-access governance, defense-in-depth with the in-band budget
	// check runs via CountTokens. Empty leaves the check provider/model-scoped only.
	SessionRef string
}

// BudgetDecision is the gate's admission answer for a prospective routing decision.
type BudgetDecision struct {
	// Allowed is true when no enforcing budget caps the selected model. It is the
	// OPT-IN default: budget enforcement applies ONLY when an action=throttle|block
	// budget that scopes the selection is at its limit.
	Allowed bool
	// Action is the most restrictive enforcing action when denied: throttle | block.
	Action string
	// BudgetRef is the capping budget's id (provenance). Reason is a short, money-free
	// explanation (docs/SECURITY-HARDENING.md: never a USD amount in user-facing text).
	BudgetRef string
	Reason    string
}

// BudgetGate is the FinOps pre-flight seam. Check is consulted AFTER a routing policy
// resolves a primary target and BEFORE that decision is returned, so a capped budget
// denies the selection rather than letting the gateway spend against it.
type BudgetGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims BudgetDims) (BudgetDecision, error)
}

// allowBudgetGate is the OPT-IN default: with no FinOps gate wired, no budget ever
// denies a routing resolve. Deliberately NOT deny-closed — an absent enforcing budget
// is the NORMAL state, not a safety gap — and it mirrors finops.CheckBudget's
// fail-open contract (a FinOps gap must not take down routing; the emitted
// finops_budget_cap finding remains the backstop). The composition root always wires
// the real, in-process finops-backed gate, so this default is only the honest behavior
// for an isolated module / a test.
type allowBudgetGate struct{}

func (allowBudgetGate) Check(context.Context, model.TenantID, BudgetDims) (BudgetDecision, error) {
	return BudgetDecision{Allowed: true}, nil
}

// ----------------------------------------------------------------------------
// StopGate — the estate kill-switch pre-flight on the EXECUTE path (the
// spend). Resolve stays readable during a stop (pure selection, no spend);
// execution does not. Models has no agent dimension, so only the estate-wide
// graduation applies here. The composition root injects a governance-backed
// adapter.
// ----------------------------------------------------------------------------

// StopDecision is the gate's verdict.
type StopDecision struct {
	Stopped bool
	// StopRef is the stop row's id (the operator-facing pointer to who/why).
	StopRef string
}

// StopGate reports whether an active estate-wide emergency stop freezes routed
// execution. DENY-CLOSED BY CONTRACT: callers treat a gate ERROR as stopped —
// an unreadable stop state must never mean "go" (the inverse of the budget
// gate's fail-open posture).
type StopGate interface {
	Check(ctx context.Context, tenant model.TenantID) (StopDecision, error)
}

// allowStopGate is the unwired default (no stop state exists); the composition
// root always wires the real, in-process governance-backed gate.
type allowStopGate struct{}

func (allowStopGate) Check(context.Context, model.TenantID) (StopDecision, error) {
	return StopDecision{}, nil
}
