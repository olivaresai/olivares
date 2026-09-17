// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"fmt"
	"strings"
)

// finopscustodypostgres.go renders core v12 on PostgreSQL: the three relations, the single
// guard function of SCHEMA-V12 §4.2, its triggers in ALWAYS, the statement-level TRUNCATE
// refusals, and the PUBLIC revocation.
//
// ONE FUNCTION, SIX TRIGGERS, and the reason it is not six functions: the predicates are the
// same predicates on both engines, and six bodies would be six places for them to drift. The
// function dispatches on TG_TABLE_NAME and TG_OP, which PostgreSQL supplies, so the dispatch
// cannot disagree with the attachment.
//
// WHY THESE TRIGGERS DO NOT CALL olivares_block_mutation, which is the repository's usual
// append-only guard. Calling it would put C1–C3 inside the append-only ACL scope, and that
// scope is reconciled on every boot: an existing per-boot normalizer would then discover,
// revoke and re-assert on relations whose posture this migration establishes ONCE and
// deliberately never re-asserts. The relations are NOT admitted to
// control_appendonly_scope, and the consequence is stated rather than hidden — drift in
// their ACL is OBSERVED by a later verifier, never normalized.
//
// WHY EVERY TRIGGER IS `ENABLE ALWAYS` AND NOT ORIGIN, measured elsewhere in this package
// and not re-derived here: a guard left in the default 'O' state does not fire in a replica
// session, so an UPDATE arriving through logical replication applies with zero errors while
// 'A' preserves the row and raises. Custody history that a subscriber can rewrite is not
// custody history.
//
// WHY PLAIN `CREATE`, NEVER `OR REPLACE`. `CREATE OR REPLACE FUNCTION` preserves the owner
// and the body of a function that already exists under that name, so a pre-existing
// hostile definition would survive the migration that believes it authored it. A plain
// CREATE fails loudly on a name collision, and failing loudly inside the migration
// transaction rolls the whole thing back.

// postgresCustodyGuardFn is the one guard function v12 creates. The name is normative: a
// later verifier names the function it expects rather than copying the literal.
const postgresCustodyGuardFn = "olivares_custody_enrollment_guard"

// PostgresCustodyGuardFunction is the exported identity of that function.
const PostgresCustodyGuardFunction = postgresCustodyGuardFn

// pgCustodyRel qualifies a custody relation against EngineSchema.
//
// The qualification is required, not stylistic: the guard function runs with
// `SET search_path = pg_catalog` so that every operator and function it names resolves to a
// definition no other schema can shadow, and that setting makes an unqualified relation
// name unresolvable.
func pgCustodyRel(t string) string { return EngineSchema + "." + t }

