// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"strings"
	"testing"
)

// TestLockPlanValidateRefusesUnsafeShapes pins the three properties that make a plan
// executable rather than merely declared.
//
// Each row is a plan that would run perfectly well and produce a defect later, which
// is why this is checked before any statement is issued rather than discovered at
// the deadlock.
func TestLockPlanValidateRefusesUnsafeShapes(t *testing.T) {
	t.Parallel()

	ok := lockPlan{
		Metadata: []plannedLock{
			{Schema: "olv", Name: "a_receipts", Mode: lockModeRowExclusive},
			{Schema: "olv", Name: "b_manifest", Mode: lockModeRowExclusive},
		},
		Target:          plannedLock{Schema: "olv", Name: "target", Mode: lockModeShareRowExclusive},
		TargetStatement: `LOCK TABLE ONLY "olv"."target" IN SHARE ROW EXCLUSIVE MODE`,
	}
	if err := ok.validate(); err != nil {
		t.Fatalf("a well-formed plan was refused: %v", err)
	}

	cases := []struct {
		name string
		plan lockPlan
		want string
		why  string
	}{
		{
			name: "metadata out of order",
			plan: lockPlan{
				Metadata: []plannedLock{
					{Schema: "olv", Name: "b_manifest", Mode: lockModeRowExclusive},
					{Schema: "olv", Name: "a_receipts", Mode: lockModeRowExclusive},
				},
				Target:          ok.Target,
				TargetStatement: ok.TargetStatement,
			},
			want: "total order",
			why:  "a prefix that is common but differently ordered prevents no cycle at all, and the difference is invisible at a glance",
		},
		{
			name: "the same relation twice",
			plan: lockPlan{
				Metadata: []plannedLock{
					{Schema: "olv", Name: "a_receipts", Mode: lockModeRowExclusive},
					{Schema: "olv", Name: "a_receipts", Mode: lockModeExclusive},
				},
				Target:          ok.Target,
				TargetStatement: ok.TargetStatement,
			},
			want: "twice",
			why:  "the second entry is either redundant or an escalation, and escalating inside a transaction is the documented deadlock recipe",
		},
		{
			name: "metadata stronger than the target",
			plan: lockPlan{
				Metadata:        []plannedLock{{Schema: "olv", Name: "a_receipts", Mode: lockModeAccessExclusive}},
				Target:          ok.Target,
				TargetStatement: ok.TargetStatement,
			},
			want: "escalates",
			why:  "the target must be taken last AND at the maximum, or the unit escalates on the relation with real concurrent writers",
		},
		{
			name: "the target also appears in the prefix",
			plan: lockPlan{
				Metadata:        []plannedLock{{Schema: "olv", Name: "target", Mode: lockModeRowExclusive}},
				Target:          ok.Target,
				TargetStatement: ok.TargetStatement,
			},
			want: "metadata prefix",
			why:  "locking the target early takes it at the wrong mode and then escalates to the right one",
		},
		{
			name: "no target statement",
			plan: lockPlan{Metadata: ok.Metadata, Target: ok.Target},
			want: "no target statement",
			why:  "a plan with nothing to run is a plan that silently locks nothing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.plan.validate()
			if err == nil {
				t.Fatalf("accepted an unsafe plan — %s", tc.why)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused with %q, want it to mention %q so the diagnosis names the property", err, tc.want)
			}
		})
	}
}

