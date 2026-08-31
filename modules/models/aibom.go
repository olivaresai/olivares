// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// AIBOM (AI Bill of Materials, CycloneDX 1.6). Emits/seals a CycloneDX
// machine-learning BOM for an owned model: the model's versions as
// `machine-learning-model` components carrying a `modelCard`, the datasets they are
// built on as `data` components (referenced via modelCard.modelParameters.datasets[].ref),
// and the SIGNED-MODEL-ADMISSION verdict + the supplier GPAI posture as honest
// component properties. This is the compliance/lineage evidence side of G15: the
// AIBOM is exportable, and a SEALED AIBOM (models.aibom) anchors a content hash to
// the append-only audit ledger — exactly like compliance's evidence package — so the
// lineage is tamper-evident, auditable evidence (consumed by modules/compliance XIII).
//
// The shape is mirrored faithfully from the CycloneDX 1.6 JSON schema
// (cyclonedx.org/docs/1.6/json, bom-1.6.schema.json, verified 2026-06-09): bomFormat
// "CycloneDX", specVersion "1.6", components[] of type "machine-learning-model" (with
// modelCard{modelParameters,considerations}) and "data" (with data[].{type:"dataset",
// classification, ...}). Hand-written structs (no cyclonedx-go dependency, per the
// dep-isolation decision). MINIMAL-DATA (docs/SECURITY-HARDENING.md): only metadata/refs/hashes —
// never weights or dataset contents. Fields we do not have are OMITTED, never
// fabricated (no invented performance metrics or training figures).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// --- CycloneDX 1.6 wire types (the subset we emit) ---------------------------

type cdxBOM struct {
	BOMFormat    string         `json:"bomFormat"`
	SpecVersion  string         `json:"specVersion"`
	SerialNumber string         `json:"serialNumber,omitempty"`
	Version      int            `json:"version"`
	Metadata     *cdxMetadata   `json:"metadata,omitempty"`
	Components   []cdxComponent `json:"components,omitempty"`
}

type cdxMetadata struct {
	Timestamp  string        `json:"timestamp,omitempty"`
	Component  *cdxComponent `json:"component,omitempty"`
	Properties []cdxProperty `json:"properties,omitempty"`
}

type cdxComponent struct {
	Type       string        `json:"type"`
	BOMRef     string        `json:"bom-ref,omitempty"`
	Name       string        `json:"name"`
	Version    string        `json:"version,omitempty"`
	Hashes     []cdxHash     `json:"hashes,omitempty"`
	Data       []cdxData     `json:"data,omitempty"`      // type=="data"
	ModelCard  *cdxModelCard `json:"modelCard,omitempty"` // type=="machine-learning-model"
	Properties []cdxProperty `json:"properties,omitempty"`
}

type cdxHash struct {
	Alg     string `json:"alg"` // e.g. "SHA-256"
	Content string `json:"content"`
}

type cdxData struct {
	Type           string `json:"type"` // "dataset"
	Name           string `json:"name,omitempty"`
	Classification string `json:"classification,omitempty"`
	Description    string `json:"description,omitempty"`
}

type cdxModelCard struct {
	BOMRef          string              `json:"bom-ref,omitempty"`
	ModelParameters *cdxModelParameters `json:"modelParameters,omitempty"`
	Considerations  *cdxConsiderations  `json:"considerations,omitempty"`
	Properties      []cdxProperty       `json:"properties,omitempty"`
}

type cdxModelParameters struct {
	Task               string          `json:"task,omitempty"`
	ArchitectureFamily string          `json:"architectureFamily,omitempty"`
	Datasets           []cdxDatasetRef `json:"datasets,omitempty"`
}

type cdxDatasetRef struct {
	Ref string `json:"ref"`
}

type cdxConsiderations struct {
	TechnicalLimitations []string `json:"technicalLimitations,omitempty"`
}

type cdxProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// --- AIBOM generation --------------------------------------------------------

const aibomDisclaimer = "AIBOM is audit-readiness lineage evidence for an AI control plane, not a certification or conformity assessment (docs/08 §9). Coverage: model/dataset identity, provenance and signed-admission verdict; NOT dataset quality, bias or model performance."

