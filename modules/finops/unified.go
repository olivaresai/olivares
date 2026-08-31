// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CostProvenance classifies where a surface's cost data comes from. Anthropic's
// admin APIs (Usage & Cost, Claude Code Analytics, Enterprise Analytics) cover
// ONLY the first-party Claude API surfaces (direct + claude-platform-aws); the
// other surfaces (Bedrock, Vertex, Foundry) are excluded by design — their data
// arrives through the cloud-provider connectors (bedrock, vertex, azure-openai).
// The unified view marks each surface's data with its provenance so a consumer
// never confuses billed API data with estimated cloud-connector data.
type CostProvenance string

const (
	// ProvenanceAdminBilled is cost from the Anthropic cost_report (billed, per-workspace/day).
	ProvenanceAdminBilled CostProvenance = "admin-api-billed"
	// ProvenanceAdminEstimated is cost from the Anthropic usage_report or Claude Code
	// Analytics (estimated from list pricing, per-model/per-key granularity).
	ProvenanceAdminEstimated CostProvenance = "admin-api-estimated"
	// ProvenanceCloudConnector is cost from a cloud-provider connector (bedrock, vertex,
	// azure-openai) — the provider's own billing/usage data, never Anthropic's.
	ProvenanceCloudConnector CostProvenance = "cloud-connector-derived"
	// ProvenanceNone means no cost data is available for this surface (the surface
	// exists but no connector has emitted samples for it).
	ProvenanceNone CostProvenance = "no-data"
)

// unifiedSurfaceBucket is one (surface, model, cost_type, workspace) cell in the
// unified cross-surface cost view.
type unifiedSurfaceBucket struct {
	Surface      string         `json:"surface"`
	Model        string         `json:"model"`
	CostType     string         `json:"cost_type"`
	Workspace    string         `json:"workspace"`
	CostMicroUSD int64          `json:"cost_micro_usd"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	Samples      int            `json:"samples"`
	Provenance   CostProvenance `json:"provenance"`
}

// unifiedResponse is the cross-surface unified cost view that Anthropic does NOT
// provide: it joins the admin-API surfaces (direct, claude-platform-aws) with the
// cloud-connector surfaces (bedrock, vertex, foundry) in a single attribution by
// (surface, model, cost_type, workspace), each cell tagged with its data provenance.
type unifiedResponse struct {
	Since         string                 `json:"since,omitempty"`
	Until         string                 `json:"until,omitempty"`
	TotalMicroUSD int64                  `json:"total_micro_usd"`
	Surfaces      []unifiedSurfaceBucket `json:"surfaces"`
	Truncated     bool                   `json:"truncated,omitempty"`
}

// adminAPISurfaces are the surface/gateway values whose cost data comes from the
// Anthropic admin APIs (Usage & Cost + Claude Code Analytics + cost_report). They
// correspond to surfaces.go entries with Admin: true.
var adminAPISurfaces = map[string]bool{
	"direct":              true,
	"claude-platform-aws": true,
}

// classifyProvenance determines the data provenance of a cost sample based on its
// gateway (surface) and its provenance column (estimated/billed).
func classifyProvenance(gateway, prov string) CostProvenance {
	if adminAPISurfaces[gateway] {
		if prov == provenanceBilled {
			return ProvenanceAdminBilled
		}
		return ProvenanceAdminEstimated
	}
	if gateway != "" {
		return ProvenanceCloudConnector
	}
	return ProvenanceAdminEstimated
}

type unifiedKey struct {
	surface, mdl, costType, workspace string
	provenance                        CostProvenance
}

// unifiedCrossSurface builds the unified cross-surface cost view over a time window.
// It scans the FULL cost stream (estimated + billed) and classifies each sample's
// provenance from its gateway × provenance columns. The billed stream is NOT excluded
// here (unlike the default analytics which exclude billed to avoid double-counting)
// because the unified view explicitly labels provenance — a consumer sees that
// "direct" has both admin-api-billed and admin-api-estimated rows and can choose
// which to use.
func unifiedCrossSurface(ctx context.Context, sc store.Scope, since time.Time, hasSince bool, until time.Time, hasUntil bool) (unifiedResponse, error) {
	// No estimatedFilter — we want both estimated and billed streams, labeled.
	var filters []model.Filter
	if hasSince {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(since).String()})
	}
	if hasUntil {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpLte, Value: model.NewTimestamp(until).String()})
	}

	buckets := map[unifiedKey]*unifiedSurfaceBucket{}
	var total int64
	trunc, err := scanSamples(ctx, sc, filters, func(r model.Record) {
		gw := r.String(colGateway)
		mdl := r.String(colModelRef)
		ct := r.String(colCostType)
		ws := r.String(colWorkspaceRef)
		prov := r.String(colProvenance)

		cp := classifyProvenance(gw, prov)
		k := unifiedKey{gw, mdl, ct, ws, cp}
		b := buckets[k]
		if b == nil {
			b = &unifiedSurfaceBucket{
				Surface:    gw,
				Model:      mdl,
				CostType:   ct,
				Workspace:  ws,
				Provenance: cp,
			}
			buckets[k] = b
		}
		c := r.Int(colCostMicroUSD)
		b.CostMicroUSD += c
		b.InputTokens += r.Int(colInputTokens)
		b.OutputTokens += r.Int(colOutputTokens)
		b.Samples++
		total += c
	})
	if err != nil {
		return unifiedResponse{}, err
	}

	sorted := make([]unifiedSurfaceBucket, 0, len(buckets))
	for _, b := range buckets {
		sorted = append(sorted, *b)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CostMicroUSD != sorted[j].CostMicroUSD {
			return sorted[i].CostMicroUSD > sorted[j].CostMicroUSD
		}
		c := strings.Compare(sorted[i].Surface, sorted[j].Surface)
		if c != 0 {
			return c < 0
		}
		return sorted[i].Model < sorted[j].Model
	})

	out := unifiedResponse{TotalMicroUSD: total, Surfaces: sorted, Truncated: trunc}
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}
