// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ratecatalog.go is the model rate catalog — the per-provider/model
// pricing table that resolves list-price rates (micro-USD per 1M tokens) for a
// given instant, enabling the FinOps module to estimate cost when a provider's
// cost API does not report a dollar amount. All monetary amounts are integer
// micro-USD: no floats in money (README.md).

// modelRateDTO is the JSON DTO for a model rate catalog entry. Rates are
// per 1M tokens in micro-USD (integer, no floats). effective_from marks when
// this pricing takes effect; effective_until (optional) marks when it expires
// — a null/empty effective_until means the rate is still current.
type modelRateDTO struct {
	ID                        string `json:"id,omitempty"`
	Provider                  string `json:"provider"`
	Model                     string `json:"model"`
	InputRateMicroUSD         int64  `json:"input_rate_micro_usd"`
	OutputRateMicroUSD        int64  `json:"output_rate_micro_usd"`
	CacheReadRateMicroUSD     int64  `json:"cache_read_rate_micro_usd"`
	CacheCreationRateMicroUSD int64  `json:"cache_creation_rate_micro_usd"`
	EffectiveFrom             string `json:"effective_from"`
	EffectiveUntil            string `json:"effective_until,omitempty"`
	Notes                     string `json:"notes,omitempty"`
	CreatedAt                 string `json:"created_at,omitempty"`
	UpdatedAt                 string `json:"updated_at,omitempty"`
}

// toModelRateDTO maps a store record to the JSON DTO.
func toModelRateDTO(rec model.Record) modelRateDTO {
	return modelRateDTO{
		ID:                        rec.String(model.ColID),
		Provider:                  rec.String(colRateProvider),
		Model:                     rec.String(colRateModel),
		InputRateMicroUSD:         rec.Int(colRateInputMicroUSD),
		OutputRateMicroUSD:        rec.Int(colRateOutputMicroUSD),
		CacheReadRateMicroUSD:     rec.Int(colRateCacheReadMicroUSD),
		CacheCreationRateMicroUSD: rec.Int(colRateCacheCreationMicroUSD),
		EffectiveFrom:             rec.String(colRateEffectiveFrom),
		EffectiveUntil:            rec.String(colRateEffectiveUntil),
		Notes:                     rec.String(colRateNotes),
		CreatedAt:                 rec.String(model.ColCreatedAt),
		UpdatedAt:                 rec.String(model.ColUpdatedAt),
	}
}

// validate returns a human error for a malformed rate entry ("" = valid).
func (d modelRateDTO) validate() string {
	if d.Provider == "" {
		return "provider is required"
	}
	if d.Model == "" {
		return "model is required"
	}
	if d.InputRateMicroUSD <= 0 {
		return "input_rate_micro_usd must be > 0"
	}
	if d.OutputRateMicroUSD <= 0 {
		return "output_rate_micro_usd must be > 0"
	}
	if d.EffectiveFrom == "" {
		return "effective_from is required"
	}
	if _, err := time.Parse(time.RFC3339, d.EffectiveFrom); err != nil {
		return "effective_from must be a valid RFC3339 timestamp"
	}
	if d.EffectiveUntil != "" {
		if _, err := time.Parse(time.RFC3339, d.EffectiveUntil); err != nil {
			return "effective_until must be a valid RFC3339 timestamp"
		}
	}
	return ""
}

// --- HTTP Handlers ----------------------------------------------------------

