// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the module's PUBLIC read surface for composition-root adapters — the
// evals.ScoreOutputs pattern (docs/contracts): methods on the concrete *Module
// in the module's OWN DTOs, so the root (the only place authorized to import sibling
// modules) can bridge the module-II live read-model into the sampling seams
// (evals.SessionSource / sandbox.HistorySource —) with pure type translation.
// Both reads are minimal-data by construction: they expose behavioral signals and
// already-redacted references, never content (docs/SECURITY-HARDENING.md) — the timeline stores no
// payload column to begin with (schema.go).

// errNoData is the fail-closed answer of an un-wired module: a read before UseData
// errors honestly instead of dereferencing a nil handle.
var errNoData = errors.New("sessions: no data handle wired")

const (
	// sampleCap mirrors the store's max page (sqlstore clamps larger limits to 1000
	// SILENTLY). SampleLive is a single recency-sorted page — a keyset cursor cannot
	// combine with a custom sort — so the sample is bounded by construction and the
	// cap keeps that bound explicit rather than silent.
	sampleCap = 1000
	// defaultReplayMax bounds a reconstructed replay timeline when the caller does
	// not supply its own bound.
	defaultReplayMax = 10000
)

// LiveSampleQuery bounds a SampleLive read: optional exact-match filters, a recency
// window and a row cap, all in the module's own vocabulary.
type LiveSampleQuery struct {
	// SessionRef/AgentRef/ModelRef optionally narrow the sample by exact match
	// ("" = any). SessionRef is the EXTERNAL reference (the live row's key).
	SessionRef string
	AgentRef   string
	ModelRef   string
	// Window keeps only sessions whose last event is within now-Window (0 = no
	// recency bound). It is the sampling-freshness knob: the composition root
	// defaults it short so monitor samples stay recent.
	Window time.Duration
	// Limit caps the sample (<=0 or >sampleCap ⇒ sampleCap).
	Limit int
}

// LiveSample is the minimal-data behavioral view of one live session: liveness,
// attribution refs, live token/cost totals and the core findings attributed to it.
// It carries SIGNALS only — never output text, which the platform never persists.
type LiveSample struct {
	SessionRef   string
	AgentRef     string
	ModelRef     string
	CCState      string // active | idle | ended | silent_evasion (derived at read time)
	Findings     int    // core findings attributed to the session (see SampleLive)
	MaxSeverity  string // highest attributed core-finding severity ("" when none)
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64
	LastEventAt  time.Time
}

