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
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the OPEN-CORE handler half of compliance-depth
// seam: US state AI law packs, sector-overlay packs (HIPAA/PCI/FINRA),
// continuous controls monitoring (CCM) snapshots + drift, and FedRAMP
// 20x Key Security Indicator (KSI) documents. The seam interface
// (ComplianceDepthPackager) and value objects live in depthseam.go; the
// entity kinds and column constants live in schema.go. Without a wired
// depth packager every generate/snapshot/drift/KSI endpoint answers 501;
// the default binary is byte-identical (no rug-pull, LICENSING.md S9).
//
// Honesty (docs/SECURITY-HARDENING.md S9): the add-on automates evidence gathering,
// obligation mapping and reporting against named regulations; it does
// NOT make the operator compliant with any law and is NOT a
// certification. Every emitted verdict is PROVISIONAL and requires
// human attestation. An honest gap is exported as a gap, never as
// satisfied; satisfied never rests on architectural evidence alone
// (assess.go:15).

// usStateLawFrameworks lists the four US state AI law frameworks
// assessed in a US state law compliance pack.
var usStateLawFrameworks = []string{
	"tx_traiga", "ca_sb53", "il_hb3773", "co_sb26_189",
}

// sectorOverlayFrameworks lists the three sector-overlay frameworks
// assessed in a sector-overlay compliance pack.
var sectorOverlayFrameworks = []string{
	"hipaa_clinical_ai", "pci_dss_401_ai", "finra_genai",
}

// --- error types --------------------------------------------------------

type errDepthRejected struct{ err error }

func (e errDepthRejected) Error() string {
	return "depth pack rejected: " + e.err.Error()
}
func (e errDepthRejected) Unwrap() error { return e.err }

type errFedRAMPRejected struct{ err error }

func (e errFedRAMPRejected) Error() string {
	return "FedRAMP KSI rejected: " + e.err.Error()
}
func (e errFedRAMPRejected) Unwrap() error { return e.err }

func writeDepthError(w http.ResponseWriter, err error) {
	var rej errDepthRejected
	if errors.As(err, &rej) {
		writeJSON(w, http.StatusUnprocessableEntity,
			errorBody("depth pack rejected: "+
				clamp(rej.err.Error(), maxNameLen)))
		return
	}
	writeStoreError(w, err)
}

func writeFedRAMPError(w http.ResponseWriter, err error) {
	var rej errFedRAMPRejected
	if errors.As(err, &rej) {
		writeJSON(w, http.StatusUnprocessableEntity,
			errorBody("FedRAMP KSI rejected: "+
				clamp(rej.err.Error(), maxNameLen)))
		return
	}
	writeStoreError(w, err)
}

// --- record-to-DTO converters -------------------------------------------

func recordToDepthPackDTO(
	rec model.Record, includeBody bool,
) depthPackDTO {
	var sections map[string]any
	var validation []DepthIssue
	_ = jsonUnmarshal(rec.String(colDPSections), &sections)
	_ = jsonUnmarshal(
		rec.String(colDPValidation), &validation)
	packType := rec.String(colDPPackType)
	disclaimer := usStateLawDisclaimer
	if packType == "sector_overlay" {
		disclaimer = sectorOverlayDisclaimer
	}
	dto := depthPackDTO{
		ID:          rec.String(model.ColID),
		PackType:    packType,
		Regulation:  rec.String(colDPRegulation),
		Validation:  validation,
		ErrorCount:  countDepthErrors(validation),
		Note:        rec.String(colDPNote),
		DocSHA256:   rec.String(colDPDocSHA),
		ScopeNote:   rec.String(colDPScopeNote),
		GeneratedBy: rec.String(colGeneratedBy),
		GeneratedAt: rec.String(colGeneratedAt),
		Disclaimer:  disclaimer,
	}
	if includeBody {
		dto.Sections = sections
	}
	return dto
}

func recordToCCMSnapshotDTO(
	rec model.Record, includeBody bool,
) ccmSnapshotDTO {
	var frameworks, summary map[string]any
	_ = jsonUnmarshal(
		rec.String(colCSFrameworks), &frameworks)
	_ = jsonUnmarshal(rec.String(colCSSummary), &summary)
	dto := ccmSnapshotDTO{
		ID:          rec.String(model.ColID),
		SnapshotAt:  rec.String(colCSSnapshotAt),
		Note:        rec.String(colCSNote),
		GeneratedBy: rec.String(colGeneratedBy),
		GeneratedAt: rec.String(colGeneratedAt),
		Disclaimer:  ccmDisclaimer,
	}
	if includeBody {
		dto.Frameworks = frameworks
		dto.Summary = summary
	}
	return dto
}

