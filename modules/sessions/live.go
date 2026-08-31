// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Cooperative-metadata wire strings sessions derives the live operational
// columns from. They are the connector's ResourceKind / FindingReport.Kind
// vocabulary (connectors/claude identity.go + observations.go), matched as
// literals — a module never imports a connector. agent_ref is attribution (which
// named Claude agent ran the session); a forensic finding (a context compaction)
// is the cooperative close/compaction metadata the summary is derived from. goal
// is NOT derivable here: it needs the prompt text, which the structural-by-default
// connector never carries in-process (it stays empty — minimal-data, never
// fabricated; docs/SECURITY-HARDENING.md).
// Label keys a connector uses to declare which engine a session belongs to and how
// firmly this action was governed. They mirror the connector-side constants
// (connectors/codex/session) — the SDK has no typed field for either, so the wire is
// the label map.
const (
	labelEngine  = "engine"
	labelPosture = "posture"
	// postureObserved is the WEAKER of the two postures; see the fold in onEdge.
	postureObserved = "observed"
)

const (
	resIdentityAgent    = "identity.agent" // session→agent attribution edge (OBS-09)
	forensicFindingKind = "forensic"       // context-compaction continuity finding (ANT2-09)
	maxSummaryLen       = 256              // defensive bound on the derived summary
)

// onEdge folds one of a session's actions into its live state: it bumps the
// activity counters, advances the current action (the last tool used), writes the
// agent attribution when the edge carries it, and appends a timeline entry. Only
// session-origin edges carry live operation; an edge whose origin is an
// agent/identity/mcp-server belongs to inventory, not to a live session.
func (m *Module) onEdge(ctx context.Context, tenantRef string, edge sdkmodel.EdgeObservation) error {
	if edge.OriginKind != "session" || edge.OriginRef == "" {
		return nil
	}
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return nil
	}
	ref := edge.OriginRef
	at := nonZeroTime(edge.ObservedAt, m.clock)

	var snap *liveSnapshot
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		rec, err := m.upsertLive(ctx, sc, ref, at, func(rec model.Record, _ bool) {
			rec[colEventCount] = rec.Int(colEventCount) + 1
			advanceLast(rec, at)
			if edge.ToolRef != "" {
				rec[colToolCalls] = rec.Int(colToolCalls) + 1
				rec[colCurrentTool] = edge.ToolRef
				rec[colCurrentRes] = edge.ResourceRef
				rec[colCurrentMode] = string(edge.Mode)
			}
			// SG-01: the engine and the enforcement posture the producing connector
			// declared. Labels are the SDK's attribution channel and are explicitly not
			// part of any dedup key, which is what makes them safe to fold here.
			//
			// An absent label NEVER clears a known value: a connector that does not
			// declare its engine leaves the session's engine as it was, so one
			// unlabelled fact cannot erase what an earlier labeled one established.
			// The posture takes the WEAKEST value seen: a session with one merely
			// observed action is not an enforced session, and rounding it up would be
			// the overstatement this column exists to prevent.
			if v := edge.Labels[labelEngine]; v != "" {
				rec[colEngine] = v
			}
			if v := edge.Labels[labelPosture]; v != "" {
				if rec.String(colPosture) != postureObserved {
					rec[colPosture] = v
				}
			}
			// agent_ref (was a dead column, schema.go:colAgentRef): written from the
			// connector's identity-attribution edge, never guessed. The session is
			// linked to its Claude agent once (OBS-09); the ref is already redacted
			// (an opaque agent name, not content).
			if edge.ResourceKind == resIdentityAgent && edge.ResourceRef != "" {
				rec[colAgentRef] = edge.ResourceRef
			}
		})
		if err != nil {
			return err
		}
		tlKind := tlTool
		if edge.ResourceKind == "mcp.server" {
			tlKind = tlMCP
		}
		if err := m.appendTimeline(ctx, sc, ref, at, tlKind, edge.ToolRef, edge.ResourceRef, string(edge.Mode), string(edge.Source), edgeTitle(edge)); err != nil {
			return err
		}
		s := m.snapshot(rec, tenant)
		snap = &s
		return nil
	})
	if err == nil && snap != nil {
		m.broker.publish(*snap)
	}
	return err
}

