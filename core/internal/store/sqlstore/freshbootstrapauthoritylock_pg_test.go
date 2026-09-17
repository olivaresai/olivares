// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// THE H LOCK'S PRE-EFFECT COMPATIBILITY BOUNDARY, ON A REAL POSTGRESQL 16 SERVER.
//
// Every case here runs against a real server through the production Open, on a private
// database, with the real owner/application role split unless it says otherwise. None of them
// wraps a SQLite fixture to stand in for one: the defect this file measures does not exist on
// SQLite, because it is about a routine name resolved in a schema by a catalog projection.
//
// WHAT THE MATRIX IS FOR. The refusal it exercises already existed — a foreign
// `public.olivares_lock_core_user_authority(<anything>)` has always made this build's own v10
// authority verifier refuse. What did NOT exist was the refusal arriving before the boot had
// committed the three rollout relations and core v1 through v9. So an assertion that "Open
// returned an error" proves nothing here, and no test below settles for one: the property is
// that the captured logical schema fields and table rows are unchanged. Some cases also
// compare a detailed routine snapshot; neither snapshot represents every database byte.
//
// The three cases the early check must NOT swallow are as load-bearing as the refusals, and
// each has its own test: an exact twin is still the MANAGED CENSUS's refusal, a foreign
// retention overload of the measured `(text)` or variadic shape completes ordinary Open,
// and an incomplete reserved fence family is still the admission's own reserved-family refusal.

// userAuthorityLockPGStore is accessEvidencePGStore plus a SUPERUSER handle, and the extra
// handle is not a convenience.
//
// Every preservation claim in this file is a logical snapshot of the managed schema, and that
// snapshot enumerates the ROWS of every ordinary relation. From core v9 onwards those relations
// carry FORCE ROW LEVEL SECURITY whose policy calls `current_setting('app.tenant_id')` WITHOUT
// missing_ok — deliberately, so a forgotten bind RAISES instead of silently matching zero rows.
// The owner role is subject to that policy, so a raw census through it either fails outright or,
// worse, succeeds against a connection some earlier probe left with the GUC defined and reports
// the rows of ONE tenant as if they were all of them.
//
// A superuser bypasses row-level security entirely, so the snapshot sees every row and the
// before/after comparison means what it says. It is used for READING only; every boot in this
// file still runs through the ordinary application and owner roles.
func userAuthorityLockPGStore(t *testing.T) (store.Config, *sql.DB, *sql.DB, dialect.Dialect) {
	t.Helper()
	dsns := isolatedPGSplit(t)
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}
	return cfg, guardPGProbe(t, dsns.Owner), guardPGProbe(t, dsns.Superuser), dia
}

// pgUserAuthorityRoutineSnapshot is a foreign routine's identity, owner, body and ACL, as the
// catalog holds them.
//
// Preservation is asserted against THIS rather than against a row count, because the claim
// being made is "it was not read for adoption, not dropped, not re-owned and not re-granted",
// and a count says none of that. proacl is rendered as text so a NULL — the default ACL, which
// is what a foreign routine nobody has granted on carries — is distinguishable from an empty
// one.
func pgUserAuthorityRoutineSnapshot(t *testing.T, db *sql.DB, schema, name string) []string {
	t.Helper()
	return pgQueryStrings(t, db, `SELECT p.prokind::text
  || '|' || COALESCE(pg_catalog.array_to_string(ARRAY(
       SELECT pg_catalog.format_type(t, NULL) FROM pg_catalog.unnest(p.proargtypes) AS t), ', '), '')
  || '|' || pg_catalog.pg_get_function_identity_arguments(p.oid)
  || '|' || o.rolname
  || '|' || p.prosrc
  || '|' || p.provariadic::text
  || '|' || p.pronargdefaults::text
  || '|' || p.prosecdef::text
  || '|' || COALESCE(p.proacl::text, '<default>')
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
JOIN pg_catalog.pg_roles o ON o.oid = p.proowner
WHERE n.nspname = $1 AND p.proname = $2`, schema, name)
}

// pgUserAuthorityZeroEffects is the assertion the whole change exists for: after a refusal,
// nothing this boot could have created exists.
//
// It names the objects in the ORDER a boot creates them, so a failure says how far the boot got
// rather than only that it got somewhere: the rollout relations commit first, the core tracker
// second, and H is the last thing core v10 creates.
func pgUserAuthorityZeroEffects(t *testing.T, owner *sql.DB) {
	t.Helper()
	for _, relation := range []string{
		dialect.ControlRolloutStateTable,
		dialect.ControlRolloutTransitionTable,
		dialect.ControlRolloutClassificationTable,
		coreTrackingTable,
		userAuthorityDescriptor.Table,
	} {
		if pgRelationExists(t, owner, dialect.EngineSchema, relation) {
			t.Errorf("the refusal created %s: the boot committed before it stopped", relation)
		}
	}
}

