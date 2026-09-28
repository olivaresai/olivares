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

// Principal declarations of the text and JSON columns above: what each stored
// value says about accounts, with the reader lines that show it.
var (
	pdeclNoneNK          = model.None("a SHA-256 digest of the natural key, compared only for dedup: ingest.go:144, ingest.go:131")
	pdeclNoneSubjectKind = model.None("a metric subject class, compared only with the fixed lens kinds: aggregate.go:83, api.go:233")
	// A developer subject is a reported email or key name; other subjects are
	// session or organization references. It is matched against every alias.
	pdeclScanSubjectRef  = model.Scan(model.ClassEvidence)
	pdeclNoneMetricName  = model.None("a metric name from the closed recognized set: contract.go:32, ingest.go:37")
	pdeclNoneDay         = model.None("a UTC calendar day (YYYY-MM-DD): ingest.go:40, ingest.go:81")
	pdeclNoneDimType     = model.None("a breakdown type compared only with fixed type values: aggregate.go:42, aggregate.go:73")
	pdeclNoneDimTool     = model.None("a tool name, tallied and rendered only: aggregate.go:54, aggregate.go:126")
	pdeclNoneDimDecision = model.None("an edit decision compared only with the reject value: aggregate.go:60")
	pdeclNoneDimModel    = model.None("a model id, summed per model and rendered: aggregate.go:79, aggregate.go:106")
	pdeclNoneTeam        = model.None("an operator team label, grouped and rendered only: aggregate.go:86, api.go:140")
	pdeclNoneSource      = model.None("the name of the publishing connector, written only: adoption.go:111, ingest.go:121")
	pdeclNoneUnit        = model.None("a measure unit label, written only: ingest.go:122")
	pdeclVerdictSummary  = model.Nested(discrepancyVerdictSummary{}, model.ClassEvidence,
		model.Leaf("day", model.None("the evaluated UTC day: discrepancy.go:80, discrepancy.go:339")),
		model.Leaf("worst_metric", model.None("a metric name from the fixed comparison set: discrepancy.go:47, discrepancy.go:340")),
		model.Leaf("severity", model.None("a finding severity from a closed set: discrepancy.go:265, sdk/model/enums.go:153")),
		model.Leaf("directions{key}", model.None("a comparison direction from a closed set, or the truncation marker: discrepancy.go:30, discrepancy.go:109, discrepancy.go:334")),
		model.Leaf("thresholds.floor{key}", model.None("a metric name from the fixed comparison set: discrepancy.go:47, discrepancy.go:372")),
	)
)

// RegisterSchema declares the adoption read-model entity. One unique row per natural
// key (per tenant), so a re-pulled Analytics day or a re-delivered OTLP delta is an
// upsert, never a second row that double-counts.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  adoptionMetricKind,
		Table: adoptionMetricTable,
		Fields: []model.FieldSpec{
			{Name: colNK, Kind: model.KindText, Principal: pdeclNoneNK},
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true, Principal: pdeclNoneSubjectKind},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true, Principal: pdeclScanSubjectRef},
			{Name: colMetricName, Kind: model.KindText, Indexed: true, Principal: pdeclNoneMetricName},
			{Name: colDay, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDay},
			{Name: colDimType, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDimType},
			{Name: colDimTool, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDimTool},
			{Name: colDimDecision, Kind: model.KindText, Nullable: true, Principal: pdeclNoneDimDecision},
			{Name: colDimModel, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneDimModel},
			{Name: colTeam, Kind: model.KindText, Nullable: true, Indexed: true, Principal: pdeclNoneTeam},
			{Name: colSource, Kind: model.KindText, Nullable: true, Principal: pdeclNoneSource},
			{Name: colUnit, Kind: model.KindText, Nullable: true, Principal: pdeclNoneUnit},
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
			{Name: colCheckDay, Kind: model.KindText, Indexed: true, Principal: pdeclNoneDay},
			{Name: colCheckEvaluated, Kind: model.KindTimestamp},
			{Name: colCheckVerdict, Kind: model.KindJSON, Principal: pdeclVerdictSummary},
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
