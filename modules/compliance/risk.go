// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file classifies agent risk (EU AI Act tiers cross-mapped to NIST AI RMF). The
// suggested tier is computed from OBSERVED signals only — the agent's R/RW access
// edges (module III) and its security findings (module IX/XVIII), plus an optional
// autonomy signal (module IV) via the AutonomySource seam. The classification is
// GOVERNED: it is a SUGGESTION until a human reviews it, and the unacceptable tier
// (EU AI Act Art. 5, a legal determination) is NEVER asserted by the heuristic — only
// a reviewer may set it. Every classification and review is audited (docs/SECURITY-HARDENING.md).

// rwHighThreshold is the number of distinct write accesses above which the heuristic
// suggests the high tier even without a high-severity finding.
const rwHighThreshold = 5

// riskScanPages bounds the per-agent finding scan (riskScanPages × listCap rows) so a
// pathological estate cannot loop unbounded; hitting the cap flags the signals
// TRUNCATED, and a truncated classification never LOWERS the tier below TierHigh
// (fail-safe) — an unseen high/critical finding must not be classified away.
const riskScanPages = 1000

// RiskDTO is a classification as returned to a caller.
type RiskDTO struct {
	ID            string         `json:"id"`
	SubjectKind   string         `json:"subject_kind"`
	SubjectRef    string         `json:"subject_ref"`
	AgentID       string         `json:"agent_id,omitempty"`
	Tier          string         `json:"tier"`
	SuggestedTier string         `json:"suggested_tier"`
	State         string         `json:"state"`
	Rationale     string         `json:"rationale,omitempty"`
	NISTFunctions []string       `json:"nist_functions,omitempty"`
	Signals       map[string]any `json:"signals,omitempty"`
	ReviewedBy    string         `json:"reviewed_by,omitempty"`
	ClassifiedAt  string         `json:"classified_at"`
	Disclaimer    string         `json:"disclaimer"`
}

// riskSignals are the observed signals that drove a suggested tier.
type riskSignals struct {
	RWEdges    int64 `json:"rw_edges"`
	TotalEdges int64 `json:"total_edges"`
	Resources  int64 `json:"distinct_resources"`
	High       int64 `json:"high_severity_findings"`
	Scheduled  bool  `json:"scheduled"`
	Autonomous bool  `json:"autonomous"`
	// Truncated is set when the finding scan could not be completed within the bounded
	// page budget (sweep). A truncated scan may have MISSED a high/critical
	// finding, so suggestTier must never suggest a tier below TierHigh when it is set
	// (fail-safe: never silently under-classify an AI system's risk).
	Truncated bool `json:"truncated,omitempty"`
}

type classifyRequest struct {
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	AgentID     string `json:"agent_id"`
}

