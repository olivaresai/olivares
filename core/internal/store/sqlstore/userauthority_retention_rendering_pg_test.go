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

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// THE SERVER-GATED HALF of the retention rendering equivalence, on a real PostgreSQL 16
// server, through the production Open.
//
// Every case here runs against an isolated database with the real role split unless it says
// otherwise, and none of them stands a SQLite fixture in for one: the subject is how a catalog
// function chooses to PRINT an OID's name, which does not exist on SQLite.
//
// WHAT THIS FILE OWES THE PURE HALF. userauthority_retention_rendering_test.go writes its whole
// matrix over two transcribed catalog strings and a mirrored framing. Neither is trustworthy on
// its own, so the first case below reads both renderings out of a real catalog through
// dialect.SchemaTriggers and compares them BYTE FOR BYTE with those constants.

// foreignRetentionZeroCallableOverloadDDL is the measured witness, with one deliberate change
// from the shape the proposal probed: its body RAISES.
//
// The overload's own body is irrelevant to the rendering — what makes the name ambiguous is
// that it is callable with zero arguments, not what it does. Making it raise a distinct marker
// converts "the foreign routine was never invoked" from an argument into an assertion: the
// retention refusals below name the compiled body's message, and a run in which the trigger had
// somehow resolved to this routine instead would say so in its own words.
const foreignRetentionZeroCallableOverloadDDL = `CREATE FUNCTION public.olivares_retain_user_authority(review text DEFAULT 'review')
RETURNS text LANGUAGE plpgsql AS $review$
BEGIN
  RAISE EXCEPTION 'FOREIGN RETENTION OVERLOAD EXECUTED: %', review;
END;
$review$`

const foreignRetentionOverloadMarker = "FOREIGN RETENTION OVERLOAD EXECUTED"

// retentionCatalogReading is the live invariant as the boot's own dialect reports it.
func retentionCatalogReading(t *testing.T, db *sql.DB) dialect.TriggerInfo {
	t.Helper()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	live, err := dia.SchemaTriggers(context.Background(), db)
	if err != nil {
		t.Fatalf("read the live schema triggers: %v", err)
	}
	info, found := live[coreRetentionKey()]
	if !found {
		t.Fatalf("the catalog carries no %v", coreRetentionKey())
	}
	return info
}

// assertRetentionBoundHandlerIsThisBuilds reads the binding rather than the printed name, so a
// case that passes cannot be passing because the rendering happened to look right.
func assertRetentionBoundHandlerIsThisBuilds(t *testing.T, db *sql.DB) {
	t.Helper()
	bound := pgQueryStrings(t, db, `SELECT n.nspname || '.' || p.proname || '(' ||
  COALESCE(pg_catalog.array_to_string(ARRAY(
    SELECT pg_catalog.format_type(a, NULL) FROM pg_catalog.unnest(p.proargtypes) AS a), ', '), '') || ')'
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE t.tgname = $1 AND NOT t.tgisinternal
  AND t.tgrelid = ($2 || '.' || $3)::pg_catalog.regclass`,
		userAuthorityRetentionTriggerName, dialect.EngineSchema, userAuthorityRetentionTable)
	want := fmt.Sprintf("%s.%s()", dialect.EngineSchema, userAuthorityRetentionFunction)
	if len(bound) != 1 || bound[0] != want {
		t.Fatalf("the trigger's bound handler = %q, want %q: the equivalence is about a RENDERING", bound, want)
	}
}

