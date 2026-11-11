// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"testing"
)

// The reference walk below recomputes every ancestor and is kept only as the oracle the
// memoized guardBootVariants walk is compared against.
//
// guardBootstrapReceiptVariants enumerates complete receipt histories, never individual
// receipt options. A later edge can therefore link its seal to the real terminal claim of
// a fresh completion, direct completion, v9 completion or prior transition without
// synthesizing an old bootstrap receipt that was not terminal on that path.
//
// The enumeration follows LINEAGES: one seal per crossed edge, in the order the edges
// were crossed. Two lineages between the same endpoints therefore produce different
// receipt sets, because each seal binds its own exact From/To pair and its predecessor's
// terminal receipt id.
func guardBootstrapReceiptVariants(current guardManifest) ([]guardBootstrapReceiptVariant, error) {
	graph, err := guardEditionGraphFor(current)
	if err != nil {
		return nil, err
	}
	return guardBootstrapVariantsForNode(graph, current.CodeEpoch)
}

func guardBootstrapVariantsForNode(graph guardEditionGraph, epoch int64) ([]guardBootstrapReceiptVariant, error) {
	node, ok := graph.node(epoch)
	if !ok {
		return nil, fmt.Errorf("%w: edition %d is not in this graph", ErrGuardManifestNoEdge, epoch)
	}
	variants, err := guardBootstrapVariantsAtNode(node)
	if err != nil {
		return nil, err
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		return nil, err
	}
	for _, parent := range graph.parentEpochs(epoch) {
		edge, eerr := graph.edge(parent, epoch)
		if eerr != nil {
			return nil, eerr
		}
		sources, serr := guardBootstrapVariantsForNode(graph, parent)
		if serr != nil {
			return nil, serr
		}
		parentNode, _ := graph.node(parent)
		for _, source := range sources {
			if !guardEditionSealEligible(edge, source) {
				continue
			}
			seal, sealErr := guardEditionTransitionSeal(edge, 0, empty, source.TerminalReceiptID)
			if sealErr != nil {
				return nil, sealErr
			}
			variants = append(variants, guardBootstrapReceiptVariant{
				State:             guardEditionReceiptsSealed,
				Receipts:          append(append([]guardReceipt(nil), source.Receipts...), seal),
				Path:              source.Path + guardEditionPath(">") + guardEditionPath(strconv.FormatInt(epoch, 10)),
				CompletedV7:       true,
				CompletedV9:       source.CompletedV9 || node.Membership.has(guardDeltaAccessEvidence),
				TerminalReceiptID: seal.ReceiptID,
				Epoch:             epoch,
			})
			_ = parentNode
		}
	}
	return variants, nil
}

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

// The edition-history verifier accepts every compiled history and refuses a distant predecessor.
func TestGuardEditionHistoryAcceptsCompiledHistories(t *testing.T) {
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
			if _, err := verifyGuardEditionHistory(ctx, db, dia, current.Manifest); (err == nil) != tc.pass {
				t.Fatalf("fixture acceptance = %t, want %t: %.400s", err == nil, tc.pass, err)
			}
		})
	}
}

// Admission is the boot entry point. A Community history must reach the edition-history
// verifier through it, with the same decision a migration gets.
func TestGuardAdmissionHistoryMatchesTheMigrationVerifier(t *testing.T) {
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
		t.Fatalf("admission = %+v, migration verifier = %+v", got, want)
	}
}

// allocatedBytes is what f allocates on the heap, measured once after a GC.
func allocatedBytes(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A fresh migration verifies the edition history four times (the v7 completion and
// manifest, the v9 seal and its fresh verification). Each verification needs the compiled
// receipt variants of the current edition and of its parents once; walking every
// ancestor again for each parent made one verification cost several compiled walks.
func TestGuardMigrationHistoryVerificationWalksTheLineageOnce(t *testing.T) {
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
	once := allocatedBytes(func() {
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
	verify := allocatedBytes(func() {
		if _, err := verifyGuardEditionHistory(ctx, db, dia, current.Manifest); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("one migration history verification allocated %d bytes; one compiled lineage walk %d bytes", verify, once)
	if verify > 2*once {
		t.Fatalf("one history verification allocated %d bytes, more than twice one compiled lineage walk (%d): it repeats ancestor work", verify, once)
	}
}
