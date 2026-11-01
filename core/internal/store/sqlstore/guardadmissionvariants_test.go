// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// guardAdmissionBusinessGraph is the epoch-28 Business census: the Community census plus
// the three Business relations, so the post-module graph also holds every compiled
// Community ancestor as a negative node.
func guardAdmissionBusinessGraph(t *testing.T) (guardManifest, guardEditionGraph) {
	t.Helper()
	census := loadPublicReleaseCensus(t)
	tables := append([]string(nil), census.ProductCensus...)
	tables = append(tables, guardAccessEvidenceTables[:]...)
	tables = append(tables, "evals_comparison", "gitpublish_observation", "skills_revision")
	tables = append(tables, commercialGuardFixtureTables...)
	manifest, err := buildGuardManifest(tables)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := guardAdmissionGraphFor(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, business := graph.nodes[-manifest.CodeEpoch]; !business || manifest.CodeEpoch != 28 {
		t.Fatalf("census is not a Business epoch-28 graph: epoch %d, community ancestors %t", manifest.CodeEpoch, business)
	}
	return manifest, graph
}

// guardAdmissionFreshVariants is the oracle: the post-module walk with no memo, one fresh
// derivation of every ancestor for every path that reaches it.
func guardAdmissionFreshVariants(graph guardEditionGraph, epoch int64) ([]guardBootstrapReceiptVariant, error) {
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
	for _, parent := range guardAdmissionParentEpochs(graph, epoch) {
		edge, err := guardAdmissionEdge(graph, parent, epoch)
		if err != nil {
			return nil, err
		}
		sources, err := guardAdmissionFreshVariants(graph, parent)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			if !guardEditionSealEligible(edge, source) {
				continue
			}
			seal, err := guardEditionTransitionSeal(edge, 0, empty, source.TerminalReceiptID)
			if err != nil {
				return nil, err
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
		}
	}
	return variants, nil
}

func guardAdmissionCurrentFixture(t *testing.T, manifest guardManifest, graph guardEditionGraph) guardBootstrapReceiptVariant {
	t.Helper()
	variants, err := guardAdmissionFreshVariants(graph, manifest.CodeEpoch)
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
	return *selected
}

// A pure function of the format must not be re-derived (three definition digests, three
// entry digests) by every seal that names it.
func TestGuardMetadataSpecsAreDerivedOnce(t *testing.T) {
	// Start cold so the tamper below reaches both the deriving call and a cached one.
	guardMetadataSpecsCompiled.Store(nil)
	want, err := deriveGuardMetadataSpecs(guardManifestFormat)
	if err != nil {
		t.Fatal(err)
	}
	got, err := guardMetadataSpecs(guardManifestFormat)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("guardMetadataSpecs differs from a fresh derivation")
	}
	// What one caller does to its copy must not reach the next caller.
	for range 2 {
		got[0].DesiredEnableState = "tampered"
		got[0].LegacyAllowedStates[0] = "tampered"
		got[1].Key.Relation = "tampered"
		again, err := guardMetadataSpecs(guardManifestFormat)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(again, want) {
			t.Fatal("a caller's edit of its metadata specs reached the next caller")
		}
		got = again
	}
	fresh := testing.AllocsPerRun(5, func() {
		if _, err := deriveGuardMetadataSpecs(guardManifestFormat); err != nil {
			t.Fatal(err)
		}
	})
	cached := testing.AllocsPerRun(5, func() {
		if _, err := guardMetadataSpecs(guardManifestFormat); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("metadata spec allocations: fresh=%.0f cached=%.0f", fresh, cached)
	if cached > fresh/3 {
		t.Fatalf("guardMetadataSpecs re-derives: %.0f allocations, want at most a third of fresh %.0f", cached, fresh)
	}
}

func TestGuardAdmissionVariantsEqualFreshWalk(t *testing.T) {
	_, graph := guardAdmissionBusinessGraph(t)
	boot := guardBootVariants{graph: graph, variantsByEpoch: make(map[int64][]guardBootstrapReceiptVariant), admission: true}
	for _, epoch := range graph.epochsDescending() {
		fresh, err := guardAdmissionFreshVariants(graph, epoch)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			got, err := boot.variants(epoch)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, fresh) {
				t.Fatalf("epoch %d: admission variants differ from the fresh walk", epoch)
			}
		}
	}
	if _, err := boot.variants(1 << 20); !errors.Is(err, ErrGuardManifestNoEdge) {
		t.Fatalf("unknown epoch error = %v", err)
	}
}