// TestLockModeCoverageFollowsTheConflictMatrix replaces a test that passed BY
// CONSTRUCTION.
//
// The old one built a list in iota order and asserted each element was less than the
// next. That is true of any enum, proves nothing about PostgreSQL, and it certified a
// comparison that was simply wrong: table-level lock modes are defined by the SET OF
// MODES THEY CONFLICT WITH, not by a scalar strength, and two of them are genuinely
// incomparable. SHARE UPDATE EXCLUSIVE conflicts with itself and with SHARE; SHARE
// permits another SHARE but conflicts with ROW EXCLUSIVE. Neither conflict set
// contains the other, so neither mode covers the other — and the ordinal comparison
// said one did.
//
// Every row below is read off the published Table-Level Lock Modes matrix.
func TestLockModeCoverageFollowsTheConflictMatrix(t *testing.T) {
	t.Parallel()

	// The pair that broke the ordering. Ordinally SHARE UPDATE EXCLUSIVE < SHARE, so
	// the old comparison authorized taking SHARE UPDATE EXCLUSIVE against a plan that
	// declared SHARE — a mode the plan does not cover at all.
	if lockModeShare.covers(lockModeShareUpdateExclusive) {
		t.Error("SHARE was reported as covering SHARE UPDATE EXCLUSIVE: it does not conflict with SHARE UPDATE EXCLUSIVE's self-conflict, so a unit could take a mode its plan never authorized")
	}
	if lockModeShareUpdateExclusive.covers(lockModeShare) {
		t.Error("SHARE UPDATE EXCLUSIVE was reported as covering SHARE: the two are incomparable, and pretending otherwise is what the ordinal comparison did")
	}

	cases := []struct {
		declared, held lockMode
		want           bool
		why            string
	}{
		{lockModeAccessExclusive, lockModeAccessShare, true, "ACCESS EXCLUSIVE conflicts with everything, so it covers every mode"},
		{lockModeAccessExclusive, lockModeShareUpdateExclusive, true, "same"},
		{lockModeAccessShare, lockModeAccessExclusive, false, "the weakest mode cannot authorize the strongest"},
		{lockModeRowExclusive, lockModeRowExclusive, true, "a mode always covers itself"},
		{lockModeShareRowExclusive, lockModeShare, true, "SHARE ROW EXCLUSIVE's conflict set contains SHARE's"},
		{lockModeShare, lockModeShareRowExclusive, false, "and not the other way round"},
		{lockModeExclusive, lockModeShareRowExclusive, true, "EXCLUSIVE conflicts with everything but ACCESS SHARE"},
		{lockModeRowExclusive, lockModeShare, false, "ROW EXCLUSIVE does not conflict with itself; SHARE does not either, but SHARE conflicts with ROW EXCLUSIVE"},
	}
	for _, tc := range cases {
		if got := tc.declared.covers(tc.held); got != tc.want {
			t.Errorf("%s covers %s = %v, want %v — %s", tc.declared, tc.held, got, tc.want, tc.why)
		}
	}

	// The matrix must be SYMMETRIC, which is a property of conflicts and a cheap way
	// to catch a transcription slip in a table nobody re-reads.
	for a, ac := range lockModeConflicts {
		for b := range ac {
			if !lockModeConflicts[b][a] {
				t.Errorf("%s conflicts with %s but not the reverse: lock conflicts are symmetric, so one of the two rows is mistranscribed", a, b)
			}
		}
	}

	// And every mode must be spellable both ways, or the comparison silently drops
	// whichever end is missing.
	if len(lockModeConflicts) != len(lockModeSQL) {
		t.Errorf("the conflict matrix has %d rows for %d modes: a mode with no row covers nothing and is covered by nothing, silently",
			len(lockModeConflicts), len(lockModeSQL))
	}
	if len(lockModeFromCatalog) != len(lockModeSQL) {
		t.Errorf("the catalog mapping has %d entries for %d modes", len(lockModeFromCatalog), len(lockModeSQL))
	}
	for raw, m := range lockModeFromCatalog {
		if !m.valid() {
			t.Errorf("catalog mode %q maps to an unspellable lock mode", raw)
		}
	}
}

// TestLockModeValidRejectsWhatIsNotAMode pins the enum check.
//
// Without it a plan declaring lockMode(8) validated cleanly and then covered every
// real mode, because the comparison was numeric and every real mode is numerically
// smaller — a footprint check that authorized everything while reading as though it
// authorized one thing.
func TestLockModeValidRejectsWhatIsNotAMode(t *testing.T) {
	t.Parallel()
	for _, m := range []lockMode{-1, 8, 99} {
		if m.valid() {
			t.Errorf("lockMode(%d) reported valid", int(m))
		}
		if m.covers(lockModeAccessShare) {
			t.Errorf("lockMode(%d) was reported as covering a real mode: an out-of-range declaration must authorize nothing, not everything", int(m))
		}
		if lockModeAccessExclusive.covers(m) {
			t.Errorf("a real mode was reported as covering lockMode(%d): an unknown held mode must never be waved through", int(m))
		}
	}
}

// TestLockPlanValidateRejectsAnUnknownMode pins that the plan refuses one before any
// statement runs, in the target and in the metadata prefix alike.
func TestLockPlanValidateRejectsAnUnknownMode(t *testing.T) {
	t.Parallel()
	base := lockPlan{
		Target:          plannedLock{Schema: "olv", Name: "target", Mode: lockModeShareRowExclusive},
		TargetStatement: `LOCK TABLE ONLY "olv"."target" IN SHARE ROW EXCLUSIVE MODE`,
	}

	bad := base
	bad.Target.Mode = lockMode(8)
	if err := bad.validate(); err == nil {
		t.Error("a plan whose TARGET declares an unknown mode validated cleanly")
	}

	bad2 := base
	bad2.Metadata = []plannedLock{{Schema: "olv", Name: "a", Mode: lockMode(42)}}
	if err := bad2.validate(); err == nil {
		t.Error("a plan whose METADATA declares an unknown mode validated cleanly")
	}
}

