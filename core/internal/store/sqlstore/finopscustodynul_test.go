// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// finopscustodynul_test.go closes one concrete canonical-identity defect in the v12 custody
// relations: on SQLite, `length()`, `substr()` and `GLOB` all stop at an embedded NUL, so a
// canonical UUID or key reference FOLLOWED BY a NUL and arbitrary suffix bytes satisfied
// every literal the contract's §2.3 and §3.3 predicates were written with.
//
// WHY THAT IS A CANONICAL-IDENTITY DEFECT AND NOT A COSMETIC ONE. `custody_instance_id` is
// the correlation key of all three relations: the order guards, the primary keys and the
// unique indexes all join on it. Two values that differ only in bytes AFTER a NUL are two
// DISTINCT keys to the storage engine and one identical value to every predicate meant to
// canonicalize them. The same holds for `custody_key_ref`, whose UNIQUE constraint is what
// stops one key reference being recorded for two generations.
//
// THE FIX IS TWO PREDICATES, and both are load-bearing in opposite directions:
//
//   - the retained TEXT length (`length(c) = 36` / `= 30`) refuses a value padded to the
//     declared BYTE width by a NUL — its pre-NUL character count falls short;
//   - the added BYTE length (`length(CAST(c AS BLOB)) = 36` / `= 30`) refuses a canonical
//     value EXTENDED past the declared width by a NUL and a suffix — its byte count
//     overshoots.
//
// Neither alone is sufficient, and the two together are exhaustive for NUL: a NUL at
// character position k makes the text length k−1 and the byte length at least k, so text
// length 36 with byte length 36 is reachable only with no NUL at all. The same pair also
// establishes CANONICAL ASCII WIDTH, because bytes equal to characters means every character
// is one byte.
//
// PostgreSQL predicates are UNCHANGED. Its `text` type does not admit a NUL at all, so the
// defect cannot exist there, and widening a predicate that has nothing to refuse would only
// make the two engines' texts harder to compare. No other canonical text constraint is
// widened by this correction.

// nulSuffixInstance is the canonical instance id followed by a NUL and a suffix: 36
// characters before the NUL, 43 bytes in total.
//
// This is THE defect value. Every literal the baseline predicate carried accepts it.
var nulSuffixInstance = custodyInstanceA + "\x00" + "evilsuf"

// nulPaddedInstance is 34 canonical characters, a NUL and one more byte: 36 bytes in total,
// 34 characters before the NUL.
//
// This is the value that shows why the TEXT length check is RETAINED rather than replaced. A
// byte-length check alone would accept it.
var nulPaddedInstance = custodyInstanceA[:34] + "\x00" + "z"

// nulSuffixKeyRef and nulPaddedKeyRef are the same pair for the 30-byte key reference.
var (
	nulSuffixKeyRef = custodyKeyRefG1 + "\x00" + "evil"
	nulPaddedKeyRef = custodyKeyRefG1[:28] + "\x00" + "z"
)

// TestSQLiteDriverTransmitsEmbeddedNUL is the MEASUREMENT the rest of this file depends on.
//
// It is not a constraint test. It establishes three facts about the actual driver and engine,
// because without them every assertion below could pass for the wrong reason — a driver that
// truncated the bound string at the NUL would make the defect value indistinguishable from
// the canonical one, and the refusals would prove nothing:
//
//  1. the driver transmits and the engine STORES the bytes after a NUL;
//  2. `length()` on that stored text stops at the NUL, which is the defect's mechanism;
//  3. `length(CAST(x AS BLOB))` does not, which is what the fix relies on.
func TestSQLiteDriverTransmitsEmbeddedNUL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := emptyCustodySQLite(t)

	if _, err := db.ExecContext(ctx, "CREATE TABLE nul_probe (v TEXT COLLATE BINARY)"); err != nil {
		t.Fatalf("create the probe relation: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO nul_probe(v) VALUES (?)", nulSuffixInstance); err != nil {
		t.Fatalf("bind a NUL-bearing string: %v", err)
	}

	var class string
	var textLen, byteLen int
	if err := db.QueryRowContext(ctx,
		"SELECT typeof(v), length(v), length(CAST(v AS BLOB)) FROM nul_probe").Scan(&class, &textLen, &byteLen); err != nil {
		t.Fatalf("measure the stored value: %v", err)
	}

	if class != "text" {
		t.Errorf("the stored value has class %q, want text", class)
	}
	if textLen != 36 {
		t.Errorf("length() reports %d characters, want 36 — the defect requires it to stop at the NUL", textLen)
	}
	if byteLen != len(nulSuffixInstance) {
		t.Fatalf("the engine stored %d bytes, want the %d transmitted: the driver truncated at the NUL and every assertion below would be vacuous",
			byteLen, len(nulSuffixInstance))
	}
	if byteLen == textLen {
		t.Fatal("byte length equals character length, so no NUL survived transmission")
	}

	// And the bytes after the NUL are the ones sent, not zero padding. This is what makes
	// the defect an identity forgery rather than a length quirk.
	var round string
	if err := db.QueryRowContext(ctx, "SELECT v FROM nul_probe").Scan(&round); err != nil {
		t.Fatalf("read the value back: %v", err)
	}
	if round != nulSuffixInstance {
		t.Errorf("the value round-tripped as %q, want %q", round, nulSuffixInstance)
	}
}

