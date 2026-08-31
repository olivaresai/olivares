// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Model.Metadata keys the module writes when it enriches a model. The capability
// set and the precise USD/MTok prices live here; the coarse per-token integer
// lives in the typed Model.*CostMicroUSD fields.
const (
	metaEnriched          = "enriched"
	metaCapabilities      = "capabilities"
	metaInPerMTok         = "input_per_mtok_usd"
	metaOutPerMTok        = "output_per_mtok_usd"
	metaCacheWritePerMTok = "cache_write_per_mtok_usd"
	metaCacheReadPerMTok  = "cache_read_per_mtok_usd"
	metaPricingAsOf       = "pricing_as_of"
	metaPricingSource     = "pricing_source"
)

// findOne returns the first entity matching the AND of filters, or ok=false.
func findOne[T any](ctx context.Context, repo store.Repository[T], filters ...model.Filter) (T, bool, error) {
	var zero T
	list, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return zero, false, err
	}
	if len(list) == 0 {
		return zero, false, nil
	}
	return list[0], true, nil
}

// enrichFromCost find-or-creates the provider and model a cost sample names and
// enriches the model with the declared reference catalog (family, context window,
// modality, per-token pricing and the capability set). It is idempotent: once a
// model carries the enrichment the function returns without a write, so the
// at-least-once cost stream does not churn versions. It runs as one Mutate so the
// provider, model and enrichment commit atomically.
//
// It mirrors inventory's discovery (find-or-create by natural key) rather than
// depending on inventory having run first: module X must work whether or not the
// inventory module is loaded.
func (m *Module) enrichFromCost(ctx context.Context, tenant model.TenantID, providerRef, modelRef string) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		var providerID model.ID
		if providerRef != "" {
			id, err := foProvider(ctx, sc, providerRef)
			if err != nil {
				return err
			}
			providerID = id
		}
		if modelRef == "" {
			return nil
		}
		return m.enrichModel(ctx, sc, modelRef, providerID)
	})
}

// foProvider find-or-creates a provider by its natural reference (its name equals
// the connector's ProviderRef), returning its id.
func foProvider(ctx context.Context, sc store.Scope, ref string) (model.ID, error) {
	if p, ok, err := findOne(ctx, sc.Providers(), eq("name", ref)); err != nil {
		return "", err
	} else if ok {
		return p.ID, nil
	}
	p, err := sc.Providers().Create(ctx, model.Provider{
		Name:   ref,
		Kind:   ref,
		Status: model.StatusActive,
	})
	return p.ID, err
}

// enrichModel find-or-creates the model by (name, provider) and applies the
// declared reference enrichment when a family matches. A model with no declared
// family is left as-is (no invented pricing).
func (m *Module) enrichModel(ctx context.Context, sc store.Scope, modelRef string, providerID model.ID) error {
	filters := []model.Filter{eq("name", modelRef)}
	if !providerID.IsZero() {
		filters = append(filters, eq("provider_id", providerID.String()))
	}
	md, ok, err := findOne(ctx, sc.Models(), filters...)
	if err != nil {
		return err
	}
	if !ok {
		md, err = sc.Models().Create(ctx, model.Model{
			Name:       modelRef,
			ProviderID: providerID,
			Status:     model.StatusActive,
		})
		if err != nil {
			return err
		}
	}

	ref, found := lookupReference(modelRef)
	if !found {
		return nil // no declared family — never invent pricing/capabilities
	}
	// A declared family may carry no verified price (Pricing nil, e.g. the Mythos
	// preview: "never fabricated"). That is a statement about OUR reference, not a
	// statement that the model is free, so it must not reach the cost columns: an
	// operator-set per-token cost is the only price such a model has, and writing
	// the zero default over it destroys the figure silently.
	hasPrice := ref.Pricing != nil
	in, out := perTokenMicroUSD(0), perTokenMicroUSD(0)
	if hasPrice {
		in = perTokenMicroUSD(ref.Pricing.InputPerMTokUSD)
		out = perTokenMicroUSD(ref.Pricing.OutputPerMTokUSD)
	}
	if alreadyEnriched(md, ref, in, out, hasPrice) {
		return nil
	}
	md.Family = ref.Family
	md.ContextWindow = ref.ContextWindow
	md.Modality = ref.Modality
	if hasPrice {
		md.InputCostMicroUSD = in
		md.OutputCostMicroUSD = out
	}
	md.Metadata = enrichMetadata(md.Metadata, ref)
	_, err = sc.Models().Update(ctx, md)
	return err
}

// alreadyEnriched reports whether md already carries this reference's enrichment,
// so a re-delivered cost sample skips the update (idempotency).
func alreadyEnriched(md model.Model, ref referenceModel, in, out int64, hasPrice bool) bool {
	if md.Metadata == nil {
		return false
	}
	enriched, _ := md.Metadata[metaEnriched].(bool)
	if !enriched || md.Family != ref.Family || md.ContextWindow != ref.ContextWindow {
		return false
	}
	if !hasPrice {
		// The reference declares no price, so the cost columns are not ours to
		// compare: whatever they hold came from the operator and stays.
		return true
	}
	return md.InputCostMicroUSD == in && md.OutputCostMicroUSD == out
}

// enrichMetadata returns md's metadata with the declared capability set and the
// precise USD/MTok pricing merged in (preserving any operator-set keys).
func enrichMetadata(existing map[string]any, ref referenceModel) map[string]any {
	out := map[string]any{}
	for k, v := range existing {
		out[k] = v
	}
	out[metaEnriched] = true
	out[metaCapabilities] = capStrings(ref.Capabilities)
	if ref.Pricing != nil {
		out[metaInPerMTok] = ref.Pricing.InputPerMTokUSD
		out[metaOutPerMTok] = ref.Pricing.OutputPerMTokUSD
		out[metaCacheWritePerMTok] = ref.Pricing.CacheWritePerMTokUSD
		out[metaCacheReadPerMTok] = ref.Pricing.CacheReadPerMTokUSD
		out[metaPricingAsOf] = ref.Pricing.AsOf
		out[metaPricingSource] = string(ref.Pricing.Source)
	}
	return out
}
