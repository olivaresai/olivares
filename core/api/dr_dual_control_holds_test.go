// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"testing"
	"time"
)

// TestDualControlHoldsForRefusesTheIDENTITIESITCANNOTTELLAPART is the external
// contrast's C-01 and C-03, and both are the same mistake: an elapsed disarm that
// opens for a caller the estate cannot ATTRIBUTE.
//
//   - C-01, the bypass. The person comparison is over a stable user, and a
//     standalone superadmin API token has none (model.APIToken.UserID zero). So the
//     admin who disarmed had only to come back through a token of theirs: person ""
//     matched no DisarmBy, the gate read off, and the job started. That is
//     lesson — one human holds a session AND their own tokens — arriving at the
//     disarm instead of at the approval.
//   - C-03, the corrupt estate. A stored record with the gate armed, an ELAPSED
//     instant and NO requester is a disarm with no provenance. There is nobody to
//     hold it against, so it used to open for everybody after a restart.
//
// Both fail CLOSED, and neither becomes a lockout: an unattributable caller is
// refused by name (a nameable admin still restores), and a provenance-less disarm
// leaves the gate ARMED and replaceable by a fresh request — exactly what
// TestDRDualControlCorruptDisarmInstantFailsClosedWithoutLockingOut already
// requires of an unreadable instant.
func TestDualControlHoldsForRefusesTheIDENTITIESITCANNOTTELLAPART(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour).Format(time.RFC3339)

	t.Run("C-01 a caller with NO person cannot consume somebody's disarm", func(t *testing.T) {
		d := drSchedule{RequireDualControl: true, DisarmAt: past, DisarmBy: "alice"}
		if !d.dualControlHoldsFor("", now) {
			t.Fatal("BYPASS: a credential with no person behind it walked through an elapsed disarm")
		}
		if !d.dualControlHoldsFor("alice", now) {
			t.Fatal("the disarm freed its own requester")
		}
		if d.dualControlHoldsFor("bob", now) {
			t.Fatal("LOCKOUT: an admin who disarmed nothing was held")
		}
	})

	t.Run("C-03 a disarm with NO provenance is not a disarm", func(t *testing.T) {
		d := drSchedule{RequireDualControl: true, DisarmAt: past}
		if !d.dualControlArmed(now) {
			t.Fatal("an elapsed disarm that names nobody opened the gate for the estate")
		}
		if !d.dualControlHoldsFor("bob", now) {
			t.Fatal("an elapsed disarm that names nobody opened the gate for a person")
		}
		// …and it must still be replaceable, or fail-closed becomes fail-shut.
		next := d
		if got := applyDualControlRequest(&next, boolPtr(false), now, "bob"); got != "disarm_scheduled" {
			t.Fatalf("LOCKOUT: a provenance-less record could not be disarmed at all: %q", got)
		}
		if next.DisarmBy != "bob" {
			t.Fatalf("the replacement disarm did not record its requester: %+v", next)
		}
	})

	t.Run("a gate that was never armed still needs no person", func(t *testing.T) {
		// The rule must not leak into estates that never opted in: with no disarm on
		// record, an anonymous system token restores exactly as it did before.
		d := drSchedule{}
		if d.dualControlHoldsFor("", now) {
			t.Fatal("an estate that never armed the gate started holding restores")
		}
	})
}

func boolPtr(b bool) *bool { return &b }