// handleClassifyRisk computes (or recomputes) a suggested risk tier for a subject from
// observed signals and persists it as a governed, audited classification.
func (m *Module) handleClassifyRisk(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req classifyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// subject_ref is the classification's identity key (unique tenant+kind+ref):
	// a clamped ref would persist as a DIFFERENT identity (see tooLong), so an
	// over-length ref is rejected, never truncated.
	subjectRef := strings.TrimSpace(req.SubjectRef)
	if subjectRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_ref is required"))
		return
	}
	if tooLong(subjectRef, maxRefLen) {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_ref exceeds "+itoa(int64(maxRefLen))+" characters; identity references are rejected, never truncated"))
		return
	}
	subjectKind := strings.TrimSpace(req.SubjectKind)
	if subjectKind == "" {
		subjectKind = "agent"
	}
	// Resolve the agent id we correlate signals against: an explicit agent_id, else a
	// UUID subject_ref. A non-UUID ref leaves signals empty (an honest minimal default).
	agentID := parseAgentID(req.AgentID, subjectRef)

	var dto RiskDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		sig, err := m.observeRiskSignals(r.Context(), sc, mc.Tenant, agentID, subjectRef)
		if err != nil {
			return err
		}
		tier, rationale := suggestTier(sig)
		nist := nistFunctionsForTier(tier)
		now := m.clock.Now()

		repo, err := sc.Ext(riskKind)
		if err != nil {
			return err
		}
		existing, err := listAll(r.Context(), repo, eq(colSubjectKind, subjectKind), eq(colSubjectRef, subjectRef))
		if err != nil {
			return err
		}

		var saved model.Record
		if len(existing) > 0 {
			rec := existing[0]
			rec[colSuggested] = string(tier)
			rec[colSignals] = encodeJSON(sig)
			rec[colRationale] = rationale
			rec[colClassifiedAt] = now.String()
			if rec.String(colAgentID) == "" && !agentID.IsZero() {
				rec[colAgentID] = agentID.String()
			}
			// Preserve a human decision: only the effective tier of a not-yet-reviewed
			// (suggested) classification tracks the new suggestion.
			if RiskState(rec.String(colRiskState)) == RiskSuggested {
				rec[colTier] = string(tier)
				rec[colNistFns] = encodeJSON(nist)
			}
			saved, err = repo.Update(r.Context(), rec)
		} else {
			saved, err = repo.Create(r.Context(), model.Record{
				colSubjectKind:  subjectKind,
				colSubjectRef:   subjectRef,
				colAgentID:      nullableID(agentID),
				colTier:         string(tier),
				colSuggested:    string(tier),
				colRiskState:    string(RiskSuggested),
				colRationale:    rationale,
				colNistFns:      encodeJSON(nist),
				colSignals:      encodeJSON(sig),
				colClassifiedAt: now.String(),
			})
		}
		if err != nil {
			return err
		}
		dto = recordToRiskDTO(saved)
		return auditEvent(r.Context(), sc, mc, "compliance.risk.classify", riskKind, model.ID(saved.String(model.ColID)), map[string]any{
			"subject": subjectKind + ":" + subjectRef, "suggested_tier": string(tier), "state": saved.String(colRiskState),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

type reviewRequest struct {
	Tier string `json:"tier"`
	Note string `json:"note"`
}

// handleReviewRisk is the governance decision surface: a reviewer approves the
// suggested tier or overrides it (the ONLY path that may set the unacceptable tier).
func (m *Module) handleReviewRisk(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req reviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tier := RiskTier(strings.TrimSpace(strings.ToLower(req.Tier)))
	if !validTier(tier) {
		writeJSON(w, http.StatusBadRequest, errorBody("tier must be one of: unacceptable, high, limited, minimal"))
		return
	}

	var dto RiskDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(riskKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		from := rec.String(colTier)
		state := RiskOverridden
		if string(tier) == rec.String(colSuggested) {
			state = RiskApproved
		}
		rec[colTier] = string(tier)
		rec[colRiskState] = string(state)
		rec[colNistFns] = encodeJSON(nistFunctionsForTier(tier))
		rec[colReviewedBy] = mc.Principal.Actor()
		if note := clamp(strings.TrimSpace(req.Note), maxNoteLen); note != "" {
			rec[colRationale] = clamp(rec.String(colRationale)+" | review: "+note, maxNoteLen)
		}
		saved, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = recordToRiskDTO(saved)
		return auditEvent(r.Context(), sc, mc, "compliance.risk.review", riskKind, id, map[string]any{
			"from": from, "to": string(tier), "state": string(state),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleListRisk(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if t := strings.TrimSpace(r.URL.Query().Get("tier")); t != "" {
		filters = append(filters, eq(colTier, t))
	}
	var items []RiskDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(riskKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, filters...)
		for _, rec := range recs {
			items = append(items, recordToRiskDTO(rec))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[RiskDTO]{Items: items})
}

// observeRiskSignals reads the agent's observed access edges and findings (and an
// optional autonomy signal) — the only inputs to the heuristic. It captures nothing;
// it reads what modules III/IX/IV already recorded.
func (m *Module) observeRiskSignals(ctx context.Context, sc store.Scope, tenant model.TenantID, agentID model.ID, subjectRef string) (riskSignals, error) {
	var sig riskSignals
	if !agentID.IsZero() {
		edges, err := sc.AccessEdges().Neighbors(ctx, model.NodeRef{Kind: "agent", ID: agentID}, model.Outgoing)
		if err != nil {
			return sig, err
		}
		resources := map[model.ID]struct{}{}
		for _, e := range edges {
			sig.TotalEdges++
			resources[e.ResourceID] = struct{}{}
			if e.Mode == sdkmodel.ModeWrite || e.Mode == sdkmodel.ModeReadWrite {
				sig.RWEdges++
			}
		}
		sig.Resources = int64(len(resources))

		// Drain EVERY page of findings by the id keyset (sweep): a single
		// Limit:listCap page silently dropped a high/critical finding that sorted onto a
		// later page, so the classifier under-stated the AI-Act risk tier. A truncation
		// past the bounded page budget sets sig.Truncated, which suggestTier treats
		// fail-safe (never lowers below TierHigh).
		q := model.Query{
			Filters: []model.Filter{eq("subject_kind", "agent"), eq("subject_id", agentID.String())},
			Limit:   listCap,
		}
		for pages := 0; ; pages++ {
			finds, page, err := sc.Findings().List(ctx, q)
			if err != nil {
				return sig, err
			}
			for _, f := range finds {
				if f.Severity == model.SeverityHigh || f.Severity == model.SeverityCritical {
					sig.High++
				}
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			if pages+1 >= riskScanPages {
				sig.Truncated = true
				break
			}
			q.Cursor = page.Cursor
		}
	}
	// Optional autonomy signal — the default source returns the zero signal, so it can
	// only ever LOWER the suggested tier, never raise it on a fabricated input.
	if as, err := m.autonomy.Autonomy(ctx, tenant, subjectRef); err == nil {
		sig.Scheduled, sig.Autonomous = as.Scheduled, as.Autonomous
	}
	return sig, nil
}

// suggestTier maps observed signals to a conservative suggested tier + a rationale.
// It NEVER returns unacceptable (a legal Art. 5 determination reserved for a reviewer).
func suggestTier(s riskSignals) (RiskTier, string) {
	// Fail-safe (sweep): a truncated scan may have missed a high/critical finding,
	// so it must never suggest a tier BELOW TierHigh (the highest heuristic tier;
	// TierUnacceptable is a legal determination reserved for a human reviewer).
	if s.Truncated {
		return TierHigh, "high: finding scan truncated at the page budget; classified fail-safe (an unseen high/critical finding must not lower the tier)"
	}
	switch {
	case s.High > 0 || s.RWEdges >= rwHighThreshold || (s.Autonomous && s.RWEdges > 0):
		return TierHigh, fmt.Sprintf("high: %d high/critical finding(s), %d write access(es) across %d resource(s), autonomous=%t", s.High, s.RWEdges, s.Resources, s.Autonomous)
	case s.RWEdges > 0 || s.Autonomous:
		return TierLimited, fmt.Sprintf("limited: %d write access(es), autonomous=%t, scheduled=%t", s.RWEdges, s.Autonomous, s.Scheduled)
	default:
		return TierMinimal, fmt.Sprintf("minimal: %d total access edge(s), no high/critical findings, not autonomous", s.TotalEdges)
	}
}

func validTier(t RiskTier) bool {
	switch t {
	case TierUnacceptable, TierHigh, TierLimited, TierMinimal:
		return true
	default:
		return false
	}
}

func parseAgentID(explicit, subjectRef string) model.ID {
	if id, err := model.ParseID(strings.TrimSpace(explicit)); err == nil {
		return id
	}
	if id, err := model.ParseID(strings.TrimSpace(subjectRef)); err == nil {
		return id
	}
	return ""
}

func nullableID(id model.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}

func recordToRiskDTO(rec model.Record) RiskDTO {
	var signals map[string]any
	if s := rec.String(colSignals); s != "" {
		signals = map[string]any{}
		_ = jsonUnmarshal(s, &signals)
	}
	return RiskDTO{
		ID:            rec.String(model.ColID),
		SubjectKind:   rec.String(colSubjectKind),
		SubjectRef:    rec.String(colSubjectRef),
		AgentID:       rec.String(colAgentID),
		Tier:          rec.String(colTier),
		SuggestedTier: rec.String(colSuggested),
		State:         rec.String(colRiskState),
		Rationale:     rec.String(colRationale),
		NISTFunctions: decodeStrings(rec.String(colNistFns)),
		Signals:       signals,
		ReviewedBy:    rec.String(colReviewedBy),
		ClassifiedAt:  rec.String(colClassifiedAt),
		Disclaimer:    reportDisclaimer,
	}
}