// TestPlannedLockIdentityIsNotCollidable pins that two different relations cannot
// share one comparison key.
//
// A dot was not injective. PostgreSQL identifiers may contain any character but NUL
// when quoted, so "r3.fp"."target" and "r3"."fp.target" are two different relations
// that both flattened to r3.fp.target — and that string is what authorizes a lock.
func TestPlannedLockIdentityIsNotCollidable(t *testing.T) {
	t.Parallel()
	a := plannedLock{Schema: "r3.fp", Name: "target", Mode: lockModeRowExclusive}
	b := plannedLock{Schema: "r3", Name: "fp.target", Mode: lockModeRowExclusive}
	if a.relation() == b.relation() {
		t.Errorf("%q and %q share the identity %q: a lock on one is then authorized by a declaration of the other",
			a.displayRelation(), b.displayRelation(), a.relation())
	}

	plan := lockPlan{Target: a, TargetStatement: a.lockStatement()}
	if _, ok := plan.declared(b.relation()); ok {
		t.Error("a plan declaring one relation reported the OTHER as declared")
	}
	if _, ok := plan.declared(a.relation()); !ok {
		t.Error("a plan did not recognize its own target")
	}
}

// TestPlannedLockStatementLocksOnlyTheNamedRelation pins the ONLY keyword.
//
// Without it a partitioned parent pulls in every partition, so one declared relation
// becomes an undeclared footprint the size of the table's history — and the footprint
// check would then refuse the very unit that generated the plan.
func TestPlannedLockStatementLocksOnlyTheNamedRelation(t *testing.T) {
	t.Parallel()
	got := plannedLock{Schema: "olv", Name: "evidence", Mode: lockModeShareRowExclusive}.lockStatement()
	want := `LOCK TABLE ONLY "olv"."evidence" IN SHARE ROW EXCLUSIVE MODE`
	if got != want {
		t.Errorf("lockStatement() = %q, want %q", got, want)
	}
}

// TestLockPlanRejectsANulInAnIdentifier pins the constructor-side half of the
// identity guarantee.
//
// NUL is what makes the comparison key injective, and PostgreSQL cannot put one in an
// identifier — but a plannedLock is built in GO, where a string may hold anything. A
// schema of "\x00oid" lands in the namespace reserved for relations whose catalog row
// is gone, so a lock on a dropped relation could be authorized by a declaration that
// spelled its OID.
//
// Mutation that must turn this red: drop the NUL check from validate().
func TestLockPlanRejectsANulInAnIdentifier(t *testing.T) {
	t.Parallel()
	base := lockPlan{
		Target:          plannedLock{Schema: "olv", Name: "target", Mode: lockModeRowExclusive},
		TargetStatement: `LOCK TABLE ONLY "olv"."target" IN ROW EXCLUSIVE MODE`,
	}
	for _, tc := range []struct {
		name string
		plan lockPlan
	}{
		{"target schema", func() lockPlan { p := base; p.Target.Schema = "\x00oid"; return p }()},
		{"target name", func() lockPlan { p := base; p.Target.Name = "a\x00b"; return p }()},
		{"metadata", func() lockPlan {
			p := base
			p.Metadata = []plannedLock{{Schema: "\x00oid", Name: "x", Mode: lockModeRowExclusive}}
			return p
		}()},
	} {
		if err := tc.plan.validate(); err == nil {
			t.Errorf("%s: a plan carrying a NUL byte validated cleanly; that byte is the separator the whole identity depends on, and the OID fallback namespace starts with it", tc.name)
		}
	}
}

// TestLockModeConflictMatrixMatchesTheGoldenTable is the 64/64 check that symmetry
// cannot give.
//
// The symmetry assertion catches ONE mistranscribed cell. It cannot catch two
// complementary ones — writing both "A conflicts with B" and "B conflicts with A"
// when neither does is perfectly symmetric and completely wrong. Only the full table,
// written out independently, closes that.
//
// Rows and columns follow PostgreSQL's Table-Level Lock Modes matrix, weakest to
// strongest. 'X' marks a conflict.
func TestLockModeConflictMatrixMatchesTheGoldenTable(t *testing.T) {
	t.Parallel()
	order := []lockMode{
		lockModeAccessShare, lockModeRowShare, lockModeRowExclusive,
		lockModeShareUpdateExclusive, lockModeShare, lockModeShareRowExclusive,
		lockModeExclusive, lockModeAccessExclusive,
	}
	//                      AS   RS   RE  SUE   S  SRE   E  AE
	golden := []string{
		/* ACCESS SHARE   */ ".......X",
		/* ROW SHARE      */ "......XX",
		/* ROW EXCLUSIVE  */ "....XXXX",
		/* SHARE UPD EXCL */ "...XXXXX",
		/* SHARE          */ "..XX.XXX",
		/* SHARE ROW EXCL */ "..XXXXXX",
		/* EXCLUSIVE      */ ".XXXXXXX",
		/* ACCESS EXCL    */ "XXXXXXXX",
	}
	if len(golden) != len(order) {
		t.Fatalf("golden table has %d rows for %d modes", len(golden), len(order))
	}

	cells := 0
	for i, row := range golden {
		if len(row) != len(order) {
			t.Fatalf("golden row %d has %d cells, want %d", i, len(row), len(order))
		}
		for j, c := range row {
			cells++
			want := c == 'X'
			got := lockModeConflicts[order[i]][order[j]]
			if got != want {
				t.Errorf("conflict(%s, %s) = %v, want %v — a single wrong cell silently authorizes or forbids a mode",
					order[i], order[j], got, want)
			}
		}
	}
	if cells != 64 {
		t.Errorf("checked %d cells, want 64", cells)
	}
}

