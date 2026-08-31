// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package pgstore is the Postgres implementation of the rate-limiter's shared
// bucket Store: in an HA cluster every node's takes hit ONE table, so a
// tenant's quota is global instead of multiplied by the replica count. It is
// Postgres-only by definition (Postgres is the HA store fixed "no
// Redis in v1"), so it may use plpgsql freely.
//
// # One round trip, locks held microseconds
//
// A take is a single statement — SELECT ... FROM olivares_ratelimit_take(...)
// — executing a plpgsql function that upserts/refills each bucket IN REQUEST
// ORDER (class then aggregate everywhere ⇒ no lock-order cycles between
// takes), checks all-or-nothing admission, and decrements only when every
// bucket admits. A multi-statement client transaction would hold the hot
// aggregate row's lock across client round trips, capping one identity's
// throughput at wire latency and poisoning the pool under contention.
//
// # Clock correctness
//
// Refill arithmetic uses clock_timestamp() — evaluated INSIDE the row-locked
// UPDATE, i.e. AFTER any lock wait — never transaction/statement timestamps,
// which freeze before the wait: a take that waited on a hot row would
// otherwise compute a stale "now", write last_take BACKWARDS and re-credit a
// refill window another take already granted (systematic over-admission under
// exactly the contention a limiter exists for). last_take is additionally
// clamped monotonic (GREATEST) and elapsed clamped >= 0, so neither a lock
// inversion nor an NTP step on the PG host can drive tokens negative or
// double-credit. The PG host is the ONE clock for every node — cross-node
// clock skew, which the in-proc design never had to face, cannot corrupt the
// shared arithmetic.
package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/internal/pgpin"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/store"
)

// engineSchema is dialect.EngineSchema rendered for direct interpolation into this
// package's SQL, validated ONCE against the repo's single identifier pattern. The
// value is a constant of this repository — a violation is a programmer error, never
// an operator-reachable path — so this is an INIT-TIME invariant assertion (package
// initialization, not `go build`), surfaced by the first test run. The reusable
// edge, pgpin.Open, returns an error for the same condition rather than panicking.
//
// Interpolating (after the guard) rather than discovering the schema per query is
// deliberate: every guard and every DDL statement below must address the SAME
// schema the pinned pools resolve unqualified names in, and a second source of
// truth is exactly how DDL and runtime drift apart.
var engineSchema = mustSafeIdent(dialect.EngineSchema)

// The two schema-qualified object names, rendered once from the validated
// schema so every DDL, DML and verification statement addresses the same pair.
var (
	schemaQualifiedBuckets      = engineSchema + ".ratelimit_buckets"
	schemaQualifiedTakeFunction = engineSchema + ".olivares_ratelimit_take"
)

