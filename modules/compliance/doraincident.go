// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the OPEN-CORE half of DORA major-incident plane: the governed
// persistence/export/audit substrate over the closed RegulatoryPackager.ClassifyMajorIncident
// (enterprise/doraregister). It is additive — the open dora.go incident TIMELINE (the
// findings dump under GET /dora) is UNCHANGED; this adds a STRUCTURED classification +
// report draft a financial entity folds into its Art 19 reporting.
//
// Honesty (docs/SECURITY-HARDENING.md): the control plane cannot MEASURE the materiality criteria (clients
// affected, downtime, economic impact) — so it never derives a "major" verdict from its own
// telemetry. The operator supplies the impact data; the packager APPLIES the criteria
// (Regulation (EU) 2022/2554 Art 18 + RTS (EU) 2024/1772) and drafts the report (RTS (EU)
// 2025/301). Every verdict and computed deadline is PROVISIONAL and requires human
// attestation; the legal classification and the duty to report rest with the entity.

// doraIncidentDisclaimer is the honesty banner every classification carries.
const doraIncidentDisclaimer = "Provisional DORA major-incident classification (Regulation (EU) 2022/2554 Art 18 + Commission Delegated Regulation (EU) 2024/1772) and report draft (Commission Delegated Regulation (EU) 2025/301 / Commission Implementing Regulation (EU) 2025/302) from operator-supplied impact data. The control plane cannot measure the materiality criteria; it applies them to your input. The 'major' verdict and the computed reporting deadlines are DECISION SUPPORT, not the legal classification — a competent person must attest them, and the duty to report rests with the financial entity. Thresholds verified against ESA artifacts, not byte-diffed against the Official Journal."

// classifiedIncidentDTO is a stored incident classification as returned to a caller.
type classifiedIncidentDTO struct {
	ID               string              `json:"id"`
	Reference        string              `json:"reference"`
	FindingID        string              `json:"finding_id,omitempty"`
	Major            bool                `json:"major"`
	Provisional      bool                `json:"provisional"`
	CriticalServices bool                `json:"critical_services"`
	CriteriaMet      []string            `json:"criteria_met,omitempty"`
	Rationale        string              `json:"rationale,omitempty"`
	Report           map[string]any      `json:"report,omitempty"`
	Deadlines        map[string]any      `json:"deadlines,omitempty"`
	Basis            []map[string]string `json:"basis,omitempty"`
	Note             string              `json:"note,omitempty"`
	DocSHA256        string              `json:"doc_sha256"`
	ClassifiedBy     string              `json:"classified_by"`
	ClassifiedAt     string              `json:"classified_at"`
	LedgerAnchor     map[string]any      `json:"ledger_anchor,omitempty"`
	Disclaimer       string              `json:"disclaimer"`
}

// incidentClassificationBody is the JSON envelope persisted in the classification column: the
// report draft, the deadlines and the basis (everything except the indexed scalars).
type incidentClassificationBody struct {
	Report    map[string]any      `json:"report,omitempty"`
	Deadlines map[string]any      `json:"deadlines,omitempty"`
	Basis     []map[string]string `json:"basis,omitempty"`
}

func recordToIncidentDTO(rec model.Record, includeBody bool) classifiedIncidentDTO {
	dto := classifiedIncidentDTO{
		ID:               rec.String(model.ColID),
		Reference:        rec.String(colDIReference),
		FindingID:        rec.String(colDIFindingID),
		Major:            rec.Bool(colDIMajor),
		Provisional:      true, // always — a machine verdict requires human attestation
		CriticalServices: rec.Bool(colDICritical),
		CriteriaMet:      decodeStrings(rec.String(colDICriteria)),
		Rationale:        rec.String(colDIRationale),
		Note:             rec.String(colDINote),
		DocSHA256:        rec.String(colDIDocSHA),
		ClassifiedBy:     rec.String(colDIClassifiedBy),
		ClassifiedAt:     rec.String(colDIClassifiedAt),
		Disclaimer:       doraIncidentDisclaimer,
	}
	if includeBody {
		var body incidentClassificationBody
		_ = jsonUnmarshal(rec.String(colDIClassif), &body)
		dto.Report, dto.Deadlines, dto.Basis = body.Report, body.Deadlines, body.Basis
	}
	return dto
}

