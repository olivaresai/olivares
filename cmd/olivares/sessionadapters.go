// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/modules/sandbox"
	"github.com/olivaresai/olivares/modules/sessions"
)

// the module-II (sessions) live read-model backs the two sampling seams.
// Both adapters are pure translation in the evalsScorerAdapter mold — the data reads
// live in the sessions module's public surface (SampleLive / ReplayTimeline), this
// file only maps DTOs — and both are ALWAYS wired (in-process, no operator config):
//
//   - evals.SessionSource ← sessions.SampleLive: POST /monitor samples REAL sessions
//     (derived cc_state, live tokens/cost, core findings joined by the external-UUID
//     ref convention) within a short recency window, so monitor samples stay fresh
//     and bounded (the short-retention posture; the persisted per-sample
//     EvalResults remain governed by retention).
//   - sandbox.HistorySource ← sessions.ReplayTimeline: POST /replay and the
//     session branch of POST /compare re-execute the session's ordered tool/mcp
//     action sequence. Inputs are the already-redacted refs the connector emitted;
//     zero actions stays an honestly DEGRADED replay, never fabricated.

const (
	// envEvalsMonitorWindow bounds how far back the evals monitor samples real
	// sessions: a Go duration ("" → defaultEvalsMonitorWindow; an explicit "0" →
	// no recency bound). Short by default so a monitor launch scores the RECENT
	// operation instead of re-judging the whole historical estate on every call.
	envEvalsMonitorWindow     = "OLIVARES_EVALS_MONITOR_WINDOW"
	defaultEvalsMonitorWindow = 24 * time.Hour

	// maxReplaySteps bounds a reconstructed replay timeline. Beyond it the adapter
	// REFUSES a partial replay (error ⇒ the sandbox records the run honestly
	// DEGRADED with zero steps) rather than silently re-executing a prefix that
	// would misrepresent the session.
	maxReplaySteps = 10000
)

// loadEvalsMonitorWindow resolves the monitor sampling window from the environment
// (the loadEventingOptions idiom: invalid values warn loudly and keep the default,
// never a boot failure).
func loadEvalsMonitorWindow(getenv func(string) string, log *slog.Logger) time.Duration {
	raw := strings.TrimSpace(getenv(envEvalsMonitorWindow))
	if raw == "" {
		return defaultEvalsMonitorWindow
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warn("evals: monitor sampling window is not a valid non-negative duration; using the default",
			"env", envEvalsMonitorWindow, "value", raw, "default", defaultEvalsMonitorWindow.String())
		return defaultEvalsMonitorWindow
	}
	return d
}

// sessionsSampleAdapter bridges the evals.SessionSource seam to the sessions
// module's public SampleLive: real, bus-observed sessions with module II's honest
// cc_state vocabulary (which evals' scoreSignal understands — silent_evasion is
// never a pass) and the canonical core findings attributed to each. It reads
// through the sessions module's own data handle, OUTSIDE the monitor's write
// transaction (handleMonitor samples a wired source before opening it).
type sessionsSampleAdapter struct {
	ss     *sessions.Module
	window time.Duration
	log    *slog.Logger
}

var _ evals.SessionSource = sessionsSampleAdapter{}

func (a sessionsSampleAdapter) Sample(ctx context.Context, tenant model.TenantID, q evals.SampleQuery) ([]evals.SessionSample, error) {
	lq, ok := liveQueryFor(q, a.window)
	if !ok {
		// An unsupported subject kind narrows to NOTHING (module II indexes
		// sessions by agent/model/session refs only). Sampling everything instead
		// would silently ignore the caller's filter; warn and return the honest
		// empty sample.
		if a.log != nil {
			a.log.Warn("evals: monitor subject_kind not sampleable from sessions; returning an empty sample",
				"subject_kind", q.SubjectKind)
		}
		return nil, nil
	}
	live, err := a.ss.SampleLive(ctx, tenant, lq)
	if err != nil {
		return nil, err
	}
	out := make([]evals.SessionSample, 0, len(live))
	for _, s := range live {
		out = append(out, evals.SessionSample{
			SessionRef:   s.SessionRef,
			AgentRef:     s.AgentRef,
			ModelRef:     s.ModelRef,
			State:        s.CCState,
			MaxSeverity:  s.MaxSeverity,
			Findings:     s.Findings,
			InputTokens:  s.InputTokens,
			OutputTokens: s.OutputTokens,
			CostMicroUSD: s.CostMicroUSD,
			OccurredAt:   s.LastEventAt,
		})
	}
	return out, nil
}

