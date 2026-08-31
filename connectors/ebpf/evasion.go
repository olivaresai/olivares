// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"context"
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// evictionTTL bounds the detector's memory for a NON-cooperative instance: one
// that has been reported or never acted is forgotten after this much idle time.
const evictionTTL = time.Hour

// coopEvictionTTL is the much longer backstop for a cooperatively-latched instance.
// Such an instance is normally reclaimed by onExit; this only bounds memory if its
// process-exit event was missed. It is long so that a healthy agent which goes
// quiet and later resumes is never forgotten and re-evaluated as non-cooperative.
const coopEvictionTTL = 24 * time.Hour

// minSweepInterval floors the janitor's tick so a tiny evasion_window cannot busy-loop.
const minSweepInterval = time.Second

// evasionState tracks, per process INSTANCE (keyed by its stable process key), the
// signals that decide whether it is acting at the kernel without cooperative
// telemetry. Keying per instance (not per workload) means a freshly started agent
// is evaluated on its own behavior, not its predecessor's.
type evasionState struct {
	subjectRef    string    // the workload identity (originRef), for the finding
	detailKey     string    // the process key, hashed into DetailHash
	firstActivity time.Time // first kernel resource access (zero = none yet)
	lastSeen      time.Time // last signal of any kind (for eviction)
	coopSeen      bool      // ever observed connecting to a cooperative endpoint
	reported      bool      // a finding was already emitted for this instance
}

// evasionDetector is the kernel-side anti-evasion heuristic (docs/SECURITY-HARDENING.md). It is a
// HEURISTIC, not proof, and is OFF unless explicitly enabled. It flags an
// agent-classified process that performs kernel-observed resource access but is
// NEVER observed opening a connection to a configured cooperative-telemetry
// endpoint within evasion_window of its first activity. It cannot detect an agent
// tearing down an already-established cooperative connection mid-session (no new
// connect event fires); that case is covered cooperatively by watchdog and
// Findings are Severity Low to signal a weak, manually-verifiable
// signal (docs/SECURITY-HARDENING.md: honest confidence).
type evasionDetector struct {
	enabled    bool
	window     time.Duration
	classifier *classifier

	mu    sync.Mutex
	state map[string]*evasionState
}

// newEvasionDetector builds the detector from the resolved configuration. When
// disabled, its observe/sweep methods are cheap no-ops. The OTLP endpoint set is
// not held here: the caller classifies a connection as cooperative (matching
// otlp_endpoints) before calling observeCooperative.
func newEvasionDetector(cfg config) *evasionDetector {
	return &evasionDetector{
		enabled:    cfg.detectEvasion,
		window:     cfg.evasionWindow,
		classifier: newClassifier(cfg.agentSigs),
		state:      map[string]*evasionState{},
	}
}

// observeAccess records that an agent-classified process performed a kernel
// resource access at time at. Non-agent processes are ignored.
func (d *evasionDetector) observeAccess(pi procInfo, at time.Time) {
	if !d.enabled || !d.classifier.isAgent(pi) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.get(pi)
	if st.firstActivity.IsZero() {
		st.firstActivity = at
	}
	if at.After(st.lastSeen) {
		st.lastSeen = at
	}
}

// observeCooperative records that an agent-classified process connected to a
// cooperative-telemetry endpoint at time at. This latches the instance as
// cooperative for the rest of its observed life: a healthy agent opens its OTLP
// connection once and streams over it, so a single connect is sufficient evidence
// and re-checking would false-positive on the persistent connection.
func (d *evasionDetector) observeCooperative(pi procInfo, at time.Time) {
	if !d.enabled || !d.classifier.isAgent(pi) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.get(pi)
	st.coopSeen = true
	if at.After(st.lastSeen) {
		st.lastSeen = at
	}
}

// onExit forgets a process instance that ended, bounding memory promptly. A
// process that exits before its window elapses is never flagged (the window is a
// deliberate grace against flagging transient processes).
func (d *evasionDetector) onExit(pi procInfo) {
	if !d.enabled {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.state, pi.processKey())
}

// get returns the state for pi, creating it on first sight.
func (d *evasionDetector) get(pi procInfo) *evasionState {
	key := pi.processKey()
	st := d.state[key]
	if st == nil {
		st = &evasionState{subjectRef: pi.originRef(), detailKey: key}
		d.state[key] = st
	}
	return st
}

// sweep emits a finding for every agent instance that has been active past the
// window without ever being seen cooperative, and evicts instances idle beyond the
// eviction TTL. now drives the check deterministically (tests pass a fixed clock).
func (d *evasionDetector) sweep(now time.Time, emit func(model.Observation)) {
	if !d.enabled {
		return
	}
	d.mu.Lock()
	var findings []model.FindingReport
	for key, st := range d.state {
		// Fire BEFORE eviction so a gap that has just matured is never dropped by
		// the same sweep that would otherwise reclaim the instance.
		if !st.reported && !st.coopSeen && !st.firstActivity.IsZero() &&
			now.Sub(st.firstActivity) > d.window {
			st.reported = true
			findings = append(findings, gapFinding(st, now))
		}
		if d.evictable(st, now) {
			delete(d.state, key)
		}
	}
	d.mu.Unlock()
	for _, f := range findings {
		emit(f)
	}
}

// evictable reports whether an idle instance can be reclaimed without changing any
// future outcome. A cooperatively-latched instance is kept (until onExit or the
// long coopEvictionTTL backstop) so a healthy agent that goes quiet and resumes is
// never re-evaluated as non-cooperative — the false positive a plain TTL would
// cause. A non-cooperative instance is reclaimed only once it can no longer fire:
// it has already been reported, or it never acted. An active, not-yet-matured
// instance is kept so eviction can never precede a gap maturing.
func (d *evasionDetector) evictable(st *evasionState, now time.Time) bool {
	idle := now.Sub(st.lastSeen)
	if st.coopSeen {
		return idle > coopEvictionTTL
	}
	if st.reported || st.firstActivity.IsZero() {
		return idle > evictionTTL
	}
	return false
}

// run periodically sweeps until ctx is canceled. A final sweep after cancellation
// is the caller's responsibility (so a gap that matured during shutdown is still
// reported).
func (d *evasionDetector) run(ctx context.Context, emit func(model.Observation), now func() time.Time) {
	interval := d.window / 2
	if interval < minSweepInterval {
		interval = minSweepInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.sweep(now(), emit)
		}
	}
}

// gapFinding builds the anti-evasion finding for an agent instance that acted at
// the kernel without ever establishing cooperative telemetry.
func gapFinding(st *evasionState, now time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingAntiEvasion,
		Severity:    model.SeverityLow,
		SubjectKind: originIdentity,
		SubjectRef:  st.subjectRef,
		Title:       "agent workload active at the kernel without observed cooperative telemetry",
		DetailHash:  hashKey(st.detailKey),
		OccurredAt:  now,
	}
}