// foreignUserAuthorityLockOverloads are the same-public-name routines this build never creates
// and can never resolve past.
//
// The four shapes are not decoration. `(boolean)` is the measured witness from the B4 review.
// The appended DEFAULT and the variadic form are the two shapes PostgreSQL's own function
// resolution treats as candidates for a call that supplies one text argument, which is exactly
// the call `SELECT public.olivares_lock_core_user_authority($1)` makes with no explicit cast —
// so they are the cases where "the name resolves to one routine" stops being true even though
// the signatures differ. Refusing them is name closure; it is NOT a claim that either would
// have hijacked the call, and no such experiment is run against a foreign body.
//
// The PROCEDURE is here because prokind is not filtered: the by-name projection this check
// protects sees it, so this check has to.
var foreignUserAuthorityLockOverloads = []struct {
	name      string
	ddl       string
	signature string
}{
	{
		name: "a boolean overload, the measured witness",
		ddl: `CREATE FUNCTION public.olivares_lock_core_user_authority(review boolean) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`,
		signature: "boolean",
	},
	{
		name: "the compiled signature with an appended defaulted parameter",
		ddl: `CREATE FUNCTION public.olivares_lock_core_user_authority(target_user text, review text DEFAULT 'review')
RETURNS bigint LANGUAGE sql IMMUTABLE AS $$ SELECT pg_catalog.length(target_user || review)::pg_catalog.int8 $$`,
		signature: "text, text",
	},
	{
		name: "a materially callable variadic form",
		ddl: `CREATE FUNCTION public.olivares_lock_core_user_authority(VARIADIC review text[]) RETURNS bigint
LANGUAGE sql IMMUTABLE AS $$ SELECT pg_catalog.array_length(review, 1)::pg_catalog.int8 $$`,
		signature: "text[]",
	},
	{
		name: "a procedure wearing the name, which prokind is not filtered to hide",
		ddl: `CREATE PROCEDURE public.olivares_lock_core_user_authority(review int)
LANGUAGE plpgsql AS $$ BEGIN RAISE NOTICE '%', review; END $$`,
		signature: "integer",
	},
}

