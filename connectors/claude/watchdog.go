// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// evictionTTL is how long a session with no signal of either kind is kept before
// it is forgotten, bounding the watchdog's memory on a long-running receiver.
const evictionTTL = time.Hour

// sessionLiveness tracks the last time each kind of cooperative signal was seen
// for one session, so the watchdog can tell a normal quiet-down from a telemetry
// gap.
type sessionLiveness struct {
	lastOTEL    time.Time
	lastHook    time.Time
	otelSeen    bool
	hookSeen    bool
	gapReported bool
}

// watchdog is the anti-evasion detector (docs/SECURITY-HARDENING.md). Crucially it does NOT flag
// a session for merely going quiet — a finished agent is silent and that is
// normal. It flags the discrepancy that actually signals evasion: a session whose
// HOOKS are still firing while its OTEL has gone silent past the threshold, i.e.
// the agent disabled its OTEL_* exporters mid-session while continuing to act.
// Kernel-level ground truth for the non-cooperative case is the eBPF backstop
//; this is the cooperative-path heuristic, emitted with honest severity.
type watchdog struct {
	threshold time.Duration
	emit      func(model.Observation)

	mu       sync.Mutex
	sessions map[string]*sessionLiveness
}

// newWatchdog returns a watchdog that flags an OTEL gap once a session's OTEL has
// been silent for threshold while its hooks remain active, emitting through emit.
func newWatchdog(threshold time.Duration, emit func(model.Observation)) *watchdog {
	return &watchdog{threshold: threshold, emit: emit, sessions: map[string]*sessionLiveness{}}
}

// observeOTEL records that session emitted OTEL at time at.
func (w *watchdog) observeOTEL(sessionID string, at time.Time) {
	w.observe(sessionID, at, true)
}

// observeHook records that session emitted a hook at time at.
func (w *watchdog) observeHook(sessionID string, at time.Time) {
	w.observe(sessionID, at, false)
}

// observe updates the liveness for session. otel selects which signal advanced.
// Fresh activity of the silent kind clears a prior gap report so a recovered
// session can be flagged again if it relapses.
func (w *watchdog) observe(sessionID string, at time.Time, otel bool) {
	if sessionID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sessionID]
	if s == nil {
		s = &sessionLiveness{}
		w.sessions[sessionID] = s
	}
	if otel {
		s.lastOTEL, s.otelSeen = at, true
		s.gapReported = false // OTEL is back
	} else {
		s.lastHook, s.hookSeen = at, true
	}
}

// sweep emits a finding for every session whose OTEL has gone silent past the
// threshold while its hooks are still fresh, and evicts sessions idle on both
// signals beyond the eviction TTL. now drives the check deterministically.
func (w *watchdog) sweep(now time.Time) {
	w.mu.Lock()
	var findings []model.Observation
	for id, s := range w.sessions {
		if w.idle(s, now) {
			delete(w.sessions, id)
			continue
		}
		if s.otelSeen && s.hookSeen && !s.gapReported &&
			now.Sub(s.lastOTEL) > w.threshold && now.Sub(s.lastHook) <= w.threshold {
			s.gapReported = true
			findings = append(findings, gapFinding(id, now))
		}
	}
	w.mu.Unlock()
	for _, f := range findings {
		w.emit(f)
	}
}

// idle reports whether a session has been silent on both signals beyond the
// eviction TTL.
func (w *watchdog) idle(s *sessionLiveness, now time.Time) bool {
	last := s.lastOTEL
	if s.lastHook.After(last) {
		last = s.lastHook
	}
	return now.Sub(last) > evictionTTL
}

// gapFinding builds the anti-evasion finding for a session whose OTEL went silent
// while it kept acting through hooks.
func gapFinding(sessionID string, now time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "anti_evasion",
		Severity:    model.SeverityHigh,
		SubjectKind: originSession,
		SubjectRef:  sessionID,
		Title:       "Claude Code OTEL telemetry went silent while hooks remained active",
		DetailHash:  redact.Hash(sessionID),
		OccurredAt:  now,
	}
}
