// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// guardeventfence_pg_test.go measures the DENY-CLOSED EVENT-TRIGGER FENCE against real
// servers, on all four certified majors.
//
// It is a different object from the `fenceSharedFunctionStatement` of F-07
// (guardcoordinator.go:477), which takes a row lock on pg_proc for the duration of an
// attempt. This one is the thing that acts BETWEEN two boots, in the session of whoever
// tries the DDL, with nobody watching.
//
// THE MUTATIONS THAT MUST TURN THIS RED, and what each of them proves:
//
//  1. Delete the two `ALTER EVENT TRIGGER … ENABLE ALWAYS` statements from
//     dialect.GuardEventFenceStmts. Measured consequence: the fence installs at
//     evtenabled='O', and 'O' does not fire for a session that has set
//     session_replication_role='replica' — a setting an ordinary role can be granted on 15,
//     16, 17 and 18 alike. Red in `the fence installed by this build is the canonical one`
//     AND in TestPostgresAnOriginEventFenceDoesNotFireUnderReplica.
//  2. Drop the handler's 28-field comparison from judgeGuardEventFence, keeping only the
//     event-trigger rows. Red ONLY in `a rewritten handler is divergent…` — which is the
//     whole point of that case: pg_event_trigger is byte-identical before and after the
//     attack, and the case asserts that identity explicitly so the mutation cannot hide
//     behind it.
//  3. Report `absent` instead of `divergent` when one leg is missing. Red in
//     `half a fence is divergent, not absent`.
//  4. Delete the AppMayRewrite branch of judgeGuardEventFence. Red in `a handler the
//     application role can rewrite is divergent` and in nothing else, because in that case
//     the body is still canonical.
//
// WHAT THIS FILE DOES NOT CLAIM. It never asserts the fence resists a superuser: measured on
// all four majors, `DROP FUNCTION <handler> CASCADE` removes it, because PostgreSQL exempts
// DDL that targets event triggers from firing event triggers. That limit is stated rather than
// tested away — and the ONLY thing that notices it is the next boot. There is no runtime probe:
// this comment used to say the limit "is why the periodic probe exists", and no such probe was
// ever built.

// eventFenceFixture is one prepared database: a guarded ledger, the shared block-mutation
// function, an application role, and — unless the case says otherwise — the fence itself.
type eventFenceFixture struct {
	db      *sql.DB
	appRole string
	major   int
}

