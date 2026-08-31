// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine

import (
	"context"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// The public database-onboarding seam, re-exporting the internal implementation so
// the `olivares` binary (a separate module that may not import core/internal) can
// run `db check` / `db init`. Like Open, these add no behavior — only visibility.

// ProbeRole opens a transient connection for cfg (no migrations, no schema) and
// reports the connecting role's RLS posture, so `olivares db check` can tell an
// operator BEFORE booting whether the engine will accept the DSN. A connection or
// auth failure is reported in the returned RolePosture (Reachable=false), not as an
// error; the error is reserved for an unsupported engine.
func ProbeRole(ctx context.Context, cfg store.Config) (store.RolePosture, error) {
	return sqlstore.ProbeRolePosture(ctx, cfg)
}

// ProbeTargetOccupied opens a transient connection for cfg (no migrations, no
// schema) and reports whether the target database already holds relations of its
// own, so `dr restore` can tell a restore that BUILDS an estate from one that
// REPLACES one. An unreachable or unreadable target is an error, never false: the
// caller must fail closed on "I could not look" rather than treat it as "empty".
func ProbeTargetOccupied(ctx context.Context, cfg store.Config) (bool, error) {
	return sqlstore.ProbeTargetOccupied(ctx, cfg)
}

// RenderProvisionSQL renders the `db init` provisioning statements for display
// (`--print-sql`) without a database connection; password literals are redacted.
func RenderProvisionSQL(spec store.PgProvisionSpec) ([]store.PgProvisionStep, error) {
	return sqlstore.RenderProvisionSQL(spec)
}

// ProvisionPostgres applies spec idempotently against superuserDSN (a superuser /
// maintenance DSN). With execute=false it only renders the steps. On execute it
// creates/updates the least-privilege roles, the database and the grants, then
// reconnects as each provisioned role to verify the engine will accept it.
func ProvisionPostgres(ctx context.Context, superuserDSN string, spec store.PgProvisionSpec, execute bool) (store.PgProvisionResult, error) {
	return sqlstore.ProvisionPostgres(ctx, superuserDSN, spec, execute)
}

// MigrationStatus opens a transient, read-only connection for cfg and returns the
// recorded schema-migration rows across the core and per-module tracking tables, so
// `olivares migrate status` can show what a database carries — which versions, in
// which expand-contract phase, any reverted — without a SQL client. It applies no
// migration and writes nothing.
func MigrationStatus(ctx context.Context, cfg store.Config) ([]store.MigrationRecord, error) {
	return sqlstore.MigrationStatus(ctx, cfg)
}
