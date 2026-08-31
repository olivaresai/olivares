// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Hostile name-resolution legs for the shared rate-limit store. Each test names
// ONE invariant so a mutation reports which layer broke, rather than going red
// somewhere generic: a single end-to-end assertion can stay green through a
// missing pin (because everything is qualified) or through a missing
// qualification (because everything is pinned), which is exactly how this class
// of defect survives a review.
//
// The threat model, measured in PostgreSQL 15.18 by the X3 work:
//   - Pinning search_path does NOT close overload capture: resolution gathers
//     candidates from every visible schema and prefers an exact match over the
//     builtin's variadic, regardless of order.
//   - Resolution covers TYPES: a `public.text` DOMAIN is selected by an
//     unqualified `::text` cast and its CHECK runs with the connecting role's
//     privileges.
//   - AfterConnect is TOO LATE on its own: target_session_attrs makes pgx run
//     `pg_is_in_recovery()` unqualified inside ValidateConnect.
package pgstore_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/api/ratelimit/pgstore"
	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// exec runs SQL on db and fails the test with the statement on error.
func exec(t *testing.T, db *sql.DB, sqlText string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), sqlText, args...); err != nil {
		t.Fatalf("exec %q: %v", sqlText, err)
	}
}

// rawConn opens an UNPINNED pool on dsn — the attacker's vantage point, and the
// only way to observe what an unpinned connection would have resolved.
func rawConn(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// plantHostilePath sets a database-level search_path that puts an attacker-owned
// schema ahead of everything. In the default single-role topology the app role
// owns the database, so this is a move it can genuinely make — the premise the
// engine's own TestProvisioningRunsOnATrustedPath encodes.
func plantHostilePath(t *testing.T, db *sql.DB, database, schema string) {
	t.Helper()
	exec(t, db, `CREATE SCHEMA IF NOT EXISTS `+schema)
	exec(t, db, fmt.Sprintf(`ALTER DATABASE %q SET search_path = %s, pg_catalog, public`, database, schema))
}

// TestOpenPinsBothPoolsToEngineSchema is the two-pool leg: after Open, a
// connection handed out by the DML pool reports the engine schema even though
// the DATABASE default says otherwise, and the DDL pool must have resolved the
// same way (its objects landed in public, asserted by the sweep/gauge legs).
// Mutation: drop the pin from either pool and the corresponding half goes red.
func TestOpenPinsBothPoolsToEngineSchema(t *testing.T) {
	d := isolated(t)
	admin := rawConn(t, d.Superuser)
	plantHostilePath(t, admin, d.Database, "evil")

	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Take forces the DML pool to hand out a physical connection and run real SQL
	// on it; if the pin were absent the qualified statements would still work, so
	// the assertion below reads the session state itself.
	if _, _, err := st.Take(context.Background(), reqs("pin-probe", 10, 10, 10, 10)); err != nil {
		t.Fatalf("take: %v", err)
	}
	// Read the STORE'S OWN pool, never a separately-opened pinned one: measured,
	// a test that opened its own pgpin pool stayed green with the pin removed
	// from Open, because it proved pgpin works and never observed the product.
	got, err := st.SearchPathForTest(context.Background())
	if err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	if got != "public" {
		t.Errorf("pinned pool search_path = %q, want %q (the hostile DATABASE default leaked through)", got, "public")
	}
	// The unpinned control: prove the hostile default is really in force, so a
	// green result above cannot be an artifact of the fixture. It needs a FRESH
	// pool — a database-level setting is applied at connection establishment, so
	// `admin`, opened before the ALTER, still carries the old value.
	var raw string
	if err := rawConn(t, d.App).QueryRowContext(context.Background(), `SELECT pg_catalog.current_setting('search_path')`).Scan(&raw); err != nil {
		t.Fatalf("read raw search_path: %v", err)
	}
	if !strings.Contains(raw, "evil") {
		t.Fatalf("fixture is not hostile: raw search_path = %q", raw)
	}
}

// TestOpenFailsWhenDMLPoolFails pins the fail-closed boot admission: database/sql
// opens lazily, so without the PingContext in Open a bad DML DSN would surface at
// the first Take, where the limiter silently falls back to the per-node store and
// un-shares every quota — the opposite of what an operator who selected a shared
// store asked for. The DDL DSN is valid, so only the DML pool can fail.
// Honest note on the mutation: removing the ping ALONE no longer turns this red,
// because admitDML (added later) also forces a connection and keeps the
// fail-closed contract. The ping is kept for the diagnosis it gives an operator
// — "cannot connect" separated from "connected but unauthorized" — and this leg
// pins the CONTRACT (Open must not return a store that cannot work), not one
// particular statement.
func TestOpenFailsWhenDMLPoolFails(t *testing.T) {
	d := isolated(t)
	_, err := pgstore.Open(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/absent?sslmode=disable",
		pgstore.Options{DDLDSN: d.App})
	if err == nil {
		t.Fatal("Open must fail when the DML pool cannot connect; a lazily-opened pool would defer the failure to the first Take and fall back to per-node limits")
	}
}

// TestTakeResolvesRealObjectsUnderShadows is the integrated single-role attack:
// a shadow table, a shadow function of the EXACT canonical signature, and a
// shadow DOMAIN type, all in a schema the hostile search_path lists first. Take
// must still hit public — and the shadows must be provably reachable from an
// unpinned connection, otherwise the test proves nothing.
func TestTakeResolvesRealObjectsUnderShadows(t *testing.T) {
	d := isolated(t)
	admin := rawConn(t, d.Superuser)
	exec(t, admin, `CREATE SCHEMA IF NOT EXISTS evil`)
	// Shadow relation, shadow domain, and a shadow take() that would report every
	// request as admitted (the interesting lie: silently disabling admission).
	exec(t, admin, `CREATE TABLE evil.ratelimit_buckets (key text PRIMARY KEY, tokens double precision NOT NULL, last_take timestamptz NOT NULL)`)
	exec(t, admin, `CREATE DOMAIN evil.text AS pg_catalog.text`)
	exec(t, admin, `CREATE FUNCTION evil.olivares_ratelimit_take(p_keys pg_catalog.text[], p_rates pg_catalog.float8[], p_bursts pg_catalog.float8[])
RETURNS TABLE(allowed pg_catalog.bool, tokens pg_catalog.float8)
LANGUAGE sql AS $$ SELECT true, 999999::pg_catalog.float8 FROM pg_catalog.unnest(p_keys) $$`)
	plantHostilePath(t, admin, d.Database, "evil")

	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Burst 1: the second take must be DENIED. Against the shadow it would be
	// admitted with 999999 tokens.
	r := reqs("shadow-probe", 0, 1, 0, 1)
	if ok, states, err := st.Take(context.Background(), r); err != nil || !ok {
		t.Fatalf("first take: ok=%v err=%v states=%v", ok, err, states)
	}
	ok, states, err := st.Take(context.Background(), r)
	if err != nil {
		t.Fatalf("second take: %v", err)
	}
	if ok {
		t.Errorf("second take was ADMITTED: the shadow evil.olivares_ratelimit_take won resolution, so admission control is silently disabled (states=%v)", states)
	}
	// The rows must exist in public and NOT in the shadow table.
	var pub, evil int
	if err := admin.QueryRowContext(context.Background(), `SELECT pg_catalog.count(*) FROM public.ratelimit_buckets`).Scan(&pub); err != nil {
		t.Fatalf("count public: %v", err)
	}
	if err := admin.QueryRowContext(context.Background(), `SELECT pg_catalog.count(*) FROM evil.ratelimit_buckets`).Scan(&evil); err != nil {
		t.Fatalf("count evil: %v", err)
	}
	if pub == 0 || evil != 0 {
		t.Errorf("buckets landed in the wrong relation: public=%d evil=%d (want public>0, evil=0)", pub, evil)
	}
}

// TestFunctionBodyIgnoresCallerPath pins the invariant the function's own
// SET search_path cannot give: pg_temp is searched FIRST for relations and types
// even under a single trusted-schema path, so the body's table reference must be
// schema-qualified. The tripwire is a pg_temp relation created on the SAME
// connection the call then uses.
// Mutation: drop the qualification of the table inside the PL/pgSQL body and this
// goes red even with SET search_path = pg_catalog still in place.
func TestFunctionBodyIgnoresCallerPath(t *testing.T) {
	d := isolated(t)
	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// One dedicated connection: a temp table is session-scoped, so the call must
	// run on the very connection that owns it.
	raw := rawConn(t, d.App)
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(),
		`CREATE TEMP TABLE ratelimit_buckets (key pg_catalog.text PRIMARY KEY, tokens pg_catalog.float8 NOT NULL, last_take pg_catalog.timestamptz NOT NULL)`); err != nil {
		t.Fatalf("create temp tripwire: %v", err)
	}
	var allowed bool
	var tokens float64
	if err := conn.QueryRowContext(context.Background(),
		`SELECT allowed, tokens FROM public.olivares_ratelimit_take($1::pg_catalog.text[], $2::pg_catalog.float8[], $3::pg_catalog.float8[])`,
		[]string{"temp-probe"}, []float64{10}, []float64{10}).Scan(&allowed, &tokens); err != nil {
		t.Fatalf("call take on the tripwire connection: %v", err)
	}
	var temp, pub int
	if err := conn.QueryRowContext(context.Background(), `SELECT pg_catalog.count(*) FROM pg_temp.ratelimit_buckets`).Scan(&temp); err != nil {
		t.Fatalf("count temp: %v", err)
	}
	if err := conn.QueryRowContext(context.Background(), `SELECT pg_catalog.count(*) FROM public.ratelimit_buckets`).Scan(&pub); err != nil {
		t.Fatalf("count public: %v", err)
	}
	if temp != 0 || pub == 0 {
		t.Errorf("the function body wrote to the WRONG relation: pg_temp=%d public=%d (want pg_temp=0, public>0) — the body's table reference resolved on the caller's path", temp, pub)
	}
}