func recordToDriftFindingDTO(
	rec model.Record,
) driftFindingDTO {
	return driftFindingDTO{
		ID:          rec.String(model.ColID),
		SnapshotRef: rec.String(colCDSnapshotRef),
		FrameworkID: rec.String(colCDFrameworkID),
		ControlID:   rec.String(colCDControlID),
		Title:       rec.String(colCDTitle),
		PrevStatus:  rec.String(colCDPrevStatus),
		CurrStatus:  rec.String(colCDCurrStatus),
		Direction:   rec.String(colCDDirection),
		Detail:      rec.String(colCDDetail),
		DetectedAt:  rec.String(colCDDetectedAt),
	}
}

func recordToFedRAMPKSIDTO(
	rec model.Record, includeBody bool,
) fedRAMPKSIDTO {
	var ksis, authPkg map[string]any
	var validation []DepthIssue
	_ = jsonUnmarshal(rec.String(colFKKSIs), &ksis)
	_ = jsonUnmarshal(rec.String(colFKAuthPkg), &authPkg)
	_ = jsonUnmarshal(
		rec.String(colFKValidation), &validation)
	dto := fedRAMPKSIDTO{
		ID:           rec.String(model.ColID),
		SystemName:   rec.String(colFKSystemName),
		ImpactLevel:  rec.String(colFKImpactLevel),
		OscalVersion: rec.String(colFKOscalVer),
		Validation:   validation,
		ErrorCount:   countDepthErrors(validation),
		Note:         rec.String(colFKNote),
		DocSHA256:    rec.String(colFKDocSHA),
		ScopeNote:    rec.String(colFKScopeNote),
		GeneratedBy:  rec.String(colGeneratedBy),
		GeneratedAt:  rec.String(colGeneratedAt),
		Disclaimer:   fedRAMPKSIDisclaimer,
	}
	if includeBody {
		dto.KSIs = ksis
		dto.AuthPkg = authPkg
	}
	return dto
}

func countDepthErrors(issues []DepthIssue) int {
	n := 0
	for _, i := range issues {
		if i.Severity == "error" {
			n++
		}
	}
	return n
}

// --- multi-framework evidence gathering ---------------------------------

// gatherMultiAssessments evaluates capabilities once and assesses each
// of the given framework IDs, returning a map keyed by framework ID.
// Unknown framework IDs are silently skipped (the packager is
// responsible for validating the requested set).
func gatherMultiAssessments(
	r *http.Request,
	sc store.Scope,
	fwIDs []string,
) (map[string]FrameworkAssessment, error) {
	s, err := gatherEvidence(r.Context(), sc)
	if err != nil {
		return nil, err
	}
	caps := evaluateCapabilities(s)
	out := make(map[string]FrameworkAssessment, len(fwIDs))
	for _, id := range fwIDs {
		fw, ok := frameworkByID[id]
		if !ok {
			continue
		}
		out[id] = assessFramework(fw, caps)
	}
	return out, nil
}

// ========================================================================
// Group 1: US State Law Packs
// ========================================================================