// TestLockPlanRefusesAnythingButTheGeneratedLockStatement is the acquisition statement's
// real guarantee, and a prefix check was never it.
//
// The plan calls TargetStatement "the single statement that takes the target lock" and
// validate() promised it must be inert. What it actually checked was a case-insensitive
// `LOCK TABLE` PREFIX — and pgx accepts a simple query containing several statements, so a
// second, mutating one rode along behind the lock and ran BEFORE the authoritative
// precondition re-read. Both footprint checks passed, because the extra statement touched
// only the relation and mode already declared; a footprint can compare relations and modes
// but cannot attribute which fragment of a string did the mutating. Measured:
//
//	ROUND9_MULTI_STATEMENT_ACQUIRE|validate=<nil>|run_err=<nil>|hidden_rows=1|receipts=1
//
// The hidden row was durable.
//
// Scanning for a semicolon would not have closed it either: that is a guess at a SQL
// parser, and comments, dollar-quoted bodies and string literals all contain semicolons
// legally. So the statement must be EXACTLY the one the plan generates for its own target
// and mode — nothing to parse, nothing to smuggle.
//
// Mutation that must turn this red: go back to a HasPrefix check.
func TestLockPlanRefusesAnythingButTheGeneratedLockStatement(t *testing.T) {
	t.Parallel()

	target := plannedLock{Schema: "public", Name: "t", Mode: lockModeRowExclusive}
	generated := target.lockStatement()

	// The premise: what the plan generates is accepted, so every refusal below is about
	// the statement and not about the fixture.
	if err := (lockPlan{Target: target, TargetStatement: generated}).validate(); err != nil {
		t.Fatalf("the generated statement %q was refused: %v", generated, err)
	}

	for _, tc := range []struct {
		name string
		stmt string
		why  string
	}{
		{
			name: "a second statement behind the lock",
			stmt: generated + `; INSERT INTO "public"."t" VALUES (1)`,
			why:  "this is the measured hole: durable work outside Execute, before the precondition is re-read",
		},
		{
			name: "a second statement hidden behind a comment",
			stmt: generated + ` -- harmless
; UPDATE "public"."t" SET id = 2`,
			why: "a semicolon scan would have to understand comments to catch this",
		},
		{
			name: "a mutating statement that merely starts with the words",
			stmt: `LOCK TABLE ONLY "public"."t" IN ROW EXCLUSIVE MODE, "public"."other" IN ACCESS EXCLUSIVE MODE`,
			why:  "a prefix check accepts a second relation the plan never declared",
		},
		{
			name: "the right statement for the wrong mode",
			stmt: `LOCK TABLE ONLY "public"."t" IN ACCESS EXCLUSIVE MODE`,
			why:  "the declaration is a claim about the protection the unit runs under",
		},
		{
			name: "the right statement without ONLY",
			stmt: `LOCK TABLE "public"."t" IN ROW EXCLUSIVE MODE`,
			why:  "without ONLY a partitioned parent pulls in every partition, which is an undeclared footprint",
		},
		{
			name: "a statement that is not a lock at all",
			stmt: `ALTER TABLE "public"."t" ENABLE ALWAYS TRIGGER g`,
			why:  "a mutating acquisition destroys the evidence of its own precondition",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := (lockPlan{Target: target, TargetStatement: tc.stmt}).validate()
			if err == nil {
				t.Fatalf("validate() accepted %q; %s", tc.stmt, tc.why)
			}
			if !strings.Contains(err.Error(), "the only statement it may issue") {
				t.Errorf("validate() refused %q with an unrelated message: %v", tc.stmt, err)
			}
		})
	}
}
