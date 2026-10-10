// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Module additions must preserve the manifests that existing installations recorded.
// Exercise the published census and each optional module independently: treating a new
// relation as part of the base makes the historical digest comparison fail.
func TestModuleEvidenceEditionsPreservePublishedAncestry(t *testing.T) {
	t.Parallel()
	published := loadPublicReleaseCensus(t)
	base := append([]string(nil), published.ProductCensus...)
	base = append(base, guardAccessEvidenceTables[:]...)
	historical, err := buildGuardEditionGraph(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		added []string
		epoch int64
	}{
		{"evaluation", []string{"evals_comparison"}, 10},
		{"publication", []string{"gitpublish_observation"}, 13},
		{"both", []string{"evals_comparison", "gitpublish_observation"}, 16},
		{"skills", []string{"skills_revision"}, 19},
		{"skills and evaluation", []string{"skills_revision", "evals_comparison"}, 22},
		{"skills and publication", []string{"skills_revision", "gitpublish_observation"}, 25},
		{"all modules", []string{"skills_revision", "evals_comparison", "gitpublish_observation"}, 28},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tables := append(append([]string(nil), base...), tc.added...)
			graph, err := buildGuardEditionGraph(tables)
			if err != nil {
				t.Fatal(err)
			}
			if graph.Current != tc.epoch || graph.BaseSHA256 != historical.BaseSHA256 {
				t.Fatalf("module addition changed historical identity: epoch %d, base %x", graph.Current, graph.BaseSHA256)
			}
			for _, epoch := range historical.epochsDescending() {
				before, _ := historical.node(epoch)
				after, ok := graph.node(epoch)
				if !ok || after.Manifest.CodeSHA256 != before.Manifest.CodeSHA256 {
					t.Fatalf("module addition changed recorded edition %d", epoch)
				}
			}
			old, _ := graph.node(published.Manifest.CodeEpoch)
			if hexDigest(old.Manifest.CodeSHA256) != published.Manifest.CodeSHA256 {
				t.Fatal("published release digest no longer has an exact compiled predecessor")
			}
			lineages, err := graph.lineagesEndingAt(graph.Current)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, lineage := range lineages {
				if lineage.start().Epoch == published.Manifest.CodeEpoch {
					found = true
				}
			}
			if !found {
				t.Fatal("no forward path from the published release")
			}
			if _, err := graph.edge(graph.Current, 7); err == nil {
				t.Fatal("an edge may not discard module evidence")
			}
			drifted, err := buildGuardEditionGraph(append(tables, "unknown_evidence_table"))
			if err != nil {
				t.Fatal(err)
			}
			for _, parent := range graph.parentEpochs(graph.Current) {
				edge, err := graph.edge(parent, graph.Current)
				if err != nil {
					t.Fatal(err)
				}
				other, _ := drifted.node(parent)
				if edge.authorizes(other.Manifest.Format, other.Manifest.CodeEpoch, other.Manifest.CodeSHA256) {
					t.Fatal("unknown evidence table was accepted as historical ancestry")
				}
			}
		})
	}
}

func TestModuleEvidenceEditionsKeepModulesIndependent(t *testing.T) {
	t.Parallel()
	for shape := 0; shape < 3; shape++ {
		base := []string{"old_alpha", guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable}
		base = append(base, guardAccessEvidenceTables[:]...)
		if shape >= 1 {
			base = append(base, guardEpoch3CommunicationTables[:]...)
		}
		if shape == 2 {
			base = append(base, guardEpoch4ProtocolTables[:]...)
		}
		for _, tc := range []struct {
			added []string
			epoch int64
		}{
			{[]string{"evals_comparison"}, int64(8 + shape)},
			{[]string{"gitpublish_observation"}, int64(11 + shape)},
			{[]string{"evals_comparison", "gitpublish_observation"}, int64(14 + shape)},
			{[]string{"skills_revision"}, int64(17 + shape)},
			{[]string{"skills_revision", "evals_comparison", "gitpublish_observation"}, int64(26 + shape)},
		} {
			graph, err := buildGuardEditionGraph(append(append([]string(nil), base...), tc.added...))
			if err != nil {
				t.Fatal(err)
			}
			if graph.Current != tc.epoch {
				t.Fatalf("shape %d: got %d, want %d", shape, graph.Current, tc.epoch)
			}
			if len(tc.added) == 2 {
				for _, parent := range []int64{int64(8 + shape), int64(11 + shape)} {
					edge, err := graph.edge(parent, graph.Current)
					if err != nil {
						t.Fatal(err)
					}
					if len(edge.Additions) != 1 {
						t.Fatalf("module edge adds %d relations", len(edge.Additions))
					}
				}
			}
		}
	}
	for _, table := range []string{"evals_comparison", "gitpublish_observation", "skills_revision"} {
		if _, err := buildGuardEditionGraph([]string{guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable, table}); err == nil {
			t.Fatalf("%s admitted without the core access-evidence edition", table)
		}
	}
}

