// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the OPEN-CORE handler half of NIS 2 Directive significant-incident
// classification plane: the governed persistence/export/audit substrate over the closed
// NIS2IncidentPackager.ClassifySignificantIncident (enterprise/nis2incident). Without a
// wired NIS2 incident packager every classify endpoint answers 501; the default binary is
// byte-identical (no rug-pull, LICENSING.md).
//
// Honesty (docs/SECURITY-HARDENING.md): the control plane cannot MEASURE the Art 23(3) criteria (operational
// disruption, financial loss, persons affected) — so it never derives a "significant" verdict
// from its own telemetry. The operator supplies the impact data; the packager APPLIES the
// criteria and drafts the reports. Every verdict and computed deadline is PROVISIONAL and
// requires human attestation; the legal classification and the duty to report rest with the
// entity.

// nis2IncidentDisclaimer is the honesty banner every classification carries.
const nis2IncidentDisclaimer = "Provisional NIS 2 Directive significant-incident classification " +
	"(Directive (EU) 2022/2555 Art 23(3)) and tiered report drafts (Art 23(4): early warning " +
	"within 24 hours, incident notification within 72 hours, final report within one month) " +
	"from operator-supplied impact data. The control plane cannot measure the Art 23(3) " +
	"criteria; it applies them to your input. The 'significant' verdict and the computed " +
	"reporting deadlines are DECISION SUPPORT, not the legal classification — a competent " +
	"person must attest them, and the duty to notify the CSIRT or competent authority rests " +
	"with the entity."

// --- DTO -------------------------------------------------------------------------

type nis2IncidentDTO struct {
	ID             string              `json:"id"`
	Reference      string              `json:"reference"`
	FindingID      string              `json:"finding_id,omitempty"`
	Significant    bool                `json:"significant"`
	Provisional    bool                `json:"provisional"`
	CrossBorder    bool                `json:"cross_border"`
	SuspectedCrime bool                `json:"suspected_crime"`
	CriteriaMet    []string            `json:"criteria_met,omitempty"`
	Rationale      string              `json:"rationale,omitempty"`
	ReportDrafts   map[string]any      `json:"report_drafts,omitempty"`
	Deadlines      map[string]any      `json:"deadlines,omitempty"`
	Basis          []map[string]string `json:"basis,omitempty"`
	Phase          string              `json:"phase"`
	Note           string              `json:"note,omitempty"`
	DocSHA256      string              `json:"doc_sha256"`
	ClassifiedBy   string              `json:"classified_by"`
	ClassifiedAt   string              `json:"classified_at"`
	LedgerAnchor   map[string]any      `json:"ledger_anchor,omitempty"`
	Disclaimer     string              `json:"disclaimer"`
}

type nis2ClassificationBody struct {
	ReportDrafts map[string]any      `json:"report_drafts,omitempty"`
	Deadlines    map[string]any      `json:"deadlines,omitempty"`
	Basis        []map[string]string `json:"basis,omitempty"`
}

func recordToNIS2IncidentDTO(rec model.Record, includeBody bool) nis2IncidentDTO {
	dto := nis2IncidentDTO{
		ID:             rec.String(model.ColID),
		Reference:      rec.String(colNIReference),
		FindingID:      rec.String(colNIFindingID),
		Significant:    rec.Bool(colNISignificant),
		Provisional:    true,
		CrossBorder:    rec.Bool(colNICrossBorder),
		SuspectedCrime: rec.Bool(colNICrime),
		CriteriaMet:    decodeStrings(rec.String(colNICriteria)),
		Rationale:      rec.String(colNIRationale),
		Phase:          rec.String(colNIPhase),
		Note:           rec.String(colNINote),
		DocSHA256:      rec.String(colNIDocSHA),
		ClassifiedBy:   rec.String(colNIClassifiedBy),
		ClassifiedAt:   rec.String(colNIClassifiedAt),
		Disclaimer:     nis2IncidentDisclaimer,
	}
	if includeBody {
		var body nis2ClassificationBody
		_ = jsonUnmarshal(rec.String(colNIClassif), &body)
		dto.ReportDrafts, dto.Deadlines, dto.Basis = body.ReportDrafts, body.Deadlines, body.Basis
	}
	return dto
}

