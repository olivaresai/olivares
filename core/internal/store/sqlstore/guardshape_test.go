// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardshape_test.go pins the two halves of the control plane's self-verification that a
// server is not needed for: the SQLite shape comparison, and the bootstrap receipts.
//
// Both are MUTATION tests rather than presence tests. A test that opens a store and asserts
// that verification passes proves only that the happy path is reachable; what has to be true
// is that each specific divergence is REFUSED, and the only way to know that is to introduce
// it and watch the refusal arrive with its own name.

// mutateSQLiteSchema removes an object, applies statements, and restores the object.
//
// The dance is needed because these relations carry SQLite's append-only trigger pair: a
// DELETE aimed at a receipt is refused by the trigger, so the trigger is dropped, the row is
// removed and the trigger is recreated from the SAME rendered text the dialect emits — which
// leaves the shape byte-identical to what verifyGuardShapeSQLite expects. Recreating it by
// hand instead would make every receipt test fail for the wrong reason.
func mutateSQLiteSchema(t *testing.T, raw *sql.DB, dia dialect.Dialect, table string, stmts ...string) {
	t.Helper()
	var triggers []string
	for _, s := range dia.GuardControlPlaneStmts() {
		if strings.HasPrefix(s, "CREATE TRIGGER "+table) {
			triggers = append(triggers, s)
		}
	}
	if len(triggers) != 2 {
		t.Fatalf("expected two append-only triggers rendered for %s, found %d", table, len(triggers))
	}
	for _, s := range triggers {
		name, ok := sqliteObjectName(s)
		if !ok {
			t.Fatalf("cannot name the object in %q", s)
		}
		if _, err := raw.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("apply %q: %v", s, err)
		}
	}
	for _, s := range triggers {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("restore %q: %v", s, err)
		}
	}
}