// handleGenerateUSStatePack structures operator-supplied jurisdiction
// context + the live assessment of the 4 US state AI law frameworks
// into a compliance pack (deny-closed: 501 without a configured
// depth packager, 422 on input the packager rejects), persists it
// (one per tenant, replace-on-regenerate) anchored to the ledger
// head, and self-audits.
func (m *Module) handleGenerateUSStatePack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	if m.depthPackager == nil {
		writeJSON(w, http.StatusNotImplemented,
			errorBody(
				"US state AI law compliance pack "+
					"generation requires the Olivares "+
					"enterprise add-on "+
					"(compliancedepth); "+
					"not linked in this build"))
		return
	}
	doc, ok := readBoundedBody(
		w, r, "US state law jurisdiction context")
	if !ok {
		return
	}
	scopeNote := clamp(
		strings.TrimSpace(
			r.URL.Query().Get("scope_note")),
		maxNoteLen)

	var dto depthPackDTO
	docSHA := hashHex(string(doc))
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			assessments, err := gatherMultiAssessments(
				r, sc, usStateLawFrameworks)
			if err != nil {
				return err
			}
			built, err := m.depthPackager.BuildUSStatePack(
				r.Context(),
				USStateInput{
					Document:  doc,
					ScopeNote: scopeNote,
				},
				assessments)
			if err != nil {
				return errDepthRejected{err}
			}
			if built == nil {
				return errDepthRejected{
					errStr("packager returned nil")}
			}

			head, headOK, err :=
				sc.Audit().Head(r.Context())
			if err != nil {
				return err
			}
			now := m.clock.Now()
			fields := map[string]any{
				colDPPackType: "us_state_law",
				colDPRegulation: encodeJSON(
					jurisdictionNames(
						built.Jurisdictions)),
				colDPSections: encodeJSON(
					usStatePackSections(built)),
				colDPValidation: encodeJSON(
					built.Validation),
				colDPNote: nullableText(
					clamp(built.Note, maxNoteLen)),
				colDPDocSHA: docSHA,
				colDPScopeNote: nullableText(
					clamp(scopeNote, maxNoteLen)),
				colGeneratedBy: mc.Principal.Actor(),
				colGeneratedAt: now.String(),
				colLedgerSeq:   head.Seq,
				colLedgerHash: nullableText(
					ledgerHashHex(head, headOK)),
			}
			repo, err := sc.Ext(usStateLawPackKind)
			if err != nil {
				return err
			}
			existing, err := listAll(r.Context(), repo)
			if err != nil {
				return err
			}
			var saved model.Record
			if len(existing) > 0 {
				rec := existing[0]
				for k, v := range fields {
					rec[k] = v
				}
				saved, err = repo.Update(
					r.Context(), rec)
			} else {
				saved, err = repo.Create(
					r.Context(),
					model.Record(fields))
			}
			if err != nil {
				return err
			}
			dto = recordToDepthPackDTO(saved, true)
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.us_law.generate",
				usStateLawPackKind,
				model.ID(
					saved.String(model.ColID)),
				map[string]any{
					"jurisdictions": len(
						built.Jurisdictions),
					"errors": countDepthErrors(
						built.Validation),
					"doc_sha256": docSHA,
				})
		})
	if err != nil {
		writeDepthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListUSStatePacks(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	var items []depthPackDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(usStateLawPackKind)
			if err != nil {
				return err
			}
			recs, lerr := listAll(r.Context(), repo)
			for _, rec := range recs {
				items = append(items,
					recordToDepthPackDTO(rec, false))
			}
			return lerr
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		listResponse[depthPackDTO]{Items: items})
}

func (m *Module) handleGetUSStatePack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	var dto depthPackDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(usStateLawPackKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			dto = recordToDepthPackDTO(rec, true)
			return nil
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleExportUSStatePack exports the maintained US state law pack
// with a LIVE ledger integrity proof so the export proves the pack
// was anchored to a tamper-evident ledger. Self-audits.
func (m *Module) handleExportUSStatePack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	var dto depthPackDTO
	var anchor map[string]any
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(usStateLawPackKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			dto = recordToDepthPackDTO(rec, true)
			anchor, err = liveLedgerAnchor(
				r.Context(), sc,
				rec.Int(colLedgerSeq),
				rec.String(colLedgerHash))
			if err != nil {
				return err
			}
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.us_law.export",
				usStateLawPackKind, id,
				map[string]any{
					"pack_type": "us_state_law",
				})
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dto.LedgerAnchor = anchor
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteUSStatePack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(usStateLawPackKind)
			if err != nil {
				return err
			}
			if _, err := repo.Get(
				r.Context(), id); err != nil {
				return err
			}
			if err := repo.Delete(
				r.Context(), id); err != nil {
				return err
			}
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.us_law.delete",
				usStateLawPackKind, id,
				map[string]any{
					"pack_type": "us_state_law",
				})
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ========================================================================
// Group 2: Sector Overlay Packs
// ========================================================================