// --- error types -----------------------------------------------------------------

type errNIS2Rejected struct{ err error }

func (e errNIS2Rejected) Error() string {
	return "NIS2 incident classification rejected: " + e.err.Error()
}
func (e errNIS2Rejected) Unwrap() error { return e.err }

// writeNIS2Error maps a classify failure. THE ORDER OF THE TWO ARMS IS THE CONTRACT.
//
// handleClassifyNIS2Incident wraps EVERY packager error in errNIS2Rejected (:149), and an
// entitlement refusal arrives through exactly that path: the enterprise packager authorizes
// before it decodes and returns license.ErrAddonRequiresLicense, which writeStoreError is
// built to render as 403 naming the add-on (helpers.go:64-76). With the 422 arm first, that
// refusal came out as "NIS2 incident classification rejected: …" — the console then told an
// operator their impact document was bad, so they would go and edit a document that is
// perfectly fine, and the commercial boundary never appeared anywhere.
//
// The 403 check therefore runs FIRST and matches through the wrapper (errNIS2Rejected has
// Unwrap). Found by the Codex sol max contrast of (F2); addonrefusal_test.go claimed the
// module's 403 mapping was covered "once here … for every handler in the module", and that was
// false for exactly the two writers that intercept before writeStoreError — this one and
// writeRegisterError.
func writeNIS2Error(w http.ResponseWriter, err error) {
	if errors.Is(err, license.ErrAddonRequiresLicense) {
		writeStoreError(w, err)
		return
	}
	var rej errNIS2Rejected
	if errors.As(err, &rej) {
		writeJSON(w, http.StatusUnprocessableEntity,
			errorBody("NIS2 incident classification rejected: "+
				clamp(rej.err.Error(), maxNameLen)))
		return
	}
	writeStoreError(w, err)
}

// --- handlers --------------------------------------------------------------------

