// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file produces the EXPORTABLE AUDIT EVIDENCE (docs/SECURITY-HARDENING.md,§9): a sealed,
// append-only evidence PACKAGE derived from the ledger. The package records the chain
// head (seq+hash) and the LIVE hash-chain verify result, so it proves the evidence it
// references was not altered. It REFERENCES the ledger; it never copies or
// reimplements it. Sealing is privileged + audited; reading/exporting a sealed package
// is a sensitive evidence read that self-audits (docs/SECURITY-HARDENING.md). The continuous WORM/SIEM
// feed stays the core's — this links to it, the dataset re-verifies offline.

// evidencePackageDTO is the sealed package as returned to a caller.
type evidencePackageDTO struct {
	ID               string            `json:"id"`
	Framework        string            `json:"framework"`
	FrameworkVersion string            `json:"framework_version"`
	GeneratedAt      string            `json:"generated_at"`
	GeneratedBy      string            `json:"generated_by"`
	LedgerSeq        int64             `json:"ledger_seq"`
	LedgerHash       string            `json:"ledger_hash,omitempty"`
	IntegrityOK      bool              `json:"integrity_ok"`
	IntegrityChecked int64             `json:"integrity_checked"`
	IntegrityReason  string            `json:"integrity_reason,omitempty"`
	Summary          AssessmentSummary `json:"summary"`
	ManifestHash     string            `json:"manifest_hash"`
	ScopeNote        string            `json:"scope_note,omitempty"`
	Disclaimer       string            `json:"disclaimer"`
}

// sealRequest is the body for sealing a package (everything optional).
type sealRequest struct {
	ScopeNote string `json:"scope_note"`
}

// handleSealEvidence seals an immutable evidence package for a framework: it assesses
// the framework against the tenant's live evidence, records the ledger head + the live
// hash-chain verify result (the integrity proof), persists the package and its
// per-control results (append-only), and self-audits the seal — all in one transaction.
func (m *Module) handleSealEvidence(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	fw, ok := frameworkByID[strings.TrimSpace(chi.URLParam(r, "id"))]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody("unknown framework"))
		return
	}
	var req sealRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	scopeNote := clamp(strings.TrimSpace(req.ScopeNote), maxNoteLen)

	var dto evidencePackageDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		s, err := gatherEvidence(r.Context(), sc)
		if err != nil {
			return err
		}
		fa := assessFramework(fw, evaluateCapabilities(s))
		manifest := manifestHash(fa)
		now := m.clock.Now()
		ledgerHash := ""
		if s.auditHeadOK {
			ledgerHash = hex.EncodeToString(s.auditHead.Hash)
		}

		repo, err := sc.Ext(packageKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colFramework:    fw.ID,
			colFrameworkVer: fw.Version,
			colGeneratedAt:  now.String(),
			colGeneratedBy:  mc.Principal.Actor(),
			colLedgerSeq:    s.auditHead.Seq,
			colLedgerHash:   nullableText(ledgerHash),
			colIntegrityOK:  s.auditHeadOK && s.auditVerify.OK,
			colIntegrityN:   s.auditVerify.Checked,
			colIntegrityWhy: nullableText(s.auditVerify.Reason),
			colCtrlTotal:    int64(fa.Summary.Total),
			colSatisfied:    int64(fa.Summary.Satisfied),
			colPartial:      int64(fa.Summary.Partial),
			colGap:          int64(fa.Summary.Gap),
			colUnmapped:     int64(fa.Summary.Unmapped),
			colManifestHash: manifest,
			colScopeNote:    nullableText(scopeNote),
		}
		out, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		pkgID := model.ID(out.String(model.ColID))

		resRepo, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		for _, ca := range fa.Controls {
			if _, err := resRepo.Create(r.Context(), model.Record{
				colPackageRef: pkgID.String(),
				colFramework:  fw.ID,
				colControlID:  ca.ControlID,
				colTitle:      clamp(ca.Title, maxNameLen),
				colStatus:     string(ca.Status),
				colEvSummary:  nullableText(controlSummaryLine(ca)),
				colCaps:       encodeJSON(ca.Capabilities),
				colOccurredAt: now.String(),
			}); err != nil {
				return err
			}
		}

		// Self-audit the seal itself (docs/SECURITY-HARDENING.md) — the NEXT chain event after the head
		// this package attests.
		if err := auditEvent(r.Context(), sc, mc, "compliance.evidence.seal", packageKind, pkgID, map[string]any{
			"framework":    fw.ID,
			"controls":     fa.Summary.Total,
			"satisfied":    fa.Summary.Satisfied,
			"by_design":    fa.Summary.ByDesign,
			"gap":          fa.Summary.Gap,
			"integrity_ok": s.auditHeadOK && s.auditVerify.OK,
			"ledger_seq":   s.auditHead.Seq,
		}); err != nil {
			return err
		}

		out, err = repo.Get(r.Context(), pkgID)
		if err != nil {
			return err
		}
		dto = recordToPackageDTO(out)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListEvidence(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if fwID := strings.TrimSpace(r.URL.Query().Get("framework")); fwID != "" {
		filters = append(filters, eq(colFramework, fwID))
	}
	var items []evidencePackageDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(packageKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, filters...)
		for _, rec := range recs {
			items = append(items, recordToPackageDTO(rec))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[evidencePackageDTO]{Items: items})
}

