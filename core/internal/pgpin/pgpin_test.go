// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgpin_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/internal/pgpin"
)

// Each test here pins ONE half of the contract, because the two hooks fail in
// different ways and a single end-to-end assertion stays green when either is
// removed — measured: deleting the ValidateConnect pin, and deleting the
// AfterConnect pin, both left the consumer's suite passing.

func dsns(t *testing.T) enginetest.DSNs {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("set %s to run the Postgres leg", enginetest.EnvSuperuserDSN)
	}
	return enginetest.IsolatedPostgres(t)
}

func exec(t *testing.T, dsn, stmt string, args ...any) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.ExecContext(context.Background(), stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// TestPinRunsBeforeConnectionValidation is the leg that only the FIRST hook can
// pass. pgx runs ValidateConnect inside ConnectConfig, before stdlib ever calls
// AfterConnect, and a DSN carrying target_session_attrs makes it execute
// `pg_is_in_recovery()` UNQUALIFIED at that point. With a hostile search_path,
// that resolves to a function the database owner defined — running with the
// connecting role's privileges before anything this package controls has run.
//
// The shadow RAISES rather than returning a value, following the proven original
// (sqlstore's TestTrustedPathIsInstalledBeforeConnectionValidation): the property
// under test is "did the shadow execute at all", and a raising shadow answers
// that whatever the driver does with the boolean.
func TestPinRunsBeforeConnectionValidation(t *testing.T) {
	d := dsns(t)
	// The shadow is planted by the APP role itself, which is the real threat model:
	// in the default single-role topology that role owns the database. Planting it
	// as superuser would leave the app without USAGE on the schema, so the shadow
	// would be invisible to it and the whole test would prove nothing — measured.
	exec(t, d.App, `CREATE SCHEMA IF NOT EXISTS evil`)
	exec(t, d.App, `CREATE FUNCTION evil.pg_is_in_recovery() RETURNS pg_catalog.bool LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'shadow pg_is_in_recovery() executed'; END $fn$`)
	// A lying set_config shadow as well, so this leg pins the PRE-pin's own
	// qualification too: unqualified, the pre-pin would resolve to this, report
	// success, leave the path hostile, and pgx's validator would then hit the
	// raising shadow above. Without it the test only proved the hook ORDER, and
	// dropping the prefix from the pre-pin alone stayed green (measured).
	exec(t, d.App, `CREATE FUNCTION evil.set_config(pg_catalog.text, pg_catalog.text, pg_catalog.bool)
RETURNS pg_catalog.text LANGUAGE sql AS $$ SELECT $2 $$`)
	exec(t, d.Superuser, fmt.Sprintf(`ALTER DATABASE %q SET search_path = evil, pg_catalog, public`, d.Database))

	dsn := d.App
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "target_session_attrs=primary"

	// Control: unpinned, the shadow answers "standby" and pgx refuses the dial.
	// If this does NOT fail, the fixture is not hostile and the assertion below
	// would be vacuous.
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	if err := raw.PingContext(context.Background()); err == nil {
		t.Fatal("fixture is not hostile: an UNPINNED connection with target_session_attrs=primary should have been refused by the shadow pg_is_in_recovery()")
	}

	db, err := pgpin.Open(dsn, "public", 1)
	if err != nil {
		t.Fatalf("pgpin.Open: %v", err)
	}
	defer db.Close() //nolint:errcheck
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("the pin must run BEFORE pgx's own validator, so target_session_attrs resolves pg_catalog.pg_is_in_recovery: %v", err)
	}
}

// TestAfterConnectPinRepairsAValidatorThatMovesThePath is the leg only the SECOND
// hook can pass: a validator supplied by the caller runs AFTER the pre-pin and
// can move search_path again (deliberately here; in the wild, any validator that
// runs SET). AfterConnect re-pins and re-reads, so the connection handed to the
// pool is on the engine schema regardless.
func TestAfterConnectPinRepairsAValidatorThatMovesThePath(t *testing.T) {
	d := dsns(t)
	exec(t, d.App, `CREATE SCHEMA IF NOT EXISTS evil`)
	// The prior validator moves the path to `evil`, so an UNQUALIFIED set_config
	// in AfterConnect would resolve to this lying shadow and report success while
	// the connection stayed on the hostile path. That is what makes this leg pin
	// AfterConnect's own qualification, independently of the pre-pin's.
	exec(t, d.App, `CREATE FUNCTION evil.set_config(pg_catalog.text, pg_catalog.text, pg_catalog.bool)
RETURNS pg_catalog.text LANGUAGE sql AS $$ SELECT $2 $$`)

	cfg, err := pgx.ParseConfig(d.App)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// A prior validator that sabotages the path after the pre-pin ran.
	moved := false
	cfg.ValidateConnect = func(ctx context.Context, c *pgconn.PgConn) error {
		moved = true
		// `evil, pg_catalog`, never bare `evil`: PostgreSQL implicitly searches
		// pg_catalog FIRST unless it is named explicitly, so a bare `evil` would
		// leave the real set_config winning and this leg would prove nothing —
		// measured, the mutation stayed green until pg_catalog was listed after.
		_, err := c.Exec(ctx, "SELECT pg_catalog.set_config('search_path', 'evil, pg_catalog', false)").ReadAll()
		return err
	}
	db, err := pgpin.OpenConfig(cfg, "public", 1)
	if err != nil {
		t.Fatalf("pgpin.OpenConfig: %v", err)
	}
	defer db.Close() //nolint:errcheck

	var got string
	if err := db.QueryRowContext(context.Background(), `SELECT pg_catalog.current_setting('search_path')`).Scan(&got); err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	if !moved {
		t.Fatal("the prior validator never ran; this test would prove nothing")
	}
	if got != "public" {
		t.Errorf("search_path = %q, want %q: AfterConnect must re-pin after a validator moved it", got, "public")
	}
}

