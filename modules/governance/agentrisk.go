// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// per-agent risk/autonomy tier: the governance classification of an
// agent's operational risk profile. The effective tier (operator-declared when
// set, else heuristic-suggested) is mirrored onto core/model.Agent.RiskTier
// for hot reads; this module owns the full lifecycle — classify, review,
// override — and writes both the profile row and the Agent column in one
// transaction.
//
// The tier vocabulary reuses the OWASP 4-tier (low/medium/high/critical)
// defined by ActionRiskTier (risktier.go). It is a DIFFERENT classification
// axis: ActionRiskTier classifies ACTIONS for the approval layer;
// AgentRiskTier classifies AGENTS for the governance layer. They compose:
// a critical-tier agent calling a critical action gets both the agent-tier
// floors AND the action-tier dual-control floor.
//
// The heuristic is a SUGGESTION until a human reviews it. A human reviewer
// can accept the suggestion or override it (the operator_tier). The
// effective_tier — the one enforcement reads — is always:
//   effective_tier = operator_tier if set, else suggested_tier

// agentRiskProfileState is the lifecycle state of a risk profile.
const (
	arpStateUnclassified = "unclassified"
	arpStateSuggested    = "suggested"
	arpStateReviewed     = "reviewed"
)

// agentRiskProfileDTO is the profile as returned to callers.
type agentRiskProfileDTO struct {
	ID            string         `json:"id"`
	AgentID       string         `json:"agent_id"`
	OperatorTier  string         `json:"operator_tier,omitempty"`
	SuggestedTier string         `json:"suggested_tier,omitempty"`
	EffectiveTier string         `json:"effective_tier"`
	State         string         `json:"state"`
	Signals       map[string]any `json:"signals,omitempty"`
	ReviewedBy    string         `json:"reviewed_by,omitempty"`
	ReviewedAt    string         `json:"reviewed_at,omitempty"`
}

func toAgentRiskProfileDTO(rec model.Record) agentRiskProfileDTO {
	var signals map[string]any
	if v, ok := rec[colARPSignals]; ok && v != nil {
		if m, ok := v.(map[string]any); ok {
			signals = m
		}
		if s, ok := v.(string); ok && s != "" {
			_ = json.Unmarshal([]byte(s), &signals)
		}
	}
	return agentRiskProfileDTO{
		ID:            rec.String(model.ColID),
		AgentID:       rec.String(colARPAgentID),
		OperatorTier:  rec.String(colARPOperatorTier),
		SuggestedTier: rec.String(colARPSuggestedTier),
		EffectiveTier: rec.String(colARPEffectiveTier),
		State:         rec.String(colARPState),
		Signals:       signals,
		ReviewedBy:    rec.String(colARPReviewedBy),
		ReviewedAt:    rec.String(colARPReviewedAt),
	}
}

// effectiveTier computes the tier enforcement reads: operator-declared if set,
// else the heuristic suggestion.
func effectiveTier(operatorTier, suggestedTier string) string {
	if operatorTier != "" {
		return operatorTier
	}
	return suggestedTier
}

// agentRiskSignals are the observed signals that drove a suggested tier.
type agentRiskSignals struct {
	RWEdges      int64 `json:"rw_edges"`
	TotalEdges   int64 `json:"total_edges"`
	Resources    int64 `json:"distinct_resources"`
	HighFindings int64 `json:"high_severity_findings"`
	CritFindings int64 `json:"critical_severity_findings"`
	Scheduled    bool  `json:"scheduled"`
	Autonomous   bool  `json:"autonomous"`
	// Truncated is set when the edge/finding scan could not be completed within the
	// bounded page budget (D-03). It is a FAIL-SAFE flag: a truncated scan may
	// have MISSED a critical finding, so the classifier must never suggest a tier
	// below critical when it is set (suggestTier enforces this).
	Truncated bool `json:"truncated,omitempty"`
}

// riskScanPages bounds the per-agent signal scan (riskScanPages × listCap rows)
// so a pathological estate cannot loop unbounded; hitting the cap flags the
// signals TRUNCATED, and a truncated classification never LOWERS the tier
// (fail-safe) — the finops scanSamples posture applied to the risk classifier.
const riskScanPages = 1000

