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
	"github.com/olivaresai/olivares/sdk/event"
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
	return m.foldEdge(ctx, tenantRef, nil, edge)
}

// foldEdge is onEdge with the host-stamped registration (nil = legacy channel).
func (m *Module) foldEdge(ctx context.Context, tenantRef string, reg *event.SourceRegistration, edge sdkmodel.EdgeObservation) error {
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
		scope, err := m.scopeForRegistration(ctx, sc, reg)
		if err != nil {
			return err
		}
		rec, err := m.upsertLiveScoped(ctx, sc, ref, scope, at, func(rec model.Record, _ bool) {
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
			//
			// B2: a PROFILE fixes the driver. On an observed row the engine is the
			// binding's driver and a contradicting payload label does not move it.
			if scope.profileID != "" {
				rec[colEngine] = scope.provider
			} else if v := edge.Labels[labelEngine]; v != "" {
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
		if err := m.appendTimelineScoped(ctx, sc, ref, rec, scope, at, tlKind, edge.ToolRef, edge.ResourceRef, string(edge.Mode), string(edge.Source), edgeTitle(edge)); err != nil {
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
	return m.foldCost(ctx, tenantRef, nil, cost)
}

// foldCost is onCost with the host-stamped registration (nil = legacy channel).
func (m *Module) foldCost(ctx context.Context, tenantRef string, reg *event.SourceRegistration, cost sdkmodel.CostSample) error {
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
		scope, err := m.scopeForRegistration(ctx, sc, reg)
		if err != nil {
			return err
		}
		rec, err := m.upsertLiveScoped(ctx, sc, cost.SessionRef, scope, at, func(rec model.Record, _ bool) {
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
		if err := m.appendTimelineScoped(ctx, sc, cost.SessionRef, rec, scope, at, tlCost, "", "", "", "cost", title); err != nil {
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
	return m.foldFinding(ctx, tenantRef, nil, f)
}

// foldFinding is onFinding with the host-stamped registration (nil = legacy channel).
func (m *Module) foldFinding(ctx context.Context, tenantRef string, reg *event.SourceRegistration, f sdkmodel.FindingReport) error {
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
		scope, err := m.scopeForRegistration(ctx, sc, reg)
		if err != nil {
			return err
		}
		rec, err := m.upsertLiveScoped(ctx, sc, f.SubjectRef, scope, at, func(rec model.Record, _ bool) {
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
		if err := m.appendTimelineScoped(ctx, sc, f.SubjectRef, rec, scope, at, tlFinding, "", "", "", f.Kind, f.Title); err != nil {
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
	return m.upsertLiveScoped(ctx, sc, ref, liveScope{}, at, apply)
}

// upsertLiveScoped is upsertLive keyed by (scope, external id) — the B2 identity
// of a live row, enforced across restarts by the (tenant_id,
// COALESCE(observation_scope,'legacy'), session_ref) unique index. A new row is
// stamped with its scope; an existing row keeps the scope it was born with.
func (m *Module) upsertLiveScoped(ctx context.Context, sc store.Scope, ref string, scope liveScope, at time.Time, apply func(rec model.Record, isNew bool)) (model.Record, error) {
	repo, err := sc.Ext(liveKind)
	if err != nil {
		return nil, err
	}
	filters := liveKeyFilters(ref, scope.scope)
	existing, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
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
	applyScopeColumns(rec, scope)
	apply(rec, true)
	created, err := repo.Create(ctx, rec)
	if err == nil {
		return created, nil
	}
	// A redelivered/raced create can hit the unique index; re-read and update.
	if errors.Is(err, store.ErrConflict) {
		again, _, lerr := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
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
	return m.appendTimelineScoped(ctx, sc, ref, nil, liveScope{}, at, kind, toolRef, resourceRef, mode, source, title)
}

// appendTimelineScoped is appendTimeline that, for a SCOPED row, writes the exact
// live row id (live_ref) and the binding it was attributed under in the same
// mutation as the fold. Legacy rows keep session_ref alone, so the bare
// external-id timeline stays exactly the legacy events and a scoped row's timeline
// is selected by its id.
func (m *Module) appendTimelineScoped(ctx context.Context, sc store.Scope, ref string, live model.Record, scope liveScope, at time.Time, kind, toolRef, resourceRef, mode, source, title string) error {
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
	if !scope.legacy() && live != nil {
		setIf(rec, colTLLiveRef, live.String(model.ColID))
		setIf(rec, colTLBindingRef, scope.bindingRef)
	}
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