// insertBootstrapReceiptSQLite writes one bootstrap receipt with a RECOMPUTED id.
//
// Recomputing is what makes the test exercise the field-by-field comparison rather than the
// digest check that sits in front of it. A hand-edited row whose id no longer matches its
// contents is refused by readGuardBootstrapReceipts before any field is compared — correct,
// and a different property from the one under test here.
func insertBootstrapReceiptSQLite(t *testing.T, raw *sql.DB, dia dialect.Dialect, r guardReceipt) {
	t.Helper()
	var err error
	if r.ReceiptID, err = r.bodyDigest(); err != nil {
		t.Fatal(err)
	}
	ordinal, prev, err := receiptStreamHead(context.Background(), raw, dia, r.RolloutID)
	if err != nil {
		t.Fatalf("read the receipt head: %v", err)
	}
	r.EventOrdinal, r.PrevEventSHA256 = ordinal+1, prev
	if r.EventSHA256, err = r.chainDigest(); err != nil {
		t.Fatal(err)
	}
	r.AppliedAt = nowRFC3339()
	var fromState any
	if r.FromEnableState.Valid {
		fromState = r.FromEnableState.V
	}
	if _, err := raw.Exec(dia.Rebind("INSERT INTO "+guardReceiptsTable+" ("+guardReceiptColumns+
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"),
		digestBytes(r.ReceiptID), r.RolloutID, r.UnitID, r.Kind, string(r.Intent),
		r.Key.Schema, r.Key.Relation, r.Key.Trigger,
		r.Epoch, r.Format, digestBytes(r.CodeSHA256), r.RetainedRevision, digestBytes(r.RetainedSHA256),
		digestBytes(r.SpecSHA256), digestBytes(r.DefinitionSHA256), digestBytes(r.PrestateSHA256),
		fromState, r.ToEnableState, r.PredecessorReceiptID.bytes(), r.AttemptID,
		r.EventOrdinal, r.PrevEventSHA256.bytes(), digestBytes(r.EventSHA256), r.AppliedAt); err != nil {
		t.Fatalf("insert a bootstrap receipt: %v", err)
	}
}

// bootBootstrapContext returns the manifest, the bootstrap rollout and the metadata specs a
// SQLite boot just used, so a test can rebuild exactly what the migration wrote.
func bootBootstrapContext(t *testing.T) (guardManifest, guardRolloutContext, []guardSpec) {
	t.Helper()
	m, err := buildGuardManifest(newRegistryForTest(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	retained, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, 0, retained)
	if err != nil {
		t.Fatal(err)
	}
	specs, err := guardMetadataSpecs(m.Format)
	if err != nil {
		t.Fatal(err)
	}
	return m, guardRolloutContext{
		RolloutID: id, Format: m.Format, CodeEpoch: m.CodeEpoch,
		CodeSHA256: m.CodeSHA256, RetainedRevision: 0, RetainedSHA256: retained,
	}, specs
}

// TestSQLiteShapeRefusesAnObjectThatIsNotTheRenderedOne covers all three asymmetries.
//
// SQLite stores the CREATE statement verbatim, so the comparison is exact and every one of
// these is a byte difference the check must name.
func TestSQLiteShapeRefusesAnObjectThatIsNotTheRenderedOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, raw *sql.DB, dia dialect.Dialect)
		want   string
	}{
		{
			name: "a declared index is gone",
			mutate: func(t *testing.T, raw *sql.DB, _ dialect.Dialect) {
				if _, err := raw.Exec("DROP INDEX " + dialect.GuardReceiptsTable + "_target_idx"); err != nil {
					t.Fatal(err)
				}
			},
			want: "is missing",
		},
		{
			name: "a declared trigger is gone",
			mutate: func(t *testing.T, raw *sql.DB, _ dialect.Dialect) {
				if _, err := raw.Exec("DROP TRIGGER " + dialect.GuardGateEventsTable + "_no_delete"); err != nil {
					t.Fatal(err)
				}
			},
			want: "is missing",
		},
		{
			name: "an index nobody declared was added",
			mutate: func(t *testing.T, raw *sql.DB, _ dialect.Dialect) {
				if _, err := raw.Exec("CREATE INDEX extra_idx ON " + dialect.GuardGateEventsTable + " (kind)"); err != nil {
					t.Fatal(err)
				}
			},
			want: "this binary never creates it",
		},
		{
			name: "a trigger nobody declared was added",
			mutate: func(t *testing.T, raw *sql.DB, _ dialect.Dialect) {
				if _, err := raw.Exec("CREATE TRIGGER extra_trg AFTER INSERT ON " + dialect.GuardReceiptsTable +
					" BEGIN SELECT 1; END"); err != nil {
					t.Fatal(err)
				}
			},
			want: "this binary never creates it",
		},
		{
			name: "the table text differs",
			mutate: func(t *testing.T, raw *sql.DB, _ dialect.Dialect) {
				// PRAGMA writable_schema is the only way to change a stored CREATE without
				// rebuilding the table, and it is exactly the surface a restore tool or a
				// hand-repair would use.
				if _, err := raw.Exec("PRAGMA writable_schema = ON"); err != nil {
					t.Fatal(err)
				}
				if _, err := raw.Exec(`UPDATE sqlite_master
SET sql = replace(sql, 'event_ordinal INTEGER NOT NULL UNIQUE', 'event_ordinal INTEGER NOT NULL')
WHERE type = 'table' AND name = ?`, dialect.GuardInventoryEventsTable); err != nil {
					t.Fatal(err)
				}
				if _, err := raw.Exec("PRAGMA writable_schema = OFF"); err != nil {
					t.Fatal(err)
				}
			},
			want: "is stored as",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, dia := openSQLiteStoreForGuards(t)
			ctx := context.Background()
			if err := verifyGuardControlPlaneShape(ctx, raw, dia); err != nil {
				t.Fatalf("the freshly created control plane already diverges: %v", err)
			}
			tc.mutate(t, raw, dia)
			err := verifyGuardControlPlaneShape(ctx, raw, dia)
			if err == nil {
				t.Fatal("the divergence was accepted")
			}
			if !errors.Is(err, ErrGuardControlPlaneShapeDivergent) {
				t.Fatalf("the refusal was %v, which is not the named shape error — so it refused for some other reason", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}
}

// TestSQLiteBootstrapReceiptsAreRequiredAndAttributed is the F-08 receipt half.
//
// Omission, extra and mutation, each with its own refusal. The mutation leg is table-driven
// over the fields a forger could choose, and every case RECOMPUTES the receipt id so that the
// field comparison — not the digest in front of it — is what refuses.
func TestSQLiteBootstrapReceiptsAreRequiredAndAttributed(t *testing.T) {
	ctx := context.Background()

	t.Run("an omitted receipt is refused", func(t *testing.T) {
		raw, dia := openSQLiteStoreForGuards(t)
		m, _, specs := bootBootstrapContext(t)
		if err := verifyGuardControlPlaneObjects(ctx, raw, dia, m); err != nil {
			t.Fatalf("the freshly bootstrapped control plane is already invalid: %v", err)
		}
		unitID, err := guardBootstrapUnitID(m.Format, specs[0].Key)
		if err != nil {
			t.Fatal(err)
		}
		mutateSQLiteSchema(t, raw, dia, dialect.GuardReceiptsTable,
			"DELETE FROM "+guardReceiptsTable+" WHERE unit_id = '"+unitID+"'")
		err = verifyGuardControlPlaneObjects(ctx, raw, dia, m)
		if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
			t.Fatalf("the refusal was %v, want the bootstrap-receipt error", err)
		}
		if !strings.Contains(err.Error(), "exactly") {
			t.Errorf("the refusal does not state the expected cardinality: %v", err)
		}
	})

	t.Run("an extra receipt is refused", func(t *testing.T) {
		raw, dia := openSQLiteStoreForGuards(t)
		m, rollout, _ := bootBootstrapContext(t)
		foreign := guardSpec{
			Key:                 guardKey{Schema: guardSchema, Relation: "some_other_table", Trigger: "some_other_table_immutable"},
			Producer:            guardProducerEngine,
			Definition:          canonicalGuardDefinition(),
			DesiredEnableState:  guardStateAlways,
			LegacyAllowedStates: []string{guardStateAlways},
		}
		var err error
		if foreign.DefinitionSHA256, err = foreign.Definition.definitionDigest(foreign.Key); err != nil {
			t.Fatal(err)
		}
		if foreign.SpecSHA256, err = foreign.specDigest(); err != nil {
			t.Fatal(err)
		}
		unitID, err := guardBootstrapUnitID(m.Format, foreign.Key)
		if err != nil {
			t.Fatal(err)
		}
		r, err := bootstrapReceiptFor(rollout, foreign, unitID)
		if err != nil {
			t.Fatal(err)
		}
		insertBootstrapReceiptSQLite(t, raw, dia, r)
		err = verifyGuardControlPlaneObjects(ctx, raw, dia, m)
		if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
			t.Fatalf("the refusal was %v, want the bootstrap-receipt error", err)
		}
	})

	t.Run("a receipt for a relation this edition does not manage is refused", func(t *testing.T) {
		raw, dia := openSQLiteStoreForGuards(t)
		m, rollout, specs := bootBootstrapContext(t)
		// One out, one foreign in: the COUNT still matches, so only the per-unit lookup and the
		// leftover sweep can catch this.
		gone, err := guardBootstrapUnitID(m.Format, specs[2].Key)
		if err != nil {
			t.Fatal(err)
		}
		mutateSQLiteSchema(t, raw, dia, dialect.GuardReceiptsTable,
			"DELETE FROM "+guardReceiptsTable+" WHERE unit_id = '"+gone+"'")
		foreign := specs[2]
		foreign.Key.Relation = "some_other_table"
		foreign.Key.Trigger = "some_other_table_immutable"
		if foreign.DefinitionSHA256, err = foreign.Definition.definitionDigest(foreign.Key); err != nil {
			t.Fatal(err)
		}
		if foreign.SpecSHA256, err = foreign.specDigest(); err != nil {
			t.Fatal(err)
		}
		unitID, err := guardBootstrapUnitID(m.Format, foreign.Key)
		if err != nil {
			t.Fatal(err)
		}
		r, err := bootstrapReceiptFor(rollout, foreign, unitID)
		if err != nil {
			t.Fatal(err)
		}
		insertBootstrapReceiptSQLite(t, raw, dia, r)
		err = verifyGuardControlPlaneObjects(ctx, raw, dia, m)
		if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
			t.Fatalf("the refusal was %v, want the bootstrap-receipt error", err)
		}
		if !strings.Contains(err.Error(), "no bootstrap receipt") {
			t.Errorf("the refusal does not name the relation left unattributed: %v", err)
		}
	})

	// Every field a forger could choose, each producing the refusal that names it. The
	// receipt_kind/intent pair is absent on purpose: the CHECK constraint makes those two
	// unable to disagree, so a test would be exercising the database rather than this code.
	for _, tc := range []struct {
		field  string
		mutate func(r *guardReceipt)
	}{
		{"rollout_id", func(r *guardReceipt) { r.RolloutID = strings.Repeat("0", len(r.RolloutID)) }},
		{"relation_name", func(r *guardReceipt) { r.Key.Relation = "some_other_table" }},
		{"trigger_name", func(r *guardReceipt) { r.Key.Trigger = "some_other_trigger" }},
		{"relation_schema", func(r *guardReceipt) { r.Key.Schema = "elsewhere" }},
		{"epoch", func(r *guardReceipt) { r.Epoch++ }},
		{"manifest_format", func(r *guardReceipt) { r.Format++ }},
		{"code_sha256", func(r *guardReceipt) { r.CodeSHA256[0] ^= 0xFF }},
		{"retained_revision", func(r *guardReceipt) { r.RetainedRevision++ }},
		{"retained_sha256", func(r *guardReceipt) { r.RetainedSHA256[0] ^= 0xFF }},
		{"spec_sha256", func(r *guardReceipt) { r.SpecSHA256[0] ^= 0xFF }},
		{"definition_sha256", func(r *guardReceipt) { r.DefinitionSHA256[0] ^= 0xFF }},
		{"prestate_sha256", func(r *guardReceipt) { r.PrestateSHA256[0] ^= 0xFF }},
		{"from_enable_state", func(r *guardReceipt) { r.FromEnableState = someText(guardStateOrigin) }},
		{"to_enable_state", func(r *guardReceipt) { r.ToEnableState = guardStateOrigin }},
		{"predecessor_receipt_id", func(r *guardReceipt) { r.PredecessorReceiptID = someDigest(r.ReceiptID) }},
		{"attempt_id", func(r *guardReceipt) { r.AttemptID = "not-the-bootstrap" }},
	} {
		t.Run("a mutated "+tc.field+" is refused", func(t *testing.T) {
			raw, dia := openSQLiteStoreForGuards(t)
			m, rollout, specs := bootBootstrapContext(t)
			unitID, err := guardBootstrapUnitID(m.Format, specs[0].Key)
			if err != nil {
				t.Fatal(err)
			}
			r, err := bootstrapReceiptFor(rollout, specs[0], unitID)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&r)
			mutateSQLiteSchema(t, raw, dia, dialect.GuardReceiptsTable,
				"DELETE FROM "+guardReceiptsTable+" WHERE unit_id = '"+unitID+"'")
			insertBootstrapReceiptSQLite(t, raw, dia, r)

			err = verifyGuardControlPlaneObjects(ctx, raw, dia, m)
			if err == nil {
				t.Fatalf("a receipt with a forged %s was accepted", tc.field)
			}
			if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
				t.Fatalf("the refusal was %v, want the bootstrap-receipt error", err)
			}
			// The message must name the field that differs, because that is what an operator
			// has to act on — and because a refusal that merely says "invalid" would pass this
			// test while telling nobody anything.
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("the refusal does not name %s: %v", tc.field, err)
			}
		})
	}
}

