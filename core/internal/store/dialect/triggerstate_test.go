// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package dialect

import (
	"strings"
	"testing"
)

// TestTriggerEnableStateFires pins the mapping from the catalog's raw value to
// "does this guard run", at the layer that owns it.
//
// The whole point of keeping the raw character is that this decision is a pure
// function and can be exercised without a PostgreSQL server. So exercise it.
func TestTriggerEnableStateFires(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state TriggerEnableState
		fires bool
		why   string
	}{
		{TriggerFiresOrigin, true, "the default: fires in origin and local sessions"},
		{TriggerFiresAlways, true, "ENABLE ALWAYS fires under every replication role"},
		{TriggerNoEnableState, true, "an engine with no per-trigger state fires unconditionally"},
		{TriggerNeverFires, false, "DISABLE TRIGGER: listed in the catalog, never runs"},
		{TriggerFiresReplicaOnly, false, "ENABLE REPLICA: this engine refuses replica sessions, so never"},
		{TriggerStateUnknown, false, "nothing was read: deny-closed"},
		{TriggerEnableState("Z"), false, "a state this build cannot interpret: deny-closed"},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			if got := tc.state.Fires(); got != tc.fires {
				t.Fatalf("Fires(%q) = %v, want %v — %s", tc.state, got, tc.fires, tc.why)
			}
			if got := tc.state.Describe(); strings.TrimSpace(got) == "" {
				t.Fatalf("Describe(%q) is empty; a boot error must be able to name the state", tc.state)
			}
		})
	}
}

// TestSQLiteStateIsNotPostgresEnableAlways is the regression for a conflation that
// a mutation caught rather than a reviewer.
//
// SQLite has no per-trigger enable state, and mapping it onto PostgreSQL's 'A'
// looked harmless. It was not: 'A' is a state an operator CHOSE, and there is an
// open policy question about accepting it (an ALWAYS trigger also fires on a
// subscriber applying replicated rows). The moment that policy said "refuse
// ALWAYS", it would have refused EVERY SQLite trigger — on an engine where the
// state cannot be chosen at all. Two different facts must not share one value.
func TestSQLiteStateIsNotPostgresEnableAlways(t *testing.T) {
	t.Parallel()
	if TriggerNoEnableState == TriggerFiresAlways {
		t.Fatal("SQLite's 'no per-trigger state' and PostgreSQL's ENABLE ALWAYS share a value: " +
			"any policy about the chosen ALWAYS state would silently govern an engine that " +
			"has no such state")
	}
	// Both fire — the distinction is about provenance, not about behavior.
	if !TriggerNoEnableState.Fires() || !TriggerFiresAlways.Fires() {
		t.Fatal("both states fire; only their provenance differs")
	}
	// And the raw value must not collide with any PostgreSQL tgenabled character,
	// which is what the Postgres dialect scans straight into this type.
	for _, pg := range []TriggerEnableState{
		TriggerFiresOrigin, TriggerNeverFires, TriggerFiresReplicaOnly, TriggerFiresAlways,
	} {
		if TriggerNoEnableState == pg {
			t.Fatalf("TriggerNoEnableState collides with the PostgreSQL value %q", pg)
		}
	}
}