// handleListModelRates lists rate catalog entries with optional provider/model
// filters, sorted by effective_from descending (most recent first).
func (m *Module) handleListModelRates(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if p := r.URL.Query().Get("provider"); p != "" {
		q.Filters = append(q.Filters, eq(colRateProvider, p))
	}
	if md := r.URL.Query().Get("model"); md != "" {
		q.Filters = append(q.Filters, eq(colRateModel, md))
	}
	q.Sort = []model.Sort{{Column: colRateEffectiveFrom, Desc: true}}
	out := listResponse[modelRateDTO]{Items: []modelRateDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toModelRateDTO(rec))
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

// handleCreateModelRate creates a new rate catalog entry after validating input
// and checking uniqueness of the (provider, model, effective_from) tuple.
func (m *Module) handleCreateModelRate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in modelRateDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out modelRateDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		// Uniqueness check: (provider, model, effective_from) must not already exist.
		existing, _, err := repo.List(r.Context(), model.Query{
			Filters: []model.Filter{
				eq(colRateProvider, in.Provider),
				eq(colRateModel, in.Model),
				eq(colRateEffectiveFrom, in.EffectiveFrom),
			},
			Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			writeJSON(w, http.StatusConflict, errorBody("a rate for this provider/model/effective_from already exists"))
			return nil
		}
		rec := model.Record{
			colRateProvider:              in.Provider,
			colRateModel:                 in.Model,
			colRateInputMicroUSD:         in.InputRateMicroUSD,
			colRateOutputMicroUSD:        in.OutputRateMicroUSD,
			colRateCacheReadMicroUSD:     in.CacheReadRateMicroUSD,
			colRateCacheCreationMicroUSD: in.CacheCreationRateMicroUSD,
			colRateEffectiveFrom:         in.EffectiveFrom,
			colRateNotes:                 in.Notes,
		}
		if in.EffectiveUntil != "" {
			rec[colRateEffectiveUntil] = in.EffectiveUntil
		}
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toModelRateDTO(created)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if out.ID == "" {
		return // conflict already written
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleGetModelRate returns a single rate catalog entry by ID.
func (m *Module) handleGetModelRate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out modelRateDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toModelRateDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateModelRate replaces a rate catalog entry by ID.
func (m *Module) handleUpdateModelRate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in modelRateDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out modelRateDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		rec[colRateProvider] = in.Provider
		rec[colRateModel] = in.Model
		rec[colRateInputMicroUSD] = in.InputRateMicroUSD
		rec[colRateOutputMicroUSD] = in.OutputRateMicroUSD
		rec[colRateCacheReadMicroUSD] = in.CacheReadRateMicroUSD
		rec[colRateCacheCreationMicroUSD] = in.CacheCreationRateMicroUSD
		rec[colRateEffectiveFrom] = in.EffectiveFrom
		if in.EffectiveUntil != "" {
			rec[colRateEffectiveUntil] = in.EffectiveUntil
		} else {
			rec[colRateEffectiveUntil] = nil
		}
		if in.Notes != "" {
			rec[colRateNotes] = in.Notes
		} else {
			rec[colRateNotes] = nil
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toModelRateDTO(updated)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteModelRate removes a rate catalog entry by ID.
func (m *Module) handleDeleteModelRate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		return repo.Delete(r.Context(), id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// --- Resolution (called from comparison.go) ---------------------------------

// resolveRate finds the rate catalog entry effective at time `at` for the given
// provider/model pair. It looks for entries where effective_from <= at AND
// (effective_until is null OR effective_until > at), returning the one with the
// most recent effective_from. Returns found=false if no matching rate exists.
func resolveRate(ctx context.Context, sc store.Scope, provider, modelRef string, at time.Time) (modelRateDTO, bool, error) {
	repo, err := sc.Ext(modelRateKind)
	if err != nil {
		return modelRateDTO{}, false, err
	}
	atStr := model.NewTimestamp(at).String()
	// Query: provider=X AND model=Y AND effective_from <= at, ordered by
	// effective_from DESC so the most recent effective entry comes first.
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			eq(colRateProvider, provider),
			eq(colRateModel, modelRef),
			{Column: colRateEffectiveFrom, Op: model.OpLte, Value: atStr},
		},
		Sort:  []model.Sort{{Column: colRateEffectiveFrom, Desc: true}},
		Limit: listCap,
	})
	if err != nil {
		return modelRateDTO{}, false, err
	}
	// Walk results (already ordered most-recent-first) and return the first one
	// whose effective_until is null/empty (still current) or > at.
	for _, rec := range recs {
		until := rec.String(colRateEffectiveUntil)
		if until == "" {
			return toModelRateDTO(rec), true, nil
		}
		// until is non-empty: the rate has expired if until <= at.
		if until > atStr {
			return toModelRateDTO(rec), true, nil
		}
	}
	return modelRateDTO{}, false, nil
}

// resolveCurrentRate is a convenience for resolveRate at the current instant.
func resolveCurrentRate(ctx context.Context, sc store.Scope, provider, modelRef string) (modelRateDTO, bool, error) {
	return resolveRate(ctx, sc, provider, modelRef, time.Now().UTC())
}