// handleGenerateSectorPack structures operator-supplied sector context
// + the live assessment of sector-overlay frameworks into a compliance
// pack (deny-closed: 501 without the depth packager, 422 on reject),
// persists it (one per tenant, replace-on-regenerate) anchored to the
// ledger head, and self-audits.
func (m *Module) handleGenerateSectorPack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	if m.depthPackager == nil {
		writeJSON(w, http.StatusNotImplemented,
			errorBody(
				"Sector-overlay compliance pack "+
					"generation requires the Olivares "+
					"enterprise add-on "+
					"(compliancedepth); "+
					"not linked in this build"))
		return
	}
	doc, ok := readBoundedBody(
		w, r, "sector overlay context")
	if !ok {
		return
	}
	scopeNote := clamp(
		strings.TrimSpace(
			r.URL.Query().Get("scope_note")),
		maxNoteLen)

	var dto depthPackDTO
	docSHA := hashHex(string(doc))
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			assessments, err := gatherMultiAssessments(
				r, sc, sectorOverlayFrameworks)
			if err != nil {
				return err
			}
			built, err := m.depthPackager.BuildSectorPack(
				r.Context(),
				SectorInput{
					Document:  doc,
					ScopeNote: scopeNote,
				},
				assessments)
			if err != nil {
				return errDepthRejected{err}
			}
			if built == nil {
				return errDepthRejected{
					errStr("packager returned nil")}
			}

			head, headOK, err :=
				sc.Audit().Head(r.Context())
			if err != nil {
				return err
			}
			now := m.clock.Now()
			fields := map[string]any{
				colDPPackType: "sector_overlay",
				colDPRegulation: encodeJSON(
					sectorNames(built.Sectors)),
				colDPSections: encodeJSON(
					sectorPackSections(built)),
				colDPValidation: encodeJSON(
					built.Validation),
				colDPNote: nullableText(
					clamp(built.Note, maxNoteLen)),
				colDPDocSHA: docSHA,
				colDPScopeNote: nullableText(
					clamp(scopeNote, maxNoteLen)),
				colGeneratedBy: mc.Principal.Actor(),
				colGeneratedAt: now.String(),
				colLedgerSeq:   head.Seq,
				colLedgerHash: nullableText(
					ledgerHashHex(head, headOK)),
			}
			repo, err := sc.Ext(sectorPackKind)
			if err != nil {
				return err
			}
			existing, err := listAll(r.Context(), repo)
			if err != nil {
				return err
			}
			var saved model.Record
			if len(existing) > 0 {
				rec := existing[0]
				for k, v := range fields {
					rec[k] = v
				}
				saved, err = repo.Update(
					r.Context(), rec)
			} else {
				saved, err = repo.Create(
					r.Context(),
					model.Record(fields))
			}
			if err != nil {
				return err
			}
			dto = recordToDepthPackDTO(saved, true)
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.sector.generate",
				sectorPackKind,
				model.ID(
					saved.String(model.ColID)),
				map[string]any{
					"sectors": len(built.Sectors),
					"errors": countDepthErrors(
						built.Validation),
					"doc_sha256": docSHA,
				})
		})
	if err != nil {
		writeDepthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListSectorPacks(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	var items []depthPackDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(sectorPackKind)
			if err != nil {
				return err
			}
			recs, lerr := listAll(r.Context(), repo)
			for _, rec := range recs {
				items = append(items,
					recordToDepthPackDTO(rec, false))
			}
			return lerr
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		listResponse[depthPackDTO]{Items: items})
}

