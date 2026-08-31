// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// extLister is the List surface of an extension repo — enough for listAllExt to walk
// pages without depending on the concrete repo type (and to be unit-tested with a fake).
type extLister interface {
	List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error)
}

// listAllPages walks EVERY page of an internal list and returns the full result set, so
// a GENERATED document/BOM (AIBOM, model card, SPDX) covers the whole inventory rather
// than being silently truncated at the first listCap page. Keyset (cursor) pagination
// requires the store's default id ordering, so this passes NO Sort; a caller that needs
// another order sorts the result in memory. It terminates when a page reports no more
// rows or yields no next cursor (a custom-sort query never paginates), so it cannot loop
// forever. The list closure adapts both typed core repos and record-based extension repos.
func listAllPages[T any](list func(model.Query) ([]T, model.Page, error), filters ...model.Filter) ([]T, error) {
	var out []T
	q := model.Query{Filters: filters, Limit: listCap}
	for {
		recs, page, err := list(q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// listAllExt adapts an extension repo to the shared page walker.
func listAllExt(ctx context.Context, repo extLister, filters ...model.Filter) ([]model.Record, error) {
	return listAllPages(func(q model.Query) ([]model.Record, model.Page, error) {
		return repo.List(ctx, q)
	}, filters...)
}

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

// capabilityDTO is one declared API-feature flag with whether the model has it.
type pricingDTO struct {
	InputPerMTokUSD        float64 `json:"input_per_mtok_usd"`
	OutputPerMTokUSD       float64 `json:"output_per_mtok_usd"`
	CacheWritePerMTokUSD   float64 `json:"cache_write_per_mtok_usd,omitempty"`
	CacheWrite1hPerMTokUSD float64 `json:"cache_write_1h_per_mtok_usd,omitempty"`
	CacheReadPerMTokUSD    float64 `json:"cache_read_per_mtok_usd,omitempty"`
	Currency               string  `json:"currency"`
	AsOf                   string  `json:"as_of"`
	Source                 string  `json:"source"`
}

func toPricingDTO(p *mp.ModelPricing) *pricingDTO {
	if p == nil {
		return nil
	}
	return &pricingDTO{
		InputPerMTokUSD: p.InputPerMTokUSD, OutputPerMTokUSD: p.OutputPerMTokUSD,
		CacheWritePerMTokUSD: p.CacheWritePerMTokUSD, CacheWrite1hPerMTokUSD: p.CacheWrite1hPerMTokUSD,
		CacheReadPerMTokUSD: p.CacheReadPerMTokUSD,
		Currency:            p.Currency, AsOf: p.AsOf, Source: string(p.Source),
	}
}

// catalogModelDTO is one declared reference model in the catalog view.
type catalogModelDTO struct {
	Family                  string      `json:"family"`
	ProviderRef             string      `json:"provider_ref"`
	Capabilities            []string    `json:"capabilities"`
	CapsToConfirm           bool        `json:"caps_to_confirm,omitempty"`
	ContextWindow           int64       `json:"context_window,omitempty"`
	MaxOutputTokens         int64       `json:"max_output_tokens,omitempty"`
	Modality                string      `json:"modality,omitempty"`
	Pricing                 *pricingDTO `json:"pricing,omitempty"`
	ServiceTierEligibility  []string    `json:"service_tier_eligibility,omitempty"`
	DataResidency           []string    `json:"data_residency,omitempty"`
	USInferenceBurndownMult float64     `json:"us_inference_burndown_mult,omitempty"`
}

func toCatalogModelDTO(r referenceModel) catalogModelDTO {
	return catalogModelDTO{
		Family: r.Family, ProviderRef: r.ProviderRef,
		Capabilities: capStrings(r.Capabilities), CapsToConfirm: r.CapsToConfirm,
		ContextWindow: r.ContextWindow, MaxOutputTokens: r.MaxOutputTokens,
		Modality: r.Modality, Pricing: toPricingDTO(r.Pricing),
		ServiceTierEligibility:  r.ServiceTierEligibility,
		DataResidency:           r.DataResidency,
		USInferenceBurndownMult: r.USInferenceBurndownMult,
	}
}

// governedModelDTO is a core Model under governance, with its enrichment.
type governedModelDTO struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ProviderID         string   `json:"provider_id,omitempty"`
	Provider           string   `json:"provider,omitempty"`
	Family             string   `json:"family,omitempty"`
	ContextWindow      int64    `json:"context_window,omitempty"`
	InputCostMicroUSD  int64    `json:"input_cost_micro_usd"`
	OutputCostMicroUSD int64    `json:"output_cost_micro_usd"`
	Modality           string   `json:"modality,omitempty"`
	Status             string   `json:"status"`
	Capabilities       []string `json:"capabilities,omitempty"`
	Enriched           bool     `json:"enriched"`
}

func toGovernedModelDTO(m model.Model) governedModelDTO {
	d := governedModelDTO{
		ID: m.ID.String(), Name: m.Name, ProviderID: optID(m.ProviderID),
		Family: m.Family, ContextWindow: m.ContextWindow,
		InputCostMicroUSD: m.InputCostMicroUSD, OutputCostMicroUSD: m.OutputCostMicroUSD,
		Modality: m.Modality, Status: string(m.Status),
	}
	if m.Metadata != nil {
		if caps, ok := m.Metadata[metaCapabilities].([]any); ok {
			for _, c := range caps {
				if s, ok := c.(string); ok {
					d.Capabilities = append(d.Capabilities, s)
				}
			}
		}
		if e, ok := m.Metadata[metaEnriched].(bool); ok {
			d.Enriched = e
		}
	}
	return d
}

// capStrings renders a capability slice as plain strings.
func capStrings(caps []mp.Capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	return out
}

// optID renders a possibly-zero id as a string or "".
func optID(id model.ID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}

// listQuery builds a List query from ?limit and ?cursor.
func listQuery(r *http.Request) model.Query {
	q := model.Query{}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			q.Limit = n
		}
	}
	return q
}

// eq is a shorthand for an equality filter.
func eq(col, val string) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// decodeJSON reads a JSON body into v, bounding the read so a malformed or huge
// body cannot exhaust memory. It returns false (and writes a 400) on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	// A BODY IS ONE JSON DOCUMENT (2026-08-06). Decode reads the FIRST value and stops,
	// so `{...}{...}` used to decode the first, silently discard the rest and perform a
	// durable mutation returning 201. Measured against a live engine on the models route,
	// with the created row read back by a separate GET; core/api/render.go has rejected
	// this since it was written, and 21 of the 22 copies of this helper had drifted from
	// it. A concatenation error becomes an apparently correct action, and two layers can
	// disagree about which document the request meant.
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	return true
}

// errorBody is the small error envelope module endpoints return.
func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeStoreError maps a store error to an HTTP status. Everything except this
// module's own conflict wording is api.StoreErrorStatus (core/api/moduleerrors.go),
// the ONE mapping the whole product shares — see the note there for what the
// thirty-six hand-written copies had drifted into.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, nil)
	case errors.Is(err, store.ErrConflict):
		// KEPT LOCAL: this module answers "version conflict" where the shared mapping
		// says "conflict". Two of the thirty-six copies word it this way; centralizing
		// the mapping is not license to change a message on the wire that nothing in
		// the tree tests and a client may be reading.
		writeJSON(w, http.StatusConflict, errorBody("version conflict"))
	default:
		status, msg, _ := api.StoreErrorStatus(err)
		writeJSON(w, status, errorBody(msg))
	}
}
