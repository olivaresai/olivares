// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import "fmt"

// finopscustodysqlite.go renders core v12 on SQLite: the three relations and the seven
// guard triggers of SCHEMA-V12 §4.1.
//
// WHAT THE GUARDS ACHIEVE AND WHAT THEY DO NOT, stated before the code because the limit is
// the interesting part. They bound writes to LEGAL TRANSITION SEQUENCES: the journal row
// must cite the head it is about to replace, the head may only advance by exactly one
// revision to the after-image its journal row declares, and neither journal nor proof may
// be updated or deleted at all. They do NOT create a role boundary — SQLite has none, and
// anything holding the file can issue the same legal transitions the engine would. The
// raw-only Go API is the intended path, not an isolation proof.
//
// AND ONE WINDOW THEY ADMIT BY DESIGN, because a silent one would be worse: the legal first
// journal row can be committed ALONE, before its proof and its head. Ordering the three
// writes the other way round is impossible — each guard reads the rows its predecessor
// wrote — so the prefix has to be reachable. A caller with direct SQL authority can stop
// there. The guards do not prevent it; the separately owned verifier's orphan checks detect
// it, which is detection, not prevention, and is labelled as such.

// sqliteCustodyRel qualifies a custody relation for a trigger body. `main.` is explicit for
// the same reason every other guard in this package qualifies: an ATTACHed database must not
// be able to shift which table a guard reads.
func sqliteCustodyRel(t string) string { return "main." + t }

