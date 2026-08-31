// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// datasets as first-class, governable AIBOM components. A CycloneDX AIBOM
// inventories MODELS *and* DATASETS (the lineage a model is trained/evaluated on);
// EU AI Act Art. 10 / Annex IV, NIST AI RMF MAP and ISO/IEC 42001 A.7.5 all require
// training-data provenance. This entity records the metadata an AIBOM needs —
// MINIMAL-DATA by construction (docs/SECURITY-HARDENING.md): a dataset is a NAME + a content
// reference (URI/registry path) + a content hash + a classification/governance
// label; never the dataset contents. It mirrors the owned-model claim-vs-verified
// idiom (verified is true only when the operator confirmed the recorded provenance).

import (
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const datasetKind model.Kind = "models.dataset"
const datasetTable = "models_dataset"

const (
	colDSName        = "name"
	colDSOwned       = "owned_ref" // optional: the owned_model this dataset feeds (lineage)
	colDSClass       = "classification"
	colDSGovernance  = "governance"
	colDSSource      = "source_ref"
	colDSContentHash = "content_hash"
	colDSContentAlg  = "content_alg"
	colDSVerified    = "verified"
	colDSAttestedBy  = "attested_by"
	colDSAttestedAt  = "attested_at"
	colDSNote        = "note"
)

// dataClassifications is the closed set of CycloneDX-style data classifications
// (kept queryable like the other enums). "other" is the catch-all.
var dataClassifications = set("public", "internal", "confidential", "restricted", "pii", "other")

func registerDatasetSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind: datasetKind, Table: datasetTable,
		Fields: []model.FieldSpec{
			{Name: colDSName, Kind: model.KindText, Indexed: true},
			{Name: colDSOwned, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colDSClass, Kind: model.KindText, Nullable: true},
			{Name: colDSGovernance, Kind: model.KindText, Nullable: true},
			{Name: colDSSource, Kind: model.KindText, Nullable: true},
			{Name: colDSContentHash, Kind: model.KindText, Nullable: true},
			{Name: colDSContentAlg, Kind: model.KindText, Nullable: true},
			{Name: colDSVerified, Kind: model.KindBool, Indexed: true},
			{Name: colDSAttestedBy, Kind: model.KindText, Nullable: true},
			{Name: colDSAttestedAt, Kind: model.KindText, Nullable: true},
			{Name: colDSNote, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{Name: "models_dataset_uniq", Columns: []string{model.ColTenantID, colDSName}, Unique: true}},
	})
}

type datasetDTO struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name"`
	OwnedRef       string `json:"owned_ref,omitempty"`
	Classification string `json:"classification,omitempty"`
	Governance     string `json:"governance,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	ContentHash    string `json:"content_hash,omitempty"`
	ContentAlg     string `json:"content_alg,omitempty"`
	Verified       bool   `json:"verified"`
	AttestedBy     string `json:"attested_by,omitempty"`
	AttestedAt     string `json:"attested_at,omitempty"`
	Note           string `json:"note,omitempty"`
}

func (d *datasetDTO) validate() string {
	d.Name = trimClamp(d.Name)
	if d.Name == "" {
		return "name is required"
	}
	d.Classification = strings.TrimSpace(strings.ToLower(d.Classification))
	if d.Classification == "" {
		d.Classification = "other"
	}
	if !dataClassifications[d.Classification] {
		return "classification must be public, internal, confidential, restricted, pii or other"
	}
	if d.ContentAlg == "" && d.ContentHash != "" {
		d.ContentAlg = "sha256"
	}
	return ""
}

func (d datasetDTO) toRecord(actor, at string) model.Record {
	return model.Record{
		colDSName: d.Name, colDSOwned: trimClamp(d.OwnedRef), colDSClass: d.Classification,
		colDSGovernance: trimClamp(d.Governance), colDSSource: trimClamp(d.SourceRef),
		colDSContentHash: trimClamp(d.ContentHash), colDSContentAlg: trimClamp(d.ContentAlg),
		colDSVerified: d.Verified, colDSAttestedBy: actor, colDSAttestedAt: at, colDSNote: trimClamp(d.Note),
	}
}

func toDatasetDTO(rec model.Record) datasetDTO {
	return datasetDTO{
		ID: rec.String(model.ColID), Name: rec.String(colDSName), OwnedRef: rec.String(colDSOwned),
		Classification: rec.String(colDSClass), Governance: rec.String(colDSGovernance), SourceRef: rec.String(colDSSource),
		ContentHash: rec.String(colDSContentHash), ContentAlg: rec.String(colDSContentAlg), Verified: rec.Bool(colDSVerified),
		AttestedBy: rec.String(colDSAttestedBy), AttestedAt: rec.String(colDSAttestedAt), Note: rec.String(colDSNote),
	}
}

func (m *Module) handleListDatasets(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("owned_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colDSOwned, v))
	}
	out := listResponse[datasetDTO]{Items: []datasetDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(datasetKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDatasetDTO(rec))
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

func (m *Module) handleCreateDataset(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in datasetDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out datasetDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// An optional owned_ref must resolve (lineage references stay sound).
		if err := checkRef(r.Context(), sc, ownedModelKind, in.OwnedRef); err != nil {
			return err
		}
		repo, err := sc.Ext(datasetKind)
		if err != nil {
			return err
		}
		actor := mc.Principal.Actor()
		at := model.NewTimestamp(time.Now()).String()
		rec, err := repo.Create(r.Context(), in.toRecord(actor, at))
		if err != nil {
			return err
		}
		out = toDatasetDTO(rec)
		return auditOwned(r.Context(), sc, mc, datasetKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleDeleteDataset(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.deleteExt(w, r, mc, datasetKind)
}
