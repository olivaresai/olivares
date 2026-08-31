// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// wiringEdgeDTO is one capability-connection edge: an origin connected to a
// capability, with provenance and liveness. It carries the connectors'
// already-redacted natural references, never payloads (docs/SECURITY-HARDENING.md).
type wiringEdgeDTO struct {
	OriginKind      string   `json:"origin_kind"`
	OriginRef       string   `json:"origin_ref"`
	CapabilityKind  string   `json:"capability_kind"`
	CapabilityRef   string   `json:"capability_ref"`
	ToolRef         string   `json:"tool_ref,omitempty"`
	SignalSources   []string `json:"signal_sources"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	OccurrenceCount int64    `json:"occurrence_count"`
}

func toWiringEdgeDTO(rec model.Record) wiringEdgeDTO {
	return wiringEdgeDTO{
		OriginKind: rec.String(colOriginKind), OriginRef: rec.String(colOriginRef),
		CapabilityKind: rec.String(colCapabilityKind), CapabilityRef: rec.String(colCapabilityRef),
		ToolRef:       rec.String(colToolRef),
		SignalSources: parseSet(rec.String(colSignalSources)),
		FirstSeen:     rec.String(colFirstSeen), LastSeen: rec.String(colLastSeen),
		OccurrenceCount: rec.Int(colOccurrence),
	}
}

// wiringPeerDTO is a node at one end of a wiring edge (an origin or a capability).
type wiringPeerDTO struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// wiringGraphDTO is the capability-connection graph "what is connected to whom":
// the deduplicated nodes and the edges between them. It is DISTINCT from module
// III's R/RW access graph: that records access to a resource and whether it
// is read or written; this records which capability an agent/session is wired to.
type wiringGraphDTO struct {
	Nodes     []wiringPeerDTO `json:"nodes"`
	Edges     []wiringEdgeDTO `json:"edges"`
	Truncated bool            `json:"truncated,omitempty"`
	Note      string          `json:"note"`
}

// handleWiring returns the capability-connection graph, optionally filtered by
// origin or capability. It bounds the result and flags truncation rather than
// silently capping (docs: no silent caps).
func (m *Module) handleWiring(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := model.Query{Limit: listCap}
	for _, f := range []struct{ param, col string }{
		{"origin_kind", colOriginKind}, {"origin_ref", colOriginRef},
		{"capability_kind", colCapabilityKind}, {"capability_ref", colCapabilityRef},
	} {
		if v := r.URL.Query().Get(f.param); v != "" {
			q.Filters = append(q.Filters, eq(f.col, v))
		}
	}
	out := wiringGraphDTO{
		Nodes: []wiringPeerDTO{}, Edges: []wiringEdgeDTO{},
		Note: "capability-connection graph (agent/session→capability and server→capability); distinct from the R/RW access graph of module III",
	}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wiringKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		out.Truncated = page.HasMore
		seen := map[wiringPeerDTO]struct{}{}
		addNode := func(kind, ref string) {
			n := wiringPeerDTO{Kind: kind, Ref: ref}
			if _, ok := seen[n]; !ok {
				seen[n] = struct{}{}
				out.Nodes = append(out.Nodes, n)
			}
		}
		for _, rec := range recs {
			e := toWiringEdgeDTO(rec)
			out.Edges = append(out.Edges, e)
			addNode(e.OriginKind, e.OriginRef)
			addNode(e.CapabilityKind, e.CapabilityRef)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// serverResources returns the resource refs an MCP server exposes (the server→
// resource wiring edges).
func (m *Module) serverResources(ctx context.Context, sc store.Scope, serverName string) ([]string, error) {
	repo, err := sc.Ext(wiringKind)
	if err != nil {
		return nil, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			eq(colOriginKind, originMCPServer), eq(colOriginRef, serverName),
			eq(colCapabilityKind, capResource),
		},
		Limit: listCap,
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.String(colCapabilityRef))
	}
	return out, nil
}

// serverConsumers returns the deduplicated origins (an internal design note (not shipped)) wired to an
// MCP server — "who uses this server".
func (m *Module) serverConsumers(ctx context.Context, sc store.Scope, serverName string) ([]wiringPeerDTO, error) {
	repo, err := sc.Ext(wiringKind)
	if err != nil {
		return nil, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colCapabilityKind, capMCPServer), eq(colCapabilityRef, serverName)},
		Limit:   listCap,
	})
	if err != nil {
		return nil, err
	}
	seen := map[wiringPeerDTO]struct{}{}
	out := make([]wiringPeerDTO, 0, len(recs))
	for _, rec := range recs {
		p := wiringPeerDTO{Kind: rec.String(colOriginKind), Ref: rec.String(colOriginRef)}
		if p.Kind == originMCPServer {
			continue // a server exposing itself is not a consumer
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}