// onCost folds a cost sample into the session's live token and cost totals. The
// totals are the LIVE figure only; the canonical CostRecord/FinOps ledger is
// module XI. A cost sample with no session reference is not live operation.
func (m *Module) onCost(ctx context.Context, tenantRef string, cost sdkmodel.CostSample) error {
	if cost.SessionRef == "" {
		return nil
	}
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return nil
	}
	at := nonZeroTime(cost.OccurredAt, m.clock)

	var snap *liveSnapshot
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		rec, err := m.upsertLive(ctx, sc, cost.SessionRef, at, func(rec model.Record, _ bool) {
			rec[colInputTokens] = rec.Int(colInputTokens) + cost.InputTokens
			rec[colOutputTokens] = rec.Int(colOutputTokens) + cost.OutputTokens
			rec[colCostMicroUSD] = rec.Int(colCostMicroUSD) + cost.CostMicroUSD
			if cost.ModelRef != "" {
				rec[colModelRef] = cost.ModelRef
			}
			advanceLast(rec, at)
		})
		if err != nil {
			return err
		}
		title := fmt.Sprintf("%d in / %d out tokens", cost.InputTokens, cost.OutputTokens)
		if err := m.appendTimeline(ctx, sc, cost.SessionRef, at, tlCost, "", "", "", "cost", title); err != nil {
			return err
		}
		s := m.snapshot(rec, tenant)
		snap = &s
		return nil
	})
	if err == nil && snap != nil {
		m.broker.publish(*snap)
	}
	return err
}

// onFinding records a session-scoped finding on its live state and timeline. An
// anti-evasion finding (the connector's discrepancy signal) marks the
// session's Claude Code state silent-evasion; a health finding is timelined.
func (m *Module) onFinding(ctx context.Context, tenantRef string, f sdkmodel.FindingReport) error {
	if f.SubjectKind != "session" || f.SubjectRef == "" {
		return nil
	}
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return nil
	}
	at := nonZeroTime(f.OccurredAt, m.clock)

	var snap *liveSnapshot
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		rec, err := m.upsertLive(ctx, sc, f.SubjectRef, at, func(rec model.Record, _ bool) {
			if f.Kind == "anti_evasion" {
				rec[colEvasionAt] = model.NewTimestamp(at).String()
			}
			// summary (was a dead column, schema.go:colSummary): derived from the
			// cooperative close/compaction metadata (a forensic-continuity finding,
			// e.g. a context compaction), bounded and non-sensitive — the finding
			// Title is a safe-to-display summary by contract, never raw transcript.
			// Empty until such metadata arrives; NEVER an LLM-fabricated summary.
			if f.Kind == forensicFindingKind && f.Title != "" {
				rec[colSummary] = clampSummary(f.Title)
			}
			advanceLast(rec, at)
		})
		if err != nil {
			return err
		}
		if err := m.appendTimeline(ctx, sc, f.SubjectRef, at, tlFinding, "", "", "", f.Kind, f.Title); err != nil {
			return err
		}
		s := m.snapshot(rec, tenant)
		snap = &s
		return nil
	})
	if err == nil && snap != nil {
		m.broker.publish(*snap)
	}
	return err
}

// upsertLive find-or-creates the live row for a session reference and applies the
// mutator, returning the persisted record. It is idempotent within the single
// subscriber goroutine and backed by the (tenant_id, session_ref) unique index
// across restarts.
func (m *Module) upsertLive(ctx context.Context, sc store.Scope, ref string, at time.Time, apply func(rec model.Record, isNew bool)) (model.Record, error) {
	repo, err := sc.Ext(liveKind)
	if err != nil {
		return nil, err
	}
	existing, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSessionRef, ref)}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		rec := existing[0]
		apply(rec, false)
		return repo.Update(ctx, rec)
	}
	atTS := model.NewTimestamp(at).String()
	rec := model.Record{
		colSessionRef:   ref,
		colInputTokens:  int64(0),
		colOutputTokens: int64(0),
		colCostMicroUSD: int64(0),
		colEventCount:   int64(0),
		colToolCalls:    int64(0),
		colFirstEventAt: atTS,
		colLastEventAt:  atTS,
	}
	apply(rec, true)
	created, err := repo.Create(ctx, rec)
	if err == nil {
		return created, nil
	}
	// A redelivered/raced create can hit the unique index; re-read and update.
	if errors.Is(err, store.ErrConflict) {
		again, _, lerr := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSessionRef, ref)}, Limit: 1})
		if lerr != nil {
			return nil, lerr
		}
		if len(again) > 0 {
			rec := again[0]
			apply(rec, false)
			return repo.Update(ctx, rec)
		}
	}
	return nil, err
}

