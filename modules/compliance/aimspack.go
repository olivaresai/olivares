// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the OPEN-CORE half of ISO/IEC 42001 AIMS cert-readiness seam: the
// interface the module consumes so a commercial add-on can structure the live assessment +
// operator-supplied context into the FORMAL artifacts of ISO/IEC 42001:2023 certification
// readiness — Statement of Applicability, AI policy, AI risk register, impact assessments,
// lifecycle-control mapping and supplier governance. The VALUE (the structuring into the
// 42001 templates, the SoA derivation, the crosswalk mapping) lives in the commercial
// add-on enterprise/iso42001, wired ONLY under -tags enterprise (the RegulatoryPackager
// / ProfileResolver pattern). The open binary never links it.
//
// Framework catalogs and live assessments are Business Compliance Packs.
// Stored-evidence reads, JSON/CSV export and operational risk remain shared.
// Without a wired AIMS packager its endpoints answer 501; the interface and
// persisted records remain available in the shared module.
//
// Honesty (docs/SECURITY-HARDENING.md): the add-on automates evidence gathering and report structuring;
// it does NOT make the organization ISO/IEC 42001 conformant and is NOT a certification
// or statement of conformity. Every emitted artifact (SoA, policy, risk register, impact
// assessment) is PROVISIONAL and requires human attestation before submission to a
// certification body or a procurement buyer. An honest gap (unmapped/gap capability) is
// exported as a gap, never as satisfied; satisfied never rests on architectural evidence
// alone (assess.go:15).

// AIMSPackager is the closed seam for ISO/IEC 42001 AIMS certification-readiness depth on
// top of the open compliance substrate. The default is nil — without a wired packager the
// AIMS endpoints answer 501. Stored evidence and risk remain shared; framework
// assessments belong to Business Compliance Packs. The implementation is enterprise/iso42001, wired under -tags
// enterprise.
type AIMSPackager interface {
	// BuildAIMSPack structures operator-supplied organizational context + the live ISO
	// 42001 assessment into a certification-readiness pack: Statement of Applicability
	// (Annex A), AI policy (clauses 4–10), AI risk register (clause 6.1 + Annex A.5.2),
	// impact assessments (Annex A.5.2/A.5.4), lifecycle-control mapping (Annex A.6.2.x)
	// and supplier/AI-component governance (Annex A.10.3/A.7.5). It MUST be deny-closed:
	// invalid input or a pack with no organization name is an error — never a silent
	// partial. The returned document is a DRAFT: it never asserts the organization
	// conforms to ISO/IEC 42001.
	BuildAIMSPack(ctx context.Context, in AIMSInput, assessment FrameworkAssessment, risks []RiskDTO) (*AIMSDocument, error)
}

// AIMSInput is the operator-supplied organizational context for an ISO/IEC 42001
// certification-readiness pack. The compliance substrate (the live assessment,
// capabilities, risk classifications) is passed alongside — the operator provides
// the scope and organizational commitments, the platform provides the evidence.
type AIMSInput struct {
	// Document is the raw operator-supplied organizational context (JSON: organization
	// name, scope boundaries, AI policy commitments, interested parties, management
	// review schedule, applicable controls selection). The packager parses, validates
	// and structures it; the operator's bytes are hashed for the minimal-data anchor
	// (SHA-256), never re-published elsewhere.
	Document []byte

	// ScopeNote is an operator-supplied free-text note for the pack scope.
	ScopeNote string
}

