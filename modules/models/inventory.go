// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// the shared inventory reader behind the model-card and SPDX 3.0 AI Profile
// exports. It reads exactly what the AIBOM emitter (aibom.go) reads — the owned
// model, its versions, its lineage datasets, the per-version signed-admission verdict
// and the provider's GPAI posture — once, as DTOs, so every export renders the SAME
// underlying inventory and none can drift from another. Read-only; an absent sibling
// row is omitted or reported absent, never fabricated (docs/SECURITY-HARDENING.md).

import (
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// modelInventory is the one-pass read of everything the exports render.
type modelInventory struct {
	Owned    ownedModelDTO
	Versions []modelVersionDTO
	Datasets []datasetDTO
	// AdmissionByVersion holds the signed-admission verdict per version id; a version
	// without a verdict is simply absent from the map (honest not_recorded).
	AdmissionByVersion map[string]modelAdmissionDTO
	// GPAIPostureRecorded/GPAIPostureVerified carry the provider's GPAI posture
	// claim-vs-verified state (FIN-13); both false when no posture row exists.
	GPAIPostureRecorded bool
	GPAIPostureVerified bool
	// VersionCreatedAt/DatasetCreatedAt map record id → base created_at (registry
	// registration time) — the SPDX export's disclosed stand-in for unrecorded
	// publisher timestamps.
	VersionCreatedAt map[string]string
	DatasetCreatedAt map[string]string
}

// readModelInventory assembles the inventory for one owned model in the caller's
// (read) transaction.
func readModelInventory(r *http.Request, sc store.Scope, ownedID model.ID) (modelInventory, error) {
	inv := modelInventory{AdmissionByVersion: map[string]modelAdmissionDTO{}}

	repo, err := sc.Ext(ownedModelKind)
	if err != nil {
		return inv, err
	}
	rec, err := repo.Get(r.Context(), ownedID)
	if err != nil {
		return inv, err
	}
	inv.Owned = toOwnedModelDTO(rec)

	verRepo, err := sc.Ext(modelVersionKind)
	if err != nil {
		return inv, err
	}
	verRecs, err := listAllExt(r.Context(), verRepo, eq(colVerOwned, inv.Owned.ID))
	if err != nil {
		return inv, err
	}
	for _, vr := range verRecs {
		inv.Versions = append(inv.Versions, toModelVersionDTO(vr))
	}
	inv.VersionCreatedAt = captureCreatedAt(verRecs)

	dsRepo, err := sc.Ext(datasetKind)
	if err != nil {
		return inv, err
	}
	dsRecs, err := listAllExt(r.Context(), dsRepo, eq(colDSOwned, inv.Owned.ID))
	if err != nil {
		return inv, err
	}
	for _, dr := range dsRecs {
		inv.Datasets = append(inv.Datasets, toDatasetDTO(dr))
	}
	inv.DatasetCreatedAt = captureCreatedAt(dsRecs)

	admRepo, err := sc.Ext(modelAdmissionKind)
	if err == nil {
		for _, ver := range inv.Versions {
			recs, _, lerr := admRepo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colAdmVersion, ver.ID)}, Limit: 1})
			if lerr == nil && len(recs) > 0 {
				inv.AdmissionByVersion[ver.ID] = toModelAdmissionDTO(recs[0])
			}
		}
	}

	if inv.Owned.ProviderRef != "" {
		if pRepo, perr := sc.Ext(GPAIPostureKind); perr == nil {
			recs, _, lerr := pRepo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colGPAIProvider, inv.Owned.ProviderRef)}, Limit: 1})
			if lerr == nil && len(recs) > 0 {
				inv.GPAIPostureRecorded = true
				inv.GPAIPostureVerified = recs[0].Bool(colGPAIVerified)
			}
		}
	}

	return inv, nil
}