// SampleLive returns the most recently active live sessions matching q, newest
// first: one single recency-sorted page of at most min(q.Limit, sampleCap) rows —
// a SAMPLE of the current operation, not a full scan. Per row it derives the
// Claude Code state now and joins the CORE findings attributed to the session
// (count + max severity), so a caller scoring behavioral signals sees the
// canonical detective record, not just liveness.
func (m *Module) SampleLive(ctx context.Context, tenant model.TenantID, q LiveSampleQuery) ([]LiveSample, error) {
	if m.data == nil {
		return nil, errNoData
	}
	limit := q.Limit
	if limit <= 0 || limit > sampleCap {
		limit = sampleCap
	}
	var filters []model.Filter
	if q.SessionRef != "" {
		filters = append(filters, eq(colSessionRef, q.SessionRef))
	}
	if q.AgentRef != "" {
		filters = append(filters, eq(colAgentRef, q.AgentRef))
	}
	if q.ModelRef != "" {
		filters = append(filters, eq(colModelRef, q.ModelRef))
	}
	if q.Window > 0 {
		// Canonical timestamps are fixed-width, so a lexical >= is a chronological
		// bound the store can apply directly.
		cutoff := model.NewTimestamp(m.clock.Now().Time().Add(-q.Window)).String()
		filters = append(filters, model.Filter{Column: colLastEventAt, Op: model.OpGte, Value: cutoff})
	}
	var out []LiveSample
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(liveKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{
			Filters: filters,
			Sort:    []model.Sort{{Column: colLastEventAt, Desc: true}},
			Limit:   limit,
		})
		if err != nil {
			return err
		}
		out = make([]LiveSample, 0, len(recs))
		for _, rec := range recs {
			s := LiveSample{
				SessionRef:   rec.String(colSessionRef),
				AgentRef:     rec.String(colAgentRef),
				ModelRef:     rec.String(colModelRef),
				CCState:      m.deriveCC(rec),
				InputTokens:  rec.Int(colInputTokens),
				OutputTokens: rec.Int(colOutputTokens),
				CostMicroUSD: rec.Int(colCostMicroUSD),
			}
			if ts, terr := model.ParseTimestamp(rec.String(colLastEventAt)); terr == nil {
				s.LastEventAt = ts.Time()
			}
			count, maxSev, ferr := sessionFindings(ctx, sc, s.SessionRef)
			if ferr != nil {
				return ferr
			}
			s.Findings, s.MaxSeverity = count, maxSev
			out = append(out, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// severityRank orders core severities for the max aggregation.
var severityRank = map[model.Severity]int{
	model.SeverityLow: 1, model.SeverityMedium: 2, model.SeverityHigh: 3, model.SeverityCritical: 4,
}

// sessionFindings counts the CORE findings attributed to a session and returns their
// highest severity. Producers persist a session finding with SubjectKind "session"
// and SubjectID = the session's EXTERNAL ref parsed as a UUID (the repo-wide
// parseIDOrZero convention, e.g. modules/security/findings.go), so the join keys on
// the external ref — possible only when that ref IS a UUID. A non-UUID ref yields
// 0/"" honestly: those findings are not attributable by id, and querying the zero
// ID would match every unattributed finding in the tenant, never do that. The
// cursor loop drains fully so the count is never silently truncated at the store's
// max page.
func sessionFindings(ctx context.Context, sc store.Scope, ref string) (int, string, error) {
	id, err := model.ParseID(ref)
	if err != nil || id.IsZero() {
		// uuid.Parse ACCEPTS the all-zero UUID (in several textual forms), and a
		// zero subject filter must never be built — enforce it here, not only in
		// the store's write-side NULL encoding.
		return 0, "", nil
	}
	count := 0
	best, bestRank := "", 0
	q := model.Query{
		Filters: []model.Filter{eq("subject_kind", "session"), eq("subject_id", id.String())},
		Limit:   sampleCap,
	}
	for {
		recs, page, err := sc.Findings().List(ctx, q)
		if err != nil {
			return 0, "", err
		}
		count += len(recs)
		for _, f := range recs {
			if r := severityRank[f.Severity]; r > bestRank {
				best, bestRank = string(f.Severity), r
			}
		}
		if !page.HasMore || page.Cursor == "" {
			return count, best, nil
		}
		q.Cursor = page.Cursor
	}
}

// ReplayEvent is one replayable ACTION of a session's timeline — a tool or mcp
// event — in the module's own terms. Cost and finding entries are telemetry about
// the session, not inputs, so they are not replayable and never returned. All
// fields are the already-redacted references the connector emitted (a tool name, a
// sanitized resource ref); the timeline holds no raw input to reconstruct, and the
// module never invents one.
type ReplayEvent struct {
	Kind        string // tool | mcp
	ToolRef     string
	ResourceRef string
	Mode        string
	At          time.Time
}

// CredentialTimeline is a timeline event correlated by credential. It mirrors
// the API's timelineDTO but lives in the module's export vocabulary so the
// composition root can bridge without importing internal DTOs.
type CredentialTimeline struct {
	At          time.Time
	Kind        string // tool | mcp | cost | finding
	ToolRef     string
	ResourceRef string
	Mode        string
	Source      string
	Title       string
}

// TimelineByCredential resolves one operational-timeline page for a recording
// session's credential. Pagination deliberately reuses the timeline repository's
// opaque UUIDv7 keyset cursor: rows are ordered by their ingestion-time IDs, so
// appending an event between requests cannot shift or duplicate prior pages.
// Returns an empty result when no run matches — never an error for an unknown
// credential.
func (m *Module) TimelineByCredential(ctx context.Context, tenant model.TenantID, cred string, max int, cursor string) (sessionRef string, timeline []CredentialTimeline, nextCursor string, hasMore bool, err error) {
	if m.data == nil {
		return "", nil, "", false, errNoData
	}
	if cred == "" {
		return "", nil, "", false, nil
	}
	if max <= 0 {
		max = defaultReplayMax
	}

	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		// Step 1: find the run with this credential_id.
		runRepo, rerr := sc.Ext(runKind)
		if rerr != nil {
			return rerr
		}
		runs, _, rerr := runRepo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colCredentialID, cred)},
			Limit:   1,
		})
		if rerr != nil {
			return rerr
		}
		if len(runs) == 0 {
			return nil
		}

		// Step 2: extract the claude_session_id which is the session_ref for
		// the live/timeline tables.
		sessionRef = runs[0].String(colClaudeSessionID)
		if sessionRef == "" {
			return nil
		}

		// Step 3: read one logical page for that session_ref. A logical page may
		// span several store pages when max exceeds the store's 1000-row clamp.
		tlRepo, terr := sc.Ext(timelineKind)
		if terr != nil {
			return terr
		}
		q := model.Query{
			Filters: []model.Filter{eq(colTLSessionRef, sessionRef)},
			Cursor:  cursor,
		}
		for len(timeline) < max {
			q.Limit = min(max-len(timeline), sampleCap)
			recs, page, lerr := tlRepo.List(ctx, q)
			if lerr != nil {
				return lerr
			}
			for _, rec := range recs {
				ev := CredentialTimeline{
					Kind:        rec.String(colTLKind),
					ToolRef:     rec.String(colTLToolRef),
					ResourceRef: rec.String(colTLResource),
					Mode:        rec.String(colTLMode),
					Source:      rec.String(colTLSource),
					Title:       rec.String(colTLTitle),
				}
				if ts, perr := model.ParseTimestamp(rec.String(colTLAt)); perr == nil {
					ev.At = ts.Time()
				}
				timeline = append(timeline, ev)
			}
			if !page.HasMore || page.Cursor == "" {
				return nil
			}
			if len(timeline) == max {
				nextCursor, hasMore = page.Cursor, true
				return nil
			}
			q.Cursor = page.Cursor
		}
		return nil
	})
	return sessionRef, timeline, nextCursor, hasMore, err
}

