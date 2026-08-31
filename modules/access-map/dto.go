// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
)

// edgeDTO projects an AccessEdge for the graph view. The natural references and
// provenance come from the edge's own metadata (already redacted), so a caller
// sees what an edge connects, with what fidelity, without resolving every id
// against every repository. This is the edge half of the React Flow data
// contract.
type edgeDTO struct {
	ID            string `json:"id"`
	OriginKind    string `json:"origin_kind"`
	OriginID      string `json:"origin_id"`
	OriginRef     string `json:"origin_ref,omitempty"`
	ResourceID    string `json:"resource_id"`
	ResourceKind  string `json:"resource_kind,omitempty"`
	ResourceRef   string `json:"resource_ref,omitempty"`
	ToolRef       string `json:"tool_ref,omitempty"`
	Mode          string `json:"mode"`
	SignalSource  string `json:"signal_source"`
	SignalSources string `json:"signal_sources,omitempty"`
	Confidence    string `json:"confidence"`
	Bridged       bool   `json:"bridged"`
	CoverageTier  string `json:"coverage_tier,omitempty"`
	Reason        string `json:"attribution_reason,omitempty"`
	// AttributionTier is the honest per-edge firmness of the origin→agent/NHI
	// attribution (firm/approximate/unknown): firm only with an SVID/WIF/dedicated
	// credential, approximate for a shared account, unknown for a store with no
	// per-identity audit (G8). The UI must NOT render approximate/unknown
	// as if it were firm. AttributionTierReason is the short, non-sensitive "why".
	AttributionTier       string `json:"attribution_tier,omitempty"`
	AttributionTierReason string `json:"attribution_tier_reason,omitempty"`
	// Observed and Permitted are the two flags whose disagreement is the drift.
	Observed        bool   `json:"observed"`
	Permitted       bool   `json:"permitted"`
	OccurrenceCount int64  `json:"occurrence_count"`
	FirstSeen       string `json:"first_seen"`
	LastSeen        string `json:"last_seen"`
}

func metaStr(meta map[string]any, k string) string {
	if meta == nil {
		return ""
	}
	if s, ok := meta[k].(string); ok {
		return s
	}
	return ""
}

func metaBool(meta map[string]any, k string) bool {
	if meta == nil {
		return false
	}
	b, _ := meta[k].(bool)
	return b
}

func toEdgeDTO(e model.AccessEdge) edgeDTO {
	return edgeDTO{
		ID:                    e.ID.String(),
		OriginKind:            e.OriginKind,
		OriginID:              e.OriginID.String(),
		OriginRef:             metaStr(e.Metadata, "origin_ref"),
		ResourceID:            e.ResourceID.String(),
		ResourceKind:          metaStr(e.Metadata, "resource_kind"),
		ResourceRef:           metaStr(e.Metadata, "resource_ref"),
		ToolRef:               metaStr(e.Metadata, "tool_ref"),
		Mode:                  string(e.Mode),
		SignalSource:          string(e.SignalSource),
		SignalSources:         metaStr(e.Metadata, "signal_sources"),
		Confidence:            string(e.Confidence),
		Bridged:               metaBool(e.Metadata, "bridged"),
		CoverageTier:          metaStr(e.Metadata, "coverage_tier"),
		Reason:                metaStr(e.Metadata, "attribution_reason"),
		AttributionTier:       metaStr(e.Metadata, "attribution_tier"),
		AttributionTierReason: metaStr(e.Metadata, "attribution_tier_reason"),
		Observed:              e.Observed,
		Permitted:             e.Permitted,
		OccurrenceCount:       e.OccurrenceCount,
		FirstSeen:             e.FirstSeen.String(),
		LastSeen:              e.LastSeen.String(),
	}
}

// nodeDTO is a graph node (an origin or a resource) derived from the edges, so
// Can lay out the React Flow graph without resolving every id. It is the node
// half of the data contract.
type nodeDTO struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

