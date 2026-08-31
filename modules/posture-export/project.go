// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package postureexport

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	accessmap "github.com/olivaresai/olivares/modules/access-map"
)

// exportNote is the honest provenance label on every export: the towers' ingest
// surfaces are not verified against a primary source (has no tower ingest
// API), so this is a neutral posture projection to PULL or route through a generic
// sink — not a claim of a working native push.
const exportNote = "Read-only ground-truth posture for control-tower enrichment (Agent 365 / ServiceNow AI Control Tower). Minimal-data: refs/hashes/relations only. Tower ingest formats are unverified; pull this projection or route it through a configured sink."

// exportDocument is the posture projection a control tower ingests.
type exportDocument struct {
	Tenant             string          `json:"tenant"`
	Note               string          `json:"note"`
	Inventory          []inventoryItem `json:"inventory"`
	InventoryTruncated bool            `json:"inventory_truncated"`
	Drift              driftSummary    `json:"posture_drift"`
	Findings           []findingItem   `json:"findings"`
	FindingsTruncated  bool            `json:"findings_truncated"`
}

// inventoryItem is one discovered entity in the inventory catalog (minimal-data).
type inventoryItem struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name,omitempty"`
	Ref             string   `json:"ref,omitempty"`
	Status          string   `json:"status"`
	SignalSources   []string `json:"signal_sources,omitempty"`
	Hosts           []string `json:"hosts,omitempty"`
	OccurrenceCount int64    `json:"occurrence_count"`
	FirstSeen       string   `json:"first_seen,omitempty"`
	LastSeen        string   `json:"last_seen,omitempty"`
}

// driftSummary is the least-privilege drift posture — the headline being the
// observed-but-not-permitted accesses (ARCHITECTURE.md).
type driftSummary struct {
	// JSONArray, not []driftEdge: the edges are accumulated with append into the
	// zero value, so a tenant with no drift — the posture an install SHOULD have —
	// would otherwise export unexpected_accesses:null next to unexpected_count:0.
	UnexpectedAccesses api.JSONArray[driftEdge] `json:"unexpected_accesses"`
	UnexpectedCount    int                      `json:"unexpected_count"`
	UnusedGrantCount   int                      `json:"unused_grant_count"`
	InventoryGrant     int                      `json:"inventory_grant_count"`
	Truncated          bool                     `json:"truncated"`
}

// driftEdge is one observed-but-unpermitted access (ids and classification only).
type driftEdge struct {
	OriginKind string `json:"origin_kind"`
	OriginID   string `json:"origin_id"`
	ResourceID string `json:"resource_id"`
	Mode       string `json:"mode"`
}