// TestSQLiteCustodyRefusesEmbeddedNULInInstanceID is the instance-id leg.
//
// The refusals come FIRST and the canonical positive LAST, because the order guard's
// revision-1 precondition is about the live head and not about the journal: a refused insert
// leaves nothing behind, so the accepted row at the end is refused-or-accepted on its own
// predicate rather than on the preceding cases' residue.
func TestSQLiteCustodyRefusesEmbeddedNULInInstanceID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)
	rel := directoryWriterRelation(dia, dialect.ControlCustodyTransitionTable)

	for _, tc := range []struct {
		name    string
		id      string
		refused string
	}{
		{
			name: "canonical id then NUL then a suffix",
			id:   nulSuffixInstance,
			// 36 characters before the NUL, 43 bytes: the text length and the GLOB
			// both see only the canonical prefix.
			refused: "the added byte length",
		},
		{
			name:    "canonical prefix padded to 36 bytes by a NUL",
			id:      nulPaddedInstance,
			refused: "the retained text length",
		},
		{
			name:    "a single NUL appended",
			id:      custodyInstanceA + "\x00",
			refused: "the added byte length",
		},
		{
			name:    "a leading NUL before the canonical id",
			id:      "\x00" + custodyInstanceA,
			refused: "the retained text length (zero characters precede the NUL)",
		},
		{
			name:    "NUL in place of the final canonical character",
			id:      custodyInstanceA[:35] + "\x00",
			refused: "the retained text length",
		},
	} {
		row := beginEnrollmentRow(custodyInstanceA)
		row["custody_instance_id"] = tc.id
		if err := insertTransition(ctx, db, dia, row); err == nil {
			t.Errorf("%s: must be refused (%s), was accepted", tc.name, tc.refused)
		}
		// PERSISTED STATE AFTER THE REFUSAL. A CHECK refusal must leave no row, and
		// asserting it is what separates "the statement returned an error" from "the
		// statement had no effect" — a distinction a BEFORE trigger that raised after a
		// partial write would not preserve.
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+rel).Scan(&rows); err != nil {
			t.Fatalf("%s: count the journal: %v", tc.name, err)
		}
		if rows != 0 {
			t.Fatalf("%s: the refused insert persisted %d rows", tc.name, rows)
		}
	}

	// The canonical positive is UNCHANGED: the correction refuses NUL, it does not
	// narrow the accepted set.
	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("the canonical instance id must still be accepted: %v", err)
	}
	var stored int
	if err := db.QueryRowContext(ctx, "SELECT length(CAST(custody_instance_id AS BLOB)) FROM "+rel).Scan(&stored); err != nil {
		t.Fatalf("measure the accepted id: %v", err)
	}
	if stored != 36 {
		t.Errorf("the accepted canonical id occupies %d bytes, want exactly 36", stored)
	}
}