// lastObservedIndex returns a POINTER to the final element, so a mutation writes into the
// fixture rather than into a copy.
//
// The last rather than a literal position, because a row that indexed by number would silently
// start attacking a different uniqueness the day the declaration grows one.
func lastObservedIndex(ix []observedIndex) *observedIndex { return &ix[len(ix)-1] }

// TestTwoDeviantIndexesOverOneTupleProduceOneStableDiagnosis is the regression the sorted copy in
// guardIndexAsymmetry never had, and round twenty is why it exists.
//
// The fix sorts a COPY of the observed candidates before choosing which one explains a missing
// declared tuple (guardshape.go:629-631). The committed table, however, installs exactly ONE
// deviant candidate per case, so with one candidate every iteration order picks the same one:
// changing the loop back to walk `got` unsorted left the table green. A test that cannot see the
// defect it is named for is decoration.
//
// WHAT MAKES THIS ONE DISCRIMINATING: two deviant twins over the SAME tuple, presented in both
// orders. pg_index is read with no ORDER BY, so the catalog is entitled to return them either
// way, and an operator who reads two different refusals for one unchanged database cannot tell a
// second defect from a reshuffled answer.
//
// The expectation is production's own constant rather than a spelling written here — the same
// rule the table above follows. `indimmediate` sorts before `indnullsnotdistinct`, so the sorted
// choice is the DEFERRABLE twin whichever way the pair arrives.
func TestTwoDeviantIndexesOverOneTupleProduceOneStableDiagnosis(t *testing.T) {
	declared := dialect.GuardControlPlaneShapePostgres()
	want := declared[0]
	if len(want.UniqueKeys) == 0 {
		t.Skip("the first declared relation carries no unique tuple, so this property has no subject here")
	}
	tuple := want.UniqueKeys[len(want.UniqueKeys)-1]

	deferrable := observedIndex{Tuple: tuple, Deviations: []guardIndexDeviation{guardIndexDeferrable}}
	nullsNotDistinct := observedIndex{Tuple: tuple, Deviations: []guardIndexDeviation{guardIndexNullsNotDistinct}}
	if deferrable.identity() == nullsNotDistinct.identity() {
		t.Fatal("the two twins share an identity, so this case cannot tell an ordering apart from a coincidence")
	}

	// observe builds the relation with the declared plain uniqueness over `tuple` REPLACED by the
	// two deviant twins, in the order given. Everything else is the identity projection, so the
	// only thing the comparator can report is the tuple that went unmatched.
	observe := func(first, second observedIndex) observedRelation {
		uniques := make([]observedIndex, 0, len(want.UniqueKeys)+1)
		for _, u := range want.UniqueKeys {
			if u == tuple {
				continue
			}
			uniques = append(uniques, observedIndex{Tuple: u})
		}
		uniques = append(uniques, first, second)
		named := make([]observedNamedIndex, 0, len(want.Indexes))
		for _, ix := range want.Indexes {
			named = append(named, observedNamedIndex{Name: ix.Name, Shape: observedIndex{Tuple: ix.Columns}})
		}
		return observedRelation{
			Found: true, Kind: "r", Persistence: "p",
			Columns:    append([]dialect.GuardColumnShape(nil), want.Columns...),
			PrimaryKey: observedIndex{Tuple: want.PrimaryKey},
			UniqueKeys: uniques,
			Indexes:    named,
			Checks:     append([]dialect.GuardCheckShape(nil), want.Checks...),
		}
	}

	forward := guardShapeDifference(want, observe(deferrable, nullsNotDistinct), true)
	reversed := guardShapeDifference(want, observe(nullsNotDistinct, deferrable), true)

	if forward == "" || reversed == "" {
		t.Fatalf("a relation whose declared uniqueness is served only by two deviant indexes compared EQUAL in one of the orders:\n forward:  %q\n reversed: %q",
			forward, reversed)
	}
	if forward != reversed {
		t.Fatalf("the same database produced two different refusals depending only on the order pg_index happened to return two deviant indexes over %q:\n forward:  %s\n reversed: %s",
			tuple, forward, reversed)
	}
	if !strings.Contains(forward, guardIndexDeferrable.Property) {
		t.Errorf("the stable diagnosis does not name %q, which is the catalog column of the twin that sorts first: %s",
			guardIndexDeferrable.Property, forward)
	}
}

