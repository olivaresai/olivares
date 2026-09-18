// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"fmt"
	"strings"
)

// finopscustody.go renders the three FinOps custody control relations of core v12
// `finops_custody_control_v1`, exactly as the ratified construction contract
// `an internal design note (not shipped)` declares them.
//
// WHAT THIS FILE IS, and what it deliberately is not. It is the DDL and the guards: the
// three relations, their typed constraints, their unique indexes, the statement-order
// guards, the append-only guards, and — on PostgreSQL — the `ENABLE ALWAYS` state, the
// statement-level TRUNCATE refusals and the PUBLIC revocation. It is NOT the migration
// plan, the ceiling, the witness capture, the ceremony, the keyring or the boot verifier.
// Those are separately owned. Nothing here is reachable from a boot until the plan names
// it.
//
// THE SHAPE, in one paragraph, because the constraints only read as deliberate once it is
// stated. C1 `control_custody_enrollment` is the single MUTABLE head per instance: the
// state, the revision counter and the three generation counters. C3
// `control_custody_transition` is the APPEND-ONLY journal of every change to that head,
// carrying both the before-image and the after-image of the four coherent fields. C2
// `control_custody_proof` is the APPEND-ONLY, retained per-generation proof. The head is
// therefore never the authority: it is a cache of the last C3 row, and any disagreement
// between them is detectable by reading C3 alone.
//
// WHY THE BEFORE-IMAGE IS NULLABLE AND NOTHING ELSE IS. Revision 1 has no predecessor, so
// its four `from_*` columns are NULL — and they are NULL exactly there, enforced by an
// equivalence rather than by four independent CHECKs, so "NULL in revision 7" and
// "non-NULL in revision 1" are both rejected by the same predicate. Every other column is
// NOT NULL, and that is load-bearing: on BOTH engines a CHECK whose value is NULL
// evaluates to NULL and therefore PASSES. A range CHECK is never a substitute for NOT
// NULL, and this file never uses one as though it were.
//
// WHY SQLITE CARRIES `typeof(...)` AND POSTGRESQL DOES NOT. SQLite columns are
// dynamically typed: a declared INTEGER column accepts the string 'abc' with no coercion
// and no complaint, so a bare `revision BETWEEN 1 AND 128` passes for text that compares
// inside the range. PostgreSQL rejects the same value at the type layer before any CHECK
// runs. The predicate expanders below therefore emit the storage-class guard on SQLite
// only, and the two engines end up accepting the same set of values through different
// text.

// The three relation names. They are normative: SCHEMA-V12 §4.3.1 requires every custody
// ACL decision to take its targets from FinOpsCustodyControlTables and from nothing else.
const (
	// ControlCustodyEnrollmentTable (C1) is the mutable head: one row per custody
	// instance, at most one of them live per domain.
	ControlCustodyEnrollmentTable = "control_custody_enrollment"
	// ControlCustodyProofTable (C2) is the retained per-generation proof, append-only.
	ControlCustodyProofTable = "control_custody_proof"
	// ControlCustodyTransitionTable (C3) is the append-only transition journal carrying
	// the before-image and the after-image of every head change.
	ControlCustodyTransitionTable = "control_custody_transition"
)

// FinOpsCustodyDomain is the only value the three relations admit in `custody_domain`.
//
// It is a CHECKed literal rather than a convention: a second domain would silently give
// the partial unique index of §3.1 a second live instance to permit, which is precisely
// the invariant the index exists to hold.
const FinOpsCustodyDomain = "finops.policy_recovery.v1"

// The finite caps of SCHEMA-V12, named once so the DDL, the guards and any later verifier
// cannot drift from each other by a literal.
const (
	finOpsCustodyRevisionCap   = 128
	finOpsCustodyGenerationCap = 32
	finOpsCustodyInstanceCap   = 64
	finOpsCustodyProofNonce    = 12
	finOpsCustodyProofCipher   = 48
	finOpsCustodyKeyringFormat = 1
	finOpsCustodyProofFormat   = 1
	finOpsCustodyCoreVersion   = 12
)

// FinOpsCustodyControlTables lists the three relations in the FIXED sorted order every
// custody ACL check takes its targets in.
//
// Exporting the order from one place is what stops the establishment statements, the
// closed target list and any later verification from disagreeing about which three
// relations they are talking about. GuardControlPlaneTables is a DIFFERENT list — the
// three C4 rollout logs — and SCHEMA-V12 §4.3.1 forbids substituting it here.
func FinOpsCustodyControlTables() []string {
	return []string{ControlCustodyEnrollmentTable, ControlCustodyProofTable, ControlCustodyTransitionTable}
}