// TestOpenRefusesUnexpectedOverload pins the fail-closed inventory. Measured in
// 15.18: an overload carrying one extra DEFAULT argument survives
// CREATE OR REPLACE and makes even a fully-qualified, fully-cast call ambiguous
// ("function is not unique") — a denial of admission control. Open must refuse,
// name the object, and DROP NOTHING.
// Mutation: remove preflightTakeFunction and this goes green while the store is
// one call away from failing every request.
func TestOpenRefusesUnexpectedOverload(t *testing.T) {
	d := isolated(t)
	admin := rawConn(t, d.Superuser)
	exec(t, admin, `CREATE FUNCTION public.olivares_ratelimit_take(p_keys pg_catalog.text[], p_rates pg_catalog.float8[], p_bursts pg_catalog.float8[], p_extra pg_catalog.int4 DEFAULT 0)
RETURNS TABLE(allowed pg_catalog.bool, tokens pg_catalog.float8)
LANGUAGE sql AS $$ SELECT true, 0::pg_catalog.float8 $$`)

	_, err := pgstore.Open(context.Background(), d.App, pgstore.Options{})
	if err == nil {
		t.Fatal("Open must refuse to boot while an unexpected overload occupies the take function's name: CREATE OR REPLACE would leave it in place and every qualified call would fail as ambiguous")
	}
	if !strings.Contains(err.Error(), "olivares_ratelimit_take") {
		t.Errorf("the refusal must name the occupying object; got: %v", err)
	}
	// Nothing is dropped: this store never removes objects it did not create.
	var n int
	if err := admin.QueryRowContext(context.Background(), `
SELECT pg_catalog.count(*) FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace ns ON ns.oid = p.pronamespace
WHERE ns.nspname = 'public' AND p.proname = 'olivares_ratelimit_take' AND p.pronargdefaults > 0`).Scan(&n); err != nil {
		t.Fatalf("count overloads: %v", err)
	}
	if n != 1 {
		t.Errorf("the unexpected overload count = %d, want 1 (Open must diagnose, never drop)", n)
	}
}