// FinOpsCustodyControlStmts renders the v12 DDL for PostgreSQL.
//
// IT IS ROLE-FREE, and that is a deliberate boundary rather than an omission. The
// application-role REVOKE/GRANT pair of the split topology depends on a role this dialect
// does not know, so it is appended by the migration constructor that does. What is here is
// every statement whose text is the same for every deployment — the relations, the indexes,
// the guard function, the triggers, the ALWAYS state and the PUBLIC revoke — which is what
// lets a managed-object inventory name this list without knowing anything about roles.
func (postgresDialect) FinOpsCustodyControlStmts() []string {
	p := finOpsCustodyPredicates{}
	text := `pg_catalog.text COLLATE pg_catalog."C"`

	out := []string{
		`CREATE TABLE ` + ControlCustodyEnrollmentTable + ` (
  custody_domain ` + text + ` NOT NULL,
  custody_instance_id ` + text + ` NOT NULL,
  keyring_format pg_catalog.int8 NOT NULL,
  state ` + text + ` NOT NULL,
  revision pg_catalog.int8 NOT NULL,
  highest_generation pg_catalog.int8 NOT NULL,
  confirmed_generation pg_catalog.int8 NOT NULL,
  active_generation pg_catalog.int8 NOT NULL,
  created_at ` + text + ` NOT NULL,
  updated_at ` + text + ` NOT NULL,
  PRIMARY KEY (custody_domain, custody_instance_id),
  CHECK (` + p.reqText("custody_domain", "custody_domain = '"+FinOpsCustodyDomain+"'") + `),
  CHECK (` + p.instanceID("custody_instance_id") + `),
  CHECK (` + p.reqInt("keyring_format", finOpsCustodyKeyringFormat, finOpsCustodyKeyringFormat) + `),
  CHECK (` + p.reqText("state", "state IN ('enrolling', 'enrolled', 'active', 'abandoned')") + `),
  CHECK (` + p.reqInt("revision", 1, finOpsCustodyRevisionCap) + `),
  CHECK (` + p.reqInt("highest_generation", 1, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("confirmed_generation", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("active_generation", 0, finOpsCustodyGenerationCap) + `),
  CHECK (confirmed_generation <= highest_generation
     AND confirmed_generation >= highest_generation - 1
     AND active_generation <= confirmed_generation),
  CHECK (` + finOpsCustodyHeadCoherenceCheck + `)
)`,
		`CREATE UNIQUE INDEX ` + ControlCustodyEnrollmentTable + `_one_live
  ON ` + ControlCustodyEnrollmentTable + ` (custody_domain) WHERE state <> 'abandoned'`,

		`CREATE TABLE ` + ControlCustodyProofTable + ` (
  custody_domain ` + text + ` NOT NULL,
  custody_instance_id ` + text + ` NOT NULL,
  generation pg_catalog.int8 NOT NULL,
  custody_key_ref ` + text + ` NOT NULL,
  proof_format pg_catalog.int8 NOT NULL,
  proof_purpose ` + text + ` NOT NULL,
  proof_nonce pg_catalog.bytea NOT NULL,
  proof_ciphertext pg_catalog.bytea NOT NULL,
  recorded_revision pg_catalog.int8 NOT NULL,
  recorded_at ` + text + ` NOT NULL,
  PRIMARY KEY (custody_domain, custody_instance_id, generation),
  UNIQUE (custody_domain, custody_instance_id, custody_key_ref),
  UNIQUE (custody_domain, custody_instance_id, recorded_revision),
  CHECK (` + p.reqText("custody_domain", "custody_domain = '"+FinOpsCustodyDomain+"'") + `),
  CHECK (` + p.instanceID("custody_instance_id") + `),
  CHECK (` + p.reqInt("generation", 1, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.keyRef("custody_key_ref") + `),
  CHECK (` + p.reqInt("proof_format", finOpsCustodyProofFormat, finOpsCustodyProofFormat) + `),
  CHECK (` + p.reqText("proof_purpose", "proof_purpose IN ('enrollment', 'generation_add')") + `),
  CHECK (` + p.reqBlob("proof_nonce", finOpsCustodyProofNonce) + `),
  CHECK (` + p.reqBlob("proof_ciphertext", finOpsCustodyProofCipher) + `),
  CHECK (` + p.reqInt("recorded_revision", 1, finOpsCustodyRevisionCap) + `),
  CHECK ((generation = 1) = (proof_purpose = 'enrollment'))
)`,

		`CREATE TABLE ` + ControlCustodyTransitionTable + ` (
  custody_domain ` + text + ` NOT NULL,
  custody_instance_id ` + text + ` NOT NULL,
  to_revision pg_catalog.int8 NOT NULL,
  transition ` + text + ` NOT NULL,
  from_state ` + text + `,
  from_highest pg_catalog.int8,
  from_confirmed pg_catalog.int8,
  from_active pg_catalog.int8,
  to_state ` + text + ` NOT NULL,
  to_highest pg_catalog.int8 NOT NULL,
  to_confirmed pg_catalog.int8 NOT NULL,
  to_active pg_catalog.int8 NOT NULL,
  subject_generation pg_catalog.int8 NOT NULL,
  writers_upgraded pg_catalog.int8 NOT NULL,
  writers_drained pg_catalog.int8 NOT NULL,
  core_version pg_catalog.int8 NOT NULL,
  guard_epoch pg_catalog.int8 NOT NULL,
  actor ` + text + ` NOT NULL,
  reason ` + text + ` NOT NULL,
  recorded_at ` + text + ` NOT NULL,
  PRIMARY KEY (custody_domain, custody_instance_id, to_revision),
  CHECK (` + p.reqText("custody_domain", "custody_domain = '"+FinOpsCustodyDomain+"'") + `),
  CHECK (` + p.instanceID("custody_instance_id") + `),
  CHECK (` + p.reqInt("to_revision", 1, finOpsCustodyRevisionCap) + `),
  CHECK (` + p.reqText("transition", "transition IN ('begin_enrollment', 'confirm_enrollment', 'abandon', 'activate', 'add_generation', 'confirm_generation', 'select_generation')") + `),
  CHECK (` + p.nullText("from_state", "from_state IN ('enrolling', 'enrolled', 'active')") + `),
  CHECK (` + p.nullInt("from_highest", 1, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.nullInt("from_confirmed", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.nullInt("from_active", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqText("to_state", "to_state IN ('enrolling', 'enrolled', 'active', 'abandoned')") + `),
  CHECK (` + p.reqInt("to_highest", 1, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("to_confirmed", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("to_active", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("subject_generation", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("writers_upgraded", 0, 1) + `),
  CHECK (` + p.reqInt("writers_drained", 0, 1) + `),
  CHECK (core_version >= ` + fmt.Sprint(finOpsCustodyCoreVersion) + `),
  CHECK (guard_epoch IN (8, 9, 10)),
  CHECK (` + p.reqBytes("actor", 1, 256) + `),
  CHECK (` + p.reqBytes("reason", 1, 1024) + `),
  CHECK (` + finOpsCustodyRevisionOneImageCheck + `),
  CHECK (to_confirmed <= to_highest AND to_confirmed >= to_highest - 1 AND to_active <= to_confirmed),
  CHECK (from_state IS NULL
     OR (from_confirmed <= from_highest
     AND from_confirmed >= from_highest - 1
     AND from_active <= from_confirmed)),
  CHECK (` + finOpsCustodyTransitionRowCheck() + `)
)`,
	}

	body := postgresCustodyGuardBody()
	out = append(out, "CREATE FUNCTION "+EngineSchema+"."+postgresCustodyGuardFn+
		"() RETURNS pg_catalog.trigger LANGUAGE plpgsql SET search_path = pg_catalog AS "+pgDollarQuote(body))

	for _, spec := range postgresFinOpsCustodyTriggerSpecs() {
		out = append(out,
			fmt.Sprintf("CREATE TRIGGER %s %s ON %s %s EXECUTE FUNCTION %s.%s()",
				spec.Name, spec.Events, spec.Table, spec.Scope, EngineSchema, postgresCustodyGuardFn),
			fmt.Sprintf("ALTER TABLE ONLY %s ENABLE ALWAYS TRIGGER %s", spec.Table, spec.Name),
		)
	}

	// PUBLIC, on both topologies and unconditionally.
	//
	// This one does not depend on a role name, so it belongs in the role-free list. It is
	// also the only custody ACL statement that is correct under EVERY topology: PUBLIC is
	// never the owner, never the application role, and a grant to it would hand the three
	// relations to every role in the cluster including ones created later.
	out = append(out, "REVOKE ALL ON TABLE "+strings.Join(FinOpsCustodyControlTables(), ", ")+" FROM PUBLIC")
	return out
}

// postgresFinOpsCustodyTrigger is one trigger attachment of §4.2.
type postgresFinOpsCustodyTrigger struct {
	Name   string
	Table  string
	Events string
	Scope  string
}

// postgresFinOpsCustodyTriggerSpecs lists the six attachments in creation order.
//
// The TRUNCATE guards are STATEMENT-level, and they have to be: TRUNCATE fires no row
// trigger at all, so a FOR EACH ROW guard on an append-only relation is silent on exactly
// the operation that empties it. Every other guard is FOR EACH ROW, because it judges a
// row's content.
func postgresFinOpsCustodyTriggerSpecs() []postgresFinOpsCustodyTrigger {
	specs := []postgresFinOpsCustodyTrigger{
		{ControlCustodyTransitionTable + "_order", ControlCustodyTransitionTable, "BEFORE INSERT", "FOR EACH ROW"},
		{ControlCustodyProofTable + "_order", ControlCustodyProofTable, "BEFORE INSERT", "FOR EACH ROW"},
		{ControlCustodyEnrollmentTable + "_write", ControlCustodyEnrollmentTable, "BEFORE INSERT OR UPDATE OR DELETE", "FOR EACH ROW"},
		{ControlCustodyProofTable + "_immutable", ControlCustodyProofTable, "BEFORE UPDATE OR DELETE", "FOR EACH ROW"},
		{ControlCustodyTransitionTable + "_immutable", ControlCustodyTransitionTable, "BEFORE UPDATE OR DELETE", "FOR EACH ROW"},
	}
	for _, t := range FinOpsCustodyControlTables() {
		specs = append(specs, postgresFinOpsCustodyTrigger{t + "_no_truncate", t, "BEFORE TRUNCATE", "FOR EACH STATEMENT"})
	}
	return specs
}

// PostgresFinOpsCustodyTriggerNames lists the guard identities v12 creates on PostgreSQL.
func PostgresFinOpsCustodyTriggerNames() []string {
	specs := postgresFinOpsCustodyTriggerSpecs()
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// postgresCustodyGuardBody is the plpgsql body shared by every custody trigger.
//
// THE DISPATCH IS FAIL-CLOSED AT BOTH ENDS. An operation this function was not attached for
// raises rather than returning, and a table it does not recognize raises rather than
// allowing. That matters because the function is reachable by name: attaching it to a fourth
// relation, or to an event it has no predicate for, must not produce a permissive guard.
func postgresCustodyGuardBody() string {
	c1, c2, c3 := pgCustodyRel(ControlCustodyEnrollmentTable), pgCustodyRel(ControlCustodyProofTable), pgCustodyRel(ControlCustodyTransitionTable)
	return fmt.Sprintf(`
DECLARE
  retained bigint;
BEGIN
  -- TRUNCATE first, and at statement level: it carries neither NEW nor OLD, so every
  -- row-shaped test below would dereference a record that does not exist.
  IF TG_OP = 'TRUNCATE' THEN
    RAISE EXCEPTION USING ERRCODE = '23514',
      MESSAGE = 'custody control relation ' || TG_TABLE_NAME || ' is not truncatable';
  END IF;

  IF TG_TABLE_NAME = '%[4]s' THEN
    IF TG_OP <> 'INSERT' THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = '%[4]s is append-only';
    END IF;
    IF NEW.to_revision = 1 THEN
      SELECT pg_catalog.count(*) INTO retained FROM %[1]s e WHERE e.custody_domain = NEW.custody_domain;
      IF EXISTS (SELECT 1 FROM %[1]s e
                 WHERE e.custody_domain = NEW.custody_domain
                   AND e.custody_instance_id = NEW.custody_instance_id)
         OR EXISTS (SELECT 1 FROM %[1]s e
                    WHERE e.custody_domain = NEW.custody_domain AND e.state <> 'abandoned')
         OR EXISTS (SELECT 1 FROM %[3]s t
                    WHERE t.custody_domain = NEW.custody_domain
                      AND t.custody_instance_id = NEW.custody_instance_id)
         OR retained >= %[5]d THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
          MESSAGE = 'custody transition rev 1 requires an empty instance slot';
      END IF;
    ELSE
      IF NOT EXISTS (SELECT 1 FROM %[3]s t
                     WHERE t.custody_domain = NEW.custody_domain
                       AND t.custody_instance_id = NEW.custody_instance_id
                       AND t.to_revision = NEW.to_revision - 1)
         OR NOT EXISTS (SELECT 1 FROM %[1]s e
                        WHERE e.custody_domain = NEW.custody_domain
                          AND e.custody_instance_id = NEW.custody_instance_id
                          AND e.revision = NEW.to_revision - 1
                          AND e.state = NEW.from_state
                          AND e.highest_generation = NEW.from_highest
                          AND e.confirmed_generation = NEW.from_confirmed
                          AND e.active_generation = NEW.from_active) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
          MESSAGE = 'custody transition must follow its predecessor and cite the live head';
      END IF;
    END IF;
    IF NEW.transition IN ('confirm_enrollment', 'activate', 'confirm_generation', 'select_generation')
       AND NOT EXISTS (SELECT 1 FROM %[2]s p
                       WHERE p.custody_domain = NEW.custody_domain
                         AND p.custody_instance_id = NEW.custody_instance_id
                         AND p.generation = NEW.subject_generation) THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'custody transition requires the subject generation proof';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_TABLE_NAME = '%[6]s' THEN
    IF TG_OP <> 'INSERT' THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = '%[6]s is append-only';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM %[3]s t
                   WHERE t.custody_domain = NEW.custody_domain
                     AND t.custody_instance_id = NEW.custody_instance_id
                     AND t.to_revision = NEW.recorded_revision
                     AND t.transition IN ('begin_enrollment', 'add_generation')
                     AND t.subject_generation = NEW.generation) THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'custody proof requires its minting transition';
    END IF;
    IF NOT ((NEW.recorded_revision = 1
             AND NOT EXISTS (SELECT 1 FROM %[1]s e
                             WHERE e.custody_domain = NEW.custody_domain
                               AND e.custody_instance_id = NEW.custody_instance_id))
            OR EXISTS (SELECT 1 FROM %[1]s e
                       WHERE e.custody_domain = NEW.custody_domain
                         AND e.custody_instance_id = NEW.custody_instance_id
                         AND e.revision = NEW.recorded_revision - 1)) THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'custody proof must precede the head advance';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_TABLE_NAME = '%[7]s' THEN
    IF TG_OP = 'DELETE' THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = '%[7]s is not deletable';
    END IF;
    IF TG_OP = 'INSERT' THEN
      IF NEW.revision <> 1
         OR NOT EXISTS (SELECT 1 FROM %[3]s t
                        WHERE t.custody_domain = NEW.custody_domain
                          AND t.custody_instance_id = NEW.custody_instance_id
                          AND t.to_revision = 1
                          AND t.transition = 'begin_enrollment'
                          AND t.to_state = NEW.state
                          AND t.to_highest = NEW.highest_generation
                          AND t.to_confirmed = NEW.confirmed_generation
                          AND t.to_active = NEW.active_generation)
         OR NOT EXISTS (SELECT 1 FROM %[2]s p
                        WHERE p.custody_domain = NEW.custody_domain
                          AND p.custody_instance_id = NEW.custody_instance_id
                          AND p.generation = 1
                          AND p.recorded_revision = 1) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
          MESSAGE = 'custody head is born at revision 1 from a recorded begin_enrollment';
      END IF;
      RETURN NEW;
    END IF;
    IF NEW.custody_domain <> OLD.custody_domain
       OR NEW.custody_instance_id <> OLD.custody_instance_id
       OR NEW.keyring_format <> OLD.keyring_format
       OR NEW.created_at <> OLD.created_at THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'custody head identity and keyring format are immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1
       OR NOT EXISTS (SELECT 1 FROM %[3]s t
                      WHERE t.custody_domain = NEW.custody_domain
                        AND t.custody_instance_id = NEW.custody_instance_id
                        AND t.to_revision = NEW.revision
                        AND t.from_state = OLD.state
                        AND t.from_highest = OLD.highest_generation
                        AND t.from_confirmed = OLD.confirmed_generation
                        AND t.from_active = OLD.active_generation
                        AND t.to_state = NEW.state
                        AND t.to_highest = NEW.highest_generation
                        AND t.to_confirmed = NEW.confirmed_generation
                        AND t.to_active = NEW.active_generation) THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'custody head advances one revision per recorded transition';
    END IF;
    IF EXISTS (SELECT 1 FROM %[3]s t
               WHERE t.custody_domain = NEW.custody_domain
                 AND t.custody_instance_id = NEW.custody_instance_id
                 AND t.to_revision = NEW.revision
                 AND t.transition = 'add_generation')
       AND NOT EXISTS (SELECT 1 FROM %[2]s p
                       WHERE p.custody_domain = NEW.custody_domain
                         AND p.custody_instance_id = NEW.custody_instance_id
                         AND p.generation = NEW.highest_generation
                         AND p.recorded_revision = NEW.revision) THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'custody generation advance requires its proof at this revision';
    END IF;
    RETURN NEW;
  END IF;

  RAISE EXCEPTION USING ERRCODE = '23514',
    MESSAGE = 'custody guard attached to an unexpected relation ' || TG_TABLE_NAME;
END `, c1, c2, c3, ControlCustodyTransitionTable, finOpsCustodyInstanceCap, ControlCustodyProofTable, ControlCustodyEnrollmentTable)
}
