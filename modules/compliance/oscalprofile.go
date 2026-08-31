// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the OPEN-CORE half of OSCAL profile/SSP ingestion: the seam the
// module consumes to let an operator-supplied OSCAL profile/catalog/SSP SELECT/FILTER
// which controls the assessment-results export evidences, and the persistence +
// governed ingestion endpoint that drives it. The VALUE — parsing and resolving the
// OSCAL models (profile imports/include-controls/exclude-controls/merge semantics, SSP
// import-profile + implemented-requirements) — lives in the commercial add-on
// enterprise/oscalingest, wired ONLY under -tags enterprise (the federation/multi-IdP
// pattern). The open binary never links it; without a wired resolver the ingestion
// endpoint answers 501 and the OSCAL export is BYTE-IDENTICAL to the prior behavior
// (include-all, no profile props). The seam interface and its value objects live here
// so both sides share them without the open module importing the closed one.
//
// Honesty (docs/SECURITY-HARDENING.md): a resolved selection is a SUBSET of an EXISTING known framework
// (Decision: require a known framework, deny-closed otherwise — never guess
// a control universe). Selected ids that map to no known framework control are reported
// honestly (a note + dropped list), never silently assessed. The export still asserts
// OSCAL `satisfied` only for live operational evidence (oscal.go); scoping changes WHICH
// controls are in the assessment-results, never their status.
//
// Minimal data (docs/SECURITY-HARDENING.md,§5): the persisted artifact is the resolved control-id
// selection + the back-references (profile/ssp uuid, import-profile href, source href)
// + a SHA-256 of the ingested document. The operator's OSCAL document itself is
// REFERENCED by hash, never copied into the store.

// ProfileResolver parses and resolves an operator-supplied OSCAL document (profile,
// catalog or system-security-plan) into a deterministic control selection over a known
// framework. The default is nil — without a wired resolver the ingestion endpoint is
// unavailable (501) and the export keeps its include-all behavior. The real
// implementation is enterprise/oscalingest, wired only under -tags enterprise.
type ProfileResolver interface {
	// Resolve parses+validates an OSCAL document and resolves its control selection
	// against the known framework catalog (cat). It MUST return a deny-closed error on
	// an invalid/unsupported document, an unresolvable framework, or an empty resulting
	// selection — never a silent partial selection. cat lets the resolver map an OSCAL
	// import/catalog href to a known framework and bound the selection to that
	// framework's controls (the open catalog stays open; the resolver never embeds it).
	Resolve(ctx context.Context, in ProfileInput, cat FrameworkCatalog) (*ResolvedProfile, error)
}

// ProfileInput is one operator-supplied OSCAL document to ingest.
type ProfileInput struct {
	// Document is the raw OSCAL JSON bytes (a profile, catalog or system-security-plan).
	Document []byte
	// Framework is an OPTIONAL operator hint: the known framework id the profile/SSP
	// targets. When set and known it disambiguates the target framework; when empty the
	// resolver derives it from the document's import/source href (deny-closed if neither
	// resolves to a known framework).
	Framework string
}

// FrameworkCatalog exposes the open framework catalog to the resolver without the
// resolver importing the catalog internals: it maps a known framework id and an OSCAL
// source/import href to that framework's ordered control-id list.
type FrameworkCatalog interface {
	// ControlIDs returns the ordered control-id list of a known framework (ok=false if
	// the framework id is unknown).
	ControlIDs(framework string) ([]string, bool)
	// FrameworkForHref maps an OSCAL import/catalog/import-profile href to a known
	// framework id (ok=false if the href maps to no known framework).
	FrameworkForHref(href string) (string, bool)
}