// installEventFenceFixture builds the substrate every case starts from, and ASSERTS it.
//
// The hand-run measurements that informed this design produced, on their first attempt, ten
// confident "REFUSED" rows from a database whose fixture had aborted on its first statement:
// every refusal was an empty database refusing nothing. A fixture that is not asserted is a
// fixture that can silently not exist, and the table it produces reads exactly like a
// measurement.
func installEventFenceFixture(t *testing.T, db *sql.DB, major int, withFence bool) eventFenceFixture {
	t.Helper()
	ctx := context.Background()
	appRole := fmt.Sprintf("olv_evfence_app_%d_%s", major, matrixScope())

	stmts := []string{
		`DROP ROLE IF EXISTS "` + appRole + `"`,
		`CREATE ROLE "` + appRole + `" LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'fence'`,
		`CREATE FUNCTION ` + dialect.BlockMutationFn + `() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'table is append-only'; END $fn$`,
		`CREATE TABLE fence_ledger (id integer PRIMARY KEY)`,
		`CREATE TRIGGER fence_ledger_immutable BEFORE UPDATE OR DELETE ON fence_ledger FOR EACH ROW EXECUTE FUNCTION ` + dialect.BlockMutationFn + `()`,
		`ALTER TABLE fence_ledger ENABLE ALWAYS TRIGGER fence_ledger_immutable`,
		`GRANT USAGE ON SCHEMA ` + dialect.EngineSchema + ` TO "` + appRole + `"`,
	}
	if withFence {
		stmts = append(stmts, dialect.GuardEventFenceStmts()...)
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("prepare the event-fence fixture on major %d: %v\nstatement: %s", major, err, s)
		}
	}
	// DROP OWNED FIRST, and the error is REPORTED.
	//
	// This cleanup is registered AFTER the scratch database's, so LIFO runs it FIRST — while
	// the database, and the GRANT above, still exist. A bare DROP ROLE then fails on the
	// dependency, and discarding that error is what let this fixture leak two roles per major
	// on every run: measured, eight roles from one green suite. DROP OWNED removes the grants
	// that create the dependency, so the role can go before its database does. CASCADE because the
	// event trigger depends on the handler the role owns, and this database is scratch — it is
	// dropped moments later by the same chain of cleanups.
	t.Cleanup(func() {
		bg := context.Background()
		if _, err := db.ExecContext(bg, `DROP OWNED BY "`+appRole+`" CASCADE`); err != nil {
			t.Errorf("could not release what %q owns before dropping it: %v", appRole, err)
		}
		if _, err := db.ExecContext(bg, `DROP ROLE IF EXISTS "`+appRole+`"`); err != nil {
			t.Errorf("LEAKED cluster-scoped role %q: %v", appRole, err)
		}
	})

	var legs int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_catalog.pg_event_trigger WHERE evtname IN ($1, $2)`,
		dialect.GuardEventFenceDropTrigger, dialect.GuardEventFenceEndTrigger).Scan(&legs); err != nil {
		t.Fatalf("count the fence's legs on major %d: %v", major, err)
	}
	want := 0
	if withFence {
		want = 2
	}
	if legs != want {
		t.Fatalf("the fixture on major %d carries %d fence leg(s) and this case needs %d: every assertion below would be measuring a database that is not the one this test describes",
			major, legs, want)
	}
	return eventFenceFixture{db: db, appRole: appRole, major: major}
}

// eventFenceStatusOf projects and judges through the SAME functions boot calls.
func eventFenceStatusOf(t *testing.T, f eventFenceFixture) guardEventFenceStatus {
	t.Helper()
	obs, err := projectGuardEventFence(context.Background(), f.db, f.appRole, f.major)
	if err != nil {
		t.Fatalf("project the event fence on major %d: %v", f.major, err)
	}
	return judgeGuardEventFence(obs)
}

// eventTriggerFingerprint is everything a projection that looked ONLY at pg_event_trigger
// would see. The rewritten-handler case compares it before and after the attack.
func eventTriggerFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT e.evtname || ' evt=' || e.evtevent || ' state=' || e.evtenabled::text || ' fn=' || p.proname
     FROM pg_catalog.pg_event_trigger e JOIN pg_catalog.pg_proc p ON p.oid = e.evtfoid
    ORDER BY e.evtname`)
	if err != nil {
		t.Fatalf("fingerprint pg_event_trigger: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan the fingerprint: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("fingerprint pg_event_trigger: %v", err)
	}
	return strings.Join(out, " | ")
}

func eventFenceMustFail(t *testing.T, db *sql.DB, what, stmt string) string {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		return err.Error()
	}
	t.Fatalf("%s was ACCEPTED; the fence exists precisely to refuse it", what)
	return ""
}

func eventFenceMustSucceed(t *testing.T, db *sql.DB, what, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("%s was refused and must not be: %v", what, err)
	}
}

// eventFenceServer opens a scratch database on one major and proves the server it reaches IS
// that major before anything is measured on it.
func eventFenceServer(t *testing.T, m majorDSN, name string) *sql.DB {
	t.Helper()
	db := scratchDatabaseOn(t, m, name)
	serverMajor, err := postgresServerMajor(context.Background(), db)
	if err != nil {
		t.Fatalf("read the server major: %v", err)
	}
	if serverMajor != m.Major {
		t.Fatalf("the DSN labeled major %d reports %d; this case would be certifying the wrong server", m.Major, serverMajor)
	}
	return db
}

type eventFenceBehaviorCase struct {
	name          string
	stmt          string
	wantSubstring string
}

