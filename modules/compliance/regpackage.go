// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the OPEN-CORE half of named-regulation DEPTH seam: the interface the
// module consumes so a commercial add-on can structure operator-supplied data into the
// FORMAL templates of a named regulation and apply that regulation's classification logic,
// plus the governed persistence/export/audit substrate that drives it. The VALUE — the
// 2024/2956 Register-of-Information structuring, the DORA Art 18 major-incident
// classification (RTS (EU) 2024/1772) and the report drafting (RTS (EU) 2025/301) — lives
// in the commercial add-on enterprise/doraregister, wired ONLY under -tags enterprise (the
// ProfileResolver / oscalingest pattern). The open binary never links it.
//
// No rug-pull (LICENSING.md): the open dora.go "ICT-risk view" export (GET /dora) is
// UNCHANGED and stays open. Without a wired packager the new register/incident endpoints
// answer 501; the default binary is byte-identical. The seam interface and its value
// objects live here so both sides share them without the open module importing the closed
// add-on.
//
// Honesty (docs/SECURITY-HARDENING.md, SESSIONS-PLAN:114-116): the add-on HELPS COMPLY — it automates
// register population and report drafting from the control plane's AI/ICT telemetry; it
// does NOT make the tenant DORA-compliant and is NOT a certification. Every emitted
// verdict (register completeness, is-major, reporting deadline) is PROVISIONAL and requires
// human attestation before submission to a competent authority. An empty register is
// exported empty; a value the source could not verify is surfaced honestly, never faked.

// RegulatoryPackager is the closed seam for named-regulation depth on top of the open
// compliance substrate. The default is nil — without a wired packager the register/incident
// endpoints answer 501 and the open dora.go ICT-risk view keeps its behavior. The real
// implementation is enterprise/doraregister, wired only under -tags enterprise.
type RegulatoryPackager interface {
	// BuildDORARegister structures operator-supplied register data into the DORA Register of
	// Information (Commission Implementing Regulation (EU) 2024/2956 templates B_01.01..
	// B_07.01 + B_99.01, mandated by Regulation (EU) 2022/2554 Art 28(3)), validated and
	// reconciled against the control plane's known ICT third-party inventory (known). It MUST
	// be deny-closed: invalid input, or a register with no maintaining-entity LEI, is an error
	// — never a silent partial. The returned document is a draft: it never asserts the entity
	// is DORA-compliant.
	BuildDORARegister(ctx context.Context, in RegisterInput, known []KnownICTProvider) (*RegisterDocument, error)

	// ClassifyMajorIncident applies the DORA major-incident materiality criteria (Regulation
	// (EU) 2022/2554 Art 18 + RTS (EU) 2024/1772) to operator-supplied incident impact data
	// and drafts the report content + reporting deadlines (RTS (EU) 2025/301) when the
	// incident classifies as major. Deny-closed on invalid input. The major verdict is
	// provisional — the legal classification and the duty to report rest with the entity.
	ClassifyMajorIncident(ctx context.Context, in IncidentInput) (*IncidentClassification, error)
}

// RegisterInput is one operator-supplied DORA register submission.
type RegisterInput struct {
	// Document is the raw operator-supplied register data (JSON: maintaining entity, entities
	// in scope, contractual arrangements, ICT third-party providers, functions, assessments).
	// The packager parses+validates+structures it; the operator's bytes are hashed for the
	// minimal-data anchor (the SHA-256), never re-published elsewhere.
	Document []byte
	// ReferenceDate is the optional reporting reference date hint (ISO 8601 yyyy-mm-dd). When
	// empty the packager derives it from the document (deny-closed if neither is present).
	ReferenceDate string
}

// KnownICTProvider is one ICT/AI third-party provider the control plane ALREADY tracks —
// the open module's reconciliation bridge to the closed packager (the differentiator: the
// packager flags providers declared in the register but not tracked, and tracked but not
// declared, an honest coverage signal — it never fabricates contractual data). It carries
// only non-sensitive references, never contract terms.
type KnownICTProvider struct {
	// Ref is the provider reference the plane tracks (e.g. "anthropic", "openai").
	Ref string
	// Source is where the plane knows it from (e.g. "gpai_posture").
	Source string
	// Verified reports whether the plane holds a verified compliance posture for it.
	Verified bool
}

