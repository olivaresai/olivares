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
// FAIL CLOSED by default: an unreachable budget store denies. The historical
// fail-open path remains only when AdmissionRequest.Unreachable is explicitly
// allow. An exhausted budget that is DEFINITIVELY over its cap still denies.

// budgetChecker is the narrow slice of the FinOps module the gates depend on. Depending
// on the capability (not the concrete *finops.Module) keeps the adapters unit-testable.
type budgetChecker interface {
	CheckBudget(ctx context.Context, tenant model.TenantID, dims finops.SpendDims) (finops.BudgetCheck, error)
	CheckSpendLimit(ctx context.Context, tenant model.TenantID, actorRef string, groups []string) (finops.SpendLimitCheck, error)
	Reserve(ctx context.Context, tenant model.TenantID, req finops.AdmissionRequest) (finops.Reservation, error)
	Commit(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error
	Release(ctx context.Context, tenant model.TenantID, handle string) error
}

var _ budgetChecker = (*finops.Module)(nil)

// orchBudgetGate adapts the FinOps pre-flight to the orchestration fire seam.
type orchBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ orchestration.BudgetGate = orchBudgetGate{}

func (g orchBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims orchestration.BudgetDims) (orchestration.BudgetDecision, error) {
	res, err := g.fin.Reserve(ctx, tenant, finops.AdmissionRequest{
		Scope:          finops.AdmissionScopeScheduledJob,
		Dims:           finops.SpendDims{AgentRef: dims.AgentRef, RoutineRef: dims.RoutineRef},
		IdempotencyKey: "scheduled_job/" + model.NewID().String(),
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: orchestration reserve failed; denying fire (fail-closed)", "err", err)
		}
		return orchestration.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return orchestration.BudgetDecision{
		Allowed: res.Allowed, Action: res.Action, BudgetRef: res.BudgetID, Reason: res.Reason,
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
	res, err := g.fin.Reserve(ctx, tenant, finops.AdmissionRequest{
		Scope: finops.AdmissionScopeSessionLaunch,
		Dims: finops.SpendDims{
			AgentRef: dims.AgentRef, SessionRef: dims.SessionRef, ModelRef: dims.ModelRef, ProviderRef: dims.ProviderRef,
		},
		IdempotencyKey: "voice_open/" + model.NewID().String(),
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: voice reserve failed; denying open (fail-closed)", "err", err)
		}
		return voice.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return voice.BudgetDecision{
		Allowed: res.Allowed, Action: res.Action, BudgetRef: res.BudgetID, Reason: res.Reason,
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
	res, err := g.fin.Reserve(ctx, tenant, finops.AdmissionRequest{
		Scope:          finops.AdmissionScopeScheduledJob,
		Dims:           finops.SpendDims{ModelRef: dims.JudgeModelRef},
		IdempotencyKey: "evals_judge/" + model.NewID().String(),
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: evals reserve failed; denying (fail-closed)", "err", err)
		}
		return evals.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return evals.BudgetDecision{Allowed: res.Allowed, Action: res.Action, Reason: res.Reason}, nil
}

// modelsBudgetGate adapts the FinOps pre-flight to the model-router resolve seam.
type modelsBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ models.BudgetGate = modelsBudgetGate{}

func (g modelsBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims models.BudgetDims) (models.BudgetDecision, error) {
	res, err := g.fin.Reserve(ctx, tenant, finops.AdmissionRequest{
		Scope: finops.AdmissionScopeModelGateway,
		Dims: finops.SpendDims{
			// SessionRef lets finops resolve a firm IDENTITY budget for the routed
			// spend — the model-access budget tie-in.
			ProviderRef: dims.ProviderRef, ModelRef: dims.ModelRef, SessionRef: dims.SessionRef,
		},
		IdempotencyKey: "model_route/" + model.NewID().String(),
	})
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: models reserve failed; denying route (fail-closed)", "err", err)
		}
		return models.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return models.BudgetDecision{
		Allowed: res.Allowed, Action: res.Action, BudgetRef: res.BudgetID, Reason: res.Reason,
	}, nil
}
