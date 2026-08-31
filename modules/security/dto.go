// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"encoding/hex"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the FINDINGS surface and the module's UI data contract (the
// "intelligence views"). Every wire struct here carries minimal data only — a
// title, a severity, references and a hash — never a raw payload (docs/SECURITY-HARDENING.md). The
// full machine contract is documented separately; the panel mapping
// in docs/UI-CONTRACT-SECURITY.md.
//
// Audit posture of reads: listing/getting findings is NOT self-audited (a finding
// is the module's own alert; auditing every console refresh would flood the
// evidence ledger). The RECON-SENSITIVE reads — the forensic timeline, the SIEM
// export, the anomaly queue and the integrity verification — DO self-audit, exactly
// as access-map audits viewing the access graph (docs/SECURITY-HARDENING.md, §4). All mutations
// (triage, case lifecycle, enforcement posture) are self-audited.

// findingDTO is the wire shape of a persisted finding.
type findingDTO struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	Status      string         `json:"status"`
	Source      string         `json:"source,omitempty"`
	SubjectKind string         `json:"subject_kind,omitempty"`
	SubjectRef  string         `json:"subject_ref,omitempty"`
	Title       string         `json:"title"`
	DetailHash  string         `json:"detail_hash,omitempty"`
	OccurredAt  string         `json:"occurred_at"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func toFindingDTO(f model.Finding) findingDTO {
	ref := ""
	if f.Metadata != nil {
		if v, ok := f.Metadata["subject_ref"].(string); ok {
			ref = v
		}
	}
	if ref == "" && !f.SubjectID.IsZero() {
		ref = f.SubjectID.String()
	}
	return findingDTO{
		ID: f.ID.String(), Kind: f.Kind, Severity: string(f.Severity), Status: string(f.Status),
		Source: f.Source, SubjectKind: f.SubjectKind, SubjectRef: ref, Title: f.Title,
		DetailHash: hex.EncodeToString(f.DetailHash), OccurredAt: f.OccurredAt.String(), Metadata: f.Metadata,
	}
}

// handleListFindings lists the tenant's findings, filterable by kind/severity/
// status/source. It is a plain read (not self-audited): a finding is the module's
// own alert, not the recon-sensitive access graph.
func (m *Module) handleListFindings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	q.Filters = append(q.Filters, findingFilters(r)...)
	out := listResponse[findingDTO]{Items: []findingDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		finds, page, err := sc.Findings().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, f := range finds {
			out.Items = append(out.Items, toFindingDTO(f))
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

// findingFilters is shared by the paginated JSON view and complete exports so
// adding a finding filter cannot make the two surfaces select different rows.
func findingFilters(r *http.Request) []model.Filter {
	var filters []model.Filter
	for _, col := range []string{"kind", "severity", "status", "source", "subject_kind"} {
		if v := strings.TrimSpace(r.URL.Query().Get(col)); v != "" {
			filters = append(filters, eq(col, v))
		}
	}
	return filters
}

// safetyPostureProvider is one provider surface's roll-up in the safety-posture view:
// how many posture findings it has and their severity breakdown. SubjectKind is the
// provider surface (openai.moderation, bedrock.guardrail, azure.rai_policy, …).
type safetyPostureProvider struct {
	SubjectKind string         `json:"subject_kind"`
	Total       int            `json:"total"`
	Open        int            `json:"open"`
	BySeverity  map[string]int `json:"by_severity"`
}

// safetyPostureResponse is the GET /safety-posture view: a per-provider-surface
// roll-up plus the underlying findings (most-recent first). It is the read-first
// "safety posture" surface over the provider-native safety controls the plane
// ingests as safety_posture findings — never the raw provider payload.
type safetyPostureResponse struct {
	Providers []safetyPostureProvider   `json:"providers"`
	Items     api.JSONArray[findingDTO] `json:"items"`
	// HasMore is true when there are more posture FINDINGS than the returned Items page.
	HasMore bool `json:"has_more"`
	// CountsPartial is true only when a tenant exceeds the roll-up scan cap, so the
	// per-provider COUNTS cover the first safetyPostureRollupCap findings rather than
	// the whole estate — surfaced honestly so the roll-up is never silently truncated.
	CountsPartial bool `json:"counts_partial"`
}

// safetyPostureRollupCap bounds how many posture findings the roll-up aggregates, so a
// pathological tenant cannot make the view drain unbounded memory. Posture is deduped
// (one row per subject + state change), so a real estate is far below this; exceeding
// it sets CountsPartial rather than silently under-counting.
const safetyPostureRollupCap = 5000

// handleSafetyPosture aggregates the tenant's provider safety-posture findings (the
// OpenAI Moderation / AWS Bedrock Guardrails / Azure RAI read-first posture) into
// a per-provider-surface roll-up. It is a plain read (a posture finding is the module's
// own alert, not the recon-sensitive access graph — same audit posture as /findings).
// The roll-up COUNTS are complete (every posture finding is drained, up to the rollup
// cap which sets CountsPartial); the returned Items are the most-recent page, with
// HasMore when there are more. Optional severity/status/source/subject_kind filters
// narrow the view.
func (m *Module) handleSafetyPosture(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	filters := []model.Filter{eq("kind", findingKindSafetyPosture)}
	for _, col := range []string{"severity", "status", "source", "subject_kind"} {
		if v := strings.TrimSpace(r.URL.Query().Get(col)); v != "" {
			filters = append(filters, eq(col, v))
		}
	}

	out := safetyPostureResponse{Providers: []safetyPostureProvider{}, Items: []findingDTO{}}
	var all []model.Finding
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		// Drain ALL posture findings (cursor pagination, no Sort — the store rejects
		// cursor+Sort) so the roll-up counts are complete, bounded by the rollup cap.
		cursor := ""
		for len(all) < safetyPostureRollupCap {
			finds, page, err := sc.Findings().List(r.Context(), model.Query{Filters: filters, Limit: listCap, Cursor: cursor})
			if err != nil {
				return err
			}
			all = append(all, finds...)
			if !page.HasMore || page.Cursor == "" {
				return nil
			}
			cursor = page.Cursor
		}
		out.CountsPartial = true // hit the rollup cap; counts cover the first N
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Per-provider-surface roll-up over the complete drain.
	groups := map[string]*safetyPostureProvider{}
	var order []string
	for _, f := range all {
		g := groups[f.SubjectKind]
		if g == nil {
			g = &safetyPostureProvider{SubjectKind: f.SubjectKind, BySeverity: map[string]int{}}
			groups[f.SubjectKind] = g
			order = append(order, f.SubjectKind)
		}
		g.Total++
		g.BySeverity[string(f.Severity)]++
		if f.Status == model.FindingOpen {
			g.Open++
		}
	}
	sort.Strings(order)
	for _, k := range order {
		out.Providers = append(out.Providers, *groups[k])
	}

	// Items: most-recent first, capped to one page (HasMore when more were drained).
	sort.SliceStable(all, func(i, j int) bool { return all[j].OccurredAt.Before(all[i].OccurredAt) })
	if len(all) > listCap {
		out.HasMore = true
		all = all[:listCap]
	}
	for _, f := range all {
		out.Items = append(out.Items, toFindingDTO(f))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetFinding returns one finding.
func (m *Module) handleGetFinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out findingDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		f, err := sc.Findings().Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found, out = true, toFindingDTO(f)
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

type triageRequest struct {
	Status *string `json:"status,omitempty"`
}

// handleTriageFinding updates a finding's triage state (open/triaged/resolved/
// dismissed). It is a mutation and is self-audited to the real principal (docs/SECURITY-HARDENING.md
// §4). The finding's evidence (Kind/Severity/DetailHash) is immutable here — triage
// changes only the workflow state.
func (m *Module) handleTriageFinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req triageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status == nil || !findingStatuses[*req.Status] {
		writeJSON(w, http.StatusBadRequest, errorBody("status must be open, triaged, resolved or dismissed"))
		return
	}
	var out findingDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		f, err := sc.Findings().Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		f.Status = model.FindingStatus(*req.Status)
		updated, err := sc.Findings().Update(r.Context(), f)
		if err != nil {
			return err
		}
		found, out = true, toFindingDTO(updated)
		return auditEvent(r.Context(), sc, mc, "security.finding.triage", model.Kind("core.finding"), id, map[string]any{"status": *req.Status})
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

// findingStatuses are the valid triage states.
var findingStatuses = map[string]bool{
	string(model.FindingOpen): true, string(model.FindingTriaged): true,
	string(model.FindingResolved): true, string(model.FindingDismissed): true,
}