// buildAIBOM assembles a CycloneDX 1.6 AIBOM for one owned model: a
// machine-learning-model component per version (with the admission verdict + supplier
// posture as honest properties) plus its datasets as data components. It reads only
// this tenant's scope; an absent sibling row is simply omitted, never faked.
func buildAIBOM(r *http.Request, sc store.Scope, ownedID model.ID) (cdxBOM, error) {
	owned, err := func() (ownedModelDTO, error) {
		repo, err := sc.Ext(ownedModelKind)
		if err != nil {
			return ownedModelDTO{}, err
		}
		rec, err := repo.Get(r.Context(), ownedID)
		if err != nil {
			return ownedModelDTO{}, err
		}
		return toOwnedModelDTO(rec), nil
	}()
	if err != nil {
		return cdxBOM{}, err
	}

	bom := cdxBOM{
		BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1,
		SerialNumber: "urn:uuid:" + model.NewID().String(),
		Metadata: &cdxMetadata{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Component: &cdxComponent{Type: "machine-learning-model", BOMRef: "model:" + owned.ID, Name: owned.Name},
			Properties: []cdxProperty{
				{Name: "olivares:aibom:generator", Value: "olivares.models"},
				{Name: "olivares:aibom:disclaimer", Value: aibomDisclaimer},
				{Name: "olivares:model:kind", Value: owned.Kind},
			},
		},
	}
	if owned.ProviderRef != "" {
		bom.Metadata.Properties = append(bom.Metadata.Properties, cdxProperty{Name: "olivares:model:provider_ref", Value: owned.ProviderRef})
	}

	// Datasets (lineage) → data components, referenced by the model components.
	dsRefs, dsComponents, err := datasetComponents(r, sc, owned.ID)
	if err != nil {
		return cdxBOM{}, err
	}
	bom.Components = append(bom.Components, dsComponents...)

	// Supplier GPAI posture (claim-vs-verified) for the model's provider, surfaced as
	// honest properties on each version component.
	supplierProps := supplierPostureProps(r, sc, owned.ProviderRef)

	// One machine-learning-model component per version, with its admission verdict.
	versions, err := listVersionsFor(r, sc, owned.ID)
	if err != nil {
		return cdxBOM{}, err
	}
	for _, ver := range versions {
		comp := cdxComponent{
			Type: "machine-learning-model", BOMRef: "model-version:" + ver.ID,
			Name: owned.Name, Version: ver.Version,
		}
		card := &cdxModelCard{}
		if owned.BaseRef != "" {
			card.ModelParameters = &cdxModelParameters{ArchitectureFamily: owned.BaseRef}
		}
		if len(dsRefs) > 0 {
			if card.ModelParameters == nil {
				card.ModelParameters = &cdxModelParameters{}
			}
			card.ModelParameters.Datasets = dsRefs
		}
		// Admission verdict for this version (the signed-model-admission evidence).
		admProps, hashes, limitations := admissionEvidence(r, sc, ver.ID)
		comp.Hashes = hashes
		card.Properties = append(card.Properties, admProps...)
		card.Properties = append(card.Properties, supplierProps...)
		if ver.SourceRef != "" {
			card.Properties = append(card.Properties, cdxProperty{Name: "olivares:lineage:source_ref", Value: ver.SourceRef})
		}
		if ver.ParentRef != "" {
			card.Properties = append(card.Properties, cdxProperty{Name: "olivares:lineage:parent_ref", Value: ver.ParentRef})
		}
		if ver.ArtifactRef != "" {
			card.Properties = append(card.Properties, cdxProperty{Name: "olivares:artifact_ref", Value: ver.ArtifactRef})
		}
		if len(limitations) > 0 {
			card.Considerations = &cdxConsiderations{TechnicalLimitations: limitations}
		}
		comp.ModelCard = card
		bom.Components = append(bom.Components, comp)
	}
	return bom, nil
}