// findingItem is one security finding (minimal-data: the detail is only its hash).
type findingItem struct {
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	Status      string         `json:"status"`
	Source      string         `json:"source"`
	SubjectKind string         `json:"subject_kind,omitempty"`
	SubjectID   string         `json:"subject_id,omitempty"`
	Title       string         `json:"title,omitempty"`
	DetailHash  string         `json:"detail_hash,omitempty"`
	OccurredAt  string         `json:"occurred_at,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// severityRank orders the finding severity for an in-Go floor filter (the column is
// text and NOT lexically ordered). 0 = unknown/empty.
func severityRank(s model.Severity) int {
	switch s {
	case model.SeverityLow:
		return 1
	case model.SeverityMedium:
		return 2
	case model.SeverityHigh:
		return 3
	case model.SeverityCritical:
		return 4
	default:
		return 0
	}
}

// readInventory projects the active inventory catalog, optionally filtered by entity
// kind. Free-form ref/name/host strings get a defensive redact pass.
func readInventory(ctx context.Context, sc store.Scope, kind string) ([]inventoryItem, bool, error) {
	repo, err := sc.Ext(model.Kind("inventory.catalog_entry"))
	if err != nil {
		return nil, false, err
	}
	filters := []model.Filter{{Column: "status", Op: model.OpEq, Value: "active"}}
	if kind != "" {
		filters = append(filters, model.Filter{Column: "entity_kind", Op: model.OpEq, Value: kind})
	}
	recs, page, err := repo.List(ctx, model.Query{Filters: filters, Limit: exportInventoryCap})
	if err != nil {
		return nil, false, err
	}
	out := make([]inventoryItem, 0, len(recs))
	for _, rec := range recs {
		out = append(out, inventoryItem{
			Kind:            rec.String("entity_kind"),
			Name:            redact.Clean(rec.String("name")),
			Ref:             redact.Clean(rec.String("ref")),
			Status:          rec.String("status"),
			SignalSources:   parseStrList(rec.String("signal_sources")),
			Hosts:           cleanList(parseStrList(rec.String("hosts"))),
			OccurrenceCount: rec.Int("occurrence_count"),
			FirstSeen:       rec.String("first_seen"),
			LastSeen:        rec.String("last_seen"),
		})
	}
	return out, page.HasMore, nil
}

// readDrift projects the reconciled least-privilege drift (the access-map seam owns
// the reconciliation; this caller already holds the audited scope). The headline
// unexpected accesses are bounded; the counts and the truncation flag are exact.
func readDrift(ctx context.Context, sc store.Scope) (driftSummary, error) {
	diff, err := accessmap.ReconciledDrift(ctx, sc, model.Query{Limit: exportDriftCap})
	if err != nil {
		return driftSummary{}, err
	}
	out := driftSummary{
		UnexpectedCount:  len(diff.UnexpectedAccesses),
		UnusedGrantCount: len(diff.UnusedGrants),
		InventoryGrant:   len(diff.InventoryGrants),
		Truncated:        diff.Truncated,
	}
	for _, d := range diff.UnexpectedAccesses {
		out.UnexpectedAccesses = append(out.UnexpectedAccesses, driftEdge{
			OriginKind: d.Edge.OriginKind,
			OriginID:   d.Edge.OriginID.String(),
			ResourceID: d.Edge.ResourceID.String(),
			Mode:       string(d.Edge.Mode),
		})
	}
	return out, nil
}

// readFindings projects security findings, applying the severity floor and the
// category filter IN GO (the severity column is not ordered, and there is no category
// column — category matches a finding kind OR subject_kind). Title/subject/metadata
// get a defensive redact pass.
func readFindings(ctx context.Context, sc store.Scope, floor model.Severity, category string) ([]findingItem, bool, error) {
	recs, page, err := sc.Findings().List(ctx, model.Query{Limit: exportFindingsCap})
	if err != nil {
		return nil, false, err
	}
	floorRank := severityRank(floor)
	out := make([]findingItem, 0, len(recs))
	for _, f := range recs {
		if floorRank > 0 && severityRank(f.Severity) < floorRank {
			continue
		}
		if category != "" && f.Kind != category && f.SubjectKind != category {
			continue
		}
		out = append(out, findingItem{
			Kind:        f.Kind,
			Severity:    string(f.Severity),
			Status:      string(f.Status),
			Source:      f.Source,
			SubjectKind: f.SubjectKind,
			SubjectID:   f.SubjectID.String(),
			Title:       redact.Clean(f.Title),
			DetailHash:  hex.EncodeToString(f.DetailHash),
			OccurredAt:  f.OccurredAt.String(),
			Metadata:    cleanMeta(f.Metadata),
		})
	}
	return out, page.HasMore, nil
}

// parseStrList decodes a JSON string-array column into a slice (best-effort).
func parseStrList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// cleanList applies the defensive redact pass to each string.
func cleanList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redact.Clean(s)
	}
	return out
}

// cleanMeta applies the defensive redact pass to every string value in a finding's
// free-form metadata (the most likely place a stray secret could ride out). Non-string
// values pass through unchanged; the map is copied (never mutating the source).
func cleanMeta(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = redact.Clean(s)
		} else {
			out[k] = v
		}
	}
	return out
}