func (m *Module) handleGetSectorPack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	var dto depthPackDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(sectorPackKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			dto = recordToDepthPackDTO(rec, true)
			return nil
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleExportSectorPack exports the maintained sector overlay pack
// with a LIVE ledger integrity proof. Self-audits.
func (m *Module) handleExportSectorPack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	var dto depthPackDTO
	var anchor map[string]any
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(sectorPackKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			dto = recordToDepthPackDTO(rec, true)
			anchor, err = liveLedgerAnchor(
				r.Context(), sc,
				rec.Int(colLedgerSeq),
				rec.String(colLedgerHash))
			if err != nil {
				return err
			}
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.sector.export",
				sectorPackKind, id,
				map[string]any{
					"pack_type": "sector_overlay",
				})
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dto.LedgerAnchor = anchor
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteSectorPack(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(sectorPackKind)
			if err != nil {
				return err
			}
			if _, err := repo.Get(
				r.Context(), id); err != nil {
				return err
			}
			if err := repo.Delete(
				r.Context(), id); err != nil {
				return err
			}
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.sector.delete",
				sectorPackKind, id,
				map[string]any{
					"pack_type": "sector_overlay",
				})
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ========================================================================
// Group 3: CCM Snapshots
// ========================================================================

// handleTriggerCCMSnapshot re-evaluates the assessment engine for a
// set of frameworks (or all catalog frameworks when none specified)
// at the current instant and persists the timestamped snapshot
// (deny-closed: 501 without the depth packager). Self-audits.
func (m *Module) handleTriggerCCMSnapshot(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	if m.depthPackager == nil {
		writeJSON(w, http.StatusNotImplemented,
			errorBody(
				"Continuous controls monitoring "+
					"(CCM) requires the Olivares "+
					"enterprise add-on "+
					"(compliancedepth); "+
					"not linked in this build"))
		return
	}
	// Parse optional request body for framework
	// selection + scope note.
	var input struct {
		Frameworks []string `json:"frameworks"`
		ScopeNote  string   `json:"scope_note"`
	}
	// NOT `ContentLength > 0`: Go reports an unknown length as -1, so a chunked
	// request carrying a narrowed framework list skipped decoding and fell through
	// to "snapshot EVERY framework" with a 201. See decodeOptionalJSON.
	if !decodeOptionalJSON(w, r, &input) {
		return
	}
	scopeNote := clamp(
		strings.TrimSpace(input.ScopeNote),
		maxNoteLen)

	// Determine which frameworks to snapshot.
	fwIDs := input.Frameworks
	if len(fwIDs) == 0 {
		for id := range frameworkByID {
			fwIDs = append(fwIDs, id)
		}
	}

	var dto ccmSnapshotDTO
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			assessments, err := gatherMultiAssessments(
				r, sc, fwIDs)
			if err != nil {
				return err
			}
			built, err := m.depthPackager.RunCCMSnapshot(
				r.Context(),
				CCMSnapshotInput{
					Frameworks: fwIDs,
					ScopeNote:  scopeNote,
				},
				assessments)
			if err != nil {
				return err
			}
			if built == nil {
				return errors.New(
					"CCM snapshot: packager " +
						"returned nil")
			}

			head, headOK, err :=
				sc.Audit().Head(r.Context())
			if err != nil {
				return err
			}
			now := m.clock.Now()
			fields := map[string]any{
				colCSSnapshotAt: nonEmpty(
					built.SnapshotAt, now.String()),
				colCSFrameworks: encodeJSON(
					built.Frameworks),
				colCSSummary: encodeJSON(
					built.Summary),
				colCSNote: nullableText(
					clamp(built.Note, maxNoteLen)),
				colGeneratedBy: mc.Principal.Actor(),
				colGeneratedAt: now.String(),
				colLedgerSeq:   head.Seq,
				colLedgerHash: nullableText(
					ledgerHashHex(head, headOK)),
			}
			repo, err := sc.Ext(ccmSnapshotKind)
			if err != nil {
				return err
			}
			saved, err := repo.Create(
				r.Context(), model.Record(fields))
			if err != nil {
				return err
			}
			dto = recordToCCMSnapshotDTO(saved, true)
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.ccm.snapshot",
				ccmSnapshotKind,
				model.ID(
					saved.String(model.ColID)),
				map[string]any{
					"frameworks": len(
						built.Frameworks),
					"total_controls": built.Summary.TotalControls,
				})
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListCCMSnapshots(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	var items []ccmSnapshotDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(ccmSnapshotKind)
			if err != nil {
				return err
			}
			recs, lerr := listAll(r.Context(), repo)
			for _, rec := range recs {
				items = append(items,
					recordToCCMSnapshotDTO(
						rec, false))
			}
			return lerr
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		listResponse[ccmSnapshotDTO]{Items: items})
}

