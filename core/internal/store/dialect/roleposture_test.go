// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"context"
	"testing"
)

func TestRolePostureRLSUnsafe(t *testing.T) {
	cases := []struct {
		name   string
		p      RolePosture
		unsafe bool
		why    string
	}{
		{"plain", RolePosture{Role: "olivares_app"}, false, "RLS-safe"},
		{"superuser", RolePosture{Role: "postgres", Superuser: true}, true, "a SUPERUSER"},
		{"bypassrls", RolePosture{Role: "admin", BypassRLS: true}, true, "a BYPASSRLS role"},
		{"both", RolePosture{Role: "root", Superuser: true, BypassRLS: true}, true, "a SUPERUSER with BYPASSRLS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.RLSUnsafe(); got != c.unsafe {
				t.Fatalf("RLSUnsafe()=%v want %v", got, c.unsafe)
			}
			if got := c.p.Why(); got != c.why {
				t.Fatalf("Why()=%q want %q", got, c.why)
			}
		})
	}
}

// TestRolePostureTriggersDisabled pins which session_replication_role values make
// ordinary triggers inert — every trigger-based guard in this schema is ordinary.
//
// PostgreSQL fires a simply-enabled trigger when the replication role is 'origin'
// (the default) OR 'local'; only 'replica' suppresses it (ALTER TABLE, "The trigger
// firing mechanism is also affected by session_replication_role"). Treating 'local'
// as unsafe is therefore an availability bug, not extra safety: it refuses to open a
// database whose triggers fire perfectly well.
func TestRolePostureTriggersDisabled(t *testing.T) {
	for _, c := range []struct {
		role     string
		disabled bool
		why      string
	}{
		// "" is a DELIBERATE non-Postgres sentinel, not a hole: it is unreachable
		// from either dialect (Postgres scans current_setting into the field, SQLite
		// returns "origin" explicitly), and it keeps a dialect that has no such
		// concept from being read as unsafe. Every non-empty value outside the
		// documented enum IS refused, so a future fourth mode fails closed.
		{"", false, "a dialect with no replication concept must not be read as unsafe"},
		{"origin", false, "the default; ordinary triggers fire"},
		{"local", false, "ordinary triggers fire exactly as in origin"},
		{"replica", true, "ordinary triggers are SKIPPED — every guard goes inert"},
		{"some_future_mode", true, "outside the documented enum: fail closed, do not guess"},
	} {
		t.Run(c.role, func(t *testing.T) {
			p := RolePosture{Role: "olivares_app", ReplicationRole: c.role}
			if got := p.TriggersDisabled(); got != c.disabled {
				t.Fatalf("TriggersDisabled() for session_replication_role=%q = %v, want %v (%s)",
					c.role, got, c.disabled, c.why)
			}
		})
	}
}

func TestSQLiteConnRolePostureIsSafe(t *testing.T) {
	p, err := sqliteDialect{}.ConnRolePosture(context.Background(), nil)
	if err != nil {
		t.Fatalf("ConnRolePosture: %v", err)
	}
	if p.RLSUnsafe() {
		t.Fatalf("SQLite must report an RLS-safe posture, got %+v", p)
	}
}
