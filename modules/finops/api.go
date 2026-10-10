// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The module's permissions, granted to the built-in roles by verb tier. Spend
// analytics and budget reads are read-tier; managing budgets is write-tier.
const (
	permSpendRead   auth.Permission = "finops:spend:read"
	permBudgetRead  auth.Permission = "finops:budget:read"
	permBudgetWrite auth.Permission = "finops:budget:write"
	// PermBudgetAdmin gates administering spend limits, the caps a budget places on a
	// user, team or organization. It is admin-tier (granted to admin and owner by verb
	// tier, never to editor) and is declared so a scoped grant can carry it. The apps
	// gateway's spend-limit routes live outside the module router and ask for it through
	// the admission seam.
	PermBudgetAdmin auth.Permission = "finops:budget:admin"
	// permCostWrite gates the HTTP cost-ingest route. It is a 3-segment module
	// permission on the write verb tier, so the engine's role grants deny it to
	// viewer and grant it to editor/admin/owner — deny-closed by default, no engine
	// change (mirrors finops:budget:write).
	permCostWrite auth.Permission = "finops:cost:write"
	// permSeatsWrite gates the seat-snapshot ingest; the utilization READ
	// rides the spend-read tier (it is a spend-adjacent analytics view).
	permSeatsWrite auth.Permission = "finops:seats:write"
	// permOutcomeWrite gates the outcome ingest — the value-attribution bridge.
	// A 3-segment write-tier permission (deny-closed for viewer, granted to editor/
	// admin/owner), mirroring finops:cost:write / finops:seats:write. The value READS
	// ride the spend-read tier (spend-adjacent analytics).
	permOutcomeWrite auth.Permission = "finops:outcomes:write"
)

// APINamespace returns the module's namespace; it roots routes at /v1/m/finops/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permSpendRead, permBudgetRead, permBudgetWrite, PermBudgetAdmin, permCostWrite, permSeatsWrite, permOutcomeWrite}
}

// APIRoutes mounts the module's routes.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Spend analytics, forecasting and optimization.
	reg.Handle("GET", "/spend", permSpendRead, m.handleSpend)
	reg.Handle("GET", "/spend/summary", permSpendRead, m.handleSummary)
	reg.Handle("GET", "/spend/trend", permSpendRead, m.handleTrend)
	reg.Handle("GET", "/spend/reconciliation", permSpendRead, m.handleReconciliation)
	reg.Handle("GET", "/spend/export", permSpendRead, m.handleExport)
	reg.Handle("GET", "/spend/allocation", permSpendRead, m.handleAllocation)
	reg.Handle("GET", "/forecast", permSpendRead, m.handleForecast)
	reg.Handle("GET", "/recommendations", permSpendRead, m.handleRecommendations)
	reg.Handle("GET", "/analytics/team-summary", permSpendRead, m.handleTeamSummary)
	reg.Handle("GET", "/spend/unified", permSpendRead, m.handleUnified)

	// Cost ingest: push a CostSample (Anthropic cost_report / live runtime cost)
	// over HTTP through the same ledger path as the bus. Deny-closed write perm.
	reg.Handle("POST", "/cost", permCostWrite, m.handleIngestCost)

	// Seat denominators + per-seat utilization: the assigned/premium seat
	// snapshot ingest (deny-closed write perm) and the active-vs-assigned view.
	reg.Handle("POST", "/seats", permSeatsWrite, m.handleIngestSeats)
	reg.Handle("GET", "/seats/utilization", permSpendRead, m.handleSeatUtilization)

	// Value attribution: the outcome ingest bridge (deny-closed write perm) and
	// the CFO panels — cost-per-outcome by subject, plus the spend-vs-outcome summary
	// with the cancellation-risk signal (burn without successful outcomes).
	reg.Handle("POST", "/outcomes", permOutcomeWrite, m.handleIngestOutcome)
	reg.Handle("GET", "/outcomes", permSpendRead, m.handleListOutcomes)
	reg.Handle("GET", "/value", permSpendRead, m.handleValue)
	reg.Handle("GET", "/value/summary", permSpendRead, m.handleValueSummary)

	// cost center management.
	reg.Handle("GET", "/cost-centers", permSpendRead, m.handleListCostCenters)
	reg.Handle("POST", "/cost-centers", permBudgetWrite, m.handleCreateCostCenter)
	reg.Handle("GET", "/cost-centers/{id}", permSpendRead, m.handleGetCostCenter)
	reg.Handle("PUT", "/cost-centers/{id}", permBudgetWrite, m.handleUpdateCostCenter)
	reg.Handle("DELETE", "/cost-centers/{id}", permBudgetWrite, m.handleDeleteCostCenter)
	reg.Handle("GET", "/cost-centers/{id}/mappings", permSpendRead, m.handleListCostCenterMappings)
	reg.Handle("POST", "/cost-centers/{id}/mappings", permBudgetWrite, m.handleCreateCostCenterMapping)
	reg.Handle("DELETE", "/cost-centers/{id}/mappings/{mid}", permBudgetWrite, m.handleDeleteCostCenterMapping)

	// model rate catalog.
	reg.Handle("GET", "/model-rates", permSpendRead, m.handleListModelRates)
	reg.Handle("POST", "/model-rates", permBudgetWrite, m.handleCreateModelRate)
	reg.Handle("GET", "/model-rates/{id}", permSpendRead, m.handleGetModelRate)
	reg.Handle("PUT", "/model-rates/{id}", permBudgetWrite, m.handleUpdateModelRate)
	reg.Handle("DELETE", "/model-rates/{id}", permBudgetWrite, m.handleDeleteModelRate)

	// model cost comparison.
	reg.Handle("GET", "/comparison", permSpendRead, m.handleComparison)

	// chargeback statements.
	reg.Handle("POST", "/statements/generate", permBudgetWrite, m.handleGenerateStatements)
	reg.Handle("GET", "/statements", permSpendRead, m.handleListStatements)
	reg.Handle("GET", "/statements/{id}", permSpendRead, m.handleGetStatement)
	reg.Handle("GET", "/statements/{id}/export", permSpendRead, m.handleExportStatement)

	// Budgets and their alerts.
	reg.Handle("GET", "/budgets", permBudgetRead, m.handleListBudgets)
	reg.Handle("POST", "/budgets", permBudgetWrite, m.handleCreateBudget)
	reg.Handle("GET", "/budgets/{id}", permBudgetRead, m.handleGetBudget)
	reg.Handle("PUT", "/budgets/{id}", permBudgetWrite, m.handleUpdateBudget)
	reg.Handle("DELETE", "/budgets/{id}", permBudgetWrite, m.handleDeleteBudget)
	reg.Handle("GET", "/budgets/{id}/status", permBudgetRead, m.handleBudgetStatus)
	reg.Handle("GET", "/alerts", permBudgetRead, m.handleListAlerts)

	// Admission: reserve before a billable effect, settle by the one handle, reconcile.
	reg.Handle("POST", "/admission/reserve", permBudgetWrite, m.handleAdmissionReserve)
	reg.Handle("POST", "/admission/commit", permBudgetWrite, m.handleAdmissionCommit)
	reg.Handle("POST", "/admission/release", permBudgetWrite, m.handleAdmissionRelease)
	reg.Handle("GET", "/admission/reconciliation", permBudgetRead, m.handleAdmissionReconciliation)
	reg.Handle("POST", "/admission/reconcile", permBudgetWrite, m.handleAdmissionReconcile)
}

