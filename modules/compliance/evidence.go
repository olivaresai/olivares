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

// Stored evidence packages preserve the recorded framework, ledger anchor and
// per-control results across edition changes. Reads and JSON/CSV exports operate
// on those existing rows; new assessments and sealing belong to Business
// Compliance Packs.

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
	if exportFormat(r) == "oscal" {
		m.handleExportOSCAL(w, r, mc)
		return
	}

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
		meta := map[string]any{"framework": dto.Framework, "format": exportFormat(r)}
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

// The stored manifest hash anchors the package body independently of the ledger.
// Reads and portable exports retain that recorded hash rather than recomputing
// an assessment against a newer catalog or edition.

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