// The post-module seam cannot create core migration history.
func TestModuleEvidenceTransitionScopeExcludesCoreDeltas(t *testing.T) {
	t.Parallel()
	for _, delta := range []guardEditionDelta{guardDeltaCommunication, guardDeltaProtocol, guardDeltaEvaluationComparison, guardDeltaGitPublication, guardDeltaSkills} {
		if !guardEditionModuleDelta(delta) {
			t.Fatalf("module delta %s cannot be activated after module schema creation", delta)
		}
	}
	for _, delta := range []guardEditionDelta{0, guardDeltaDirectory, guardDeltaAccessEvidence, guardDeltaEvaluationComparison | guardDeltaGitPublication} {
		if guardEditionModuleDelta(delta) {
			t.Fatalf("non-module delta %s accepted by the post-module seam", delta)
		}
	}
}

// A published upgrade reaches 4>7 in core v9, then must cross all three independent
// module edges. None of the final edition's immediate parents can read that history.
func TestModuleEvidenceRunnerCrossesAllPendingEdges(t *testing.T) {
	published := loadPublicReleaseCensus(t)
	tables := append([]string(nil), published.ProductCensus...)
	tables = append(tables, guardAccessEvidenceTables[:]...)
	tables = append(tables, "evals_comparison", "gitpublish_observation", "skills_revision")
	graph, err := buildGuardEditionGraph(tables)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := graph.node(graph.Current)
	for _, tc := range []struct {
		name  string
		epoch int64
		path  guardEditionPath
		state guardEditionReceiptState
		pass  bool
	}{
		{"published v9 transition", 7, "4>7", guardEditionReceiptsSealed, true},
		{"fresh v9 completion", 7, "7", guardEditionReceiptsCurrentV9Completed, true},
		{"v9 not completed", 7, "7", guardEditionReceiptsCurrentCompleted, false},
		{"core access-evidence edge not crossed", 4, "4", guardEditionReceiptsCurrentCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, _ := graph.node(tc.epoch)
			variants, err := guardBootstrapReceiptVariants(source.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			var receipts []guardReceipt
			for _, variant := range variants {
				if variant.State == tc.state && variant.Path == tc.path && len(variant.Receipts) <= 5 {
					receipts = variant.Receipts
					break
				}
			}
			if receipts == nil {
				t.Fatalf("missing fixture history %s/path-%s", tc.state, tc.path)
			}
			db, dia := guardEmptyEditionSQLiteDB(t)
			seedGuardEditionVariant(t, db, dia, source.Manifest, tc.path, receipts)
			ctx := context.Background()
			before := accessEvidenceReadDurableCensus(t, db)
			_, err = runAppendOnlyGuardUnits(ctx, db, dia, tables, "", false, nil)
			if !tc.pass {
				if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) && !errors.Is(err, ErrGuardManifestNoEdge) {
					t.Fatalf("incomplete core history was not refused: %.400s", err)
				}
				accessEvidenceAssertDurableCensusUnchanged(t, "refused module bridge", before,
					accessEvidenceReadDurableCensus(t, db))
				return
			}
			if err != nil {
				t.Fatalf("cross three pending module edges: %.400s", err)
			}
			history, err := verifyGuardEditionHistory(ctx, db, dia, target.Manifest)
			if err != nil {
				t.Fatalf("verify final history: %.400s", err)
			}
			if history.Kind != guardEditionHistoryTransitioned || !history.CompletedV9 ||
				!strings.HasPrefix(string(history.Path), string(tc.path)+">") ||
				strings.Count(string(history.Path), ">") != strings.Count(string(tc.path), ">")+3 {
				t.Fatalf("final history = %+v, want source path %s plus three module edges", history, tc.path)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != len(receipts)+3 {
				t.Fatalf("receipts = %d, want %d historical receipts plus three seals", got, len(receipts))
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != len(source.Manifest.Specs)+3 {
				t.Fatalf("inventory = %d, want %d historical activations plus three module activations", got, len(source.Manifest.Specs))
			}
			after := accessEvidenceReadDurableCensus(t, db)
			if _, err := runAppendOnlyGuardUnits(ctx, db, dia, tables, "", false, nil); err != nil {
				t.Fatalf("reopen completed module chain: %.400s", err)
			}
			accessEvidenceAssertDurableCensusUnchanged(t, "idempotent module bridge", after,
				accessEvidenceReadDurableCensus(t, db))
		})
	}
}