// The admission history check runs once per unit batch, once per rollout opening, once
// per fence close and once before serving. Each walks the same compiled variants.
func TestGuardAdmissionHistoryAvoidsRepeatedAncestorWork(t *testing.T) {
	manifest, graph := guardAdmissionBusinessGraph(t)
	fixture := guardAdmissionCurrentFixture(t, manifest, graph)
	db, dia := guardEmptyEditionSQLiteDB(t)
	seedGuardEditionVariant(t, db, dia, manifest, "28", fixture.Receipts)
	ctx := context.Background()
	want, err := guardAdmissionVerifyHistory(ctx, db, dia, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if want.Kind != guardEditionHistoryCurrentV9Completed || want.Path != "28" {
		t.Fatalf("admission history = %q path %q, want the current completed v9 history at 28", want.Kind, want.Path)
	}
	fresh := testing.AllocsPerRun(1, func() {
		if _, err := guardAdmissionFreshVariants(graph, graph.Current); err != nil {
			t.Fatal(err)
		}
		for _, parent := range guardAdmissionParentEpochs(graph, graph.Current) {
			if _, err := guardAdmissionFreshVariants(graph, parent); err != nil {
				t.Fatal(err)
			}
		}
	})
	admission := testing.AllocsPerRun(1, func() {
		if _, err := guardAdmissionVerifyHistory(ctx, db, dia, manifest); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("admission history allocations: fresh variant walk=%.0f verification=%.0f", fresh, admission)
	if admission >= fresh/2 {
		t.Fatalf("admission repeats ancestor work: %.0f allocations, want less than half of the fresh walk's %.0f", admission, fresh)
	}
}

// A refusal names every compiled variant the ledger failed to match, in walk order. The
// memo must not change which variants exist or the order they are named in, whichever way
// the ledger is wrong: a receipt missing at the start or in the middle of the bootstrap, or
// the history of another compiled edition.
func TestGuardAdmissionRefusalNamesEveryFreshVariant(t *testing.T) {
	manifest, graph := guardAdmissionBusinessGraph(t)
	fixture := guardAdmissionCurrentFixture(t, manifest, graph)
	wrongEdition := guardAdmissionOtherEditionReceipts(t, graph)
	for _, tc := range []struct {
		name     string
		receipts []guardReceipt
	}{
		{"missing its first receipt", fixture.Receipts[1:]},
		{"missing a receipt from the middle of its bootstrap",
			append(append([]guardReceipt(nil), fixture.Receipts[:3]...), fixture.Receipts[4:]...)},
		{"another compiled edition's history", wrongEdition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, dia := guardEmptyEditionSQLiteDB(t)
			seedGuardEditionVariant(t, db, dia, manifest, "28", tc.receipts)
			ctx := context.Background()
			actual, err := readGuardBootstrapReceiptHistory(ctx, db, dia)
			if err != nil {
				t.Fatal(err)
			}
			var differences []string
			current, err := guardAdmissionFreshVariants(graph, graph.Current)
			if err != nil {
				t.Fatal(err)
			}
			for _, variant := range current {
				diff := guardBootstrapReceiptSetDifference(actual, variant.Receipts)
				if diff == "" {
					t.Fatalf("%s/path-%s matches a ledger that must be refused", variant.State, variant.Path)
				}
				differences = append(differences, fmt.Sprintf("%s/path-%s: %s", variant.State, variant.Path, diff))
			}
			for _, parent := range guardAdmissionParentEpochs(graph, graph.Current) {
				edge, err := guardAdmissionEdge(graph, parent, graph.Current)
				if err != nil {
					t.Fatal(err)
				}
				sources, err := guardAdmissionFreshVariants(graph, parent)
				if err != nil {
					t.Fatal(err)
				}
				for _, source := range sources {
					if !guardEditionSealEligible(edge, source) {
						continue
					}
					diff := guardBootstrapReceiptSetDifference(actual, source.Receipts)
					if diff == "" {
						t.Fatalf("predecessor %d matches a ledger that must be refused", parent)
					}
					differences = append(differences, fmt.Sprintf("predecessor-%d/path-%s: %s", parent, source.Path, diff))
				}
			}
			want := fmt.Sprintf("%s: bootstrap history is not an exact compiled current, predecessor, or transitioned variant (%s)",
				ErrGuardBootstrapReceiptsInvalid, strings.Join(differences, "; "))
			// The second call reads the variants the first one cached.
			for call := 1; call <= 2; call++ {
				_, _, err = guardAdmissionClassifyReceipts(ctx, db, dia, manifest)
				if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
					t.Fatalf("call %d: error = %v, want %v", call, err, ErrGuardBootstrapReceiptsInvalid)
				}
				if err.Error() != want {
					t.Fatalf("call %d: refusal differs from the fresh walk's\n got: %.300s\nwant: %.300s", call, err.Error(), want)
				}
			}
		})
	}
}

// guardAdmissionOtherEditionReceipts is the shortest compiled history of an edition that
// is neither the current one nor a predecessor an admission may cross from.
func guardAdmissionOtherEditionReceipts(t *testing.T, graph guardEditionGraph) []guardReceipt {
	t.Helper()
	skip := map[int64]bool{graph.Current: true}
	for _, parent := range guardAdmissionParentEpochs(graph, graph.Current) {
		skip[parent] = true
	}
	for _, epoch := range graph.epochsDescending() {
		if skip[epoch] || epoch < 2 {
			continue
		}
		variants, err := guardAdmissionFreshVariants(graph, epoch)
		if err != nil {
			t.Fatal(err)
		}
		var shortest []guardReceipt
		for _, variant := range variants {
			if len(variant.Receipts) > 0 && (shortest == nil || len(variant.Receipts) < len(shortest)) {
				shortest = variant.Receipts
			}
		}
		if shortest != nil {
			return shortest
		}
	}
	t.Fatal("the graph holds no other edition with a history")
	return nil
}

// One manifest compiles once per process, however many opens and goroutines ask, and a
// different manifest never reads another's compiled editions.
func TestGuardAdmissionCompiledIsSharedPerManifest(t *testing.T) {
	manifest, graph := guardAdmissionBusinessGraph(t)
	// Its own cache, which keeps entries for the whole test: the process-wide one releases
	// them a minute after their last use, and a slow run must not turn that into a failure.
	cache := newGuardAdmissionTestCache(t, time.Hour)
	first, err := cache.get(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// A hit returns the entry without deriving the graph again, which hashes every compiled
	// edition. (The pointer alone cannot tell: a rebuild under the lock still finds the entry.)
	build := testing.AllocsPerRun(1, func() {
		if _, err := buildGuardAdmissionGraph(manifest); err != nil {
			t.Fatal(err)
		}
	})
	hit := testing.AllocsPerRun(5, func() {
		if _, err := cache.get(manifest); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("compiled editions allocations: build=%.0f hit=%.0f", build, hit)
	if hit > build/50 {
		t.Fatalf("a cache hit allocates %.0f, want at most a fiftieth of the %.0f of building the graph", hit, build)
	}
	fresh, err := guardAdmissionFreshVariants(graph, manifest.CodeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	// The shared entry, and a memo nobody has warmed: its goroutines race to fill it, so
	// only the mutex keeps the map safe (the race detector is what fails without it).
	cold := &guardBootVariants{graph: graph, variantsByEpoch: make(map[int64][]guardBootstrapReceiptVariant), admission: true}
	const callers = 8
	for _, memo := range []*guardBootVariants{first, cold} {
		var wg sync.WaitGroup
		compiled := make([]*guardBootVariants, callers)
		walked := make([][]guardBootstrapReceiptVariant, callers)
		errs := make([]error, callers)
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if compiled[i], errs[i] = cache.get(manifest); errs[i] != nil {
					return
				}
				walked[i], errs[i] = memo.variants(manifest.CodeEpoch)
			}()
		}
		wg.Wait()
		for i := range callers {
			if errs[i] != nil {
				t.Fatal(errs[i])
			}
			if compiled[i] != first {
				t.Fatalf("caller %d compiled the manifest again", i)
			}
			if !reflect.DeepEqual(walked[i], fresh) {
				t.Fatalf("caller %d walked variants that differ from the fresh walk", i)
			}
		}
	}
	// A caller that appends to a variant's receipts must not write into the shared array.
	for _, epoch := range graph.epochsDescending() {
		variants, err := first.variants(epoch)
		if err != nil {
			t.Fatal(err)
		}
		if cap(variants) != len(variants) {
			t.Fatalf("epoch %d: variants hold spare capacity a caller's append would share", epoch)
		}
		for _, variant := range variants {
			if cap(variant.Receipts) != len(variant.Receipts) {
				t.Fatalf("epoch %d path %s: receipts hold spare capacity a caller's append would share", epoch, variant.Path)
			}
		}
	}
	// The same epoch over a different census is another manifest with its own graph.
	census := loadPublicReleaseCensus(t)
	tables := append([]string(nil), census.ProductCensus...)
	tables = append(tables, guardAccessEvidenceTables[:]...)
	tables = append(tables, "evals_comparison", "gitpublish_observation", "skills_revision", "unrelated_new_table")
	tables = append(tables, commercialGuardFixtureTables...)
	sibling, err := buildGuardManifestAtEpoch(tables, manifest.CodeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	other, err := cache.get(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if other == first || other.graph.BaseSHA256 == first.graph.BaseSHA256 {
		t.Fatalf("a census with another base shares the compiled editions of the epoch-%d Business census", manifest.CodeEpoch)
	}
}

// An entry is the compiled editions of one manifest, field for field. manifestDigest hashes
// the stored SpecSHA256 of each spec and not the spec, so a manifest that edits a field the
// digests do not cover shares the key of the census it was copied from and must still not be
// handed that census's editions, and a caller that edits its own manifest afterwards must not
// reach the entry.
func TestGuardAdmissionCompiledIsTheManifestsOwn(t *testing.T) {
	manifest, _ := guardAdmissionBusinessGraph(t)
	cache := newGuardAdmissionTestCache(t, time.Hour)
	first, err := cache.get(manifest)
	if err != nil {
		t.Fatal(err)
	}
	edited := manifest
	edited.Specs = cloneGuardSpecs(manifest.Specs)
	edited.Specs[0].DesiredEnableState = "tampered"
	staleDigest := manifest
	staleDigest.CodeSHA256 = [32]byte{1}
	for name, variant := range map[string]guardManifest{
		"a spec edited under its stored digest": edited,
		"the same specs under a stale digest":   staleDigest,
	} {
		other, err := cache.get(variant)
		if err != nil {
			// A refusal would skip the digest-collision path this test exists to hold.
			t.Fatalf("%s: the manifest no longer compiles, so the test no longer reaches the cache: %v", name, err)
		}
		if other == first {
			t.Fatalf("%s: handed the compiled editions of the census it was copied from", name)
		}
		node, ok := other.graph.node(variant.CodeEpoch)
		if !ok || !reflect.DeepEqual(node.Manifest, variant) {
			t.Fatalf("%s: compiled editions are not those of the manifest asked about", name)
		}
	}
	if again, err := cache.get(manifest); err != nil || again != first {
		t.Fatalf("asking about another manifest displaced the entry: %v", err)
	}

	mine := manifest
	mine.Specs = cloneGuardSpecs(manifest.Specs)
	owned, err := newGuardAdmissionTestCache(t, time.Hour).get(mine)
	if err != nil {
		t.Fatal(err)
	}
	for i := range mine.Specs {
		mine.Specs[i].DesiredEnableState = "tampered"
		for j := range mine.Specs[i].LegacyAllowedStates {
			mine.Specs[i].LegacyAllowedStates[j] = "tampered"
		}
	}
	node, ok := owned.graph.node(manifest.CodeEpoch)
	if !ok || !reflect.DeepEqual(node.Manifest, manifest) {
		t.Fatal("a caller's later edit of its manifest reached the compiled editions")
	}
}

// Goroutines that ask before anything is compiled share one entry: whichever finishes
// second finds the first's and returns it. The race detector is what fails without the mutex.
func TestGuardAdmissionCompiledIsSharedByConcurrentFirstUses(t *testing.T) {
	manifest, _ := guardAdmissionBusinessGraph(t)
	cache := newGuardAdmissionTestCache(t, time.Hour)
	const callers = 8
	var wg sync.WaitGroup
	compiled := make([]*guardBootVariants, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			compiled[i], errs[i] = cache.get(manifest)
		}()
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if compiled[i] != compiled[0] {
			t.Fatalf("caller %d holds another compilation of the same manifest", i)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.byDigest) != 1 {
		t.Fatalf("%d entries for one manifest", len(cache.byDigest))
	}
}

// Only the compiled format is served from the cache: a format read from a database derives
// its own specs.
func TestGuardMetadataSpecsOfAnotherFormatAreNotServedFromTheCache(t *testing.T) {
	if _, err := guardMetadataSpecs(guardManifestFormat); err != nil {
		t.Fatal(err)
	}
	other := guardManifestFormat + 1
	fresh := testing.AllocsPerRun(5, func() {
		if _, err := deriveGuardMetadataSpecs(other); err != nil {
			t.Fatal(err)
		}
	})
	got := testing.AllocsPerRun(5, func() {
		if _, err := guardMetadataSpecs(other); err != nil {
			t.Fatal(err)
		}
	})
	if got < fresh/2 {
		t.Fatalf("format %d was served from the compiled format's cache: %.0f allocations, a derivation takes %.0f", other, got, fresh)
	}
}

// The release counts from the last use, not from the first: a process that keeps opening
// stores keeps the compiled editions for longer than one idle period. The fake clock makes
// the idle periods exact.
func TestGuardAdmissionCompiledStaysWhileOpensContinue(t *testing.T) {
	manifest, _ := guardAdmissionBusinessGraph(t)
	synctest.Test(t, func(t *testing.T) {
		const keep = time.Minute
		cache := newGuardAdmissionTestCache(t, keep)
		first, err := cache.get(manifest)
		if err != nil {
			t.Fatal(err)
		}
		// Each open comes two thirds of a period after the last one, so the fifth is well
		// past one period from the first.
		for open := 2; open <= 6; open++ {
			time.Sleep(2 * keep / 3)
			again, err := cache.get(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if again != first {
				t.Fatalf("open %d: the compiled editions were released %v after the first open although opens continued",
					open, time.Duration(open-1)*2*keep/3)
			}
		}
	})
}

// A process that stops opening stores gives the compiled editions back, and asking again
// derives the same ones. A manifest that cannot compile leaves no entry behind.
func TestGuardAdmissionCompiledIsReleasedWhenOpensStop(t *testing.T) {
	manifest, graph := guardAdmissionBusinessGraph(t)
	want, err := guardAdmissionFreshVariants(graph, manifest.CodeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		const keep = time.Minute
		cache := newGuardAdmissionTestCache(t, keep)
		held, err := cache.get(manifest)
		if err != nil {
			t.Fatal(err)
		}
		entries := func() int {
			cache.mu.Lock()
			defer cache.mu.Unlock()
			return len(cache.byDigest)
		}
		time.Sleep(keep - time.Second)
		synctest.Wait()
		if n := entries(); n != 1 {
			t.Fatalf("%d compiled manifests held a second before the idle period ended, want 1", n)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if n := entries(); n != 0 {
			t.Fatalf("%d compiled manifests still held after the idle period", n)
		}
		// A caller that still holds what was released keeps a whole, working entry.
		heldVariants, err := held.variants(manifest.CodeEpoch)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(heldVariants, want) {
			t.Fatal("variants of a released entry differ from the fresh walk")
		}
		again, err := cache.get(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if again == held {
			t.Fatal("a released manifest came back as the same entry")
		}
		got, err := again.variants(manifest.CodeEpoch)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("variants derived after a release differ from the fresh walk")
		}
		for range 2 {
			if _, err := cache.get(guardManifest{}); err == nil {
				t.Fatal("an empty manifest compiled")
			}
			if n := entries(); n != 1 {
				t.Fatalf("a manifest that failed to compile changed the entries to %d, want the one compiled manifest", n)
			}
		}
	})
}

// newGuardAdmissionTestCache is a cache of the test's own, whose idle timer ends with the test
// so no entry stays reachable from a timer after it.
func newGuardAdmissionTestCache(t *testing.T, keep time.Duration) *guardAdmissionCache {
	t.Helper()
	cache := &guardAdmissionCache{keep: keep}
	t.Cleanup(func() {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if cache.idle != nil {
			cache.idle.Stop()
		}
	})
	return cache
}