// TestGuardShapeComparatorNamesEveryClassOfDivergence exercises the PostgreSQL comparator
// directly, against declared shapes rather than a server.
//
// It is not a substitute for the PostgreSQL regressions — those prove the PROJECTION reads a
// real catalog correctly — but the comparator has branches a server cannot easily be made to
// produce, and a branch nothing ever evaluated is a branch that can be wrong for free.
func TestGuardShapeComparatorNamesEveryClassOfDivergence(t *testing.T) {
	declared := dialect.GuardControlPlaneShapePostgres()
	if len(declared) != len(dialect.GuardControlPlaneTables()) {
		t.Fatalf("the declaration covers %d relations and this engine creates %d",
			len(declared), len(dialect.GuardControlPlaneTables()))
	}
	want := declared[0]

	// The identity projection: a relation that IS the declared one.
	identity := func() observedRelation {
		uniques := make([]observedIndex, 0, len(want.UniqueKeys))
		for _, tuple := range want.UniqueKeys {
			uniques = append(uniques, observedIndex{Tuple: tuple})
		}
		named := make([]observedNamedIndex, 0, len(want.Indexes))
		for _, ix := range want.Indexes {
			named = append(named, observedNamedIndex{Name: ix.Name, Shape: observedIndex{Tuple: ix.Columns}})
		}
		return observedRelation{
			Found: true, Kind: "r", Persistence: "p",
			Columns:    append([]dialect.GuardColumnShape(nil), want.Columns...),
			PrimaryKey: observedIndex{Tuple: want.PrimaryKey},
			UniqueKeys: uniques,
			Indexes:    named,
			Checks:     append([]dialect.GuardCheckShape(nil), want.Checks...),
		}
	}
	if d := guardShapeDifference(want, identity(), true); d != "" {
		t.Fatalf("the declared shape does not compare equal to itself: %s", d)
	}

	for _, tc := range []struct {
		name   string
		mutate func(o *observedRelation)
		want   string
	}{
		{"a view wearing the name", func(o *observedRelation) { o.Kind = "v" }, "relkind"},
		{"an unlogged ledger", func(o *observedRelation) { o.Persistence = "u" }, "relpersistence"},
		{"a partition", func(o *observedRelation) { o.IsPartition = true }, "partition"},
		{"row-level security", func(o *observedRelation) { o.RowSecurity = true }, "row-level security"},
		{"a forced row-level security", func(o *observedRelation) { o.ForceRowSecurity = true }, "row-level security"},
		{"a column with a default", func(o *observedRelation) { o.ColumnDefects = []string{"kind carries a DEFAULT"} }, "DEFAULT"},
		{"a missing column", func(o *observedRelation) { o.Columns = o.Columns[1:] }, "columns"},
		{"a retyped column", func(o *observedRelation) { o.Columns[2].Type = "text" }, "column 3"},
		{"a nullable column", func(o *observedRelation) { o.Columns[1].NotNull = false }, "column 2"},
		{"a different primary key", func(o *observedRelation) { o.PrimaryKey = observedIndex{Tuple: "rollout_id"} }, "primary key"},
		{"a lost uniqueness", func(o *observedRelation) { o.UniqueKeys = o.UniqueKeys[1:] }, "uniquely-indexed tuple"},
		{"an added uniqueness", func(o *observedRelation) {
			o.UniqueKeys = append(o.UniqueKeys, observedIndex{Tuple: "kind"})
		}, "uniquely-indexed tuple"},
		{"a renamed index", func(o *observedRelation) { o.Indexes[0].Name = "somebody_elses_idx" }, "named index"},
		// THE DEVIATION ROWS. Each one leaves the declared attributes indexed and changes what
		// the index does with them, and each asserts that the refusal names the pg_catalog
		// column that moved — not merely that SOME difference was found.
		//
		// The expectation is the constant PRODUCTION emits, not a spelling written here: a test
		// that hand-wrote the property name would keep passing after the projector stopped
		// producing it. The PostgreSQL matrix (TestPostgresTheShapeRefusesAnIndexThatEnforces\
		// SomethingElse) is what checks the projector actually reads these columns off a server;
		// this checks the comparator turns them into a message an operator can act on, and it
		// needs no server to do it.
		{"an INCLUDE payload", func(o *observedRelation) {
			ix := lastObservedIndex(o.UniqueKeys)
			ix.Deviations = []guardIndexDeviation{guardIndexIncludePayload(1, len(strings.Split(ix.Tuple, ",")))}
		}, guardIndexIncludePayload(0, 0).Property},
		{"a NULLS NOT DISTINCT uniqueness", func(o *observedRelation) {
			lastObservedIndex(o.UniqueKeys).Deviations = []guardIndexDeviation{guardIndexNullsNotDistinct}
		}, guardIndexNullsNotDistinct.Property},
		{"a DEFERRABLE uniqueness", func(o *observedRelation) {
			lastObservedIndex(o.UniqueKeys).Deviations = []guardIndexDeviation{guardIndexDeferrable}
		}, guardIndexDeferrable.Property},
		{"a non-deterministic collation", func(o *observedRelation) {
			ix := lastObservedIndex(o.UniqueKeys)
			ix.Deviations = []guardIndexDeviation{guardIndexNonDeterministicCollation(ix.Tuple)}
		}, guardIndexNonDeterministicCollation("").Property},
		{"a uniqueness that is not enforcing", func(o *observedRelation) {
			lastObservedIndex(o.UniqueKeys).Deviations = []guardIndexDeviation{guardIndexNotValid}
		}, guardIndexNotValid.Property},
		{"a DEFERRABLE primary key", func(o *observedRelation) {
			o.PrimaryKey.Deviations = []guardIndexDeviation{guardIndexDeferrable}
		}, guardIndexDeferrable.Property},
		{"a named index over an unexpected operator class", func(o *observedRelation) {
			o.Indexes[0].Shape.Deviations = []guardIndexDeviation{guardIndexNonDefaultOpclass("kind")}
		}, guardIndexNonDefaultOpclass("").Property},
		{"a foreign key nobody declared", func(o *observedRelation) {
			o.ForeignOrOtherChecks = []string{`gate_fk (contype "f"): FOREIGN KEY (rollout_id) REFERENCES elsewhere(id)`}
		}, "never creates"},
		{"a dropped CHECK", func(o *observedRelation) { o.Checks = o.Checks[1:] }, "CHECK constraint"},
		{"a weakened CHECK", func(o *observedRelation) { o.Checks[0].Definition = "CHECK ((true))" }, "CHECK constraint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := identity()
			tc.mutate(&got)
			d := guardShapeDifference(want, got, true)
			if d == "" {
				t.Fatal("the divergence compared equal")
			}
			if !strings.Contains(d, tc.want) {
				t.Errorf("the difference does not mention %q: %s", tc.want, d)
			}
		})
	}

	// AND WHAT AN UNVERIFIED MAJOR STILL CATCHES, which is the correction to a limit this test
	// used to ASSERT as correct behavior. Comparing only the columns off the verified major
	// meant `CHECK ((true))` on the same column compared EQUAL to the declared vocabulary — a
	// semantic hole, not a rendering difference, because the fold's whole argument about which
	// kinds exist rests on that CHECK. The literal multiset closes it without depending on the
	// deparser: the values are the ones this binary wrote into the DDL.
	for _, tc := range []struct {
		name    string
		mutate  func(o *observedRelation)
		wantSub string
	}{
		{"a predicate replaced by true", func(o *observedRelation) { o.Checks[0].Definition = "CHECK ((true))" }, "CHECK constraint"},
		{"a vocabulary replaced by a null test", func(o *observedRelation) {
			o.Checks[0].Definition = "CHECK ((kind IS NOT NULL))"
		}, "CHECK constraint"},
		{"a member removed from the vocabulary", func(o *observedRelation) {
			o.Checks[0].Definition = "CHECK ((kind = ANY (ARRAY['pending-opened'::text])))"
		}, "CHECK constraint"},
		{"a CHECK that is gone", func(o *observedRelation) { o.Checks = o.Checks[1:] }, "CHECK constraint"},
	} {
		t.Run("under the weak comparison: "+tc.name, func(t *testing.T) {
			got := identity()
			tc.mutate(&got)
			d := guardShapeDifference(want, got, false)
			if d == "" {
				t.Fatalf("%s compared equal under the weak comparison", tc.name)
			}
			if !strings.Contains(d, tc.wantSub) {
				t.Errorf("the difference does not mention %q: %s", tc.wantSub, d)
			}
		})
	}

	// AND WHAT THE FALLBACK PATH COSTS, EXERCISED rather than asserted away.
	//
	// A predicate that keeps every literal and changes only an operator or a connective has the
	// same columns and the same literal multiset, so the WEAK comparison cannot see it. That
	// used to be the state of every major but 15; today every supported major is certified
	// (certifiedPostgresMajors) and production always compares the text, so this is the price of
	// the FALLBACK — the path a major inside the supported range but not yet certified would
	// take. Keeping it measured is what makes widening the range survivable instead of a brick,
	// and what makes certifying a major a mechanical improvement rather than paperwork.
	for _, tc := range []struct {
		name string
		from string
		to   string
	}{
		{"a comparison is flipped", ">= 0", "<= 0"},
		{"a disjunction becomes a conjunction", ") OR (", ") AND ("},
	} {
		t.Run("the residual limit: "+tc.name, func(t *testing.T) {
			got := identity()
			original := got.Checks[0].Definition
			got.Checks[0].Definition = strings.Replace(original, tc.from, tc.to, 1)
			if got.Checks[0].Definition == original {
				t.Fatalf("the fixture's first CHECK does not contain %q: %s", tc.from, original)
			}
			if d := guardShapeDifference(want, got, false); d != "" {
				t.Errorf("this case is meant to document what is NOT caught off the verified major, and it was caught: %s", d)
			}
			if d := guardShapeDifference(want, got, true); d == "" {
				t.Errorf("%s was not caught even on the verified major, where the predicate text is compared", tc.name)
			}
			t.Logf("GUARD_SHAPE_FALLBACK_COST|%s is invisible to the weak comparison and caught by the certified one (certified: %v)", tc.name, certifiedPostgresMajors())
		})
	}
}