func (m *Module) handleClassifyNIS2Incident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.nis2Packager == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody(
			"NIS 2 significant-incident classification requires the Olivares enterprise "+
				"add-on (nis2incident); not linked in this build"))
		return
	}
	reference := strings.TrimSpace(r.URL.Query().Get("reference"))
	if reference == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("reference is required (the operator's incident reference)"))
		return
	}
	if tooLong(reference, maxRefLen) {
		writeJSON(w, http.StatusBadRequest, errorBody("reference exceeds "+itoa(int64(maxRefLen))+" characters; identity references are rejected, never truncated"))
		return
	}
	findingID := clamp(strings.TrimSpace(r.URL.Query().Get("finding_id")), maxRefLen)
	impact, ok := readBoundedBody(w, r, "incident impact document")
	if !ok {
		return
	}

	var dto nis2IncidentDTO
	docSHA := hashHex(string(impact))
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		res, err := m.nis2Packager.ClassifySignificantIncident(r.Context(), NIS2IncidentInput{
			Reference: reference, FindingID: findingID, Impact: impact,
		})
		if err != nil {
			return errNIS2Rejected{err}
		}
		if res == nil {
			return errNIS2Rejected{errStr("the incident classifier returned no result")}
		}
		head, headOK, err := sc.Audit().Head(r.Context())
		if err != nil {
			return err
		}
		body := nis2ClassificationBody{ReportDrafts: res.ReportDrafts, Deadlines: res.Deadlines, Basis: res.Basis}
		now := m.clock.Now()
		fields := map[string]any{
			colNIReference:    reference,
			colNIFindingID:    nullableText(findingID),
			colNISignificant:  res.Significant,
			colNICrossBorder:  res.CrossBorder,
			colNICrime:        res.SuspectedCrime,
			colNICriteria:     encodeJSON(res.CriteriaMet),
			colNIClassif:      encodeJSON(body),
			colNIRationale:    nullableText(clamp(res.Rationale, maxNoteLen)),
			colNINote:         nullableText(clamp(res.Note, maxNoteLen)),
			colNIPhase:        "early_warning",
			colNIDocSHA:       docSHA,
			colNIClassifiedBy: mc.Principal.Actor(),
			colNIClassifiedAt: now.String(),
			colLedgerSeq:      head.Seq,
			colLedgerHash:     nullableText(ledgerHashHex(head, headOK)),
		}
		repo, err := sc.Ext(nis2IncidentKind)
		if err != nil {
			return err
		}
		existing, err := listAll(r.Context(), repo, eq(colNIReference, reference))
		if err != nil {
			return err
		}
		var saved model.Record
		if len(existing) > 0 {
			rec := existing[0]
			for k, v := range fields {
				rec[k] = v
			}
			saved, err = repo.Update(r.Context(), rec)
		} else {
			saved, err = repo.Create(r.Context(), model.Record(fields))
		}
		if err != nil {
			return err
		}
		dto = recordToNIS2IncidentDTO(saved, true)
		return auditEvent(r.Context(), sc, mc, "compliance.nis2.incident.classify", nis2IncidentKind, model.ID(saved.String(model.ColID)), map[string]any{
			"reference":   reference,
			"significant": res.Significant,
			"doc_sha256":  docSHA,
		})
	})
	if err != nil {
		writeNIS2Error(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListNIS2Incidents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var items []nis2IncidentDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nis2IncidentKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			items = append(items, recordToNIS2IncidentDTO(rec, false))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if items == nil {
		items = []nis2IncidentDTO{}
	}
	writeJSON(w, http.StatusOK, listResponse[nis2IncidentDTO]{Items: items})
}

func (m *Module) handleGetNIS2Incident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto nis2IncidentDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nis2IncidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToNIS2IncidentDTO(rec, true)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleUpdateNIS2Incident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req struct {
		Phase string `json:"phase"`
		Note  string `json:"note"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	phase := strings.TrimSpace(req.Phase)
	if phase != "" && !validNIS2Phase(phase) {
		writeJSON(w, http.StatusBadRequest, errorBody("phase must be one of: early_warning, notification, intermediate, final"))
		return
	}

	var dto nis2IncidentDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nis2IncidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if phase != "" {
			current := rec.String(colNIPhase)
			if nis2PhaseIndex(phase) <= nis2PhaseIndex(current) {
				writeJSON(w, http.StatusConflict, errorBody("phase transitions are forward-only; current phase is "+current))
				return store.ErrConflict
			}
			rec[colNIPhase] = phase
		}
		if note := strings.TrimSpace(req.Note); note != "" {
			rec[colNINote] = nullableText(clamp(note, maxNoteLen))
		}
		saved, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = recordToNIS2IncidentDTO(saved, true)
		return auditEvent(r.Context(), sc, mc, "compliance.nis2.incident.update", nis2IncidentKind, id, map[string]any{
			"reference": rec.String(colNIReference),
			"phase":     phase,
		})
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleExportNIS2Incident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto nis2IncidentDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nis2IncidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToNIS2IncidentDTO(rec, true)
		anchor, err := liveLedgerAnchor(r.Context(), sc, rec.Int(colLedgerSeq), rec.String(colLedgerHash))
		if err != nil {
			return err
		}
		dto.LedgerAnchor = anchor
		return auditEvent(r.Context(), sc, mc, "compliance.nis2.incident.export", nis2IncidentKind, id, map[string]any{
			"reference": rec.String(colNIReference),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteNIS2Incident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nis2IncidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "compliance.nis2.incident.delete", nis2IncidentKind, id, map[string]any{
			"reference": rec.String(colNIReference),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