// eventFenceBehaviourCaseTable is a WRITTEN-DOWN literal, and the count below is written down
// too, never len(table): an expectation derived from the table it checks shrinks with it,
// which is how three quarters of a matrix once passed in green.
func eventFenceBehaviorCaseTable() []eventFenceBehaviorCase {
	return []eventFenceBehaviorCase{
		{"drop the guard outright", `DROP TRIGGER fence_ledger_immutable ON fence_ledger`, "refusing to drop the append-only guard"},
		{"drop the guarded relation, taking the guard by cascade", `DROP TABLE fence_ledger`, "refusing to drop the append-only guard"},
		{"disable the guard", `ALTER TABLE fence_ledger DISABLE TRIGGER fence_ledger_immutable`, "disabled or replica-only"},
		{"leave the guard replica-only", `ALTER TABLE fence_ledger ENABLE REPLICA TRIGGER fence_ledger_immutable`, "disabled or replica-only"},
	}
}

func eventFenceCaseNames(names []string) []string { return names }

// TestPostgresTheDenyClosedEventFenceRefusesWhatItExistsToRefuse is the behavioral half.
//
// The cascade case earns its place: `DROP TABLE` never names the trigger, it takes it by
// dependency — and measured, pg_event_trigger_dropped_objects() reports the trigger as a row
// of its own, which is why one rule about dropped triggers covers both shapes.
func TestPostgresTheDenyClosedEventFenceRefusesWhatItExistsToRefuse(t *testing.T) {
	table := eventFenceBehaviorCaseTable()
	if len(table) != 4 {
		t.Fatalf("this test expects 4 behavior vectors and the table carries %d: a vector added without updating the expectation is uncovered, and one removed without updating it is silently gone", len(table))
	}
	names := make([]string, 0, len(table))
	for _, c := range table {
		names = append(names, c.name)
	}
	coverage := newMajorCaseCoverage(eventFenceCaseNames(names))
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			for i, c := range table {
				t.Run(c.name, func(t *testing.T) {
					db := eventFenceServer(t, m, fmt.Sprintf("olv_evf_b%d_%d_%s", i, m.Major, matrixScope()))
					f := installEventFenceFixture(t, db, m.Major, true)
					msg := eventFenceMustFail(t, f.db, c.name, c.stmt)
					if !strings.Contains(msg, c.wantSubstring) {
						t.Fatalf("the refusal must name what it refused.\n  want substring: %q\n  got: %s", c.wantSubstring, msg)
					}
					coverage.markCase(fmt.Sprintf("%d/%s", m.Major, c.name))
				})
			}
		})
	}
}

type eventFenceProjectionCase struct {
	name string
	run  func(t *testing.T, f eventFenceFixture)
}

func requireEventFenceDivergentNaming(t *testing.T, status guardEventFenceStatus, want string) {
	t.Helper()
	if status.Verdict != guardEventFenceDivergent {
		t.Fatalf("verdict: want %s, got %s (reasons: %v)", guardEventFenceDivergent, status.Verdict, status.Reasons)
	}
	joined := strings.Join(status.Reasons, "; ")
	if !strings.Contains(joined, want) {
		t.Fatalf("the verdict must NAME what diverged.\n  want substring: %q\n  reasons: %s", want, joined)
	}
}

