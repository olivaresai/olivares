// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func guardBootVariantGraph(t *testing.T) guardEditionGraph {
	t.Helper()
	census := loadPublicReleaseCensus(t)
	tables := append([]string(nil), census.ProductCensus...)
	tables = append(tables, guardAccessEvidenceTables[:]...)
	tables = append(tables, "evals_comparison", "gitpublish_observation", "skills_revision")
	graph, err := buildGuardEditionGraph(tables)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Current != 28 {
		t.Fatalf("current epoch = %d, want 28", graph.Current)
	}
	return graph
}

func TestGuardBootVariantsEqualFreshWalk(t *testing.T) {
	graph := guardBootVariantGraph(t)
	boot := guardBootVariants{graph: graph, variantsByEpoch: make(map[int64][]guardBootstrapReceiptVariant)}
	for _, epoch := range graph.epochsDescending() {
		fresh, err := guardBootstrapVariantsForNode(graph, epoch)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			got, err := boot.variants(epoch)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, fresh) {
				t.Fatalf("epoch %d: boot variants differ from fresh walk", epoch)
			}
		}
	}
	if _, err := boot.variants(-1); !errors.Is(err, ErrGuardManifestNoEdge) {
		t.Fatalf("unknown epoch error = %v", err)
	}
}

func TestGuardBootVariantsAvoidRepeatedAncestorWork(t *testing.T) {
	graph := guardBootVariantGraph(t)
	fresh := testing.AllocsPerRun(1, func() {
		if _, err := guardBootstrapVariantsForNode(graph, graph.Current); err != nil {
			t.Fatal(err)
		}
		for _, parent := range graph.parentEpochs(graph.Current) {
			if _, err := guardBootstrapVariantsForNode(graph, parent); err != nil {
				t.Fatal(err)
			}
		}
	})
	boot := testing.AllocsPerRun(1, func() {
		b := guardBootVariants{graph: graph, variantsByEpoch: make(map[int64][]guardBootstrapReceiptVariant)}
		if _, err := b.variants(graph.Current); err != nil {
			t.Fatal(err)
		}
		for _, parent := range graph.parentEpochs(graph.Current) {
			if _, err := b.variants(parent); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Logf("compiled receipt allocations: fresh=%.0f boot=%.0f", fresh, boot)
	if boot >= fresh/2 {
		t.Fatalf("boot repeats ancestor work: %.0f allocations, want less than half of fresh %.0f", boot, fresh)
	}
}

// Compare the whole boot decision, including refusals, against the immutable verifier.
func TestGuardBootEditionHistoryEqualFreshVerification(t *testing.T) {
	graph := guardBootVariantGraph(t)
	current, _ := graph.currentNode()
	for _, tc := range []struct {
		name  string
		epoch int64
		path  guardEditionPath
		state guardEditionReceiptState
		pass  bool
	}{
		{"current", 28, "28", guardEditionReceiptsCurrentV9Completed, true},
		{"current incomplete", 28, "28", guardEditionReceiptsCurrentCompleted, true},
		{"direct", 28, "28", guardEditionReceiptsDirectV9Completed, true},
		{"predecessor", 16, "16", guardEditionReceiptsCurrentV9Completed, true},
		{"transitioned", 28, "16>28", guardEditionReceiptsSealed, true},
		{"distant predecessor refused", 7, "4>7", guardEditionReceiptsSealed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, _ := graph.node(tc.epoch)
			variants, err := guardBootstrapReceiptVariants(source.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			var selected *guardBootstrapReceiptVariant
			for i := range variants {
				v := &variants[i]
				if v.Path == tc.path && v.State == tc.state && (selected == nil || len(v.Receipts) < len(selected.Receipts)) {
					selected = v
				}
			}
			if selected == nil {
				t.Fatalf("missing fixture %s/%s", tc.state, tc.path)
			}
			db, dia := guardEmptyEditionSQLiteDB(t)
			seedGuardEditionVariant(t, db, dia, source.Manifest, tc.path, selected.Receipts)
			ctx := context.Background()
			fresh, freshErr := verifyGuardEditionHistory(ctx, db, dia, current.Manifest)
			if (freshErr == nil) != tc.pass {
				t.Fatalf("fresh fixture acceptance = %t, want %t: %.400s", freshErr == nil, tc.pass, freshErr)
			}
			got, gotErr := verifyGuardBootEditionHistory(ctx, db, dia, current.Manifest)
			if !reflect.DeepEqual(got, fresh) || (freshErr == nil) != (gotErr == nil) {
				t.Fatalf("boot = %+v / %.400s; fresh = %+v / %.400s", got, gotErr, fresh, freshErr)
			}
			if freshErr != nil && gotErr.Error() != freshErr.Error() {
				t.Fatal("boot refusal differs from fresh verifier")
			}
		})
	}
}

// Admission is the boot entry point. A Community history must reach the shared
// variant walk through it, with the same decision the migration walk gives.
func TestGuardAdmissionHistoryUsesBootWalk(t *testing.T) {
	graph := guardBootVariantGraph(t)
	current, _ := graph.currentNode()
	variants, err := guardBootstrapReceiptVariants(current.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var selected *guardBootstrapReceiptVariant
	for i := range variants {
		v := &variants[i]
		if v.Path == "28" && v.State == guardEditionReceiptsCurrentV9Completed &&
			(selected == nil || len(v.Receipts) < len(selected.Receipts)) {
			selected = v
		}
	}
	if selected == nil {
		t.Fatal("missing current epoch-28 fixture")
	}
	db, dia := guardEmptyEditionSQLiteDB(t)
	seedGuardEditionVariant(t, db, dia, current.Manifest, "28", selected.Receipts)
	ctx := context.Background()
	want, err := verifyGuardEditionHistory(ctx, db, dia, current.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := guardAdmissionVerifyHistory(ctx, db, dia, current.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("admission = %+v, migration walk = %+v", got, want)
	}
	fresh := testing.AllocsPerRun(1, func() {
		if _, err := verifyGuardEditionHistory(ctx, db, dia, current.Manifest); err != nil {
			t.Fatal(err)
		}
	})
	admission := testing.AllocsPerRun(1, func() {
		if _, err := guardAdmissionVerifyHistory(ctx, db, dia, current.Manifest); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("history verification allocations: migration walk=%.0f admission=%.0f", fresh, admission)
	if admission >= fresh/2 {
		t.Fatalf("admission repeats ancestor work: %.0f allocations, want less than half of %.0f", admission, fresh)
	}
}
