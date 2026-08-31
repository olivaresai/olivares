// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package migrate is the engine's minimal schema-migration runner. It applies an
// ordered list of migrations to an *sql.DB, each in its own transaction,
// recording applied versions in a tracking table so re-runs are idempotent.
//
// It deliberately replaces a third-party migration library. golang-migrate —
// the obvious choice — pulls a second major version of the Postgres driver
// (jackc/pgx v4) plus lib/pq alongside the engine's pgx v5, which contradicts
// the minimal-dependency posture of a security product (docs/SECURITY-HARDENING.md); and its
// file-source model cannot apply the per-module DDL the engine GENERATES from
// registered descriptors. This runner applies hand-authored core statements and
// generated module statements through one identical path, over the engine's
// existing connection, with no added dependency and a surface small enough to
// audit. The migration STATEMENTS are assembled by the store from the dialect
// and the entity descriptors (see internal/store/sqlstore), so the schema has a
// single source of truth and cannot drift from the descriptors.
package migrate