// RegisterDocument is the structured DORA Register of Information the packager produces. The
// open module persists it (so the plane is the maintained system of record) and serves the
// export; it does not interpret the template bodies (the 2024/2956 semantics are the closed
// add-on's depth) — it is a governed persistence/export/audit substrate over them.
type RegisterDocument struct {
	// Regulation is the full citation of the instrument the register is structured to.
	Regulation string
	// EntityLEI is the maintaining financial entity's LEI (template B_01.01.0010); the
	// register's identity key. Empty ⇒ the open handler rejects the document (deny-closed).
	EntityLEI string
	// EntityName is the maintaining entity's name (B_01.01.0020), display only.
	EntityName string
	// ReferenceDate is the reporting reference date (B_01.01.0060).
	ReferenceDate string
	// Templates is the structured Register of Information: a map keyed by template code
	// ("B_01.01".."B_99.01") to that template's rows. The open module stores it verbatim.
	Templates map[string]any
	// Validation are the packager's validation findings (missing mandatory fields, malformed
	// LEI/codes, broken cross-references) — surfaced honestly, NEVER silently passed. A
	// register with errors is still stored (so the operator can fix it), flagged as a draft.
	Validation []RegisterIssue
	// Reconciliation are the declared-vs-tracked reconciliation notes against `known`
	// (declared-but-untracked / tracked-but-undeclared providers) — coverage signal, never a
	// claim of completeness.
	Reconciliation []RegisterIssue
	// Counts is the per-template row count (display + the persisted summary).
	Counts map[string]int
	// Note is an honest coverage caveat (e.g. "labels rest on ESA artifacts, not byte-diffed
	// against the OJ"); empty when clean.
	Note string
}