// TestOpenShipsNoStartupParameters is the pooler ratchet, mirroring
// sqlstore's TestEnginePoolsShipNoStartupParameters. pgx copies RuntimeParams
// verbatim into the StartupMessage, and a pooler rejects any parameter outside
// its fixed allowlist — PgBouncer answers `FATAL: unsupported startup parameter:
// search_path` — so a pin shipped that way stops every pooled deployment at the
// dial. Worse, the workaround operators reach for (ignore_startup_parameters)
// makes the pooler DROP it, removing the pin instead of fixing it.
func TestOpenShipsNoStartupParameters(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://svc:pw@127.0.0.1:5432/olivares?sslmode=disable"
	base, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	got, err := pgpin.ConnConfigForTest(dsn, "public")
	if err != nil {
		t.Fatalf("ConnConfigForTest: %v", err)
	}
	if v, ok := got.RuntimeParams["search_path"]; ok {
		t.Errorf("search_path is shipped as a startup parameter (%q); a pooled deployment is refused at the dial", v)
	}
	for k, v := range got.RuntimeParams {
		if bv, ok := base.RuntimeParams[k]; !ok || bv != v {
			t.Errorf("startup parameter %q=%q was added by pgpin; the pooler allowlist is not ours to widen", k, v)
		}
	}
}

// TestOpenRefusesANonIdentifierPath keeps the interpolation guard honest: the
// path reaches a simple-protocol statement by concatenation, so anything that is
// not a plain identifier must be refused before a connection is dialed.
func TestOpenRefusesANonIdentifierPath(t *testing.T) {
	t.Parallel()
	// TEST-NET-1 (192.0.2.1), the convention of dbsetup_test.go and
	// migrationobserver_pg_test.go: this literal must never resolve to a live
	// server — the refusal under test has to happen BEFORE any dial, and with
	// the CI port ephemeral a 127.0.0.1:5432 literal could silently become a
	// neighboring job's cluster on a shared runner host.
	for _, bad := range []string{"public, evil", `pub"lic`, "Public", "", "pg_catalog;--"} {
		if _, err := pgpin.Open("postgres://svc:pw@192.0.2.1:5432/db?connect_timeout=1&sslmode=disable", bad, 1); err == nil {
			t.Errorf("path %q was accepted; only a plain identifier may be interpolated", bad)
		}
	}
}

// TestPinSurvivesAShadowedSetConfig is the one leg that qualifying set_config
// exists for. The pin statement runs while search_path is still whatever the
// connection inherited — it is the ONE place in this package where an
// unqualified name could be resolved by an attacker-controlled schema — and the
// read-back is no defense against a shadow that RETURNS the expected value
// without changing the GUC.
//
// The shadow here has the exact signature (text, text, boolean) and echoes its
// second argument while leaving the real setting untouched. Mutation: drop the
// pg_catalog. prefix from either pin call and this goes red, because the pool's
// connections keep resolving on the hostile path while reporting success.
// Ported from the twin's precedent (sqlstore appendonly_acl_pg_test.go), because
// the twin's tests do not protect a reimplementation living in another package.
func TestPinSurvivesAShadowedSetConfig(t *testing.T) {
	d := dsns(t)
	// Planted by the APP role: in the default single-role topology it owns the
	// database, which is exactly the threat this closes.
	exec(t, d.App, `CREATE SCHEMA IF NOT EXISTS evil`)
	exec(t, d.App, `CREATE FUNCTION evil.set_config(pg_catalog.text, pg_catalog.text, pg_catalog.bool)
RETURNS pg_catalog.text LANGUAGE sql AS $$ SELECT $2 $$`)
	exec(t, d.Superuser, fmt.Sprintf(`ALTER DATABASE %q SET search_path = evil, pg_catalog, public`, d.Database))

	// Precondition: the shadow really wins an unqualified call and really lies.
	raw, err := sql.Open("pgx", d.App)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	defer conn.Close()
	var echoed, actual string
	if err := conn.QueryRowContext(context.Background(), `SELECT set_config('search_path', 'public', false)`).Scan(&echoed); err != nil {
		t.Fatalf("call unqualified set_config: %v", err)
	}
	if err := conn.QueryRowContext(context.Background(), `SELECT pg_catalog.current_setting('search_path')`).Scan(&actual); err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	if echoed != "public" || actual == "public" {
		t.Fatalf("fixture is not hostile: the shadow returned %q and the real setting is %q — an unqualified set_config must appear to succeed while changing nothing", echoed, actual)
	}

	db, err := pgpin.Open(d.App, "public", 1)
	if err != nil {
		t.Fatalf("pgpin.Open: %v", err)
	}
	defer db.Close() //nolint:errcheck
	var got string
	if err := db.QueryRowContext(context.Background(), `SELECT pg_catalog.current_setting('search_path')`).Scan(&got); err != nil {
		t.Fatalf("read pinned search_path: %v", err)
	}
	if got != "public" {
		t.Errorf("search_path = %q, want %q: the pin statement must call pg_catalog.set_config, or a same-signature shadow reports success while every later name resolves on the hostile path", got, "public")
	}
}
