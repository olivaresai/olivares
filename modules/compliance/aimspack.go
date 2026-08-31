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
// No rug-pull (LICENSING.md): the open catálogo iso_42001 (frameworks.go), the evidence
// engine (evidence.go/assess.go), the risk classifier (risk.go), and the crosswalk
// frameworks (nist_ai_600_1, csa_maestro, owasp_agentic_tm, owasp_agentic_top10) are ALL
// unchanged and stay open. Without a wired packager the new AIMS endpoints answer 501;
// the default binary is byte-identical.
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
// AIMS endpoints answer 501 and the open catalog/evidence/risk surfaces keep their
// behavior. The real implementation is enterprise/iso42001, wired only under -tags
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
	// ⛔ EL NOMBRE GO Y EL DEL CABLE DIVERGEN A PROPÓSITO, y esto existe para que nadie
	//    «arregle» el segundo. El identificador era `OrganisationName` —ortografía británica
	//    contra el `locale: US` que `.golangci.yml` fija, y contra los seis «organization» que
	//    este mismo fichero escribe en sus comentarios—, así que renombrarlo es interno y gratis.
	//
	//    **La etiqueta JSON `organisation_name` NO se toca.** Medido el 2026-08-19: no está en
	//    `web/openapi/openapi.json` (0 apariciones), pero **la consola SÍ la lee**
	//    (`web/src/features/compliance/regops-view.tsx:1222` y su celda) y aparece en una spec
	//    publicada (`docs/superpowers/specs/…nis2-mapping-iso42001-wizard-design.md:101`).
	//    Cambiarla es un cambio COORDINADO de dos mitades, no un barrido de ortografía: pertenece
	//    al trabajo de la superficie de cumplimiento, con las dos partes en el mismo commit.
	//
	// ⛔⛔ Y «RENOMBRARLO ES INTERNO Y GRATIS» ERA FALSO. Medido el 2026-08-27 en la CI
	//    del overlay enterprise, que es el consumidor que este bloque no miró:
	//
	//        enterprise/iso42001/packager.go:112:3:
	//          unknown field OrganisationName in struct literal of type compliance.AIMSDocument
	//
	//    Este campo lo escribe OTRO REPOSITORIO (el overlay enterprise, en un repositorio PRIVADO aparte),
	//    que consume este árbol por un submódulo PINEADO. En el pin de hoy (`25d9478e9`) el
	//    campo todavía se llama `OrganisationName`, y en `main` se llama `OrganizationName`
	//    desde `e41c46d68`. ⇒ el overlay **no puede compilar contra los dos a la vez**: arreglar
	//    su lado lo rompe contra su propio pin, y dejarlo lo rompe contra `main`. Sale rojo en
	//    `hub-sha-verify`, que es un job que nadie lee porque no está entre los requeridos.
	//
	//    Lo que estaba mal NO es el renombrado —el `locale: US` es la regla del proyecto y la
	//    etiqueta JSON se congeló bien—: es el ALCANCE. «Interno» se midió dentro de este
	//    repositorio, y un identificador EXPORTADO no termina en el borde del repositorio.
	//    ⇒ un barrido de ortografía congela las cadenas que salen del proceso Y los
	//    identificadores exportados que consume otro árbol; y si hay que romperlos, se rompen
	//    con el consumidor en el mismo movimiento (aquí: junto al re-pin, C02-01).
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

// aimsPackDisclaimer is the honesty banner every AIMS pack carries (docs/SECURITY-HARDENING.md).
const aimsPackDisclaimer = "ISO/IEC 42001:2023 certification-readiness pack structured from " +
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
		Disclaimer:       aimsPackDisclaimer,
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
				"the Olivares enterprise add-on (iso42001); not linked in this build"))
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
		"disclaimer":          aimsPackDisclaimer,
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