// Every SQL statement this package sends, each in ONE place so the static
// ratchet (TestProductionSQLIsFullyQualified) can scan them and so a future edit
// cannot introduce an unqualified name in a copy nobody inspects.
var (
	advisoryLockSQL   = `SELECT pg_catalog.pg_advisory_lock(pg_catalog.hashtextextended($1, 0))`
	advisoryUnlockSQL = `SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended($1, 0))`

	// #nosec G202 -- the ONLY interpolated value is engineSchema, validated by mustSafeIdent against store.SafeIdentPattern; Postgres cannot bind an identifier position, and TestProductionSQLIsFullyQualified is a static ratchet over these very statements
	createBucketsTableSQL = `
CREATE TABLE IF NOT EXISTS ` + schemaQualifiedBuckets + ` (
	key       pg_catalog.text        PRIMARY KEY,
	tokens    pg_catalog.float8      NOT NULL,
	last_take pg_catalog.timestamptz NOT NULL
)`

	// #nosec G202 -- the ONLY interpolated value is engineSchema, validated by mustSafeIdent against store.SafeIdentPattern; Postgres cannot bind an identifier position, and TestProductionSQLIsFullyQualified is a static ratchet over these very statements
	grantTakeExecuteSQL = `GRANT EXECUTE ON FUNCTION ` + schemaQualifiedTakeFunction + `(pg_catalog.text[], pg_catalog.float8[], pg_catalog.float8[]) TO PUBLIC`

	admitDMLSQL = `
SELECT pg_catalog.has_schema_privilege($1, 'USAGE'),
       pg_catalog.has_table_privilege($2, 'SELECT'),
       pg_catalog.has_table_privilege($2, 'INSERT'),
       pg_catalog.has_table_privilege($2, 'UPDATE'),
       pg_catalog.has_table_privilege($2, 'DELETE'),
       pg_catalog.has_function_privilege($3, 'EXECUTE')`

	inventoryTakeSQL = `
SELECT p.oid, p.prokind::pg_catalog.text, p.provariadic <> 0, p.pronargdefaults,
       pg_catalog.pg_get_function_identity_arguments(p.oid),
       pg_catalog.pg_get_userbyid(p.proowner),
       p.proowner = (SELECT r.oid FROM pg_catalog.pg_roles r WHERE r.rolname = current_user),
       (` + canonicalTakeArgsPredicate + `)
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = 'olivares_ratelimit_take'`

	verifyTakeSQL = `
SELECT pg_catalog.count(*),
       COALESCE(pg_catalog.bool_and(
             (` + canonicalTakeArgsPredicate + `)
         AND p.prokind = 'f' AND p.provariadic = 0 AND p.pronargdefaults = 0
         AND NOT p.prosecdef
         AND p.proretset
         AND p.prorettype = 'pg_catalog.record'::pg_catalog.regtype
         AND p.prolang = (SELECT l.oid FROM pg_catalog.pg_language l WHERE l.lanname = 'plpgsql')
         AND p.proallargtypes::pg_catalog.oid[] = ARRAY[
               'pg_catalog.text[]'::pg_catalog.regtype::pg_catalog.oid,
               'pg_catalog.float8[]'::pg_catalog.regtype::pg_catalog.oid,
               'pg_catalog.float8[]'::pg_catalog.regtype::pg_catalog.oid,
               'pg_catalog.bool'::pg_catalog.regtype::pg_catalog.oid,
               'pg_catalog.float8'::pg_catalog.regtype::pg_catalog.oid]
         AND p.proargmodes::pg_catalog.text[] = ARRAY['i','i','i','t','t']
         AND p.proconfig::pg_catalog.text[] = ARRAY['search_path=pg_catalog']
         AND p.proowner = (SELECT r.oid FROM pg_catalog.pg_roles r WHERE r.rolname = current_user)), false),
       COALESCE(pg_catalog.bool_and(EXISTS (
         SELECT 1 FROM pg_catalog.aclexplode(
                  COALESCE(p.proacl, pg_catalog.acldefault('f'::pg_catalog."char", p.proowner))) a
         WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE')), false),
       COALESCE(pg_catalog.string_agg(pg_catalog.pg_get_function_identity_arguments(p.oid) || ' owner=' || pg_catalog.pg_get_userbyid(p.proowner), '; '), 'none')
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = 'olivares_ratelimit_take'`

	// #nosec G202 -- the ONLY interpolated value is engineSchema, validated by mustSafeIdent against store.SafeIdentPattern; Postgres cannot bind an identifier position, and TestProductionSQLIsFullyQualified is a static ratchet over these very statements
	takeCallSQL = `SELECT allowed, tokens FROM ` + schemaQualifiedTakeFunction + `($1::pg_catalog.text[], $2::pg_catalog.float8[], $3::pg_catalog.float8[])`

	// #nosec G202 -- the ONLY interpolated value is engineSchema, validated by mustSafeIdent against store.SafeIdentPattern; Postgres cannot bind an identifier position, and TestProductionSQLIsFullyQualified is a static ratchet over these very statements
	sweepSQL = `
DELETE FROM ` + schemaQualifiedBuckets + ` WHERE key IN (
	SELECT key FROM ` + schemaQualifiedBuckets + `
	WHERE last_take < pg_catalog.now() - pg_catalog.make_interval(secs => $1)
	FOR UPDATE SKIP LOCKED
	LIMIT $2
)`

	// #nosec G202 -- the ONLY interpolated value is engineSchema, validated by mustSafeIdent against store.SafeIdentPattern; Postgres cannot bind an identifier position, and TestProductionSQLIsFullyQualified is a static ratchet over these very statements
	gaugeSQL = `SELECT pg_catalog.count(*) FROM ` + schemaQualifiedBuckets
)

func mustSafeIdent(s string) string {
	if !regexp.MustCompile(store.SafeIdentPattern).MatchString(s) {
		panic(fmt.Sprintf("ratelimit pgstore: engine schema %q is not a plain identifier", s))
	}
	return s
}