func (m *Module) handleGetCCMSnapshot(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	var dto ccmSnapshotDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(ccmSnapshotKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			dto = recordToCCMSnapshotDTO(rec, true)
			return nil
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// ========================================================================
// Group 4: CCM Drift
// ========================================================================

// handleDetectDrift compares two CCM snapshots (the two latest, or a
// specified snapshot_id and its predecessor) and persists per-control
// drift findings (deny-closed: 501 without the depth packager).
// Self-audits.
func (m *Module) handleDetectDrift(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	if m.depthPackager == nil {
		writeJSON(w, http.StatusNotImplemented,
			errorBody(
				"CCM drift detection requires "+
					"the Olivares enterprise "+
					"add-on (compliancedepth); "+
					"not linked in this build"))
		return
	}
	snapshotIDStr := strings.TrimSpace(
		r.URL.Query().Get("snapshot_id"))

	var items []driftFindingDTO
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			snapRepo, err := sc.Ext(ccmSnapshotKind)
			if err != nil {
				return err
			}
			// Load the two snapshots to compare.
			var curr, prev *CCMSnapshot
			if snapshotIDStr != "" {
				sid, ok := idParam(snapshotIDStr)
				if !ok {
					return store.ErrNotFound
				}
				rec, err := snapRepo.Get(
					r.Context(), sid)
				if err != nil {
					return err
				}
				curr = recordToCCMSnapshot(rec)
				// Find the predecessor by listing
				// all and picking the one before.
				all, err := listAll(
					r.Context(), snapRepo)
				if err != nil {
					return err
				}
				prev = findPredecessor(
					all, rec.String(model.ColID))
			} else {
				all, err := listAll(
					r.Context(), snapRepo)
				if err != nil {
					return err
				}
				if len(all) < 2 {
					// 422, not 500: nothing failed. The
					// estate has one snapshot and drift
					// needs two. This handler used to hand
					// a BARE error to writeStoreError, which
					// has no case for it and returns
					// "internal error" — so a request the
					// caller could see was unsatisfiable
					// read as a broken server. Wrapping is
					// half the fix; the other half is the
					// writer swap at the bottom of this
					// handler.
					return errDepthRejected{errStr(
						"drift detection requires " +
							"at least two CCM " +
							"snapshots")}
				}
				curr = recordToCCMSnapshot(
					all[len(all)-1])
				prev = recordToCCMSnapshot(
					all[len(all)-2])
			}
			if prev == nil {
				// Same reasoning: the OLDEST snapshot has no
				// predecessor by construction, so pinning it
				// is an unsatisfiable request, not a fault.
				return errDepthRejected{errStr(
					"no predecessor snapshot " +
						"found for drift " +
						"comparison")}
			}

			findings, err :=
				m.depthPackager.DetectDrift(
					r.Context(), prev, curr)
			if err != nil {
				return err
			}

			driftRepo, err := sc.Ext(ccmDriftKind)
			if err != nil {
				return err
			}
			now := m.clock.Now()
			for _, f := range findings {
				fields := map[string]any{
					colCDSnapshotRef: nonEmpty(
						snapshotIDStr, "latest"),
					colCDFrameworkID: clamp(
						f.FrameworkID,
						maxRefLen),
					colCDControlID: clamp(
						f.ControlID, maxRefLen),
					colCDTitle: clamp(
						f.Title, maxNameLen),
					colCDPrevStatus: clamp(
						f.PrevStatus, maxRefLen),
					colCDCurrStatus: clamp(
						f.CurrStatus, maxRefLen),
					colCDDirection: clamp(
						f.Direction, maxRefLen),
					colCDDetail: nullableText(
						clamp(f.Detail,
							maxNoteLen)),
					colCDDetectedAt: now.String(),
				}
				saved, err := driftRepo.Create(
					r.Context(),
					model.Record(fields))
				if err != nil {
					return err
				}
				items = append(items,
					recordToDriftFindingDTO(saved))
			}
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.ccm.drift",
				ccmDriftKind,
				"",
				map[string]any{
					"findings": len(findings),
				})
		})
	if err != nil {
		// writeDepthError, not writeStoreError: the two "this comparison cannot
		// be made" refusals above are errDepthRejected and map to 422 (:64-73).
		// Everything else — including the unresolvable snapshot_id, which is
		// store.ErrNotFound → 404 — falls through to writeStoreError exactly as
		// before, because that is the last thing writeDepthError does.
		writeDepthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated,
		listResponse[driftFindingDTO]{Items: items})
}

func (m *Module) handleListDriftFindings(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	var items []driftFindingDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(ccmDriftKind)
			if err != nil {
				return err
			}
			recs, lerr := listAll(r.Context(), repo)
			for _, rec := range recs {
				items = append(items,
					recordToDriftFindingDTO(rec))
			}
			return lerr
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		listResponse[driftFindingDTO]{Items: items})
}

// ========================================================================
// Group 5: FedRAMP KSIs
// ========================================================================

