// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file ENRICHES a forensic reconstruction with the signals the module consumes
// but does not own: WHO the subject acts as (identity), what it accessed that it
// should not have (least-privilege drift), and what data it derived from (data lineage). Each consumer is read-only and TOLERANT of an absent neighbor
// (e.g. knowledge not installed → no lineage) so forensics degrades gracefully
// rather than failing.

// ---- identity attribution --------------------------------------------------

// attributionDTO is the resolved identity a forensic subject acts as. A SHARED
// identity (bound to >1 agent) is flagged because per-agent attribution then
// collapses — honest uncertainty rather than a fabricated certainty (ARCHITECTURE.md).
type attributionDTO struct {
	AgentID        string `json:"agent_id,omitempty"`
	IdentityID     string `json:"identity_id,omitempty"`
	IdentityName   string `json:"identity_name,omitempty"`
	IdentityKind   string `json:"identity_kind,omitempty"`
	ExternalID     string `json:"external_id,omitempty"`
	Provider       string `json:"provider,omitempty"`
	SharedIdentity bool   `json:"shared_identity"`
	AgentCount     int    `json:"agent_count,omitempty"`
	Note           string `json:"note,omitempty"`
}

// resolveAttribution resolves who a forensic subject acts as. For an agent it
// follows Agent.IdentityID → Identity; for a session it follows Session.AgentID →
// Agent → Identity; for an identity it resolves directly. A non-id or absent subject
// yields nil (no fabricated attribution).
func resolveAttribution(ctx context.Context, sc store.Scope, subjectKind, subjectRef string) *attributionDTO {
	id, err := model.ParseID(subjectRef)
	if err != nil {
		return nil
	}
	switch subjectKind {
	case "agent":
		return agentAttribution(ctx, sc, id)
	case "identity":
		return identityAttribution(ctx, sc, id)
	case "session":
		sess, err := sc.Sessions().Get(ctx, id)
		if err != nil || sess.AgentID.IsZero() {
			return nil
		}
		return agentAttribution(ctx, sc, sess.AgentID)
	default:
		return nil
	}
}

func agentAttribution(ctx context.Context, sc store.Scope, agentID model.ID) *attributionDTO {
	a, err := sc.Agents().Get(ctx, agentID)
	if err != nil {
		return nil
	}
	out := &attributionDTO{AgentID: a.ID.String()}
	if a.IdentityID.IsZero() {
		out.Note = "agent has no resolved identity — attribution unavailable (not yet bound)"
		return out
	}
	fillIdentity(ctx, sc, a.IdentityID, out)
	out.AgentCount = countAgentsForIdentity(ctx, sc, a.IdentityID)
	if out.AgentCount > 1 {
		out.SharedIdentity = true
		out.Note = "identity bound to multiple agents — per-agent attribution may be ambiguous"
	}
	return out
}

func identityAttribution(ctx context.Context, sc store.Scope, identityID model.ID) *attributionDTO {
	out := &attributionDTO{}
	fillIdentity(ctx, sc, identityID, out)
	if out.IdentityID == "" {
		return nil
	}
	out.AgentCount = countAgentsForIdentity(ctx, sc, identityID)
	if out.AgentCount > 1 {
		out.SharedIdentity = true
	}
	return out
}

func fillIdentity(ctx context.Context, sc store.Scope, id model.ID, out *attributionDTO) {
	i, err := sc.Identities().Get(ctx, id)
	if err != nil {
		return
	}
	out.IdentityID, out.IdentityName, out.IdentityKind = i.ID.String(), i.Name, i.Kind
	out.ExternalID, out.Provider = i.ExternalID, i.Provider
}

// countAgentsForIdentity counts agents bound to an identity — the shared-identity
// (attribution-collapse) signal (ARCHITECTURE.md).
func countAgentsForIdentity(ctx context.Context, sc store.Scope, identityID model.ID) int {
	agents, _, err := sc.Agents().List(ctx, model.Query{
		Filters: []model.Filter{eq("identity_id", identityID.String())}, Limit: listCap,
	})
	if err != nil {
		return 0
	}
	return len(agents)
}