// migrateLockKey serializes DDL cluster-wide — the SAME advisory-lock key the
// engine store's schema apply uses (core/internal/store/sqlstore), so N nodes
// booting in parallel (the Helm chart's podManagementPolicy: Parallel) never
// race CREATE TABLE IF NOT EXISTS into a duplicate-key catalog error.
const migrateLockKey = "olivares.migrate.v1"

// defaultMaxConns bounds the dedicated pool. Takes are one ~microseconds
// round trip, so a handful of connections sustains thousands of takes/sec;
// the cap keeps N nodes' limiter pools from crowding max_connections (the
// Concern that capped the app pool).
const defaultMaxConns = 8

// Options tunes the store.
type Options struct {
	// IdleTTL is the sweep eviction horizon — pass ratelimit.IdleTTLFor(tiers)
	// so eviction is provably safe (an idle-this-long bucket has refilled to
	// full; deleting it equals handing out a fresh full bucket). 0 disables the
	// sweeper (tests). The proof assumes every node runs the SAME tier table:
	// a node on a stale config (smaller burst/rate ratios) can sweep a peer's
	// not-yet-refilled idle bucket — bounded over-grant of at most one burst,
	// idle-traffic only (denied buckets are protected by last_take advancing).
	// Distribute OLIVARES_RATELIMIT_CONFIG uniformly (the Helm chart does).
	IdleTTL time.Duration
	// MaxConns caps the dedicated pool (0 = defaultMaxConns).
	MaxConns int
	// Registry, when set, receives the scrape-time shared-bucket gauge.
	Registry *metrics.Registry
	// Logger receives sweeper warnings. nil = slog.Default().
	Logger *slog.Logger
	// DDLDSN, when set, is the owner/DDL-role DSN used for the one-time schema
	// DDL (bucket table + take function). Under the hardened owner/app split
	// (docs/SECURITY-HARDENING.md) the app role has no CREATE on the schema, so running the
	// DDL on the app pool fails boot with SQLSTATE 42501 — the same failure
	// the staging rehearsal caught in the leader elector. Empty = run the
	// DDL on dsn (single-role deployments, unchanged behavior).
	DDLDSN string
}

// Store implements ratelimit.Store over a dedicated Postgres pool (reusing the
// engine DSN, like the leader elector's lock pool — the limiter's latency must
// not contend with app-query checkouts and vice versa).
type Store struct {
	db       *sql.DB
	log      *slog.Logger
	idleTTL  time.Duration
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

var _ ratelimit.Store = (*Store)(nil)

// Open connects the dedicated pool, ensures the schema (table + take
// function) under the cluster migration lock, and starts the idle sweeper.
// Any error is a boot failure for the caller: the operator selected a shared
// store; a node that silently ran per-node would un-share every quota.
//
// Both pools are search_path-pinned to the engine schema on every physical
// connection (core/internal/pgpin — the two-hook pattern proven in sqlstore),
// and this package's SQL is additionally schema-qualified. The two layers cover
// different things: qualification is what actually closes name capture, while
// the pin bounds whatever a future edit leaves unqualified and stops an
// inherited hostile path from reaching the statements at all.
//
// Stated honestly, because a stronger claim was measured false: pinning to the
// engine schema does NOT make every operator trusted. When that schema is
// writable by the same role, an exact-signature operator planted there (probed
// in 15.18: `=(text[],text[])`) beats pg_catalog's polymorphic equality, and
// only OPERATOR(pg_catalog.=) is immune. It stays same-privilege — the role
// would be shadowing itself — and the security-critical body is closed by the
// function's own SET search_path = pg_catalog.
func Open(ctx context.Context, dsn string, opts Options) (*Store, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	maxConns := opts.MaxConns
	if maxConns <= 0 {
		maxConns = defaultMaxConns
	}
	db, err := pgpin.Open(dsn, engineSchema, maxConns)
	if err != nil {
		return nil, fmt.Errorf("ratelimit pgstore: open: %w", err)
	}
	db.SetMaxIdleConns(maxConns)
	ddlDSN := opts.DDLDSN
	if ddlDSN == "" {
		ddlDSN = dsn
	}
	if err := ensureSchemaOn(ctx, ddlDSN); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ratelimit pgstore: ensure schema: %w", err)
	}
	// database/sql opens lazily, so pgpin.Open succeeding proves NOTHING about the
	// DML pool: without a live round trip the first pin (and any DSN failure)
	// would surface on the first Take, where the limiter falls back to the
	// per-node store and silently un-shares every quota — the outcome Open's
	// contract calls a boot failure.
	//
	// admitDML below also forces a connection, so this ping is no longer the ONLY
	// thing keeping that contract (removing it alone leaves the suite green, which
	// is honest to state rather than to pretend otherwise). It is kept because it
	// separates "cannot connect" from "connected but unauthorized" in the error an
	// operator reads at boot, and it is the check that survives if the admission
	// is ever moved or made conditional.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ratelimit pgstore: admit pinned pool: %w", err)
	}
	// Connecting is not the same as being ABLE to work. The DDL ran on a possibly
	// different role, so the app role's authorization is a separate fact: a
	// revoked grant on an already-existing table (default privileges only cover
	// objects created afterwards) lets Open succeed and makes the first Take fail
	// with 42501 — straight into the per-node fallback this admission exists to
	// prevent. Measured on a split fixture. Checked from the DML pool, since that
	// is the identity that will run the traffic.
	if err := admitDML(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ratelimit pgstore: %w", err)
	}
	s := &Store{
		db: db, log: log, idleTTL: opts.IdleTTL,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if opts.Registry != nil {
		opts.Registry.RegisterFunc("olivares_http_ratelimit_store_buckets", s.writeBuckets)
	}
	if s.idleTTL > 0 {
		go s.sweepLoop()
	} else {
		close(s.done)
	}
	return s, nil
}