// scanFolding pages a filtered list by the default id keyset, folding each row via
// fn, up to riskScanPages pages. It returns truncated=true if it hit the cap
// WITHOUT exhausting the result set, so the caller can refuse to lower a security
// tier on a partial scan (D-03). It folds in place rather than materializing
// the whole set, so a huge estate does not blow up memory.
func scanFolding[T any](ctx context.Context, repo listLister[T], filters []model.Filter, fn func(T)) (bool, error) {
	q := model.Query{Filters: filters, Limit: listCap}
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return false, err
		}
		for _, r := range recs {
			fn(r)
		}
		if !page.HasMore || page.Cursor == "" {
			return false, nil
		}
		if pages+1 >= riskScanPages {
			return true, nil
		}
		q.Cursor = page.Cursor
	}
}

// suggestTier computes a suggested tier from observed signals. It follows the
// compliance risk.go pattern: observable signals only, never a fabricated
// inference. The unacceptable tier is never suggested (a legal determination
// that only a human may make — but we use the OWASP vocabulary here, so there
// is no unacceptable; critical is the highest).
func suggestTier(s agentRiskSignals) string {
	// Fail-safe (D-03): a truncated scan may have missed a critical finding, so
	// it must never yield a tier BELOW critical. Truncation is pathological (an agent
	// with > riskScanPages×listCap signals), and over-classifying is the safe error.
	if s.Truncated || s.CritFindings > 0 {
		return string(RiskTierCritical)
	}
	if s.HighFindings > 0 || (s.RWEdges > 10 && s.Autonomous) {
		return string(RiskTierHigh)
	}
	if s.RWEdges > 5 || s.Scheduled {
		return string(RiskTierMedium)
	}
	if s.TotalEdges > 0 {
		return string(RiskTierLow)
	}
	return string(RiskTierLow)
}

// gatherAgentRiskSignals collects the observable signals for one agent from the
// live data. It reads access edges and findings; the autonomy signal comes from
// the compliance AutonomySource seam (wired at construction).
func (m *Module) gatherAgentRiskSignals(ctx context.Context, sc store.Scope, agentID model.ID) (agentRiskSignals, error) {
	var s agentRiskSignals

	// Drain EVERY page of access edges and findings by the id keyset (D-03):
	// a single Limit:1000 page silently dropped a critical finding that sorted onto a
	// later page, so the classifier suggested a lower tier and the tier-floor stopped
	// applying the critical immediate-stop. A truncation past the bounded page budget
	// sets s.Truncated, which suggestTier treats fail-safe (never lowers the tier).
	resources := map[string]struct{}{}
	edgeTrunc, err := scanFolding[model.AccessEdge](ctx, sc.AccessEdges(), []model.Filter{
		{Column: "origin_kind", Op: model.OpEq, Value: "agent"},
		{Column: "origin_id", Op: model.OpEq, Value: agentID.String()},
	}, func(e model.AccessEdge) {
		s.TotalEdges++
		if e.Mode == sdkmodel.ModeReadWrite || e.Mode == sdkmodel.ModeWrite {
			s.RWEdges++
		}
		resources[e.ResourceID.String()] = struct{}{}
	})
	if err != nil {
		return s, err
	}
	s.Resources = int64(len(resources))

	findTrunc, ferr := scanFolding[model.Finding](ctx, sc.Findings(), []model.Filter{
		{Column: "subject_kind", Op: model.OpEq, Value: "agent"},
		{Column: "subject_id", Op: model.OpEq, Value: agentID.String()},
	}, func(f model.Finding) {
		switch f.Severity {
		case model.SeverityHigh:
			s.HighFindings++
		case model.SeverityCritical:
			s.CritFindings++
		}
	})
	if ferr != nil {
		return s, ferr
	}
	s.Truncated = edgeTrunc || findTrunc

	if m.autonomy != nil {
		as, aerr := m.autonomy.Autonomy(ctx, sc.Tenant(), agentID.String())
		if aerr == nil {
			s.Scheduled = as.Scheduled
			s.Autonomous = as.Autonomous
		}
	}
	return s, nil
}

