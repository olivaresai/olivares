//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package compliance

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Route descriptions retain their published contracts. These Community handlers
// return 501; Business Compliance Packs supplies the described implementations.
var errComplianceViewsUnavailable = errors.New("compliance views require Business Compliance Packs")
var catalog []Framework
var frameworkByID = map[string]Framework{}
var capabilityCatalog []Capability

type capState struct{}

func gatherEvidence(context.Context, store.Scope) (*capState, error) {
	return nil, errComplianceViewsUnavailable
}
func evaluateCapabilities(*capState) map[CapabilityKey]CapabilityEvidence { return nil }
func assessFramework(Framework, map[CapabilityKey]CapabilityEvidence) FrameworkAssessment {
	return FrameworkAssessment{}
}
func (m *Module) AssessAll(context.Context, model.TenantID) ([]FrameworkAssessment, error) {
	return nil, errComplianceViewsUnavailable
}
func PublicCatalog() PublicCatalogDoc { return PublicCatalogDoc{Version: PublicCatalogVersion} }
func complianceViewsAvailable() bool  { return false }
func unavailableComplianceView(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotImplemented, api.ErrorBody("compliance_views_unavailable", errComplianceViewsUnavailable.Error()))
}
func (m *Module) handleListFrameworks(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}
func (m *Module) handleGetFramework(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}
func (m *Module) handleFrameworkStatus(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}
func (m *Module) handleFrameworkGaps(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}

// handleCapabilities returns the capability catalog with the tenant's live evidence
// state — the "what the platform can evidence right now" map.
func (m *Module) handleCapabilities(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}

// handleSummary returns a cross-framework posture summary for the tenant (the org/
// tenant roll-up). The executive PDF/dashboard is XXI; this is the data.
func (m *Module) handleSummary(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}

// handleCalendar returns the regulatory calendar + watchlist (read-tier; static
// verified data, not tenant evidence). ?framework= filters both lists to the
// milestones/items linked to one catalog framework.
func (m *Module) handleCalendar(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}

// handleDORAExport assembles and returns the DORA export. Exporting the tenant's
// risk register + incident timeline is a SENSITIVE evidence read, so it self-audits
// in a committed transaction, exactly like the evidence export.
func (m *Module) handleDORAExport(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}
func (m *Module) handleHIPAAGapReport(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}

// handleSealEvidence seals an immutable evidence package for a framework: it assesses
// the framework against the tenant's live evidence, records the ledger head + the live
// hash-chain verify result (the integrity proof), persists the package and its
// per-control results (append-only), and self-audits the seal — all in one transaction.
func (m *Module) handleSealEvidence(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}
func (m *Module) handleExportOSCAL(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	unavailableComplianceView(w)
}