// ensureSchemaOn applies the DDL on a transient connection to ddlDSN (the
// owner role under the split topology; the app DSN otherwise) under the
// cluster-wide migration advisory lock (session lock on one checked-out conn,
// released on the same conn). The bucket DML pool never runs DDL: the owner's
// DEFAULT PRIVILEGES grant the app role DML on the created table, and the take
// function's EXECUTE is granted explicitly below (default privileges cover
// tables and sequences, not functions — and CREATE OR REPLACE preserves a
// pre-existing identity's ACL, so relying on the default would let a hardened
// or hostile predecessor survive the replace).
//
// The connection is search_path-pinned (pgpin) AND every statement qualified;
// db.Conn forces a physical connection so both pin hooks have provably run
// before the first DDL byte.
func ensureSchemaOn(ctx context.Context, ddlDSN string) error {
	db, err := pgpin.Open(ddlDSN, engineSchema, 0)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, advisoryLockSQL, migrateLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), advisoryUnlockSQL, migrateLockKey)
	}()
	// Cluster infrastructure, not tenant data: no tenant_id, no RLS — the
	// leader_epoch precedent. Bucket keys are metering identities
	// (tn:<uuid>|class), never user data.
	if _, err := conn.ExecContext(ctx, createBucketsTableSQL); err != nil {
		return err
	}
	// Overload inventory, fail-closed, under the same advisory lock. CREATE OR
	// REPLACE only replaces the identity with the SAME input types; a shadow
	// overload — measured: one extra DEFAULT argument — survives the replace and
	// makes even a fully-qualified, fully-cast call ambiguous ("function is not
	// unique"), a denial of admission control. Nothing is dropped: an unexpected
	// object is reported and boot refuses, per the no-function-deletion rule and
	// because appropriating an object this product did not create is how a
	// hostile object gains our ACL.
	if err := preflightTakeFunction(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, takeFunctionSQL); err != nil {
		return err
	}
	// Explicit, because CREATE OR REPLACE preserves a pre-existing identity's
	// owner AND ACL: a predecessor without PUBLIC EXECUTE (locally hardened
	// default privileges, or a revoked grant) would otherwise survive and break
	// the split topology at the first Take. SECURITY INVOKER means EXECUTE alone
	// grants no table DML — this widens nothing.
	if _, err := conn.ExecContext(ctx, grantTakeExecuteSQL); err != nil {
		return err
	}
	return verifyTakeFunction(ctx, conn)
}