// TestOpenRestoresPublicExecute pins the ACL half: CREATE OR REPLACE preserves a
// pre-existing identity's owner AND permissions, so a predecessor whose EXECUTE
// was revoked (hardened default privileges, or a manual REVOKE) would survive the
// replace and break every Take under the split topology. Open grants explicitly.
// Mutation: remove the GRANT and this goes red.
func TestOpenRestoresPublicExecute(t *testing.T) {
	d := isolated(t)
	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	admin := rawConn(t, d.Superuser)
	exec(t, admin, `REVOKE EXECUTE ON FUNCTION public.olivares_ratelimit_take(pg_catalog.text[], pg_catalog.float8[], pg_catalog.float8[]) FROM PUBLIC`)

	st2, err := pgstore.Open(context.Background(), d.App, pgstore.Options{})
	if err != nil {
		t.Fatalf("second open must restore PUBLIC EXECUTE rather than fail: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	if _, _, err := st2.Take(context.Background(), reqs("acl-probe", 10, 10, 10, 10)); err != nil {
		t.Fatalf("take after a revoked-then-restored EXECUTE: %v", err)
	}
}

// isolated provisions a private single-role database and returns every DSN, so a
// leg can act as the attacker (superuser) and as the product (app) at once.
func isolated(t *testing.T) enginetest.DSNs {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s not set; skipping the Postgres rate-limit store tests", enginetest.EnvSuperuserDSN)
	}
	return enginetest.IsolatedPostgres(t)
}

