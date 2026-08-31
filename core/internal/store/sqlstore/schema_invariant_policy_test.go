// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the pure half of the trigger-boundary proof. A decision left
// tangled with the catalog query is only exercised where a server is configured —
// which is exactly how "the self-test checks a NAME" survived a review.
// schemaInvariantViolation is pure, so the whole matrix runs
// here: absent, DISABLED, replica-only, an unrecognized state, moved to another
// table, body swapped, function not executable.
//
// The server-gated half (schema_invariant_pg_test.go) proves the one thing this
// cannot: that PostgreSQL reports the tgenabled character these tests assume. It
// runs locally too — the dev container carries a live PostgreSQL (CONTRIBUTING.md).

const testSchema = "public"

// firingPosture is an ordinary session: the one state in which
// TriggerEnableState.Fires is meaningful.
var firingPosture = dialect.RolePosture{Role: "olivares_app", ReplicationRole: "origin"}

func required(table, name string) registeredSchemaTrigger {
	return registeredSchemaTrigger{
		namespace:     "module",
		SchemaTrigger: store.SchemaTrigger{Name: name, Table: table},
	}
}

func requiredWithDigest(table, name, digest string) registeredSchemaTrigger {
	r := required(table, name)
	r.DefinitionSHA256 = digest
	return r
}

func liveTrigger(table, name string, state dialect.TriggerEnableState) (dialect.TriggerKey, dialect.TriggerInfo) {
	return dialect.TriggerKey{Schema: testSchema, Table: table, Name: name},
		dialect.TriggerInfo{
			Function:    "public.olivares_block_mutation()",
			CanExecute:  true,
			EnableState: state,
		}
}

func catalog(entries ...func(map[dialect.TriggerKey]dialect.TriggerInfo)) map[dialect.TriggerKey]dialect.TriggerInfo {
	live := make(map[dialect.TriggerKey]dialect.TriggerInfo)
	for _, e := range entries {
		e(live)
	}
	return live
}

func withTrigger(table, name string, state dialect.TriggerEnableState) func(map[dialect.TriggerKey]dialect.TriggerInfo) {
	return func(live map[dialect.TriggerKey]dialect.TriggerInfo) {
		k, v := liveTrigger(table, name, state)
		live[k] = v
	}
}

func withDefinition(table, name string, state dialect.TriggerEnableState, definition string) func(map[dialect.TriggerKey]dialect.TriggerInfo) {
	return func(live map[dialect.TriggerKey]dialect.TriggerInfo) {
		k, v := liveTrigger(table, name, state)
		v.Definition = definition
		live[k] = v
	}
}

