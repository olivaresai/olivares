// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/voice"
)

// budgetgate.go is the FinOps↔actuation seam adapter (FIN-08): it implements the
// orchestration / voice / models BudgetGate ports by consulting the FinOps module's
// pre-flight admission decision (finops.Module.CheckBudget). Like orchdispatch.go /
// voicedispatch.go / approvalbridge.go it lives in the composition root (cmd, AGPL)
// because it bridges three AGPL module ports to a fourth AGPL module — which none of
// them may import directly (the in-process seam convention, modules/*/ports.go).
//
// It turns FIN-08 from finding-only into REAL enforcement: an enforcing budget
// (action=throttle|block) at its cap now DENIES the fire/open/route instead of merely
// emitting the finops_budget_cap finding. Unlike the approval gate / dispatcher it
// needs NO operator config — FinOps is in-process — so it is ALWAYS wired.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): only provider-neutral references cross the seam (the
// module BudgetDims → finops.SpendDims), and only the budget id + action + a money-free
// reason come back (NEVER a USD amount — feedback_no_dollar_amounts_users; the
// SpendMicroUSD/LimitMicroUSD of finops.BudgetCheck are deliberately dropped).
//
// FAIL OPEN (per finops.CheckBudget's own documented contract): a FinOps read error
// never denies actuation — the adapter logs it and allows. The finops_budget_cap
// finding remains the backstop. An exhausted budget that is DEFINITIVELY over its cap
// is what denies (deny-closed enforcement); an outage does not.

// budgetChecker is the narrow slice of the FinOps module the gates depend on. Depending
// on the capability (not the concrete *finops.Module) keeps the adapters unit-testable.
type budgetChecker interface {
	CheckBudget(ctx context.Context, tenant model.TenantID, dims finops.SpendDims) (finops.BudgetCheck, error)
	CheckSpendLimit(ctx context.Context, tenant model.TenantID, actorRef string, groups []string) (finops.SpendLimitCheck, error)
}

var _ budgetChecker = (*finops.Module)(nil)

// orchBudgetGate adapts the FinOps pre-flight to the orchestration fire seam.
type orchBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ orchestration.BudgetGate = orchBudgetGate{}

func (g orchBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims orchestration.BudgetDims) (orchestration.BudgetDecision, error) {
	chk, err := g.fin.CheckBudget(ctx, tenant, finops.SpendDims{AgentRef: dims.AgentRef, RoutineRef: dims.RoutineRef})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: orchestration check failed; allowing fire (fail-open)", "err", err)
		}
		return orchestration.BudgetDecision{Allowed: true}, nil
	}
	return orchestration.BudgetDecision{
		Allowed: chk.Allowed, Action: chk.Action, BudgetRef: chk.BudgetID, Reason: chk.Reason,
	}, nil
}

// voiceBudgetGate adapts the FinOps pre-flight to the voice open seam. A voice open
// knows its model/provider/agent/session, so model- and provider-scoped enforcing
// budgets (and global) can cap it.
type voiceBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ voice.BudgetGate = voiceBudgetGate{}

func (g voiceBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims voice.BudgetDims) (voice.BudgetDecision, error) {
	chk, err := g.fin.CheckBudget(ctx, tenant, finops.SpendDims{
		AgentRef: dims.AgentRef, SessionRef: dims.SessionRef, ModelRef: dims.ModelRef, ProviderRef: dims.ProviderRef,
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: voice check failed; allowing open (fail-open)", "err", err)
		}
		return voice.BudgetDecision{Allowed: true}, nil
	}
	return voice.BudgetDecision{
		Allowed: chk.Allowed, Action: chk.Action, BudgetRef: chk.BudgetID, Reason: chk.Reason,
	}, nil
}

// evalsBudgetGate adapts the FinOps pre-flight to the evals regression-gate seam
// (a budget over the CI's own judge spend). The judge model is
// the spend dimension. Unlike the other three it is LATE-BOUND (bind) because the
// evals module is constructed before FinOps in wire.go; an unbound or erroring
// FinOps read allows (fail-open — same posture as the sibling gates), while a
// definitive block/throttle stops the gate from spending.
type evalsBudgetGate struct {
	fin budgetChecker // nil until bind(); nil allows
	log *slog.Logger
}

var _ evals.BudgetGate = (*evalsBudgetGate)(nil)

func (g *evalsBudgetGate) bind(fin budgetChecker) { g.fin = fin }

func (g *evalsBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims evals.BudgetDims) (evals.BudgetDecision, error) {
	if g.fin == nil {
		return evals.BudgetDecision{Allowed: true}, nil
	}
	chk, err := g.fin.CheckBudget(ctx, tenant, finops.SpendDims{ModelRef: dims.JudgeModelRef})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: evals gate check failed; allowing (fail-open)", "err", err)
		}
		return evals.BudgetDecision{Allowed: true}, nil
	}
	return evals.BudgetDecision{Allowed: chk.Allowed, Action: chk.Action, Reason: chk.Reason}, nil
}

// modelsBudgetGate adapts the FinOps pre-flight to the model-router resolve seam.
type modelsBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ models.BudgetGate = modelsBudgetGate{}

func (g modelsBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims models.BudgetDims) (models.BudgetDecision, error) {
	chk, err := g.fin.CheckBudget(ctx, tenant, finops.SpendDims{
		// SessionRef lets finops resolve a firm IDENTITY budget for the routed
		// spend — the model-access budget tie-in. CheckBudget resolves the identity
		// from the session itself; an empty ref leaves the check provider/model-scoped.
		ProviderRef: dims.ProviderRef, ModelRef: dims.ModelRef, SessionRef: dims.SessionRef,
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: models check failed; allowing route (fail-open)", "err", err)
		}
		return models.BudgetDecision{Allowed: true}, nil
	}
	return models.BudgetDecision{
		Allowed: chk.Allowed, Action: chk.Action, BudgetRef: chk.BudgetID, Reason: chk.Reason,
	}, nil
}