func datasetComponents(r *http.Request, sc store.Scope, ownedID string) ([]cdxDatasetRef, []cdxComponent, error) {
	repo, err := sc.Ext(datasetKind)
	if err != nil {
		return nil, nil, err
	}
	recs, err := listAllExt(r.Context(), repo, eq(colDSOwned, ownedID))
	if err != nil {
		return nil, nil, err
	}
	var refs []cdxDatasetRef
	var comps []cdxComponent
	for _, rec := range recs {
		d := toDatasetDTO(rec)
		ref := "data:" + d.ID
		refs = append(refs, cdxDatasetRef{Ref: ref})
		comp := cdxComponent{
			Type: "data", BOMRef: ref, Name: d.Name,
			Data: []cdxData{{Type: "dataset", Name: d.Name, Classification: d.Classification, Description: d.Governance}},
		}
		if h, ok := cdxHashEntry(d.ContentAlg, d.ContentHash); ok {
			comp.Hashes = []cdxHash{h}
		}
		comp.Properties = []cdxProperty{{Name: "olivares:dataset:provenance_verified", Value: fmt.Sprintf("%t", d.Verified)}}
		if d.SourceRef != "" {
			comp.Properties = append(comp.Properties, cdxProperty{Name: "olivares:dataset:source_ref", Value: d.SourceRef})
		}
		comps = append(comps, comp)
	}
	return refs, comps, nil
}

func listVersionsFor(r *http.Request, sc store.Scope, ownedID string) ([]modelVersionDTO, error) {
	repo, err := sc.Ext(modelVersionKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAllExt(r.Context(), repo, eq(colVerOwned, ownedID))
	if err != nil {
		return nil, err
	}
	out := make([]modelVersionDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toModelVersionDTO(rec))
	}
	return out, nil
}

// admissionEvidence returns the signed-admission verdict for a version as CycloneDX
// properties + the subject digest as a hash + honest technical-limitation notes. An
// absent verdict yields an explicit "not_admitted" property (honest, never a fake).
func admissionEvidence(r *http.Request, sc store.Scope, versionID string) ([]cdxProperty, []cdxHash, []string) {
	repo, err := sc.Ext(modelAdmissionKind)
	if err != nil {
		return []cdxProperty{{Name: "olivares:admission:status", Value: "not_recorded"}}, nil, nil
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colAdmVersion, versionID)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return []cdxProperty{{Name: "olivares:admission:status", Value: "not_recorded"}}, nil, nil
	}
	a := toModelAdmissionDTO(recs[0])
	props := []cdxProperty{
		{Name: "olivares:admission:signature_verified", Value: fmt.Sprintf("%t", a.SignatureVerified)},
		{Name: "olivares:admission:artifact_verified", Value: fmt.Sprintf("%t", a.ArtifactVerified)},
	}
	if a.Method != "" {
		props = append(props, cdxProperty{Name: "olivares:admission:method", Value: a.Method})
	}
	if a.PredicateType != "" {
		props = append(props, cdxProperty{Name: "olivares:admission:predicate_type", Value: a.PredicateType})
	}
	if a.SignerIdentity != "" {
		props = append(props, cdxProperty{Name: "olivares:admission:signer_identity", Value: a.SignerIdentity})
	}
	if a.SignerIssuer != "" {
		props = append(props, cdxProperty{Name: "olivares:admission:signer_issuer", Value: a.SignerIssuer})
	}
	// The anchoring trust root(s) that verified the signature: provenance-complete evidence of
	// WHICH root admitted this model version (a certificate-mode verdict pins these; empty for bare-key).
	if len(a.SignerRoots) > 0 {
		props = append(props, cdxProperty{Name: "olivares:admission:signer_roots", Value: strings.Join(a.SignerRoots, " ")})
	}
	props = append(props, cdxProperty{Name: "olivares:admission:tlog_verified", Value: fmt.Sprintf("%t", a.TLogVerified)})

	var hashes []cdxHash
	if h, ok := cdxHashEntry("sha256", a.SubjectDigest); ok {
		hashes = []cdxHash{h}
	}
	var limitations []string
	if a.CoverageNote != "" {
		limitations = append(limitations, "admission coverage: "+a.CoverageNote)
	}
	if !a.SignatureVerified && a.Reason != "" {
		limitations = append(limitations, "admission not verified: "+a.Reason)
	}
	return props, hashes, limitations
}

// supplierPostureProps surfaces the provider's GPAI posture (claim-vs-verified) as
// honest properties — reusing the FIN-13 entity by KIND, no module coupling.
func supplierPostureProps(r *http.Request, sc store.Scope, providerRef string) []cdxProperty {
	if providerRef == "" {
		return nil
	}
	repo, err := sc.Ext(GPAIPostureKind)
	if err != nil {
		return nil
	}
	recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colGPAIProvider, providerRef)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return nil
	}
	return []cdxProperty{{Name: "olivares:supplier:gpai_verified", Value: fmt.Sprintf("%t", recs[0].Bool(colGPAIVerified))}}
}