// ResolvedProfile is the minimal, deterministic result of resolving an OSCAL document:
// the in-scope control selection plus the back-references the assessment-results export
// anchors to. It is minimal-data — control-ids and references only, never the document
// payload.
type ResolvedProfile struct {
	// DocKind is the OSCAL model ingested: "profile" | "catalog" | "system-security-plan".
	DocKind string
	// Framework is the resolved KNOWN framework id the selection is scoped to.
	Framework string
	// SelectedControlIDs is the ordered, deterministic, de-duplicated control-id
	// selection — a subset of the framework's controls (catalog order preserved).
	SelectedControlIDs []string
	// DroppedControlIDs are control-ids the document selected that are NOT controls of
	// the resolved framework (out of assessment scope — surfaced honestly, never assessed).
	DroppedControlIDs []string
	// ProfileUUID is the OSCAL profile uuid (a profile, or the profile an SSP imports
	// when known); empty otherwise.
	ProfileUUID string
	// SSPUUID is the OSCAL system-security-plan uuid (an SSP ingestion); empty otherwise.
	SSPUUID string
	// ImportProfileHref is ssp.import-profile.href (an SSP ingestion); empty otherwise.
	ImportProfileHref string
	// SourceHref is the profile import[].href / catalog source the selection derives from.
	SourceHref string
	// OscalVersion is metadata.oscal-version of the ingested document.
	OscalVersion string
	// Title is metadata.title of the ingested document (display only).
	Title string
	// Note is an honest coverage caveat (e.g. "N selected controls map to no framework
	// control and are out of assessment scope"); empty when the selection is clean.
	Note string
}

// ProfileRef is the export-facing subset of a registered profile: the references the
// OSCAL assessment-results carry back (props) plus the resolved selection that scopes
// reviewed-controls. A nil *ProfileRef passed to oscalDocument keeps the include-all
// behavior (no profile props) byte-identical to the no-profile export.
type ProfileRef struct {
	DocKind           string
	ProfileUUID       string
	SSPUUID           string
	ImportProfileHref string
	SourceHref        string
	DocSHA256         string
	OscalVersion      string
	SelectedIDs       []string
}

// NewFrameworkCatalog returns the open framework-catalog view (FrameworkCatalog) over the
// in-repo catalog — the bridge the OSCAL resolver consumes to map an import/source href to
// a known framework and to bound a selection to that framework's controls. The module uses
// it internally at ingestion; it is exported so the commercial resolver can be exercised
// against the REAL catalog (integration tests, composition root) without the open module
// exposing its internals or importing the closed add-on.
func NewFrameworkCatalog() FrameworkCatalog { return frameworkCatalogAdapter{} }

// frameworkCatalogAdapter is the open implementation of FrameworkCatalog over the
// in-repo framework catalog (frameworks.go). It is the ONLY view of the catalog the
// resolver gets — it never sees control text, capabilities or status.
type frameworkCatalogAdapter struct{}

func (frameworkCatalogAdapter) ControlIDs(framework string) ([]string, bool) {
	fw, ok := frameworkByID[strings.TrimSpace(framework)]
	if !ok {
		return nil, false
	}
	ids := make([]string, len(fw.Controls))
	for i, c := range fw.Controls {
		ids[i] = c.ID
	}
	return ids, true
}

// oscalSourcePrefix is the canonical source URL the OSCAL export stamps for an Olivares
// framework (oscal.go: source = ".../compliance/frameworks/<id>"). An operator authoring
// a profile against our published catalog imports exactly that href; FrameworkForHref is
// its inverse. It is deliberately exact (no fuzzy guessing — Decision #2).
//
// ⛔ These three are the ONLY declarations of the public URLs the export seals, and that
// is the point. Until 2026-08-27 `oscal.go` restated this prefix as a second literal —
// the same fact in two places, where changing one would have left FrameworkForHref
// silently unable to invert its own exporter's output. They are also what
// PublicCatalog() publishes to the website (publiccatalog.go), so the page that must
// SERVE the URL and the exporter that SEALS it read the same constant.
const (
	oscalSourcePrefix         = "https://olivares.ai/compliance/frameworks/"
	oscalAssessmentPlanPrefix = "https://olivares.ai/compliance/assessment-plan/"
	oscalCapabilitiesURL      = "https://olivares.ai/compliance/capabilities"
)