// finOpsCustodyPredicates expands the SCHEMA-V12 §2.2 notation into literal CHECK text for
// one engine.
type finOpsCustodyPredicates struct{ sqlite bool }

// reqInt expands REQ_INT(c, lo, hi) on a NOT NULL integer column.
func (p finOpsCustodyPredicates) reqInt(c string, lo, hi int) string {
	rng := fmt.Sprintf("%s BETWEEN %d AND %d", c, lo, hi)
	if p.sqlite {
		return fmt.Sprintf("typeof(%s) = 'integer' AND %s", c, rng)
	}
	return rng
}

// nullInt expands NULL_INT(c, lo, hi) on one of the four nullable before-image columns.
//
// The parenthesis on the SQLite arm is not cosmetic: `c IS NULL OR typeof(c) = 'integer'
// AND c BETWEEN …` would bind AND tighter than OR and still be accepted, but the grouping
// is what makes the intent auditable against the contract text.
func (p finOpsCustodyPredicates) nullInt(c string, lo, hi int) string {
	rng := fmt.Sprintf("%s BETWEEN %d AND %d", c, lo, hi)
	if p.sqlite {
		return fmt.Sprintf("%s IS NULL OR (typeof(%s) = 'integer' AND %s)", c, c, rng)
	}
	return fmt.Sprintf("%s IS NULL OR %s", c, rng)
}

// reqText expands REQ_TEXT(c, P) on a NOT NULL text column.
func (p finOpsCustodyPredicates) reqText(c, pred string) string {
	if p.sqlite {
		return fmt.Sprintf("typeof(%s) = 'text' AND %s", c, pred)
	}
	return pred
}

// nullText expands NULL_TEXT(c, P), used only for from_state.
func (p finOpsCustodyPredicates) nullText(c, pred string) string {
	if p.sqlite {
		return fmt.Sprintf("%s IS NULL OR (typeof(%s) = 'text' AND %s)", c, c, pred)
	}
	return fmt.Sprintf("%s IS NULL OR %s", c, pred)
}

// reqBlob expands REQ_BLOB(c, n): an exact octet length on a binary column.
func (p finOpsCustodyPredicates) reqBlob(c string, n int) string {
	if p.sqlite {
		return fmt.Sprintf("typeof(%s) = 'blob' AND length(%s) = %d", c, c, n)
	}
	return fmt.Sprintf("pg_catalog.octet_length(%s) = %d", c, n)
}

// reqBytes expands REQ_BYTES(c, lo, hi): a byte-length bound on a text column.
//
// The CAST to BLOB on the SQLite arm is the whole point. `length()` on text counts
// CHARACTERS, so a 256-character field of three-byte runes would pass a 256-byte bound
// while storing 768 bytes. PostgreSQL's octet_length already counts bytes.
func (p finOpsCustodyPredicates) reqBytes(c string, lo, hi int) string {
	if p.sqlite {
		return fmt.Sprintf("typeof(%s) = 'text' AND length(CAST(%s AS BLOB)) BETWEEN %d AND %d", c, c, lo, hi)
	}
	return fmt.Sprintf("pg_catalog.octet_length(%s) BETWEEN %d AND %d", c, lo, hi)
}

// reqTimestamp is the storage-class guard on an RFC 3339 nanosecond UTC text column.
//
// PostgreSQL gets NO CHECK here, and that is the contract (§3.1–§3.3): its `text` type
// already denies every non-text value, so a CHECK would restate the type system. SQLite
// has no such denial.
func (p finOpsCustodyPredicates) reqTimestamp(c string) string {
	if p.sqlite {
		return fmt.Sprintf("typeof(%s) = 'text'", c)
	}
	return ""
}

