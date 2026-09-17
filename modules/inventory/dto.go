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
	Kind          string   `json:"kind"`
	EntityID      string   `json:"entity_id"`
	Name          string   `json:"name"`
	Ref           string   `json:"ref,omitempty"`
	Status        string   `json:"status"`
	SignalSources []string `json:"signal_sources"`
	Hosts         []string `json:"hosts,omitempty"`
	FirstSeen     string   `json:"first_seen"`
	LastSeen      string   `json:"last_seen"`
	// OccurredAt is the source's own claim about WHEN THE FACT HAPPENED, omitted when it
	// made none. LastSeen above is ours: when this platform last saw the entity.
	OccurredAt      string `json:"occurred_at,omitempty"`
	OccurrenceCount int64  `json:"occurrence_count"`
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
		OccurredAt:      rec.String(colOccurredAt),
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

// observationDTO is ONE stored observation receipt that named a catalog entity,
// projected onto a closed allowlist. It is the unit of the entity observation
// history (GET /entities/{kind}/{id}/observations) and is built field by field
// from validated rows in provenance_read.go — never by serializing the stored
// record — so nothing reaches the wire that is not named here.
//
// What is deliberately absent, and why: the event id and receipt key (delivery
// identity, not evidence), the facts hash and the raw facts JSON (the projection
// is the contract, the encoding is not), the member's Name/Ref/Signal/Host and
// native reference, the source label and binding reference (free-form strings
// received from a connector or references into other modules' frontiers, whose
// universal saneness this module does not certify), edge origin/resource/tool,
// access mode, confidence and cost facts (the access graph is module III's, cost
// accounting module XI's), and anything about the source's CURRENT roster state,
// health, owner, grants or coverage (not recorded here, so not published here).
type observationDTO struct {
	// ReceiptID is the platform receipt id (canonical UUID). One item per receipt:
	// a receipt whose members name this entity twice is still one item.
	ReceiptID string `json:"receipt_id"`
	// EventType is the first-party observation type the receipt recorded:
	// "edge.observed" or "cost.sampled".
	EventType string `json:"event_type"`
	// Registration is the HISTORICAL registration snapshot copied into the receipt
	// when it was received. It says what the producer's registration looked like
	// then; it does not say the source is registered, healthy or readable now.
	Registration observationRegistrationDTO `json:"registration"`
	// SourceOccurredAt is the instant the SOURCE declared for the fact, taken from
	// the validated member that names this entity — the same claim that feeds the
	// catalog entry's occurred_at. Omitted when the source declared none.
	SourceOccurredAt string `json:"source_occurred_at,omitempty"`
	// FirstReceivedAt and LastReceivedAt are this platform's reception clock for
	// the receipt's RETAINED facts: the original delivery and the last delivery
	// that carried equal facts. A conflicting redelivery has its own counters and
	// is not folded into these.
	FirstReceivedAt string `json:"first_received_at"`
	LastReceivedAt  string `json:"last_received_at"`
	// Deliveries counts deliveries of the receipt with equal facts (the original
	// plus exact replays). It is not distinct activity, and it excludes
	// conflicting variants.
	Deliveries int64 `json:"deliveries"`
	// ConflictingRedelivery reports whether at least one redelivery of this receipt
	// carried DIFFERENT facts and was retained separately. It is a boolean by
	// design: the conflicting facts, their count and their times are not published.
	ConflictingRedelivery bool `json:"conflicting_redelivery"`
}

// observationRegistrationDTO is the registration snapshot part of an observation
// item. The three components are emitted ONLY for registration_state
// "registered_snapshot", where the recorded snapshot was complete; an incomplete
// snapshot is published as "invalid" with no components, so a partial identity is
// never presented as one, and "unattributed" records that none was stamped.
type observationRegistrationDTO struct {
	// RegistrationState is "registered_snapshot", "unattributed" or "invalid".
	RegistrationState string `json:"registration_state"`
	// SourceID is the persistent id of the roster row the snapshot named. It is a
	// historical identifier, not a grant to read that roster row.
	SourceID string `json:"source_id,omitempty"`
	// SourceRevision is the roster revision the snapshot says was applied when the
	// observation was produced — not the roster's current revision.
	SourceRevision int64 `json:"source_revision,omitempty"`
	// EnvironmentRef identifies the persistent local execution environment the
	// snapshot recorded; it is not a host or a workspace.
	EnvironmentRef string `json:"environment_ref,omitempty"`
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