var _ = ratelimit.StoreRequest{}

// TestOpenRefusesEachMissingPrivilege is the split NEGATIVE leg, ORTHOGONAL by
// construction. Connecting is not the same as being able to work: default
// privileges only cover objects the owner creates AFTERWARDS, so a grant revoked
// on an existing table left Open succeeding and the first Take failing with
// 42501 — straight into the limiter's per-node fallback, silently un-sharing the
// quota the operator chose to share.
//
// One revocation per subtest, because revoking all four at once does not prove
// the admission covers each: replacing the DELETE check with `true` kept the
// whole suite green while the sweeper's own privilege went unchecked (measured).
//
// EXECUTE on the take function is deliberately NOT in this table, and the
// asymmetry is real rather than an oversight: Open RE-GRANTS it on every boot
// (ensureSchemaOn, because CREATE OR REPLACE preserves a predecessor's ACL), so
// a revoked EXECUTE is REPAIRED rather than refused — that path is pinned by
// TestOpenRestoresPublicExecute. Table privileges have no such repair: they come
// from the owner's DEFAULT PRIVILEGES at creation time and are never re-issued,
// which is exactly why a revocation on an existing table survived to become a
// 42501 at the first take. The admission's EXECUTE check remains as the
// fail-closed backstop for a grant that does not take effect.
func TestOpenRefusesEachMissingPrivilege(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s not set; skipping the Postgres rate-limit store tests", enginetest.EnvSuperuserDSN)
	}
	const fn = "public.olivares_ratelimit_take(pg_catalog.text[], pg_catalog.float8[], pg_catalog.float8[])"
	for _, tc := range []struct {
		name   string
		revoke string
		why    string
	}{
		{"schema USAGE", "REVOKE USAGE ON SCHEMA public FROM %s", "without USAGE nothing in the schema resolves"},
		{"table SELECT", "REVOKE SELECT ON public.ratelimit_buckets FROM %s", "the take function reads bucket state"},
		{"table INSERT", "REVOKE INSERT ON public.ratelimit_buckets FROM %s", "a first take inserts the bucket"},
		{"table UPDATE", "REVOKE UPDATE ON public.ratelimit_buckets FROM %s", "refill and decrement are UPDATEs"},
		{"table DELETE", "REVOKE DELETE ON public.ratelimit_buckets FROM %s", "the idle sweeper deletes; unlike the others this breaks the SWEEPER, not the takes — buckets would accumulate unbounded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := enginetest.IsolatedPostgresSplitOwner(t)
			st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{DDLDSN: d.Owner})
			if err != nil {
				t.Fatalf("clean provisioning must succeed: %v", err)
			}
			_ = st.Close()

			var appRole string
			if err := rawConn(t, d.App).QueryRowContext(context.Background(), `SELECT current_user`).Scan(&appRole); err != nil {
				t.Fatalf("read app role: %v", err)
			}
			owner := rawConn(t, d.Owner)
			// Revoke from PUBLIC as well as the role: a default grant to PUBLIC
			// would otherwise keep the privilege and make the subtest vacuous.
			exec(t, owner, strings.ReplaceAll(tc.revoke, "%s", "PUBLIC"))
			exec(t, owner, fmt.Sprintf(tc.revoke, appRole))

			if _, err := pgstore.Open(context.Background(), d.App, pgstore.Options{DDLDSN: d.Owner}); err == nil {
				t.Fatalf("Open must refuse without %s (%s): booting anyway fails at SQLSTATE 42501 on the path that privilege serves", tc.name, tc.why)
			}
		})
	}
}