func (frameworkCatalogAdapter) FrameworkForHref(href string) (string, bool) {
	h := strings.TrimSpace(href)
	if !strings.HasPrefix(h, oscalSourcePrefix) {
		return "", false
	}
	id := strings.Trim(strings.TrimPrefix(h, oscalSourcePrefix), "/")
	// A href may carry a fragment/suffix (".../eu_ai_act#xyz" or ".../eu_ai_act.json");
	// take the leading path segment and verify it is a real framework — never invent one.
	if i := strings.IndexAny(id, "/#?"); i >= 0 {
		id = id[:i]
	}
	id = strings.TrimSuffix(id, ".json")
	if _, ok := frameworkByID[id]; !ok {
		return "", false
	}
	return id, true
}

// --- persisted registered-profile DTO ---------------------------------------------

// registeredProfileDTO is a registered OSCAL profile/SSP as returned to a caller. It is
// the minimal-data artifact: the selection + references + the document hash.
type registeredProfileDTO struct {
	ID                 string   `json:"id"`
	Framework          string   `json:"framework"`
	DocKind            string   `json:"doc_kind"`
	ProfileUUID        string   `json:"profile_uuid,omitempty"`
	SSPUUID            string   `json:"ssp_uuid,omitempty"`
	ImportProfileHref  string   `json:"import_profile_href,omitempty"`
	SourceHref         string   `json:"source_href,omitempty"`
	SelectedControlIDs []string `json:"selected_control_ids"`
	DroppedControlIDs  []string `json:"dropped_control_ids,omitempty"`
	SelectedCount      int      `json:"selected_count"`
	OscalVersion       string   `json:"oscal_version,omitempty"`
	DocSHA256          string   `json:"doc_sha256"`
	Title              string   `json:"title,omitempty"`
	Note               string   `json:"note,omitempty"`
	ScopeNote          string   `json:"scope_note,omitempty"`
	RegisteredBy       string   `json:"registered_by"`
	RegisteredAt       string   `json:"registered_at"`
	Disclaimer         string   `json:"disclaimer"`
}

func recordToProfileDTO(rec model.Record) registeredProfileDTO {
	sel := decodeStrings(rec.String(colOPSelected))
	return registeredProfileDTO{
		ID:                 rec.String(model.ColID),
		Framework:          rec.String(colFramework),
		DocKind:            rec.String(colOPDocKind),
		ProfileUUID:        rec.String(colOPProfileUUID),
		SSPUUID:            rec.String(colOPSSPUUID),
		ImportProfileHref:  rec.String(colOPImportHref),
		SourceHref:         rec.String(colOPSourceHref),
		SelectedControlIDs: sel,
		DroppedControlIDs:  decodeStrings(rec.String(colOPDropped)),
		SelectedCount:      len(sel),
		OscalVersion:       rec.String(colOPOscalVer),
		DocSHA256:          rec.String(colOPDocSHA),
		Title:              rec.String(colTitle),
		Note:               rec.String(colOPNote),
		ScopeNote:          rec.String(colScopeNote),
		RegisteredBy:       rec.String(colOPRegisteredBy),
		RegisteredAt:       rec.String(colOPRegisteredAt),
		Disclaimer:         reportDisclaimer,
	}
}

// --- handlers ----------------------------------------------------------------------