// classifyRequest is the input for classify/reclassify.
type classifyAgentRiskRequest struct {
	AgentID string `json:"agent_id"`
}

// handleClassifyAgentRisk computes (or recomputes) the suggested risk tier for
// an agent from observed signals. Idempotent: it updates the suggestion but
// never touches the operator's declaration.
func (m *Module) handleClassifyAgentRisk(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req classifyAgentRiskRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	agentRef := strings.TrimSpace(req.AgentID)
	if agentRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("agent_id is required"))
		return
	}
	agentID, perr := model.ParseID(agentRef)
	if perr != nil || agentID.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid agent_id"))
		return
	}

	var out agentRiskProfileDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		agent, gerr := sc.Agents().Get(r.Context(), agentID)
		if gerr != nil {
			return gerr
		}

		signals, serr := m.gatherAgentRiskSignals(r.Context(), sc, agentID)
		if serr != nil {
			return serr
		}
		suggested := suggestTier(signals)

		repo, err := sc.Ext(agentRiskProfileKind)
		if err != nil {
			return err
		}

		existing, found, err := findOne(r.Context(), repo, eq(colARPAgentID, agentID.String()))
		if err != nil {
			return err
		}

		signalsJSON := map[string]any{
			"rw_edges": signals.RWEdges, "total_edges": signals.TotalEdges,
			"distinct_resources":         signals.Resources,
			"high_severity_findings":     signals.HighFindings,
			"critical_severity_findings": signals.CritFindings,
			"scheduled":                  signals.Scheduled, "autonomous": signals.Autonomous,
		}
		if signals.Truncated {
			// Record that the classification ran on a truncated scan (D-03): the
			// tier was pinned critical fail-safe, and a reviewer sees WHY.
			signalsJSON["truncated"] = true
		}

		var rec model.Record
		if found {
			existing[colARPSuggestedTier] = suggested
			existing[colARPSignals] = signalsJSON
			eff := effectiveTier(existing.String(colARPOperatorTier), suggested)
			existing[colARPEffectiveTier] = eff
			if existing.String(colARPState) == arpStateUnclassified {
				existing[colARPState] = arpStateSuggested
			}
			rec, err = repo.Update(r.Context(), existing)
			if err != nil {
				return err
			}
		} else {
			eff := effectiveTier("", suggested)
			rec, err = repo.Create(r.Context(), model.Record{
				colARPAgentID:       agentID.String(),
				colARPSuggestedTier: suggested,
				colARPEffectiveTier: eff,
				colARPState:         arpStateSuggested,
				colARPSignals:       signalsJSON,
			})
			if err != nil {
				return err
			}
		}

		eff := rec.String(colARPEffectiveTier)
		if agent.RiskTier != eff {
			agent.RiskTier = eff
			if _, uerr := sc.Agents().Update(r.Context(), agent); uerr != nil {
				return uerr
			}
		}

		out = toAgentRiskProfileDTO(rec)
		return auditEvent(r.Context(), sc, mc, "governance.agent_risk.classify",
			agentRiskProfileKind, model.ID(out.ID), map[string]any{
				"agent_id": agentRef, "suggested_tier": suggested,
				"effective_tier": eff,
			})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// setTierRequest sets the operator's authoritative tier.
type setTierRequest struct {
	Tier string `json:"tier"`
}