// assertRetentionRefusesAndRetains exercises BOTH statement kinds the guard names and proves
// the rows survived, which a refusal alone does not.
//
// Everything happens in ONE bound transaction, and that is not tidiness. The relation carries
// FORCE ROW LEVEL SECURITY whose policy reads `app.tenant_id` WITHOUT missing_ok — deliberately,
// so a forgotten bind RAISES instead of silently answering zero. A count taken on the pool would
// therefore either fail or, worse, succeed on a connection some earlier probe left bound and
// report one tenant's rows as all of them. Each refusal runs inside a SAVEPOINT because a failed
// statement poisons the transaction and the count afterwards is the whole point.
func assertRetentionRefusesAndRetains(t *testing.T, owner *sql.DB) {
	t.Helper()
	ctx := context.Background()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only probe
	if err := dia.BindTenant(ctx, tx, model.SystemTenantID); err != nil {
		t.Fatalf("bind the SYSTEM tenant: %v", err)
	}
	before := retentionRowCountInTx(t, tx)
	for _, stmt := range []string{
		"DELETE FROM " + dialect.EngineSchema + "." + userAuthorityRetentionTable,
		"TRUNCATE " + dialect.EngineSchema + "." + userAuthorityRetentionTable,
	} {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT retention_probe"); err != nil {
			t.Fatal(err)
		}
		_, execErr := tx.ExecContext(ctx, stmt)
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT retention_probe"); err != nil {
			t.Fatalf("roll back the probe savepoint after %q: %v", stmt, err)
		}
		if execErr == nil {
			t.Errorf("%q was allowed on the permanent authority relation", stmt)
			continue
		}
		if strings.Contains(execErr.Error(), foreignRetentionOverloadMarker) {
			t.Fatalf("%q reached the FOREIGN overload's body: %v", stmt, execErr)
		}
		if !strings.Contains(execErr.Error(), "User authority is permanent") {
			t.Errorf("%q = %v, want the compiled retention body's own refusal", stmt, execErr)
		}
	}
	if after := retentionRowCountInTx(t, tx); after != before {
		t.Errorf("the refused statements changed the visible row count: %d -> %d", before, after)
	}
	t.Logf("RETENTION_RENDERING_EFFECT|rows visible to the bound SYSTEM tenant before and after both refusals=%d", before)
}

func retentionRowCountInTx(t *testing.T, tx *sql.Tx) int {
	t.Helper()
	var n int
	if err := tx.QueryRowContext(context.Background(),
		"SELECT pg_catalog.count(*) FROM "+dialect.EngineSchema+"."+userAuthorityRetentionTable).Scan(&n); err != nil {
		t.Fatalf("count the authority relation: %v", err)
	}
	return n
}