// handleGenerateFedRAMPKSIs structures the live assessment into
// FedRAMP 20x Key Security Indicators in OSCAL v1.1.3 format with
// DoD impact-level framing (deny-closed: 501 without the depth
// packager, 422 on reject). Persists (one per tenant, replace-on-
// regenerate) anchored to the ledger head. Self-audits.
func (m *Module) handleGenerateFedRAMPKSIs(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	if m.depthPackager == nil {
		writeJSON(w, http.StatusNotImplemented,
			errorBody(
				"FedRAMP 20x KSI generation "+
					"requires the Olivares "+
					"enterprise add-on "+
					"(compliancedepth); "+
					"not linked in this build"))
		return
	}
	doc, ok := readBoundedBody(
		w, r, "FedRAMP authorization context")
	if !ok {
		return
	}
	impactLevel := clamp(
		strings.TrimSpace(
			r.URL.Query().Get("impact_level")),
		maxRefLen)
	scopeNote := clamp(
		strings.TrimSpace(
			r.URL.Query().Get("scope_note")),
		maxNoteLen)

	// Gather evidence from frameworks relevant to
	// FedRAMP (NIST 800-53 mapped via existing
	// catalog). Use all catalog frameworks so the
	// packager can crosswalk freely.
	var allFwIDs []string
	for id := range frameworkByID {
		allFwIDs = append(allFwIDs, id)
	}

	var dto fedRAMPKSIDTO
	docSHA := hashHex(string(doc))
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			assessments, err := gatherMultiAssessments(
				r, sc, allFwIDs)
			if err != nil {
				return err
			}
			built, err :=
				m.depthPackager.BuildFedRAMPKSIs(
					r.Context(),
					FedRAMPKSIInput{
						Document:    doc,
						ImpactLevel: impactLevel,
						ScopeNote:   scopeNote,
					},
					assessments)
			if err != nil {
				return errFedRAMPRejected{err}
			}
			if built == nil ||
				strings.TrimSpace(
					built.SystemName) == "" {
				return errFedRAMPRejected{
					errStr("system name " +
						"is required")}
			}

			head, headOK, err :=
				sc.Audit().Head(r.Context())
			if err != nil {
				return err
			}
			now := m.clock.Now()
			fields := map[string]any{
				colFKSystemName: clamp(
					built.SystemName,
					maxNameLen),
				colFKImpactLevel: clamp(
					nonEmpty(
						built.ImpactLevel,
						"IL2"),
					maxRefLen),
				colFKKSIs: encodeJSON(built.KSIs),
				colFKOscalVer: clamp(
					nonEmpty(
						built.OscalVersion,
						"1.1.3"),
					maxRefLen),
				colFKAuthPkg: encodeJSON(
					built.AuthorizationPackage),
				colFKValidation: encodeJSON(
					built.Validation),
				colFKNote: nullableText(
					clamp(built.Note, maxNoteLen)),
				colFKDocSHA: docSHA,
				colFKScopeNote: nullableText(
					clamp(scopeNote, maxNoteLen)),
				colGeneratedBy: mc.Principal.Actor(),
				colGeneratedAt: now.String(),
				colLedgerSeq:   head.Seq,
				colLedgerHash: nullableText(
					ledgerHashHex(head, headOK)),
			}
			repo, err := sc.Ext(fedRAMPKSIKind)
			if err != nil {
				return err
			}
			existing, err := listAll(r.Context(), repo)
			if err != nil {
				return err
			}
			var saved model.Record
			if len(existing) > 0 {
				rec := existing[0]
				for k, v := range fields {
					rec[k] = v
				}
				saved, err = repo.Update(
					r.Context(), rec)
			} else {
				saved, err = repo.Create(
					r.Context(),
					model.Record(fields))
			}
			if err != nil {
				return err
			}
			dto = recordToFedRAMPKSIDTO(saved, true)
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.fedramp.generate",
				fedRAMPKSIKind,
				model.ID(
					saved.String(model.ColID)),
				map[string]any{
					"system_name": built.SystemName,
					"impact_level": nonEmpty(
						built.ImpactLevel, "IL2"),
					"errors": countDepthErrors(
						built.Validation),
					"doc_sha256": docSHA,
				})
		})
	if err != nil {
		writeFedRAMPError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListFedRAMPKSIs(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	var items []fedRAMPKSIDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(fedRAMPKSIKind)
			if err != nil {
				return err
			}
			recs, lerr := listAll(r.Context(), repo)
			for _, rec := range recs {
				items = append(items,
					recordToFedRAMPKSIDTO(
						rec, false))
			}
			return lerr
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK,
		listResponse[fedRAMPKSIDTO]{Items: items})
}