// handleGetEvidence returns a sealed package + its per-control results. It is a
// SENSITIVE evidence read, so it self-audits in a committed transaction.
func (m *Module) handleGetEvidence(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto evidencePackageDTO
	var results []controlResultDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		pkgRepo, err := sc.Ext(packageKind)
		if err != nil {
			return err
		}
		rec, err := pkgRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToPackageDTO(rec)
		results, err = readControlResults(r.Context(), sc, id)
		if err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "compliance.evidence.read", packageKind, id, map[string]any{"framework": dto.Framework})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"package": dto, "controls": results})
}

// handleExportEvidence exports a sealed package as an auditor-consumable dataset +
// manifest. ?format=csv flattens the control results; json (default) is the full
// package + manifest + integrity proof. It self-audits the export (docs/SECURITY-HARDENING.md).
func (m *Module) handleExportEvidence(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto evidencePackageDTO
	var results []controlResultDTO
	var profile *ProfileRef
	var oscalDoc map[string]any // pre-rendered for the oscal format, so the self-audit reflects it
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		pkgRepo, err := sc.Ext(packageKind)
		if err != nil {
			return err
		}
		rec, err := pkgRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToPackageDTO(rec)
		results, err = readControlResults(r.Context(), sc, id)
		if err != nil {
			return err
		}
		// if an operator registered an OSCAL profile/SSP for this framework, scope
		// the OSCAL export to its selection. A read failure degrades to include-all (the
		// SAFE direction — never silently hide a control) and must not fail the export.
		if pr, perr := activeProfileRef(r.Context(), sc, dto.Framework); perr == nil {
			profile = pr
		} else {
			m.debugf("compliance: oscal export profile read failed; exporting include-all", "err", perr)
		}
		meta := map[string]any{"framework": dto.Framework, "format": exportFormat(r)}
		// Only the OSCAL view is actually scoped to the profile; CSV/JSON return the full
		// sealed set. Stamp the scoping back-reference ONLY when the bytes are scoped, so
		// the self-audit row never claims a scope the returned artifact does not have.
		if profile != nil && exportFormat(r) == "oscal" {
			meta["oscal_profile_sha256"] = profile.DocSHA256
			meta["oscal_selected"] = len(profile.SelectedIDs)
		}
		// render the OSCAL bundle HERE (it is pure over the rows just read) so the
		// self-audit reflects what is actually emitted. A POA&M is attached only when the
		// enterprise builder is wired AND there are open (not-satisfied) controls to plan, so
		// meta["oscal_poam"] is stamped from the REAL attach outcome — never a "wired" guess
		// (the invariant: the audit row never claims more than the artifact carries).
		if exportFormat(r) == "oscal" {
			fwName := dto.Framework
			if fw, ok := frameworkByID[dto.Framework]; ok {
				fwName = fw.Name
			}
			rendered := results
			if profile != nil {
				rendered = filterResultsBySelection(results, profile.SelectedIDs)
			}
			oscalDoc = oscalDocument(dto, rendered, fwName, profile)
			if m.attachPOAM(oscalDoc, dto, rendered, fwName, profile) {
				meta["oscal_poam"] = true
			}
		}
		return auditEvent(r.Context(), sc, mc, "compliance.evidence.export", packageKind, id, meta)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	switch exportFormat(r) {
	case "csv":
		writeCSV(w, evidenceCSV(dto, results))
		return
	case "oscal":
		// OSCAL component-definition + assessment-results + control-mapping (+ a FedRAMP-adjacent
		// POA&M when the enterprise builder is wired and there are open controls), anchored to the
		// ledger (FIN-10). The bundle was rendered INSIDE the tx above (so the self-audit reflects
		// it); profile == nil + no builder keeps the three-model export byte-identical.
		writeJSON(w, http.StatusOK, oscalDoc)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"package":    dto,
		"controls":   results,
		"manifest":   evidenceManifest(dto),
		"disclaimer": reportDisclaimer,
	})
}

// controlResultDTO is one sealed control result.
type controlResultDTO struct {
	ControlID    string               `json:"control_id"`
	Framework    string               `json:"framework"`
	Title        string               `json:"title"`
	Status       string               `json:"status"`
	Summary      string               `json:"evidence_summary,omitempty"`
	Capabilities []CapabilityEvidence `json:"capabilities,omitempty"`
}