// TestSQLiteCustodyRefusesEmbeddedNULInKeyRef is the key-reference leg.
//
// `substr()` is the extra mechanism here: the baseline predicate read the alphabet tail with
// `substr(c, 5)`, which also stops at the NUL, so the 26 characters it examined were the
// canonical ones and the suffix was never looked at.
func TestSQLiteCustodyRefusesEmbeddedNULInKeyRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)
	rel := directoryWriterRelation(dia, dialect.ControlCustodyProofTable)

	// One legal journal row, so the proof's order guard has its minting transition and the
	// refusals below are about the key reference rather than about ordering.
	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("rev 1: %v", err)
	}

	for _, tc := range []struct{ name, keyRef, refused string }{
		{"canonical key ref then NUL then a suffix", nulSuffixKeyRef, "the added byte length"},
		{"canonical prefix padded to 30 bytes by a NUL", nulPaddedKeyRef, "the retained text length"},
		{"a single NUL appended", custodyKeyRefG1 + "\x00", "the added byte length"},
		{"a leading NUL before the canonical key ref", "\x00" + custodyKeyRefG1, "the retained text length"},
		{"NUL in place of the final alphabet character", custodyKeyRefG1[:29] + "\x00", "the retained text length"},
	} {
		if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, tc.keyRef, "enrollment"); err == nil {
			t.Errorf("%s: must be refused (%s), was accepted", tc.name, tc.refused)
		}
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+rel).Scan(&rows); err != nil {
			t.Fatalf("%s: count the proofs: %v", tc.name, err)
		}
		if rows != 0 {
			t.Fatalf("%s: the refused insert persisted %d rows", tc.name, rows)
		}
	}

	if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "enrollment"); err != nil {
		t.Fatalf("the canonical key reference must still be accepted: %v", err)
	}
	var stored int
	if err := db.QueryRowContext(ctx, "SELECT length(CAST(custody_key_ref AS BLOB)) FROM "+rel).Scan(&stored); err != nil {
		t.Fatalf("measure the accepted key ref: %v", err)
	}
	if stored != 30 {
		t.Errorf("the accepted canonical key ref occupies %d bytes, want exactly 30", stored)
	}
}

// TestSQLiteCustodyPreservesNonNULCanonicalCases re-runs the malformed and multibyte cases
// the correction must NOT have disturbed.
//
// It exists because adding a byte-length term is exactly the kind of change that can make a
// predicate refuse for a new reason and stop exercising the old one. The multibyte cases are
// named separately from the ASCII malformed ones: a 36-CHARACTER id containing a multibyte
// rune now fails the byte-length term as well as the GLOB alphabet, and both are correct
// refusals of the same value, so the test asserts refusal without claiming which term fired.
func TestSQLiteCustodyPreservesNonNULCanonicalCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dia := openCustodySQLite(t)

	for _, tc := range []struct{ name, id string }{
		// ASCII malformed, unchanged from the foundation's cases.
		{"uppercase", strings.ToUpper(custodyInstanceA)},
		{"version nibble not 4", "7f3a1c2e-4b5d-1e6f-8a9b-0c1d2e3f4a5b"},
		{"variant class out of range", "7f3a1c2e-4b5d-4e6f-7a9b-0c1d2e3f4a5b"},
		{"non-hex character", "7f3a1c2g-4b5d-4e6f-8a9b-0c1d2e3f4a5b"},
		{"hyphen misplaced", "7f3a1c2e4-b5d-4e6f-8a9b-0c1d2e3f4a5b"},
		{"one character short", custodyInstanceA[:35]},
		{"one character long", custodyInstanceA + "a"},
		// Multibyte: 36 characters, more than 36 bytes. Refused before the correction by
		// the GLOB alphabet and after it by that and the byte width.
		{"multibyte in a hex position", "7f3a1c2é-4b5d-4e6f-8a9b-0c1d2e3f4a5b"},
		{"multibyte replacing a hyphen", "7f3a1c2e‐4b5d-4e6f-8a9b-0c1d2e3f4a5b"},
		// A whitespace suffix rather than a NUL: the ordinary overlong case, which the
		// text length already refused and still does.
		{"trailing space", custodyInstanceA + " "},
	} {
		row := beginEnrollmentRow(custodyInstanceA)
		row["custody_instance_id"] = tc.id
		if err := insertTransition(ctx, db, dia, row); err == nil {
			t.Errorf("%s: must be refused, was accepted", tc.name)
		}
	}

	for _, tc := range []struct{ name, keyRef string }{
		{"wrong prefix", "kr2_abcdefghijklmnopqrstuvwxyz"},
		{"base32 alphabet violated: 0", "kr1_0bcdefghijklmnopqrstuvwxyz"},
		{"uppercase", strings.ToUpper(custodyKeyRefG1)},
		{"too short", custodyKeyRefG1[:29]},
		{"too long", custodyKeyRefG1 + "a"},
		{"multibyte in the alphabet tail", "kr1_abcdefghijklmnopqrstuvwxé"},
	} {
		if err := insertProof(ctx, db, dia, custodyInstanceA, 1, 1, tc.keyRef, "enrollment"); err == nil {
			t.Errorf("key ref %s: must be refused, was accepted", tc.name)
		}
	}

	// Control positive, after every refusal.
	if err := insertTransition(ctx, db, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("the canonical row must still be accepted: %v", err)
	}
}
