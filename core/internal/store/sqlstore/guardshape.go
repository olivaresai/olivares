// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardshape.go answers the question the preflight used to skip: not "is there a table by
// this name" but "is it THE table".
//
// The old probe called any relation with at least one column present. A v6 tracking row plus
// three homonymous relations without their unique constraints therefore classified as a
// finished bootstrap — and the ledger's whole argument rests on those uniquenesses, because
// the chains are ordered by an ordinal that nothing else stops from repeating.
//
// TWO ENGINES, TWO MECHANISMS, and the difference is not a compromise:
//
//   - SQLite stores the CREATE statement VERBATIM in sqlite_master, so the comparison is
//     between the text this binary renders and the text the database holds. Nothing is
//     approximated and nothing is declared twice.
//   - PostgreSQL keeps no such text, so the shape is DECLARED (dialect.GuardControlPlaneShapePostgres)
//     and projected from the catalog. Every declared value was measured against a real server.
//
// WHAT IS ENFORCED WHERE, stated because one layer is version-bound and the other is not:
//
//   - Structure — relkind, persistence, partitioning, row-level security, columns with their
//     types and nullability and the absence of defaults/identity/generation, the primary key,
//     the set of unique tuples, the named indexes with their validity/readiness/liveness and
//     access method, and the COLUMNS each CHECK constrains — is catalog fact. It is compared on
//     every supported major.
//   - The LITERALS each CHECK predicate mentions are compared on every supported major too, and
//     that layer exists because columns alone were a semantic hole rather than a weaker
//     rendering: `kind IN ('pending-opened', …)` and `kind IS NOT NULL` constrain the same
//     column, so on an unverified major the second compared EQUAL to the first — and the fold's
//     entire vocabulary argument rests on that CHECK. Literals are the values THIS binary wrote
//     into the DDL, so they do not move with the deparser. See renderCheck.
//   - The CHECK PREDICATE text is the deparser's rendering, which is a property of the server
//     version. It is compared on every CERTIFIED major — postgresMajorCertified, over
//     certifiedPostgresMajors, which today covers the whole supported range. Pinning an
//     unmeasured rendering would refuse to boot a correct database, which is a worse failure
//     than the one it would prevent. (This paragraph named the scalar verifiedPostgresMajor
//     long after production had moved to the set; verifiedPostgresMajor is now only the major
//     the DECLARED shape was generated FROM, which is a different question.)
//
// WHAT THE MIDDLE LAYER STILL DOES NOT CATCH, stated because the alternative is implying it is
// complete: a predicate that keeps every literal and changes the OPERATOR or the connective has
// the same columns and the same literals. Closing that needs the per-major certified deparse
// the amendment records as owed, and it is why certifying a major is not paperwork — widening
// verifiedPostgresMajor is what turns the strongest layer on for that major.

// verifyGuardControlPlaneShape refuses to proceed when the live control plane is not the
// declared one.
//
// It runs in TWO places on purpose: in the preflight, where a `complete` disposition is about
// to authorize skipping the migration that would have created these relations; and after the
// migrations, where it covers the boot that just created them. The first is what stops a
// foreign schema being adopted; the second is what stops a server having quietly created
// something other than what was asked for.
func verifyGuardControlPlaneShape(ctx context.Context, q rowQuerier, dia dialect.Dialect) error {
	if dia.Name() == store.EnginePostgres {
		return verifyGuardShapePostgres(ctx, q, dia)
	}
	return verifyGuardShapeSQLite(ctx, q, dia)
}

// ---------------------------------------------------------------------------------------
// SQLite: the stored CREATE text, compared with the rendered one.
// ---------------------------------------------------------------------------------------