func readControlResults(ctx context.Context, sc store.Scope, pkgID model.ID) ([]controlResultDTO, error) {
	repo, err := sc.Ext(resultKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo, eq(colPackageRef, pkgID.String()))
	if err != nil {
		return nil, err
	}
	out := make([]controlResultDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, controlResultDTO{
			ControlID:    rec.String(colControlID),
			Framework:    rec.String(colFramework),
			Title:        rec.String(colTitle),
			Status:       rec.String(colStatus),
			Summary:      rec.String(colEvSummary),
			Capabilities: decodeCaps(rec.String(colCaps)),
		})
	}
	return out, nil
}

// recordToPackageDTO maps a stored package record to its DTO.
func recordToPackageDTO(rec model.Record) evidencePackageDTO {
	return evidencePackageDTO{
		ID:               rec.String(model.ColID),
		Framework:        rec.String(colFramework),
		FrameworkVersion: rec.String(colFrameworkVer),
		GeneratedAt:      rec.String(colGeneratedAt),
		GeneratedBy:      rec.String(colGeneratedBy),
		LedgerSeq:        rec.Int(colLedgerSeq),
		LedgerHash:       rec.String(colLedgerHash),
		IntegrityOK:      rec.Bool(colIntegrityOK),
		IntegrityChecked: rec.Int(colIntegrityN),
		IntegrityReason:  rec.String(colIntegrityWhy),
		Summary: AssessmentSummary{
			Total:     int(rec.Int(colCtrlTotal)),
			Satisfied: int(rec.Int(colSatisfied)),
			Partial:   int(rec.Int(colPartial)),
			Gap:       int(rec.Int(colGap)),
			Unmapped:  int(rec.Int(colUnmapped)),
		},
		ManifestHash: rec.String(colManifestHash),
		ScopeNote:    rec.String(colScopeNote),
		Disclaimer:   reportDisclaimer,
	}
}

// manifestHash is the canonical, order-stable hash of an assessment body so the
// package is tamper-evident independently of the ledger: a change to any control's
// status or any capability's evidence state changes the hash.
func manifestHash(fa FrameworkAssessment) string {
	var b strings.Builder
	b.WriteString(fa.Framework)
	b.WriteByte('|')
	b.WriteString(fa.Version)
	for _, ca := range fa.Controls {
		b.WriteByte('\n')
		b.WriteString(ca.ControlID)
		b.WriteByte('=')
		b.WriteString(string(ca.Status))
		for _, ev := range ca.Capabilities {
			b.WriteByte(';')
			b.WriteString(string(ev.Key))
			b.WriteByte(':')
			b.WriteString(string(ev.State))
		}
	}
	return hashHex(b.String())
}

// evidenceManifest is the verification manifest an auditor uses to re-check the
// package offline: the ledger anchor + integrity result + the body hash.
func evidenceManifest(dto evidencePackageDTO) map[string]any {
	return map[string]any{
		"ledger_seq":        dto.LedgerSeq,
		"ledger_hash":       dto.LedgerHash,
		"integrity_ok":      dto.IntegrityOK,
		"integrity_checked": dto.IntegrityChecked,
		"integrity_reason":  dto.IntegrityReason,
		"manifest_hash":     dto.ManifestHash,
		"verify_endpoint":   "/v1/audit/verify",
		"export_endpoint":   "/v1/audit/export",
		"note":              "Re-verify by checking integrity_ok against GET /v1/audit/verify and the WORM/SIEM export; the chain hash anchors this package to the ledger.",
	}
}

func evidenceCSV(dto evidencePackageDTO, results []controlResultDTO) string {
	var b strings.Builder
	b.WriteString("# evidence package " + dto.ID + " framework=" + dto.Framework + " integrity_ok=" + strconv.FormatBool(dto.IntegrityOK) + " ledger_seq=" + strconv.FormatInt(dto.LedgerSeq, 10) + "\n")
	b.WriteString("control_id,status,title,evidence_summary\n")
	for _, c := range results {
		b.WriteString(csvField(c.ControlID))
		b.WriteByte(',')
		b.WriteString(csvField(c.Status))
		b.WriteByte(',')
		b.WriteString(csvField(c.Title))
		b.WriteByte(',')
		b.WriteString(csvField(c.Summary))
		b.WriteByte('\n')
	}
	return b.String()
}

// controlSummaryLine is the short evidence summary stored on a control result.
func controlSummaryLine(ca ControlAssessment) string {
	present := 0
	for _, ev := range ca.Capabilities {
		if ev.State == EvidencePresent {
			present++
		}
	}
	line := fmt.Sprintf("%s: %d/%d capabilities present", ca.Status, present, len(ca.Capabilities))
	if miss := missingCapabilities(ca); len(miss) > 0 {
		parts := make([]string, len(miss))
		for i, k := range miss {
			parts[i] = string(k)
		}
		line += "; missing: " + strings.Join(parts, ",")
	}
	return clamp(line, maxNoteLen)
}

func exportFormat(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "csv":
		return "csv"
	case "oscal":
		return "oscal"
	default:
		return "json"
	}
}

// nullableText returns nil for an empty string so a nullable column is stored NULL.
func nullableText(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
