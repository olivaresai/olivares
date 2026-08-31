// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import "context"

// Access-graph routes: the R/RW map view (a core route) and the
// permitted-vs-observed least-privilege diff. The diff is module III's RECONCILED
// drift (/v1/m/accessmap/drift) — the single, cross-origin-reconciled source of
// truth. The raw core /v1/access-edges/drift was REMOVED in (C2)
// because it served UNRECONCILED drift that double-counts cross-origin access (an
// agent's observed access against the identity it assumes appeared as BOTH a false
// unexpected access AND a false unused grant) — shipping false positives into IaC
// plans. Both are read-tier. PERMITTED is a DERIVED view (declared grants + observed
// signals), never a writable object — so it is exposed only as a data source.
const (
	accessEdgesPath = "/v1/access-edges"
	driftPath       = "/v1/m/accessmap/drift"

	// Reconciled drift kinds in the module III wire contract.
	DriftUnexpectedAccess = "unexpected_access" // observed, not permitted (the headline)
	DriftUnusedGrant      = "unused_grant"      // permitted, never observed
)

// AccessEdge is the wire representation of one access edge (the R/RW map),
// matching the core AccessEdgeDTO. Permitted/Observed are the two faces of the
// least-privilege diff: an edge declared by policy/wiring is permitted; an edge
// seen in telemetry is observed.
type AccessEdge struct {
	ID              string `json:"id"`
	OriginKind      string `json:"origin_kind"`
	OriginID        string `json:"origin_id"`
	ResourceID      string `json:"resource_id"`
	Mode            string `json:"mode"`
	SignalSource    string `json:"signal_source"`
	Confidence      string `json:"confidence"`
	Permitted       bool   `json:"permitted"`
	Observed        bool   `json:"observed"`
	OccurrenceCount int64  `json:"occurrence_count"`
	FirstSeen       string `json:"first_seen"`
	LastSeen        string `json:"last_seen"`
}

// edgeList is the list envelope returned by GET /access-edges.
type edgeList struct {
	Items   []AccessEdge `json:"items"`
	Cursor  string       `json:"cursor"`
	HasMore bool         `json:"has_more"`
}

// DriftEdge is one entry of the RECONCILED permitted-vs-observed diff: an access
// edge plus its drift Kind (DriftUnexpectedAccess = observed but not granted;
// DriftUnusedGrant = granted but never observed). ReconciliationPending marks an
// unexpected access whose permitted-ness cannot yet be decided because the
// agent↔identity link is unresolved — honest uncertainty surfaced to the
// operator, NOT a firm violation. The field name matches the module wire DTO so an
// entry decodes directly from either array of the diff envelope.
type DriftEdge struct {
	Edge                  AccessEdge `json:"edge"`
	Kind                  string     `json:"kind"`
	ReconciliationPending bool       `json:"reconciliation_pending"`
}

// diffResponse is the reconciled diff envelope returned by GET /v1/m/accessmap/drift:
// two arrays (unexpected accesses + unused grants) plus their counts. It is NOT
// paginated and carries no cursor (unlike the removed raw route's {items,cursor}).
type diffResponse struct {
	UnexpectedAccesses []DriftEdge `json:"unexpected_accesses"`
	UnusedGrants       []DriftEdge `json:"unused_grants"`
	UnexpectedCount    int         `json:"unexpected_count"`
	UnusedCount        int         `json:"unused_count"`
}

// ListAccessEdges returns the R/RW access-edge map, following the cursor.
func (c *Client) ListAccessEdges(ctx context.Context, tenantOverride string) ([]AccessEdge, error) {
	var all []AccessEdge
	cursor := ""
	for {
		path := accessEdgesPath
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		var page edgeList
		if err := c.getInto(ctx, path, tenantOverride, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasMore || page.Cursor == "" {
			return all, nil
		}
		cursor = page.Cursor
	}
}

// ListDrift returns the RECONCILED permitted-vs-observed least-privilege diff from
// module III, flattening the unexpected-accesses and unused-grants arrays into one
// list (each entry keeps its Kind). The reconciled envelope is not paginated, so this
// is a single GET — no cursor loop.
func (c *Client) ListDrift(ctx context.Context, tenantOverride string) ([]DriftEdge, error) {
	var resp diffResponse
	if err := c.getInto(ctx, driftPath, tenantOverride, &resp); err != nil {
		return nil, err
	}
	out := make([]DriftEdge, 0, len(resp.UnexpectedAccesses)+len(resp.UnusedGrants))
	out = append(out, resp.UnexpectedAccesses...)
	out = append(out, resp.UnusedGrants...)
	return out, nil
}
