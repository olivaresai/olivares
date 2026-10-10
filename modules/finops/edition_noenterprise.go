// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package finops

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func (*Module) handleAllocation(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleComparison(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleCreateBudget(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleCreateCostCenter(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleCreateCostCenterMapping(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleCreateModelRate(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleDeleteCostCenter(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleDeleteCostCenterMapping(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleDeleteModelRate(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleExportStatement(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleForecast(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleGenerateStatements(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleGetCostCenter(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleGetModelRate(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleGetStatement(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleIngestOutcome(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleIngestSeats(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleListCostCenterMappings(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleListCostCenters(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleListModelRates(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleListOutcomes(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleListStatements(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleRecommendations(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleReconciliation(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleSeatUtilization(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleSpend(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleSummary(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleTeamSummary(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleTrend(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleUnified(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleUpdateBudget(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleUpdateCostCenter(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleUpdateModelRate(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleValue(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) handleValueSummary(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("FinOps is a Business feature: https://olivares.ai/pricing"))
}

func (*Module) SpendLimitUpsert(context.Context, model.TenantID, SpendLimitSpec, string) (SpendLimit, bool, error) {
	return SpendLimit{}, false, ErrNotInEdition
}

func addBudgetForecast(context.Context, store.Scope, budgetSpec, time.Time, time.Time, bool, aggResult, int64, *budgetStatusDTO) {
}