func withUnexecutable(table, name string) func(map[dialect.TriggerKey]dialect.TriggerInfo) {
	return func(live map[dialect.TriggerKey]dialect.TriggerInfo) {
		k, v := liveTrigger(table, name, dialect.TriggerFiresOrigin)
		v.CanExecute = false
		live[k] = v
	}
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestSchemaInvariantEnableStateMatrix is the O/D/R/A regression the close review
// demanded, at the layer that DECIDES.
//
// PostgreSQL keeps a trigger in the catalog after ALTER TABLE … DISABLE TRIGGER and
// after ENABLE REPLICA TRIGGER. Both states look identical to a check that lists
// names: the object is there, on the right table, invoking the right function — and
// it never runs. 'R' is the nastier of the two, because it reads as "enabled for
// replicas" while this engine refuses to open a replica session at all, so it fires
// precisely never.
func TestSchemaInvariantEnableStateMatrix(t *testing.T) {
	t.Parallel()
	inv := []registeredSchemaTrigger{required("module_config", "module_config_sticky")}

	for _, tc := range []struct {
		name      string
		state     dialect.TriggerEnableState
		wantFires bool
		wantIn    string // substring the boot error must contain when it does not fire
	}{
		{"O — the default, fires in origin and local", dialect.TriggerFiresOrigin, true, ""},
		// 'A' is deliberately NOT in this table — its acceptance is a recorded, still
		// open product decision, so it lives in
		// TestSchemaInvariantEnableAlwaysFollowsTheRecordedDecision.
		{"D — DISABLE TRIGGER, present and inert", dialect.TriggerNeverFires, false, "DISABLE TRIGGER"},
		{"R — ENABLE REPLICA, never fires on this engine", dialect.TriggerFiresReplicaOnly, false, "REPLICA-ONLY"},
		{"unrecognized state is deny-closed", dialect.TriggerEnableState("X"), false, "unrecognized enable state"},
		{"absent state is deny-closed", dialect.TriggerStateUnknown, false, "no enable state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			live := catalog(withTrigger("module_config", "module_config_sticky", tc.state))
			err := schemaInvariantViolation(
				testSchema, firingPosture, live, inv, nil, store.EnginePostgres, false)
			if tc.wantFires {
				if err != nil {
					t.Fatalf("tgenabled=%q must be accepted, boot refused: %v", tc.state, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("tgenabled=%q was ACCEPTED: the guard is listed in the catalog and "+
					"never runs, which is the exact state a name check cannot see", tc.state)
			}
			// Attribute by CATEGORY. Matching message text would freeze a diagnostic
			// detail and, worse, could not tell "absent" from "present and inert" —
			// different incidents with different remedies.
			if !errors.Is(err, store.ErrSchemaTriggerInert) {
				t.Fatalf("refused, but not as an inert trigger, so the enable state was not "+
					"what decided it: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("the error does not tell an operator what state to undo (want %q): %v",
					tc.wantIn, err)
			}
			// The message must name the offending trigger, not just the class of fault.
			if !strings.Contains(err.Error(), "public.module_config.module_config_sticky") {
				t.Fatalf("the error does not name the trigger: %v", err)
			}
		})
	}
}

// DECISION instead of freezing the second into a regression.
//
// The fact is not in dispute: `ENABLE ALWAYS` ('A') fires under every replication role,
// so Fires() is true and the guard demonstrably runs (the PostgreSQL matrix proves that
// behaviourally). The DECISION — whether a deployment that has set ALWAYS on a boundary
// trigger should be accepted — is open, because such a trigger also fires on a
// subscriber applying replicated rows and can re-materialize a fact the subscription is
// already delivering. That is why the rollout migration leaves its own triggers at 'O'.
//
// So this test asserts the fact unconditionally, and asserts the acceptance AGAINST THE
// RECORDED DECISION (acceptAlwaysEnabledBoundaryTriggers). Taking the decision is then
// a one-constant change in production code, with the expectation here following
// automatically — not a test that has to be edited to permit the decision.
func TestSchemaInvariantEnableAlwaysFollowsTheRecordedDecision(t *testing.T) {
	t.Parallel()
	// The fact, independent of any policy.
	if !dialect.TriggerFiresAlways.Fires() {
		t.Fatal("ENABLE ALWAYS does not fire, which contradicts PostgreSQL: a guard in " +
			"that state runs under every replication role")
	}

	inv := []registeredSchemaTrigger{required("module_config", "module_config_sticky")}
	live := catalog(withTrigger("module_config", "module_config_sticky", dialect.TriggerFiresAlways))
	err := schemaInvariantViolation(
		testSchema, firingPosture, live, inv, nil, store.EnginePostgres, false)

	if acceptAlwaysEnabledBoundaryTriggers {
		if err != nil {
			t.Fatalf("the recorded decision accepts ENABLE ALWAYS, but boot refused it: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("the recorded decision refuses ENABLE ALWAYS, but boot accepted it")
	}
	if !errors.Is(err, store.ErrSchemaTriggerInert) {
		t.Fatalf("refused, but not as an inert boundary: %v", err)
	}
	// The refusal must not read as a PostgreSQL fact — it is a posture choice.
	if !strings.Contains(err.Error(), "recorded logical-replication posture") {
		t.Fatalf("the refusal does not say it is a policy decision rather than a "+
			"PostgreSQL behavior: %v", err)
	}
}

// TestSchemaInvariantRejectsAHomonymOnAnotherTable is the second regression the
// close review demanded.
//
// A PostgreSQL trigger name only has to be unique PER TABLE. Keyed by name alone,
// a trigger called `module_config_sticky` sitting on some OTHER table satisfies the
// requirement for the one on module_config — and the boundary that was supposed to
// be verified is simply absent. This is not hypothetical tampering: a migration
// that recreates a guard against the wrong table produces exactly this catalog.
func TestSchemaInvariantRejectsAHomonymOnAnotherTable(t *testing.T) {
	t.Parallel()
	const shared = "module_config_sticky"
	inv := []registeredSchemaTrigger{required("module_config", shared)}

	// The required table has NO trigger; an unrelated table carries the name.
	live := catalog(withTrigger("module_other", shared, dialect.TriggerFiresOrigin))
	err := schemaInvariantViolation(
		testSchema, firingPosture, live, inv, nil, store.EnginePostgres, false)
	if err == nil {
		t.Fatal("a trigger of the required NAME on a DIFFERENT table satisfied the invariant: " +
			"module_config has no guard at all and the store would open")
	}
	if !errors.Is(err, store.ErrSchemaTriggerMissing) {
		t.Fatalf("refused, but not as a MISSING trigger: %v", err)
	}
	if !strings.Contains(err.Error(), "public.module_config."+shared) {
		t.Fatalf("the error must name the table that is actually unguarded: %v", err)
	}
	// The report must not point at the innocent table.
	if strings.Contains(err.Error(), "module_other") {
		t.Fatalf("the error blames the wrong table: %v", err)
	}
}

// TestSchemaInvariantHomonymsAreVerifiedIndependently is the sharper half of the
// same hazard: BOTH tables carry the name, and only the required one is inert.
//
// Under a name-keyed map the two collapse into one entry, so whichever the catalog
// happened to return last decides the verdict — a 50% chance of booting with the
// guard switched off. Keyed by (schema, table, name) each is judged on its own.
func TestSchemaInvariantHomonymsAreVerifiedIndependently(t *testing.T) {
	t.Parallel()
	const shared = "module_no_truncate"
	inv := []registeredSchemaTrigger{
		required("module_config", shared),
		required("module_facts", shared),
	}

	t.Run("both firing is accepted", func(t *testing.T) {
		t.Parallel()
		live := catalog(
			withTrigger("module_config", shared, dialect.TriggerFiresOrigin),
			withTrigger("module_facts", shared, dialect.TriggerFiresOrigin),
		)
		if err := schemaInvariantViolation(
			testSchema, firingPosture, live, inv, nil, store.EnginePostgres, false); err != nil {
			t.Fatalf("two same-named guards on their own tables were refused: %v", err)
		}
	})

	t.Run("one disabled is refused and correctly attributed", func(t *testing.T) {
		t.Parallel()
		live := catalog(
			withTrigger("module_config", shared, dialect.TriggerNeverFires),
			withTrigger("module_facts", shared, dialect.TriggerFiresOrigin),
		)
		err := schemaInvariantViolation(
			testSchema, firingPosture, live, inv, nil, store.EnginePostgres, false)
		if err == nil {
			t.Fatal("a DISABLED guard was accepted because its homonym on another table fires")
		}
		if !errors.Is(err, store.ErrSchemaTriggerInert) {
			t.Fatalf("refused, but not as an inert trigger: %v", err)
		}
		if !strings.Contains(err.Error(), "public.module_config."+shared) {
			t.Fatalf("the error must name the table whose guard is inert: %v", err)
		}
		if strings.Contains(err.Error(), "public.module_facts."+shared) {
			t.Fatalf("the error blames the healthy homonym too: %v", err)
		}
	})
}

// TestSchemaInvariantReportsEveryFaultBeforeFailing pins the diagnostic contract:
// the boundary's whole state is reported, not the first fault found. An operator
// who fixes one trigger, reboots, and discovers a second is being made to bisect a
// state the engine already knew in full.
func TestSchemaInvariantReportsEveryFaultBeforeFailing(t *testing.T) {
	t.Parallel()
	inv := []registeredSchemaTrigger{
		required("module_config", "guard_a"),
		required("module_facts", "guard_b"),
	}
	live := catalog() // neither exists
	err := schemaInvariantViolation(
		testSchema, firingPosture, live, inv, nil, store.EnginePostgres, false)
	if err == nil {
		t.Fatal("two missing guards were accepted")
	}
	if !errors.Is(err, store.ErrSchemaTriggerMissing) {
		t.Fatalf("refused, but not as MISSING triggers: %v", err)
	}
	for _, want := range []string{"public.module_config.guard_a", "public.module_facts.guard_b"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error omits %s, so an operator fixes one fault at a time: %v", want, err)
		}
	}
}

// TestSchemaInvariantDetectsAReplacedBody covers the state every structural check
// passes: right name, right table, firing, and a body swapped for a no-op. Only the
// catalog's own canonical evidence distinguishes it. SQLite reports its stored
// trigger statement; PostgreSQL reports an injectively framed trigger+function.
func TestSchemaInvariantDetectsAReplacedBody(t *testing.T) {
	t.Parallel()
	// These fixtures carry the state a SQLite catalog actually reports:
	// TriggerNoEnableState, not PostgreSQL's ENABLE ALWAYS. Using 'A' as a generic
	// stand-in for "it fires" silently coupled these cases to the open
	// logical-replication decision — a mutation caught it.
	const realBody = "CREATE TRIGGER g BEFORE UPDATE ON module_config BEGIN SELECT RAISE(ABORT,'no'); END"
	const noopBody = "CREATE TRIGGER g BEFORE UPDATE ON module_config BEGIN SELECT 1; END"
	inv := []registeredSchemaTrigger{
		requiredWithDigest("module_config", "g", digestOf(realBody)),
	}

	t.Run("matching body is accepted", func(t *testing.T) {
		t.Parallel()
		live := catalog(withDefinition("module_config", "g", dialect.TriggerNoEnableState, realBody))
		if err := schemaInvariantViolation(
			testSchema, firingPosture, live, inv, nil, store.EngineSQLite, false); err != nil {
			t.Fatalf("the declared body was rejected: %v", err)
		}
	})

	t.Run("no-op body is refused", func(t *testing.T) {
		t.Parallel()
		live := catalog(withDefinition("module_config", "g", dialect.TriggerNoEnableState, noopBody))
		err := schemaInvariantViolation(
			testSchema, firingPosture, live, inv, nil, store.EngineSQLite, false)
		if err == nil {
			t.Fatal("a same-name, same-table, firing trigger with a NO-OP body was accepted")
		}
		if !errors.Is(err, store.ErrSchemaTriggerTampered) {
			t.Fatalf("refused, but not as a TAMPERED definition: %v", err)
		}
	})

	// An invariant that declares no digest must not be silently "verified" by the
	// absence of one — its definition is simply out of scope for this check.
	t.Run("no declared digest means the body is not checked", func(t *testing.T) {
		t.Parallel()
		live := catalog(withDefinition("module_config", "g", dialect.TriggerFiresOrigin, noopBody))
		if err := schemaInvariantViolation(
			testSchema, firingPosture,
			live, []registeredSchemaTrigger{required("module_config", "g")},
			nil, store.EnginePostgres, false); err != nil {
			t.Fatalf("an invariant without a declared digest must not fail on body: %v", err)
		}
	})
}

// TestSchemaInvariantAppRolePrivileges covers the owner/app split: the guard can be
// present and firing while the application role cannot EXECUTE the function it calls.
func TestSchemaInvariantAppRolePrivileges(t *testing.T) {
	t.Parallel()
	inv := []registeredSchemaTrigger{required("module_config", "g")}
	live := catalog(withUnexecutable("module_config", "g"))

	t.Run("split refuses a function the app role cannot execute", func(t *testing.T) {
		t.Parallel()
		err := schemaInvariantViolation(
			testSchema, firingPosture, live, inv,
			[]string{"module_facts"}, store.EnginePostgres, true)
		if err == nil {
			t.Fatal("the app role cannot EXECUTE the trigger function and boot was allowed")
		}
		if !errors.Is(err, store.ErrSchemaTriggerUnexecutable) {
			t.Fatalf("wrong failure category: %v", err)
		}
	})

	// Single-role deployments own the function; the probe is not applicable and must
	// not manufacture a failure.
	t.Run("single role does not probe execute", func(t *testing.T) {
		t.Parallel()
		if err := schemaInvariantViolation(
			testSchema, firingPosture, live, inv, nil, store.EnginePostgres, false); err != nil {
			t.Fatalf("the single-role path must not probe EXECUTE: %v", err)
		}
	})

	// SQLite has no function privilege layer at all.
	t.Run("sqlite has no execute privileges to probe", func(t *testing.T) {
		t.Parallel()
		if err := schemaInvariantViolation(
			testSchema, firingPosture, live, inv, nil, store.EngineSQLite, true); err != nil {
			t.Fatalf("SQLite has no EXECUTE privilege layer; boot must not fail on it: %v", err)
		}
	})

	// A split that declares triggers but registers no boundary fact table means the
	// grant half of the boundary is unverifiable — refuse rather than report success.
	t.Run("split with no boundary table is refused", func(t *testing.T) {
		t.Parallel()
		ok := catalog(withTrigger("module_config", "g", dialect.TriggerFiresOrigin))
		err := schemaInvariantViolation(
			testSchema, firingPosture, ok, inv, nil, store.EnginePostgres, true)
		if err == nil {
			t.Fatal("a split with no security-boundary table was accepted")
		}
		if !errors.Is(err, store.ErrSchemaBoundaryTableMissing) {
			t.Fatalf("wrong failure category: %v", err)
		}
	})
}

// TestSchemaInvariantRefusesToJudgeInAReplicaSession pins the precondition every
// firing verdict rests on.
//
// In session_replication_role='replica' PostgreSQL's rules INVERT: 'R' fires and 'O'
// does not. Judging the boundary there would report a healthy guard as inert and — far
// worse — an inert one as healthy. The boot guards refuse such a session already
// (store.go:105, dbsetup.go:87); this makes the decision function itself total, so a
// reordering refactor cannot silently reintroduce the hazard.
func TestSchemaInvariantRefusesToJudgeInAReplicaSession(t *testing.T) {
	t.Parallel()
	inv := []registeredSchemaTrigger{required("module_config", "g")}
	live := catalog(withTrigger("module_config", "g", dialect.TriggerFiresOrigin))
	replica := dialect.RolePosture{Role: "olivares_app", ReplicationRole: "replica"}

	err := schemaInvariantViolation(
		testSchema, replica, live, inv, nil, store.EnginePostgres, false)
	if err == nil {
		t.Fatal("the boundary was judged in a replica session, where 'O' does not fire: " +
			"an inert guard would be reported as healthy")
	}
	if !errors.Is(err, store.ErrSchemaBoundaryUnjudgeable) {
		t.Fatalf("refused, but not as an unjudgeable boundary: %v", err)
	}
	if !strings.Contains(err.Error(), "session_replication_role") {
		t.Fatalf("the error does not name the precondition that failed: %v", err)
	}

	// 'local' is NOT replica: PostgreSQL fires ordinary triggers there, so a store
	// whose guards work must open. This is the availability half of the same rule.
	local := dialect.RolePosture{Role: "olivares_app", ReplicationRole: "local"}
	if err := schemaInvariantViolation(
		testSchema, local, live, inv, nil, store.EnginePostgres, false); err != nil {
		t.Fatalf("session_replication_role='local' fires ordinary triggers; boot must proceed: %v", err)
	}
}

// TestSchemaInvariantNoDeclarationsIsANoOp guards the empty case: a store with no
// module invariants must open. It is here so a future "fail when nothing is declared"
// change is a deliberate decision rather than an accident.
func TestSchemaInvariantNoDeclarationsIsANoOp(t *testing.T) {
	t.Parallel()
	if err := schemaInvariantViolation(
		testSchema, firingPosture, nil, nil, nil, store.EnginePostgres, true); err != nil {
		t.Fatalf("a store with no declared invariants must open: %v", err)
	}
}