// --- spend analytics ---------------------------------------------------------

// handleTeamSummary returns team-level cost aggregation with project/model
// breakdown and a per-calendar-day trend series for a fixed period (7d/30d/90d).

// --- cost ingest -------------------------------------------------------------

// handleIngestCost ingests one CostSample over HTTP (the Anthropic cost_report or a
// live runtime-cost sample) through the SAME canonical onCost path the bus uses — so
// it shares the natural-key dedup, the attribution resolution and the
// single-provenance CostRecord ledger; it never opens a second divergent path. The
// principal's privileged write is audited atomically inside onCost's transaction.
// Returns 202 Accepted (not 201) because dedup can make the write a no-op.
func (m *Module) handleIngestCost(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in costIngestRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	// Mirror onCost's ignore rule (ingest.go) with an explicit 400 so a caller that
	// reports neither a provider nor a model gets a clear error, not a silent no-op.
	if in.ProviderRef == "" && in.ModelRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("provider_ref or model_ref is required"))
		return
	}
	sample := in.toCostSample()
	// Audit to the REAL principal (docs/SECURITY-HARDENING.md): WHO ingested a cost for which
	// provider/model — ids and counts only, never the payload, a secret or PII. Run
	// inside onCost's transaction so a rolled-back ingest leaves no phantom audit.
	audit := func(ctx context.Context, sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      mc.Principal.Actor(),
			ActorKind:  mc.Principal.ActorKind(),
			Action:     "finops.cost.ingest",
			TargetKind: costSampleKind,
			Meta: map[string]any{
				"provider_ref":   sample.ProviderRef,
				"model_ref":      sample.ModelRef,
				"cost_micro_usd": sample.CostMicroUSD,
				"provenance":     provenanceOf(sample.Provenance),
			},
		})
		return err
	}
	if err := m.onCost(r.Context(), mc.Tenant, sample, audit, nil); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// --- budgets -----------------------------------------------------------------

func (m *Module) handleListBudgets(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	q.Filters = append(q.Filters, eq("kind", policyKindBudget))
	out := listResponse[budgetDTO]{Items: []budgetDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		recs, page, err := sc.Policies().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, p := range recs {
			out.Items = append(out.Items, toBudgetDTO(p))
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

func (m *Module) handleGetBudget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out budgetDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindBudget {
			return nil
		}
		found, out = true, toBudgetDTO(p)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleDeleteBudget(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	notBudget := false
	err := m.mutate(r.Context(), mc.Tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindBudget {
			notBudget = true
			return nil
		}
		return sc.Policies().Delete(r.Context(), id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notBudget {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (m *Module) handleBudgetStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now().Time()
	var out budgetStatusDTO
	notBudget := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		p, err := sc.Policies().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindBudget {
			notBudget = true
			return nil
		}
		out, err = budgetStatus(r.Context(), sc, p, now)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notBudget {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleListAlerts(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if bid := r.URL.Query().Get("budget_id"); bid != "" {
		q.Filters = append(q.Filters, eq(colBudgetID, bid))
	}
	// A4.2: retrieve one alert's evidence BY ITS OWN ID, through the same permission,
	// the same tenant scope and the same paginated envelope as the list. The id is
	// validated with the engine's own parser, so a malformed reference is a 400 here
	// rather than an unfiltered read; another tenant's id simply matches nothing,
	// because the repository is bound to the caller's tenant. Holding a reference
	// grants nothing on its own.
	if aid := r.URL.Query().Get("alert_id"); aid != "" {
		id, err := model.ParseID(aid)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid alert_id"))
			return
		}
		q.Filters = append(q.Filters, eq(model.ColID, id.String()))
	}
	out := listResponse[alertDTO]{Items: []alertDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(budgetAlertKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toAlertDTO(rec, sc.Tenant()))
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