// handleSetAgentRiskTier sets the operator's authoritative tier for an agent.
// This is the "override" — the operator's word takes precedence over the
// heuristic. Pass tier="" to clear the operator override and fall back to the
// heuristic suggestion.
func (m *Module) handleSetAgentRiskTier(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req setTierRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tier := strings.ToLower(strings.TrimSpace(req.Tier))
	if tier != "" && !validRiskTier(tier) {
		writeJSON(w, http.StatusBadRequest, errorBody("tier must be one of low, medium, high, critical (or empty to clear)"))
		return
	}

	now := m.clock.Now()
	var out agentRiskProfileDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(agentRiskProfileKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}

		if tier == "" {
			rec[colARPOperatorTier] = nil
		} else {
			rec[colARPOperatorTier] = tier
		}
		eff := effectiveTier(tier, rec.String(colARPSuggestedTier))
		rec[colARPEffectiveTier] = eff
		rec[colARPState] = arpStateReviewed
		rec[colARPReviewedBy] = mc.Principal.Actor()
		rec[colARPReviewedAt] = now.String()

		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}

		agentID, _ := model.ParseID(rec.String(colARPAgentID))
		if !agentID.IsZero() {
			agent, gerr := sc.Agents().Get(r.Context(), agentID)
			if gerr != nil {
				return gerr
			}
			if agent.RiskTier != eff {
				agent.RiskTier = eff
				if _, uerr := sc.Agents().Update(r.Context(), agent); uerr != nil {
					return uerr
				}
			}
		}

		out = toAgentRiskProfileDTO(rec)
		return auditEvent(r.Context(), sc, mc, "governance.agent_risk.set_tier",
			agentRiskProfileKind, id, map[string]any{
				"operator_tier": tier, "effective_tier": eff,
			})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReviewAgentRisk marks a classification as human-reviewed. The reviewer
// accepts the current effective tier as correct.
func (m *Module) handleReviewAgentRisk(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	var out agentRiskProfileDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(agentRiskProfileKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		rec[colARPState] = arpStateReviewed
		rec[colARPReviewedBy] = mc.Principal.Actor()
		rec[colARPReviewedAt] = now.String()
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toAgentRiskProfileDTO(rec)
		return auditEvent(r.Context(), sc, mc, "governance.agent_risk.review",
			agentRiskProfileKind, id, map[string]any{
				"effective_tier": out.EffectiveTier,
			})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetAgentRiskProfile returns one profile.
func (m *Module) handleGetAgentRiskProfile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out agentRiskProfileDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(agentRiskProfileKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toAgentRiskProfileDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListAgentRiskProfiles lists profiles, optionally filtered by tier.
func (m *Module) handleListAgentRiskProfiles(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("effective_tier"); v != "" {
		q.Filters = append(q.Filters, eq(colARPEffectiveTier, v))
	}
	if v := r.URL.Query().Get("state"); v != "" {
		q.Filters = append(q.Filters, eq(colARPState, v))
	}
	out := listResponse[agentRiskProfileDTO]{Items: []agentRiskProfileDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(agentRiskProfileKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toAgentRiskProfileDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AgentEffectiveTier reads an agent's effective risk tier from the hot-read
// column on the Agent entity. Exported for the guardian tier-floor check and
// the circuit-breaker gate. Returns "" for an unclassified agent (no floor
// applies).
func (m *Module) AgentEffectiveTier(ctx context.Context, tenant model.TenantID, agentRef string) (string, error) {
	if m.data == nil {
		return "", errNoData
	}
	var tier string
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		agent, err := resolveAgent(ctx, sc, agentRef)
		if err != nil {
			return err
		}
		tier = agent.RiskTier
		return nil
	})
	return tier, err
}

// tierRank orders the agent risk tier vocabulary for comparison (the same scale
// as ActionRiskTier). An empty or unknown tier ranks 0 (below everything).
func tierRank(tier string) int {
	switch ActionRiskTier(strings.ToLower(strings.TrimSpace(tier))) {
	case RiskTierLow:
		return 1
	case RiskTierMedium:
		return 2
	case RiskTierHigh:
		return 3
	case RiskTierCritical:
		return 4
	default:
		return 0
	}
}

// resolveAgent finds an agent by UUID or external_id.
func resolveAgent(ctx context.Context, sc store.Scope, ref string) (model.Agent, error) {
	if id, perr := model.ParseID(ref); perr == nil && !id.IsZero() {
		return sc.Agents().Get(ctx, id)
	}
	agents, _, err := sc.Agents().List(ctx, model.Query{
		Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: ref}},
		Limit:   1,
	})
	if err != nil {
		return model.Agent{}, err
	}
	if len(agents) == 0 {
		return model.Agent{}, store.ErrNotFound
	}
	return agents[0], nil
}
