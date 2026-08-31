// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

// entryDTO is a catalog entry: a discovered entity with its provenance and
// liveness. It is the unit of the inventory list and the estate's catalog.
type entryDTO struct {
	Kind            string   `json:"kind"`
	EntityID        string   `json:"entity_id"`
	Name            string   `json:"name"`
	Ref             string   `json:"ref,omitempty"`
	Status          string   `json:"status"`
	SignalSources   []string `json:"signal_sources"`
	Hosts           []string `json:"hosts,omitempty"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	OccurrenceCount int64    `json:"occurrence_count"`
}

func toEntryDTO(rec model.Record) entryDTO {
	hosts := parseSet(rec.String(colHosts))
	if len(hosts) == 0 {
		hosts = nil
	}
	return entryDTO{
		Kind:            rec.String(colEntityKind),
		EntityID:        rec.String(colEntityID),
		Name:            rec.String(colName),
		Ref:             rec.String(colRef),
		Status:          rec.String(colStatus),
		SignalSources:   parseSet(rec.String(colSignalSources)),
		Hosts:           hosts,
		FirstSeen:       rec.String(colFirstSeen),
		LastSeen:        rec.String(colLastSeen),
		OccurrenceCount: rec.Int(colOccurrence),
	}
}

// kindCount is the per-kind tally in the estate summary.
type kindCount struct {
	Active int `json:"active"`
	Stale  int `json:"stale"`
	Total  int `json:"total"`
}

// summaryDTO is the estate overview: counts by entity kind and by signal source.
type summaryDTO struct {
	ByKind    map[string]*kindCount `json:"by_kind"`
	BySource  map[string]int        `json:"by_source"`
	Total     int                   `json:"total"`
	Truncated bool                  `json:"truncated,omitempty"`
}

// detailDTO is a catalog entry plus a minimal projection of the underlying core
// entity it overlays.
type detailDTO struct {
	Entry  entryDTO       `json:"entry"`
	Detail map[string]any `json:"detail,omitempty"`
}

// writeJSON writes v as a JSON response. Modules cannot reach the core API's
// unexported render helper, so each module owns a tiny equivalent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