// TestSplitOwnerTopologyEndToEnd exercises the topology DDLDSN exists for: the
// owner runs the DDL, the app role only ever does DML. It pins that the owner's
// DEFAULT PRIVILEGES really hand the app role its DML, that the explicit
// function GRANT makes Take reachable, and that the SWEEPER works as the app —
// the earlier version ran only Take and accepted any owner whose name merely
// lacked "app".
func TestSplitOwnerTopologyEndToEnd(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s not set; skipping the Postgres rate-limit store tests", enginetest.EnvSuperuserDSN)
	}
	d := enginetest.IsolatedPostgresSplitOwner(t)
	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{DDLDSN: d.Owner, IdleTTL: time.Hour})
	if err != nil {
		t.Fatalf("open split: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if ok, _, err := st.Take(context.Background(), reqs("split-probe", 10, 10, 10, 10)); err != nil || !ok {
		t.Fatalf("take under split topology: ok=%v err=%v", ok, err)
	}

	// The object must belong to the OWNER role exactly, compared against the role
	// that DSN authenticates as — not against a substring of its name.
	var ownerRole, tableOwner, fnOwner string
	if err := rawConn(t, d.Owner).QueryRowContext(context.Background(), `SELECT current_user`).Scan(&ownerRole); err != nil {
		t.Fatalf("read owner role: %v", err)
	}
	admin := rawConn(t, d.Superuser)
	if err := admin.QueryRowContext(context.Background(), `
SELECT pg_catalog.pg_get_userbyid(c.relowner) FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
WHERE ns.nspname = 'public' AND c.relname = 'ratelimit_buckets'`).Scan(&tableOwner); err != nil {
		t.Fatalf("read table owner: %v", err)
	}
	if err := admin.QueryRowContext(context.Background(), `
SELECT pg_catalog.pg_get_userbyid(p.proowner) FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace ns ON ns.oid = p.pronamespace
WHERE ns.nspname = 'public' AND p.proname = 'olivares_ratelimit_take'`).Scan(&fnOwner); err != nil {
		t.Fatalf("read function owner: %v", err)
	}
	if tableOwner != ownerRole || fnOwner != ownerRole {
		t.Errorf("objects must be owned by the DDL role %q; table=%q function=%q (the app role must own nothing under the split topology)", ownerRole, tableOwner, fnOwner)
	}

	// The sweeper runs as the APP role: its DELETE is a distinct privilege from
	// the take path's, and nothing else in this file exercises it under split.
	exec(t, admin, `UPDATE public.ratelimit_buckets SET last_take = pg_catalog.now() - pg_catalog.make_interval(secs => 7200)`)
	st.SweepOnceForTest()
	var left int
	if err := admin.QueryRowContext(context.Background(), `SELECT pg_catalog.count(*) FROM public.ratelimit_buckets`).Scan(&left); err != nil {
		t.Fatalf("count after sweep: %v", err)
	}
	if left != 0 {
		t.Errorf("the sweeper left %d idle buckets: under the split topology the app role must hold DELETE on the owner-created table", left)
	}
}