func eventFenceProjectionCaseTable() []eventFenceProjectionCase {
	return []eventFenceProjectionCase{
		{
			name: "the fence installed by this build is the canonical one",
			run: func(t *testing.T, f eventFenceFixture) {
				status := eventFenceStatusOf(t, f)
				if status.Verdict != guardEventFenceInstalled {
					t.Fatalf("this build's own DDL must satisfy this build's own verifier.\n  verdict: %s\n  reasons: %v", status.Verdict, status.Reasons)
				}
			},
		},
		{
			name: "a leg left at ORIGIN is divergent",
			run: func(t *testing.T, f eventFenceFixture) {
				eventFenceMustSucceed(t, f.db, "downgrade one leg to ORIGIN",
					`ALTER EVENT TRIGGER `+dialect.GuardEventFenceDropTrigger+` ENABLE`)
				requireEventFenceDivergentNaming(t, eventFenceStatusOf(t, f), `evtenabled is "O"`)
			},
		},
		{
			name: "half a fence is divergent, not absent",
			run: func(t *testing.T, f eventFenceFixture) {
				eventFenceMustSucceed(t, f.db, "remove one leg",
					`DROP EVENT TRIGGER `+dialect.GuardEventFenceEndTrigger)
				status := eventFenceStatusOf(t, f)
				if status.Verdict == guardEventFenceAbsent {
					t.Fatal("one leg standing and the other gone is not 'never installed': nobody installs half a fence")
				}
				requireEventFenceDivergentNaming(t, status, "the event trigger is missing while the rest of the fence stands")
			},
		},
		{
			name: "a rewritten handler is divergent even though pg_event_trigger is unchanged",
			run: func(t *testing.T, f eventFenceFixture) {
				before := eventTriggerFingerprint(t, f.db)
				eventFenceMustSucceed(t, f.db, "rewrite the handler into a no-op",
					`CREATE OR REPLACE FUNCTION `+dialect.EngineSchema+`.`+dialect.GuardEventFenceHandlerFn+
						`() RETURNS event_trigger LANGUAGE plpgsql AS $x$ BEGIN END $x$`)
				after := eventTriggerFingerprint(t, f.db)
				if before != after {
					t.Fatalf("this case's premise is that the event-trigger rows do NOT change; they did:\n  before: %s\n  after:  %s", before, after)
				}
				// The attack really works: with the body replaced, the fence refuses nothing.
				eventFenceMustSucceed(t, f.db, "drop the guard through the neutralized fence",
					`DROP TRIGGER fence_ledger_immutable ON fence_ledger`)
				requireEventFenceDivergentNaming(t, eventFenceStatusOf(t, f), "prosrc")
			},
		},
		{
			name: "a handler the application role can rewrite is divergent",
			run: func(t *testing.T, f eventFenceFixture) {
				eventFenceMustSucceed(t, f.db, "hand the handler to the application role",
					`ALTER FUNCTION `+dialect.EngineSchema+`.`+dialect.GuardEventFenceHandlerFn+`() OWNER TO "`+f.appRole+`"`)
				requireEventFenceDivergentNaming(t, eventFenceStatusOf(t, f), "which the application role owns or can become")
			},
		},
		{
			// ROUND 18, BLOCKER 1. The rewritability predicate answered "can the APPLICATION
			// role reach the owner", which is not the same question as "is this handler
			// safe". Measured on four majors: a THIRD ordinary role owning the handler
			// rewrote it into a no-op while the verifier reported installed, and the guard
			// was dropped through the fence moments later. CREATE OR REPLACE preserves a
			// pre-existing owner, so pre-creating the function survives the operator's
			// re-apply — which is why the DDL now converges the owner as well as the body.
			name: "a handler owned by a third ordinary role is divergent",
			run: func(t *testing.T, f eventFenceFixture) {
				other := fmt.Sprintf("olv_evfence_other_%d_%s", f.major, matrixScope())
				eventFenceMustSucceed(t, f.db, "create an unrelated ordinary role",
					`CREATE ROLE "`+other+`" LOGIN NOSUPERUSER PASSWORD 'x'`)
				// Same class as the fixture's own cleanup: this role ends up OWNING the handler,
				// so a bare DROP ROLE fails on that dependency and a discarded error leaks it.
				t.Cleanup(func() {
					bg := context.Background()
					if _, err := f.db.ExecContext(bg, `DROP OWNED BY "`+other+`" CASCADE`); err != nil {
						t.Errorf("could not release what %q owns before dropping it: %v", other, err)
					}
					if _, err := f.db.ExecContext(bg, `DROP ROLE IF EXISTS "`+other+`"`); err != nil {
						t.Errorf("LEAKED cluster-scoped role %q: %v", other, err)
					}
				})
				eventFenceMustSucceed(t, f.db, "hand the handler to that role",
					`ALTER FUNCTION `+dialect.EngineSchema+`.`+dialect.GuardEventFenceHandlerFn+`() OWNER TO "`+other+`"`)
				requireEventFenceDivergentNaming(t, eventFenceStatusOf(t, f), "which is not a superuser")
			},
		},
		{
			// ROUND 18, BLOCKER 2. The handler bound the schema by the literal name
			// 'public' in both legs. In the canonical single-role topology the application
			// role owns the database and therefore that schema through pg_database_owner,
			// so it could rename it: ddl_command_end then queried a name that no longer
			// existed and accepted the rename, and sql_drop saw the new schema name and
			// accepted the drop. Reproduced 4/4 before the fix.
			name: "the fence still refuses after the schema is renamed",
			run: func(t *testing.T, f eventFenceFixture) {
				renamed := fmt.Sprintf("olv_shift_%d_%s", f.major, matrixScope())
				eventFenceMustSucceed(t, f.db, "rename the engine schema",
					`ALTER SCHEMA `+dialect.EngineSchema+` RENAME TO "`+renamed+`"`)
				t.Cleanup(func() {
					_, _ = f.db.ExecContext(context.Background(), `ALTER SCHEMA "`+renamed+`" RENAME TO `+dialect.EngineSchema)
				})
				msg := eventFenceMustFail(t, f.db, "drop the guard after the schema moved",
					`DROP TRIGGER fence_ledger_immutable ON "`+renamed+`".fence_ledger`)
				if !strings.Contains(msg, "refusing to drop the append-only guard") {
					t.Fatalf("the fence refused for another reason: %s", msg)
				}
			},
		},
		{
			// ROUND 20. The quoted-name FALSE NEGATIVE, measured in round eighteen and left
			// unadjudicated through two more rounds: a real guard whose logical name merely
			// carries a capital letter renders its identity as `"User_immutable" on ...`, and
			// the closing quote sits between the reserved suffix and the ` on `, so the text
			// pattern never matched it. The fence refused nothing and the guard came off.
			//
			// It is fixed structurally rather than by a second text pattern: address_names
			// carries {schema,table,trigger} with the trigger name unquoted. This case is the
			// one that falls if that half is withdrawn.
			name: "a guard whose name needs quoting is refused too",
			run: func(t *testing.T, f eventFenceFixture) {
				eventFenceMustSucceed(t, f.db, "create a relation whose guard needs quoting",
					`CREATE TABLE "Users" (id integer PRIMARY KEY)`)
				eventFenceMustSucceed(t, f.db, "guard it under a name that must be quoted",
					`CREATE TRIGGER "User_immutable" BEFORE UPDATE OR DELETE ON "Users" `+
						`FOR EACH ROW EXECUTE FUNCTION `+dialect.BlockMutationFn+`()`)
				eventFenceMustSucceed(t, f.db, "make it ALWAYS like every other guard",
					`ALTER TABLE "Users" ENABLE ALWAYS TRIGGER "User_immutable"`)

				msg := eventFenceMustFail(t, f.db, "drop a guard whose name needs quoting",
					`DROP TRIGGER "User_immutable" ON "Users"`)
				if !strings.Contains(msg, "refusing to drop the append-only guard") {
					t.Fatalf("the fence refused for another reason: %s", msg)
				}
				// AND IT IS STILL THERE. A refusal that did not roll back would read the same
				// from the error text alone and leave the relation unguarded.
				var n int
				if err := f.db.QueryRowContext(context.Background(),
					`SELECT count(*) FROM pg_catalog.pg_trigger t
					   JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
					  WHERE c.relname = 'Users' AND t.tgname = 'User_immutable' AND NOT t.tgisinternal`).Scan(&n); err != nil {
					t.Fatalf("read the quoted guard back after the refusal: %v", err)
				}
				if n != 1 {
					t.Fatalf("the quoted guard is gone (%d) after a refusal that claimed to stop it", n)
				}
			},
		},
		{
			// ROUND 20. The case above renames the schema and then DROPS the guard, so it
			// exercises `sql_drop` and nothing else. Round 18's fix had TWO legs — sql_drop
			// lost its `schema_name='public'` filter AND ddl_command_end stopped comparing
			// two literal 'public' names, joining the table's namespace to the function's by
			// OID instead (dialect/guardeventfence.go:148-149). Only the first leg was ever
			// guarded: reverting the OID join to two literal comparisons left the committed
			// case green, because a DROP never reaches the end leg at all.
			//
			// This is the other half, and it is the one an operator meets when a rename is
			// followed by a DISABLE rather than a DROP. The OID join is what keeps it
			// refusing: after the rename the table and the shared function are still in the
			// SAME namespace, so `fn.oid = c.relnamespace` holds while `nspname = 'public'`
			// no longer does for either of them.
			name: "the end leg still refuses a disable after the schema is renamed",
			run: func(t *testing.T, f eventFenceFixture) {
				renamed := fmt.Sprintf("olv_endshift_%d_%s", f.major, matrixScope())
				eventFenceMustSucceed(t, f.db, "rename the engine schema",
					`ALTER SCHEMA `+dialect.EngineSchema+` RENAME TO "`+renamed+`"`)
				t.Cleanup(func() {
					_, _ = f.db.ExecContext(context.Background(), `ALTER SCHEMA "`+renamed+`" RENAME TO `+dialect.EngineSchema)
				})
				msg := eventFenceMustFail(t, f.db, "disable the guard after the schema moved",
					`ALTER TABLE "`+renamed+`".fence_ledger DISABLE TRIGGER fence_ledger_immutable`)
				if !strings.Contains(msg, "disabled or replica-only") {
					t.Fatalf("the fence refused for another reason, so this case is not measuring the end leg: %s", msg)
				}
				// AND THE GUARD IS STILL ALWAYS AFTERWARDS. A refusal that rolled back is the
				// point; one that merely reported would leave the guard at 'D' and the next
				// DML unguarded, which reads identically from the error text alone.
				var state string
				if err := f.db.QueryRowContext(context.Background(),
					`SELECT t.tgenabled FROM pg_catalog.pg_trigger t
					   JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
					   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
					  WHERE n.nspname = $1 AND c.relname = 'fence_ledger' AND t.tgname = 'fence_ledger_immutable'`,
					renamed).Scan(&state); err != nil {
					t.Fatalf("read the guard's state back after the refusal: %v", err)
				}
				if state != "A" {
					t.Fatalf("the guard is %q after a refused DISABLE and must be \"A\": the statement was reported as refused and still took effect", state)
				}
			},
		},
		{
			// ROUND 19. Without the trigger-name half of the end leg's predicate, ANY
			// disabled trigger calling an unrelated function that merely shares the
			// engine's function name made EVERY subsequent DDL statement fail —
			// measured 4/4, an innocuous CREATE TABLE rolled back. A fence that stops
			// unrelated DDL is an outage, not a protection.
			name: "an unrelated disabled trigger does not stop every DDL statement",
			run: func(t *testing.T, f eventFenceFixture) {
				alt := fmt.Sprintf("olv_alt_%d_%s", f.major, matrixScope())
				eventFenceMustSucceed(t, f.db, "create a foreign schema", `CREATE SCHEMA "`+alt+`"`)
				t.Cleanup(func() {
					_, _ = f.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+alt+`" CASCADE`)
				})
				// A foreign function that merely SHARES the engine's reserved name, and a
				// foreign trigger that does NOT use the reserved suffix.
				eventFenceMustSucceed(t, f.db, "create a same-named foreign function",
					`CREATE FUNCTION "`+alt+`".`+dialect.BlockMutationFn+`() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RETURN NEW; END $fn$`)
				eventFenceMustSucceed(t, f.db, "create a foreign table", `CREATE TABLE "`+alt+`".business (id integer)`)
				eventFenceMustSucceed(t, f.db, "create a foreign trigger",
					`CREATE TRIGGER business_audit BEFORE UPDATE ON "`+alt+`".business FOR EACH ROW EXECUTE FUNCTION "`+alt+`".`+dialect.BlockMutationFn+`()`)
				eventFenceMustSucceed(t, f.db, "disable the foreign trigger",
					`ALTER TABLE "`+alt+`".business DISABLE TRIGGER business_audit`)
				// The fence must not turn somebody else's disabled trigger into a
				// database-wide DDL outage.
				eventFenceMustSucceed(t, f.db, "an unrelated CREATE TABLE",
					`CREATE TABLE "`+alt+`".harmless (id integer)`)
				var n int
				if err := f.db.QueryRowContext(context.Background(),
					`SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
					  WHERE n.nspname=$1 AND c.relname='harmless'`, alt).Scan(&n); err != nil {
					t.Fatalf("look for the harmless table: %v", err)
				}
				if n != 1 {
					t.Fatalf("the harmless table is not there (%d): the fence rolled back a statement that has nothing to do with it", n)
				}
				// And the fence still does its job on the real guard.
				msg := eventFenceMustFail(t, f.db, "disable the REAL guard",
					`ALTER TABLE fence_ledger DISABLE TRIGGER fence_ledger_immutable`)
				if !strings.Contains(msg, "disabled or replica-only") {
					t.Fatalf("narrowing the predicate broke the real refusal: %s", msg)
				}
			},
		},
		{
			// ROUND 19. CREATE EVENT TRIGGER has no IF NOT EXISTS, so re-applying the
			// artifact aborted on the first duplicate leg and never reached the OWNER TO
			// that repairs a mis-owned handler. Measured 4/4.
			name: "the install artifact can be re-applied",
			run: func(t *testing.T, f eventFenceFixture) {
				for _, s := range dialect.GuardEventFenceStmts() {
					if _, err := f.db.ExecContext(context.Background(), s); err != nil {
						t.Fatalf("re-applying the artifact must be a no-op, not an abort: %v\nstatement: %s", err, s)
					}
				}
				if status := eventFenceStatusOf(t, f); status.Verdict != guardEventFenceInstalled {
					t.Fatalf("after a re-apply the fence must still be installed.\n  verdict: %s\n  reasons: %v", status.Verdict, status.Reasons)
				}
			},
		},
		{
			name: "a leg declared on the wrong event is divergent",
			run: func(t *testing.T, f eventFenceFixture) {
				eventFenceMustSucceed(t, f.db, "remove the drop leg",
					`DROP EVENT TRIGGER `+dialect.GuardEventFenceDropTrigger)
				eventFenceMustSucceed(t, f.db, "recreate it on the wrong event",
					`CREATE EVENT TRIGGER `+dialect.GuardEventFenceDropTrigger+` ON ddl_command_end EXECUTE FUNCTION `+
						dialect.EngineSchema+`.`+dialect.GuardEventFenceHandlerFn+`()`)
				eventFenceMustSucceed(t, f.db, "make it ALWAYS",
					`ALTER EVENT TRIGGER `+dialect.GuardEventFenceDropTrigger+` ENABLE ALWAYS`)
				requireEventFenceDivergentNaming(t, eventFenceStatusOf(t, f), "evtevent: want sql_drop")
			},
		},
	}
}