// TestPostgresUserAuthorityRetentionRenderingPinsBothMeasuredForms is the measurement the whole
// change rests on, taken through the SAME projector the boot uses.
//
// It measures one estate in four catalog states: the canonical rendering, the rendering after a
// zero-callable overload appears, and then the two owner/ACL states that are LIMIT CONTROLS
// rather than accepted postures — their digest is unchanged, which is exactly why calling
// either digest "owner and ACL verification" would be false.
func TestPostgresUserAuthorityRetentionRenderingPinsBothMeasuredForms(t *testing.T) {
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

	canonical := retentionCatalogReading(t, owner)
	if want := framedRetentionDefinition(measuredRetentionTriggerCanonical, measuredRetentionFunctionDef); canonical.Definition != want {
		t.Fatalf("the live canonical framed definition is not the transcribed one.\n--- live ---\n%q\n--- transcribed ---\n%q",
			canonical.Definition, want)
	}
	if got := digestOf(canonical.Definition); got != postgresUserAuthorityRetentionDigest {
		t.Fatalf("the live canonical digest = %s, and the invariant declares %s", got, postgresUserAuthorityRetentionDigest)
	}
	if canonical.FunctionSchema != dialect.EngineSchema || canonical.FunctionName != userAuthorityRetentionFunction {
		t.Fatalf("the canonical reading binds %s.%s, want %s.%s",
			canonical.FunctionSchema, canonical.FunctionName, dialect.EngineSchema, userAuthorityRetentionFunction)
	}
	if canonical.EnableState != dialect.TriggerFiresAlways {
		t.Errorf("the installed retention trigger is in state %q, want ENABLE ALWAYS", canonical.EnableState)
	}
	assertRetentionBoundHandlerIsThisBuilds(t, owner)
	handlerOIDBefore := pgQueryStrings(t, owner, `SELECT t.tgfoid::pg_catalog.text
FROM pg_catalog.pg_trigger t WHERE t.tgname = $1 AND NOT t.tgisinternal`, userAuthorityRetentionTriggerName)

	mustExec(t, owner, foreignRetentionZeroCallableOverloadDDL)

	qualified := retentionCatalogReading(t, owner)
	if want := framedRetentionDefinition(measuredRetentionTriggerQualified, measuredRetentionFunctionDef); qualified.Definition != want {
		t.Fatalf("the live qualified framed definition is not the transcribed one.\n--- live ---\n%q\n--- transcribed ---\n%q",
			qualified.Definition, want)
	}
	if got := digestOf(qualified.Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Fatalf("the live qualified digest = %s, and the companion constant is %s",
			got, postgresRetentionMeasuredRevisionQualifiedDigest)
	}
	if qualified.FunctionSchema != canonical.FunctionSchema || qualified.FunctionName != canonical.FunctionName {
		t.Fatalf("the structural handler identity moved with the rendering: %s.%s -> %s.%s",
			canonical.FunctionSchema, canonical.FunctionName, qualified.FunctionSchema, qualified.FunctionName)
	}
	handlerOIDAfter := pgQueryStrings(t, owner, `SELECT t.tgfoid::pg_catalog.text
FROM pg_catalog.pg_trigger t WHERE t.tgname = $1 AND NOT t.tgisinternal`, userAuthorityRetentionTriggerName)
	if len(handlerOIDBefore) != 1 || len(handlerOIDAfter) != 1 || handlerOIDBefore[0] != handlerOIDAfter[0] {
		t.Fatalf("the bound handler OID moved: %q -> %q. This would be a different finding entirely",
			handlerOIDBefore, handlerOIDAfter)
	}
	assertRetentionBoundHandlerIsThisBuilds(t, owner)

	// The comparator, fed the REAL readings rather than a fixture.
	for _, tc := range []struct {
		name string
		info dialect.TriggerInfo
	}{{"canonical", canonical}, {"qualified", qualified}} {
		if !schemaInvariantDefinitionMatches(store.EnginePostgres, coreRetentionKey(),
			coreRetentionInvariant(), tc.info, digestOf(tc.info.Definition)) {
			t.Errorf("the comparator refused the live %s reading", tc.name)
		}
	}

	// THE TWO LIMIT CONTROLS. pg_get_functiondef renders neither owner nor ACL, so both of
	// these leave the digest exactly where it was. They are recorded as the boundary of what
	// the pair proves, not as postures this build accepts.
	mustExec(t, super, "GRANT EXECUTE ON FUNCTION "+dialect.EngineSchema+"."+userAuthorityRetentionFunction+"() TO PUBLIC")
	if got := digestOf(retentionCatalogReading(t, owner).Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Errorf("a PUBLIC grant changed the complete digest to %s: the limit recorded in the report is wrong", got)
	}
	mustExec(t, super, "ALTER FUNCTION "+dialect.EngineSchema+"."+userAuthorityRetentionFunction+"() OWNER TO "+dialect.DefaultAppRole)
	if got := digestOf(retentionCatalogReading(t, owner).Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Errorf("an owner change changed the complete digest to %s: the limit recorded in the report is wrong", got)
	}
	t.Logf("RETENTION_RENDERING_LIMIT|owner and ACL are not encoded in either digest; "+
		"the independent EXECUTE probe and the restore ceremony's inventory helpers remain their only verification|digest=%s",
		postgresRetentionMeasuredRevisionQualifiedDigest)
}