// graphResponse is the React Flow data contract: a deduplicated node set plus the
// edges between them, with the keyset pagination envelope.
type graphResponse struct {
	Nodes   []nodeDTO `json:"nodes"`
	Edges   []edgeDTO `json:"edges"`
	Cursor  string    `json:"cursor,omitempty"`
	HasMore bool      `json:"has_more"`
}

// toGraphResponse builds the node+edge contract from a slice of edges, deriving
// each endpoint node once (by id) from the edge and its redacted metadata.
func toGraphResponse(edges []model.AccessEdge) graphResponse {
	out := graphResponse{Nodes: []nodeDTO{}, Edges: []edgeDTO{}}
	seen := map[string]bool{}
	addNode := func(id, kind, ref string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out.Nodes = append(out.Nodes, nodeDTO{ID: id, Kind: kind, Ref: ref})
	}
	for _, e := range edges {
		d := toEdgeDTO(e)
		addNode(d.OriginID, d.OriginKind, d.OriginRef)
		addNode(d.ResourceID, resourceNodeKind(d.ResourceKind), d.ResourceRef)
		out.Edges = append(out.Edges, d)
	}
	return out
}

// resourceNodeKind labels a resource node with its kind, defaulting to "resource".
func resourceNodeKind(rk string) string {
	if rk == "" {
		return "resource"
	}
	return rk
}

// Drift kinds in the wire contract (mapped from model.DriftKind).
const (
	driftUnexpected = "unexpected_access" // observed, not permitted (the headline)
	driftUnused     = "unused_grant"      // permitted, never observed
)

// driftDTO is one least-privilege discrepancy for the diff view. Pending marks an
// unexpected access whose permitted-ness cannot yet be decided because the
// agent↔identity link is unresolved: the UI must NOT headline it as a firm
// violation — honest uncertainty, not a fabricated finding (docs/SECURITY-HARDENING.md).
type driftDTO struct {
	Kind    string  `json:"kind"`
	Pending bool    `json:"reconciliation_pending,omitempty"`
	Edge    edgeDTO `json:"edge"`
}

// diffResponse is the permitted-vs-observed result: the unexpected accesses
// (highlighted first — the security finding) and the unused grants, each with a
// count so a dashboard can headline the totals. inventory_count totals the
// permitted-only grants on kinds with no observed-side collector (NOT drift;
// the rows stay queryable in the graph view by signal_source=policy); truncated
// marks a diff reconciled over a PARTIAL drift window (drainDrift's page bound
// fired) so a consumer never presents it as authoritative.
type diffResponse struct {
	UnexpectedAccesses []driftDTO `json:"unexpected_accesses"`
	UnusedGrants       []driftDTO `json:"unused_grants"`
	UnexpectedCount    int        `json:"unexpected_count"`
	UnusedCount        int        `json:"unused_count"`
	InventoryCount     int        `json:"inventory_count"`
	Truncated          bool       `json:"truncated,omitempty"`
}

func toDiffResponse(d PrivilegeDiff) diffResponse {
	out := diffResponse{UnexpectedAccesses: []driftDTO{}, UnusedGrants: []driftDTO{}}
	for _, v := range d.UnexpectedAccesses {
		out.UnexpectedAccesses = append(out.UnexpectedAccesses, driftDTO{
			Kind: driftUnexpected, Pending: metaBool(v.Edge.Metadata, "reconciliation_pending"), Edge: toEdgeDTO(v.Edge),
		})
	}
	for _, v := range d.UnusedGrants {
		out.UnusedGrants = append(out.UnusedGrants, driftDTO{Kind: driftUnused, Edge: toEdgeDTO(v.Edge)})
	}
	out.UnexpectedCount = len(out.UnexpectedAccesses)
	out.UnusedCount = len(out.UnusedGrants)
	out.InventoryCount = len(d.InventoryGrants)
	out.Truncated = d.Truncated
	return out
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

// errorBody is the small error envelope module endpoints return.
func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}