// TestPostgresTheEventFenceThisBuildInstallsIsTheOneItDeclares is the projection half: this
// build's own DDL satisfies this build's own verifier, and each separate way of breaking the
// fence is judged DIVERGENT naming THAT field.
func TestPostgresTheEventFenceThisBuildInstallsIsTheOneItDeclares(t *testing.T) {
	table := eventFenceProjectionCaseTable()
	if len(table) != 12 {
		t.Fatalf("this test expects 12 projection vectors and the table carries %d: a vector added without updating the expectation is uncovered, and one removed without updating it is silently gone", len(table))
	}
	names := make([]string, 0, len(table))
	for _, c := range table {
		names = append(names, c.name)
	}
	coverage := newMajorCaseCoverage(eventFenceCaseNames(names))
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			for i, c := range table {
				t.Run(c.name, func(t *testing.T) {
					db := eventFenceServer(t, m, fmt.Sprintf("olv_evf_p%d_%d_%s", i, m.Major, matrixScope()))
					f := installEventFenceFixture(t, db, m.Major, true)
					c.run(t, f)
					coverage.markCase(fmt.Sprintf("%d/%s", m.Major, c.name))
				})
			}
		})
	}
}

// TestPostgresAnAbsentEventFenceIsAbsentAndNotDivergent pins the distinction the whole boot
// posture rests on: a database that never had the fence must not be reported as one that lost
// it — and applying the DDL this build renders must turn that same database into an installed
// one, which is the two halves of the contract measured against each other rather than
// declared.
func TestPostgresAnAbsentEventFenceIsAbsentAndNotDivergent(t *testing.T) {
	coverage := newMajorCoverage()
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			db := eventFenceServer(t, m, fmt.Sprintf("olv_evf_abs_%d_%s", m.Major, matrixScope()))
			f := installEventFenceFixture(t, db, m.Major, false)
			if status := eventFenceStatusOf(t, f); status.Verdict != guardEventFenceAbsent {
				t.Fatalf("a database that never had the fence is ABSENT.\n  verdict: %s\n  reasons: %v", status.Verdict, status.Reasons)
			}
			for _, s := range dialect.GuardEventFenceStmts() {
				if _, err := db.ExecContext(context.Background(), s); err != nil {
					t.Fatalf("apply the rendered fence DDL: %v\nstatement: %s", err, s)
				}
			}
			if got := eventFenceStatusOf(t, f); got.Verdict != guardEventFenceInstalled {
				t.Fatalf("applying this build's own DDL must reach INSTALLED.\n  verdict: %s\n  reasons: %v", got.Verdict, got.Reasons)
			}
			coverage.mark(m.Major)
		})
	}
}