// appendTimeline records one replayable event in a session's history. Rows are
// ordered by their time-ordered id (ingestion order), so the timeline is
// keyset-paginated chronologically.
func (m *Module) appendTimeline(ctx context.Context, sc store.Scope, ref string, at time.Time, kind, toolRef, resourceRef, mode, source, title string) error {
	repo, err := sc.Ext(timelineKind)
	if err != nil {
		return err
	}
	rec := model.Record{
		colTLSessionRef: ref,
		colTLAt:         model.NewTimestamp(at).String(),
		colTLKind:       kind,
	}
	setIf(rec, colTLToolRef, toolRef)
	setIf(rec, colTLResource, resourceRef)
	setIf(rec, colTLMode, mode)
	setIf(rec, colTLSource, source)
	setIf(rec, colTLTitle, title)
	_, err = repo.Create(ctx, rec)
	return err
}

// advanceLast moves last_event_at forward to at (canonical timestamps sort
// lexically, so a string compare is a valid chronological advance).
func advanceLast(rec model.Record, at time.Time) {
	atTS := model.NewTimestamp(at).String()
	if cur := rec.String(colLastEventAt); cur == "" || cur < atTS {
		rec[colLastEventAt] = atTS
	}
}

// deriveCC derives the displayed Claude Code state from the live record at read
// time. It is never stored: a session that stopped emitting is honestly idle
// then ended (silence is normal); only the sticky anti-evasion signal
// overrides recency.
func (m *Module) deriveCC(rec model.Record) string {
	now := m.clock.Now().Time()
	if ev := rec.String(colEvasionAt); ev != "" {
		if t, err := model.ParseTimestamp(ev); err == nil && now.Sub(t.Time()) <= m.idleWindow {
			return ccEvasion
		}
	}
	if t, err := model.ParseTimestamp(rec.String(colLastEventAt)); err == nil {
		switch d := now.Sub(t.Time()); {
		case d <= m.activeWindow:
			return ccActive
		case d <= m.idleWindow:
			return ccIdle
		default:
			return ccEnded
		}
	}
	return ccEnded
}

// nonZeroTime returns t's time, or the clock's now when t is the zero instant.
func nonZeroTime(t time.Time, clock model.Clock) time.Time {
	if t.IsZero() {
		return clock.Now().Time()
	}
	return t
}

// edgeTitle builds a short, non-sensitive timeline title from an edge's already
// redacted references.
func edgeTitle(edge sdkmodel.EdgeObservation) string {
	switch {
	case edge.ToolRef != "" && edge.ResourceRef != "":
		return edge.ToolRef + " " + edge.ResourceRef
	case edge.ToolRef != "":
		return edge.ToolRef
	case edge.ResourceRef != "":
		return edge.ResourceRef
	default:
		return edge.ResourceKind
	}
}

// clampSummary bounds a derived summary to maxSummaryLen bytes on a rune boundary:
// it trims trailing bytes until the prefix is valid UTF-8, dropping any rune the byte
// cut left incomplete (its lead byte too, not only continuation bytes), so a long
// finding title never becomes an unbounded or invalid-UTF-8 value in the live row.
func clampSummary(s string) string {
	if len(s) <= maxSummaryLen {
		return s
	}
	b := s[:maxSummaryLen]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

// setIf stores v under col only when non-empty (leaving a nullable column NULL).
func setIf(rec model.Record, col, v string) {
	if v != "" {
		rec[col] = v
	}
}

// eq is a shorthand for an equality filter.
func eq(col, val string) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}