// admitDML verifies, from the pool that will carry the traffic, that the app role
// can actually do the work: USAGE on the engine schema, all four table privileges
// the take function and the sweeper need, and EXECUTE on the exact canonical
// identity. Every check is by catalog function against a resolved OID, so it
// reports authorization rather than guessing from role membership.
//
// TABLE-level by design, and the trade-off is stated because it is a real (if
// narrow) false negative: has_table_privilege does not aggregate column grants,
// so a hand-built posture of column-level SELECT/INSERT/UPDATE plus table DELETE
// executes every statement fine yet is refused here (measured). The supported
// provisioning issues table-level DML, and being refused at boot with a precise
// message is a better failure than booting into per-node quotas — but a future
// revision that wants to support column grants needs has_column_privilege, not a
// weakening of this check.
func admitDML(ctx context.Context, db *sql.DB) error {
	var schemaUsage, tableSel, tableIns, tableUpd, tableDel, fnExec bool
	err := db.QueryRowContext(ctx, admitDMLSQL,
		engineSchema, schemaQualifiedBuckets,
		schemaQualifiedTakeFunction+"(pg_catalog.text[], pg_catalog.float8[], pg_catalog.float8[])",
	).Scan(&schemaUsage, &tableSel, &tableIns, &tableUpd, &tableDel, &fnExec)
	if err != nil {
		return fmt.Errorf("admit dml privileges: %w", err)
	}
	if !schemaUsage || !tableSel || !tableIns || !tableUpd || !tableDel || !fnExec {
		return fmt.Errorf("the application role lacks TABLE-level privileges the shared rate-limit store requires (schema USAGE=%t; %s SELECT=%t INSERT=%t UPDATE=%t DELETE=%t; take EXECUTE=%t). Booting without them fails at SQLSTATE 42501: SELECT/INSERT/UPDATE and EXECUTE break every take, which drops the node to per-node quotas and silently un-shares the limit the operator asked to share; DELETE instead breaks the idle SWEEPER, so buckets accumulate unbounded while takes keep working. Note the check is deliberately table-level (has_table_privilege): a hand-built posture granting only COLUMN privileges can satisfy the actual statements yet be refused here — grant table-level DML, which is what the supported provisioning issues (deploy/postgres, docs/08 §4)",
			schemaUsage, schemaQualifiedBuckets, tableSel, tableIns, tableUpd, tableDel, fnExec)
	}
	return nil
}

// canonicalTakeArgsPredicate matches pg_proc rows whose INPUT signature is exactly
// (text[], float8[], float8[]) by type OID — never by rendered signature text.
// oidvector subscripts are 0-based.
const canonicalTakeArgsPredicate = `
	p.pronargs = 3
	AND p.proargtypes[0] = 'pg_catalog.text[]'::pg_catalog.regtype
	AND p.proargtypes[1] = 'pg_catalog.float8[]'::pg_catalog.regtype
	AND p.proargtypes[2] = 'pg_catalog.float8[]'::pg_catalog.regtype`

// preflightTakeFunction refuses boot unless the take function's name in the engine
// schema is either unclaimed or already exactly the canonical identity owned by the
// effective DDL role. Any other occupant — a different signature, a DEFAULT-argument
// overload, a variadic, a procedure, or the right identity under someone else's
// ownership — is diagnosed, never dropped or appropriated.
func preflightTakeFunction(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, inventoryTakeSQL, engineSchema)
	if err != nil {
		return fmt.Errorf("inventory %s.olivares_ratelimit_take: %w", engineSchema, err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var (
			oid          int64
			kind         string
			variadic     bool
			nargDefaults int
			identityArgs string
			owner        string
			ownedByUs    bool
			canonical    bool
		)
		if err := rows.Scan(&oid, &kind, &variadic, &nargDefaults, &identityArgs, &owner, &ownedByUs, &canonical); err != nil {
			return err
		}
		seen++
		if seen > 1 || !canonical || kind != "f" || variadic || nargDefaults != 0 {
			return fmt.Errorf("refusing boot: %s.olivares_ratelimit_take is occupied by an unexpected object (oid %d, kind %q, args %q, owner %s): an extra overload makes even a fully-qualified call ambiguous, which is a denial of admission control; remove it manually — this store never drops objects it did not create", engineSchema, oid, kind, identityArgs, owner)
		}
		if !ownedByUs {
			return fmt.Errorf("refusing boot: %s.olivares_ratelimit_take (oid %d) exists but belongs to %s, not the DDL role: CREATE OR REPLACE would fail or, worse, adopt an object this product did not define", engineSchema, oid, owner)
		}
	}
	return rows.Err()
}

