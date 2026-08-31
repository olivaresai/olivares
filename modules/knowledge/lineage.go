// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// lineageDTO is one append-only origin→answer record: which agent retrieved what
// from which KB, the chunks/sources it drew from, the residency region, the
// decision, and whether the query left the perimeter (egress). It is the evidence
// (compliance) and (forensics) consume; it carries refs + hashes + counts
// only, never any content. Reads are self-audited (recon-relevant, like).
type lineageDTO struct {
	ID              string     `json:"id"`
	KBRef           string     `json:"kb_ref"`
	AgentRef        string     `json:"agent_ref"`
	SessionRef      string     `json:"session_ref,omitempty"`
	QueryHash       string     `json:"query_hash"`
	ChunkRefs       []chunkRef `json:"chunk_refs"`
	SourceRefs      []string   `json:"source_refs"`
	ResidencyRegion string     `json:"residency_region"`
	Decision        string     `json:"decision"`
	Reason          string     `json:"reason,omitempty"`
	Egress          bool       `json:"egress"`
	EgressProvider  string     `json:"egress_provider,omitempty"`
	ResultCount     int64      `json:"result_count"`
	OccurredAt      string     `json:"occurred_at"`
}

func toLineageDTO(rec model.Record) lineageDTO {
	var refs []chunkRef
	if s := rec.String(colChunkRefs); strings.TrimSpace(s) != "" {
		_ = json.Unmarshal([]byte(s), &refs)
	}
	return lineageDTO{
		ID: rec.String(model.ColID), KBRef: rec.String(colKBRef), AgentRef: rec.String(colAgentRef),
		SessionRef: rec.String(colSessionRef), QueryHash: rec.String(colQueryHash), ChunkRefs: refs,
		SourceRefs: unmarshalStrings(rec, colSourceRefs), ResidencyRegion: rec.String(colResidency),
		Decision: rec.String(colDecision), Reason: rec.String(colReason), Egress: rec.Bool(colEgress),
		EgressProvider: rec.String(colEgressProvider), ResultCount: rec.Int(colResultCount),
		OccurredAt: rec.String(colOccurredAt),
	}
}

// handleListLineage lists lineage records, optionally filtered by kb_id, agent_ref
// or decision. Self-audited (reading the data lineage is a privileged, recon-
// relevant action, docs/SECURITY-HARDENING.md).
func (m *Module) handleListLineage(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("kb_id")); v != "" {
		q.Filters = append(q.Filters, eq(colKBRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("agent_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colAgentRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("decision")); v != "" {
		q.Filters = append(q.Filters, eq(colDecision, v))
	}
	out := listResponse[lineageDTO]{Items: []lineageDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(lineageKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toLineageDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return auditEvent(r.Context(), sc, mc, "knowledge.lineage.list", lineageKind, "", map[string]any{"count": len(out.Items)})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetLineage returns one lineage record (self-audited).
func (m *Module) handleGetLineage(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out lineageDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(lineageKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found, out = true, toLineageDTO(rec)
		return auditEvent(r.Context(), sc, mc, "knowledge.lineage.get", lineageKind, id, nil)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}