// TestOpenRefusesAnOverloadWithoutCreatingTheCanonical is the preflight's OWN
// property, separate from the post-flight's. With only the hostile overload
// planted, a refusal proves nothing by itself — the post-flight would refuse too,
// AFTER CREATE OR REPLACE had already created the canonical identity. What only
// the preflight guarantees is that nothing was written at all.
// Mutation: remove preflightTakeFunction alone and this goes red, while the
// older overload test stayed green because the post-flight caught the count.
func TestOpenRefusesAnOverloadWithoutCreatingTheCanonical(t *testing.T) {
	d := isolated(t)
	admin := rawConn(t, d.Superuser)
	exec(t, admin, `CREATE FUNCTION public.olivares_ratelimit_take(p_keys pg_catalog.text[], p_rates pg_catalog.float8[], p_bursts pg_catalog.float8[], p_extra pg_catalog.int4 DEFAULT 0)
RETURNS TABLE(allowed pg_catalog.bool, tokens pg_catalog.float8)
LANGUAGE sql AS $$ SELECT true, 0::pg_catalog.float8 $$`)

	if _, err := pgstore.Open(context.Background(), d.App, pgstore.Options{}); err == nil {
		t.Fatal("Open must refuse while an unexpected overload occupies the name")
	}
	var canonical int
	if err := admin.QueryRowContext(context.Background(), `
SELECT pg_catalog.count(*) FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace ns ON ns.oid = p.pronamespace
WHERE ns.nspname = 'public' AND p.proname = 'olivares_ratelimit_take'
  AND p.pronargs = 3 AND p.pronargdefaults = 0`).Scan(&canonical); err != nil {
		t.Fatalf("count canonical: %v", err)
	}
	if canonical != 0 {
		t.Errorf("the canonical identity was CREATED before the refusal (%d found): the pre-flight must reject before touching an occupied namespace, so a rejected boot leaves the database exactly as it found it", canonical)
	}
}

// TestSweepAndGaugeResolveRealObjectsUnderShadows extends the integrated attack
// to the two SQL paths Take does not exercise. They are separate statements on
// separate code paths, so a qualification lost in either is invisible to the
// Take assertions.
func TestSweepAndGaugeResolveRealObjectsUnderShadows(t *testing.T) {
	d := isolated(t)
	admin := rawConn(t, d.Superuser)
	exec(t, admin, `CREATE SCHEMA IF NOT EXISTS evil`)
	exec(t, admin, `CREATE TABLE evil.ratelimit_buckets (key text PRIMARY KEY, tokens double precision NOT NULL, last_take timestamptz NOT NULL)`)
	// A decoy row in the shadow: a gauge reading the shadow would report it.
	exec(t, admin, `INSERT INTO evil.ratelimit_buckets VALUES ('decoy', 1, now())`)
	plantHostilePath(t, admin, d.Database, "evil")

	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{IdleTTL: time.Hour})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := st.Take(context.Background(), reqs("sweep-probe", 10, 10, 10, 10)); err != nil {
		t.Fatalf("take: %v", err)
	}

	var buf bytes.Buffer
	st.WriteBucketsForTest(&buf)
	// One take creates TWO real buckets (class + aggregate); the shadow holds a
	// single decoy, so the number itself distinguishes which relation was read.
	if !strings.Contains(buf.String(), "olivares_http_ratelimit_store_buckets 2") {
		t.Errorf("the gauge must count the REAL table (2 buckets), not the shadow's single decoy: %q", buf.String())
	}

	// Age the real row past the TTL and sweep: the real row goes, the shadow's
	// decoy must remain untouched.
	exec(t, admin, `UPDATE public.ratelimit_buckets SET last_take = pg_catalog.now() - pg_catalog.make_interval(secs => 7200)`)
	st.SweepOnceForTest()
	var real, shadow int
	if err := admin.QueryRowContext(context.Background(), `SELECT pg_catalog.count(*) FROM public.ratelimit_buckets`).Scan(&real); err != nil {
		t.Fatalf("count real: %v", err)
	}
	if err := admin.QueryRowContext(context.Background(), `SELECT pg_catalog.count(*) FROM evil.ratelimit_buckets`).Scan(&shadow); err != nil {
		t.Fatalf("count shadow: %v", err)
	}
	if real != 0 || shadow != 1 {
		t.Errorf("the sweep hit the wrong relation: public=%d evil=%d (want public=0, evil=1)", real, shadow)
	}
}