// verifyGuardShapeSQLite compares sqlite_master against the statements this binary renders.
//
// Both directions are checked. A declared object that is absent or differs is a refusal, and
// so is an object attached to one of the three relations that this binary never rendered: an
// extra trigger on an append-only log could rewrite what an INSERT stores, and an extra index
// is at best a schema nobody declared. Deny-closed means the unexpected is refused, not
// tolerated because it looked harmless.
//
// The implicit indexes SQLite creates for PRIMARY KEY and UNIQUE (sqlite_autoindex_*) carry a
// NULL sql and are skipped — they are not separate objects, they are the CREATE TABLE text
// already being compared.
func verifyGuardShapeSQLite(ctx context.Context, q rowQuerier, dia dialect.Dialect) error {
	want := map[string]string{}
	for _, stmt := range dia.GuardControlPlaneStmts() {
		name, ok := sqliteObjectName(stmt)
		if !ok {
			return fmt.Errorf("sqlstore: guard control plane shape: this binary renders a statement whose object cannot be named: %.60q", stmt)
		}
		if prev, dup := want[name]; dup {
			return fmt.Errorf("sqlstore: guard control plane shape: %q is rendered twice (%.40q and %.40q)", name, prev, stmt)
		}
		want[name] = stmt
	}

	tables := dialect.GuardControlPlaneTables()
	list, args := sqliteNameList(tables)
	// #nosec G202 -- `list` is sqliteNameList's output: ONLY "?,?,…" placeholders. Every name travels as a bound argument
	rows, err := q.QueryContext(ctx, `SELECT name, sql FROM sqlite_master
WHERE (tbl_name IN (`+list+`) OR name IN (`+list+`)) AND sql IS NOT NULL
ORDER BY name`, append(args, args...)...)
	if err != nil {
		return fmt.Errorf("sqlstore: guard control plane shape: read sqlite_master: %w", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var name, text string
		if err := rows.Scan(&name, &text); err != nil {
			return fmt.Errorf("sqlstore: guard control plane shape: read sqlite_master: %w", err)
		}
		got[name] = text
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: guard control plane shape: read sqlite_master: %w", err)
	}

	for _, name := range sortedKeys(want) {
		text, present := got[name]
		if !present {
			return fmt.Errorf("%w: the guard control plane is missing %q, which this binary creates as part of the same migration as the relations that are present",
				ErrGuardControlPlaneShapeDivergent, name)
		}
		if text != want[name] {
			return fmt.Errorf("%w: %q is stored as %q where this binary renders %q",
				ErrGuardControlPlaneShapeDivergent, name, text, want[name])
		}
	}
	for _, name := range sortedKeys(got) {
		if _, declared := want[name]; !declared {
			return fmt.Errorf("%w: %q is attached to the guard control plane and this binary never creates it; an object nobody declared can change what an append-only log stores",
				ErrGuardControlPlaneShapeDivergent, name)
		}
	}
	return nil
}

// sqliteObjectName extracts the object name from a CREATE statement this package rendered.
//
// It parses only the shapes this package emits — CREATE TABLE / CREATE INDEX / CREATE TRIGGER,
// with no IF NOT EXISTS and no schema qualification — and returns false for anything else, so
// a future statement in a form this cannot read fails loudly instead of comparing nothing.
func sqliteObjectName(stmt string) (string, bool) {
	fields := strings.Fields(stmt)
	if len(fields) < 3 || !strings.EqualFold(fields[0], "CREATE") {
		return "", false
	}
	switch {
	case strings.EqualFold(fields[1], "TABLE"), strings.EqualFold(fields[1], "INDEX"), strings.EqualFold(fields[1], "TRIGGER"):
		name := fields[2]
		// A CREATE TABLE's name may abut its opening parenthesis.
		if i := strings.IndexByte(name, '('); i > 0 {
			name = name[:i]
		}
		if name == "" {
			return "", false
		}
		return name, true
	default:
		return "", false
	}
}

// sqliteNameList renders '?' placeholders for a name list. SQLite keeps '?', so there is no
// rebinding to do and no name ever reaches the SQL text.
func sqliteNameList(names []string) (string, []any) {
	args := make([]any, len(names))
	ph := make([]string, len(names))
	for i, n := range names {
		ph[i], args[i] = "?", n
	}
	return strings.Join(ph, ","), args
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// closeRows closes a result set and reports the iteration error, which is the one an
// early-exit loop hides. A projection that stopped halfway must not be compared as though it
// were complete: a relation would then appear to be missing columns it has.
func closeRows(rows *sql.Rows, what string) error {
	err := rows.Err()
	cerr := rows.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("sqlstore: guard control plane shape: read %s: %w", what, err)
	}
	return nil
}

// ---------------------------------------------------------------------------------------
// PostgreSQL: the declared shape, projected from the catalog.
// ---------------------------------------------------------------------------------------

// observedRelation is one relation as the catalog answers for it.
type observedRelation struct {
	Found       bool
	Kind        string
	Persistence string
	IsPartition bool
	// HasChildren is true when some relation INHERITS from this one.
	//
	// See guardOnly for the measurement: a child inherits the columns and the CHECKs and
	// inherits neither the unique indexes nor the triggers, while a bare SELECT on the parent
	// reads its rows. Every read is now qualified with ONLY, so the child's rows no longer reach
	// a fold — and this is the other half, so the anomaly is REPORTED rather than stepped over.
	HasChildren          bool
	RowSecurity          bool
	ForceRowSecurity     bool
	Columns              []dialect.GuardColumnShape
	ColumnDefects        []string
	PrimaryKey           observedIndex
	UniqueKeys           []observedIndex
	Indexes              []observedNamedIndex
	Checks               []dialect.GuardCheckShape
	ForeignOrOtherChecks []string
}

// guardIndexDeviation is ONE way an index departs from the plain, immediately-enforced
// uniqueness this binary declares over exactly the attributes it names.
//
// IT CARRIES THE CATALOG COLUMN, and that is the whole reason the type exists. Each departure
// used to be a suffix concatenated onto the string the multiset comparison holds — and a
// declared tuple sorts BEFORE its annotated twin, so the report always took the "declared and
// absent" branch. MEASURED on 15.18, 16.14, 17.10 and 18.4: three of the four divergences round
// four built against a real server produced the IDENTICAL refusal,
//
//	this binary declares a uniquely-indexed tuple the database does not have: rollout_id,event_ordinal
//
// for an INCLUDE payload, a DEFERRABLE constraint and a non-deterministic collation. An operator
// reading that goes looking for a missing index; the index is there, enforcing another rule. A
// refusal that cannot tell an operator what to look at is a defect in the refusal, not a detail
// of its wording.
type guardIndexDeviation struct {
	// Property is the pg_catalog column the departure was read from, so whoever reads the
	// refusal knows exactly what to SELECT.
	Property string
	// Detail is what that column said, in the terms of what it costs.
	Detail string
}

// The parameterless deviations are values rather than constructors so that production and its
// regressions name the SAME constant. A test that spelled the text by hand would assert a
// message the projector might not produce — the "expectation derived from the thing under test"
// defect this campaign has already paid for once, in the shape table.
var (
	// THE FIRST WORDING OF THIS ONE SAID THE OPPOSITE OF WHAT THE SERVER DOES, and the contrast
	// caught it. It claimed PostgreSQL ignores an invalid index "for queries AND for constraint
	// enforcement". MEASURED on 16.14: with indisvalid forced false, a duplicate INSERT is still
	// REJECTED — `duplicate key value violates unique constraint "t1u"`. What indisvalid=false
	// actually means is narrower and worse to state loosely: the build may have aborted before
	// verifying the rows that already existed, so the uniqueness is unproven BACKWARDS while
	// still enforced forwards, and the planner will not use the index. An operator told "it
	// enforces nothing" would go looking for new duplicates and find none.
	guardIndexNotValid = guardIndexDeviation{"indisvalid",
		"false: a CREATE INDEX CONCURRENTLY that never finished. Measured on 16.14, new inserts ARE still checked, so the gap is backwards — the build may have aborted before verifying the rows that predate it, and the planner will not use it"}
	guardIndexNotReady = guardIndexDeviation{"indisready",
		"false: the index is not yet accepting inserts, so rows written while it is in this state are not checked against it"}
	guardIndexNotLive = guardIndexDeviation{"indislive",
		"false: the index is being dropped"}
	guardIndexPartial = guardIndexDeviation{"indpred",
		"present: the index is PARTIAL, so it enforces uniqueness only over the rows its predicate admits"}
	// AND THIS ONE WAS INVERTED. It said NULLS NOT DISTINCT makes such rows "no longer collide".
	// MEASURED on 16.14: two rows (1, NULL) under NULLS NOT DISTINCT COLLIDE — one survives —
	// while under the declared default both are accepted. The rule is STRICTER, not looser, and
	// the divergence that matters is that this database would refuse rows the engine expects to
	// be able to write. Saying it backwards sent a reader looking for an integrity hole where
	// the failure is an availability one.
	guardIndexNullsNotDistinct = guardIndexDeviation{"indnullsnotdistinct",
		"true: NULLS NOT DISTINCT, so two rows differing only in a NULL now COLLIDE where the declared default lets them coexist; this database would refuse rows the engine expects to write"}
	// MEASURED ON 16.14, because the first wording claimed the check happens "at COMMIT" and that
	// is only true of INITIALLY DEFERRED:
	//
	//	UNIQUE (x)                                condeferrable=f  ->  indimmediate=TRUE
	//	UNIQUE (x) DEFERRABLE INITIALLY IMMEDIATE condeferrable=t  ->  indimmediate=FALSE
	//	UNIQUE (x) DEFERRABLE INITIALLY DEFERRED  condeferrable=t  ->  indimmediate=FALSE
	//
	// indimmediate tracks condeferrable, NOT condeferred — so the projection catches both
	// deferrable variants and there is no gap. What was wrong was the consequence: an INITIALLY
	// IMMEDIATE constraint IS checked per statement until some transaction says otherwise, and
	// `SET CONSTRAINTS ... DEFERRED` is available to any of them. Measured: two duplicate rows
	// held inside such a transaction and visible to everything it read, with the COMMIT rejecting
	// them afterwards. A non-deferrable uniqueness refuses the deferral outright —
	// `ERROR: constraint "ua" is not deferrable` — which is the property the declaration wants.
	guardIndexDeferrable = guardIndexDeviation{"indimmediate",
		"false: the uniqueness is DEFERRABLE. Any transaction may then SET CONSTRAINTS ... DEFERRED and hold a duplicate for the rest of it, visible to everything that transaction reads, until the COMMIT rejects it; a non-deferrable uniqueness refuses the deferral outright"}
)

func guardIndexAccessMethod(am string) guardIndexDeviation {
	return guardIndexDeviation{"pg_am.amname",
		am + ", not btree: the equality this uniqueness rests on is a btree property"}
}

func guardIndexExpressions(expressions, total int) guardIndexDeviation {
	return guardIndexDeviation{"indnatts",
		fmt.Sprintf("%d of %d attributes are expressions, so the joined name list is not the indexed tuple", expressions, total)}
}

func guardIndexIncludePayload(payload, total int) guardIndexDeviation {
	return guardIndexDeviation{"indnkeyatts",
		fmt.Sprintf("%d of %d attributes are INCLUDE payload and enforce nothing", payload, total)}
}

func guardIndexNonDefaultOpclass(cols string) guardIndexDeviation {
	return guardIndexDeviation{"indclass",
		"a non-default operator class on " + cols + ": equality is that operator class's, not the type's default"}
}

func guardIndexDeclaredCollation(cols string) guardIndexDeviation {
	return guardIndexDeviation{"indcollation",
		"a collation other than the column's declared on " + cols}
}

func guardIndexNonDeterministicCollation(cols string) guardIndexDeviation {
	return guardIndexDeviation{"collisdeterministic",
		"false on " + cols + ": a non-deterministic collation makes values that are not equal byte for byte collide, which moves the equality this uniqueness is built on"}
}

func guardIndexNonDefaultOrdering(cols string) guardIndexDeviation {
	return guardIndexDeviation{"indoption",
		"a non-default ordering on " + cols}
}

// observedIndex is one index as the catalog answers for it.
type observedIndex struct {
	// Tuple is the joined attribute names, in the spelling a DECLARED tuple uses and free of
	// annotations. Keeping it clean is what lets a report PAIR an observed index with the
	// declared tuple it fails to be, instead of calling that tuple absent.
	//
	// It also removes a fragility the suffix encoding created: the expression check counts the
	// commas in this string, so it had to run before any annotation that could contain one.
	// Nothing is appended here now, so the order of the checks below no longer matters.
	Tuple string
	// Deviations is every departure from a plain uniqueness over exactly those attributes, in
	// the order they are read from the catalog.
	Deviations []guardIndexDeviation
}

// identity is the comparable form: two indexes are the same index exactly when these match.
//
// Production and its regressions render through this ONE function, so a test cannot assert a
// spelling the projector does not produce.
func (o observedIndex) identity() string {
	if len(o.Deviations) == 0 {
		return o.Tuple
	}
	return o.Tuple + " " + o.deviationText()
}

// deviationText names every departure, each tagged with the catalog column it was read from.
func (o observedIndex) deviationText() string {
	parts := make([]string, 0, len(o.Deviations))
	for _, d := range o.Deviations {
		parts = append(parts, "<"+d.Property+" "+d.Detail+">")
	}
	return strings.Join(parts, " ")
}

// observedNamedIndex is a named, non-unique index. The name belongs to its identity because the
// declaration names it — see dialect.GuardIndexShape.
type observedNamedIndex struct {
	Name  string
	Shape observedIndex
}

func verifyGuardShapePostgres(ctx context.Context, q rowQuerier, dia dialect.Dialect) error {
	major, err := postgresServerMajor(ctx, q)
	if err != nil {
		return err
	}
	observed, err := projectGuardControlPlaneShape(ctx, q)
	if err != nil {
		return err
	}
	// THE PREDICATE TEXT IS COMPARED ON EVERY CERTIFIED MAJOR. The rendering is the deparser's,
	// so enforcing an unmeasured one would refuse correct databases — but "unmeasured" is now a
	// shrinking set rather than everything except 15. See certifiedPostgresMajors: the list is
	// evidence from a run, and today it covers the whole supported range.
	comparePredicates := postgresMajorCertified(major)
	for _, want := range dialect.GuardControlPlaneShapePostgres() {
		got := observed[want.Relation]
		if !got.Found {
			return fmt.Errorf("%w: %s.%s does not exist as a relation",
				ErrGuardControlPlaneShapeDivergent, dialect.EngineSchema, want.Relation)
		}
		if diff := guardShapeDifference(want, got, comparePredicates); diff != "" {
			suffix := ""
			if !comparePredicates {
				suffix = fmt.Sprintf(" (this server is PostgreSQL %d, which is not in the certified set %v; its CHECK predicates are compared by columns and literals, and their exact text is not)",
					major, certifiedPostgresMajors())
			}
			return fmt.Errorf("%w: %s.%s is not the declared relation: %s%s",
				ErrGuardControlPlaneShapeDivergent, dialect.EngineSchema, want.Relation, diff, suffix)
		}
	}
	return nil
}

// guardShapeDifference names the FIRST difference, or "" when there is none.
//
// One difference rather than all of them, and the first in a fixed order, because the message
// is what an operator acts on: a relation that is a view is not usefully described by also
// listing every column the view happens to lack.
func guardShapeDifference(want dialect.GuardRelationShape, got observedRelation, comparePredicates bool) string {
	// The class of the object comes first. A VIEW over a mutable table would answer every
	// column question correctly while holding none of the guarantees, and an UNLOGGED table
	// loses the entire ledger on an unclean stop — after which a shorter chain verifies
	// perfectly.
	if got.Kind != "r" {
		return fmt.Sprintf("it is relkind %q, not an ordinary table", got.Kind)
	}
	if got.Persistence != "p" {
		return fmt.Sprintf("it is relpersistence %q, not permanent; an unlogged or temporary ledger does not survive an unclean stop, and a chain that lost its tail still verifies", got.Persistence)
	}
	if got.IsPartition {
		return "it is a partition of another relation, so what it holds is decided elsewhere"
	}
	// A CHILD BY CLASSICAL INHERITANCE IS A SECOND, UNGUARDED TABLE UNDER THIS NAME.
	//
	// Measured on 15.18: a child inherits the columns and the CHECKs and inherits NEITHER the
	// unique indexes NOR the triggers, and `INSERT INTO child` with a primary key the parent
	// already holds is accepted. Every read here is qualified with ONLY (see guardOnly), so its
	// rows do not reach a fold — but a relation that has one is a relation somebody prepared to
	// write history into, and reporting that is the difference between defense and silence.
	if got.HasChildren {
		return "another relation INHERITS from it; a child carries neither its uniqueness nor its immutability guard, and rows written there are read by any query that does not say ONLY"
	}
	// Row-level security on an append-only LOG is not a hardening: a policy filters what the
	// fold can SEE, so a chain could verify over a subset while the rows that contradict it
	// stay invisible.
	if got.RowSecurity || got.ForceRowSecurity {
		return fmt.Sprintf("row-level security is enabled (rowsecurity=%t force=%t); a policy that hides rows would let the chain verify over a subset of its own history",
			got.RowSecurity, got.ForceRowSecurity)
	}
	if len(got.ColumnDefects) > 0 {
		return "column " + got.ColumnDefects[0]
	}
	if len(got.Columns) != len(want.Columns) {
		return fmt.Sprintf("it has %d columns and this binary declares %d (%s)",
			len(got.Columns), len(want.Columns), columnNameDiff(want.Columns, got.Columns))
	}
	for i, c := range want.Columns {
		g := got.Columns[i]
		if g != c {
			return fmt.Sprintf("column %d is %s %s (not null: %t) where this binary declares %s %s (not null: %t)",
				i+1, g.Name, g.Type, g.NotNull, c.Name, c.Type, c.NotNull)
		}
	}
	if got.PrimaryKey.identity() != want.PrimaryKey {
		// A PRIMARY KEY OVER THE DECLARED ATTRIBUTES THAT ENFORCES SOMETHING ELSE IS NOT A
		// DIFFERENT PRIMARY KEY. Reporting it as one sends an operator to compare column lists
		// that already agree.
		if got.PrimaryKey.Tuple == want.PrimaryKey && len(got.PrimaryKey.Deviations) > 0 {
			return fmt.Sprintf("its primary key is over the declared (%s) and does not enforce it as declared: %s",
				want.PrimaryKey, got.PrimaryKey.deviationText())
		}
		return fmt.Sprintf("its primary key is (%s) where this binary declares (%s)", got.PrimaryKey.identity(), want.PrimaryKey)
	}
	if d := guardIndexAsymmetry("uniquely-indexed tuple", want.UniqueKeys, got.UniqueKeys); d != "" {
		return d
	}
	wantIdx := make([]string, 0, len(want.Indexes))
	for _, ix := range want.Indexes {
		wantIdx = append(wantIdx, ix.Name+" ("+ix.Columns+")")
	}
	gotIdx := make([]observedIndex, 0, len(got.Indexes))
	for _, ix := range got.Indexes {
		// The name is folded into Tuple so a named index pairs the same way a uniqueness does:
		// same name, same columns, different enforcement is a deviation, not an absence.
		gotIdx = append(gotIdx, observedIndex{Tuple: ix.Name + " (" + ix.Shape.Tuple + ")", Deviations: ix.Shape.Deviations})
	}
	if d := guardIndexAsymmetry("named index", wantIdx, gotIdx); d != "" {
		return d
	}
	if len(got.ForeignOrOtherChecks) > 0 {
		return fmt.Sprintf("it carries a constraint this binary never creates: %s", got.ForeignOrOtherChecks[0])
	}
	wantChecks := make([]string, 0, len(want.Checks))
	for _, c := range want.Checks {
		wantChecks = append(wantChecks, renderCheck(c, comparePredicates))
	}
	gotChecks := make([]string, 0, len(got.Checks))
	for _, c := range got.Checks {
		gotChecks = append(gotChecks, renderCheck(c, comparePredicates))
	}
	return setDifference("CHECK constraint", wantChecks, gotChecks)
}

// renderCheck is the comparable form of one CHECK.
//
// THREE LAYERS, and the middle one is the correction. Comparing only the COLUMNS on an
// unverified major was a semantic hole, not merely a weaker rendering: replacing
// `kind IN ('pending-opened', ...)` with `kind IS NOT NULL` keeps conkey identical, so it
// compared EQUAL — and the fold's whole vocabulary argument rests on that CHECK. `CHECK (true)`
// on the same column did the same.
//
// The literal multiset closes it WITHOUT depending on the deparser. What varies between majors
// is the SYNTAX a predicate is rendered in — the shape of the ANY/ARRAY spelling, which casts
// are printed, whether pg_catalog is elided. What does not vary is the set of literals the
// predicate contains, because those are the values this binary wrote into the DDL. So:
//
//   - the COLUMNS, on every major: catalog fact;
//   - the LITERALS the predicate mentions, on every major: `kind IS NOT NULL` and `true` both
//     carry none where the declared vocabulary carries seven, and `octet_length(x) >= 0`
//     carries 0 where the declared one carries 32;
//   - the PREDICATE TEXT, only on the major whose deparser this repository has run.
//
// WHAT REMAINS OPEN, said plainly rather than implied: a predicate that keeps every literal and
// changes the OPERATOR or the connective — `kind <> ANY (...)`, or an OR where an AND was — has
// the same columns and the same literals and is not caught outside the verified major. Closing
// that needs the certified per-major deparse the amendment records as owed.
func renderCheck(c dialect.GuardCheckShape, withPredicate bool) string {
	if withPredicate {
		return "on (" + c.Columns + ") " + c.Definition
	}
	return "on (" + c.Columns + ") literals[" + checkLiterals(c.Definition) + "]"
}

// checkLiterals is the sorted multiset of literals a predicate mentions, tagged by kind.
//
// It is a scanner rather than a regexp because two things must not be mistaken for literals:
// the digits inside an IDENTIFIER (event_sha256 would otherwise contribute 256) and the
// doubled quote of an escaped apostrophe inside a string.
func checkLiterals(def string) string {
	isIdentRune := func(r rune) bool {
		return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	isIdentStart := func(r rune) bool {
		return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	}
	runes := []rune(def)
	var lits []string
	for i := 0; i < len(runes); {
		switch {
		case runes[i] == '\'':
			var sb strings.Builder
			j := i + 1
			for j < len(runes) {
				if runes[j] == '\'' {
					if j+1 < len(runes) && runes[j+1] == '\'' {
						sb.WriteRune('\'')
						j += 2
						continue
					}
					break
				}
				sb.WriteRune(runes[j])
				j++
			}
			lits = append(lits, "s:"+sb.String())
			i = j + 1
		case isIdentStart(runes[i]):
			// An identifier, and any digits inside it belong to it.
			for i < len(runes) && isIdentRune(runes[i]) {
				i++
			}
		case runes[i] >= '0' && runes[i] <= '9':
			j := i
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
			lits = append(lits, "n:"+string(runes[i:j]))
			i = j
		default:
			i++
		}
	}
	sort.Strings(lits)
	return strings.Join(lits, ",")
}

// guardIndexAsymmetry names the first difference between the tuples this binary declares and the
// indexes the catalog holds.
//
// IT PAIRS BEFORE IT REPORTS AN ABSENCE, which is the whole correction. An index whose attributes
// are exactly the declared ones and whose enforcement is not is PRESENT, and the two states call
// for opposite actions: one operator has to create an index, the other has to drop and rebuild
// one that is sitting there looking correct. The multiset comparison alone cannot tell them
// apart — it sees two unequal strings — so when a declared tuple goes unmatched this looks for
// the observed index that carries exactly those attributes and names the catalog property that
// moved.
//
// The exact-match comparison is unchanged and still runs first: a deviation is part of an index's
// identity, so NULLS NOT DISTINCT never compares equal to the declaration. This only decides what
// the refusal SAYS, never whether there is one.
func guardIndexAsymmetry(what string, want []string, got []observedIndex) string {
	ident := make([]string, 0, len(got))
	for _, g := range got {
		ident = append(ident, g.identity())
	}
	plain := setDifference(what, want, ident)
	if plain == "" {
		return ""
	}
	unmatched := map[string]int{}
	for _, w := range want {
		unmatched[w]++
	}
	for _, id := range ident {
		unmatched[id]--
	}
	declared := make([]string, 0, len(unmatched))
	for tuple, n := range unmatched {
		if n > 0 {
			declared = append(declared, tuple)
		}
	}
	// BOTH SIDES SORTED, and the second one is the correction. The declared tuples were ordered
	// and the OBSERVED candidates were not, so a relation carrying two deviant indexes over the
	// same tuple picked whichever the catalog query happened to return first — pg_index is read
	// with no ORDER BY, so that is not stable across boots. A message that promises to be the
	// same on every run has to be.
	sort.Strings(declared)
	candidates := append([]observedIndex(nil), got...)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].identity() < candidates[j].identity() })
	for _, tuple := range declared {
		for _, g := range candidates {
			// Only an index the declaration does NOT already account for can explain a missing
			// tuple. Without that test, a relation carrying both the declared uniqueness and a
			// deviant twin would have the twin explain away a tuple that is genuinely there.
			if g.Tuple != tuple || len(g.Deviations) == 0 || unmatched[g.identity()] >= 0 {
				continue
			}
			return fmt.Sprintf("the %s (%s) is indexed and the index does not enforce what this binary declares: %s",
				what, tuple, g.deviationText())
		}
	}
	return plain
}

