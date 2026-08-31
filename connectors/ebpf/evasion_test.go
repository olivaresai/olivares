// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olivaresai/olivares/sdk/model"
)

// collector captures findings emitted by the detector.
type collector struct{ findings []model.FindingReport }

func (c *collector) emit(o model.Observation) {
	if f, ok := o.(model.FindingReport); ok {
		c.findings = append(c.findings, f)
	}
}

func enabledDetector(window time.Duration) *evasionDetector {
	return newEvasionDetector(config{
		detectEvasion: true,
		evasionWindow: window,
		otlpEndpoints: []string{"127.0.0.1:4317"},
		agentSigs:     []string{"claude"},
	})
}

var agentProc = procInfo{execID: "agent-1", exeBase: "claude", container: "c0ffee123456"}
var plainProc = procInfo{execID: "proc-2", exeBase: "python3"}

func TestEvasionGapFiresWhenAgentActiveWithoutCooperative(t *testing.T) {
	d := enabledDetector(time.Minute)
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	d.observeAccess(agentProc, base)

	var c collector
	d.sweep(base.Add(30*time.Second), c.emit) // within window: nothing yet
	require.Empty(t, c.findings)

	d.sweep(base.Add(2*time.Minute), c.emit) // past window: one finding
	require.Len(t, c.findings, 1)
	f := c.findings[0]
	assert.Equal(t, findingAntiEvasion, f.Kind)
	assert.Equal(t, model.SeverityLow, f.Severity)
	assert.Equal(t, originIdentity, f.SubjectKind)
	assert.Equal(t, agentProc.originRef(), f.SubjectRef)
	assert.Equal(t, hashKey(agentProc.processKey()), f.DetailHash)
	assert.Equal(t, model.ObsFinding, f.ObservationType())

	// Idempotent: a later sweep does not re-report the same instance.
	d.sweep(base.Add(5*time.Minute), c.emit)
	assert.Len(t, c.findings, 1)
}

func TestEvasionNoGapWhenCooperativeSeen(t *testing.T) {
	d := enabledDetector(time.Minute)
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	d.observeCooperative(agentProc, base)             // connected to OTLP once
	d.observeAccess(agentProc, base.Add(time.Second)) // then did file work

	var c collector
	d.sweep(base.Add(10*time.Minute), c.emit)
	assert.Empty(t, c.findings, "an agent seen cooperative is never flagged")
}

func TestEvasionNoGapWhenDisabled(t *testing.T) {
	d := newEvasionDetector(config{detectEvasion: false, evasionWindow: time.Minute, agentSigs: []string{"claude"}})
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d.observeAccess(agentProc, base)
	var c collector
	d.sweep(base.Add(time.Hour), c.emit)
	assert.Empty(t, c.findings, "off by default")
}

func TestEvasionIgnoresNonAgent(t *testing.T) {
	d := enabledDetector(time.Minute)
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d.observeAccess(plainProc, base)
	var c collector
	d.sweep(base.Add(time.Hour), c.emit)
	assert.Empty(t, c.findings, "non-agent processes are not subject to the gap check")
}

func TestEvasionOnExitEvicts(t *testing.T) {
	d := enabledDetector(time.Minute)
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d.observeAccess(agentProc, base)
	d.onExit(agentProc) // exited before the window matured

	var c collector
	d.sweep(base.Add(time.Hour), c.emit)
	assert.Empty(t, c.findings, "an exited instance is not flagged")
}

func TestEvasionNoGapWithoutActivity(t *testing.T) {
	d := enabledDetector(time.Minute)
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	// State exists for an agent that has done no kernel resource access yet
	// (firstActivity is zero). It must never be flagged regardless of elapsed time.
	d.get(agentProc)
	var c collector
	d.sweep(base.Add(time.Hour), c.emit)
	assert.Empty(t, c.findings, "an agent with no resource activity is never flagged")
}

func TestEvasionFiresBeforeEviction(t *testing.T) {
	d := enabledDetector(time.Minute) // window 1m, evictionTTL 1h
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d.observeAccess(agentProc, base)

	// Sweep at a time past BOTH the window and the eviction TTL: the matured gap
	// must still fire in the very sweep that then reclaims the instance.
	var c collector
	d.sweep(base.Add(2*time.Hour), c.emit)
	require.Len(t, c.findings, 1, "a matured gap fires even in the sweep that evicts it")
}

func TestEvasionEvictsReportedNonCooperativeState(t *testing.T) {
	d := enabledDetector(time.Minute)
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d.observeAccess(agentProc, base)

	var c collector
	d.sweep(base.Add(2*time.Minute), c.emit) // fires; marks reported
	require.Len(t, c.findings, 1)

	d.sweep(base.Add(2*evictionTTL), func(model.Observation) {}) // now idle past TTL
	assert.False(t, d.has(agentProc), "a reported non-cooperative instance is reclaimed after the idle TTL")
}

func TestEvasionKeepsCooperativeState(t *testing.T) {
	d := enabledDetector(time.Minute)
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d.observeCooperative(agentProc, base)

	// A cooperatively-latched instance survives the non-cooperative TTL, so a quiet
	// healthy agent that resumes is never re-evaluated as evading (the false
	// positive a plain TTL would cause).
	d.sweep(base.Add(2*evictionTTL), func(model.Observation) {})
	assert.True(t, d.has(agentProc), "a cooperative instance is not evicted at the non-coop TTL")

	d.sweep(base.Add(2*coopEvictionTTL), func(model.Observation) {})
	assert.False(t, d.has(agentProc), "a cooperative instance is reclaimed after the long backstop")
}

// has reports whether state for pi is currently retained (test helper).
func (d *evasionDetector) has(pi procInfo) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.state[pi.processKey()]
	return ok
}
