// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

// MigrationRecord is one row of an applied schema-migration tracking table, read by
// `olivares migrate status`. It lives in the PUBLIC store package — not the internal
// sqlstore — because the `olivares` binary is a SEPARATE module that may not import
// core/internal: the CLI consumes these inert shapes, the internal implementation
// produces them, and core/engine re-exports the reader (mirroring RolePosture and
// the db-onboarding types). It is read-only introspection: the reader opens a
// transient connection and never writes, migrates or reverts.
type MigrationRecord struct {
	// Table is the tracking table the row came from: "schema_migrations_core" for the
	// engine's own schema, or "schema_migrations_mod_<namespace>" for a module's file
	// migrations.
	Table string
	// Version is the migration's monotonically increasing version within Table.
	Version int
	// Name is the human label recorded with the migration.
	Name string
	// Phase is "expand" (additive, online-safe) or "contract" (the destructive
	// cleanup that completes a parallel-change and ships a release AFTER its expand).
	// It is the field that tells an operator whether rolling the BINARY back across
	// this migration is safe — a contract is not undone by redeploying the prior
	// binary. Empty for a tracking table created before the phase column existed.
	Phase string
	// AppliedAt is the RFC3339 timestamp the migration was recorded (bookkeeping,
	// never part of the audit hash-chain).
	AppliedAt string
	// Reverted is true when the migration was rolled back via migrate.Revert — the
	// row is kept (not deleted), so the history is preserved.
	Reverted bool
}