// handleClassifyIncident applies the DORA major-incident criteria to operator-supplied impact
// data and persists the structured classification + report draft (one per tenant+reference,
// replace-on-reclassify so the report evolves initial → intermediate → final). Deny-closed:
// 501 without the enterprise packager, 422 on input the packager rejects. The impact JSON is
// the request BODY; the incident reference and optional finding link are query parameters.
func (m *Module) handleClassifyIncident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.regPackager == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("DORA major-incident classification requires the Olivares enterprise add-on (doraregister); not linked in this build"))
		return
	}
	reference := strings.TrimSpace(r.URL.Query().Get("reference"))
	if reference == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("reference is required (the operator's incident reference)"))
		return
	}
	// The reference is the identity key (unique tenant+reference): a clamped reference would
	// persist as a DIFFERENT incident (see tooLong), so an over-length one is rejected.
	if tooLong(reference, maxRefLen) {
		writeJSON(w, http.StatusBadRequest, errorBody("reference exceeds "+itoa(int64(maxRefLen))+" characters; identity references are rejected, never truncated"))
		return
	}
	findingID := clamp(strings.TrimSpace(r.URL.Query().Get("finding_id")), maxRefLen)
	impact, ok := readBoundedBody(w, r, "incident impact document")
	if !ok {
		return
	}

	var dto classifiedIncidentDTO
	docSHA := hashHex(string(impact))
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		res, err := m.regPackager.ClassifyMajorIncident(r.Context(), IncidentInput{Reference: reference, FindingID: findingID, Impact: impact})
		if err != nil {
			return errRegisterRejected{err}
		}
		if res == nil {
			return errRegisterRejected{errStr("the incident classifier returned no result")}
		}
		head, headOK, err := sc.Audit().Head(r.Context())
		if err != nil {
			return err
		}
		body := incidentClassificationBody{Report: res.Report, Deadlines: res.Deadlines, Basis: res.Basis}
		now := m.clock.Now()
		fields := map[string]any{
			colDIReference:    reference,
			colDIFindingID:    nullableText(findingID),
			colDIMajor:        res.Major,
			colDICritical:     res.CriticalServices,
			colDICriteria:     encodeJSON(res.CriteriaMet),
			colDIClassif:      encodeJSON(body),
			colDIRationale:    nullableText(clamp(res.Rationale, maxNoteLen)),
			colDINote:         nullableText(clamp(res.Note, maxNoteLen)),
			colDIDocSHA:       docSHA,
			colDIClassifiedBy: mc.Principal.Actor(),
			colDIClassifiedAt: now.String(),
			colLedgerSeq:      head.Seq,
			colLedgerHash:     nullableText(ledgerHashHex(head, headOK)),
		}
		repo, err := sc.Ext(doraIncidentKind)
		if err != nil {
			return err
		}
		existing, err := listAll(r.Context(), repo, eq(colDIReference, reference))
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
		dto = recordToIncidentDTO(saved, true)
		return auditEvent(r.Context(), sc, mc, "compliance.dora.incident.classify", doraIncidentKind, model.ID(saved.String(model.ColID)), map[string]any{
			"reference":         reference,
			"major":             res.Major,
			"critical_services": res.CriticalServices,
			"criteria_met":      len(res.CriteriaMet),
			"doc_sha256":        docSHA,
		})
	})
	if err != nil {
		writeRegisterError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListIncidents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if maj := strings.TrimSpace(r.URL.Query().Get("major")); maj == "true" {
		filters = append(filters, eq(colDIMajor, true))
	}
	var items []classifiedIncidentDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraIncidentKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, filters...)
		for _, rec := range recs {
			items = append(items, recordToIncidentDTO(rec, false))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[classifiedIncidentDTO]{Items: items})
}

func (m *Module) handleGetIncident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto classifiedIncidentDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraIncidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToIncidentDTO(rec, true)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleExportIncidentReport exports the structured major-incident report draft with a LIVE
// ledger integrity proof. Exporting a stored classification is a sensitive evidence read, so
// it self-audits in a committed transaction.
func (m *Module) handleExportIncidentReport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto classifiedIncidentDTO
	var anchor map[string]any
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraIncidentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToIncidentDTO(rec, true)
		anchor, err = liveLedgerAnchor(r.Context(), sc, rec.Int(colLedgerSeq), rec.String(colLedgerHash))
		if err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "compliance.dora.incident.export", doraIncidentKind, id, map[string]any{
			"reference": rec.String(colDIReference),
			"major":     rec.Bool(colDIMajor),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Arrays export as [], never null (the module's convention, calendar.go) — a below-threshold
	// incident legitimately has an empty criteria/basis.
	criteriaMet := dto.CriteriaMet
	if criteriaMet == nil {
		criteriaMet = []string{}
	}
	basis := dto.Basis
	if basis == nil {
		basis = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at":      m.clock.Now().String(),
		"reference":         dto.Reference,
		"major":             dto.Major,
		"provisional":       true,
		"critical_services": dto.CriticalServices,
		"criteria_met":      criteriaMet,
		"rationale":         dto.Rationale,
		"report":            dto.Report,
		"deadlines":         dto.Deadlines,
		"basis":             basis,
		"note":              dto.Note,
		"doc_sha256":        dto.DocSHA256,
		"ledger_anchor":     anchor,
		"disclaimer":        doraIncidentDisclaimer,
	})
}

// handleDeleteIncident removes a stored classification; admin-tier and self-audited.
func (m *Module) handleDeleteIncident(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraIncidentKind)
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
		return auditEvent(r.Context(), sc, mc, "compliance.dora.incident.delete", doraIncidentKind, id, map[string]any{
			"reference": rec.String(colDIReference),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
