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
// orchestration / voice / models BudgetGate ports by asking the FinOps module's
// admission (finops.Module.Reserve). Like orchdispatch.go /
// voicedispatch.go / internal/approvalbridge it lives in the composition root (cmd, AGPL)
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
// amounts of finops.Reservation, its estimate included, are deliberately dropped).
//
// FAIL CLOSED: a ledger that cannot be read denies. Admission's allow posture exists
// only as an explicit opt-in, and no in-process gate takes it. An exhausted budget that
// is DEFINITIVELY over its cap denies as before.

// engineReserveUnreachable is the unreachable-ledger posture EVERY in-process gate passes
// to finops Reserve, and it is deny without exception: Reserve HOLDS money, so a ledger it
// could not write has no headroom to hand out and no hold to commit or release later. It
// is spelled at each call site rather than left to the zero value, so the rule is visible
// where the request is built.
//
// It is deliberately NOT the session launch gate's availability posture
// (resolveAvailabilityPosture, sessiongov.go). That one answers a control the launch gate
// could not READ, and an operator may set it to fail-open; reading it here would let that
// environment variable make a WRITE fail-open, where admission would report a hold that
// does not exist and concurrent launches would over-admit against one cap. What the
// launch posture still decides is what the LAUNCH does with admission's refusal, a
// separate branch in sessionLaunchGate.Authorize.
const engineReserveUnreachable = finops.UnreachableDeny

// engineGateNoEstimate is the amount EVERY in-process YES/NO gate reserves, and it is zero
// because that is the truth about these seams, not a placeholder. A launch, a fire, a
// voice open, an evals judge, a model route and a durable MCP task all ask one question,
// may this proceed, and none of them learns what the effect cost: the cost arrives later,
// on the bus, attached to other requests. A caller that cannot learn the cost cannot
// commit it and has no reason to release, so it must not be handed a hold to settle.
//
// Admission answers that shape by holding nothing and issuing no hold, while it still
// evaluates every enforcing budget under the writer lock: the refusal is as firm as ever,
// and no ledger row is left behind for its expiry to retire. The one caller that does
// learn the cost is the inference proxy: it holds a real estimate
// (proxyAdmissionEstimate) and settles it in Finalize.
const engineGateNoEstimate = 0

// budgetChecker is the narrow slice of the FinOps module the gates depend on. Depending
// on the capability (not the concrete *finops.Module) keeps the adapters unit-testable.
type budgetChecker interface {
	CheckBudget(ctx context.Context, tenant model.TenantID, dims finops.SpendDims) (finops.BudgetCheck, error)
	CheckSpendLimit(ctx context.Context, tenant model.TenantID, actorRef string, groups []string) (finops.SpendLimitCheck, error)
	Reserve(ctx context.Context, tenant model.TenantID, req finops.AdmissionRequest) (finops.Reservation, error)
	Commit(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error
	Release(ctx context.Context, tenant model.TenantID, handle string) error
}

// engineGateAdmission is the admission request of an in-process YES/NO gate: a fresh key
// under the seam's prefix for every call, since no two calls of these seams are one
// effect, no amount to hold, and deny when the ledger cannot be read.
func engineGateAdmission(scope, keyPrefix string, dims finops.SpendDims) finops.AdmissionRequest {
	return finops.AdmissionRequest{
		Scope:            scope,
		Dims:             dims,
		EstimateMicroUSD: engineGateNoEstimate,
		IdempotencyKey:   keyPrefix + "/" + model.NewID().String(),
		Unreachable:      engineReserveUnreachable,
	}
}

var _ budgetChecker = (*finops.Module)(nil)

// orchBudgetGate adapts FinOps admission to the orchestration fire seam.
type orchBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ orchestration.BudgetGate = orchBudgetGate{}

func (g orchBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims orchestration.BudgetDims) (orchestration.BudgetDecision, error) {
	res, err := g.fin.Reserve(ctx, tenant, engineGateAdmission(finops.AdmissionScopeScheduledJob, "scheduled_job",
		finops.SpendDims{AgentRef: dims.AgentRef, RoutineRef: dims.RoutineRef}))
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: orchestration admission failed; denying fire (fail-closed)", "err", err)
		}
		return orchestration.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return orchestration.BudgetDecision{
		Allowed: res.Allowed, Action: res.Action, BudgetRef: res.BudgetID, Reason: res.Reason,
	}, nil
}

// voiceBudgetGate adapts FinOps admission to the voice open seam. A voice open
// knows its model/provider/agent/session, so model- and provider-scoped enforcing
// budgets (and global) can cap it.
type voiceBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ voice.BudgetGate = voiceBudgetGate{}

func (g voiceBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims voice.BudgetDims) (voice.BudgetDecision, error) {
	res, err := g.fin.Reserve(ctx, tenant, engineGateAdmission(finops.AdmissionScopeSessionLaunch, "voice_open", finops.SpendDims{
		AgentRef: dims.AgentRef, SessionRef: dims.SessionRef, ModelRef: dims.ModelRef, ProviderRef: dims.ProviderRef,
	}))
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: voice admission failed; denying open (fail-closed)", "err", err)
		}
		return voice.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return voice.BudgetDecision{
		Allowed: res.Allowed, Action: res.Action, BudgetRef: res.BudgetID, Reason: res.Reason,
	}, nil
}

// evalsBudgetGate adapts FinOps admission to the evals regression-gate seam
// (a budget over the CI's own judge spend). The judge model is
// the spend dimension. Unlike the other three it is LATE-BOUND (bind) because the
// evals module is constructed before FinOps in wire.go; an unbound gate allows, since
// there is no ledger to ask yet. A ledger that cannot be read denies (fail-closed, as
// in the sibling gates), and a definitive block/throttle stops the gate from spending.
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
	res, err := g.fin.Reserve(ctx, tenant, engineGateAdmission(finops.AdmissionScopeScheduledJob, "evals_judge",
		finops.SpendDims{ModelRef: dims.JudgeModelRef}))
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: evals admission failed; denying (fail-closed)", "err", err)
		}
		return evals.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return evals.BudgetDecision{Allowed: res.Allowed, Action: res.Action, Reason: res.Reason}, nil
}

// modelsBudgetGate adapts FinOps admission to the model-router resolve seam.
type modelsBudgetGate struct {
	fin budgetChecker
	log *slog.Logger
}

var _ models.BudgetGate = modelsBudgetGate{}

func (g modelsBudgetGate) Check(ctx context.Context, tenant model.TenantID, dims models.BudgetDims) (models.BudgetDecision, error) {
	res, err := g.fin.Reserve(ctx, tenant, engineGateAdmission(finops.AdmissionScopeModelGateway, "model_route", finops.SpendDims{
		// SessionRef lets finops resolve a firm IDENTITY budget for the routed
		// spend — the model-access budget tie-in. Admission resolves the identity
		// from the session itself; an empty ref leaves the check provider/model-scoped.
		ProviderRef: dims.ProviderRef, ModelRef: dims.ModelRef, SessionRef: dims.SessionRef,
	}))
	if err != nil {
		if g.log != nil {
			g.log.Error("budget-gate: models admission failed; denying route (fail-closed)", "err", err)
		}
		return models.BudgetDecision{Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable}, nil
	}
	return models.BudgetDecision{
		Allowed: res.Allowed, Action: res.Action, BudgetRef: res.BudgetID, Reason: res.Reason,
	}, nil
}