// verifyTakeFunction is the post-flight: after the CREATE OR REPLACE and the
// explicit grant, exactly one identity must exist and every attribute the runtime
// depends on must hold — by catalog OID and attribute, not by rendered text.
func verifyTakeFunction(ctx context.Context, conn *sql.Conn) error {
	var (
		count    int
		okShape  bool
		okACL    bool
		diagnose string
	)
	err := conn.QueryRowContext(ctx, verifyTakeSQL, engineSchema).Scan(&count, &okShape, &okACL, &diagnose)
	if err != nil {
		return fmt.Errorf("verify %s.olivares_ratelimit_take: %w", engineSchema, err)
	}
	if count != 1 || !okShape || !okACL {
		return fmt.Errorf("refusing boot: %s.olivares_ratelimit_take failed post-DDL verification (identities=%d, shape ok=%t, public execute=%t; found: %s)", engineSchema, count, okShape, okACL, diagnose)
	}
	return nil
}

// takeFunctionSQL is the whole take, server-side. Loop order = request order
// (deterministic everywhere ⇒ no take↔take deadlock); clock_timestamp() is
// evaluated per-row INSIDE the locked upsert (see the package comment); its
// two evaluations within one UPDATE differ by microseconds, which the
// GREATEST clamps make harmless. The function body runs in the single
// statement's implicit transaction: a DENIED take still commits its refills
// and last_take advances (the anti-reset-under-attack invariant) while
// decrementing nothing.
//
// Hostile-resolution posture, each layer closing what the previous cannot:
// SECURITY INVOKER declared (it was already the default; the runtime depends
// on it, so it is stated, and verifyTakeFunction pins prosecdef=false);
// SET search_path = pg_catalog so the body's operators never resolve in a
// hostile schema; table and catalog functions qualified anyway, because
// pg_temp is searched FIRST for relations and types even under a single
// trusted-schema path; signature and DECLARE types pg_catalog-qualified so a
// shadow DOMAIN cannot capture them. COALESCE/GREATEST/LEAST/EXTRACT are SQL
// grammar, not resolvable functions — qualifying them does not parse.
// #nosec G202 -- the ONLY interpolated value is engineSchema, validated by mustSafeIdent against store.SafeIdentPattern; Postgres cannot bind an identifier position, and TestProductionSQLIsFullyQualified is a static ratchet over these very statements
var takeFunctionSQL = `
CREATE OR REPLACE FUNCTION ` + schemaQualifiedTakeFunction + `(p_keys pg_catalog.text[], p_rates pg_catalog.float8[], p_bursts pg_catalog.float8[])
RETURNS TABLE(allowed pg_catalog.bool, tokens pg_catalog.float8)
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog
AS $fn$
DECLARE
	n   pg_catalog.int4 := COALESCE(pg_catalog.array_length(p_keys, 1), 0);
	i   pg_catalog.int4;
	t   pg_catalog.float8;
	tok pg_catalog.float8[] := '{}';
	ok  pg_catalog.bool := true;
BEGIN
	FOR i IN 1..n LOOP
		INSERT INTO ` + schemaQualifiedBuckets + ` AS b (key, tokens, last_take)
		VALUES (p_keys[i], p_bursts[i], pg_catalog.clock_timestamp())
		ON CONFLICT (key) DO UPDATE SET
			tokens    = LEAST(p_bursts[i], b.tokens + GREATEST(0, EXTRACT(EPOCH FROM (pg_catalog.clock_timestamp() - b.last_take))) * p_rates[i]),
			last_take = GREATEST(b.last_take, pg_catalog.clock_timestamp())
		RETURNING b.tokens INTO t;
		tok := pg_catalog.array_append(tok, t);
		IF t < 1 THEN
			ok := false;
		END IF;
	END LOOP;
	IF ok THEN
		UPDATE ` + schemaQualifiedBuckets + ` b SET tokens = b.tokens - 1 WHERE b.key = ANY(p_keys);
		FOR i IN 1..n LOOP
			tok[i] := tok[i] - 1;
		END LOOP;
	END IF;
	RETURN QUERY SELECT ok, pg_catalog.unnest(tok);
END;
$fn$`