// retentionOverloadPosture builds a fixture in one of the two role postures this product
// supports, with the zero-callable overload ALREADY in the database.
//
// Creating the overload before the first Open is the point: this is the fresh, max0 path, where
// core v10 creates its own routine and trigger beside a foreign name that is already ambiguous.
func retentionOverloadPosture(t *testing.T, posture string) (store.Config, *sql.DB, *sql.DB, dialect.Dialect) {
	t.Helper()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	var dsns pgtest.DSNs
	var cfg store.Config
	if posture == "single role" {
		dsns = isolatedPG(t)
		cfg = store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
	} else {
		dsns = isolatedPGSplit(t)
		cfg = store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4}
	}
	owner := guardPGProbe(t, dsns.Owner)
	super := guardPGProbe(t, dsns.Superuser)
	mustExec(t, owner, foreignRetentionZeroCallableOverloadDDL)
	return cfg, owner, super, dia
}

// TestPostgresUserAuthorityRetentionZeroCallableOverloadCompletesAnOrdinaryOpen is the positive
// this change exists for, and it is the case that was RED before the comparator was corrected.
//
// A refusal here was never a claim about the trigger: the binding is this build's own zero-input
// routine both before and after the overload appears. What refused was an exact digest
// comparison against one of the two complete definitions PostgreSQL renders for it.
//
// The assertions are deliberately about the ESTATE and the BEHAVIOUR rather than about Open
// returning nil: the boot reaches the supported version, leaves exactly this build's routine beside the foreign
// one, does not touch the foreign routine, serves its own SYSTEM scope, and still refuses a
// DELETE and a TRUNCATE with the compiled body's own message.
func TestPostgresUserAuthorityRetentionZeroCallableOverloadCompletesAnOrdinaryOpen(t *testing.T) {
	for _, posture := range []string{"split owner and application roles", "single role"} {
		t.Run(posture, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, super, dia := retentionOverloadPosture(t, posture)
			foreignBefore := pgUserAuthorityRoutineSnapshot(t, super, dialect.EngineSchema, userAuthorityRetentionFunction)
			if len(foreignBefore) != 1 {
				t.Fatalf("the fixture left %d retention routines, want exactly the foreign one", len(foreignBefore))
			}

			for _, pass := range []string{"first Open", "reopen"} {
				st, err := Open(ctx, cfg, nil)
				if err != nil {
					t.Fatalf("%s: a zero-callable retention overload refused an ordinary boot: %v", pass, err)
				}
				// A store that opens but cannot serve its own system scope has not
				// proved the boot completed.
				if err := st.System(ctx, func(sys store.SystemScope) error {
					if _, e := sys.EnsureSystemTenant(ctx); e != nil {
						return e
					}
					return sys.EnsureDefaultWorkspaces(ctx)
				}); err != nil {
					t.Fatalf("%s: bootstrap the SYSTEM tenant: %v", pass, err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			}

			if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
				t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
			}
			if got := pgRoutineSignatures(t, owner, userAuthorityRetentionFunction); len(got) != 2 ||
				got[0] != "" || got[1] != "text" {
				t.Fatalf("retention signatures = %q, want the foreign (text) overload beside this build's zero-input routine", got)
			}
			after := pgUserAuthorityRoutineSnapshot(t, super, dialect.EngineSchema, userAuthorityRetentionFunction)
			var preserved bool
			for _, row := range after {
				if row == foreignBefore[0] {
					preserved = true
				}
			}
			if !preserved {
				t.Fatalf("the boot changed the foreign retention routine.\n--- before ---\n%v\n--- after ---\n%v",
					foreignBefore, after)
			}
			// The rendering really is the qualified one: without this the case could be
			// green because the overload failed to make the name ambiguous.
			if got := digestOf(retentionCatalogReading(t, owner).Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
				t.Fatalf("the live digest = %s, want the qualified rendering this case is about", got)
			}
			assertRetentionBoundHandlerIsThisBuilds(t, owner)
			assertPGUserAuthorityRuntimeBehaviour(t, owner)
			assertRetentionRefusesAndRetains(t, owner)
		})
	}
}

