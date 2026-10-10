// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"
)

// --- C2 item 6: the lineage repair cadence ------------------------------------

type spyScheduler struct {
	name      string
	interval  time.Duration
	immediate bool
	calls     int
}

func (s *spyScheduler) SchedulePeriodic(name string, interval time.Duration, runImmediately bool, _ func(context.Context) error) error {
	s.calls++
	s.name, s.interval, s.immediate = name, interval, runImmediately
	return nil
}

func TestRunLineageRepairRegistersOnceImmediateThenFifteenMinutes(t *testing.T) {
	spy := &spyScheduler{}
	loop := &runLineageRepairLoop{interval: runLineageRepairInterval}
	if err := loop.register(spy); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Fatalf("registered %d times, want 1", spy.calls)
	}
	if spy.interval != 15*time.Minute {
		t.Fatalf("the steady-state cadence = %s, want 15m", spy.interval)
	}
	if !spy.immediate {
		t.Fatal("the first pass runs at boot (runImmediately), then every 15 minutes")
	}
	if spy.name != runLineageRepairJobName {
		t.Fatalf("job name = %q, want %q", spy.name, runLineageRepairJobName)
	}
}
