// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// guardEditionSevenFixture is a synthetic full-profile census: a two-relation base plus
// every compiled delta. It is the only shape whose graph contains all seven nodes, so it
// is where the DAG's structure can be measured at all.
func guardEditionSevenFixture(t *testing.T) guardManifest {
	t.Helper()
	tables := []string{
		"old_alpha",
		guardEpoch2UserTombstoneTable,
		"old_omega",
		guardEpoch2DirectoryTombstoneTable,
	}
	tables = append(tables, guardEpoch3CommunicationTables[:]...)
	tables = append(tables, guardEpoch4ProtocolTables[:]...)
	tables = append(tables, guardAccessEvidenceTables[:]...)
	m, err := buildGuardManifest(tables)
	if err != nil {
		t.Fatal(err)
	}
	if m.CodeEpoch != 7 {
		t.Fatalf("full-profile census is edition %d, want 7", m.CodeEpoch)
	}
	return m
}

// TestAccessEvidenceGraphIsTheFiniteSameBaseDAG measures the graph itself: seven nodes,
// eight edges, and exactly the parents the contract names.
//
// It is a structural test on purpose. Every later behavior — which edge v9 crosses, which
// lineage the selector accepts, what the module seam may do — is a consequence of this
// shape, and a change to it that nothing measured would be invisible until a real
// database took the wrong route.
func TestAccessEvidenceGraphIsTheFiniteSameBaseDAG(t *testing.T) {
	t.Parallel()
	graph, err := guardEditionGraphFor(guardEditionSevenFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := graph.epochsDescending(); len(got) != 7 {
		t.Fatalf("graph has %d nodes (%v), want the seven compiled editions", len(got), got)
	}
	wantParents := map[int64][]int64{
		1: nil,
		2: {1},
		3: {2},
		4: {3},
		5: {2},
		6: {3, 5},
		7: {4, 6},
	}
	edges := 0
	for epoch, want := range wantParents {
		got := graph.parentEpochs(epoch)
		if len(got) != len(want) {
			t.Fatalf("edition %d has parents %v, want %v", epoch, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("edition %d has parents %v, want %v", epoch, got, want)
			}
		}
		edges += len(got)
	}
	if edges != 8 {
		t.Fatalf("graph has %d edges, want the eight compiled transitions", edges)
	}
	// EVERY node shares one base fingerprint. That is what "same-B" means as a
	// mechanism rather than a label: the edges exist because the bases are equal, and
	// nothing here can compare two different products' editions.
	for _, epoch := range graph.epochsDescending() {
		node, _ := graph.node(epoch)
		if node.BaseSHA256 != graph.BaseSHA256 {
			t.Fatalf("edition %d carries base %s, want the graph's %s",
				epoch, hexDigest(node.BaseSHA256), hexDigest(graph.BaseSHA256))
		}
	}
	if len(graph.Base) != 2 || graph.Base[0] != "old_alpha" || graph.Base[1] != "old_omega" {
		t.Fatalf("base census = %v, want exactly the two non-delta relations", graph.Base)
	}
}

// TestAccessEvidenceEdgesAreExactNonemptyDeltas proves each edge adds its complete named
// delta, removes nothing, and carries every other spec forward byte-identically.
func TestAccessEvidenceEdgesAreExactNonemptyDeltas(t *testing.T) {
	t.Parallel()
	graph, err := guardEditionGraphFor(guardEditionSevenFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		from, to int64
		delta    guardEditionDelta
		size     int
	}{
		{1, 2, guardDeltaDirectory, 2},
		{2, 3, guardDeltaCommunication, 5},
		{3, 4, guardDeltaProtocol, 2},
		{2, 5, guardDeltaAccessEvidence, 4},
		{3, 6, guardDeltaAccessEvidence, 4},
		{4, 7, guardDeltaAccessEvidence, 4},
		{5, 6, guardDeltaCommunication, 5},
		{6, 7, guardDeltaProtocol, 2},
	} {
		t.Run(fmt.Sprintf("%d-%d", tc.from, tc.to), func(t *testing.T) {
			edge, eerr := graph.edge(tc.from, tc.to)
			if eerr != nil {
				t.Fatal(eerr)
			}
			if len(edge.Additions) != tc.size {
				t.Fatalf("edge adds %d relations, want %d", len(edge.Additions), tc.size)
			}
			want := map[string]bool{}
			for _, table := range guardEditionDeltaTables(tc.delta) {
				want[table] = true
			}
			for _, spec := range edge.Additions {
				if !want[spec.Key.Relation] {
					t.Fatalf("edge adds %s, which is not part of the %s delta", spec.Key.Relation, tc.delta)
				}
				delete(want, spec.Key.Relation)
			}
			if len(want) != 0 {
				t.Fatalf("edge omits %v from the %s delta", want, tc.delta)
			}
			if len(edge.From.Specs)+len(edge.Additions) != len(edge.To.Specs) {
				t.Fatalf("edge removes relations: %d + %d != %d",
					len(edge.From.Specs), len(edge.Additions), len(edge.To.Specs))
			}
			for _, old := range edge.From.Specs {
				now, ok := edge.To.lookup(old.Key)
				if !ok || !guardSpecsByteIdentical(old, now) {
					t.Fatalf("edge does not carry %s forward byte-identically", old.Key)
				}
			}
		})
	}
	// The pairs that are NOT edges: an edition never skips a delta, and no edge adds two.
	for _, tc := range [][2]int64{{2, 6}, {2, 7}, {3, 7}, {1, 5}, {5, 7}, {4, 6}, {5, 2}} {
		if _, eerr := graph.edge(tc[0], tc[1]); eerr == nil {
			t.Fatalf("edition %d -> %d is not a compiled edge but was built", tc[0], tc[1])
		} else if !errors.Is(eerr, ErrGuardManifestNoEdge) {
			t.Fatalf("edition %d -> %d = %v, want ErrGuardManifestNoEdge", tc[0], tc[1], eerr)
		}
	}
}

// TestAccessEvidenceCoreOnlyGraphStopsAtItsOwnCensus is the same-base rule seen from the
// side that matters operationally: a core-only build has no edition 6 or 7 at all, so a
// database whose module census differs has no edge rather than a weaker one.
func TestAccessEvidenceCoreOnlyGraphStopsAtItsOwnCensus(t *testing.T) {
	t.Parallel()
	coreOnly, err := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	got := coreOnly.epochsDescending()
	want := []int64{5, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("core-only graph = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("core-only graph = %v, want %v", got, want)
		}
	}
	// The module-bearing profile is a DIFFERENT base, so its edition 5 is a different
	// digest and neither graph's edges can authorize the other's history.
	full, err := guardEditionGraphFor(guardEditionSevenFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if coreOnly.BaseSHA256 == full.BaseSHA256 {
		t.Fatal("a core-only census and a module-bearing census share a base fingerprint")
	}
	coreFive, _ := coreOnly.node(5)
	fullFive, _ := full.node(5)
	if coreFive.Manifest.CodeSHA256 == fullFive.Manifest.CodeSHA256 {
		t.Fatal("two profiles' edition 5 share a manifest digest")
	}
	edge, err := full.edge(2, 5)
	if err != nil {
		t.Fatal(err)
	}
	coreTwo, _ := coreOnly.node(2)
	if edge.authorizes(coreTwo.Manifest.Format, coreTwo.Manifest.CodeEpoch, coreTwo.Manifest.CodeSHA256) {
		t.Fatal("a module-bearing 2 -> 5 edge authorized a core-only epoch-2 database")
	}
}

// TestAccessEvidenceCensusIsOrderIndependentAndDriftSensitive covers two contract cases at
// once because they are the same measurement from opposite sides: permuting the SAME
// declarations must change nothing, and adding or mutating ONE must change everything
// downstream of it.
func TestAccessEvidenceCensusIsOrderIndependentAndDriftSensitive(t *testing.T) {
	t.Parallel()
	base := []string{
		"old_alpha", "old_omega",
		guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable,
	}
	base = append(base, guardAccessEvidenceTables[:]...)
	first, err := buildGuardEditionGraph(base)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := append([]string(nil), base...)
	sort.Sort(sort.Reverse(sort.StringSlice(shuffled)))
	second, err := buildGuardEditionGraph(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseSHA256 != second.BaseSHA256 || first.Current != second.Current {
		t.Fatalf("permuted census produced base %s/edition %d, want %s/%d",
			hexDigest(second.BaseSHA256), second.Current, hexDigest(first.BaseSHA256), first.Current)
	}
	for _, epoch := range first.epochsDescending() {
		a, _ := first.node(epoch)
		b, ok := second.node(epoch)
		if !ok || a.Manifest.CodeSHA256 != b.Manifest.CodeSHA256 {
			t.Fatalf("permuted census changed edition %d's digest", epoch)
		}
	}
	firstLineages, err := first.lineagesEndingAt(first.Current)
	if err != nil {
		t.Fatal(err)
	}
	secondLineages, err := second.lineagesEndingAt(second.Current)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstLineages) != len(secondLineages) {
		t.Fatalf("permuted census changed the lineage count %d -> %d", len(firstLineages), len(secondLineages))
	}
	for i := range firstLineages {
		if firstLineages[i].path() != secondLineages[i].path() {
			t.Fatalf("permuted census changed lineage %d: %s vs %s",
				i, firstLineages[i].path(), secondLineages[i].path())
		}
	}

	// ONE unknown append-only declaration changes the base fingerprint and every node
	// above it, so no compiled edge of the original graph authorizes the new census.
	drifted, err := buildGuardEditionGraph(append(append([]string(nil), base...), "unrelated_new_table"))
	if err != nil {
		t.Fatal(err)
	}
	if drifted.BaseSHA256 == first.BaseSHA256 {
		t.Fatal("an unknown append-only declaration was absorbed into the base census")
	}
	originalEdge, err := first.edge(2, 5)
	if err != nil {
		t.Fatal(err)
	}
	driftedTwo, _ := drifted.node(2)
	if originalEdge.authorizes(driftedTwo.Manifest.Format, driftedTwo.Manifest.CodeEpoch, driftedTwo.Manifest.CodeSHA256) {
		t.Fatal("the original 2 -> 5 edge authorized a drifted epoch-2 census")
	}
}

// TestAccessEvidencePartialDeltaCensusRefuses proves a build that declares some of the
// access-evidence relations and not others is refused before it can record a manifest a
// complete build would later call same-epoch drift.
func TestAccessEvidencePartialDeltaCensusRefuses(t *testing.T) {
	t.Parallel()
	for i := 1; i < len(guardAccessEvidenceTables); i++ {
		tables := []string{
			"old_alpha", "old_omega",
			guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable,
		}
		tables = append(tables, guardAccessEvidenceTables[:i]...)
		if _, err := buildGuardManifest(tables); err == nil {
			t.Fatalf("a census with %d of %d access-evidence relations was accepted",
				i, len(guardAccessEvidenceTables))
		} else if !strings.Contains(err.Error(), "census is partial") {
			t.Fatalf("partial access-evidence census = %v, want a partial-census refusal", err)
		}
	}
}

// TestAccessEvidenceLineagesToEditionSixAreOrderedAndDistinct is the DAG's discriminator.
//
// Edition 6 can be reached from edition 3 by the access-evidence edge or from edition 5 by
// the communication edge. Both routes activate exactly the same relations, and the only
// thing that tells the two databases apart is the ORDER their inventories recorded. If the
// expectation builder ever sorted its output, both would match every inventory and the
// selector would be choosing an upgrade history at random.
func TestAccessEvidenceLineagesToEditionSixAreOrderedAndDistinct(t *testing.T) {
	t.Parallel()
	graph, err := guardEditionGraphFor(guardEditionSevenFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	lineages, err := graph.lineagesEndingAt(6)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[guardEditionPath][]string{}
	for _, lineage := range lineages {
		relations := make([]string, 0)
		for _, expectation := range guardActivationExpectationsForLineage(lineage) {
			relations = append(relations, fmt.Sprintf("%s@%d", expectation.Spec.Key.Relation, expectation.Epoch))
		}
		seen[lineage.path()] = relations
	}
	viaEvidence, okA := seen["2>3>6"]
	viaCommunication, okB := seen["2>5>6"]
	if !okA || !okB {
		t.Fatalf("edition 6 lineages = %v, want both 2>3>6 and 2>5>6", seen)
	}
	if len(viaEvidence) != len(viaCommunication) {
		t.Fatalf("the two routes activate different relation counts: %d vs %d",
			len(viaEvidence), len(viaCommunication))
	}
	same := true
	for i := range viaEvidence {
		if viaEvidence[i] != viaCommunication[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("the two edition-6 routes produce identical activation sequences, so no inventory can tell them apart")
	}
	// And every enumerated lineage is unique, so "accept exactly one" is decidable.
	paths := map[guardEditionPath]bool{}
	for _, lineage := range lineages {
		if paths[lineage.path()] {
			t.Fatalf("lineage %s is enumerated twice", lineage.path())
		}
		paths[lineage.path()] = true
	}
}

// TestAccessEvidenceV9SealGoldenBodies freezes the durable ABI of the v9 completion seal
// against an independently stated expectation, so a change to its domain, attempt id,
// predecessor binding or prestate is a red test rather than a silent new receipt shape.
//
// The two seals differ ONLY in the terminal receipt they name, which is exactly the
// distinction that makes a fresh completion unusable as a direct one.
func TestAccessEvidenceV9SealGoldenBodies(t *testing.T) {
	t.Parallel()
	current := coreOnlyAccessEvidenceManifest(t)
	freshV7, err := guardV7Seal(current, guardV7SealCompletion, false)
	if err != nil {
		t.Fatal(err)
	}
	directV7, err := guardV7Seal(current, guardV7SealCompletion, true)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := guardAccessEvidenceV9Seal(current, freshV7.ReceiptID, false)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := guardAccessEvidenceV9Seal(current, directV7.ReceiptID, true)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ReceiptID == direct.ReceiptID {
		t.Fatal("the fresh and direct v9 seals share a receipt id, so one could stand in for the other")
	}
	for _, seal := range []guardReceipt{fresh, direct} {
		if seal.AttemptID != guardAccessEvidenceV9AttemptID {
			t.Fatalf("v9 seal attempt = %q, want %q", seal.AttemptID, guardAccessEvidenceV9AttemptID)
		}
		if seal.Kind != guardReceiptKindBootstrap || seal.Intent != guardIntentBootstrap {
			t.Fatalf("v9 seal kind/intent = %s/%s", seal.Kind, seal.Intent)
		}
		if seal.Epoch != current.CodeEpoch || seal.Format != current.Format ||
			seal.CodeSHA256 != current.CodeSHA256 || seal.RetainedRevision != 0 {
			t.Fatalf("v9 seal is not bound to the current target: %+v", seal)
		}
		if seal.Key.Relation != guardGateEventsTable {
			t.Fatalf("v9 seal target = %s, want the first fixed metadata relation", seal.Key)
		}
		if seal.FromEnableState.String() != guardStateAlways || seal.ToEnableState != guardStateAlways {
			t.Fatalf("v9 seal states = %s -> %s, want A -> A", seal.FromEnableState, seal.ToEnableState)
		}
		if !seal.PredecessorReceiptID.Valid {
			t.Fatal("v9 seal has no predecessor receipt")
		}
		body, berr := seal.bodyDigest()
		if berr != nil {
			t.Fatal(berr)
		}
		if body != seal.ReceiptID {
			t.Fatal("v9 seal id is not the digest of its own body")
		}
	}
	if fresh.PredecessorReceiptID.D != freshV7.ReceiptID {
		t.Fatal("the fresh v9 seal does not name the fresh v7 completion as its terminal predecessor")
	}
	if direct.PredecessorReceiptID.D != directV7.ReceiptID {
		t.Fatal("the direct v9 seal does not name the direct v7 completion as its terminal predecessor")
	}
	// Its unit id is a THIRD domain: neither a bootstrap unit nor a v7 seal unit, so no
	// receipt of one kind can ever be presented as another.
	meta, err := guardMetadataSpecs(current.Format)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapUnit, err := guardBootstrapUnitID(current.Format, meta[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	startUnit, err := guardV7SealUnitID(current.Format, guardV7SealStart, meta[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	completionUnit, err := guardV7SealUnitID(current.Format, guardV7SealCompletion, meta[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{bootstrapUnit, startUnit, completionUnit} {
		if fresh.UnitID == other {
			t.Fatalf("the v9 seal reuses unit id %s", other)
		}
	}
	// An edition that does not carry the access-evidence delta has nothing to seal.
	if _, err := guardAccessEvidenceV9Seal(coreOnlyCurrentGuardManifest(t), freshV7.ReceiptID, false); err == nil {
		t.Fatal("an epoch-2 manifest produced a v9 completion seal")
	}
	// And a seal with no terminal predecessor is refused rather than written with a zero.
	if _, err := guardAccessEvidenceV9Seal(current, [32]byte{}, false); err == nil {
		t.Fatal("a v9 seal was produced without a terminal v7 predecessor")
	}
}

// --- Real SQLite databases -------------------------------------------------------------

// coreOnlyRegistry is the closed registry a `register == nil` boot produces: core
// descriptors and no module ones. It is the ratified <=v5 source profile, and passing it
// explicitly is how a direct-path test states which profile it claims to reproduce.
func coreOnlyRegistry(t *testing.T) *registry {
	t.Helper()
	reg := newRegistry()
	for _, d := range coreDescriptors() {
		if err := reg.registerCore(d); err != nil {
			t.Fatal(err)
		}
	}
	reg.closed = true
	return reg
}

func accessEvidenceSQLiteStore(t *testing.T) (store.Config, *sql.DB, dialect.Dialect) {
	t.Helper()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	cfg := store.Config{Engine: store.EngineSQLite, DSN: t.TempDir() + "/access-evidence.db", MaxConns: 1}
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return cfg, db, dia
}

// TestAccessEvidenceFreshSQLiteSealsEditionFive is the fresh-verify-and-seal action end to
// end: current v2 creates the four relations, v7 completes, and v9 verifies them and
// appends its completion witness without inventing a transition.
func TestAccessEvidenceFreshSQLiteSealsEditionFive(t *testing.T) {
	ctx := context.Background()
	cfg, db, dia := accessEvidenceSQLiteStore(t)
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("fresh core-only Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	current := coreOnlyAccessEvidenceManifest(t)
	history, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if history.Kind != guardEditionHistoryCurrentV9Completed || !history.CompletedV9 {
		t.Fatalf("fresh history = %s (completedV9=%t), want %s",
			history.Kind, history.CompletedV9, guardEditionHistoryCurrentV9Completed)
	}
	// NO TRANSITION was invented: v6 bootstrapped edition 5 directly, so there is no
	// 2 -> 5 edge in this database's inventory and no edition seal beside the two
	// completions.
	if history.Path != "5" {
		t.Fatalf("fresh lineage = %s, want the trivial edition-5 path", history.Path)
	}
	if got := countRows(t, db, dia.Rebind(
		"SELECT COUNT(*) FROM "+guardInventoryEventsTable+" WHERE code_epoch <> ?"), current.CodeEpoch); got != 0 {
		t.Fatalf("a fresh edition-5 bootstrap activated %d relations under another edition", got)
	}
	if err := verifyAccessEvidenceRelationsExact(ctx, db, dia, coreDescriptors()); err != nil {
		t.Fatalf("fresh access-evidence relations: %v", err)
	}
	if got := countRows(t, db, dia.Rebind(
		"SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
		coreAccessEvidenceMigrationVersion); got != 1 {
		t.Fatalf("v9 tracking rows = %d, want exactly one", got)
	}
	// A second Open is a verification, not a replay.
	receipts := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
	inventory := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
	reopened, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("reopen a completed v9 database: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receipts {
		t.Fatalf("reopen changed receipts %d -> %d", receipts, got)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventory {
		t.Fatalf("reopen changed inventory %d -> %d", inventory, got)
	}
}

// TestAccessEvidenceCompletedV9DamageIsReportedNotRepaired is section 10.2 case 6: the
// three ways a completed upgrade can be dismantled, each refused and each left exactly as
// the test found it.
//
// The seal is what makes the third case decidable. Deleting the tracking row AND the four
// relations reproduces the authorized pre-v9 checkpoint byte for byte in every durable
// place except the receipt ledger, which is append-only and still holds the completion.
func TestAccessEvidenceCompletedV9DamageIsReportedNotRepaired(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(*testing.T, *sql.DB, dialect.Dialect)
	}{
		{
			name: "v9 tracker deleted alone",
			damage: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				if _, err := db.ExecContext(context.Background(), dia.Rebind(
					"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
					coreAccessEvidenceMigrationVersion); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "one access-evidence relation dropped",
			damage: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				if _, err := db.ExecContext(context.Background(),
					"DROP TABLE main."+actionObservationTable); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "tracker and every relation removed",
			damage: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				if _, err := db.ExecContext(context.Background(), dia.Rebind(
					"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
					coreAccessEvidenceMigrationVersion); err != nil {
					t.Fatal(err)
				}
				for _, table := range accessEvidenceRelationNames {
					if _, err := db.ExecContext(context.Background(), "DROP TABLE main."+table); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			tc.damage(t, db, dia)
			receipts := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
			inventory := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
			present := accessEvidenceTablesPresent(t, db)

			reopened, err := Open(ctx, cfg, nil)
			if err == nil {
				_ = reopened.Close()
				t.Fatal("Open accepted a dismantled v9 database")
			}
			if got := accessEvidenceTablesPresent(t, db); got != present {
				t.Fatalf("the refused Open recreated relations: %d -> %d present", present, got)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receipts {
				t.Fatalf("the refused Open changed receipts %d -> %d", receipts, got)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventory {
				t.Fatalf("the refused Open changed inventory %d -> %d", inventory, got)
			}
			if got := countRows(t, db, dia.Rebind(
				"SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
				coreAccessEvidenceMigrationVersion); got != 0 && strings.Contains(tc.name, "deleted") {
				t.Fatalf("the refused Open restored %d v9 tracking rows", got)
			}
		})
	}
}

// TestAccessEvidenceAlteredRelationIsReportedBeforeReconcile is the "still there and no
// longer what its descriptor renders" case: the relation exists, one append-only guard leg
// is gone, and the generic reconciler is strictly additive — so a boot that did not compare
// the COMPLETE contract would put the trigger back and serve.
//
// WHERE THE REFUSAL COMES FROM MOVED, and the move is the correction of review finding F2.
// It used to be the pre-reconcile verification, after six schema versions had committed on
// a damaged pre-v6 checkpoint. It is now the read-only boot classifier, before any
// migration: `v9-complete`'s row says the four relations are `all exact`, and this is what
// proving that cell looks like. The assertion accepts either fence by name rather than
// pinning one, because both are contracted and only their ORDER changed.
//
// The two are now genuinely redundant for damage that is present when a boot starts, and
// the r2 report says so instead of implying the later one is load-bearing here.
func TestAccessEvidenceAlteredRelationIsReportedBeforeReconcile(t *testing.T) {
	ctx := context.Background()
	cfg, db, dia := accessEvidenceSQLiteStore(t)
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// SQLite's append-only defense is a trigger PAIR, and the update leg is the one a
	// forger would want gone: without it a row's content can be rewritten in place while
	// the relation still refuses deletes and still looks guarded to a shallow check.
	const guard = policyArtifactTable + "_no_update"
	if _, err := db.ExecContext(ctx, "DROP TRIGGER main."+guard); err != nil {
		t.Fatalf("remove the append-only guard: %v", err)
	}
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?", guard); got != 0 {
		t.Fatalf("the fixture did not remove %s", guard)
	}

	reopened, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("Open accepted an access-evidence relation whose append-only guard was removed")
	}
	if !strings.Contains(err.Error(), "requires the exact access-evidence contract") &&
		!strings.Contains(err.Error(), "verify core access-evidence relations") &&
		!strings.Contains(err.Error(), "per-boot verification") {
		t.Fatalf("refusal = %v, want the start-shape or the per-boot access-evidence verification", err)
	}
	// AND THE DAMAGE IS STILL THERE. A boot that recreated the guard and then refused
	// would be laundering with extra steps: the object looking correct again does not
	// unmake the event.
	if got := countRows(t, db,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?", guard); got != 0 {
		t.Fatalf("the refused Open recreated %s", guard)
	}
	if got := accessEvidenceTablesPresent(t, db); got != len(accessEvidenceRelationNames) {
		t.Fatalf("the refused Open changed the relation count to %d", got)
	}
	_ = dia
}

func accessEvidenceTablesPresent(t *testing.T, db *sql.DB) int {
	t.Helper()
	present := 0
	for _, table := range accessEvidenceRelationNames {
		present += countRows(t, db,
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table)
	}
	return present
}

// TestAccessEvidencePartialPrecreationRefusesEveryProperSubset is section 10.2 case 7, and
// it now enumerates what its name says.
//
// THE PREVIOUS VERSION CLAIMED "every nonempty proper subset" AND CREATED THREE PREFIXES.
// The independent review caught the gap: three prefixes of a four-element set are not its
// fourteen nonempty proper subsets, and a prefix cannot distinguish "the first k were
// created" from "these particular k were". Every subset is enumerated by its bitmask, so
// the claim and the measurement are the same sentence.
func TestAccessEvidencePartialPrecreationRefusesEveryProperSubset(t *testing.T) {
	ordered, err := exactAccessEvidenceDescriptors(coreDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	full := 1<<len(ordered) - 1
	for mask := 1; mask < full; mask++ {
		names := make([]string, 0, len(ordered))
		for i, desc := range ordered {
			if mask&(1<<i) != 0 {
				names = append(names, desc.Table)
			}
		}
		t.Run(strings.Join(names, "+"), func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := directV7StoreFixture(t, store.EngineSQLite)
			seedCoreV5(t, db, dia, false)
			for i, desc := range ordered {
				if mask&(1<<i) == 0 {
					continue
				}
				for _, stmt := range dia.CreateTableStmts(desc) {
					if _, err := db.ExecContext(ctx, stmt); err != nil {
						t.Fatal(err)
					}
				}
			}
			if got := accessEvidenceTablesPresent(t, db); got != len(names) {
				t.Fatalf("precreated %d relations, observed %d", len(names), got)
			}
			before := accessEvidenceReadDurableCensus(t, db)

			st, err := Open(ctx, cfg, nil)
			if err == nil {
				_ = st.Close()
				t.Fatal("Open accepted a partial access-evidence precreation")
			}
			if !strings.Contains(err.Error(), "partial access-evidence relation set") {
				t.Fatalf("partial precreation = %v, want the partial-set refusal", err)
			}
			accessEvidenceAssertDurableCensusUnchanged(t,
				"refused partial precreation", before, accessEvidenceReadDurableCensus(t, db))
		})
	}
}

// TestAccessEvidencePreV6FamilyMixturesAreRefused completes the other axis: the pairs of
// COMPLETE families a pre-v6 database can present, and the partial directory set.
//
// R2 §5.2 gives a pre-v6 database exactly two authored rows — both families absent (the
// allowlisted legacy source) or both `all exact` (a current v2 that crashed before v6).
// Everything else is an unlisted cross-product, and the point of enumerating them is that
// "not listed" has to be a refusal that names what it saw rather than a fall-through.
func TestAccessEvidencePreV6FamilyMixturesAreRefused(t *testing.T) {
	evidence, err := exactAccessEvidenceDescriptors(coreDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := exactCoreDirectoryDescriptors(coreDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		precreate []model.EntityDescriptor
		want      string
	}{
		{"complete access evidence, no directory", evidence, "is not an authored start"},
		{"complete directory, no access evidence", directory, "is not an authored start"},
		{"one directory relation", directory[:1], "partial core directory relation set"},
		{"two directory relations", directory[:2], "partial core directory relation set"},
		// The fourth combination — BOTH families complete and exact over a <=v5 tracker —
		// is deliberately absent from this list, and its absence is the finding rather
		// than an omission. That state IS `fresh-current-pre-v6`: R2 §5.2's row for it is
		// "a contiguous current prefix ending at v2..v5, both families all exact", which
		// is what a current v2 that crashed before v6 leaves. It is accepted, and
		// TestAccessEvidencePreV6BothFamiliesExactIsTheFreshCheckpoint is the positive
		// control that says so out loud.
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := directV7StoreFixture(t, store.EngineSQLite)
			seedCoreV5(t, db, dia, false)
			for _, desc := range tc.precreate {
				for _, stmt := range dia.CreateTableStmts(desc) {
					if _, err := db.ExecContext(ctx, stmt); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := accessEvidenceReadDurableCensus(t, db)
			st, err := Open(ctx, cfg, nil)
			if err == nil {
				_ = st.Close()
				t.Fatalf("Open accepted a pre-v6 database with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, want one naming %q", err, tc.want)
			}
			accessEvidenceAssertDurableCensusUnchanged(t,
				"refused pre-v6 mixture", before, accessEvidenceReadDurableCensus(t, db))
		})
	}
}

// TestAccessEvidencePreV6BothFamiliesExactIsTheFreshCheckpoint is the positive control the
// mixture table above points at.
//
// A <=v5 tracker with BOTH families complete and exact is not an unlisted cross-product: it
// is precisely R2 §5.2's `fresh-current-pre-v6`, the shape a current v2 leaves when the
// boot dies before v6. The classifier accepts it, and the boot completes through the newest
// core migration this binary supports.
//
// It is stated as its own case because the honest limit is worth naming: a contiguous
// tracker prefix cannot distinguish "current v2 created these" from "something else
// created exactly what current v2 would have". What the classifier can and does demand is
// that the objects be byte-for-byte the current contract, which is the same evidence the
// fresh path would have produced.
func TestAccessEvidencePreV6BothFamiliesExactIsTheFreshCheckpoint(t *testing.T) {
	ctx := context.Background()
	cfg, db, dia := directV7StoreFixture(t, store.EngineSQLite)
	seedCoreV5(t, db, dia, false)
	directory, err := exactCoreDirectoryDescriptors(coreDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := exactAccessEvidenceDescriptors(coreDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	for _, desc := range append(append([]model.EntityDescriptor(nil), directory...), evidence...) {
		for _, stmt := range dia.CreateTableStmts(desc) {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	graph, err := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := classifyAccessEvidenceBoot(ctx, db, dia, graph, coreDescriptors(), coreOnlyRegistry(t), nil,
		guardEventFenceFacts{})
	if err != nil {
		t.Fatalf("classify a complete pre-v6 checkpoint: %v", err)
	}
	if plan.Class != accessEvidenceStartFreshCurrentPreV6 || plan.Action != accessEvidenceV9FreshVerify {
		t.Fatalf("class = %s/%s, want %s/%s",
			plan.Class, plan.Action, accessEvidenceStartFreshCurrentPreV6, accessEvidenceV9FreshVerify)
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open a complete pre-v6 checkpoint: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "SELECT MAX(version) FROM "+coreTrackingTable); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	// The guard edition is a separate axis from the core migration ceiling, so this
	// assertion stays pinned to v9 on purpose: v10's own descriptor is not append-only
	// ("does not join the append-only edition", userauthority.go), so the newest edition
	// the manifest declares is still the v9 access-evidence one.
	history, err := verifyGuardEditionHistory(ctx, db, dia, coreOnlyAccessEvidenceManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if history.Kind != guardEditionHistoryCurrentV9Completed {
		t.Fatalf("history = %s, want %s", history.Kind, guardEditionHistoryCurrentV9Completed)
	}
}

// TestAccessEvidenceLegacyModuleCensusCrossProduct is the module half of the same axis.
//
// R2 §5.2 requires of the allowlisted <=v5 class that every currently registered module
// table already have BOTH an applied_module_tables row and a physical relation, with no
// tracking row outside that set — and, separately, that the SOURCE be the ratified
// core-only profile. The five arms are the four ways that can be false plus the control,
// and each is asserted by the relation it names rather than only by refusing.
func TestAccessEvidenceLegacyModuleCensusCrossProduct(t *testing.T) {
	const moduleTable = "review_legacy_event"
	moduleDescriptor := func() model.EntityDescriptor {
		desc := widgetDescriptor
		desc.Kind = "review.legacy_event"
		desc.Table = moduleTable
		desc.AppendOnly = true
		return desc
	}
	register := func(reg store.ExtensionRegistry) error { return reg.Register(moduleDescriptor()) }

	for _, tc := range []struct {
		name        string
		register    func(store.ExtensionRegistry) error
		createRow   bool
		createTable bool
		wantRefusal string
	}{
		{name: "control: core-only source, no module census", register: nil},
		{
			name:     "registered module with neither tracker row nor relation",
			register: register, wantRefusal: "no " + moduleTablesTracking + " row: [" + moduleTable + "]",
		},
		{
			name:     "registered module with a tracker row and no relation",
			register: register, createRow: true,
			wantRefusal: "no physical relation: [" + moduleTable + "]",
		},
		{
			name:     "registered module with a relation and no tracker row",
			register: register, createTable: true,
			wantRefusal: "no " + moduleTablesTracking + " row: [" + moduleTable + "]",
		},
		{
			// Both halves of the census check pass; the SOURCE is still not the ratified
			// profile, and that is a separate fact rather than the same one restated.
			name:     "registered module fully present on the source",
			register: register, createRow: true, createTable: true,
			wantRefusal: accessEvidenceAllowlistedLegacySourceCommit,
		},
		{
			name:     "core-only source carrying an unrelated module tracker row",
			register: nil, createRow: true,
			wantRefusal: "records module tables [" + moduleTable + "]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := directV7StoreFixture(t, store.EngineSQLite)
			seedCoreV5(t, db, dia, false)
			if tc.createRow {
				if _, err := db.ExecContext(ctx,
					"CREATE TABLE IF NOT EXISTS "+moduleTablesTracking+
						" (table_name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, dia.Rebind(
					"INSERT INTO "+moduleTablesTracking+" (table_name, applied_at) VALUES (?, ?)"),
					moduleTable, "2026-09-06T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.createTable {
				for _, stmt := range dia.CreateTableStmts(moduleDescriptor()) {
					if _, err := db.ExecContext(ctx, stmt); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := accessEvidenceReadDurableCensus(t, db)

			st, err := Open(ctx, cfg, tc.register)
			if st != nil {
				_ = st.Close()
			}
			if tc.wantRefusal == "" {
				if err != nil {
					t.Fatalf("control: the ratified core-only profile failed: %v", err)
				}
				if got := countRows(t, db,
					"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version=9"); got != 1 {
					t.Fatalf("control: v9 tracking rows = %d, want 1", got)
				}
				return
			}
			if err == nil {
				t.Fatalf("Open accepted a <=v5 source with %s", tc.name)
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Fatalf("refusal = %v, want ErrGuardManifestNoEdge", err)
			}
			if !strings.Contains(err.Error(), tc.wantRefusal) {
				t.Fatalf("refusal = %v, want one naming %q", err, tc.wantRefusal)
			}
			accessEvidenceAssertDurableCensusUnchanged(t,
				"refused legacy module census", before, accessEvidenceReadDurableCensus(t, db))
		})
	}
}

// TestAccessEvidenceOldMaxV8ReaderRefusesBeforeDDL is section 9's last row.
//
// WHAT IT PROVES AND WHAT IT DOES NOT. It calls the very function an older binary runs as
// the FIRST operation inside its migration lock, with that binary's supported ceiling, and
// shows it refuses a database this binary has just brought to its own newest supported core
// migration, before ensureTracking can perform any DDL. Both halves of the refusal are
// derived from the two constants rather than written out, so appending a core migration moves
// the case forward instead of turning it into a pin on a version nobody installs.
//
// It does not execute a separately built v26.8 executable; that fixture is named as a
// remaining obligation in the implementation report rather than approximated here.
func TestAccessEvidenceOldMaxV8ReaderRefusesBeforeDDL(t *testing.T) {
	ctx := context.Background()
	cfg, db, dia := accessEvidenceSQLiteStore(t)
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	err = preflightCoreMigrationVersion(ctx, db, dia, coreLineageMigrationVersion)
	if !errors.Is(err, ErrCoreSchemaVersionAhead) {
		t.Fatalf("max-v%d preflight over a v%d database = %v, want ErrCoreSchemaVersionAhead",
			coreLineageMigrationVersion, coreSupportedMigrationVersion, err)
	}
	wantVersions := fmt.Sprintf("database=%d binary=%d",
		coreSupportedMigrationVersion, coreLineageMigrationVersion)
	if !strings.Contains(err.Error(), wantVersions) {
		t.Fatalf("refusal does not name the exact versions %q: %v", wantVersions, err)
	}
	// The current binary still opens it, so the refusal is about the ceiling and not
	// about the database being broken.
	if err := preflightCoreMigrationVersion(ctx, db, dia, coreSupportedMigrationVersion); err != nil {
		t.Fatalf("the current ceiling refused its own database: %v", err)
	}
}

// TestAccessEvidenceUpgradePreservesTenantRows is section 10.2 case 11 on SQLite: rows
// written by the legacy source before the upgrade survive it byte for byte, per tenant.
func TestAccessEvidenceUpgradePreservesTenantRows(t *testing.T) {
	ctx := context.Background()
	cfg, db, dia := directV7StoreFixture(t, store.EngineSQLite)
	seedCoreV5(t, db, dia, false)
	// The reserved SYSTEM organization, exactly as EnsureSystemTenant has written it on
	// the first boot of every deployment since the initial import (system.go: id and
	// tenant are SystemTenantID, "System"/"system", active, version 1). A legacy source
	// always carries this row because the active writer provisions it before any
	// business tenant exists, and since core v10 the boot inventory treats a business
	// organization without it as corruption. The row is seeded with the same legacy
	// timestamp as the tenants below; the signed genesis audit event is deliberately
	// not fabricated, because this case measures org-row survival and the upgrade
	// consumes the organization inventory, not the SYSTEM chain.
	if _, err := db.ExecContext(ctx, dia.Rebind(
		"INSERT INTO orgs (id, tenant_id, created_at, updated_at, version, name, slug, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"),
		model.SystemTenantID.String(), model.SystemTenantID.String(),
		"2026-09-06T00:00:00Z", "2026-09-06T00:00:00Z", 1,
		"System", "system", "active"); err != nil {
		t.Fatalf("seed the legacy SYSTEM organization row: %v", err)
	}
	// Canonical UUIDv7 tenants, and each organization's id IS its tenant id. Both are
	// directory invariants the boot enumeration enforces, so a fixture that ignored
	// either would fail the upgrade for a reason that has nothing to do with it.
	tenants := []string{
		"01920000-0000-7000-8000-0000000000a1",
		"01920000-0000-7000-8000-0000000000b2",
	}
	for i, tenant := range tenants {
		if _, err := db.ExecContext(ctx, dia.Rebind(
			"INSERT INTO orgs (id, tenant_id, created_at, updated_at, version, name, slug, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"),
			tenant, tenant,
			"2026-09-06T00:00:00Z", "2026-09-06T00:00:00Z", 1,
			fmt.Sprintf("legacy-org-%d", i+1), fmt.Sprintf("legacy-org-%d", i+1), "active"); err != nil {
			t.Fatalf("seed legacy row for tenant %d: %v", i+1, err)
		}
	}
	before := accessEvidenceOrgCensus(t, db, dia)
	if len(before) != len(tenants)+1 {
		t.Fatalf("legacy census = %d rows, want the SYSTEM row plus %d tenants", len(before), len(tenants))
	}

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("direct upgrade Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	after := accessEvidenceOrgCensus(t, db, dia)
	if len(before) != len(after) {
		t.Fatalf("the upgrade changed the row count %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("the upgrade changed row %d: %q -> %q", i, before[i], after[i])
		}
	}
	if err := verifyAccessEvidenceRelationsExact(ctx, db, dia, coreDescriptors()); err != nil {
		t.Fatalf("upgraded access-evidence relations: %v", err)
	}
	for _, table := range accessEvidenceRelationNames {
		if got := countRows(t, db, "SELECT COUNT(*) FROM main."+table); got != 0 {
			t.Fatalf("%s holds %d rows after the upgrade; v9 backfills nothing", table, got)
		}
	}
}

func accessEvidenceOrgCensus(t *testing.T, db *sql.DB, dia dialect.Dialect) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT tenant_id, id, name, version FROM orgs ORDER BY tenant_id, id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // read-only census
	var out []string
	for rows.Next() {
		var tenant, id, name string
		var version int64
		if err := rows.Scan(&tenant, &id, &name, &version); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%d", tenant, id, name, version))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// --- The published v26.8.0 upgrade baseline ---------------------------------------------

// publicReleaseCensus is the measured append-only census of the FIRST PUBLIC RELEASE's
// exact product boot callback, with the provenance of how it was obtained.
type publicReleaseCensus struct {
	Source struct {
		SourceCommit string `json:"source_commit"`
		SourceTree   string `json:"source_tree"`
	} `json:"source"`
	CoreAppendOnly   []string `json:"core_append_only"`
	ModuleAppendOnly []string `json:"module_append_only"`
	ProductCensus    []string `json:"product_census"`
	Manifest         struct {
		Format     int64  `json:"format"`
		CodeEpoch  int64  `json:"code_epoch"`
		CodeSHA256 string `json:"code_sha256"`
	} `json:"manifest"`
}

func loadPublicReleaseCensus(t *testing.T) publicReleaseCensus {
	t.Helper()
	const artifactSHA = "a8dba2008563838e49fc8aff5117a667a194cf7031777c14305ca9d064a8c201"
	raw, err := os.ReadFile("testdata/access-evidence-upgrade/public-v26.8.0-product-census.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != artifactSHA {
		t.Fatalf("public census artifact SHA = %s, want %s", got, artifactSHA)
	}
	var census publicReleaseCensus
	if err := json.Unmarshal(raw, &census); err != nil {
		t.Fatal(err)
	}
	return census
}

// TestPublicV268ProductBaselineHasACompiledAccessEvidenceEdge is the additional mandatory
// fixture the root decision names: the real first public release, tag v26.8.0, commit
// f443e084 / tree cea97471, with its EXACT product callback — not a hub tree, not
// sessions-only, and not `migrate manifest` output, which omits two of the callback's
// three calls.
//
// WHAT IT MEASURES. The published source carries core migrations 1-7, guard format 1 and
// maximum epoch 4. Its own encoder, run inside an extraction of its own tree, computes the
// manifest tuple frozen below. This test rebuilds the SAME edition from THIS binary's
// compiled graph and proves three things: the two agree byte for byte, the base census B
// is identical, and the compiled 4 -> 7 access-evidence edge authorizes exactly that
// recorded tuple.
//
// WHAT IT DOES NOT CLAIM, and the distinction is the whole reason the provenance file
// records its commands. This is a reconstruction from the PUBLISHED SOURCE: no release
// archive was downloaded or executed, so it is not a statement about binary
// reproducibility. And a matching manifest tuple is an upgrade PRECONDITION, not an
// executed upgrade — the running fixture for that source is named as a remaining
// obligation in the implementation report.
func TestPublicV268ProductBaselineHasACompiledAccessEvidenceEdge(t *testing.T) {
	t.Parallel()
	census := loadPublicReleaseCensus(t)
	if census.Source.SourceCommit != "f443e0844dff6259679cc7b1e020058d467a0927" ||
		census.Source.SourceTree != "cea97471e1e3c16af533f508d8bf212de367c6ce" {
		t.Fatalf("the fixture names source %s / tree %s, not the published release",
			census.Source.SourceCommit, census.Source.SourceTree)
	}
	if census.Manifest.CodeEpoch != 4 {
		t.Fatalf("the published release is edition %d, want 4", census.Manifest.CodeEpoch)
	}

	// THIS binary's product census is the published one plus exactly the four
	// access-evidence relations. Building it here, rather than asserting it, is what
	// makes the comparison below a measurement of the delta.
	candidate := append(append([]string(nil), census.ProductCensus...), guardAccessEvidenceTables[:]...)
	graph, err := buildGuardEditionGraph(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Current != 7 {
		t.Fatalf("the candidate product census is edition %d, want 7", graph.Current)
	}
	recorded, ok := graph.node(census.Manifest.CodeEpoch)
	if !ok {
		t.Fatalf("edition %d is not in the candidate's compiled graph", census.Manifest.CodeEpoch)
	}
	if got := hexDigest(recorded.Manifest.CodeSHA256); got != census.Manifest.CodeSHA256 {
		t.Fatalf("THE PUBLISHED RELEASE HAS NO COMPILED EDGE: this binary rebuilds edition %d as %s and the published tree's own encoder computed %s. The base census differs, so the additional transition required is a census-activation edge naming the exact difference — not a widened access-evidence edge.",
			census.Manifest.CodeEpoch, got, census.Manifest.CodeSHA256)
	}
	if recorded.Manifest.Format != census.Manifest.Format {
		t.Fatalf("manifest format = %d, want the published %d", recorded.Manifest.Format, census.Manifest.Format)
	}

	// The edge that carries it, and the fact that it is the ACCESS-EVIDENCE one: the
	// published release already has the directory, communication and protocol deltas, so
	// the only thing this upgrade adds is DA.
	edge, err := graph.edge(census.Manifest.CodeEpoch, graph.Current)
	if err != nil {
		t.Fatalf("no compiled edge from the published edition to this one: %v", err)
	}
	delta, err := guardEditionEdgeDelta(edge.From.CodeEpoch, edge.To.CodeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if delta != guardDeltaAccessEvidence || len(edge.Additions) != len(guardAccessEvidenceTables) {
		t.Fatalf("the published upgrade edge adds %s (%d relations), want exactly the access-evidence delta",
			delta, len(edge.Additions))
	}
	if !edge.authorizes(census.Manifest.Format, census.Manifest.CodeEpoch, recorded.Manifest.CodeSHA256) {
		t.Fatal("the compiled edge does not authorize the published release's own recorded tuple")
	}
	// The base census is the same object on both sides, which is what `same-B` means.
	publicGraph, err := buildGuardEditionGraph(census.ProductCensus)
	if err != nil {
		t.Fatal(err)
	}
	if publicGraph.Current != 4 {
		t.Fatalf("the published census rebuilds as edition %d, want 4", publicGraph.Current)
	}
	if publicGraph.BaseSHA256 != graph.BaseSHA256 {
		t.Fatalf("base census differs: published %s, candidate %s",
			hexDigest(publicGraph.BaseSHA256), hexDigest(graph.BaseSHA256))
	}
	t.Logf("PUBLIC_V268_BASELINE|commit=%s|tree=%s|epoch=%d|code_sha256=%s|base_sha256=%s|edge=%d->%d|delta=%s",
		census.Source.SourceCommit, census.Source.SourceTree, census.Manifest.CodeEpoch,
		census.Manifest.CodeSHA256, hexDigest(graph.BaseSHA256),
		edge.From.CodeEpoch, edge.To.CodeEpoch, delta)
}

// TestAccessEvidenceV9FailureAfterDDLRollsBackWholly is section 10.2 case 2 at the v9
// boundary: an injected failure AFTER the four relations are created and verified, and
// after the completion seal is appended, but before the transaction commits.
//
// The three durable things v9 writes — the relations, the seal and its tracking row —
// commit together or not at all, so the retry starts from the same authorized checkpoint
// the first attempt did rather than from a state no execution can produce.
func TestAccessEvidenceV9FailureAfterDDLRollsBackWholly(t *testing.T) {
	ctx := context.Background()
	_, db, dia := directV7StoreFixture(t, store.EngineSQLite)
	seedCoreV5(t, db, dia, false)
	current, err := buildGuardManifest(coreOnlyRegistry(t).appendOnlyTables())
	if err != nil {
		t.Fatal(err)
	}
	graph, err := guardEditionGraphFor(current)
	if err != nil {
		t.Fatal(err)
	}
	through8 := buildCoreMigrations(dia, coreDescriptors(),
		guardBootstrapExec(dia, current), guardEditionTwoMigrationExec(dia, current))
	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, through8); err != nil {
		t.Fatalf("apply the direct path through v8: %v", err)
	}
	plan, err := classifyAccessEvidenceBoot(ctx, db, dia, graph, coreDescriptors(), coreOnlyRegistry(t), nil,
		guardEventFenceFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != accessEvidenceV9DirectMaterialize {
		t.Fatalf("planned v9 action = %s, want %s", plan.Action, accessEvidenceV9DirectMaterialize)
	}
	assertDirectV7Completed(t, db, dia)

	boom := errors.New("injected failure after the v9 relations and seal")
	real := coreAccessEvidenceMigration(dia, coreDescriptors(), graph, plan)
	inner := real.Exec
	failing := real
	failing.Exec = func(ctx context.Context, tx *sql.Tx) error {
		if err := inner(ctx, tx); err != nil {
			return err
		}
		// The whole transaction is observable here, so the assertion is that the work
		// really happened before the injected failure discards it.
		for _, table := range accessEvidenceRelationNames {
			var n int
			if err := tx.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("v9 did not create %s inside its transaction", table)
			}
		}
		return boom
	}
	if err := migrate.Apply(ctx, db, dia, coreTrackingTable,
		[]migrate.Migration{failing}); !errors.Is(err, boom) {
		t.Fatalf("failed v9 = %v, want the injected failure", err)
	}
	if got := accessEvidenceTablesPresent(t, db); got != 0 {
		t.Fatalf("the rolled-back v9 left %d access-evidence relations", got)
	}
	if got := countRows(t, db, dia.Rebind(
		"SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
		coreAccessEvidenceMigrationVersion); got != 0 {
		t.Fatalf("the rolled-back v9 left %d tracking rows", got)
	}
	// The checkpoint is exactly the one it started from: direct-v7-completed, five
	// bootstrap receipts, no v9 seal.
	assertDirectV7Completed(t, db, dia)

	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{real}); err != nil {
		t.Fatalf("retry v9 from the same checkpoint: %v", err)
	}
	assertDirectV9Completed(t, db, dia)
	if err := verifyAccessEvidenceRelationsExact(ctx, db, dia, coreDescriptors()); err != nil {
		t.Fatalf("retried v9 relations: %v", err)
	}
}

// --- The independent review's three causal probes, as permanent requirements -------------
//
// These reproduce, verbatim in scenario and assertion, the three cases the independent
// review of commit b2b1b54d used to prove the classifier's two defects. Each keeps its
// LEGITIMATE CONTROL — the same fixture without the defect-triggering change — because a
// negative that passes for the wrong reason is worth nothing, and because the controls are
// what show the correction did not simply make the path refuse everything.

// accessEvidenceDurableCensus is what "left all durable schema/tracker/data unchanged"
// means as a measurement: the highest recorded core version, and the complete sorted set of
// ordinary relations with their row counts.
type accessEvidenceDurableCensus struct {
	MaxCoreVersion int
	Relations      map[string]int
}

func accessEvidenceReadDurableCensus(t *testing.T, db *sql.DB) accessEvidenceDurableCensus {
	t.Helper()
	out := accessEvidenceDurableCensus{Relations: map[string]int{}}
	var tracker int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", coreTrackingTable).Scan(&tracker); err != nil {
		t.Fatal(err)
	}
	if tracker == 1 {
		var max sql.NullInt64
		if err := db.QueryRowContext(context.Background(),
			"SELECT MAX(version) FROM "+coreTrackingTable).Scan(&max); err != nil {
			t.Fatal(err)
		}
		if max.Valid {
			out.MaxCoreVersion = int(max.Int64)
		}
	}
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var n int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM main.\""+table+"\"").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out.Relations[table] = n
	}
	return out
}

func accessEvidenceAssertDurableCensusUnchanged(
	t *testing.T,
	what string,
	before, after accessEvidenceDurableCensus,
) {
	t.Helper()
	if before.MaxCoreVersion != after.MaxCoreVersion {
		t.Fatalf("%s: the core migration history advanced from v%d to v%d",
			what, before.MaxCoreVersion, after.MaxCoreVersion)
	}
	if len(before.Relations) != len(after.Relations) {
		t.Fatalf("%s: the relation count changed from %d to %d",
			what, len(before.Relations), len(after.Relations))
	}
	for table, count := range before.Relations {
		got, ok := after.Relations[table]
		if !ok {
			t.Fatalf("%s: relation %s disappeared", what, table)
		}
		if got != count {
			t.Fatalf("%s: relation %s changed from %d rows to %d", what, table, count, got)
		}
	}
}

// TestAccessEvidenceLegacyProfileMustNotSilentlyExpand is the review's F1, kept.
//
// The same allowlisted <=v5 seed is opened twice: once with the ratified core-only
// registrar, and once with one additional append-only module descriptor the source never
// had — no applied_module_tables row and no physical relation. The first must succeed; the
// second must refuse BEFORE anything is written, because installing that module from the
// direct bootstrap is a census activation, which R2 keeps as a separate authorized
// transition.
func TestAccessEvidenceLegacyProfileMustNotSilentlyExpand(t *testing.T) {
	for _, addModule := range []bool{false, true} {
		t.Run(fmt.Sprintf("additional-append-only-module-%t", addModule), func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := directV7StoreFixture(t, store.EngineSQLite)
			seedCoreV5(t, db, dia, false)
			var register func(store.ExtensionRegistry) error
			if addModule {
				register = func(reg store.ExtensionRegistry) error {
					desc := widgetDescriptor
					desc.Kind = "review.legacy_event"
					desc.Table = "review_legacy_event"
					desc.AppendOnly = true
					return reg.Register(desc)
				}
			}
			before := accessEvidenceReadDurableCensus(t, db)

			st, err := Open(ctx, cfg, register)
			if st != nil {
				_ = st.Close()
			}
			v9Rows := countRows(t, db, "SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version=9")
			moduleTables := countRows(t, db,
				"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='review_legacy_event'")
			t.Logf("REVIEW_LEGACY_PROFILE|additional_module=%t|open_error=%v|v9_rows=%d|module_tables=%d",
				addModule, err, v9Rows, moduleTables)

			if !addModule {
				if err != nil {
					t.Fatalf("control: the ratified core-only shape failed: %v", err)
				}
				if v9Rows != 1 {
					t.Fatalf("control: v9 tracking rows = %d, want 1", v9Rows)
				}
				return
			}
			if err == nil {
				t.Fatal("the legacy core-only source silently accepted a new append-only base census and committed v9")
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Fatalf("refusal = %v, want ErrGuardManifestNoEdge", err)
			}
			if !strings.Contains(err.Error(), "review_legacy_event") {
				t.Fatalf("the refusal does not name the relation it refused to activate: %v", err)
			}
			// NOTHING WAS WRITTEN. Not the module table, not v9, and not v6/v7/v8 either:
			// the refusal is read-only and precedes every migration this boot would run.
			if moduleTables != 0 {
				t.Fatal("the refused Open created the module table anyway")
			}
			accessEvidenceAssertDurableCensusUnchanged(t, "refused legacy expansion",
				before, accessEvidenceReadDurableCensus(t, db))
		})
	}
}

// TestAccessEvidenceFreshPreV2RequiresBothFamiliesAbsent is the review's F2 v1 subcase.
//
// `fresh-pre-v2`'s row requires the directory AND access-evidence families absent. A v1
// checkpoint with the four access-evidence relations precreated used to be admitted as
// that class and then met conflicting v2 DDL at statement 114.
func TestAccessEvidenceFreshPreV2RequiresBothFamiliesAbsent(t *testing.T) {
	for _, precreate := range []bool{false, true} {
		t.Run(fmt.Sprintf("precreated-full-DA-%t", precreate), func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			migrations := buildCoreMigrations(dia, coreDescriptors(), nil, nil)
			if err := migrate.Apply(ctx, db, dia, coreTrackingTable, migrations[:1]); err != nil {
				t.Fatal(err)
			}
			if precreate {
				ordered, oerr := exactAccessEvidenceDescriptors(coreDescriptors())
				if oerr != nil {
					t.Fatal(oerr)
				}
				for _, desc := range ordered {
					for _, stmt := range dia.CreateTableStmts(desc) {
						if _, err := db.ExecContext(ctx, stmt); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			before := accessEvidenceReadDurableCensus(t, db)
			graph, err := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
			if err != nil {
				t.Fatal(err)
			}
			plan, classErr := classifyAccessEvidenceBoot(
				ctx, db, dia, graph, coreDescriptors(), coreOnlyRegistry(t), nil, guardEventFenceFacts{})
			st, openErr := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("REVIEW_PREV2|precreated=%t|class=%s|classification_error=%v|open_error=%v|v9_rows=%d|DA=%d",
				precreate, plan.Class, classErr, openErr,
				countRows(t, db, "SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version=9"),
				accessEvidenceTablesPresent(t, db))

			if !precreate {
				if classErr != nil || openErr != nil {
					t.Fatalf("control: the exact current v1-only checkpoint failed: class=%v open=%v", classErr, openErr)
				}
				return
			}
			if classErr == nil {
				t.Fatal("fresh-pre-v2 accepted all four access-evidence relations precreated, though its row requires both families absent")
			}
			if !errors.Is(classErr, ErrGuardManifestNoEdge) {
				t.Fatalf("classification refusal = %v, want ErrGuardManifestNoEdge", classErr)
			}
			if openErr == nil {
				t.Fatal("Open accepted a v1 checkpoint with the access-evidence relations precreated")
			}
			accessEvidenceAssertDurableCensusUnchanged(t, "refused v1 precreation",
				before, accessEvidenceReadDurableCensus(t, db))
		})
	}
}

// TestAccessEvidenceFreshV2DamageRefusesBeforeAnyMigration is the review's F2 v2 subcase,
// and the one whose failure was measured in committed schema versions.
//
// Current migrations are applied to v2 and one append-only guard leg is removed. The
// classifier used to call that `fresh-current-pre-v6` without error; the boot then
// committed v3 through v8 and only v9 refused. R2 §5.1 promised a read-only rejection
// before ANY migration, so the assertion is on the tracker as much as on the error.
func TestAccessEvidenceFreshV2DamageRefusesBeforeAnyMigration(t *testing.T) {
	for _, damage := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-update-guard-%t", damage), func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			migrations := buildCoreMigrations(dia, coreDescriptors(), nil, nil)
			if err := migrate.Apply(ctx, db, dia, coreTrackingTable, migrations[:2]); err != nil {
				t.Fatal(err)
			}
			if damage {
				if _, err := db.ExecContext(ctx,
					"DROP TRIGGER main."+policyArtifactTable+"_no_update"); err != nil {
					t.Fatal(err)
				}
			}
			graph, err := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
			if err != nil {
				t.Fatal(err)
			}
			plan, classErr := classifyAccessEvidenceBoot(
				ctx, db, dia, graph, coreDescriptors(), coreOnlyRegistry(t), nil, guardEventFenceFacts{})
			st, openErr := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			maxCore := countRows(t, db, "SELECT MAX(version) FROM "+coreTrackingTable)
			guards := countRows(t, db,
				"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?",
				policyArtifactTable+"_no_update")
			t.Logf("REVIEW_FRESH_V2_DAMAGE|damaged=%t|class=%s|classification_error=%v|open_error=%v|max_core=%d|update_guards=%d",
				damage, plan.Class, classErr, openErr, maxCore, guards)

			if !damage {
				if classErr != nil || openErr != nil || maxCore != coreSupportedMigrationVersion {
					t.Fatalf("control: the clean current-v2 checkpoint failed: class=%v open=%v max=%d, want max=%d",
						classErr, openErr, maxCore, coreSupportedMigrationVersion)
				}
				return
			}
			if classErr == nil {
				t.Fatal("a current-v2 checkpoint with an append-only guard removed was classified without error")
			}
			if !errors.Is(classErr, ErrGuardManifestNoEdge) {
				t.Fatalf("classification refusal = %v, want ErrGuardManifestNoEdge", classErr)
			}
			if openErr == nil {
				t.Fatal("Open accepted a damaged current-v2 checkpoint")
			}
			// THE MEASUREMENT THAT MATTERS: the history did not advance. A refusal that
			// arrives after v3-v8 have committed is not the refusal R2 §5.1 describes.
			if maxCore != 2 {
				t.Fatalf("the refused boot advanced the core history from v2 to v%d", maxCore)
			}
			// And the damage was not repaired on the way out.
			if guards != 0 {
				t.Fatal("the refused Open recreated the guard it refused over")
			}
		})
	}
}