// Take implements ratelimit.Store: one round trip, one row per requested
// bucket (request order), the allowed flag repeated on each.
func (s *Store) Take(ctx context.Context, reqs []ratelimit.StoreRequest) (bool, []ratelimit.StoreState, error) {
	if len(reqs) == 0 {
		// Zero buckets constrain nothing — admitted by definition. (The SQL
		// function would RETURN zero rows for an empty array and the scan loop
		// would otherwise report allowed=false against the server's ok=true.)
		return true, nil, nil
	}
	keys := make([]string, len(reqs))
	rates := make([]float64, len(reqs))
	bursts := make([]float64, len(reqs))
	for i, r := range reqs {
		keys[i] = r.Key
		rates[i] = r.Limit.Rate
		bursts[i] = r.Limit.Burst
	}
	// Qualified name AND pg_catalog-qualified casts: the casts force the exact
	// canonical identity so an ordinary same-name overload cannot win resolution
	// (a DEFAULT-argument overload could still make this ambiguous, which is why
	// ensureSchemaOn refuses boot while one exists).
	rows, err := s.db.QueryContext(ctx, takeCallSQL, keys, rates, bursts)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	allowed := false
	states := make([]ratelimit.StoreState, 0, len(reqs))
	for rows.Next() {
		var st ratelimit.StoreState
		if err := rows.Scan(&allowed, &st.Tokens); err != nil {
			return false, nil, err
		}
		states = append(states, st)
	}
	if err := rows.Err(); err != nil {
		return false, nil, err
	}
	if len(states) != len(reqs) {
		return false, nil, fmt.Errorf("ratelimit pgstore: take returned %d states for %d buckets", len(states), len(reqs))
	}
	return allowed, states, nil
}

// sweepLoop evicts idle buckets on a jittered half-TTL cadence. Batches use
// FOR UPDATE SKIP LOCKED so the sweep NEVER waits on (or deadlocks with) a row
// a take currently holds — an active bucket simply skips this round. Eviction
// safety is the IdleTTL derivation (idle ≥ burst/rate ⇒ refilled to full).
// Every node may sweep; the work is idempotent and skip-locked.
func (s *Store) sweepLoop() {
	defer close(s.done)
	for {
		// Jitter ±25% so N nodes' sweeps de-synchronize.
		base := s.idleTTL / 2
		jitter := time.Duration(rand.Int64N(int64(base)/2+1)) - base/4 // #nosec G404 -- non-crypto jitter to spread cache-TTL expiry, not a security decision
		select {
		case <-s.stop:
			return
		case <-time.After(base + jitter):
		}
		s.sweepOnce()
	}
}

func (s *Store) sweepOnce() {
	const batch = 1000
	for {
		select {
		case <-s.stop: // a closing engine must not wait through a long backlog
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, err := s.db.ExecContext(ctx, sweepSQL, s.idleTTL.Seconds(), batch)
		cancel()
		if err != nil {
			s.log.Warn("ratelimit pgstore: idle-bucket sweep failed (will retry next cycle)", "err", err)
			return
		}
		n, _ := res.RowsAffected()
		if n < batch {
			return
		}
	}
}

// writeBuckets emits the live shared-bucket count at scrape time (the HA
// counterpart of olivares_http_ratelimit_active_buckets, which counts only
// this node's in-memory fallback shards). Short timeout so a wedged store
// degrades the gauge, never the scrape.
func (s *Store) writeBuckets(w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var n int64
	if err := s.db.QueryRowContext(ctx, gaugeSQL).Scan(&n); err != nil {
		return // absent on error; olivares_http_ratelimit_store_up carries the outage signal
	}
	const name = "olivares_http_ratelimit_store_buckets"
	fmt.Fprintf(w, "# HELP %s Live token buckets in the shared rate-limit store (cluster-wide).\n# TYPE %s gauge\n%s %d\n", name, name, name, n)
}

// Close stops the sweeper (idempotent and concurrency-safe) and closes the
// pool. sweepOnce checks the stop channel between batches, so Close never
// waits through a long multi-batch backlog.
func (s *Store) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	return s.db.Close()
}

// ErrNotPostgres is returned by callers that resolve the store selection; kept
// here so the message stays with the backend it describes.
var ErrNotPostgres = errors.New("the shared rate-limit store requires --engine postgres (buckets live in the engine's Postgres)")