// instanceID expands the canonical lowercase UUIDv4 predicate of §2.3.
//
// The two arms accept EXACTLY the same strings, and the SQLite arm is not a weaker
// approximation of the regular expression: SQLite GLOB is case-sensitive and anchored to
// the whole string, the length is pinned at 36 characters AND at 36 bytes, and the pattern
// carries one single-character token per position with the hyphens fixed at 9/14/19/24, the
// version nibble `4` at 15 and the variant class `[89ab]` at 20. Token-level equivalence is
// asserted by an executed test on both engines, not by reading the pattern.
//
// ⛔ THE BYTE LENGTH IS NOT A SECOND OPINION ON THE CHARACTER LENGTH. It was added on
// 2026-09-12 after Root inspected these literals, and it closes a canonical-identity defect
// that the character length alone could not: on SQLite `length()`, `substr()` and `GLOB` ALL
// STOP AT AN EMBEDDED NUL. A canonical UUID followed by a NUL and arbitrary suffix bytes
// therefore satisfied every term this predicate previously carried — measured on the actual
// driver, accepted, and PERSISTED. The relation's primary key and the order guards join on
// this column, so two values differing only after a NUL were two distinct keys to the
// storage engine and one identical value to the predicate meant to canonicalize them.
//
// THE TWO TERMS ARE LOAD-BEARING IN OPPOSITE DIRECTIONS, which is why the character term is
// RETAINED rather than replaced:
//
//   - `length(c) = 36` refuses a value PADDED to 36 bytes by a NUL: its pre-NUL character
//     count falls short.
//   - `length(CAST(c AS BLOB)) = 36` refuses a canonical value EXTENDED past 36 bytes by a
//     NUL and a suffix: its byte count overshoots.
//
// Together they are exhaustive for NUL rather than merely better: a NUL at character
// position k leaves the character length at k−1 and the byte length at k or more, so 36
// characters AND 36 bytes is reachable only with no NUL anywhere. The same pair also
// establishes CANONICAL ASCII WIDTH, because bytes equal to characters means every character
// occupies one byte.
//
// PostgreSQL is unchanged and needs no equivalent: its `text` type does not admit a NUL at
// all, so there is nothing there for a byte term to refuse.
func (p finOpsCustodyPredicates) instanceID(c string) string {
	if !p.sqlite {
		return fmt.Sprintf("%s OPERATOR(pg_catalog.~) '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'", c)
	}
	hex := "[0-9a-f]"
	glob := strings.Repeat(hex, 8) + "-" + strings.Repeat(hex, 4) + "-4" + strings.Repeat(hex, 3) +
		"-[89ab]" + strings.Repeat(hex, 3) + "-" + strings.Repeat(hex, 12)
	return fmt.Sprintf("typeof(%s) = 'text' AND length(%s) = 36 AND length(CAST(%s AS BLOB)) = 36 AND %s GLOB '%s'", c, c, c, c, glob)
}

// keyRef expands the custody key reference predicate: `kr1_` then 26 base32 characters.
//
// The SQLite arm uses a NEGATED GLOB class over the tail rather than 26 positional
// classes, because `*[^a-z2-7]*` is exact once the width is pinned: with 30 characters AND
// 30 bytes and a fixed 4-character prefix the tail is exactly 26 characters, so "no
// character outside the alphabet" and "26 characters from the alphabet" are the same set.
//
// ⛔ THE BYTE LENGTH CLOSES THE SAME DEFECT AS IN instanceID, and here `substr()` is the
// extra mechanism that made it invisible: the alphabet tail was read with `substr(c, 5)`,
// which stops at an embedded NUL, so the 26 characters examined were the canonical ones and
// the suffix bytes were never looked at. `kr1_` + 26 legal characters + NUL + a suffix
// satisfied the prefix equality, the character length and the negated class at once —
// measured on the actual driver, accepted, and persisted. This column carries a UNIQUE
// constraint whose job is to stop one key reference being recorded for two generations, so a
// forgeable suffix defeated exactly that.
//
// The pair is load-bearing in both directions for the same reason as above: the retained
// character term refuses a value padded to 30 bytes by a NUL, and the added byte term refuses
// a canonical value extended past 30 bytes. PostgreSQL is unchanged.
func (p finOpsCustodyPredicates) keyRef(c string) string {
	if !p.sqlite {
		return fmt.Sprintf("%s OPERATOR(pg_catalog.~) '^kr1_[a-z2-7]{26}$'", c)
	}
	return fmt.Sprintf("typeof(%s) = 'text' AND length(%s) = 30 AND length(CAST(%s AS BLOB)) = 30 AND substr(%s, 1, 4) = 'kr1_' AND substr(%s, 5) NOT GLOB '*[^a-z2-7]*'", c, c, c, c, c)
}