// TestPostgresUserAuthorityLockCompatibilityRefusesBeforeAnyEffect is the case root ratified
// option A for.
//
// The fixture is a max0 database carrying ONE foreign routine under H's public name and one
// unrelated sentinel table holding a row. The managed census correctly does not match the
// routine — a different signature is a different function — so before this change the classifier
// answered `fresh-empty`, classifyRolloutControls committed the three rollout relations, core
// v1 through v9 committed, and core v10's verifier then refused on `more than one overload`.
//
// Every assertion below is about the ESTATE and not about the error text: the typed sentinel,
// then a logical snapshot of the whole schema taken before the refused Open and compared after
// it, then the foreign routine's own body, owner and ACL, then the sentinel row.
func TestPostgresUserAuthorityLockCompatibilityRefusesBeforeAnyEffect(t *testing.T) {
	for _, tc := range foreignUserAuthorityLockOverloads {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, super, _ := userAuthorityLockPGStore(t)
			for _, stmt := range []string{
				"CREATE TABLE public.review_unrelated (value text NOT NULL)",
				"INSERT INTO public.review_unrelated (value) VALUES ('preserve-me')",
				tc.ddl,
			} {
				mustExec(t, owner, stmt)
			}
			before := pgLogicalSnapshot(t, super, dialect.EngineSchema)
			foreignBefore := pgUserAuthorityRoutineSnapshot(t, super, dialect.EngineSchema,
				"olivares_lock_core_user_authority")
			if len(foreignBefore) != 1 {
				t.Fatalf("the fixture left %d routines under the H name, want exactly the foreign one", len(foreignBefore))
			}

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("H_LOCK_PREFLIGHT|case=%s|open_error=%v", tc.name, err)
			if err == nil {
				t.Fatal("Open served a database whose H lock name cannot resolve to this build's routine")
			}
			// The state claims come first and do NOT stop the run. "Refused before any
			// effect" is the property; when it breaks, the error identity is the next
			// thing worth reading rather than a reason to stop looking.
			pgUserAuthorityZeroEffects(t, super)
			if !errors.Is(err, ErrUserAuthorityLockNameIncompatible) {
				t.Errorf("Open error = %v, want the User authority lock's own compatibility refusal", err)
			}
			if !strings.Contains(err.Error(), "("+tc.signature+")") {
				t.Errorf("the refusal does not name the observed signature (%s): %v", tc.signature, err)
			}
			if got := pgUserAuthorityRoutineSnapshot(t, super, dialect.EngineSchema,
				"olivares_lock_core_user_authority"); len(got) != 1 || got[0] != foreignBefore[0] {
				t.Errorf("the refusal changed the foreign routine.\n--- before ---\n%v\n--- after ---\n%v",
					foreignBefore, got)
			}
			var value string
			if qerr := owner.QueryRowContext(ctx,
				"SELECT value FROM public.review_unrelated").Scan(&value); qerr != nil || value != "preserve-me" {
				t.Errorf("the unrelated sentinel row changed: value=%q err=%v", value, qerr)
			}
			if got := pgLogicalSnapshot(t, super, dialect.EngineSchema); got != before {
				t.Errorf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestPostgresUserAuthorityLockCompatibilityCoversTheSchemaOnlyPhase discharges root's
// instruction to VERIFY the ordering the two phases share rather than assume it.
//
// `ApplyMigrations` is the separate migrate → GRANT → serve phase: it runs the same
// withMigrationLock callback and returns as soon as that lock is released, before anything that
// would make the process able to serve. So the compatibility check is inside the phase it needs
// to be inside, and this is the measurement rather than the reading — an operator who runs
// `migrate` first must get the refusal there, with the estate intact, and not discover it at
// `serve` after the schema has been applied.
func TestPostgresUserAuthorityLockCompatibilityCoversTheSchemaOnlyPhase(t *testing.T) {
	ctx := context.Background()
	cfg, owner, super, _ := userAuthorityLockPGStore(t)
	mustExec(t, owner, foreignUserAuthorityLockOverloads[0].ddl)
	before := pgLogicalSnapshot(t, super, dialect.EngineSchema)

	err := ApplyMigrations(ctx, cfg, nil)
	t.Logf("H_LOCK_PREFLIGHT_SCHEMA_ONLY|apply_error=%v", err)
	if err == nil {
		t.Fatal("the schema-only phase applied a schema whose H lock name cannot resolve to this build's routine")
	}
	pgUserAuthorityZeroEffects(t, super)
	if !errors.Is(err, ErrUserAuthorityLockNameIncompatible) {
		t.Errorf("ApplyMigrations error = %v, want the User authority lock's own compatibility refusal", err)
	}
	if got := pgLogicalSnapshot(t, super, dialect.EngineSchema); got != before {
		t.Errorf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
	// The control on the same fixture family: without the foreign routine, this phase
	// applies the schema and reaches the current supported version, so the refusal above is the routine's and not the
	// phase's.
	cleanCfg, cleanOwner, _, dia := userAuthorityLockPGStore(t)
	if aerr := ApplyMigrations(ctx, cleanCfg, nil); aerr != nil {
		t.Fatalf("the schema-only phase refused a clean database: %v", aerr)
	}
	if got := postgresMaxCoreVersion(t, cleanOwner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("the schema-only phase reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
}

// TestPostgresUserAuthorityLockCompatibilityLeavesExactTwinsToTheManagedCensus is the boundary
// that keeps the two contracts apart, and it FAILS if this check ever absorbs the other one.
//
// An exactly precreated H — rendered from the production constant, so it is exact by
// construction — is not a compatibility conflict: it is an object of this build's own managed
// identity sitting in a database that records no core migration, which is the max0 admission's
// question and answered by ErrGuardManifestNoEdge. This check must let it through so the
// admission can refuse it, and must NOT report it as a foreign occupation of the name.
//
// It is also the exact shape of root's "does not adopt a sole existing exact signature as valid
// authority": permitting it HERE is not adopting it anywhere.
func TestPostgresUserAuthorityLockCompatibilityLeavesExactTwinsToTheManagedCensus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		routine string
		seed    string
	}{
		{
			name:    "the lock function, exactly as core v10 renders it",
			routine: "olivares_lock_core_user_authority",
			seed:    postgresUserAuthorityLockDDL,
		},
		{
			name:    "the retention function, exactly as core v10 renders it",
			routine: "olivares_retain_user_authority",
			seed:    postgresUserAuthorityRetentionDDL,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, super, _ := userAuthorityLockPGStore(t)
			mustExec(t, owner, tc.seed)
			before := pgLogicalSnapshot(t, super, dialect.EngineSchema)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			if err == nil {
				t.Fatal("Open admitted an exact twin of a managed core v10 identity")
			}
			pgUserAuthorityZeroEffects(t, super)
			if errors.Is(err, ErrUserAuthorityLockNameIncompatible) {
				t.Errorf("the compatibility check claimed an exact twin, which is the managed census's refusal: %v", err)
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) &&
				!errors.Is(err, ErrGuardControlPlaneBootstrapInconsistent) {
				t.Errorf("Open error = %v, want the max0 admission's own refusal", err)
			}
			if !strings.Contains(err.Error(), tc.routine) {
				t.Errorf("the refusal does not name the object it refused: %v", err)
			}
			if got := pgLogicalSnapshot(t, super, dialect.EngineSchema); got != before {
				t.Errorf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestPostgresUserAuthorityLockCompatibilityAdmitsTheOrdinaryPostures is the control every
// refusal above is measured against, on BOTH role postures this product supports.
//
// Without it a refusal proves nothing: a fixture family that never boots would produce the same
// red. It also carries the positive half of the contract — that the boot really does end with
// exactly one routine under each v10 name, that the routine bound to that name is the COMPILED
// body (its own error message is the witness, and no foreign body can produce it), and that the
// retention trigger really refuses a DELETE.
func TestPostgresUserAuthorityLockCompatibilityAdmitsTheOrdinaryPostures(t *testing.T) {
	for _, posture := range []string{"split owner and application roles", "single role"} {
		t.Run(posture, func(t *testing.T) {
			ctx := context.Background()
			var cfg store.Config
			var owner *sql.DB
			var dia dialect.Dialect
			if posture == "single role" {
				dsns := isolatedPG(t)
				var ok bool
				if dia, ok = dialect.New(store.EnginePostgres); !ok {
					t.Fatal("no PostgreSQL dialect")
				}
				cfg = store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
				owner = guardPGProbe(t, dsns.App)
			} else {
				cfg, owner, dia = accessEvidencePGStore(t)
			}

			for _, pass := range []string{"first Open", "reopen"} {
				st, err := Open(ctx, cfg, nil)
				if err != nil {
					t.Fatalf("%s: the clean fixture refused its own intended path: %v", pass, err)
				}
				// The NORMAL SYSTEM BOOTSTRAP, on the first pass and again on the
				// second: a store that opens but cannot serve its own system scope has
				// not proved the boot completed.
				if err := st.System(ctx, func(sys store.SystemScope) error {
					if _, e := sys.EnsureSystemTenant(ctx); e != nil {
						return e
					}
					return sys.EnsureDefaultWorkspaces(ctx)
				}); err != nil {
					t.Fatalf("%s: bootstrap the SYSTEM tenant: %v", pass, err)
				}
				if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
					_, e := sc.DefaultWorkspace(ctx)
					if !errors.Is(e, store.ErrNotFound) {
						return fmt.Errorf("system default workspace: %w", e)
					}
					return nil
				}); err != nil {
					t.Fatalf("%s: serve the SYSTEM scope: %v", pass, err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			}

			if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
				t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
			}
			if got := pgRoutineSignatures(t, owner, "olivares_lock_core_user_authority"); len(got) != 1 || got[0] != "text" {
				t.Fatalf("H lock signatures = %q, want exactly this build's own (text)", got)
			}
			if got := pgRoutineSignatures(t, owner, "olivares_retain_user_authority"); len(got) != 1 || got[0] != "" {
				t.Fatalf("retention signatures = %q, want exactly this build's own zero-input routine", got)
			}
			assertPGUserAuthorityRuntimeBehaviour(t, owner)
		})
	}
}

// assertPGUserAuthorityRuntimeBehaviour proves the two v10 routines the name resolves to are
// THIS BUILD'S, by their own behavior rather than by their presence in a catalog.
//
//   - the lock, called the way userauthority.go calls it and with no tenant bound, raises the
//     compiled body's own scope message. A foreign routine cannot produce that string, and a
//     routine that had been silently replaced would not either;
//   - the retention trigger refuses a DELETE. It is a statement-level BEFORE trigger, so it
//     fires on an empty relation too, which is what makes this assertion independent of whether
//     any authority row happens to exist.
func assertPGUserAuthorityRuntimeBehaviour(t *testing.T, owner *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var version int64
	err := owner.QueryRowContext(ctx, "SELECT public.olivares_lock_core_user_authority($1)",
		"01993926-1000-7000-8000-000000000001").Scan(&version)
	if err == nil || !strings.Contains(err.Error(), "invalid User authority lock scope") {
		t.Errorf("the H lock call = %v, want the compiled body's own scope refusal", err)
	}
	tx, terr := owner.BeginTx(ctx, nil)
	if terr != nil {
		t.Fatal(terr)
	}
	defer tx.Rollback() //nolint:errcheck // probe only
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	if err := dia.BindTenant(ctx, tx, model.SystemTenantID); err != nil {
		t.Fatalf("bind the SYSTEM tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM public."+userAuthorityDescriptor.Table); err == nil ||
		!strings.Contains(err.Error(), "User authority is permanent") {
		t.Errorf("DELETE on the authority relation = %v, want the retention trigger's own refusal", err)
	}
}

// TestPostgresUserAuthorityLockCompatibilityPreservesRoutinesItDoesNotClaim is the expensive
// direction: refusing an install this build has no business refusing.
//
// All three neighbours are legitimate objects of somebody else's, and the queries and calls this
// check protects name `public` explicitly and one name exactly. A prefix rule, a name-global
// rule or a schema-blind rule would each get one of these wrong, and the block-mutation overload
// additionally pins FB-1: that preservation predates this change and must survive it.
func TestPostgresUserAuthorityLockCompatibilityPreservesRoutinesItDoesNotClaim(t *testing.T) {
	ctx := context.Background()
	cfg, owner, super, dia := userAuthorityLockPGStore(t)
	mustExec(t, owner, "CREATE SCHEMA review_foreign")
	for _, stmt := range []string{
		// The SAME name and the SAME signature, in another owner's schema.
		`CREATE FUNCTION review_foreign.olivares_lock_core_user_authority(target_user text) RETURNS bigint
LANGUAGE sql IMMUTABLE AS $$ SELECT pg_catalog.length(target_user)::pg_catalog.int8 $$`,
		// A neighbouring name in public that shares every character of a prefix.
		`CREATE FUNCTION public.olivares_lock_core_user_authority_review(target_user text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT target_user $$`,
		// FB-1's preserved overload of a DIFFERENT managed routine name.
		`CREATE FUNCTION public.olivares_block_mutation(review text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`,
	} {
		mustExec(t, owner, stmt)
	}
	foreignBefore := pgLogicalSnapshot(t, super, "review_foreign")

	for _, pass := range []string{"first Open", "reopen"} {
		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("%s: routines this build does not administer blocked an install: %v", pass, err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	if got := pgLogicalSnapshot(t, super, "review_foreign"); got != foreignBefore {
		t.Fatalf("the install changed a foreign schema.\n--- before ---\n%s\n--- after ---\n%s", foreignBefore, got)
	}
	if got := pgRoutineSignatures(t, owner, "olivares_lock_core_user_authority"); len(got) != 1 || got[0] != "text" {
		t.Fatalf("public H lock signatures = %q, want exactly this build's own", got)
	}
	if got := pgRoutineSignatures(t, owner, "olivares_lock_core_user_authority_review"); len(got) != 1 || got[0] != "text" {
		t.Fatalf("the neighbouring name = %q, want it preserved untouched", got)
	}
	if got := pgRoutineSignatures(t, owner, dialect.BlockMutationFn); len(got) != 2 || got[0] != "" || got[1] != "text" {
		t.Fatalf("%s signatures = %q, want the foreign (text) overload beside this build's zero-input function",
			dialect.BlockMutationFn, got)
	}
	assertPGUserAuthorityRuntimeBehaviour(t, owner)
}

// TestPostgresRetentionOverloadAloneCompletesAnOrdinaryOpen ISOLATES the case the B4 combined
// fixture could not decide, which is why root asked for it by name.
//
// That fixture created a foreign H lock AND a foreign retention overload together, so the boot's
// refusal was attributable to the H lock alone and said nothing about retention. The source
// answer is that they are different call paths: CREATE TRIGGER binds its handler's OID, and the
// ordinary relation verifier reads the registered trigger invariant through tgfoid rather than
// projecting the retention name. This test measures two compatible nonzero-input shapes;
// the DEFAULT overload's separate trigger-rendering equivalence is measured below.
//
// `(text)` is B4's OWN retention shape, so this is the direct isolation of its fixture: on its
// own it boots, reopens, and leaves the retention trigger refusing a DELETE. The variadic form
// is here because it is nonzero-input and NOT callable with zero arguments, which is the
// distinction the seam below turns out to rest on.
//
// SO THESE TWO POSITIVES DO NOT GENERALISE, and the correction below is what proves it: being
// nonzero-input was never the property that made them compatible. `(review text DEFAULT
// 'review')` is nonzero-input TOO and was incompatible, because a default makes it callable
// with zero arguments. Read these cases as "these two shapes were measured", never as "every
// nonzero-input overload was already fine".
func TestPostgresRetentionOverloadAloneCompletesAnOrdinaryOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		ddl  string
	}{
		{
			name: "the (text) overload B4's combined fixture also carried",
			ddl: `CREATE FUNCTION public.olivares_retain_user_authority(review text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`,
		},
		{
			name: "a variadic overload",
			ddl: `CREATE FUNCTION public.olivares_retain_user_authority(VARIADIC review text[]) RETURNS bigint
LANGUAGE sql IMMUTABLE AS $$ SELECT pg_catalog.array_length(review, 1)::pg_catalog.int8 $$`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, super, dia := userAuthorityLockPGStore(t)
			mustExec(t, owner, tc.ddl)
			foreignBefore := pgUserAuthorityRoutineSnapshot(t, super, dialect.EngineSchema,
				"olivares_retain_user_authority")
			if len(foreignBefore) != 1 {
				t.Fatalf("the fixture left %d retention routines, want exactly the foreign one", len(foreignBefore))
			}

			for _, pass := range []string{"first Open", "reopen"} {
				st, err := Open(ctx, cfg, nil)
				if err != nil {
					// A refusal here is a real finding and is reported as the concrete
					// seam it is, rather than hidden by widening the early deny list.
					t.Fatalf("%s: a foreign retention overload alone refused the boot, which is a downstream seam this change did not touch: %v",
						pass, err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
				t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
			}
			// THE ACTUAL TRIGGER, exercised: the DELETE is refused by the routine core v10
			// created, with the foreign overload of that same name sitting beside it.
			assertPGUserAuthorityRuntimeBehaviour(t, owner)
			after := pgUserAuthorityRoutineSnapshot(t, super, dialect.EngineSchema, "olivares_retain_user_authority")
			if len(after) != 2 {
				t.Fatalf("retention routines after the boot = %d (%v), want the foreign one beside this build's own",
					len(after), after)
			}
			var found bool
			for _, row := range after {
				if row == foreignBefore[0] {
					found = true
				}
			}
			if !found {
				t.Fatalf("the boot changed the foreign retention routine.\n--- before ---\n%v\n--- after ---\n%v",
					foreignBefore, after)
			}
		})
	}
}

// TestPostgresRetentionZeroCallableOverloadMeetsTheTriggerDefinitionSeam ASSERTS THE REPAIR the
// measurement it used to record asked for.
//
// ITS HISTORY IS THE POINT, so it is written down rather than deleted with the skip. In the
// frozen H tree this case recorded a KNOWN FAILURE: a retention overload declared
// `(review text DEFAULT 'review')` is callable with ZERO arguments, so
// `olivares_retain_user_authority` stops being an unambiguous zero-argument reference,
// pg_get_triggerdef deparses the handler SCHEMA-QUALIFIED, and a self-test that hashed exactly
// that rendered text against ONE expected digest refused the boot. The test asserted the
// refusal and skipped if it ever stopped happening. Root ratified the bounded correction; this
// tree carries it, so the case now asserts the POSITIVE and never skips.
//
// WHAT IT IS STILL NOT: the trigger's BINDING never moved. pg_trigger.tgfoid points at the
// exact zero-input handler core v10 created both before and after, and pg_get_functiondef of
// that OID is byte-identical; what changed is how a catalog function chooses to PRINT the name.
// The assertion below still requires the H compatibility sentinel to be ABSENT, because the H
// lock's reserved-name contract has nothing to say about a retention overload and must not
// start claiming one.
//
// The estate is built FIRST, deliberately. On a max0 fixture the overload is present while core
// v10 itself installs the trigger, which is the same equivalence measured one phase earlier;
// that path has its own case in userauthority_retention_rendering_pg_test.go. Here the subject
// is an EXISTING trigger whose rendering changes underneath a running estate.
func TestPostgresRetentionZeroCallableOverloadMeetsTheTriggerDefinitionSeam(t *testing.T) {
	ctx := context.Background()
	cfg, owner, _, dia := userAuthorityLockPGStore(t)
	built, berr := Open(ctx, cfg, nil)
	if berr != nil {
		t.Fatalf("build the current estate: %v", berr)
	}
	if err := built.Close(); err != nil {
		t.Fatal(err)
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	mustExec(t, owner, `CREATE FUNCTION public.olivares_retain_user_authority(review text DEFAULT 'review') RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`)

	st, err := Open(ctx, cfg, nil)
	t.Logf("RETENTION_TRIGGERDEF_SEAM|open_error=%v", err)
	if err != nil {
		if errors.Is(err, ErrUserAuthorityLockNameIncompatible) {
			t.Fatalf("the H lock compatibility check claimed a RETENTION overload, which is not its contract: %v", err)
		}
		t.Fatalf("the corrected comparator still refused the qualified rendering of its own trigger: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// The mechanism, read directly, so this case names what it accepted and not only that it
	// accepted something. Both facts are asserted: the printed name gained its schema
	// qualifier, and the binding did not move.
	rendered := pgQueryStrings(t, owner, `SELECT pg_catalog.pg_get_triggerdef(t.oid)
FROM pg_catalog.pg_trigger t WHERE t.tgname = 'core_user_authority_no_delete' AND NOT t.tgisinternal`)
	if len(rendered) != 1 || !strings.Contains(rendered[0], "EXECUTE FUNCTION public.olivares_retain_user_authority()") {
		t.Fatalf("the rendered trigger definition = %q, want the schema-qualified handler this seam is about", rendered)
	}
	bound := pgQueryStrings(t, owner, `SELECT CASE WHEN p.pronargs = 0 THEN '<zero-input>'
  ELSE pg_catalog.array_to_string(ARRAY(
    SELECT pg_catalog.format_type(a, NULL) FROM pg_catalog.unnest(p.proargtypes) AS a), ', ') END
FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
WHERE t.tgname = 'core_user_authority_no_delete' AND NOT t.tgisinternal`)
	if len(bound) != 1 || bound[0] != "<zero-input>" {
		t.Fatalf("the trigger's bound handler = %q, want this build's zero-input routine: the seam is a RENDERING one", bound)
	}
	// And the guard the estate serves with is still the compiled one, by its own behaviour.
	assertPGUserAuthorityRuntimeBehaviour(t, owner)
}

// TestPostgresUserAuthorityLockCompatibilityKeepsTheReservedFenceBoundary is the control that
// detects an accidental relaxation of the SHARED projector.
//
// The event fence is the other consumer of a by-name function projection, and it is
// operator-provisioned rather than created by any migration. Both halves are asserted on the
// same fixture family: a COMPLETE canonical fence is still admitted and still boots, and the
// handler ALONE is still refused by the admission's own reserved-family judge — with this
// check's sentinel absent from that refusal, because half a fence is not a name conflict.
func TestPostgresUserAuthorityLockCompatibilityKeepsTheReservedFenceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		legs    int
		wantErr bool
	}{
		{name: "the complete canonical fence the real constructor renders", legs: -1},
		{name: "the handler alone, which is not an installation", legs: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dsns := isolatedPG(t)
			super := guardPGProbe(t, dsns.Superuser)
			app := guardPGProbe(t, dsns.App)
			installFenceStatements(t, super, tc.legs)
			dia, ok := dialect.New(store.EnginePostgres)
			if !ok {
				t.Fatal("no PostgreSQL dialect")
			}
			cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			legs, handler := countFenceObjects(t, super)
			t.Logf("FENCE_BOUNDARY|case=%s|open_error=%v|handler=%d|legs=%d", tc.name, err, handler, legs)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("an operator-provisioned complete fence refused the boot: %v", err)
				}
				if got := postgresMaxCoreVersion(t, app, dia); got != coreSupportedMigrationVersion {
					t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
				}
				return
			}
			if err == nil {
				t.Fatal("Open served a database whose reserved fence family is a proper subset")
			}
			if errors.Is(err, ErrUserAuthorityLockNameIncompatible) {
				t.Errorf("the H compatibility check claimed the fence family, which is another contract: %v", err)
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Errorf("Open error = %v, want the admission's own reserved-family refusal", err)
			}
			pgUserAuthorityZeroEffects(t, super)
			if handler != 1 || legs != 0 {
				t.Errorf("the refusal changed the operator's fence: handler=%d legs=%d, want 1 and 0", handler, legs)
			}
		})
	}
}

// sourceNativePostgresV9 builds a core v9 database with THIS REPOSITORY'S OWN constructors and
// stops there.
//
// It is source-native in the strict sense: every object comes from buildCoreMigrations and
// coreAccessEvidenceMigration, applied through the product's own migrate.Apply, with the guard
// bootstrap and the edition transition the real boot passes them. Nothing is a hand-typed DDL
// fixture, and nothing is produced by rewinding a v10 database — a subtraction fixture would be
// asserting that the removal was complete, which is a different claim from this one.
//
// The v10 predecessor conditions are the reason it is worth building: on such a database the
// boot takes the pre-v10 branch, so it does not consult verifyUserAuthorityPerBoot, and the
// compatibility refusal has to come from the preflight or not at all.
func sourceNativePostgresV9(t *testing.T) (store.Config, *sql.DB, *sql.DB, dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	cfg, owner, super, dia := userAuthorityLockPGStore(t)
	graph := accessEvidenceCoreOnlyGraph(t)
	edition5, ok := graph.node(5)
	if !ok {
		t.Fatal("the core-only graph declares no edition 5")
	}
	through8 := buildCoreMigrations(dia, coreDescriptors(),
		guardBootstrapExec(dia, edition5.Manifest), guardEditionTwoMigrationExec(dia, edition5.Manifest))
	if err := migrate.Apply(ctx, owner, dia, coreTrackingTable, through8); err != nil {
		t.Fatalf("apply the product's own core plan through v8: %v", err)
	}
	plan, err := classifyAccessEvidenceBoot(ctx, owner, dia, graph, coreDescriptors(), coreOnlyRegistry(t), nil,
		guardEventFenceFacts{})
	if err != nil {
		t.Fatalf("classify the access-evidence start class of the v8 source: %v", err)
	}
	if err := migrate.Apply(ctx, owner, dia, coreTrackingTable,
		[]migrate.Migration{coreAccessEvidenceMigration(dia, coreDescriptors(), graph, plan)}); err != nil {
		t.Fatalf("apply the product's own core v9: %v", err)
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreAccessEvidenceMigrationVersion {
		t.Fatalf("the source-native fixture recorded v%d, want v%d", got, coreAccessEvidenceMigrationVersion)
	}
	if got := pgRoutineSignatures(t, owner, "olivares_lock_core_user_authority"); len(got) != 0 {
		t.Fatalf("a v9 source already carries %d H lock routine(s): %q", len(got), got)
	}
	return cfg, owner, super, dia
}

// TestPostgresUserAuthorityLockCompatibilityOverASourceNativeV9 is the pre-v10 boundary root
// ratified, with its own positive control.
//
// The POSITIVE half is not optional: without it, the refusal below would not distinguish "the
// foreign routine was refused" from "a v9 source cannot upgrade at all". The NEGATIVE half is
// where the extension earns its place — on a v9 source there is no tracked v10, so the per-boot
// authority verifier is not consulted, and the whole estate that v1 through v9 built is what a
// late refusal would have been unable to protect.
func TestPostgresUserAuthorityLockCompatibilityOverASourceNativeV9(t *testing.T) {
	t.Run("control: a v9 source upgrades to the current supported version", func(t *testing.T) {
		ctx := context.Background()
		cfg, owner, _, dia := sourceNativePostgresV9(t)
		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("a source-native v9 refused its own intended upgrade: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
			t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
		}
		assertPGUserAuthorityRuntimeBehaviour(t, owner)
	})

	for _, tc := range foreignUserAuthorityLockOverloads {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, super, dia := sourceNativePostgresV9(t)
			mustExec(t, owner, tc.ddl)
			before := pgLogicalSnapshot(t, super, dialect.EngineSchema)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("H_LOCK_PREFLIGHT_V9|case=%s|open_error=%v", tc.name, err)
			if err == nil {
				t.Fatal("a v9 source upgraded into an H lock name it cannot resolve")
			}
			if !errors.Is(err, ErrUserAuthorityLockNameIncompatible) {
				t.Errorf("Open error = %v, want the User authority lock's own compatibility refusal", err)
			}
			// The v9 estate is intact: its migration history did not advance and core v10
			// created none of its three objects.
			if got := postgresMaxCoreVersion(t, owner, dia); got != coreAccessEvidenceMigrationVersion {
				t.Errorf("the refusal advanced core history to v%d, want v%d still",
					got, coreAccessEvidenceMigrationVersion)
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, userAuthorityDescriptor.Table) {
				t.Error("the refusal created H")
			}
			if got := pgRoutineSignatures(t, owner, "olivares_retain_user_authority"); len(got) != 0 {
				t.Errorf("the refusal created the retention routine: %q", got)
			}
			if got := pgLogicalSnapshot(t, super, dialect.EngineSchema); got != before {
				t.Errorf("the refusal changed the v9 source's durable state.\n--- before ---\n%s\n--- after ---\n%s",
					before, got)
			}
		})
	}
}

// TestPostgresUserAuthorityLockCompatibilityOverAnAlreadyV10Estate is the third source root
// named, and it is the one a blanket "H must be absent" rule would break.
//
// An already-v10 estate carries exactly one routine under that name — this build's own — and
// must keep reopening. A foreign overload introduced AFTER that install is still refused, and
// still before this boot changes anything; the per-boot authority verifier would also have
// refused it, and it is deliberately still there, downstream, unchanged.
func TestPostgresUserAuthorityLockCompatibilityOverAnAlreadyV10Estate(t *testing.T) {
	ctx := context.Background()
	cfg, owner, super, dia := userAuthorityLockPGStore(t)
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("build the current estate: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	// The sole exact entry reopens, repeatedly: the check reads the compiled identity and
	// finds it, and adopts nothing by doing so.
	for _, pass := range []string{"first reopen", "second reopen"} {
		reopened, rerr := Open(ctx, cfg, nil)
		if rerr != nil {
			t.Fatalf("%s of a v10 estate whose only H entry is this build's own: %v", pass, rerr)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	assertPGUserAuthorityRuntimeBehaviour(t, owner)

	mustExec(t, owner, foreignUserAuthorityLockOverloads[0].ddl)
	before := pgLogicalSnapshot(t, super, dialect.EngineSchema)
	refused, rerr := Open(ctx, cfg, nil)
	if refused != nil {
		_ = refused.Close()
	}
	if rerr == nil {
		t.Fatal("a v10 estate served with a second routine under its authority lock's name")
	}
	if !errors.Is(rerr, ErrUserAuthorityLockNameIncompatible) {
		t.Errorf("Open error = %v, want the User authority lock's own compatibility refusal", rerr)
	}
	if got := pgLogicalSnapshot(t, super, dialect.EngineSchema); got != before {
		t.Errorf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

// TestUserAuthorityLockCompiledIdentityIsDenyClosed covers the arm nothing else can reach: the
// derivation FAILING.
//
// It needs no server, and it is in this file because it is the same contract. If
// postgresUserAuthorityLockDDL ever grows a form the inventory's statement reader cannot name
// exactly, the check must refuse the boot rather than proceed on a guessed identity — a wrong
// expected signature would refuse the routine this build creates and admit the one it does not.
// The two inputs below are the two ways that reading can go wrong.
func TestUserAuthorityLockCompiledIdentityIsDenyClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		stmt string
	}{
		{
			name: "a routine declaration with no argument list at all",
			stmt: "CREATE FUNCTION public.olivares_lock_core_user_authority RETURNS bigint",
		},
		{
			name: "a routine declaration that is not schema-qualified",
			stmt: "CREATE FUNCTION olivares_lock_core_user_authority(target_user text) RETURNS bigint",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := compiledRoutineSchema(tc.stmt); !errors.Is(err, errManagedStatementUnreadable) {
				t.Fatalf("reading the schema of %q = %v, want the inventory's own unreadable-statement refusal",
					tc.stmt, err)
			}
		})
	}
	// And the compiled constant itself still reads, which is what makes the two refusals above
	// a boundary rather than a description of today's DDL.
	got, err := compiledUserAuthorityLockIdentity()
	if err != nil {
		t.Fatalf("the compiled User authority lock DDL is no longer readable by the inventory's statement reader: %v", err)
	}
	if got.schema != dialect.EngineSchema || got.name != "olivares_lock_core_user_authority" || got.signature != "text" {
		t.Fatalf("the compiled identity = %s, want %s.olivares_lock_core_user_authority(text)", got, dialect.EngineSchema)
	}
}

// TestPostgresUserAuthorityLockCompiledIdentityMatchesARealInstall is what stops the derived
// identity drifting away from the object the migration actually creates.
//
// compiledUserAuthorityLockIdentity reads postgresUserAuthorityLockDDL through the inventory's
// own statement reader. This test checks that reading against the CATALOG of a real install
// rather than against the constant again, which is the only comparison that is not circular: if
// the DDL ever grows a form the reader misparses, the derived signature and the installed one
// stop agreeing here.
func TestPostgresUserAuthorityLockCompiledIdentityMatchesARealInstall(t *testing.T) {
	ctx := context.Background()
	cfg, owner, _ := accessEvidencePGStore(t)
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("build the current estate: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	want, err := compiledUserAuthorityLockIdentity()
	if err != nil {
		t.Fatalf("derive the compiled H lock identity: %v", err)
	}
	installed := pgQueryStrings(t, owner, `SELECT n.nspname || '.' || p.proname || '(' ||
  COALESCE(pg_catalog.array_to_string(ARRAY(
    SELECT pg_catalog.format_type(t, NULL) FROM pg_catalog.unnest(p.proargtypes) AS t), ', '), '') || ')'
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND p.prokind = 'f'`, want.schema, want.name)
	if len(installed) != 1 || installed[0] != want.String() {
		t.Fatalf("a real core v10 install left %q under that name; the compiled identity derives %q",
			installed, want.String())
	}
}
