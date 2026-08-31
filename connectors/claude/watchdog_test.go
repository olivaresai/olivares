// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"
	"time"
)

func TestWatchdogNoFalsePositiveOnNormalSilence(t *testing.T) {
	c := &collect{}
	wd := newWatchdog(time.Minute, c.emit)
	// A session that only ever emitted OTEL, then went quiet (a finished agent),
	// must NOT be flagged — silence at the end is normal.
	wd.observeOTEL("s", testTime)
	wd.sweep(testTime.Add(10 * time.Minute))
	if len(c.findings()) != 0 {
		t.Errorf("normal silence flagged: %d findings", len(c.findings()))
	}
}

func TestWatchdogFlagsCrossSignalGap(t *testing.T) {
	c := &collect{}
	wd := newWatchdog(time.Minute, c.emit)
	// OTEL last seen at T; hooks keep firing past T+threshold → OTEL was disabled
	// mid-session while the agent kept acting. This is the evasion signal.
	wd.observeOTEL("s", testTime)
	wd.observeHook("s", testTime.Add(90*time.Second))
	wd.sweep(testTime.Add(95 * time.Second))
	f := c.findings()
	if len(f) != 1 {
		t.Fatalf("cross-signal gap not flagged: %d findings", len(f))
	}
	if f[0].Kind != "anti_evasion" || f[0].SubjectRef != "s" {
		t.Errorf("bad finding: %+v", f[0])
	}
	// Idempotent: a second sweep in the same gap must not re-report.
	wd.sweep(testTime.Add(100 * time.Second))
	if len(c.findings()) != 1 {
		t.Errorf("gap re-reported: %d", len(c.findings()))
	}
}

func TestWatchdogRecoveryReArms(t *testing.T) {
	c := &collect{}
	wd := newWatchdog(time.Minute, c.emit)
	wd.observeOTEL("s", testTime)
	wd.observeHook("s", testTime.Add(90*time.Second))
	wd.sweep(testTime.Add(95 * time.Second)) // first gap report
	// OTEL recovers, then lapses again with fresh hooks → a second report allowed.
	wd.observeOTEL("s", testTime.Add(120*time.Second))
	wd.observeHook("s", testTime.Add(200*time.Second))
	wd.sweep(testTime.Add(205 * time.Second))
	if len(c.findings()) != 2 {
		t.Errorf("recovery did not re-arm: %d findings", len(c.findings()))
	}
}

func TestWatchdogOnlyOneSignalNoGap(t *testing.T) {
	c := &collect{}
	wd := newWatchdog(time.Minute, c.emit)
	// Hooks only, never any OTEL → cannot conclude OTEL was suppressed.
	wd.observeHook("s", testTime)
	wd.observeHook("s", testTime.Add(2*time.Minute))
	wd.sweep(testTime.Add(2 * time.Minute))
	if len(c.findings()) != 0 {
		t.Errorf("single-signal session flagged: %d", len(c.findings()))
	}
}

func TestWatchdogEviction(t *testing.T) {
	c := &collect{}
	wd := newWatchdog(time.Minute, c.emit)
	wd.observeOTEL("s", testTime)
	wd.observeHook("s", testTime)
	// Far beyond the eviction TTL: the session is forgotten (no leak), and being
	// gone it cannot be flagged.
	wd.sweep(testTime.Add(2 * evictionTTL))
	if len(wd.sessions) != 0 {
		t.Errorf("session not evicted: %d remain", len(wd.sessions))
	}
}