// ReplayTimeline reconstructs the ordered action sequence of a session: every
// tool/mcp timeline event carrying at least one reference, in ingestion order (the
// time-ordered UUIDv7 row id — the same chronological order GET /live/{ref}/timeline
// serves). It drains the keyset cursor fully, so the silent store page clamp can
// never truncate a long session. max bounds the reconstruction (<=0 ⇒
// defaultReplayMax); when more replayable actions exist beyond it, the bounded
// prefix is returned with truncated=true so the caller can refuse a partial replay
// rather than silently re-execute a prefix. An unknown session yields an empty,
// honest result — never an error, never fabricated steps.
func (m *Module) ReplayTimeline(ctx context.Context, tenant model.TenantID, sessionRef string, max int) ([]ReplayEvent, bool, error) {
	if m.data == nil {
		return nil, false, errNoData
	}
	if sessionRef == "" {
		return nil, false, nil
	}
	if max <= 0 {
		max = defaultReplayMax
	}
	var out []ReplayEvent
	truncated := false
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(timelineKind)
		if err != nil {
			return err
		}
		q := model.Query{Filters: []model.Filter{eq(colTLSessionRef, sessionRef)}, Limit: sampleCap}
		for {
			recs, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				kind := rec.String(colTLKind)
				if kind != tlTool && kind != tlMCP {
					continue
				}
				ev := ReplayEvent{
					Kind:        kind,
					ToolRef:     rec.String(colTLToolRef),
					ResourceRef: rec.String(colTLResource),
					Mode:        rec.String(colTLMode),
				}
				if ev.ToolRef == "" && ev.ResourceRef == "" {
					// An action with no reference at all is not a replayable input.
					continue
				}
				if len(out) == max {
					truncated = true
					return nil
				}
				if ts, terr := model.ParseTimestamp(rec.String(colTLAt)); terr == nil {
					ev.At = ts.Time()
				}
				out = append(out, ev)
			}
			if !page.HasMore || page.Cursor == "" {
				return nil
			}
			q.Cursor = page.Cursor
		}
	})
	if err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}