// TestPostgresUserAuthorityRetentionZeroCallableOverloadCompletesTheSchemaOnlyPhase covers the
// other entry point into the same preparation: migrate as the owner, serve later as the app.
func TestPostgresUserAuthorityRetentionZeroCallableOverloadCompletesTheSchemaOnlyPhase(t *testing.T) {
	ctx := context.Background()
	cfg, owner, _, dia := retentionOverloadPosture(t, "split owner and application roles")
	if err := ApplyMigrations(ctx, cfg, nil); err != nil {
		t.Fatalf("the schema-only phase refused a database whose retention name is merely ambiguous: %v", err)
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("the schema-only phase reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	if got := digestOf(retentionCatalogReading(t, owner).Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Fatalf("the live digest = %s, want the qualified rendering this case is about", got)
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("serving the schema this phase applied: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	assertRetentionRefusesAndRetains(t, owner)
}

// TestPostgresUserAuthorityRetentionZeroCallableOverloadOnAnAlreadyV10Estate is the upgrade-free
// path most existing installations would take: the overload appears in a database that is
// already at v10 and the binary is asked to reopen it.
func TestPostgresUserAuthorityRetentionZeroCallableOverloadOnAnAlreadyV10Estate(t *testing.T) {
	ctx := context.Background()
	cfg, owner, _, dia := userAuthorityLockPGStore(t)
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
	mustExec(t, owner, foreignRetentionZeroCallableOverloadDDL)
	for _, pass := range []string{"first reopen", "second reopen"} {
		reopened, rerr := Open(ctx, cfg, nil)
		if rerr != nil {
			t.Fatalf("%s of a v10 estate whose retention name became ambiguous: %v", pass, rerr)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	assertRetentionBoundHandlerIsThisBuilds(t, owner)
	assertPGUserAuthorityRuntimeBehaviour(t, owner)
	assertRetentionRefusesAndRetains(t, owner)
}

// TestPostgresUserAuthorityRetentionZeroCallableOverloadOverACurrentConstructorV9 exercises the
// pre-v10 upgrade path with the overload already present.
//
// LABEL, because root's direction is explicit about it: this fixture is built by THIS
// build's own migration constructors through migrate.Apply, stopping at v9. It is NOT a
// historical binary, and nothing here attributes anything to one.
func TestPostgresUserAuthorityRetentionZeroCallableOverloadOverACurrentConstructorV9(t *testing.T) {
	ctx := context.Background()
	cfg, owner, _, dia := sourceNativePostgresV9(t)
	mustExec(t, owner, foreignRetentionZeroCallableOverloadDDL)

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("a current-constructor v9 source refused its own upgrade over an ambiguous retention name: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	if got := digestOf(retentionCatalogReading(t, owner).Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Fatalf("the live digest = %s, want the qualified rendering this case is about", got)
	}
	assertRetentionBoundHandlerIsThisBuilds(t, owner)
	assertRetentionRefusesAndRetains(t, owner)
}

// relationContractRefusal is the text of the typed relation oracle's own refusal.
//
// It is matched as a substring rather than as a sentinel because that comparison returns a
// plain formatted error today. The string is asserted, not merely logged, because MEASURING
// this file taught the thing worth writing down: for almost every structural mutation below,
// the descriptor-rendered probe refuses FIRST and the digest comparator is never reached.
const relationContractRefusal = "triggers differ from descriptor-rendered probe"

// TestPostgresUserAuthorityRetentionDefinitionNegativesRefuseInsideTheAcceptedEnvironment is the
// closure half, and every case runs WITH the zero-callable overload present.
//
// That is what makes it more than a repeat of the old rejection: each mutation is measured in
// the very environment the alternative rendering makes acceptable, so a comparator that had
// widened into "any qualified rendering passes" would be caught here rather than in review.
//
// EACH CASE PINS THE BOUNDARY THAT ACTUALLY REFUSED, and the distribution is a finding rather
// than bookkeeping. On an existing v10 estate the typed relation oracle
// (verifyCoreDirectoryRelationContract) compares the whole relation against a probe built from
// the descriptor IN THE SAME CATALOG, so it sees a moved handler, an added trigger argument,
// AFTER timing, WHEN (false), and an absent trigger before the invariant self-test runs at all.
// It is unaffected by this change precisely because probe and actual are rendered under the same
// ambiguity, which is why its `want` text below carries the qualifier too. What reaches the
// digest comparator is the one mutation the probe cannot see: the bound function's BODY.
//
// So that the closure claim does not rest on whichever layer happens to be first, every case
// whose definition changed ALSO asserts the pure comparator refuses the live catalog reading.
func TestPostgresUserAuthorityRetentionDefinitionNegativesRefuseInsideTheAcceptedEnvironment(t *testing.T) {
	const sameBody = `RETURNS trigger LANGUAGE plpgsql VOLATILE SECURITY INVOKER
SET search_path = pg_catalog AS $retention$
BEGIN
  RAISE EXCEPTION 'User authority is permanent';
END;
$retention$`
	recreate := func(clause string) []string {
		return []string{
			"DROP TRIGGER " + userAuthorityRetentionTriggerName + " ON " + dialect.EngineSchema + "." + userAuthorityRetentionTable,
			"CREATE TRIGGER " + userAuthorityRetentionTriggerName + " " + clause,
			"ALTER TABLE ONLY " + dialect.EngineSchema + "." + userAuthorityRetentionTable +
				" ENABLE ALWAYS TRIGGER " + userAuthorityRetentionTriggerName,
		}
	}
	for _, tc := range []struct {
		name string
		// stmts is the mutation, applied to a healthy v10 estate that already carries the
		// zero-callable overload.
		stmts []string
		// wantErr is the typed refusal, when the boundary that fires has one.
		wantErr error
		// wantText is the untyped relation oracle's own refusal, when IT is what fires.
		wantText string
		// comparatorMustRefuse asserts the definition comparator's verdict on the LIVE
		// reading independently of which boundary reported first. It is false only where
		// the complete definition is genuinely unchanged or the trigger is gone.
		comparatorMustRefuse bool
	}{
		{
			name: "a same-body handler in another schema",
			stmts: append([]string{
				"CREATE SCHEMA review_wrong",
				"CREATE FUNCTION review_wrong." + userAuthorityRetentionFunction + "() " + sameBody,
			}, recreate("BEFORE DELETE OR TRUNCATE ON "+dialect.EngineSchema+"."+userAuthorityRetentionTable+
				" FOR EACH STATEMENT EXECUTE FUNCTION review_wrong."+userAuthorityRetentionFunction+"()")...),
			wantText:             relationContractRefusal,
			comparatorMustRefuse: true,
		},
		{
			name: "a same-body handler under another name",
			stmts: append([]string{
				"CREATE FUNCTION " + dialect.EngineSchema + ".review_wrong_handler() " + sameBody,
			}, recreate("BEFORE DELETE OR TRUNCATE ON "+dialect.EngineSchema+"."+userAuthorityRetentionTable+
				" FOR EACH STATEMENT EXECUTE FUNCTION "+dialect.EngineSchema+".review_wrong_handler()")...),
			wantText:             relationContractRefusal,
			comparatorMustRefuse: true,
		},
		{
			name: "the bound handler's body replaced with a no-op",
			stmts: []string{
				"CREATE OR REPLACE FUNCTION " + dialect.EngineSchema + "." + userAuthorityRetentionFunction + `()
RETURNS trigger LANGUAGE plpgsql VOLATILE SECURITY INVOKER
SET search_path = pg_catalog AS $retention$
BEGIN
  RETURN NULL;
END;
$retention$`,
			},
			// The ONLY case the relation oracle cannot see: the trigger's own rendering is
			// byte-identical and the replacement lives in the function it invokes.
			wantErr:              store.ErrSchemaTriggerTampered,
			comparatorMustRefuse: true,
		},
		{
			name: "an added trigger argument",
			stmts: recreate("BEFORE DELETE OR TRUNCATE ON " + dialect.EngineSchema + "." + userAuthorityRetentionTable +
				" FOR EACH STATEMENT EXECUTE FUNCTION " + dialect.EngineSchema + "." + userAuthorityRetentionFunction + "('public.extra')"),
			wantText:             relationContractRefusal,
			comparatorMustRefuse: true,
		},
		{
			name: "AFTER timing, which cannot refuse the statement it observes",
			stmts: recreate("AFTER DELETE OR TRUNCATE ON " + dialect.EngineSchema + "." + userAuthorityRetentionTable +
				" FOR EACH STATEMENT EXECUTE FUNCTION " + dialect.EngineSchema + "." + userAuthorityRetentionFunction + "()"),
			wantText:             relationContractRefusal,
			comparatorMustRefuse: true,
		},
		{
			name: "WHEN (false), which never fires",
			stmts: recreate("BEFORE DELETE OR TRUNCATE ON " + dialect.EngineSchema + "." + userAuthorityRetentionTable +
				" FOR EACH STATEMENT WHEN (false) EXECUTE FUNCTION " + dialect.EngineSchema + "." + userAuthorityRetentionFunction + "()"),
			wantText:             relationContractRefusal,
			comparatorMustRefuse: true,
		},
		{
			name: "the trigger removed entirely",
			stmts: []string{
				"DROP TRIGGER " + userAuthorityRetentionTriggerName + " ON " + dialect.EngineSchema + "." + userAuthorityRetentionTable,
			},
			wantText: relationContractRefusal,
		},
		{
			name: "the same name attached to another table",
			stmts: []string{
				"DROP TRIGGER " + userAuthorityRetentionTriggerName + " ON " + dialect.EngineSchema + "." + userAuthorityRetentionTable,
				"CREATE TABLE " + dialect.EngineSchema + ".review_other(id text PRIMARY KEY)",
				"CREATE TRIGGER " + userAuthorityRetentionTriggerName + " BEFORE DELETE OR TRUNCATE ON " +
					dialect.EngineSchema + ".review_other FOR EACH STATEMENT EXECUTE FUNCTION " +
					dialect.EngineSchema + "." + userAuthorityRetentionFunction + "()",
			},
			wantText: relationContractRefusal,
		},
		{
			// Enable state is not rendered into any deparsed text, so the relation oracle
			// is blind to it and the invariant self-test is the boundary that answers.
			name: "the guard DISABLED",
			stmts: []string{
				"ALTER TABLE " + dialect.EngineSchema + "." + userAuthorityRetentionTable +
					" DISABLE TRIGGER " + userAuthorityRetentionTriggerName,
			},
			wantErr: store.ErrSchemaTriggerInert,
		},
		{
			name: "the guard left firing only for replicas",
			stmts: []string{
				"ALTER TABLE " + dialect.EngineSchema + "." + userAuthorityRetentionTable +
					" ENABLE REPLICA TRIGGER " + userAuthorityRetentionTriggerName,
			},
			wantErr: store.ErrSchemaTriggerInert,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, super, _ := userAuthorityLockPGStore(t)
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("build the current estate: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			mustExec(t, owner, foreignRetentionZeroCallableOverloadDDL)
			// The control on this very fixture: with the overload and NOTHING else
			// changed, the estate reopens. Without it, a refusal below would not
			// distinguish the mutation from the overload.
			control, cerr := Open(ctx, cfg, nil)
			if cerr != nil {
				t.Fatalf("the accepted qualification environment itself refused to reopen: %v", cerr)
			}
			if err := control.Close(); err != nil {
				t.Fatal(err)
			}
			for _, stmt := range tc.stmts {
				mustExec(t, super, stmt)
			}

			refused, rerr := Open(ctx, cfg, nil)
			if refused != nil {
				_ = refused.Close()
			}
			t.Logf("RETENTION_RENDERING_NEGATIVE|case=%s|open_error=%v", tc.name, rerr)
			if rerr == nil {
				t.Fatal("the boot accepted a replaced retention boundary inside the accepted qualification environment")
			}
			if tc.wantErr != nil && !errors.Is(rerr, tc.wantErr) {
				t.Fatalf("Open error = %v, want %v", rerr, tc.wantErr)
			}
			if tc.wantText != "" && !strings.Contains(rerr.Error(), tc.wantText) {
				t.Fatalf("Open error = %v, want the boundary that names %q", rerr, tc.wantText)
			}
			if tc.comparatorMustRefuse {
				live := retentionCatalogReading(t, owner)
				if schemaInvariantDefinitionMatches(store.EnginePostgres, coreRetentionKey(),
					coreRetentionInvariant(), live, digestOf(live.Definition)) {
					t.Fatalf("the definition comparator ACCEPTED the mutated catalog reading (digest %s): "+
						"the closure claim would rest entirely on the relation oracle", digestOf(live.Definition))
				}
			}
		})
	}
}

// TestPostgresUserAuthorityRetentionOverloadDoesNotMaskAnUnexecutableHandler keeps the EXECUTE
// probe honest in the environment the equivalence makes acceptable.
//
// The definition digest says nothing about privileges, and this is where that is measured
// rather than assumed: the complete definition is one of the two accepted forms, and the boot
// still refuses because the application role cannot run the function the guard invokes. The
// PUBLIC grant is revoked first, because leaving it would let PUBLIC's EXECUTE mask the
// revocation and turn this case green for the wrong reason.
func TestPostgresUserAuthorityRetentionOverloadDoesNotMaskAnUnexecutableHandler(t *testing.T) {
	ctx := context.Background()
	cfg, owner, super, _ := userAuthorityLockPGStore(t)
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("build the current estate: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	mustExec(t, owner, foreignRetentionZeroCallableOverloadDDL)
	qualified := retentionCatalogReading(t, owner)
	if got := digestOf(qualified.Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Fatalf("the live digest = %s, want the qualified rendering this case is about", got)
	}
	if !qualified.CanExecute {
		t.Fatal("the application role could not EXECUTE the handler before the revoke: the fixture proves nothing")
	}

	fn := dialect.EngineSchema + "." + userAuthorityRetentionFunction + "()"
	mustExec(t, super, "REVOKE EXECUTE ON FUNCTION "+fn+" FROM PUBLIC")
	mustExec(t, super, "REVOKE EXECUTE ON FUNCTION "+fn+" FROM "+dialect.DefaultAppRole)
	if after := retentionCatalogReading(t, owner); after.CanExecute {
		t.Fatal("the revoke did not take: PUBLIC or a group role still carries EXECUTE")
	} else if got := digestOf(after.Definition); got != postgresRetentionMeasuredRevisionQualifiedDigest {
		t.Fatalf("revoking EXECUTE changed the complete digest to %s: it is not supposed to encode privileges", got)
	}

	refused, rerr := Open(ctx, cfg, nil)
	if refused != nil {
		_ = refused.Close()
	}
	t.Logf("RETENTION_RENDERING_EXECUTE|open_error=%v", rerr)
	if rerr == nil {
		t.Fatal("the boot served a boundary whose guard the application role cannot execute")
	}
	if !errors.Is(rerr, store.ErrSchemaTriggerUnexecutable) {
		t.Fatalf("Open error = %v, want the self-test's own unexecutable-trigger refusal", rerr)
	}
}