// handleRegisterOSCALProfile ingests an operator-supplied OSCAL profile/catalog/SSP and
// registers the resolved control selection for a framework (one active selection per
// tenant+framework; re-registering replaces it). It is a GOVERNED, deny-closed action:
//
//   - resolver UN-WIRED (default AGPL build) ⇒ 501; OSCAL ingestion is the enterprise
//     add-on (no rug-pull: the export keeps include-all here).
//   - document invalid / framework unresolvable / selection empty ⇒ 422 and NOTHING is
//     persisted (never a silent partial selection).
//   - resolved ⇒ persist the minimal artifact (selection + refs + doc SHA-256) and
//     self-audit; the OSCAL export now scopes assessment-results to the selection and
//     references the profile.
//
// The raw OSCAL JSON is the request BODY (so an existing OSCAL pipeline can stream its
// file directly); the optional framework hint and scope note are query parameters.
func (m *Module) handleRegisterOSCALProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.profileResolver == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("OSCAL profile/SSP ingestion requires the Olivares enterprise add-on (oscalingest); not linked in this build"))
		return
	}
	doc, ok := readBoundedBody(w, r, "OSCAL document")
	if !ok {
		return
	}
	fwHint := clamp(strings.TrimSpace(r.URL.Query().Get("framework")), maxNameLen)
	scopeNote := clamp(strings.TrimSpace(r.URL.Query().Get("scope_note")), maxNoteLen)

	resolved, err := m.profileResolver.Resolve(r.Context(), ProfileInput{Document: doc, Framework: fwHint}, frameworkCatalogAdapter{})
	if err != nil {
		// Deny-closed: a document we cannot fully resolve is rejected, never partially
		// applied. The resolver's messages are safe OSCAL-shape diagnostics.
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("OSCAL document rejected: "+clamp(err.Error(), maxNameLen)))
		return
	}
	// Defense in depth: the resolver contract forbids an empty selection, but the open
	// module must never persist a meaningless (zero-control) scope even if a future
	// resolver regresses.
	if resolved == nil || len(resolved.SelectedControlIDs) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("OSCAL document selected no controls of any known framework"))
		return
	}
	if _, known := frameworkByID[resolved.Framework]; !known {
		// The resolver must resolve to a known framework; reject an unknown id rather
		// than persist a dangling selection.
		writeJSON(w, http.StatusUnprocessableEntity, errorBody("OSCAL document did not resolve to a known framework"))
		return
	}

	docSHA := hashHex(string(doc))
	var dto registeredProfileDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(oscalProfileKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		fields := map[string]any{
			colFramework:      resolved.Framework,
			colOPDocKind:      resolved.DocKind,
			colOPProfileUUID:  nullableText(resolved.ProfileUUID),
			colOPSSPUUID:      nullableText(resolved.SSPUUID),
			colOPImportHref:   nullableText(clamp(resolved.ImportProfileHref, maxRefLen)),
			colOPSourceHref:   nullableText(clamp(resolved.SourceHref, maxRefLen)),
			colOPSelected:     encodeJSON(resolved.SelectedControlIDs),
			colOPDropped:      encodeJSON(resolved.DroppedControlIDs),
			colOPOscalVer:     nullableText(clamp(resolved.OscalVersion, maxNameLen)),
			colOPDocSHA:       docSHA,
			colTitle:          nullableText(clamp(resolved.Title, maxNameLen)),
			colOPNote:         nullableText(clamp(resolved.Note, maxNoteLen)),
			colScopeNote:      nullableText(scopeNote),
			colOPRegisteredBy: mc.Principal.Actor(),
			colOPRegisteredAt: now.String(),
		}
		existing, err := listAll(r.Context(), repo, eq(colFramework, resolved.Framework))
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
		dto = recordToProfileDTO(saved)
		return auditEvent(r.Context(), sc, mc, "compliance.oscal.profile.register", oscalProfileKind, model.ID(saved.String(model.ColID)), map[string]any{
			"framework":      resolved.Framework,
			"doc_kind":       resolved.DocKind,
			"selected":       len(resolved.SelectedControlIDs),
			"dropped":        len(resolved.DroppedControlIDs),
			"doc_sha256":     docSHA,
			"oscal_version":  resolved.OscalVersion,
			"import_profile": resolved.ImportProfileHref,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListOSCALProfiles(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if fwID := strings.TrimSpace(r.URL.Query().Get("framework")); fwID != "" {
		filters = append(filters, eq(colFramework, fwID))
	}
	var items []registeredProfileDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(oscalProfileKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, filters...)
		for _, rec := range recs {
			items = append(items, recordToProfileDTO(rec))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[registeredProfileDTO]{Items: items})
}

func (m *Module) handleGetOSCALProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto registeredProfileDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(oscalProfileKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToProfileDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleDeleteOSCALProfile unregisters a profile — the export reverts to include-all for
// that framework. It is admin-tier and self-audits (a scope change is a governance act).
func (m *Module) handleDeleteOSCALProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(oscalProfileKind)
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
		return auditEvent(r.Context(), sc, mc, "compliance.oscal.profile.unregister", oscalProfileKind, id, map[string]any{
			"framework": rec.String(colFramework),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// --- export-side selection (read by handleExportEvidence) --------------------------

// activeProfileRef returns the registered profile reference for a framework within an
// open transaction (so the export reads it in its own self-auditing tx), or nil when no
// profile is registered (⇒ the OSCAL export keeps include-all, byte-identical). It never
// fails the export: a read error degrades to "no profile" (honest — an unreadable scope
// must not silently drop controls), surfaced via the returned error for logging.
func activeProfileRef(ctx context.Context, sc store.Scope, framework string) (*ProfileRef, error) {
	repo, err := sc.Ext(oscalProfileKind)
	if err != nil {
		return nil, err
	}
	rec, found, err := findOne(ctx, repo, eq(colFramework, framework))
	if err != nil || !found {
		return nil, err
	}
	sel := decodeStrings(rec.String(colOPSelected))
	if len(sel) == 0 {
		return nil, nil
	}
	return &ProfileRef{
		DocKind:           rec.String(colOPDocKind),
		ProfileUUID:       rec.String(colOPProfileUUID),
		SSPUUID:           rec.String(colOPSSPUUID),
		ImportProfileHref: rec.String(colOPImportHref),
		SourceHref:        rec.String(colOPSourceHref),
		DocSHA256:         rec.String(colOPDocSHA),
		OscalVersion:      rec.String(colOPOscalVer),
		SelectedIDs:       sel,
	}, nil
}

// filterResultsBySelection restricts the sealed control results to the profile's
// selection, preserving the sealed (catalog) order. Selected ids absent from the sealed
// package are simply not present (the intersection); ids in the package but not selected
// are dropped. It never reorders or mutates a result — only filters.
func filterResultsBySelection(results []controlResultDTO, selected []string) []controlResultDTO {
	want := make(map[string]struct{}, len(selected))
	for _, id := range selected {
		want[id] = struct{}{}
	}
	out := make([]controlResultDTO, 0, len(results))
	for _, rc := range results {
		if _, ok := want[rc.ControlID]; ok {
			out = append(out, rc)
		}
	}
	return out
}

// --- helpers -----------------------------------------------------------------------

// readBoundedBody reads the raw request body up to the module's 1 MiB cap (docs/SECURITY-HARDENING.md).
// It is used by endpoints whose body is a raw JSON document (not a wrapper struct), so the
// exact bytes can be hashed for the minimal-data anchor: the OSCAL ingestion, the DORA
// register and the incident impact. label names the document in the empty-body error so
// each domain reports its own (not a misleading "OSCAL" for a DORA endpoint).
//
// ⛔ AN OVER-LENGTH DOCUMENT IS REJECTED, NEVER TRUNCATED — and until it was truncated,
// silently. `io.LimitReader(r.Body, maxReqBytes)` stops at the cap and returns what it got,
// which every caller then treats as the whole document: the prefix is hashed into the
// minimal-data anchor, handed to the packager, and persisted. A parser that accepts one JSON
// value followed by EOF sees a COMPLETE document, so nothing anywhere reports a problem — the
// operator gets a 201 and an anchor over bytes that are not what they sent, with the tail
// gone. Found by the Codex sol max contrast of (F1).
//
// This is the same rule the module already applies to identity strings one file over: "IDENTITY
// fields must be REJECTED when over-length, never clamped" (helpers.go tooLong) — for the same
// reason, that a shortened value is a DIFFERENT value that still looks valid. An evidence
// document earns it more, not less.
//
// The mechanism is reading ONE byte past the cap: LimitReader cannot distinguish "exactly at
// the cap" from "cut off there", so the extra byte is the only thing that makes the difference
// observable. 413 matches the module's siblings (an internal design note (not shipped):138,
// reporting/enterprise.go:324).
func readBoundedBody(w http.ResponseWriter, r *http.Request, label string) ([]byte, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxReqBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("cannot read request body"))
		return nil, false
	}
	if len(b) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("empty "+label))
		return nil, false
	}
	if len(b) > maxReqBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody(label+
			" exceeds "+itoa(int64(maxReqBytes))+" bytes; it is rejected, never truncated — a "+
			"shortened evidence document would be hashed and classified as if it were the whole thing"))
		return nil, false
	}
	return b, true
}