// RegisterIssue is one validation or reconciliation finding on a register. It is bounded,
// non-sensitive metadata: a severity, the template/field it concerns and a short message.
type RegisterIssue struct {
	Severity string `json:"severity"` // "error" | "warning" | "info"
	Template string `json:"template,omitempty"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
}

// IncidentInput is one operator-supplied incident to classify against the DORA criteria.
type IncidentInput struct {
	// Reference is the operator's incident reference (the identity key; rejected over-length,
	// never clamped — a truncated reference would be a different identity).
	Reference string
	// FindingID optionally links the incident to a governed finding the plane already records
	// (the plane-known metadata: kind/severity/subject/when). Display/correlation only.
	FindingID string
	// Impact is the raw operator-supplied impact data (JSON: clients/transactions affected,
	// downtime, geographical spread, data losses, economic impact, criticality) the RTS
	// materiality thresholds apply to. The plane cannot MEASURE these, so it never derives a
	// major verdict from its own telemetry alone — it applies the criteria to operator input.
	Impact []byte
}

// IncidentClassification is the packager's provisional major-incident assessment + report
// draft. The open module persists it (the reporting lifecycle is stateful: initial →
// intermediate → final) and serves it; the duty to report and the legal classification rest
// with the financial entity.
type IncidentClassification struct {
	// Reference echoes the operator's incident reference.
	Reference string
	// Major is the provisional verdict (Regulation (EU) 2022/2554 Art 18 + RTS (EU) 2024/1772
	// Art 8). It is decision support, NOT the legal classification.
	Major bool
	// CriticalServices reports the Art 6 gating precondition (a major verdict requires it).
	CriticalServices bool
	// CriteriaMet lists the Art 9 materiality thresholds met (e.g. "9(3)", "9(4)", "9(5)(b)").
	CriteriaMet []string
	// Rationale is the human-readable explanation of the verdict (which gate + thresholds).
	Rationale string
	// Report is the structured report draft (general / initial / intermediate / final field
	// groups, RTS (EU) 2025/301 + ITS (EU) 2025/302), populated as far as the input allows.
	Report map[string]any
	// Deadlines is the computed reporting timetable (provisional): initial (4h from
	// classification / 24h from awareness), intermediate (72h from initial submission), final
	// (1 month from intermediate). Surfaced with the governing article so a human can check it.
	Deadlines map[string]any
	// Basis cites the criteria/articles the verdict rests on, as data (provision + source_url +
	// verified_on), the dora.go dates-as-data bar.
	Basis []map[string]string
	// Note is an honest caveat (e.g. provisional values, unverified labels); empty when clean.
	Note string
}

// --- persisted register DTO -------------------------------------------------------

// registeredRegisterDTO is a stored DORA register as returned to a caller (the maintained
// system-of-record artifact).
type registeredRegisterDTO struct {
	ID             string          `json:"id"`
	Regulation     string          `json:"regulation"`
	EntityLEI      string          `json:"entity_lei"`
	EntityName     string          `json:"entity_name,omitempty"`
	ReferenceDate  string          `json:"reference_date,omitempty"`
	Templates      map[string]any  `json:"templates,omitempty"`
	Validation     []RegisterIssue `json:"validation,omitempty"`
	Reconciliation []RegisterIssue `json:"reconciliation,omitempty"`
	Counts         map[string]int  `json:"counts,omitempty"`
	ErrorCount     int             `json:"error_count"`
	Note           string          `json:"note,omitempty"`
	DocSHA256      string          `json:"doc_sha256"`
	GeneratedBy    string          `json:"generated_by"`
	GeneratedAt    string          `json:"generated_at"`
	LedgerAnchor   map[string]any  `json:"ledger_anchor,omitempty"`
	Disclaimer     string          `json:"disclaimer"`
}

// doraRegisterDisclaimer is the honesty banner every register carries (docs/SECURITY-HARDENING.md,
// SESSIONS-PLAN:114-116). It HELPS COMPLY; it never makes the entity compliant or certifies.
const doraRegisterDisclaimer = "DORA Register of Information drafted to the templates of Commission Implementing Regulation (EU) 2024/2956 (under Regulation (EU) 2022/2554 Art 28(3)) from operator-supplied data, reconciled against the control plane's tracked ICT providers. The control plane automates register population and validation; it does NOT make the tenant DORA-compliant and this is NOT legal advice or a certification. Validation findings are advisory; the register is a DRAFT a competent person must review before submission to the competent authority. Field labels rest on ESA artifacts and were not byte-diffed against the Official Journal — verify against EUR-Lex before filing."

func recordToRegisterDTO(rec model.Record, includeBody bool) registeredRegisterDTO {
	var templates map[string]any
	var validation, reconciliation []RegisterIssue
	var counts map[string]int
	_ = jsonUnmarshal(rec.String(colDRRegister), &templates)
	_ = jsonUnmarshal(rec.String(colDRValidation), &validation)
	_ = jsonUnmarshal(rec.String(colDRReconcile), &reconciliation)
	_ = jsonUnmarshal(rec.String(colDRCounts), &counts)
	dto := registeredRegisterDTO{
		ID:             rec.String(model.ColID),
		Regulation:     rec.String(colDRRegulation),
		EntityLEI:      rec.String(colDREntityLEI),
		EntityName:     rec.String(colDREntityName),
		ReferenceDate:  rec.String(colDRRefDate),
		Validation:     validation,
		Reconciliation: reconciliation,
		Counts:         counts,
		ErrorCount:     countErrors(validation),
		Note:           rec.String(colDRNote),
		DocSHA256:      rec.String(colDRDocSHA),
		GeneratedBy:    rec.String(colDRGeneratedBy),
		GeneratedAt:    rec.String(colDRGeneratedAt),
		Disclaimer:     doraRegisterDisclaimer,
	}
	// The full template body is large; the list view omits it (summary only), the get/export
	// views include it.
	if includeBody {
		dto.Templates = templates
	}
	return dto
}

func countErrors(issues []RegisterIssue) int {
	n := 0
	for _, i := range issues {
		if i.Severity == "error" {
			n++
		}
	}
	return n
}

// --- handlers ---------------------------------------------------------------------

// handleGenerateDORARegister ingests operator-supplied register data, structures it into the
// 2024/2956 Register of Information (deny-closed: 501 without a configured packager, 422 on
// a document the packager rejects), persists the maintained register (one per tenant+entity,
// replace-on-regenerate) anchored to the ledger head, and self-audits. The raw register JSON
// is the request BODY (so an existing GRC pipeline can stream it); the optional reference
// date is a query parameter.
func (m *Module) handleGenerateDORARegister(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.regPackager == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("DORA Register of Information generation requires the Olivares enterprise add-on (doraregister); not linked in this build"))
		return
	}
	doc, ok := readBoundedBody(w, r, "DORA register document")
	if !ok {
		return
	}
	refDate := clamp(strings.TrimSpace(r.URL.Query().Get("reference_date")), maxNameLen)

	var dto registeredRegisterDTO
	docSHA := hashHex(string(doc))
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		known, err := knownICTProviders(r.Context(), sc)
		if err != nil {
			return err
		}
		built, err := m.regPackager.BuildDORARegister(r.Context(), RegisterInput{Document: doc, ReferenceDate: refDate}, known)
		if err != nil {
			return errRegisterRejected{err}
		}
		// Defense in depth: the packager contract forbids a register with no maintaining
		// entity, but the open module must never persist an identity-less artifact even if a
		// future packager regresses.
		if built == nil || strings.TrimSpace(built.EntityLEI) == "" {
			return errRegisterRejected{errNoEntity}
		}
		// The maintaining-entity LEI is the register's identity key (unique tenant+entity_lei);
		// it must NOT be clamped (a truncated LEI would persist as a different identity, and a
		// clamped store vs raw lookup would split the replace-on-regenerate). Reject over-length
		// and use one canonical value for both the lookup and the stored field.
		lei := strings.TrimSpace(built.EntityLEI)
		if tooLong(lei, maxNameLen) {
			return errRegisterRejected{errStr("maintaining-entity LEI (B_01.01.0010) exceeds " + itoa(int64(maxNameLen)) + " characters; an identity field is rejected, never truncated")}
		}

		head, headOK, err := sc.Audit().Head(r.Context())
		if err != nil {
			return err
		}
		now := m.clock.Now()
		fields := map[string]any{
			colDREntityLEI:   lei,
			colDREntityName:  nullableText(clamp(built.EntityName, maxNameLen)),
			colDRRefDate:     nullableText(clamp(built.ReferenceDate, maxNameLen)),
			colDRRegulation:  clamp(nonEmpty(built.Regulation, doraRegisterRegulation), maxRefLen),
			colDRRegister:    encodeJSON(built.Templates),
			colDRValidation:  encodeJSON(built.Validation),
			colDRReconcile:   encodeJSON(built.Reconciliation),
			colDRCounts:      encodeJSON(built.Counts),
			colDRNote:        nullableText(clamp(built.Note, maxNoteLen)),
			colDRDocSHA:      docSHA,
			colDRGeneratedBy: mc.Principal.Actor(),
			colDRGeneratedAt: now.String(),
			colLedgerSeq:     head.Seq,
			colLedgerHash:    nullableText(ledgerHashHex(head, headOK)),
		}
		repo, err := sc.Ext(doraRegisterKind)
		if err != nil {
			return err
		}
		existing, err := listAll(r.Context(), repo, eq(colDREntityLEI, lei))
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
		dto = recordToRegisterDTO(saved, true)
		return auditEvent(r.Context(), sc, mc, "compliance.dora.register.generate", doraRegisterKind, model.ID(saved.String(model.ColID)), map[string]any{
			"entity_lei":     lei,
			"reference_date": built.ReferenceDate,
			"errors":         countErrors(built.Validation),
			"reconcile":      len(built.Reconciliation),
			"doc_sha256":     docSHA,
		})
	})
	if err != nil {
		writeRegisterError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListDORARegisters(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var items []registeredRegisterDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraRegisterKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo)
		for _, rec := range recs {
			items = append(items, recordToRegisterDTO(rec, false))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[registeredRegisterDTO]{Items: items})
}

func (m *Module) handleGetDORARegister(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto registeredRegisterDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraRegisterKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToRegisterDTO(rec, true)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleExportDORARegister exports the maintained register as the formal Register of
// Information, with a LIVE ledger integrity proof (the dora.go/evidence-package pattern) so
// the export proves the register was anchored to a tamper-evident ledger. Exporting a stored
// register is a sensitive evidence read, so it self-audits in a committed transaction.
func (m *Module) handleExportDORARegister(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto registeredRegisterDTO
	var anchor map[string]any
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraRegisterKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToRegisterDTO(rec, true)
		anchor, err = liveLedgerAnchor(r.Context(), sc, rec.Int(colLedgerSeq), rec.String(colLedgerHash))
		if err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "compliance.dora.register.export", doraRegisterKind, id, map[string]any{
			"entity_lei": rec.String(colDREntityLEI),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at":   m.clock.Now().String(),
		"regulation":     dto.Regulation,
		"entity_lei":     dto.EntityLEI,
		"reference_date": dto.ReferenceDate,
		"templates":      dto.Templates,
		"validation":     dto.Validation,
		"reconciliation": dto.Reconciliation,
		"counts":         dto.Counts,
		"error_count":    dto.ErrorCount,
		"note":           dto.Note,
		"doc_sha256":     dto.DocSHA256,
		"ledger_anchor":  anchor,
		"disclaimer":     doraRegisterDisclaimer,
	})
}

// handleDeleteDORARegister removes a maintained register; admin-tier and self-audited (a
// register is a governance artifact).
func (m *Module) handleDeleteDORARegister(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(doraRegisterKind)
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
		return auditEvent(r.Context(), sc, mc, "compliance.dora.register.delete", doraRegisterKind, id, map[string]any{
			"entity_lei": rec.String(colDREntityLEI),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// --- shared helpers ---------------------------------------------------------------

// doraRegisterRegulation is the full official citation of the RoI Implementing Regulation,
// the fallback the open module stamps if the packager omits it.
const doraRegisterRegulation = "Commission Implementing Regulation (EU) 2024/2956 of 29 November 2024 laying down implementing technical standards for the application of Regulation (EU) 2022/2554 with regard to the standard templates for the register of information"

// knownICTProviders reads the control plane's tracked ICT/AI third-party providers (the
// models module's per-provider GPAI posture, the same source dora.go's third-party register
// uses) so the packager can reconcile the operator's declared providers against them. It is
// graceful: when the models module is not registered the inventory is empty (an honest empty
// reconciliation, never a fabricated provider).
func knownICTProviders(ctx context.Context, sc store.Scope) ([]KnownICTProvider, error) {
	repo, err := sc.Ext(gpaiPostureExtKind)
	if err != nil {
		return nil, nil // models module absent ⇒ empty inventory (honest)
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]KnownICTProvider, 0, len(recs))
	for _, rec := range recs {
		ref := strings.TrimSpace(rec.String("provider_ref"))
		if ref == "" {
			continue
		}
		out = append(out, KnownICTProvider{Ref: ref, Source: "gpai_posture", Verified: rec.Bool("verified")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// ledgerHashHex returns the hex ledger-head hash when the head is valid, "" otherwise — so
// the persisted anchor never carries a hash for an empty/absent chain.
func ledgerHashHex(head store.HeadRef, ok bool) string {
	if !ok {
		return ""
	}
	return hex.EncodeToString(head.Hash)
}

// liveLedgerAnchor re-verifies the ledger at export and returns the proof: the seq+hash the
// artifact was anchored to at generation, plus a LIVE integrity result. A read error never
// fails the export — it degrades to integrity_ok=false (honest), surfaced for the caller.
func liveLedgerAnchor(ctx context.Context, sc store.Scope, sealedSeq int64, sealedHash string) (map[string]any, error) {
	rep, err := sc.Audit().Verify(ctx, 0)
	if err != nil {
		return nil, err
	}
	head, headOK, err := sc.Audit().Head(ctx)
	if err != nil {
		return nil, err
	}
	anchor := map[string]any{
		"anchored_ledger_seq": sealedSeq,
		"integrity_ok":        headOK && rep.OK && rep.Checked > 0,
		"integrity_checked":   rep.Checked,
	}
	if sealedHash != "" {
		anchor["anchored_ledger_hash"] = sealedHash
	}
	if headOK {
		anchor["current_ledger_seq"] = head.Seq
	}
	return anchor, nil
}

// errRegisterRejected wraps a packager rejection so the handler maps it to 422 (the document
// was understood but cannot be structured), distinct from a store error (500).
type errRegisterRejected struct{ err error }

func (e errRegisterRejected) Error() string { return e.err.Error() }

// Unwrap is not decoration, and its absence was the ROOT of F2 on this side.
//
// Without it this wrapper is OPAQUE to errors.Is, and every sentinel the module routes on
// disappears behind it: writeStoreError is one errors.Is switch (helpers.go:60-95), so a
// wrapped license.ErrAddonRequiresLicense, store.ErrNotFound, store.ErrAuditSpoolFull or
// workspace-confinement error all fell through to `default:` — 500 "internal error". A
// commercial refusal and a missing row were reported to the operator as a server fault, and
// in the logs they were indistinguishable from a real one.
//
// Its sibling errNIS2Rejected has had Unwrap since it was written (nis2incident.go:105); this
// one did not, and the asymmetry is the whole defect. Measured by the cross-product cell in
// s694_test.go, which put this wrapper through both writers: writeNIS2Error answered 500 and
// writeRegisterError answered 422, for an error whose identity says 403.
func (e errRegisterRejected) Unwrap() error { return e.err }

// errNoEntity is the defense-in-depth rejection of a register with no maintaining entity.
var errNoEntity = errStr("the register has no maintaining-entity LEI (template B_01.01.0010)")

type errStr string

func (e errStr) Error() string { return string(e) }

// writeRegisterError maps a DORA register/incident failure. Same ordered contract as
// writeNIS2Error, and the same defect it fixes (Codex contrast F2): every packager error
// is wrapped in errRegisterRejected (:275, doraincident.go:119), so an entitlement refusal came
// out as 422 "DORA register rejected" instead of the 403 that names the add-on. An operator
// reading that would edit a document that was never the problem.
func writeRegisterError(w http.ResponseWriter, err error) {
	if errors.Is(err, license.ErrAddonRequiresLicense) {
		writeStoreError(w, err)
		return
	}
	var rej errRegisterRejected
	if errors.As(err, &rej) {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("DORA register rejected: "+clamp(rej.err.Error(), maxNameLen)))
		return
	}
	writeStoreError(w, err)
}