// AIMSDocument is the structured certification-readiness pack the closed add-on returns.
// Each section maps to a deliverable an organization prepares for ISO/IEC 42001
// certification readiness.
type AIMSDocument struct {
	// OrganizationName identifies the entity this pack is prepared for.
	//
	// The Go name follows locale: US in .golangci.yml; the published JSON key
	// organisation_name stays unchanged. The 2026-08-19 measurement found no occurrence
	// in web/openapi/openapi.json, but the Business console's regulatory operations
	// panel read the key and the published
	// docs/superpowers/specs/…nis2-mapping-iso42001-wizard-design.md:101 specified it.
	// Changing the wire key requires coordinated producer and consumer changes.
	//
	// The exported Go field also has an external consumer. Measured the
	// enterprise overlay's CI failure on 2026-08-27:
	//
	//     enterprise/iso42001/packager.go:112:3:
	//       unknown field OrganisationName in struct literal of type compliance.AIMSDocument
	//
	// That separate private repository consumed this tree through a pinned submodule.
	// Its pin 25d9478e9 still used OrganisationName; main used OrganizationName since
	// e41c46d68. One consumer spelling could not compile against both. hub-sha-verify
	// reported the failure outside the required jobs.
	// A spelling cleanup must preserve wire strings AND exported names consumed by
	// other trees, or coordinate the change with the consumer's re-pin (C02-01).
	OrganizationName string

	// Standard is the standard identifier (always "ISO/IEC 42001:2023").
	Standard string

	// SoA is the Statement of Applicability — per-control (Annex A): applicable yes/no,
	// justification for inclusion/exclusion, implementation status derived from the live
	// controlStatus, and evidence reference. The central deliverable.
	SoA map[string]any

	// Policy is the structured AI policy (clauses 4–10 of the management system),
	// populated with what the platform evidences and marking organizational
	// responsibilities as explicit gaps.
	Policy map[string]any

	// RiskRegister is the AI risk register (clause 6.1 + Annex A.5.2), feeding from
	// risk.go per-agent classifications. Every entry is PROVISIONAL until attested.
	RiskRegister map[string]any

	// ImpactAssessments is the impact assessment structure (Annex A.5.2 process /
	// A.5.4 individuals), derived from existing risk classifications. Gaps where the
	// platform cannot measure (fairness/bias/societal) are explicit.
	ImpactAssessments map[string]any

	// LifecycleControls maps Annex A.6.2.x (deployment A.6.2.5, operation A.6.2.6,
	// V&V A.6.2.4, logging A.6.2.8) to the live evidence (change ledger, audit trail,
	// eval results, adversarial testing).
	LifecycleControls map[string]any

	// SupplierGovernance maps Annex A.10.3 (suppliers) and A.7.5 (data provenance /
	// AIBOM) to the platform's tracked supplier GPAI posture and sealed AIBOMs.
	SupplierGovernance map[string]any

	// Validation is the list of findings from the packager's deny-closed validation
	// (structural issues, missing required fields, honest gaps).
	Validation []AIMSIssue

	// Note is an optional operator/packager-supplied note.
	Note string
}