// hexHashLens are the digest hex-lengths CycloneDX 1.6 accepts for hashes[].content
// (bom-1.6.schema.json hash-content pattern: MD5/SHA-1/SHA-256/SHA-384/SHA-512 and the
// equal-length SHA3/BLAKE variants).
var hexHashLens = map[int]bool{32: true, 40: true, 64: true, 96: true, 128: true}

// cdxAlg maps an algorithm name to its exact CycloneDX 1.6 hash-alg enum spelling, or
// ok=false for an algorithm not in the enum (so we never emit a schema-invalid alg).
func cdxAlg(alg string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(alg)) {
	case "", "sha256", "sha-256":
		return "SHA-256", true
	case "sha384", "sha-384":
		return "SHA-384", true
	case "sha512", "sha-512":
		return "SHA-512", true
	case "sha1", "sha-1":
		return "SHA-1", true
	case "md5":
		return "MD5", true
	case "sha3-256":
		return "SHA3-256", true
	case "sha3-384":
		return "SHA3-384", true
	case "sha3-512":
		return "SHA3-512", true
	case "blake2b-256":
		return "BLAKE2b-256", true
	case "blake2b-384":
		return "BLAKE2b-384", true
	case "blake2b-512":
		return "BLAKE2b-512", true
	case "blake3":
		return "BLAKE3", true
	default:
		return "", false
	}
}

// cdxHashEntry returns a schema-valid CycloneDX hash for (alg, hexDigest), or ok=false
// when the algorithm is not a CycloneDX enum or the digest is not a hex string of a
// length matching a known hash. A non-conformant digest (e.g. a registry path or a
// truncated value) is OMITTED rather than emitted, so every generated BOM validates
// against bom-1.6.schema.json.
func cdxHashEntry(alg, digest string) (cdxHash, bool) {
	a, ok := cdxAlg(alg)
	if !ok {
		return cdxHash{}, false
	}
	d := strings.ToLower(strings.TrimSpace(digest))
	if !isHexDigest(d) || !hexHashLens[len(d)] {
		return cdxHash{}, false
	}
	return cdxHash{Alg: a, Content: d}, true
}

func isHexDigest(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// canonicalAIBOMHash hashes the SUBSTANTIVE BOM content (components + the metadata
// component), excluding the volatile serialNumber and timestamp, so the same
// underlying model/dataset/admission state always hashes identically — the seal's
// tamper-evidence is over the lineage, not over the generation instant.
func canonicalAIBOMHash(bom cdxBOM) (string, error) {
	c := bom
	c.SerialNumber = ""
	if c.Metadata != nil {
		md := *c.Metadata
		md.Timestamp = ""
		c.Metadata = &md
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// --- sealed AIBOM record (ledger-anchored evidence) -------------------------

const aibomKind model.Kind = "models.aibom"
const aibomTable = "models_aibom"

const (
	colAIOwned      = "owned_ref"
	colAISerial     = "serial_number"
	colAIContentHsh = "content_hash"
	colAISpecVer    = "spec_version"
	colAICompCount  = "component_count"
	colAILedgerSeq  = "ledger_seq"
	colAILedgerHash = "ledger_hash"
	colAIScopeNote  = "scope_note"
	colAIGenBy      = "generated_by"
	colAIGenAt      = "generated_at"
)

func registerAIBOMSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind: aibomKind, Table: aibomTable, AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colAIOwned, Kind: model.KindText, Indexed: true},
			{Name: colAISerial, Kind: model.KindText},
			{Name: colAIContentHsh, Kind: model.KindText, Indexed: true},
			{Name: colAISpecVer, Kind: model.KindText},
			{Name: colAICompCount, Kind: model.KindInt},
			{Name: colAILedgerSeq, Kind: model.KindInt},
			{Name: colAILedgerHash, Kind: model.KindText, Nullable: true},
			{Name: colAIScopeNote, Kind: model.KindText, Nullable: true},
			{Name: colAIGenBy, Kind: model.KindText, Nullable: true},
			{Name: colAIGenAt, Kind: model.KindText, Nullable: true},
		},
	})
}