// ---- least-privilege drift -------------------------------------------------

// driftRefDTO is one unexpected access (observed, not permitted) involving the
// forensic subject — consumed from drift, not recomputed (ARCHITECTURE.md).
type driftRefDTO struct {
	OriginKind      string `json:"origin_kind"`
	OriginID        string `json:"origin_id"`
	ResourceID      string `json:"resource_id"`
	Mode            string `json:"mode"`
	SignalSource    string `json:"signal_source"`
	OccurrenceCount int64  `json:"occurrence_count"`
	LastSeen        string `json:"last_seen,omitempty"`
}

// subjectDrift returns the unexpected accesses whose origin is the subject. The
// store-level Drift is the raw signal; access-map's /drift is the reconciled
// authority, so these are context, not headline verdicts (docs/SECURITY-HARDENING.md).
func subjectDrift(ctx context.Context, sc store.Scope, subjectRef string) []driftRefDTO {
	q := model.Query{Limit: listCap}
	if id, err := model.ParseID(subjectRef); err == nil {
		q.Filters = append(q.Filters, eq("origin_id", id.String()))
	}
	drifts, err := sc.AccessEdges().Drift(ctx, q)
	if err != nil {
		return nil
	}
	var out []driftRefDTO
	for _, d := range drifts {
		if d.Kind != model.DriftViolation {
			continue
		}
		out = append(out, driftRefDTO{
			OriginKind: d.Edge.OriginKind, OriginID: d.Edge.OriginID.String(), ResourceID: d.Edge.ResourceID.String(),
			Mode: string(d.Edge.Mode), SignalSource: string(d.Edge.SignalSource),
			OccurrenceCount: d.Edge.OccurrenceCount, LastSeen: d.Edge.LastSeen.String(),
		})
	}
	return out
}

// ---- data lineage ----------------------------------------------------------

// lineageKnowledgeKind is the knowledge module's append-only lineage entity. The
// security module reads it cross-module via Ext, tolerating its absence (knowledge
// not installed) so the timeline still renders.
const lineageKnowledgeKind model.Kind = "knowledge.lineage"

// lineageRefDTO is one origin→answer lineage record for the subject: what the
// agent retrieved, the decision, and whether the query left the perimeter.
type lineageRefDTO struct {
	ID         string `json:"id"`
	AgentRef   string `json:"agent_ref"`
	KBRef      string `json:"kb_ref,omitempty"`
	Decision   string `json:"decision"`
	Egress     bool   `json:"egress"`
	QueryHash  string `json:"query_hash,omitempty"`
	OccurredAt string `json:"occurred_at,omitempty"`
}

// subjectLineage returns the subject agent's recent data lineage. It is
// tolerant: if knowledge is not installed (Ext → ErrUnknownEntity) it returns nil,
// and the forensic timeline simply omits the lineage block.
func subjectLineage(ctx context.Context, sc store.Scope, subjectRef string) []lineageRefDTO {
	if subjectRef == "" {
		return nil
	}
	repo, err := sc.Ext(lineageKnowledgeKind)
	if err != nil {
		return nil // knowledge module not installed — lineage unavailable, not an error
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq("agent_ref", subjectRef)}, Limit: 100})
	if err != nil {
		return nil
	}
	var out []lineageRefDTO
	for _, rec := range recs {
		out = append(out, lineageRefDTO{
			ID: rec.String("id"), AgentRef: rec.String("agent_ref"), KBRef: rec.String("kb_ref"),
			Decision: rec.String("decision"), Egress: rec.Bool("egress"), QueryHash: rec.String("query_hash"),
			OccurredAt: rec.String("occurred_at"),
		})
	}
	return out
}