// FinOpsCustodyControlStmts renders the v12 DDL for SQLite.
//
// It is role-free on purpose: SQLite has no role model, so there is no ACL leg here and
// FinOpsCustodyControlTables carries no establishment statements on this engine. The
// statement list is therefore identical for every deployment, which is what lets a managed-
// object inventory name it without knowing anything about roles.
func (sqliteDialect) FinOpsCustodyControlStmts() []string {
	p := finOpsCustodyPredicates{sqlite: true}
	c1, c2, c3 := sqliteCustodyRel(ControlCustodyEnrollmentTable), sqliteCustodyRel(ControlCustodyProofTable), sqliteCustodyRel(ControlCustodyTransitionTable)

	out := []string{
		// C1, the mutable head. No DELETE is ever legal (the guard refuses it
		// unconditionally), so "mutable" here means UPDATE through a cited journal row
		// and nothing else.
		`CREATE TABLE ` + ControlCustodyEnrollmentTable + ` (
  custody_domain TEXT COLLATE BINARY NOT NULL,
  custody_instance_id TEXT COLLATE BINARY NOT NULL,
  keyring_format INTEGER NOT NULL,
  state TEXT COLLATE BINARY NOT NULL,
  revision INTEGER NOT NULL,
  highest_generation INTEGER NOT NULL,
  confirmed_generation INTEGER NOT NULL,
  active_generation INTEGER NOT NULL,
  created_at TEXT COLLATE BINARY NOT NULL,
  updated_at TEXT COLLATE BINARY NOT NULL,
  PRIMARY KEY (custody_domain, custody_instance_id),
  CHECK (` + p.reqText("custody_domain", "custody_domain = '"+FinOpsCustodyDomain+"'") + `),
  CHECK (` + p.instanceID("custody_instance_id") + `),
  CHECK (` + p.reqInt("keyring_format", finOpsCustodyKeyringFormat, finOpsCustodyKeyringFormat) + `),
  CHECK (` + p.reqText("state", "state IN ('enrolling', 'enrolled', 'active', 'abandoned')") + `),
  CHECK (` + p.reqInt("revision", 1, finOpsCustodyRevisionCap) + `),
  CHECK (` + p.reqInt("highest_generation", 1, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("confirmed_generation", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqInt("active_generation", 0, finOpsCustodyGenerationCap) + `),
  CHECK (` + p.reqTimestamp("created_at") + `),
  CHECK (` + p.reqTimestamp("updated_at") + `),
  -- The generation order. confirmed trails highest by AT MOST one, which is what makes
  -- "a generation was added and not yet confirmed" the only legal gap.
  CHECK (confirmed_generation <= highest_generation
     AND confirmed_generation >= highest_generation - 1
     AND active_generation <= confirmed_generation),
  CHECK (` + finOpsCustodyHeadCoherenceCheck + `)
)`,
		// At most one LIVE instance per domain. A partial unique index rather than a
		// trigger because the engine enforces it on every path, including the ones no
		// guard sees.
		`CREATE UNIQUE INDEX ` + ControlCustodyEnrollmentTable + `_one_live
  ON ` + ControlCustodyEnrollmentTable + ` (custody_domain) WHERE state <> 'abandoned'`,

		// C2, the retained per-generation proof.
		`CREATE TABLE ` + ControlCustodyProofTable + ` (
  custody_domain TEXT COLLATE BINARY NOT NULL,
  custody_instance_id TEXT COLLATE BINARY NOT NULL,
  generation INTEGER NOT NULL,
  custody_key_ref TEXT COLLATE BINARY NOT NULL,
  proof_format INTEGER NOT NULL,
  proof_purpose TEXT COLLATE BINARY NOT NULL,
  proof_nonce BLOB NOT NULL,
  proof_ciphertext BLOB NOT NULL,
  recorded_revision INTEGER NOT NULL,
  recorded_at TEXT COLLATE BINARY NOT NULL,
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
  CHECK (` + p.reqTimestamp("recorded_at") + `),
  -- An equivalence, not an implication: generation 1 is the enrollment proof AND no
  -- later generation may claim to be one.
  CHECK ((generation = 1) = (proof_purpose = 'enrollment'))
)`,

		// C3, the append-only journal with both images.
		`CREATE TABLE ` + ControlCustodyTransitionTable + ` (
  custody_domain TEXT COLLATE BINARY NOT NULL,
  custody_instance_id TEXT COLLATE BINARY NOT NULL,
  to_revision INTEGER NOT NULL,
  transition TEXT COLLATE BINARY NOT NULL,
  from_state TEXT COLLATE BINARY,
  from_highest INTEGER,
  from_confirmed INTEGER,
  from_active INTEGER,
  to_state TEXT COLLATE BINARY NOT NULL,
  to_highest INTEGER NOT NULL,
  to_confirmed INTEGER NOT NULL,
  to_active INTEGER NOT NULL,
  subject_generation INTEGER NOT NULL,
  writers_upgraded INTEGER NOT NULL,
  writers_drained INTEGER NOT NULL,
  core_version INTEGER NOT NULL,
  guard_epoch INTEGER NOT NULL,
  actor TEXT COLLATE BINARY NOT NULL,
  reason TEXT COLLATE BINARY NOT NULL,
  recorded_at TEXT COLLATE BINARY NOT NULL,
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
  CHECK (typeof(core_version) = 'integer' AND core_version >= ` + fmt.Sprint(finOpsCustodyCoreVersion) + `),
  CHECK (typeof(guard_epoch) = 'integer' AND guard_epoch IN (8, 9, 10)),
  CHECK (` + p.reqBytes("actor", 1, 256) + `),
  CHECK (` + p.reqBytes("reason", 1, 1024) + `),
  CHECK (` + p.reqTimestamp("recorded_at") + `),
  CHECK (` + finOpsCustodyRevisionOneImageCheck + `),
  CHECK (to_confirmed <= to_highest AND to_confirmed >= to_highest - 1 AND to_active <= to_confirmed),
  CHECK (from_state IS NULL
     OR (from_confirmed <= from_highest
     AND from_confirmed >= from_highest - 1
     AND from_active <= from_confirmed)),
  CHECK (` + finOpsCustodyTransitionRowCheck() + `)
)`,
	}
	return append(out, sqliteFinOpsCustodyGuards(c1, c2, c3)...)
}

// sqliteFinOpsCustodyGuards renders the seven §4.1 triggers.
//
// Each ORDER guard reads only rows the prescribed statement order has ALREADY inserted, so
// none of them depends on a row the same statement is about to write. That is what makes
// them composable with one transaction rather than requiring a deferred constraint SQLite
// does not have.
func sqliteFinOpsCustodyGuards(c1, c2, c3 string) []string {
	needsProof := "NEW.transition IN ('confirm_enrollment', 'activate', 'confirm_generation', 'select_generation')"
	return []string{
		// C3 INSERT: the journal row must cite a head that exists in the state it
		// claims, or — at revision 1 — must find no head and no live sibling at all.
		fmt.Sprintf(`CREATE TRIGGER %s_order BEFORE INSERT ON %s
FOR EACH ROW
BEGIN
  -- Revision 1 opens an instance. It requires an EMPTY slot: no head for this
  -- instance, no live head anywhere in the domain, no prior journal row for this
  -- instance, and retained capacity below the finite cap.
  SELECT RAISE(ABORT, 'custody transition rev 1 requires an empty instance slot')
  WHERE NEW.to_revision = 1
    AND (EXISTS (SELECT 1 FROM %s e
                 WHERE e.custody_domain = NEW.custody_domain
                   AND e.custody_instance_id = NEW.custody_instance_id)
      OR EXISTS (SELECT 1 FROM %s e
                 WHERE e.custody_domain = NEW.custody_domain AND e.state <> 'abandoned')
      OR EXISTS (SELECT 1 FROM %s t
                 WHERE t.custody_domain = NEW.custody_domain
                   AND t.custody_instance_id = NEW.custody_instance_id)
      OR (SELECT COUNT(*) FROM %s e WHERE e.custody_domain = NEW.custody_domain) >= %d);
  -- Revision n >= 2 must follow revision n-1 in the journal AND must cite the head
  -- exactly as the head currently stands. Citing it is what makes a stale or forged
  -- before-image unusable rather than merely unlikely.
  SELECT RAISE(ABORT, 'custody transition must follow its predecessor and cite the live head')
  WHERE NEW.to_revision >= 2
    AND (NOT EXISTS (SELECT 1 FROM %s t
                     WHERE t.custody_domain = NEW.custody_domain
                       AND t.custody_instance_id = NEW.custody_instance_id
                       AND t.to_revision = NEW.to_revision - 1)
      OR NOT EXISTS (SELECT 1 FROM %s e
                     WHERE e.custody_domain = NEW.custody_domain
                       AND e.custody_instance_id = NEW.custody_instance_id
                       AND e.revision = NEW.to_revision - 1
                       AND e.state = NEW.from_state
                       AND e.highest_generation = NEW.from_highest
                       AND e.confirmed_generation = NEW.from_confirmed
                       AND e.active_generation = NEW.from_active));
  -- A transition that ACTS ON a generation requires that generation's proof to be
  -- already retained. Minting the journal row first would let a confirmation name a
  -- generation no proof ever covered.
  SELECT RAISE(ABORT, 'custody transition requires the subject generation proof')
  WHERE %s
    AND NOT EXISTS (SELECT 1 FROM %s p
                    WHERE p.custody_domain = NEW.custody_domain
                      AND p.custody_instance_id = NEW.custody_instance_id
                      AND p.generation = NEW.subject_generation);
END`, ControlCustodyTransitionTable, ControlCustodyTransitionTable, c1, c1, c3, c1, finOpsCustodyInstanceCap, c3, c1, needsProof, c2),

		// C2 INSERT: a proof belongs to a journal row that minted its generation, and
		// must land BEFORE the head advances past that revision.
		fmt.Sprintf(`CREATE TRIGGER %s_order BEFORE INSERT ON %s
FOR EACH ROW
BEGIN
  SELECT RAISE(ABORT, 'custody proof requires its minting transition')
  WHERE NOT EXISTS (SELECT 1 FROM %s t
                    WHERE t.custody_domain = NEW.custody_domain
                      AND t.custody_instance_id = NEW.custody_instance_id
                      AND t.to_revision = NEW.recorded_revision
                      AND t.transition IN ('begin_enrollment', 'add_generation')
                      AND t.subject_generation = NEW.generation);
  -- The head must not yet have advanced. At revision 1 there is no head at all; later
  -- the head still stands one revision behind the journal row being proved.
  SELECT RAISE(ABORT, 'custody proof must precede the head advance')
  WHERE NOT (
    (NEW.recorded_revision = 1
     AND NOT EXISTS (SELECT 1 FROM %s e
                     WHERE e.custody_domain = NEW.custody_domain
                       AND e.custody_instance_id = NEW.custody_instance_id))
    OR EXISTS (SELECT 1 FROM %s e
               WHERE e.custody_domain = NEW.custody_domain
                 AND e.custody_instance_id = NEW.custody_instance_id
                 AND e.revision = NEW.recorded_revision - 1));
END`, ControlCustodyProofTable, ControlCustodyProofTable, c3, c1, c1),

		// C1 INSERT: the head is born at revision 1 only, and only as the image its
		// opening journal row already declared, with generation 1 already proved.
		fmt.Sprintf(`CREATE TRIGGER %s_insert BEFORE INSERT ON %s
FOR EACH ROW
BEGIN
  SELECT RAISE(ABORT, 'custody head is born at revision 1 from a recorded begin_enrollment')
  WHERE NEW.revision <> 1
     OR NOT EXISTS (SELECT 1 FROM %s t
                    WHERE t.custody_domain = NEW.custody_domain
                      AND t.custody_instance_id = NEW.custody_instance_id
                      AND t.to_revision = 1
                      AND t.transition = 'begin_enrollment'
                      AND t.to_state = NEW.state
                      AND t.to_highest = NEW.highest_generation
                      AND t.to_confirmed = NEW.confirmed_generation
                      AND t.to_active = NEW.active_generation)
     OR NOT EXISTS (SELECT 1 FROM %s p
                    WHERE p.custody_domain = NEW.custody_domain
                      AND p.custody_instance_id = NEW.custody_instance_id
                      AND p.generation = 1
                      AND p.recorded_revision = 1);
END`, ControlCustodyEnrollmentTable, ControlCustodyEnrollmentTable, c3, c2),

		// C1 UPDATE: identity and birth facts are frozen, the revision advances by
		// exactly one, and the new image must be the after-image of the journal row for
		// that exact revision.
		fmt.Sprintf(`CREATE TRIGGER %s_update BEFORE UPDATE ON %s
FOR EACH ROW
BEGIN
  SELECT RAISE(ABORT, 'custody head identity and keyring format are immutable')
  WHERE NEW.custody_domain <> OLD.custody_domain
     OR NEW.custody_instance_id <> OLD.custody_instance_id
     OR NEW.keyring_format <> OLD.keyring_format
     OR NEW.created_at <> OLD.created_at;
  SELECT RAISE(ABORT, 'custody head advances one revision per recorded transition')
  WHERE NEW.revision <> OLD.revision + 1
     OR NOT EXISTS (SELECT 1 FROM %s t
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
                      AND t.to_active = NEW.active_generation);
  -- An add_generation head advance requires the new generation's proof, recorded at
  -- this very revision. Without it the head could claim a generation nothing proves.
  SELECT RAISE(ABORT, 'custody generation advance requires its proof at this revision')
  WHERE EXISTS (SELECT 1 FROM %s t
                WHERE t.custody_domain = NEW.custody_domain
                  AND t.custody_instance_id = NEW.custody_instance_id
                  AND t.to_revision = NEW.revision
                  AND t.transition = 'add_generation')
    AND NOT EXISTS (SELECT 1 FROM %s p
                    WHERE p.custody_domain = NEW.custody_domain
                      AND p.custody_instance_id = NEW.custody_instance_id
                      AND p.generation = NEW.highest_generation
                      AND p.recorded_revision = NEW.revision);
END`, ControlCustodyEnrollmentTable, ControlCustodyEnrollmentTable, c3, c3, c2),

		// The three unconditional refusals. The head is never deleted, and the journal
		// and the proof are never updated or deleted.
		fmt.Sprintf("CREATE TRIGGER %s_no_delete BEFORE DELETE ON %s\nFOR EACH ROW\nBEGIN SELECT RAISE(ABORT, '%s is not deletable'); END", ControlCustodyEnrollmentTable, ControlCustodyEnrollmentTable, ControlCustodyEnrollmentTable),
		fmt.Sprintf("CREATE TRIGGER %s_no_update BEFORE UPDATE ON %s\nFOR EACH ROW\nBEGIN SELECT RAISE(ABORT, '%s is append-only'); END", ControlCustodyProofTable, ControlCustodyProofTable, ControlCustodyProofTable),
		fmt.Sprintf("CREATE TRIGGER %s_no_delete BEFORE DELETE ON %s\nFOR EACH ROW\nBEGIN SELECT RAISE(ABORT, '%s is append-only'); END", ControlCustodyProofTable, ControlCustodyProofTable, ControlCustodyProofTable),
		fmt.Sprintf("CREATE TRIGGER %s_no_update BEFORE UPDATE ON %s\nFOR EACH ROW\nBEGIN SELECT RAISE(ABORT, '%s is append-only'); END", ControlCustodyTransitionTable, ControlCustodyTransitionTable, ControlCustodyTransitionTable),
		fmt.Sprintf("CREATE TRIGGER %s_no_delete BEFORE DELETE ON %s\nFOR EACH ROW\nBEGIN SELECT RAISE(ABORT, '%s is append-only'); END", ControlCustodyTransitionTable, ControlCustodyTransitionTable, ControlCustodyTransitionTable),
	}
}

// finOpsCustodySQLiteGuardNames lists the guard identities v12 creates on SQLite, so a
// verifier or a census can name them without re-parsing the DDL.
func finOpsCustodySQLiteGuardNames() []string {
	return []string{
		ControlCustodyEnrollmentTable + "_insert",
		ControlCustodyEnrollmentTable + "_update",
		ControlCustodyEnrollmentTable + "_no_delete",
		ControlCustodyProofTable + "_order",
		ControlCustodyProofTable + "_no_update",
		ControlCustodyProofTable + "_no_delete",
		ControlCustodyTransitionTable + "_order",
		ControlCustodyTransitionTable + "_no_update",
		ControlCustodyTransitionTable + "_no_delete",
	}
}