// TestGuardShapeDeclarationMatchesTheRelationsThisEngineCreates is the cheap coupling check.
//
// The declaration and the DDL are two files, and the PostgreSQL regression that compares them
// against a live catalog needs a server. This one needs nothing and catches the commonest way
// the pair drifts: a relation added to one and not the other.
func TestGuardShapeDeclarationMatchesTheRelationsThisEngineCreates(t *testing.T) {
	declared := map[string]bool{}
	for _, s := range dialect.GuardControlPlaneShapePostgres() {
		if declared[s.Relation] {
			t.Errorf("%s is declared twice", s.Relation)
		}
		declared[s.Relation] = true
		if len(s.Columns) == 0 || len(s.Checks) == 0 || s.PrimaryKey == "" || len(s.UniqueKeys) == 0 {
			t.Errorf("%s is declared with an empty section, which would compare equal to a relation that has none", s.Relation)
		}
	}
	for _, tbl := range dialect.GuardControlPlaneTables() {
		if !declared[tbl] {
			t.Errorf("%s is created by this engine and has no declared shape, so its catalog would be compared against nothing", tbl)
		}
		delete(declared, tbl)
	}
	for tbl := range declared {
		t.Errorf("%s has a declared shape and is not one of the relations this engine creates", tbl)
	}
}

