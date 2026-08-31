// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package enginetest is the PUBLIC seam for Postgres test isolation, so suites
// outside the core module (the `modules` module's cross-backend tests) can
// provision a private database per test.
//
// It re-exports core/internal/pgtest exactly the way core/engine re-exports the
// store and provisioning implementation: it adds no behavior, only visibility.
// See the pgtest package doc for WHY a shared database was unsound.
package enginetest

import (
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/internal/pgtest"
)

// Environment variables the Postgres-backed suites are gated on.
const (
	// EnvSuperuserDSN is the maintenance DSN isolated databases are provisioned from.
	EnvSuperuserDSN = pgtest.EnvSuperuserDSN
	// EnvAppDSN is the legacy shared application DSN; setting it WITHOUT the
	// superuser DSN is a hard error rather than a silent skip.
	EnvAppDSN = pgtest.EnvAppDSN
)

// DSNs are the connection strings for one isolated database.
type DSNs struct {
	// Database is the provisioned database name.
	Database string
	// App is the runtime-traffic role: NOSUPERUSER NOBYPASSRLS.
	App string
	// Owner owns the database and runs DDL; equal to App in single-role mode.
	Owner string
	// Admin is the cross-tenant read role: NOSUPERUSER BYPASSRLS.
	Admin string
	// Superuser is the maintenance role pointed at THIS database.
	Superuser string
}

// String redacts. Every DSN field carries a password, so the default %v/%+v would
// print four of them, and a CI failure log is posted verbatim as a PR comment.
func (d DSNs) String() string {
	return fmt.Sprintf("enginetest.DSNs{Database: %q, App/Owner/Admin/Superuser: <redacted DSNs>}", d.Database)
}

// GoString redacts for %#v as well.
func (d DSNs) GoString() string { return d.String() }

// PostgresAvailable reports whether a Postgres server is available to provision
// isolated databases from, so a cross-backend table can add the Postgres leg
// only when it can actually run. It fails tb when the legacy shared DSN is set
// without the superuser DSN, so a misconfiguration can never silently delete the
// Postgres leg.
func PostgresAvailable(tb testing.TB) bool {
	tb.Helper()
	return pgtest.Available(tb)
}

// IsolatedPostgres provisions a private database for tb whose APPLICATION role
// owns it — the topology CI provisioned for the shared database, so this is the
// drop-in replacement for reading OLIVARES_TEST_POSTGRES_DSN.
//
// The DATABASE and the per-test admin role are dropped in t.Cleanup. The shared
// application role is NOT dropped: its name is compiled into the schema's
// append-only REVOKE, so it is reused rather than minted, and dropping it would
// break the tests running alongside. It skips tb when no Postgres is configured.
func IsolatedPostgres(tb testing.TB) DSNs {
	tb.Helper()
	return convert(pgtest.Isolate(tb, engine.ProvisionPostgres, pgtest.SingleRole))
}

// IsolatedPostgresSplitOwner is IsolatedPostgres with a SEPARATE least-privilege
// owner role that owns the database and runs DDL, the app role holding only DML
// — the store.Config.OwnerDSN topology.
func IsolatedPostgresSplitOwner(tb testing.TB) DSNs {
	tb.Helper()
	return convert(pgtest.Isolate(tb, engine.ProvisionPostgres, pgtest.SplitOwner))
}

func convert(d pgtest.DSNs) DSNs {
	return DSNs{Database: d.Database, App: d.App, Owner: d.Owner, Admin: d.Admin, Superuser: d.Superuser}
}
