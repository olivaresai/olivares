// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "testing"

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
	for _, table := range []string{"evals_comparison", "gitpublish_observation"} {
		if _, err := buildGuardEditionGraph([]string{guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable, table}); err == nil {
			t.Fatalf("%s admitted without the core access-evidence edition", table)
		}
	}
}

// The post-module seam cannot create core migration history.
func TestModuleEvidenceTransitionScopeExcludesCoreDeltas(t *testing.T) {
	t.Parallel()
	for _, delta := range []guardEditionDelta{guardDeltaCommunication, guardDeltaProtocol, guardDeltaEvaluationComparison, guardDeltaGitPublication} {
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
