// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package pgpin opens database/sql Postgres pools whose every physical connection
// has search_path pinned to a caller-fixed schema before ANYTHING else runs on it.
//
// It is the shared home of the double-pin pattern proven in
// core/internal/store/sqlstore (openPGPinnedToEngineSchema / pinBeforeValidate /
// pinSearchPathOnPgConn): the first consumer is core/api/ratelimit/pgstore, which
// sits outside that package and must not import it. UNIFICATION DEBT, named on
// purpose: once the C4 lane releases the core/internal/store frontier, sqlstore
// imports THIS package and its private twins are deleted — two implementations do
// not live on indefinitely (an internal design note (not shipped)
// pinning-design.md §B).
//
// Why two hooks, restated from the proven original:
//
//   - ValidateConnect FIRST. pgx runs ValidateConnect inside ConnectConfig
//     (pgconn/pgconn.go:514) and stdlib calls AfterConnect only afterwards
//     (stdlib/sql.go:271,275). A DSN carrying target_session_attrs installs a
//     validator that executes `select pg_is_in_recovery()` UNQUALIFIED, so on a
//     database whose owner set a hostile search_path that resolves to a function
//     the owner defined — before anything this process controls has executed.
//   - AfterConnect AS WELL. It is the pool-admission check, covers DSNs with no
//     validator at all, and re-reads the value pgx itself will use.
//
// The pin is per physical connection via pg_catalog.set_config — never a
// RuntimeParams startup parameter, which a connection pooler is entitled to
// reject at the dial (or, with ignore_startup_parameters, silently DROP). The
// set_config call is schema-qualified because it is the one statement that runs
// while search_path is still whatever the connection inherited. The read-back is
// fail-closed: an unverifiable pin is worse than none.
package pgpin

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/olivaresai/olivares/core/store"
)

// safeIdent is the repo's ONE identifier pattern (core/store SafeIdentPattern):
// Postgres cannot bind an identifier position, so this guard is what makes the
// single interpolation below safe.
var safeIdent = regexp.MustCompile(store.SafeIdentPattern)

// Open opens a pool whose every physical connection is pinned to path before it
// is used. path must be a plain lower-case identifier (e.g. the engine schema
// constant); anything else is refused before a single connection is dialed.
// maxConns <= 0 leaves the pool bound only by database/sql defaults.
//
// database/sql opens lazily: Open returning nil error does NOT mean a pin has
// executed yet. A caller whose contract is fail-closed admission at boot must
// force a round trip before it declares success — pgstore pings and then runs a
// privilege admission, either of which establishes a connection. Stated
// precisely because it was measured: removing the ping alone no longer turns
// that suite red, since the admission query also forces one. The obligation on
// a caller is "force a live round trip and close on error", not "call
// PingContext" specifically.
func Open(dsn, path string, maxConns int) (*sql.DB, error) {
	if !safeIdent.MatchString(path) {
		return nil, fmt.Errorf("pgpin: refusing to pin a search_path that is not a plain identifier: %q", path)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgpin: parse postgres dsn: %w", err)
	}
	return OpenConfig(cfg, path, maxConns)
}

// OpenConfig is Open for a caller that already holds a parsed config — including
// one carrying its own ValidateConnect, which the pin wraps rather than replaces.
// Exported (within core/internal) so the pin's second hook can be tested against
// a config that moves search_path AFTER the pre-pin: that property has no other
// entry point, and the alternative — a test-only backdoor — would be a second
// way into the same code with none of the guarantees.
// It adds NOTHING to cfg.RuntimeParams: those become startup-packet parameters,
// and a connection pooler is entitled to reject any it does not track.
func OpenConfig(cfg *pgx.ConnConfig, path string, maxConns int) (*sql.DB, error) {
	if cfg == nil {
		return nil, fmt.Errorf("pgpin: nil connection config")
	}
	if !safeIdent.MatchString(path) {
		return nil, fmt.Errorf("pgpin: refusing to pin a search_path that is not a plain identifier: %q", path)
	}
	// stdlib.OpenDB rather than RegisterConnConfig + sql.Open: the register form
	// stashes the config — password included — in a process-global map. This form
	// owns nothing global.
	db := stdlib.OpenDB(*pinBeforeValidate(cfg, path), stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		return pinOnConn(ctx, conn, path)
	}))
	if maxConns > 0 {
		db.SetMaxOpenConns(maxConns)
	}
	return db, nil
}

// pinBeforeValidate returns a copy of cfg whose ValidateConnect installs the pin
// FIRST and only then delegates to whatever validator the DSN asked for.
func pinBeforeValidate(cfg *pgx.ConnConfig, path string) *pgx.ConnConfig {
	c := cfg.Copy()
	prior := c.ValidateConnect
	c.ValidateConnect = func(ctx context.Context, pgConn *pgconn.PgConn) error {
		if err := pinOnPgConn(ctx, pgConn, path); err != nil {
			return err
		}
		if prior != nil {
			return prior(ctx, pgConn)
		}
		return nil
	}
	return c
}

// pinOnPgConn installs the pin at the raw-connection stage, where no *pgx.Conn
// exists yet. path was validated against safeIdent in Open and is re-checked here
// so the guard holds even for a future caller of this helper alone; the guarded
// interpolation keeps the statement one simple-protocol round trip (ExecParams
// exists at this stage too — the choice is deliberate and narrow, matching the
// proven original).
func pinOnPgConn(ctx context.Context, c *pgconn.PgConn, path string) error {
	if !safeIdent.MatchString(path) {
		return fmt.Errorf("pgpin: refusing to pin a search_path that is not a plain identifier: %q", path)
	}
	res, err := c.Exec(ctx, "SELECT pg_catalog.set_config('search_path', '"+path+"', false)").ReadAll()
	if err != nil {
		return fmt.Errorf("pgpin: pin search_path before connection validation: %w", err)
	}
	if len(res) == 0 || len(res[0].Rows) == 0 || len(res[0].Rows[0]) == 0 {
		return fmt.Errorf("pgpin: pinning search_path to %q returned no value to verify", path)
	}
	if got := string(res[0].Rows[0][0]); got != path {
		return fmt.Errorf("pgpin: search_path reads back as %q after pinning it to %q", got, path)
	}
	return nil
}

// pinOnConn is the AfterConnect half: same statement, bound as a parameter (a
// *pgx.Conn can bind here), same fail-closed read-back.
func pinOnConn(ctx context.Context, conn *pgx.Conn, path string) error {
	var got string
	if err := conn.QueryRow(ctx, "SELECT pg_catalog.set_config('search_path', $1, false)", path).Scan(&got); err != nil {
		return fmt.Errorf("pgpin: pin search_path to %q: %w", path, err)
	}
	if got != path {
		return fmt.Errorf("pgpin: search_path reads back as %q after pinning it to %q; something between this process and the server is rewriting it", got, path)
	}
	return nil
}
