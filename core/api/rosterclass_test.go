// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "testing"

// The table is TOTAL over the statuses the roster can carry, crossed with both
// values of `enabled`. That is the point: the defect this file closes was three
// call sites each matching the literal "failed", so every OTHER status fell into
// whichever bucket that site's switch happened to have — and `not_wired` fell into
// "disabled" on a row the operator had explicitly ENABLED.
func TestClassifyRosterEntryIsTotalAndFailsClosed(t *testing.T) {
	cases := []struct {
		enabled bool
		status  string
		want    rosterClass
		isErr   bool
	}{
		{true, "running", rosterRunning, false},
		{true, "failed", rosterErrored, true},
		// The row this whole file exists for: enabled, and the engine refused to
		// wire it. Same consequence as "failed" — no data — so same class.
		{true, "not_wired", rosterErrored, true},
		{true, "stopped", rosterHalted, false},
		// enabled:true with status "disabled" is a contradiction the roster should
		// not produce. It is surfaced, not filed away as switched-off.
		{true, "disabled", rosterUnknownStatus, true},
		// A status added by a future build. It must not read as healthy.
		{true, "quiesced", rosterUnknownStatus, true},
		{true, "", rosterUnknownStatus, true},

		// enabled == false always means disabled, whatever the status says: the
		// operator switched it off and nothing about it is an error.
		{false, "running", rosterDisabled, false},
		{false, "failed", rosterDisabled, false},
		{false, "not_wired", rosterDisabled, false},
		{false, "anything", rosterDisabled, false},
	}
	for _, c := range cases {
		got := classifyRosterEntry(c.enabled, c.status)
		if got != c.want {
			t.Errorf("classify(enabled=%v, %q) = %v, want %v", c.enabled, c.status, got, c.want)
		}
		if got.isRosterError() != c.isErr {
			t.Errorf("classify(enabled=%v, %q).isRosterError() = %v, want %v",
				c.enabled, c.status, got.isRosterError(), c.isErr)
		}
	}
}

// The zero value of rosterClass has to be the UNKNOWN one, so a switch that
// forgets a case cannot default into "healthy". This is a structural property, not
// a style preference, and it is asserted so a reordering of the const block cannot
// quietly invert it.
func TestUnknownRosterStatusIsTheZeroValue(t *testing.T) {
	var zero rosterClass
	if zero != rosterUnknownStatus {
		t.Fatalf("the zero rosterClass is %v; an uninitialised classification must not mean healthy", zero)
	}
	if !zero.isRosterError() {
		t.Fatal("the zero rosterClass does not count as an error; a forgotten case would pass as fine")
	}
}

func TestRosterTallyKeepsEnabledFailuresOutOfTheDisabledBucket(t *testing.T) {
	var tally rosterTally
	for _, e := range []struct {
		enabled bool
		status  string
	}{
		{true, "running"},
		{true, "not_wired"}, // enabled and never wired
		{true, "failed"},
		{true, "stopped"},
		{false, "not_wired"}, // genuinely switched off
	} {
		tally.add(e.enabled, e.status)
	}
	if tally.Total != 5 {
		t.Fatalf("total = %d, want 5", tally.Total)
	}
	if tally.Errored != 2 {
		t.Errorf("errored = %d, want 2 (failed + enabled not_wired)", tally.Errored)
	}
	if tally.Disabled != 1 {
		t.Errorf("disabled = %d, want 1 — only the row with enabled=false", tally.Disabled)
	}
	if tally.Running != 1 || tally.Halted != 1 {
		t.Errorf("running=%d halted=%d, want 1 and 1", tally.Running, tally.Halted)
	}
	// enabledTotal is the denominator for "is the fleet whole". Diluting it with
	// the rows nobody expects to run turns a total outage into "degraded".
	if got := tally.enabledTotal(); got != 4 {
		t.Errorf("enabledTotal = %d, want 4", got)
	}
}

// A fleet whose every ENABLED row is broken is an outage even when disabled rows
// outnumber them — the case a naive `failed == len(sources)` gets wrong.
func TestEnabledTotalIsNotDilutedByDisabledRows(t *testing.T) {
	var tally rosterTally
	tally.add(true, "not_wired")
	for i := 0; i < 9; i++ {
		tally.add(false, "stopped")
	}
	if tally.enabledTotal() != 1 || tally.Errored != 1 {
		t.Fatalf("enabledTotal=%d errored=%d, want 1 and 1", tally.enabledTotal(), tally.Errored)
	}
	if tally.Errored != tally.enabledTotal() {
		t.Error("every enabled row is broken, so this must read as a total outage")
	}
}
