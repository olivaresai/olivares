// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/api"
	sdkevent "github.com/olivaresai/olivares/sdk/event"
)

// olSchemaURL is the canonical OpenLineage 2-0-2 RunEvent schema reference.
const olSchemaURL = "https://openlineage.io/spec/2-0-2/OpenLineage.json#/$defs/RunEvent"

// TypeOpenLineageRunEvent is the bus event type for OpenLineage RunEvent payloads.
// It is a module-defined type (not a model.Observation) that travels as unversioned
// JSON on the bus — exactly like TypeGuardrailObserved in sdk/event/observed.go — so
// it does not touch the sealed sdk/model.Observation sum type.
const TypeOpenLineageRunEvent sdkevent.Type = "knowledge.openlineage.run_event"

// olRunEvent is the OpenLineage 2-0-2 RunEvent payload emitted after every governed
// retrieval. It is minimal-data by construction: no chunk text, no query content,
// only identifiers, governance facets and data-flow edges (docs/SECURITY-HARDENING.md).
type olRunEvent struct {
	EventType string      `json:"eventType"`
	EventTime string      `json:"eventTime"`
	Producer  string      `json:"producer"`
	SchemaURL string      `json:"schemaURL"`
	Job       olJob       `json:"job"`
	Run       olRun       `json:"run"`
	Inputs    []olDataset `json:"inputs"`
	Outputs   []olDataset `json:"outputs"`
}

type olJob struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type olRun struct {
	RunID  string         `json:"runId"`
	Facets map[string]any `json:"facets,omitempty"`
}

type olDataset struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Facets    map[string]any `json:"facets,omitempty"`
}

// buildOLEvent constructs an olRunEvent from a governed-retrieval lineage row. It is
// a pure function (no clock, no I/O) so it can be verified without a running module.
// EventTime is left empty — emitOpenLineage stamps it with m.clock.Now() before
// publishing, keeping the clock dependency out of the mapping logic.
//
// OL mapping:
//
//	eventType   "COMPLETE" (allowed) / "FAIL" (denied)
//	job         olivares.knowledge / retrieval/<kbRef>
//	run.runId   lineageID (the DB row — the authoritative governance record)
//	inputs[]    one entry per unique (sourceKind, docRef) from chunkRefs
//	outputs[]   one entry: olivares.knowledge / response/<lineageID>
//	facets      olivares:governance + olivares:provenance
func buildOLEvent(lr lineageRow, lineageID string, embedModel string) olRunEvent {
	eventType := "COMPLETE"
	if lr.decision == decisionDenied {
		eventType = "FAIL"
	}

	// Deduplicate inputs by (sourceKind, docRef): each unique source document
	// is one OL input dataset; multiple chunks from the same document collapse.
	inputs := make([]olDataset, 0, len(lr.chunkRefs))
	seen := map[string]bool{}
	for _, ref := range lr.chunkRefs {
		key := ref.SourceKind + "/" + ref.DocRef
		if seen[key] {
			continue
		}
		seen[key] = true
		inputs = append(inputs, olDataset{
			Namespace: ref.SourceKind,
			Name:      ref.DocRef,
			Facets: map[string]any{
				"dataSource": map[string]string{
					"source_kind": ref.SourceKind,
					"source_ref":  ref.SourceRef,
				},
			},
		})
	}

	return olRunEvent{
		Producer:  Name,
		SchemaURL: olSchemaURL,
		EventType: eventType,
		Job: olJob{
			Namespace: Name,
			Name:      "retrieval/" + lr.kbRef.String(),
		},
		Run: olRun{
			RunID: lineageID,
			Facets: map[string]any{
				"olivares:governance": map[string]any{
					"decision":               lr.decision,
					"egress":                 lr.egress,
					"region":                 lr.region,
					"result_count":           lr.resultCount,
					"dlp_withheld":           lr.dlpWithheld,
					"context_truncated":      lr.contextTruncated,
					"context_dropped_chunks": lr.contextDroppedChunks,
					"context_budget":         lr.contextBudget,
					"context_winning_scope":  lr.contextWinningScope,
				},
				"olivares:provenance": map[string]any{
					"chunk_count": len(lr.chunkRefs),
					"embed_model": embedModel,
					"query_hash":  lr.queryHash,
				},
			},
		},
		Inputs: inputs,
		Outputs: []olDataset{{
			Namespace: Name,
			Name:      "response/" + lineageID,
		}},
	}
}

// emitOpenLineage publishes an OpenLineage RunEvent on the bus immediately after a
// governed retrieval writes its lineage row. It is best-effort: an emission failure
// is logged (never swallowed) but does not fail the retrieval — the DB lineage row
// is the authoritative governance record; the bus event is the open-standard
// integration surface for downstream lineage consumers.
func (m *Module) emitOpenLineage(ctx context.Context, mc api.ModuleContext, lr lineageRow, lineageID string) {
	if m.host == nil {
		return
	}
	ol := buildOLEvent(lr, lineageID, m.embedder.ModelRef())
	now := m.clock.Now().Time()
	ol.EventTime = now.UTC().Format(time.RFC3339)
	if err := m.host.Publish(ctx, sdkevent.Event{
		Type:    TypeOpenLineageRunEvent,
		Tenant:  mc.Tenant.String(),
		Source:  Name,
		Time:    now,
		Payload: ol,
	}); err != nil {
		m.errorf("knowledge: failed to emit OpenLineage event", "lineage_id", lineageID, "err", err)
	}
}