// TestPostgresAnOriginEventFenceDoesNotFireUnderReplica executes the measurement that makes
// ENABLE ALWAYS load-bearing rather than decorative.
//
// It is the fence's own version of the fact the row guards already record: only ALWAYS is
// independent of session_replication_role — and that setting is grantable to an ordinary role
// on every certified major, which is a wider door than the `event_triggers` GUC that does not
// even exist before 17.
func TestPostgresAnOriginEventFenceDoesNotFireUnderReplica(t *testing.T) {
	coverage := newMajorCoverage()
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)
	ctx := context.Background()

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			db := eventFenceServer(t, m, fmt.Sprintf("olv_evf_rep_%d_%s", m.Major, matrixScope()))
			f := installEventFenceFixture(t, db, m.Major, true)

			replicaConn := func(what string) *sql.Conn {
				c, err := f.db.Conn(ctx)
				if err != nil {
					t.Fatalf("take a connection for %s: %v", what, err)
				}
				t.Cleanup(func() { _ = c.Close() })
				if _, err := c.ExecContext(ctx, `SET session_replication_role = 'replica'`); err != nil {
					t.Fatalf("set session_replication_role for %s: %v", what, err)
				}
				return c
			}

			always := replicaConn("the ALWAYS leg")
			if _, err := always.ExecContext(ctx, `DROP TRIGGER fence_ledger_immutable ON fence_ledger`); err == nil {
				t.Fatal("an ALWAYS fence must still refuse a replica-role session; it accepted")
			}

			if _, err := f.db.ExecContext(ctx, `ALTER EVENT TRIGGER `+dialect.GuardEventFenceDropTrigger+` ENABLE`); err != nil {
				t.Fatalf("downgrade the fence to ORIGIN: %v", err)
			}
			origin := replicaConn("the ORIGIN leg")
			if _, err := origin.ExecContext(ctx, `DROP TRIGGER fence_ledger_immutable ON fence_ledger`); err != nil {
				t.Fatalf("this test's whole point is that an ORIGIN fence is bypassable under replica; it refused: %v", err)
			}
			coverage.mark(m.Major)
		})
	}
}