// TestDDLPoolIsPinnedBeforeValidation observes the OTHER pool. The objects
// landing in public proves nothing about the DDL connection, because the DDL
// statements are schema-qualified anyway — measured: removing the DDL pin left
// the pin, attack and split legs all green.
//
// So the DDL DSN carries target_session_attrs=primary, which makes pgx run
// `pg_is_in_recovery()` UNQUALIFIED inside ValidateConnect, and the app role (the
// database owner here, as in any single-role deployment) plants a raising shadow
// of it on the inherited path. If ensureSchemaOn's connection is pinned first,
// the real catalog function answers and Open succeeds; if it is not, the shadow
// executes and the DDL connection is refused.
// Mutation: drop the DDL pin and this goes red with "shadow pg_is_in_recovery()".
func TestDDLPoolIsPinnedBeforeValidation(t *testing.T) {
	d := isolated(t)
	exec(t, rawConn(t, d.App), `CREATE SCHEMA IF NOT EXISTS evil`)
	exec(t, rawConn(t, d.App), `CREATE FUNCTION evil.pg_is_in_recovery() RETURNS pg_catalog.bool LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'shadow pg_is_in_recovery() executed'; END $fn$`)
	plantHostilePath(t, rawConn(t, d.Superuser), d.Database, "evil")

	ddlDSN := d.App
	sep := "?"
	if strings.Contains(ddlDSN, "?") {
		sep = "&"
	}
	ddlDSN += sep + "target_session_attrs=primary"

	// Precondition: unpinned, that DSN is refused — otherwise this proves nothing.
	probe := rawConn(t, ddlDSN)
	if err := probe.PingContext(context.Background()); err == nil {
		t.Fatal("fixture is not hostile: an unpinned connection carrying target_session_attrs should have hit the shadow")
	}

	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{DDLDSN: ddlDSN})
	if err != nil {
		t.Fatalf("the DDL pool must be pinned before pgx's validator runs, so target_session_attrs resolves pg_catalog.pg_is_in_recovery: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
}

// TestAdmitDMLRefusesARevokedFunctionExecute pins the EXECUTE arm of the
// admission. It is deliberately NOT driven through Open: ensureSchemaOn re-grants
// EXECUTE on every boot (CREATE OR REPLACE preserves a predecessor's ACL), so in
// the serial supported path the grant always wins the race and the arm is
// unreachable from there — which is why the earlier table-driven leg could not
// cover it, and why leaving it untested would have been an untested branch
// pretending to be covered.
//
// The arm still earns its place: it fires when a revocation lands between the
// post-flight and the admission, or when the DDL and DML DSNs address states
// that differ. Calling the check directly is the honest way to pin exactly that.
func TestAdmitDMLRefusesARevokedFunctionExecute(t *testing.T) {
	d := isolated(t)
	st, err := pgstore.Open(context.Background(), d.App, pgstore.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = st.Close()

	if err := pgstore.AdmitDMLForTest(context.Background(), d.App); err != nil {
		t.Fatalf("admission must pass on a freshly provisioned database: %v", err)
	}
	admin := rawConn(t, d.Superuser)
	var appRole string
	if err := rawConn(t, d.App).QueryRowContext(context.Background(), `SELECT current_user`).Scan(&appRole); err != nil {
		t.Fatalf("read app role: %v", err)
	}
	const fn = `public.olivares_ratelimit_take(pg_catalog.text[], pg_catalog.float8[], pg_catalog.float8[])`
	exec(t, admin, `REVOKE EXECUTE ON FUNCTION `+fn+` FROM PUBLIC`)
	exec(t, admin, `REVOKE EXECUTE ON FUNCTION `+fn+` FROM `+appRole)

	err = pgstore.AdmitDMLForTest(context.Background(), d.App)
	if err == nil {
		t.Fatal("admission must refuse when the application role cannot EXECUTE the take function: every take would fail with 42501 and the node would fall back to per-node quotas")
	}
	if !strings.Contains(err.Error(), "EXECUTE=false") {
		t.Errorf("the refusal must name the missing EXECUTE; got: %v", err)
	}
}