type aibomSealDTO struct {
	ID             string `json:"id,omitempty"`
	OwnedRef       string `json:"owned_ref"`
	SerialNumber   string `json:"serial_number"`
	ContentHash    string `json:"content_hash"`
	SpecVersion    string `json:"spec_version"`
	ComponentCount int64  `json:"component_count"`
	LedgerSeq      int64  `json:"ledger_seq"`
	LedgerHash     string `json:"ledger_hash,omitempty"`
	ScopeNote      string `json:"scope_note,omitempty"`
	GeneratedBy    string `json:"generated_by,omitempty"`
	GeneratedAt    string `json:"generated_at,omitempty"`
}

func toAIBOMSealDTO(rec model.Record) aibomSealDTO {
	return aibomSealDTO{
		ID: rec.String(model.ColID), OwnedRef: rec.String(colAIOwned), SerialNumber: rec.String(colAISerial),
		ContentHash: rec.String(colAIContentHsh), SpecVersion: rec.String(colAISpecVer), ComponentCount: rec.Int(colAICompCount),
		LedgerSeq: rec.Int(colAILedgerSeq), LedgerHash: rec.String(colAILedgerHash), ScopeNote: rec.String(colAIScopeNote),
		GeneratedBy: rec.String(colAIGenBy), GeneratedAt: rec.String(colAIGenAt),
	}
}

// handleGenerateAIBOM returns the live AI bill of materials for an owned model
// (read-only export, not sealed). Read-tier; not audited (observer effect).
// ?format=spdx renders the SAME inventory as an SPDX 3.0.1 AI Profile JSON-LD
// document; the default is the CycloneDX 1.6 AIBOM — the seal stays
// CycloneDX-canonical so there is exactly one ledger-anchored truth.
func (m *Module) handleGenerateAIBOM(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "spdx") {
		var doc map[string]any
		err := mc.Data.View(r.Context(), func(sc store.Scope) error {
			inv, err := readModelInventory(r, sc, id)
			if err != nil {
				return err
			}
			doc = buildSPDXAIDocument(inv, time.Now().UTC().Format(time.RFC3339))
			return nil
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
		return
	}
	var (
		bom   cdxBOM
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		b, err := buildAIBOM(r, sc, id)
		if err != nil {
			return err
		}
		bom, found = b, true
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
	writeJSON(w, http.StatusOK, bom)
}

// handleSealAIBOM generates the AIBOM, computes its canonical content hash, anchors
// the current audit-chain head, persists an append-only models.aibom seal record and
// self-audits the seal as the next ledger event — making the AIBOM tamper-evident
// compliance evidence (the same pattern as compliance's evidence package). Write-tier,
// audited. Returns the seal record and the generated BOM.
func (m *Module) handleSealAIBOM(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		seal aibomSealDTO
		bom  cdxBOM
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		b, err := buildAIBOM(r, sc, id)
		if err != nil {
			return err
		}
		hash, err := canonicalAIBOMHash(b)
		if err != nil {
			return err
		}
		head, headOK, err := sc.Audit().Head(r.Context())
		if err != nil {
			return err
		}
		ledgerSeq := int64(0)
		ledgerHash := ""
		if headOK {
			ledgerSeq = head.Seq
			ledgerHash = hex.EncodeToString(head.Hash)
		}
		repo, err := sc.Ext(aibomKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colAIOwned: id.String(), colAISerial: b.SerialNumber, colAIContentHsh: hash,
			colAISpecVer: b.SpecVersion, colAICompCount: int64(len(b.Components)),
			colAILedgerSeq: ledgerSeq, colAILedgerHash: ledgerHash, colAIScopeNote: aibomDisclaimer,
			colAIGenBy: mc.Principal.Actor(), colAIGenAt: time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		seal, bom = toAIBOMSealDTO(rec), b
		// Self-audit the seal as the NEXT chain event after the head it anchors.
		return auditOwned(r.Context(), sc, mc, aibomKind, "seal", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"seal": seal, "aibom": bom})
}

func (m *Module) handleListAIBOMs(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("owned_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colAIOwned, v))
	}
	out := listResponse[aibomSealDTO]{Items: []aibomSealDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(aibomKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toAIBOMSealDTO(rec))
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