// AIMSIssue is a validation finding from the AIMS packager.
type AIMSIssue struct {
	Severity string `json:"severity"` // "error", "warning", "info"
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// aimsPackDisclaimer is snapshotted when a pack is generated (docs/SECURITY-HARDENING.md).
const aimsPackDisclaimer = "Draft ISO/IEC 42001:2023 certification-readiness pack based on " +
	"Olivares AI's current assessment and operator-supplied context. All artifacts are drafts; " +
	"a competent person must review them before submission to a certification body, auditor " +
	"or buyer. This pack provides neither certification nor conformity assurance nor legal " +
	"advice. Certification requires an accredited body (ISO/IEC 42006:2025). Control status " +
	"reflects current tenant evidence; gaps remain unsatisfied."

// Packs generated before snapshots were stored retain these exact historical bytes.
const legacyAIMSPackDisclaimer = "ISO/IEC 42001:2023 certification-readiness pack structured from " +
	"the control plane's live assessment and operator-supplied organizational context. " +
	"The control plane automates evidence gathering and report structuring; it does NOT " +
	"make the organization conformant to ISO/IEC 42001 and this is NOT a certification, " +
	"statement of conformity, or legal advice. Every artifact (Statement of " +
	"Applicability, AI policy, risk register, impact assessment) is a DRAFT that a " +
	"competent person must review before submission to a certification body, an auditor " +
	"or a procurement buyer. The certification itself is issued by an accredited " +
	"certification body (ISO/IEC 42006:2025). Control status is derived from live " +
	"tenant evidence; an honest gap is never asserted as satisfied."

// --- DTO -------------------------------------------------------------------------

type aimsPackDTO struct {
	ID                 string         `json:"id"`
	Standard           string         `json:"standard"`
	OrganizationName   string         `json:"organisation_name"`
	SoA                map[string]any `json:"soa,omitempty"`
	Policy             map[string]any `json:"policy,omitempty"`
	RiskRegister       map[string]any `json:"risk_register,omitempty"`
	ImpactAssessments  map[string]any `json:"impact_assessments,omitempty"`
	LifecycleControls  map[string]any `json:"lifecycle_controls,omitempty"`
	SupplierGovernance map[string]any `json:"supplier_governance,omitempty"`
	Validation         []AIMSIssue    `json:"validation,omitempty"`
	ErrorCount         int            `json:"error_count"`
	ScopeNote          string         `json:"scope_note,omitempty"`
	DocSHA256          string         `json:"doc_sha256"`
	GeneratedBy        string         `json:"generated_by"`
	GeneratedAt        string         `json:"generated_at"`
	LedgerAnchor       map[string]any `json:"ledger_anchor,omitempty"`
	Disclaimer         string         `json:"disclaimer"`
}

func recordToAIMSPackDTO(rec model.Record, includeBody bool) aimsPackDTO {
	var soa, policy, riskReg, impact, lifecycle, supplier map[string]any
	var validation []AIMSIssue
	_ = jsonUnmarshal(rec.String(colAPSoA), &soa)
	_ = jsonUnmarshal(rec.String(colAPPolicy), &policy)
	_ = jsonUnmarshal(rec.String(colAPRiskReg), &riskReg)
	_ = jsonUnmarshal(rec.String(colAPImpact), &impact)
	_ = jsonUnmarshal(rec.String(colAPLifecycle), &lifecycle)
	_ = jsonUnmarshal(rec.String(colAPSupplier), &supplier)
	_ = jsonUnmarshal(rec.String(colAPValidation), &validation)
	disclaimer := rec.String(colAPDisclaimer)
	if rec.IsNull(colAPDisclaimer) {
		disclaimer = legacyAIMSPackDisclaimer
	}
	dto := aimsPackDTO{
		ID:               rec.String(model.ColID),
		Standard:         rec.String(colAPStandard),
		OrganizationName: rec.String(colAPOrgName),
		Validation:       validation,
		ErrorCount:       countAIMSErrors(validation),
		ScopeNote:        rec.String(colAPScopeNote),
		DocSHA256:        rec.String(colAPDocSHA),
		GeneratedBy:      rec.String(colAPGeneratedBy),
		GeneratedAt:      rec.String(colAPGeneratedAt),
		Disclaimer:       disclaimer,
	}
	if includeBody {
		dto.SoA = soa
		dto.Policy = policy
		dto.RiskRegister = riskReg
		dto.ImpactAssessments = impact
		dto.LifecycleControls = lifecycle
		dto.SupplierGovernance = supplier
	}
	return dto
}

func countAIMSErrors(issues []AIMSIssue) int {
	n := 0
	for _, i := range issues {
		if i.Severity == "error" {
			n++
		}
	}
	return n
}

// --- handlers --------------------------------------------------------------------

// handleGenerateAIMSPack structures operator-supplied organizational context + the live
// ISO 42001 assessment into a certification-readiness pack (deny-closed: 501 without
// a configured packager, 422 on a document the packager rejects), persists the pack
// (one per tenant, replace-on-regenerate) anchored to the ledger head, and self-audits.
func (m *Module) handleGenerateAIMSPack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.aimsPackager == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody(
			"ISO/IEC 42001 AIMS certification-readiness pack generation requires "+
				"the Olivares Business edition (iso42001); not linked in this build"))
		return
	}
	doc, ok := readBoundedBody(w, r, "AIMS organizational context")
	if !ok {
		return
	}
	scopeNote := clamp(strings.TrimSpace(r.URL.Query().Get("scope_note")), maxNoteLen)

	var dto aimsPackDTO
	docSHA := hashHex(string(doc))
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// Gather the live ISO 42001 assessment.
		fw, fwOK := frameworkByID["iso_42001"]
		if !fwOK {
			return errors.New("iso_42001 framework not found in catalog")
		}
		s, err := gatherEvidence(r.Context(), sc)
		if err != nil {
			return err
		}
		caps := evaluateCapabilities(s)
		assessment := assessFramework(fw, caps)

		// Gather the live risk classifications for the AI risk register.
		risks, err := listAllRisks(r.Context(), sc)
		if err != nil {
			return err
		}

		built, err := m.aimsPackager.BuildAIMSPack(r.Context(), AIMSInput{
			Document:  doc,
			ScopeNote: scopeNote,
		}, assessment, risks)
		if err != nil {
			return errAIMSRejected{err}
		}
		if built == nil || strings.TrimSpace(built.OrganizationName) == "" {
			return errAIMSRejected{errNoOrganisation}
		}

		head, headOK, err := sc.Audit().Head(r.Context())
		if err != nil {
			return err
		}
		now := m.clock.Now()
		fields := map[string]any{
			colAPStandard:    clamp(nonEmpty(built.Standard, aimsStandard), maxRefLen),
			colAPOrgName:     clamp(built.OrganizationName, maxNameLen),
			colAPSoA:         encodeJSON(built.SoA),
			colAPPolicy:      encodeJSON(built.Policy),
			colAPRiskReg:     encodeJSON(built.RiskRegister),
			colAPImpact:      encodeJSON(built.ImpactAssessments),
			colAPLifecycle:   encodeJSON(built.LifecycleControls),
			colAPSupplier:    encodeJSON(built.SupplierGovernance),
			colAPValidation:  encodeJSON(built.Validation),
			colAPScopeNote:   nullableText(clamp(scopeNote, maxNoteLen)),
			colAPDocSHA:      docSHA,
			colAPGeneratedBy: mc.Principal.Actor(),
			colAPGeneratedAt: now.String(),
			colAPDisclaimer:  aimsPackDisclaimer,
			colLedgerSeq:     head.Seq,
			colLedgerHash:    nullableText(ledgerHashHex(head, headOK)),
		}
		repo, err := sc.Ext(aimsPackKind)
		if err != nil {
			return err
		}
		// One active pack per tenant — replace-on-regenerate.
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
			saved, err = repo.Update(r.Context(), rec)
		} else {
			saved, err = repo.Create(r.Context(), model.Record(fields))
		}
		if err != nil {
			return err
		}
		dto = recordToAIMSPackDTO(saved, true)
		return auditEvent(r.Context(), sc, mc, "compliance.aims.pack.generate", aimsPackKind, model.ID(saved.String(model.ColID)), map[string]any{
			"organisation": built.OrganizationName,
			"standard":     built.Standard,
			"errors":       countAIMSErrors(built.Validation),
			"doc_sha256":   docSHA,
		})
	})
	if err != nil {
		writeAIMSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListAIMSPacks(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var items []aimsPackDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(aimsPackKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo)
		for _, rec := range recs {
			items = append(items, recordToAIMSPackDTO(rec, false))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[aimsPackDTO]{Items: items})
}

func (m *Module) handleGetAIMSPack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto aimsPackDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(aimsPackKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToAIMSPackDTO(rec, true)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleExportAIMSPack exports the maintained AIMS pack with a LIVE ledger integrity
// proof (the regpackage.go/evidence-package pattern) so the export proves the pack was
// anchored to a tamper-evident ledger. Exporting a stored pack is a sensitive evidence
// read, so it self-audits in a committed transaction.
func (m *Module) handleExportAIMSPack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto aimsPackDTO
	var anchor map[string]any
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(aimsPackKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToAIMSPackDTO(rec, true)
		anchor, err = liveLedgerAnchor(r.Context(), sc, rec.Int(colLedgerSeq), rec.String(colLedgerHash))
		if err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "compliance.aims.pack.export", aimsPackKind, id, map[string]any{
			"organisation": rec.String(colAPOrgName),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at":        m.clock.Now().String(),
		"standard":            dto.Standard,
		"organisation_name":   dto.OrganizationName,
		"soa":                 dto.SoA,
		"policy":              dto.Policy,
		"risk_register":       dto.RiskRegister,
		"impact_assessments":  dto.ImpactAssessments,
		"lifecycle_controls":  dto.LifecycleControls,
		"supplier_governance": dto.SupplierGovernance,
		"validation":          dto.Validation,
		"error_count":         dto.ErrorCount,
		"scope_note":          dto.ScopeNote,
		"doc_sha256":          dto.DocSHA256,
		"ledger_anchor":       anchor,
		"disclaimer":          dto.Disclaimer,
	})
}

// handleDeleteAIMSPack removes a maintained pack; admin-tier and self-audited.
func (m *Module) handleDeleteAIMSPack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(aimsPackKind)
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
		return auditEvent(r.Context(), sc, mc, "compliance.aims.pack.delete", aimsPackKind, id, map[string]any{
			"organisation": rec.String(colAPOrgName),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- error handling --------------------------------------------------------------

const aimsStandard = "ISO/IEC 42001:2023"

var errNoOrganisation = errors.New("organization name is required")

type errAIMSRejected struct{ err error }

func (e errAIMSRejected) Error() string { return "AIMS pack rejected: " + e.err.Error() }
func (e errAIMSRejected) Unwrap() error { return e.err }

func writeAIMSError(w http.ResponseWriter, err error) {
	var rej errAIMSRejected
	if errors.As(err, &rej) {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("AIMS pack rejected: "+clamp(rej.err.Error(), maxNameLen)))
		return
	}
	writeStoreError(w, err)
}

// --- risk listing helper ---------------------------------------------------------

func listAllRisks(ctx context.Context, sc store.Scope) ([]RiskDTO, error) {
	repo, err := sc.Ext(riskKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]RiskDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, recordToRiskDTO(rec))
	}
	return out, nil
}