// finOpsCustodyTransitionRowCheck is the per-transition row predicate of §3.2.
//
// It is ROW-LOCAL and IDENTICAL on both engines, and it is one OR of seven conjunctions,
// each opening with an equality on the NOT NULL `transition` column. That opening is what
// makes the whole disjunction total rather than merely permissive: a row makes every
// non-matching conjunction FALSE, never NULL, so an unknown transition name satisfies
// nothing and is refused. Within the matching conjunction the revision-1 image CHECK
// guarantees the referenced `from_*` values are non-NULL whenever to_revision >= 2.
//
// `add_generation`, `confirm_generation` and `select_generation` are SCHEMA-PRESENT here
// and their commands are not constructed in this increment (U7). Declaring their legal
// shape now costs nothing and means the relation does not have to be altered to admit them
// later; the engine API, separately owned, refuses them.
func finOpsCustodyTransitionRowCheck() string {
	clauses := []string{
		`transition = 'begin_enrollment' AND to_revision = 1 AND from_state IS NULL
     AND to_state = 'enrolling' AND to_highest = 1 AND to_confirmed = 0 AND to_active = 0
     AND subject_generation = 1 AND writers_drained = 1`,
		`transition = 'confirm_enrollment' AND to_revision >= 2 AND from_state = 'enrolling'
     AND to_state = 'enrolled' AND from_highest = 1 AND to_highest = 1
     AND from_confirmed = 0 AND to_confirmed = 1 AND from_active = 0 AND to_active = 0
     AND subject_generation = 1 AND writers_drained = 1`,
		`transition = 'abandon' AND to_revision >= 2 AND from_state IN ('enrolling', 'enrolled')
     AND to_state = 'abandoned' AND to_highest = from_highest AND to_confirmed = from_confirmed
     AND from_active = 0 AND to_active = 0 AND subject_generation = 0 AND writers_drained = 1`,
		`transition = 'activate' AND to_revision >= 2 AND from_state = 'enrolled'
     AND to_state = 'active' AND to_highest = from_highest AND to_confirmed = from_confirmed
     AND from_active = 0 AND subject_generation BETWEEN 1 AND from_confirmed
     AND to_active = subject_generation AND writers_upgraded = 1 AND writers_drained = 1`,
		`transition = 'add_generation' AND to_revision >= 2 AND from_state IN ('enrolled', 'active')
     AND to_state = from_state AND from_confirmed = from_highest AND to_highest = from_highest + 1
     AND to_confirmed = from_confirmed AND to_active = from_active
     AND subject_generation = to_highest AND writers_drained = 1`,
		`transition = 'confirm_generation' AND to_revision >= 2 AND from_state IN ('enrolled', 'active')
     AND to_state = from_state AND from_confirmed = from_highest - 1 AND to_highest = from_highest
     AND to_confirmed = from_highest AND to_active = from_active
     AND subject_generation = to_confirmed AND writers_drained = 1`,
		`transition = 'select_generation' AND to_revision >= 2 AND from_state = 'active'
     AND to_state = 'active' AND to_highest = from_highest AND to_confirmed = from_confirmed
     AND subject_generation > from_active AND subject_generation <= from_confirmed
     AND to_active = subject_generation AND writers_upgraded = 1 AND writers_drained = 1`,
	}
	return "(" + strings.Join(clauses, ")\n  OR (") + ")"
}

// finOpsCustodyRevisionOneImageCheck is the revision-1 before-image equivalence of §3.2
// decision 5.
//
// Written as three chained equivalences rather than as four nullability CHECKs, because
// the property is not "each column may be NULL" but "the four are NULL TOGETHER, and
// exactly in revision 1". `(to_revision = 1) = (from_state IS NULL)` rejects both a
// revision-1 row carrying a before-image and a revision-7 row missing one, with the same
// text, on both engines.
const finOpsCustodyRevisionOneImageCheck = `(to_revision = 1) = (from_state IS NULL)
  AND (from_state IS NULL) = (from_highest IS NULL)
  AND (from_state IS NULL) = (from_confirmed IS NULL)
  AND (from_state IS NULL) = (from_active IS NULL)`

// finOpsCustodyHeadCoherenceCheck is the C1 state/generation coherence of §3.1.
const finOpsCustodyHeadCoherenceCheck = `(state = 'enrolling' AND highest_generation = 1 AND confirmed_generation = 0 AND active_generation = 0)
  OR (state = 'enrolled' AND confirmed_generation >= 1 AND active_generation = 0)
  OR (state = 'active' AND confirmed_generation >= 1 AND active_generation >= 1)
  OR (state = 'abandoned' AND active_generation = 0)`
