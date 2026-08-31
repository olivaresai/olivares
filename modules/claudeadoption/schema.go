// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// adoptionMetricKind is the module's owned read-model entity: one row per
// (subject, metric, day, dimension-tuple), the dedup/upsert guard AND the by-name
// aggregation substrate. The table is NOT audited — it is high-frequency automated bus
// ingestion (like the FinOps cost read-model), and its reads are RBAC-gated at the API.
const adoptionMetricKind model.Kind = "adoption.metric"

const adoptionMetricTable = "adoption_metric"

// adoptionDiscrepancyCheckKind is the module-owned ingest-time dedup marker for the
// official-vs-observed comparison. It is intentionally tiny: one row per tenant/day,
// stamped after the previous UTC day has been evaluated, with only a coarse verdict
// summary. The detailed per-metric tuple is used only to hash the emitted finding detail.
const adoptionDiscrepancyCheckKind model.Kind = "adoption.discrepancy_check"

const adoptionDiscrepancyCheckTable = "adoption_discrepancy_check"

// Read-model columns.
const (
	// colNK is the natural key (subject_kind/subject_ref/metric_name/day + canonical
	// dimensions) — the unique upsert guard so a re-pulled day or a re-delivered delta
	// folds onto the SAME row instead of double-counting.
	colNK          = "nk"
	colSubjectKind = "subject_kind"
	colSubjectRef  = "subject_ref"
	colMetricName  = "metric_name"
	colDay         = "day" // UTC calendar day, YYYY-MM-DD
	// Promoted dimensions (the axes the dashboard slices on; nullable — a metric carries
	// only the ones that apply). dim_type = added/removed | input/output/cacheRead/
	// cacheCreation | user/cli; dim_tool = Edit/MultiEdit/Write/NotebookEdit; dim_decision
	// = accept/reject; dim_model = the model id (token-mix).
	colDimType     = "dim_type"
	colDimTool     = "dim_tool"
	colDimDecision = "dim_decision"
	colDimModel    = "dim_model"
	// colTeam is the operator-supplied team label (from OTEL_RESOURCE_ATTRIBUTES);
	// empty when the source carries no team (the Analytics per-developer feed does not).
	colTeam = "team"
	// colSource is the originating connector (provenance/coverage display); not in the key.
	colSource = "source"
	colUnit   = "unit"
	// colValue is the accumulated (delta SUM) or snapshot (latest/max) measure.
	colValue = "value"
	// colAdditive is 1 when the row sums delta increments, 0 when it holds a snapshot.
	colAdditive = "additive"
	// colLastAt is the high-water of contributing datapoint instants: an additive sample
	// older-or-equal is a duplicate/out-of-order re-delivery and is dropped (idempotency).
	colLastAt = "last_at"

	// Discrepancy-check marker columns. colCheckDay is the natural key within a tenant;
	// colCheckVerdict stores coarse JSON such as material_count/worst_metric, never the
	// raw per-metric comparison tuple that feeds the FindingReport.DetailHash.
	colCheckDay       = "day"
	colCheckEvaluated = "evaluated_at"
	colCheckVerdict   = "verdict_summary"
)

// RegisterSchema declares the adoption read-model entity. One unique row per natural
// key (per tenant), so a re-pulled Analytics day or a re-delivered OTLP delta is an
// upsert, never a second row that double-counts.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  adoptionMetricKind,
		Table: adoptionMetricTable,
		Fields: []model.FieldSpec{
			{Name: colNK, Kind: model.KindText},
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colMetricName, Kind: model.KindText, Indexed: true},
			{Name: colDay, Kind: model.KindText, Indexed: true},
			{Name: colDimType, Kind: model.KindText, Nullable: true},
			{Name: colDimTool, Kind: model.KindText, Nullable: true},
			{Name: colDimDecision, Kind: model.KindText, Nullable: true},
			{Name: colDimModel, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colTeam, Kind: model.KindText, Nullable: true, Indexed: true},
			{Name: colSource, Kind: model.KindText, Nullable: true},
			{Name: colUnit, Kind: model.KindText, Nullable: true},
			{Name: colValue, Kind: model.KindInt},
			{Name: colAdditive, Kind: model.KindInt},
			{Name: colLastAt, Kind: model.KindTimestamp},
		},
		Indexes: []model.IndexSpec{{
			// One row per natural key — the ingestion dedup/upsert guard. Leads with
			// tenant_id so it never couples tenants.
			Name:    "adoption_metric_uniq",
			Columns: []string{model.ColTenantID, colNK},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  adoptionDiscrepancyCheckKind,
		Table: adoptionDiscrepancyCheckTable,
		Fields: []model.FieldSpec{
			{Name: colCheckDay, Kind: model.KindText, Indexed: true},
			{Name: colCheckEvaluated, Kind: model.KindTimestamp},
			{Name: colCheckVerdict, Kind: model.KindJSON},
		},
		Indexes: []model.IndexSpec{{
			// One marker per tenant/day. The ingest hook creates this before doing the
			// bounded previous-day comparison; a concurrent unique-key conflict means
			// another writer won and this ingest skips the secondary work.
			Name:    "adoption_discrepancy_check_uniq",
			Columns: []string{model.ColTenantID, colCheckDay},
			Unique:  true,
		}},
	})
}