func (m *Module) handleGetFedRAMPKSI(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	var dto fedRAMPKSIDTO
	err := mc.Data.View(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(fedRAMPKSIKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			dto = recordToFedRAMPKSIDTO(rec, true)
			return nil
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleExportFedRAMPKSI exports the maintained FedRAMP KSI document
// with a LIVE ledger integrity proof. Self-audits.
func (m *Module) handleExportFedRAMPKSI(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	var dto fedRAMPKSIDTO
	var anchor map[string]any
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(fedRAMPKSIKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			dto = recordToFedRAMPKSIDTO(rec, true)
			anchor, err = liveLedgerAnchor(
				r.Context(), sc,
				rec.Int(colLedgerSeq),
				rec.String(colLedgerHash))
			if err != nil {
				return err
			}
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.fedramp.export",
				fedRAMPKSIKind, id,
				map[string]any{
					"system_name": rec.String(
						colFKSystemName),
				})
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	dto.LedgerAnchor = anchor
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteFedRAMPKSI(
	w http.ResponseWriter,
	r *http.Request,
	mc api.ModuleContext,
) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(
		r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(fedRAMPKSIKind)
			if err != nil {
				return err
			}
			if _, err := repo.Get(
				r.Context(), id); err != nil {
				return err
			}
			if err := repo.Delete(
				r.Context(), id); err != nil {
				return err
			}
			return auditEvent(
				r.Context(), sc, mc,
				"compliance.depth.fedramp.delete",
				fedRAMPKSIKind, id,
				map[string]any{
					"system_name": "",
				})
		})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ========================================================================
// Shared pack-structuring helpers
// ========================================================================

// usStatePackSections flattens a USStatePack into a single
// map[string]any for persisting in the colDPSections JSON column.
func usStatePackSections(
	p *USStatePack,
) map[string]any {
	out := map[string]any{
		"crosswalk_nist": p.CrosswalkNIST,
	}
	jurisdictions := make([]map[string]any, 0,
		len(p.Jurisdictions))
	for _, j := range p.Jurisdictions {
		jurisdictions = append(jurisdictions,
			map[string]any{
				"framework_id":        j.FrameworkID,
				"law_name":            j.LawName,
				"obligation_map":      j.ObligationMap,
				"notice_templates":    j.NoticeTemplates,
				"recordkeeping":       j.RecordkeepingInventory,
				"impact_assessment":   j.ImpactAssessment,
				"affirmative_defense": j.AffirmativeDefense,
			})
	}
	out["jurisdictions"] = jurisdictions
	return out
}

// jurisdictionNames extracts the law names from jurisdiction results.
func jurisdictionNames(
	jrs []JurisdictionResult,
) []string {
	out := make([]string, 0, len(jrs))
	for _, j := range jrs {
		out = append(out, j.LawName)
	}
	return out
}

// sectorPackSections flattens a SectorPack into a single map for
// persisting in the colDPSections JSON column.
func sectorPackSections(p *SectorPack) map[string]any {
	sectors := make([]map[string]any, 0,
		len(p.Sectors))
	for _, s := range p.Sectors {
		sectors = append(sectors, map[string]any{
			"framework_id":    s.FrameworkID,
			"sector_name":     s.SectorName,
			"control_mapping": s.ControlMapping,
			"recordkeeping":   s.RecordkeepingInventory,
			"gap_analysis":    s.GapAnalysis,
		})
	}
	return map[string]any{"sectors": sectors}
}

// sectorNames extracts the sector names from sector results.
func sectorNames(srs []SectorResult) []string {
	out := make([]string, 0, len(srs))
	for _, s := range srs {
		out = append(out, s.SectorName)
	}
	return out
}

// recordToCCMSnapshot reconstructs a CCMSnapshot from a persisted
// record (used by drift detection to feed the packager).
func recordToCCMSnapshot(
	rec model.Record,
) *CCMSnapshot {
	var frameworks []CCMFrameworkSnapshot
	var summary CCMSummary
	_ = jsonUnmarshal(
		rec.String(colCSFrameworks), &frameworks)
	_ = jsonUnmarshal(
		rec.String(colCSSummary), &summary)
	return &CCMSnapshot{
		SnapshotAt: rec.String(colCSSnapshotAt),
		Frameworks: frameworks,
		Summary:    summary,
		Note:       rec.String(colCSNote),
	}
}

// findPredecessor returns the snapshot immediately before targetID in
// the chronological list, or nil if targetID is the first or not
// found.
func findPredecessor(
	all []model.Record, targetID string,
) *CCMSnapshot {
	for i, rec := range all {
		if rec.String(model.ColID) == targetID &&
			i > 0 {
			return recordToCCMSnapshot(all[i-1])
		}
	}
	return nil
}