// TestSQLiteObjectNameReadsEveryStatementThisEngineRenders keeps the SQLite comparison from
// silently covering less than it claims.
//
// verifyGuardShapeSQLite refuses outright when a rendered statement cannot be named, so a
// future statement in an unparseable form fails loudly rather than quietly — but that failure
// would arrive at a user's boot. This one arrives in CI.
func TestSQLiteObjectNameReadsEveryStatementThisEngineRenders(t *testing.T) {
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	seen := map[string]bool{}
	for _, s := range dia.GuardControlPlaneStmts() {
		name, ok := sqliteObjectName(s)
		if !ok {
			t.Fatalf("the SQLite comparison cannot name the object in %.60q", s)
		}
		if seen[name] {
			t.Errorf("%s is rendered twice", name)
		}
		seen[name] = true
	}
	// Three tables, three named indexes and two triggers each.
	if want := 3 + 3 + 6; len(seen) != want {
		t.Errorf("the SQLite control plane renders %d named objects, want %d", len(seen), want)
	}
	for _, bad := range []string{"", "SELECT 1", "CREATE VIEW v AS SELECT 1", "CREATE"} {
		if name, ok := sqliteObjectName(bad); ok {
			t.Errorf("%q was named %q; anything this cannot read must be refused, not guessed", bad, name)
		}
	}
}