// setDifference compares two MULTISETS and names the first asymmetry.
//
// Multisets rather than sets: two CHECK constraints can legitimately constrain the same
// columns, and collapsing them would let one of the pair be dropped unnoticed.
func setDifference(what string, want, got []string) string {
	w, g := append([]string(nil), want...), append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	counts := map[string]int{}
	for _, s := range w {
		counts[s]++
	}
	for _, s := range g {
		counts[s]--
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case counts[k] > 0:
			return fmt.Sprintf("this binary declares a %s the database does not have: %s", what, k)
		case counts[k] < 0:
			return fmt.Sprintf("the database has a %s this binary does not declare: %s", what, k)
		}
	}
	return ""
}

func columnNameDiff(want, got []dialect.GuardColumnShape) string {
	w := make([]string, 0, len(want))
	for _, c := range want {
		w = append(w, c.Name)
	}
	g := make([]string, 0, len(got))
	for _, c := range got {
		g = append(g, c.Name)
	}
	if d := setDifference("column", w, g); d != "" {
		return d
	}
	return "the same names in a different order"
}

// projectGuardControlPlaneShape reads the three relations from the catalog in four queries.
//
// Four rather than four-per-relation: the control plane is read on every boot, and a probe
// that costs a roundtrip per relation per property is how a boot path acquires a latency
// nobody designed.
func projectGuardControlPlaneShape(ctx context.Context, q rowQuerier) (map[string]observedRelation, error) {
	tables := dialect.GuardControlPlaneTables()
	out := make(map[string]observedRelation, len(tables))
	list, args := tableParams([]any{dialect.EngineSchema}, tables)

	// #nosec G202 -- `list` is tableParams' output: ONLY "$2,$3,…" placeholders. Names travel as bound args
	rows, err := q.QueryContext(ctx, `SELECT c.relname, c.relkind, c.relpersistence, c.relispartition,
       c.relrowsecurity, c.relforcerowsecurity,
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhparent = c.oid)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname IN (`+list+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_class: %w", err)
	}
	for rows.Next() {
		var name string
		var r observedRelation
		var kind, persistence []byte
		if err := rows.Scan(&name, &kind, &persistence, &r.IsPartition, &r.RowSecurity, &r.ForceRowSecurity, &r.HasChildren); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_class: %w", err)
		}
		r.Found, r.Kind, r.Persistence = true, string(kind), string(persistence)
		out[name] = r
	}
	if err := closeRows(rows, "pg_class"); err != nil {
		return nil, err
	}

	// #nosec G202 -- same placeholder-only list
	rows, err = q.QueryContext(ctx, `SELECT c.relname, a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod), a.attnotnull,
       a.atthasdef, a.attidentity, a.attgenerated
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname IN (`+list+`) AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_attribute: %w", err)
	}
	for rows.Next() {
		var name string
		var col dialect.GuardColumnShape
		var hasDefault bool
		var identity, generated []byte
		if err := rows.Scan(&name, &col.Name, &col.Type, &col.NotNull, &hasDefault, &identity, &generated); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_attribute: %w", err)
		}
		r := out[name]
		r.Columns = append(r.Columns, col)
		// A DEFAULT, an identity or a generation expression means the value a row holds is not
		// the value the engine wrote — and every digest in this ledger is computed over what the
		// engine believes it wrote. None of the three appear in the DDL, so any of them is a
		// relation somebody else shaped.
		switch {
		case hasDefault:
			r.ColumnDefects = append(r.ColumnDefects, col.Name+" carries a DEFAULT; the ledger's digests are computed over the values the engine writes, so a column the server can fill is a column the digests do not cover")
		case len(identity) > 0:
			r.ColumnDefects = append(r.ColumnDefects, col.Name+" is an identity column, which the engine never declares")
		case len(generated) > 0:
			r.ColumnDefects = append(r.ColumnDefects, col.Name+" is a generated column, so what it stores is computed rather than written")
		}
		out[name] = r
	}
	if err := closeRows(rows, "pg_attribute"); err != nil {
		return nil, err
	}

	// #nosec G202 -- same placeholder-only list
	rows, err = q.QueryContext(ctx, `SELECT c.relname, con.contype, con.conname, con.convalidated,
       COALESCE((SELECT string_agg(a.attname, ',' ORDER BY k.ord)
                 FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
                 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum), ''),
       pg_catalog.pg_get_constraintdef(con.oid)
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname IN (`+list+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_constraint: %w", err)
	}
	for rows.Next() {
		var name, cols, def, conname string
		var contype []byte
		var convalidated bool
		if err := rows.Scan(&name, &contype, &conname, &convalidated, &cols, &def); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_constraint: %w", err)
		}
		r := out[name]
		switch string(contype) {
		case "c":
			r.Checks = append(r.Checks, dialect.GuardCheckShape{Columns: cols, Definition: def})
		case "p", "u":
			// Handled through pg_index, which sees a unique index whether or not a constraint
			// backs it — and an index without its constraint enforces the same uniqueness.
		case "n":
			// NOT NULL, WHICH POSTGRESQL 18 PUT IN THE CATALOG. Before 18 a column's NOT NULL
			// lived only in pg_attribute.attnotnull; 18 materializes it as a pg_constraint row
			// of its own. Measured: 16.14 and 17.10 create none of these for this DDL, 18.4
			// creates one per NOT NULL column — so the `default` arm below rejected them as
			// "a constraint this binary never creates" and THE ENGINE WOULD HAVE REFUSED TO
			// BOOT ON 18. Nothing but running an 18 could have found that.
			//
			// It is skipped rather than compared because the property it expresses is already
			// compared, column by column, through attnotnull — two readings of one fact. What is
			// NOT redundant is its validity: 18 allows a NOT NULL constraint to be NOT VALID,
			// which leaves pre-existing rows unchecked, so an unvalidated one is a divergence
			// even though attnotnull still reads true.
			if !convalidated {
				r.ForeignOrOtherChecks = append(r.ForeignOrOtherChecks,
					fmt.Sprintf("%s (contype %q): NOT VALID, so rows that predate it were never checked", conname, string(contype)))
			}
		default:
			r.ForeignOrOtherChecks = append(r.ForeignOrOtherChecks,
				fmt.Sprintf("%s (contype %q): %s", conname, string(contype), def))
		}
		out[name] = r
	}
	if err := closeRows(rows, "pg_constraint"); err != nil {
		return nil, err
	}

	// THE FOUR KEY-COLUMN PROPERTIES ARE PROJECTED ONE COLUMN EACH, because a refusal has to name
	// the catalog column that moved. They used to arrive concatenated into a single token per
	// column — `rollout_id:collation:nondeterministic-collation` — which cannot be read back into
	// a property without parsing a format nothing else knows.
	//
	// The FROM/JOIN body is a constant fragment composed once rather than copied four times, so
	// the four readings cannot drift apart. A payload attribute of an INCLUDE index has no entry
	// in indclass, so the multi-argument unnest pads it with NULL and the pg_opclass join drops
	// it — which is correct: a payload column has no operator class to deviate from.
	const indexKeyColumns = `FROM unnest(i.indkey::int2[], i.indclass::oid[], i.indcollation::oid[], i.indoption::int2[])
                      WITH ORDINALITY k(attnum, class, coll, opt, ord)
                 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
                 JOIN pg_opclass oc ON oc.oid = k.class
                 LEFT JOIN pg_collation col ON col.oid = k.coll`
	keyColumnsWhere := func(pred string) string {
		return `COALESCE((SELECT string_agg(a.attname, ',' ORDER BY k.ord)
                 ` + indexKeyColumns + `
                 WHERE ` + pred + `), '')`
	}
	// #nosec G202 -- `list` is tableParams' placeholder-only output and every keyColumnsWhere
	// argument is a literal written in this function; no value from outside reaches the SQL text
	rows, err = q.QueryContext(ctx, `SELECT c.relname, ic.relname, i.indisunique, i.indisprimary,
       COALESCE((SELECT string_agg(a.attname, ',' ORDER BY k.ord)
                 FROM unnest(i.indkey) WITH ORDINALITY k(attnum, ord)
                 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum), ''),
       i.indnatts, i.indpred IS NOT NULL, i.indisvalid, i.indisready, i.indislive, am.amname,
       i.indnkeyatts, i.indnullsnotdistinct, i.indimmediate,
       `+keyColumnsWhere("oc.opcdefault IS NOT TRUE")+`,
       `+keyColumnsWhere("k.coll <> a.attcollation")+`,
       `+keyColumnsWhere("col.collisdeterministic IS FALSE")+`,
       `+keyColumnsWhere("k.opt <> 0")+`
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_am am ON am.oid = ic.relam
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname IN (`+list+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_index: %w", err)
	}
	for rows.Next() {
		var name, indexName, cols, am string
		var nonDefaultOpclass, declaredCollation, nonDeterministic, nonDefaultOrdering string
		var unique, primary, partial, valid, ready, live, nullsNotDistinct, immediate bool
		var natts, nkeyatts int
		if err := rows.Scan(&name, &indexName, &unique, &primary, &cols, &natts, &partial, &valid, &ready, &live, &am,
			&nkeyatts, &nullsNotDistinct, &immediate,
			&nonDefaultOpclass, &declaredCollation, &nonDeterministic, &nonDefaultOrdering); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlstore: guard control plane shape: read pg_index: %w", err)
		}
		r := out[name]
		idx := observedIndex{Tuple: cols}
		// AN INDEX THAT IS NOT VALID, READY OR LIVE IS NOT THE INDEX THAT WAS DECLARED, and the
		// columns say nothing about that. pg_index documents all three
		// (https://www.postgresql.org/docs/current/catalog-pg-index.html), and each fails in its
		// own direction — which is the correction, because this comment used to flatten them into
		// "enforces nothing" and that is false of the first one. MEASURED on 16.14: with
		// indisvalid forced false a duplicate INSERT is still REJECTED. indisvalid false is a
		// CREATE INDEX CONCURRENTLY that never finished, so the uniqueness is unproven BACKWARDS
		// over the rows that predate the build while still enforced forwards, and the planner
		// will not use it; indisready false is one still not accepting inserts, so rows written
		// in that state go unchecked; indislive false is one being dropped. The exact wording an
		// operator receives lives in guardIndexNotValid/NotReady/NotLive above, and it is
		// deliberately not repeated here — see that block for the measurement.
		//
		// Projecting only (name, columns) meant such an index compared EQUAL to the uniqueness
		// this ledger's whole ordering argument rests on.
		//
		// Each is its own deviation rather than one joint annotation, because they are three
		// different states of an index and an operator acts differently on each.
		//
		// The access method is projected for the same reason: uniqueness is a btree property
		// here, and an index built with another method under the same name and columns would
		// compare equal while enforcing something else.
		if !valid {
			idx.Deviations = append(idx.Deviations, guardIndexNotValid)
		}
		if !ready {
			idx.Deviations = append(idx.Deviations, guardIndexNotReady)
		}
		if !live {
			idx.Deviations = append(idx.Deviations, guardIndexNotLive)
		}
		if am != "btree" {
			idx.Deviations = append(idx.Deviations, guardIndexAccessMethod(am))
		}
		// A PARTIAL unique index enforces uniqueness only where its predicate holds, so it is
		// not the uniqueness this binary declares even when its columns match. Recording it as a
		// deviation is what makes the comparison refuse rather than silently accept it.
		if partial {
			idx.Deviations = append(idx.Deviations, guardIndexPartial)
		}
		// An expression index reports attnum 0 for the computed columns, so the joined name
		// list is shorter than indnatts. Saying so beats comparing a truncated tuple.
		//
		// It counts the commas in the TUPLE, which no longer carries annotations, so this no
		// longer has to run before anything.
		if named := len(strings.Split(cols, ",")); named != natts {
			idx.Deviations = append(idx.Deviations, guardIndexExpressions(natts-named, natts))
		}
		// THE THREE WAYS AN INDEX CAN CARRY THE DECLARED COLUMNS AND ENFORCE SOMETHING ELSE.
		// Round four built all three against a real PostgreSQL 16 and the projector accepted
		// every one of them, because it read the column NAMES and nothing about what the index
		// does with them.
		//
		//  1. INCLUDE. Payload attributes live in indkey and count in indnatts, so
		//     `UNIQUE (rollout_id) INCLUDE (event_ordinal)` rendered the same two names as a
		//     uniqueness over BOTH — while enforcing it over one. indnkeyatts is the split
		//     PostgreSQL documents for exactly this
		//     (https://www.postgresql.org/docs/current/catalog-pg-index.html).
		//  2. NULLS NOT DISTINCT. Two of the diagnostic uniqueness's three columns are nullable,
		//     and the DDL creates it with the default (nulls distinct). Flipping it changes which
		//     rows may coexist without changing a single column name.
		//  3. DEFERRABLE. indimmediate tracks condeferrable and NOT condeferred, so it is false
		//     for BOTH deferrable variants and the projection has no gap. What it does NOT mean
		//     is "checked at COMMIT" — that is only INITIALLY DEFERRED. An INITIALLY IMMEDIATE
		//     constraint is still checked per statement UNTIL some transaction says
		//     `SET CONSTRAINTS ... DEFERRED`, which any of them may; a non-deferrable one refuses
		//     the deferral outright. That is the property the declaration wants, and the measured
		//     table is in guardIndexDeferrable above. The pg_constraint arm deliberately ignores
		//     'p'/'u', so nothing else sees it.
		//
		// And the operator class, collation and ordering of each key column, because uniqueness
		// is EQUALITY under an operator class and a collation: a non-deterministic collation on
		// a text key changes which values count as equal.
		//
		// THE COLLATION ARM HAD A HOLE OF EXACTLY THAT KIND, and round five named it. Comparing
		// indcollation against attcollation only sees an index that DECLARES a different
		// collation from its column. An index that declares none inherits the column's, the two
		// oids match, and the comparison reports nothing — so a column re-typed to a
		// NON-DETERMINISTIC collation moved the equality relation the uniqueness is built on
		// while the projection saw an unchanged index. The identity of the collation was never
		// the property that mattered; collisdeterministic is, and it is read directly now.
		if nkeyatts < natts {
			idx.Deviations = append(idx.Deviations, guardIndexIncludePayload(natts-nkeyatts, natts))
		}
		if nullsNotDistinct {
			idx.Deviations = append(idx.Deviations, guardIndexNullsNotDistinct)
		}
		if !immediate {
			idx.Deviations = append(idx.Deviations, guardIndexDeferrable)
		}
		// THE FOUR KEY-COLUMN PROPERTIES, SEPARATELY. They used to share one annotation, so a
		// refusal could say "non-default operator class, collation or ordering" without saying
		// which — three catalog columns behind one word, and the reader left to guess.
		if nonDefaultOpclass != "" {
			idx.Deviations = append(idx.Deviations, guardIndexNonDefaultOpclass(nonDefaultOpclass))
		}
		if declaredCollation != "" {
			idx.Deviations = append(idx.Deviations, guardIndexDeclaredCollation(declaredCollation))
		}
		if nonDeterministic != "" {
			idx.Deviations = append(idx.Deviations, guardIndexNonDeterministicCollation(nonDeterministic))
		}
		if nonDefaultOrdering != "" {
			idx.Deviations = append(idx.Deviations, guardIndexNonDefaultOrdering(nonDefaultOrdering))
		}
		switch {
		case primary:
			r.PrimaryKey = idx
			r.UniqueKeys = append(r.UniqueKeys, idx)
		case unique:
			r.UniqueKeys = append(r.UniqueKeys, idx)
		default:
			r.Indexes = append(r.Indexes, observedNamedIndex{Name: indexName, Shape: idx})
		}
		out[name] = r
	}
	if err := closeRows(rows, "pg_index"); err != nil {
		return nil, err
	}
	return out, nil
}
