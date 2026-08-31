// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// This file holds the read-tier catalog + reporting surface: the in-repo framework
// catalog, the on-read control STATUS assessment, the GAP analysis, the live
// capability EVIDENCE map and the cross-framework SUMMARY. These are derived
// aggregates (not raw ledger evidence), so they are read-tier and not self-audited;
// sealing/exporting an evidence package (evidence.go) is the privileged audited path.

// reportDisclaimer is stamped on every reporting response. The module designs-for-
// audit; it never certifies (docs/SECURITY-HARDENING.md).
const reportDisclaimer = "Technical control-status mapping derived from observed platform evidence. NOT a certification and NOT legal advice."

// frameworkSummaryDTO is the light catalog entry for the list endpoint.
type frameworkSummaryDTO struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Version   string       `json:"version"`
	Authority string       `json:"authority"`
	Pin       FrameworkPin `json:"pin"`
	Controls  int          `json:"controls"`
}

func (m *Module) handleListFrameworks(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	items := make([]frameworkSummaryDTO, 0, len(catalog))
	for _, fw := range catalog {
		items = append(items, frameworkSummaryDTO{
			ID: fw.ID, Name: fw.Name, Version: fw.Version, Authority: fw.Authority, Pin: fw.Pin, Controls: len(fw.Controls),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "disclaimer": reportDisclaimer})
}

func (m *Module) handleGetFramework(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	fw, ok := frameworkByID[strings.TrimSpace(chi.URLParam(r, "id"))]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody("unknown framework"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"framework": fw, "disclaimer": reportDisclaimer})
}

// gatherFor reads the tenant's evidence once in a read-only transaction.
func (m *Module) gatherFor(ctx context.Context, mc api.ModuleContext) (*capState, error) {
	var s *capState
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		var e error
		s, e = gatherEvidence(ctx, sc)
		return e
	})
	return s, err
}

func (m *Module) handleFrameworkStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	fw, ok := frameworkByID[strings.TrimSpace(chi.URLParam(r, "id"))]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody("unknown framework"))
		return
	}
	s, err := m.gatherFor(r.Context(), mc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	fa := assessFramework(fw, evaluateCapabilities(s))
	if wantsCSV(r) {
		writeCSV(w, controlsCSV(fa.Controls))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assessment": fa, "disclaimer": reportDisclaimer})
}

func (m *Module) handleFrameworkGaps(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	fw, ok := frameworkByID[strings.TrimSpace(chi.URLParam(r, "id"))]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody("unknown framework"))
		return
	}
	s, err := m.gatherFor(r.Context(), mc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	fa := assessFramework(fw, evaluateCapabilities(s))
	gaps := gapControls(fa)
	if wantsCSV(r) {
		writeCSV(w, gapsCSV(gaps))
		return
	}
	type gapDTO struct {
		ControlAssessment
		Missing []CapabilityKey `json:"missing_capabilities"`
	}
	items := make([]gapDTO, 0, len(gaps))
	for _, ca := range gaps {
		items = append(items, gapDTO{ControlAssessment: ca, Missing: missingCapabilities(ca)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"framework": fa.Framework, "name": fa.Name, "summary": fa.Summary,
		"gaps": items, "disclaimer": reportDisclaimer,
	})
}

// handleCapabilities returns the capability catalog with the tenant's live evidence
// state — the "what the platform can evidence right now" map.
func (m *Module) handleCapabilities(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	s, err := m.gatherFor(r.Context(), mc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	caps := evaluateCapabilities(s)
	items := make([]CapabilityEvidence, 0, len(capabilityCatalog))
	for _, c := range capabilityCatalog {
		ev := caps[c.Key]
		// Carry the human name/description alongside the live state.
		ev.Detail = c.Name + " — " + ev.Detail
		items = append(items, ev)
	}
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": items, "disclaimer": reportDisclaimer})
}

// handleSummary returns a cross-framework posture summary for the tenant (the org/
// tenant roll-up). The executive PDF/dashboard is XXI; this is the data.
func (m *Module) handleSummary(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	s, err := m.gatherFor(r.Context(), mc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	caps := evaluateCapabilities(s)
	type frameworkRow struct {
		Framework string            `json:"framework"`
		Name      string            `json:"name"`
		Version   string            `json:"version"`
		Summary   AssessmentSummary `json:"summary"`
	}
	rows := make([]frameworkRow, 0, len(catalog))
	flat := make([]summaryRow, 0, len(catalog))
	for _, fw := range catalog {
		fa := assessFramework(fw, caps)
		rows = append(rows, frameworkRow{Framework: fw.ID, Name: fw.Name, Version: fw.Version, Summary: fa.Summary})
		flat = append(flat, summaryRow{framework: fw.ID, name: fw.Name, version: fw.Version, s: fa.Summary})
	}
	if wantsCSV(r) {
		writeCSV(w, summaryCSV(flat))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"frameworks": rows, "disclaimer": reportDisclaimer})
}

// summaryRow is the flat shape the summary CSV writer consumes.
type summaryRow struct {
	framework, name, version string
	s                        AssessmentSummary
}

// --- CSV writers (flat, RFC 4180) -------------------------------------------------

func controlsCSV(cs []ControlAssessment) string {
	var b strings.Builder
	b.WriteString("control_id,status,title,present,total,note\n")
	for _, ca := range cs {
		present := 0
		for _, ev := range ca.Capabilities {
			if ev.State == EvidencePresent {
				present++
			}
		}
		b.WriteString(csvField(ca.ControlID))
		b.WriteByte(',')
		b.WriteString(string(ca.Status))
		b.WriteByte(',')
		b.WriteString(csvField(ca.Title))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(present))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(len(ca.Capabilities)))
		b.WriteByte(',')
		b.WriteString(csvField(ca.Note))
		b.WriteByte('\n')
	}
	return b.String()
}

func gapsCSV(cs []ControlAssessment) string {
	var b strings.Builder
	b.WriteString("control_id,status,title,missing_capabilities,note\n")
	for _, ca := range cs {
		miss := missingCapabilities(ca)
		parts := make([]string, len(miss))
		for i, k := range miss {
			parts[i] = string(k)
		}
		b.WriteString(csvField(ca.ControlID))
		b.WriteByte(',')
		b.WriteString(string(ca.Status))
		b.WriteByte(',')
		b.WriteString(csvField(ca.Title))
		b.WriteByte(',')
		b.WriteString(csvField(strings.Join(parts, " ")))
		b.WriteByte(',')
		b.WriteString(csvField(ca.Note))
		b.WriteByte('\n')
	}
	return b.String()
}

func summaryCSV(rows []summaryRow) string {
	var b strings.Builder
	b.WriteString("framework,name,version,total,satisfied,by_design,partial,gap,unmapped\n")
	for _, r := range rows {
		b.WriteString(csvField(r.framework))
		b.WriteByte(',')
		b.WriteString(csvField(r.name))
		b.WriteByte(',')
		b.WriteString(csvField(r.version))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(r.s.Total))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(r.s.Satisfied))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(r.s.ByDesign))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(r.s.Partial))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(r.s.Gap))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(r.s.Unmapped))
		b.WriteByte('\n')
	}
	return b.String()
}