// liveQueryFor translates the evals subject filter + cap into the sessions module's
// own query. ok=false flags a subject kind module II cannot index ("" means
// unfiltered; an empty ref leaves the kind unfiltered too, matching the lenient
// SampleQuery contract "optionally narrow"). An exact single-session probe drops
// the recency window: the caller named ONE subject, and silently hiding it because
// it is older than the sampling window would be indistinguishable from "does not
// exist" — the window bounds SAMPLES, never an explicit lookup.
func liveQueryFor(q evals.SampleQuery, window time.Duration) (sessions.LiveSampleQuery, bool) {
	lq := sessions.LiveSampleQuery{Window: window, Limit: q.Limit}
	ref := strings.TrimSpace(q.SubjectRef)
	switch strings.TrimSpace(q.SubjectKind) {
	case "":
	case "agent":
		lq.AgentRef = ref
	case "model":
		lq.ModelRef = ref
	case "session":
		lq.SessionRef = ref
		if ref != "" {
			lq.Window = 0
		}
	default:
		return sessions.LiveSampleQuery{}, false
	}
	return lq, true
}

// sessionsHistoryAdapter bridges the sandbox.HistorySource seam to the sessions
// module's public ReplayTimeline: the session's ordered tool/mcp actions become
// replay steps. A Timeline error reaches the sandbox as an error, which it logs and
// degrades on (zero steps) — never a fabricated input.
//
// A test-only mirror of this adapter lives in
// modules/sandbox/sessions_integration_test.go (a module test cannot import the
// composition root) — keep the two in lockstep when changing the key/input mapping.
type sessionsHistoryAdapter struct {
	ss *sessions.Module
	// max overrides the replay bound (tests); <=0 → maxReplaySteps.
	max int
}

var _ sandbox.HistorySource = sessionsHistoryAdapter{}

func (a sessionsHistoryAdapter) Timeline(ctx context.Context, tenant model.TenantID, sessionRef string) ([]sandbox.ReplayStep, error) {
	bound := a.max
	if bound <= 0 {
		bound = maxReplaySteps
	}
	events, truncated, err := a.ss.ReplayTimeline(ctx, tenant, sessionRef, bound)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("session %q timeline exceeds %d replayable steps; refusing a partial replay", sessionRef, bound)
	}
	steps := make([]sandbox.ReplayStep, 0, len(events))
	for i, ev := range events {
		steps = append(steps, sandbox.ReplayStep{Key: replayStepKey(i, ev), Input: replayStepInput(ev)})
	}
	return steps, nil
}

// replayStepKey is a ZERO-PADDED 1-based sequence number plus the action's
// tool/kind label. The padding is load-bearing: sandbox re-sorts outputs
// lexicographically by step_key at read time (loadRunOutputs), so keys must sort in
// execution order ('step-10' < 'step-2' would lie past ten steps); the index also
// guarantees uniqueness (duplicate keys collapse in the scorer's outputs map).
func replayStepKey(i int, ev sessions.ReplayEvent) string {
	label := ev.ToolRef
	if label == "" {
		label = ev.Kind
	}
	return fmt.Sprintf("%05d %s", i+1, label)
}

// replayStepInput is the already-redacted reference the action asked for — the
// value a replay mock matches on (Mock.Resource == Step.Input). An action with no
// resource replays as the tool invocation itself. Timeline titles are display
// text, never inputs.
func replayStepInput(ev sessions.ReplayEvent) string {
	if ev.ResourceRef != "" {
		return ev.ResourceRef
	}
	return ev.ToolRef
}
