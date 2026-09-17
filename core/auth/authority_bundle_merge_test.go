// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var mergeDeadline = model.NewTimestamp(time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))

// mergeID is a canonical lowercase UUIDv7 whose text order follows i.
func mergeID(i int) model.ID {
	return model.ID(fmt.Sprintf("01920000-0000-7000-8000-%012x", i))
}

func mergePlain(kind model.Kind, i int, version int64) store.AuthorizationFactRef {
	return store.AuthorizationFactRef{Kind: kind, ID: mergeID(i), Version: version}
}

func mergeLease(
	t testing.TB, kind model.Kind, i int, version int64, subject string, fence int64,
	deadline model.Timestamp,
) store.AuthorizationFactRef {
	t.Helper()
	ref, err := store.NewLeaseFenceAuthorizationFactRef(kind, mergeID(i), version, subject, fence, deadline)
	if err != nil {
		t.Fatalf("lease fixture: %v", err)
	}
	return ref
}

func mergeUser(i int, version int64) store.UserAuthorityFactRef {
	return store.UserAuthorityFactRef{UserID: mergeID(i), Version: version}
}

func mustMerge(t *testing.T, a, b store.AuthoritySnapshotBundle) store.AuthoritySnapshotBundle {
	t.Helper()
	out, err := auth.MergeAuthoritySnapshotBundles(a, b)
	if err != nil {
		t.Fatalf("merge refused: %v", err)
	}
	return out
}

// requireMergeRefused checks both argument orders: a refusal is never an artifact
// of which bundle came first, and it never carries a partial result.
func requireMergeRefused(t *testing.T, a, b store.AuthoritySnapshotBundle) {
	t.Helper()
	for _, pair := range [][2]store.AuthoritySnapshotBundle{{a, b}, {b, a}} {
		out, err := auth.MergeAuthoritySnapshotBundles(pair[0], pair[1])
		if !errors.Is(err, auth.ErrRouteUndecided) {
			t.Fatalf("merge returned %v, want ErrRouteUndecided", err)
		}
		if out.Facts != nil || out.UserAuthorities != nil {
			t.Fatalf("refused merge returned a non-empty bundle: %+v", out)
		}
	}
}

func requireBundle(
	t *testing.T, got store.AuthoritySnapshotBundle,
	facts []store.AuthorizationFactRef, users []store.UserAuthorityFactRef,
) {
	t.Helper()
	if !slices.Equal(got.Facts, facts) {
		t.Fatalf("facts = %+v\nwant    %+v", got.Facts, facts)
	}
	if !slices.Equal(got.UserAuthorities, users) {
		t.Fatalf("users = %+v\nwant    %+v", got.UserAuthorities, users)
	}
}

func TestMergeAuthoritySnapshotBundlesUnionsDisjointEvidence(t *testing.T) {
	leased := mergeLease(t, "test.alpha", 9, 1, "holder", 4, mergeDeadline)
	a := store.AuthoritySnapshotBundle{
		Facts:           []store.AuthorizationFactRef{mergePlain("test.zeta", 2, 3), leased},
		UserAuthorities: []store.UserAuthorityFactRef{mergeUser(5, 2)},
	}
	b := store.AuthoritySnapshotBundle{
		Facts:           []store.AuthorizationFactRef{mergePlain("test.alpha", 1, 7)},
		UserAuthorities: []store.UserAuthorityFactRef{mergeUser(3, 1)},
	}
	want := []store.AuthorizationFactRef{mergePlain("test.alpha", 1, 7), leased, mergePlain("test.zeta", 2, 3)}
	wantUsers := []store.UserAuthorityFactRef{mergeUser(3, 1), mergeUser(5, 2)}
	requireBundle(t, mustMerge(t, a, b), want, wantUsers)
	out := mustMerge(t, b, a)
	requireBundle(t, out, want, wantUsers)

	subject, fence, deadline, ok := out.Facts[1].LeaseFenceWitness()
	if !ok || subject != "holder" || fence != 4 || deadline.String() != mergeDeadline.String() {
		t.Fatalf("lease witness not preserved: %q %d %s %v", subject, fence, deadline, ok)
	}
	if _, _, _, ok := out.Facts[0].LeaseFenceWitness(); ok {
		t.Fatal("an unleased fact gained a lease witness")
	}

	// Token-only bundles stay without a User slice.
	requireBundle(t, mustMerge(t,
		store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{mergePlain("test.a", 1, 1)}},
		store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{mergePlain("test.a", 2, 1)}},
	), []store.AuthorizationFactRef{mergePlain("test.a", 1, 1), mergePlain("test.a", 2, 1)}, nil)
}

func TestMergeAuthoritySnapshotBundlesDeduplicatesExactEvidence(t *testing.T) {
	plain := mergePlain("test.fact", 1, 4)
	leased := mergeLease(t, "test.lease", 2, 6, "holder", 11, mergeDeadline)
	// The same leased fact rebuilt from an equal witness is the same evidence.
	sameLease := mergeLease(t, "test.lease", 2, 6, "holder", 11,
		model.NewTimestamp(mergeDeadline.Time().In(time.FixedZone("x", 3600))))
	a := store.AuthoritySnapshotBundle{
		Facts:           []store.AuthorizationFactRef{leased, plain, leased},
		UserAuthorities: []store.UserAuthorityFactRef{mergeUser(7, 3), mergeUser(7, 3)},
	}
	b := store.AuthoritySnapshotBundle{
		Facts:           []store.AuthorizationFactRef{plain, sameLease},
		UserAuthorities: []store.UserAuthorityFactRef{mergeUser(7, 3)},
	}
	want := []store.AuthorizationFactRef{plain, leased}
	wantUsers := []store.UserAuthorityFactRef{mergeUser(7, 3)}
	requireBundle(t, mustMerge(t, a, b), want, wantUsers)
	requireBundle(t, mustMerge(t, b, a), want, wantUsers)
	// A merged result is itself a valid input, and merging it again is a no-op.
	requireBundle(t, mustMerge(t, mustMerge(t, a, b), a), want, wantUsers)
}

func TestMergeAuthoritySnapshotBundlesKeepsSameIDInDifferentKinds(t *testing.T) {
	a := store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{mergePlain("test.zeta", 1, 2)}}
	b := store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{
		mergeLease(t, "test.alpha", 1, 2, "holder", 1, mergeDeadline),
	}}
	requireBundle(t, mustMerge(t, a, b), []store.AuthorizationFactRef{b.Facts[0], a.Facts[0]}, nil)
	requireBundle(t, mustMerge(t, b, a), []store.AuthorizationFactRef{b.Facts[0], a.Facts[0]}, nil)
}

func TestMergeAuthoritySnapshotBundlesRefusesContradictoryFacts(t *testing.T) {
	later := model.NewTimestamp(mergeDeadline.Time().Add(time.Nanosecond))
	filler := mergePlain("test.filler", 90, 1)
	cases := []struct {
		name       string
		base, diff store.AuthorizationFactRef
	}{
		{"unleased version", mergePlain("test.fact", 1, 3), mergePlain("test.fact", 1, 4)},
		{"leased version", mergeLease(t, "test.lease", 1, 3, "holder", 5, mergeDeadline),
			mergeLease(t, "test.lease", 1, 4, "holder", 5, mergeDeadline)},
		{"leased and unleased", mergeLease(t, "test.lease", 1, 3, "holder", 5, mergeDeadline),
			mergePlain("test.lease", 1, 3)},
		{"lease subject", mergeLease(t, "test.lease", 1, 3, "holder", 5, mergeDeadline),
			mergeLease(t, "test.lease", 1, 3, "holder2", 5, mergeDeadline)},
		{"lease fence", mergeLease(t, "test.lease", 1, 3, "holder", 5, mergeDeadline),
			mergeLease(t, "test.lease", 1, 3, "holder", 6, mergeDeadline)},
		{"lease deadline", mergeLease(t, "test.lease", 1, 3, "holder", 5, mergeDeadline),
			mergeLease(t, "test.lease", 1, 3, "holder", 5, later)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Across the two inputs.
			requireMergeRefused(t,
				store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{filler, tc.base}},
				store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{tc.diff}})
			// Inside one input.
			requireMergeRefused(t,
				store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{tc.base, filler, tc.diff}},
				store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{filler}})
		})
	}
}

func TestMergeAuthoritySnapshotBundlesRefusesConflictingUserVersions(t *testing.T) {
	facts := []store.AuthorizationFactRef{mergePlain("test.fact", 1, 1)}
	requireMergeRefused(t,
		store.AuthoritySnapshotBundle{Facts: facts, UserAuthorities: []store.UserAuthorityFactRef{mergeUser(1, 2), mergeUser(4, 1)}},
		store.AuthoritySnapshotBundle{Facts: facts, UserAuthorities: []store.UserAuthorityFactRef{mergeUser(4, 1), mergeUser(1, 3)}})
	requireMergeRefused(t,
		store.AuthoritySnapshotBundle{Facts: facts, UserAuthorities: []store.UserAuthorityFactRef{mergeUser(1, 2), mergeUser(1, 3)}},
		store.AuthoritySnapshotBundle{Facts: facts})
}

func TestMergeAuthoritySnapshotBundlesRefusesEmptyAndMalformedInput(t *testing.T) {
	good := store.AuthoritySnapshotBundle{
		Facts:           []store.AuthorizationFactRef{mergePlain("test.fact", 1, 1)},
		UserAuthorities: []store.UserAuthorityFactRef{mergeUser(1, 1)},
	}
	withFact := func(f store.AuthorizationFactRef) store.AuthoritySnapshotBundle {
		// The malformed element trails a valid one, so refusal is not a first-element artifact.
		return store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{mergePlain("test.fact", 2, 1), f}}
	}
	withUser := func(u store.UserAuthorityFactRef) store.AuthoritySnapshotBundle {
		return store.AuthoritySnapshotBundle{
			Facts:           []store.AuthorizationFactRef{mergePlain("test.fact", 2, 1)},
			UserAuthorities: []store.UserAuthorityFactRef{mergeUser(2, 1), u},
		}
	}
	upper := model.ID(strings.ToUpper(string(mergeID(0xabc))))
	braced := model.ID("{" + string(mergeID(3)) + "}")
	cases := map[string]store.AuthoritySnapshotBundle{
		"zero bundle":                {},
		"empty facts with users":     {Facts: []store.AuthorizationFactRef{}, UserAuthorities: good.UserAuthorities},
		"empty kind":                 withFact(store.AuthorizationFactRef{ID: mergeID(3), Version: 1}),
		"kind without namespace":     withFact(mergePlain("fact", 3, 1)),
		"uppercase kind":             withFact(mergePlain("Test.fact", 3, 1)),
		"empty fact id":              withFact(store.AuthorizationFactRef{Kind: "test.fact", Version: 1}),
		"nil uuid fact id":           withFact(store.AuthorizationFactRef{Kind: "test.fact", ID: "00000000-0000-0000-0000-000000000000", Version: 1}),
		"non-uuid fact id":           withFact(store.AuthorizationFactRef{Kind: "test.fact", ID: "not-a-uuid", Version: 1}),
		"non-canonical fact id":      withFact(store.AuthorizationFactRef{Kind: "test.fact", ID: upper, Version: 1}),
		"braced fact id":             withFact(store.AuthorizationFactRef{Kind: "test.fact", ID: braced, Version: 1}),
		"zero fact version":          withFact(mergePlain("test.fact", 3, 0)),
		"negative fact version":      withFact(mergePlain("test.fact", 3, -1)),
		"leased directory epoch":     withFact(mergeLease(t, model.DirectoryEpochKind, 3, 1, "holder", 1, mergeDeadline)),
		"empty user id":              withUser(store.UserAuthorityFactRef{Version: 1}),
		"nil uuid user id":           withUser(store.UserAuthorityFactRef{UserID: "00000000-0000-0000-0000-000000000000", Version: 1}),
		"non-canonical user id":      withUser(store.UserAuthorityFactRef{UserID: upper, Version: 1}),
		"braced user id":             withUser(store.UserAuthorityFactRef{UserID: braced, Version: 1}),
		"zero user version":          withUser(mergeUser(3, 0)),
		"negative user version":      withUser(mergeUser(3, -2)),
		"users without tenant facts": {UserAuthorities: good.UserAuthorities},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			requireMergeRefused(t, good, bad)
			requireMergeRefused(t, bad, bad)
		})
	}
	requireMergeRefused(t, store.AuthoritySnapshotBundle{}, store.AuthoritySnapshotBundle{})
}

func mergeFacts(t testing.TB, from, n int) []store.AuthorizationFactRef {
	out := make([]store.AuthorizationFactRef, 0, n)
	for i := from; i < from+n; i++ {
		if i%2 == 0 {
			out = append(out, mergeLease(t, "test.lease", i, int64(i+1), fmt.Sprintf("holder-%d", i), int64(i+1), mergeDeadline))
		} else {
			out = append(out, mergePlain("test.fact", i, int64(i+1)))
		}
	}
	return out
}

func mergeUsers(from, n int) []store.UserAuthorityFactRef {
	out := make([]store.UserAuthorityFactRef, 0, n)
	for i := from; i < from+n; i++ {
		out = append(out, mergeUser(i, int64(i+1)))
	}
	return out
}

// sortedMergeFacts is the canonical order of mergeFacts: every "test.fact" (odd
// i) precedes every "test.lease" (even i), each group ascending by i.
func sortedMergeFacts(facts []store.AuthorizationFactRef) []store.AuthorizationFactRef {
	var plain, leased []store.AuthorizationFactRef
	for _, f := range facts {
		if f.Kind == "test.fact" {
			plain = append(plain, f)
		} else {
			leased = append(leased, f)
		}
	}
	return append(plain, leased...)
}

func TestMergeAuthoritySnapshotBundlesInputBudgets(t *testing.T) {
	one := []store.AuthorizationFactRef{mergePlain("test.fact", 1, 2)}
	// 64 supplied facts and 64 supplied Users per input are admitted.
	full := store.AuthoritySnapshotBundle{Facts: mergeFacts(t, 0, 64), UserAuthorities: mergeUsers(0, 64)}
	out := mustMerge(t, full, store.AuthoritySnapshotBundle{Facts: one, UserAuthorities: mergeUsers(0, 1)})
	requireBundle(t, out, sortedMergeFacts(full.Facts), full.UserAuthorities)

	// 65 supplied facts refuse even when every one of them is the same evidence.
	dup65 := slices.Repeat(one, 65)
	requireMergeRefused(t, store.AuthoritySnapshotBundle{Facts: dup65}, store.AuthoritySnapshotBundle{Facts: one})
	// 65 supplied Users refuse even when all are the same reference.
	requireMergeRefused(t,
		store.AuthoritySnapshotBundle{Facts: one, UserAuthorities: slices.Repeat(mergeUsers(3, 1), 65)},
		store.AuthoritySnapshotBundle{Facts: one})
}

func TestMergeAuthoritySnapshotBundlesOutputBudgets(t *testing.T) {
	one := []store.AuthorizationFactRef{mergePlain("test.fact", 1, 2)}
	lo, hi := mergeFacts(t, 0, 32), mergeFacts(t, 32, 32)
	requireBundle(t,
		mustMerge(t,
			store.AuthoritySnapshotBundle{Facts: lo, UserAuthorities: mergeUsers(0, 32)},
			store.AuthoritySnapshotBundle{Facts: hi, UserAuthorities: mergeUsers(32, 32)}),
		sortedMergeFacts(append(slices.Clone(lo), hi...)), mergeUsers(0, 64))

	// One distinct fact past 64, from either direction.
	requireMergeRefused(t,
		store.AuthoritySnapshotBundle{Facts: mergeFacts(t, 0, 64)},
		store.AuthoritySnapshotBundle{Facts: mergeFacts(t, 64, 1)})
	requireMergeRefused(t,
		store.AuthoritySnapshotBundle{Facts: mergeFacts(t, 0, 33)},
		store.AuthoritySnapshotBundle{Facts: mergeFacts(t, 33, 32)})
	// One distinct User past 64.
	requireMergeRefused(t,
		store.AuthoritySnapshotBundle{Facts: one, UserAuthorities: mergeUsers(0, 64)},
		store.AuthoritySnapshotBundle{Facts: one, UserAuthorities: mergeUsers(64, 1)})
	requireMergeRefused(t,
		store.AuthoritySnapshotBundle{Facts: one, UserAuthorities: mergeUsers(0, 40)},
		store.AuthoritySnapshotBundle{Facts: one, UserAuthorities: mergeUsers(40, 25)})
}

func TestMergeAuthoritySnapshotBundlesCollapses64IdenticalFactsAcrossInputs(t *testing.T) {
	facts := mergeFacts(t, 0, 64)
	users := mergeUsers(0, 64)
	reversedFacts, reversedUsers := slices.Clone(facts), slices.Clone(users)
	slices.Reverse(reversedFacts)
	slices.Reverse(reversedUsers)
	out := mustMerge(t,
		store.AuthoritySnapshotBundle{Facts: facts, UserAuthorities: users},
		store.AuthoritySnapshotBundle{Facts: reversedFacts, UserAuthorities: reversedUsers})
	requireBundle(t, out, sortedMergeFacts(facts), users)
}

func TestMergeAuthoritySnapshotBundlesIsDeterministicAndCommutative(t *testing.T) {
	pool := mergeFacts(t, 0, 20)
	userPool := mergeUsers(0, 12)
	want, wantUsers := sortedMergeFacts(pool), userPool
	rng := rand.New(rand.NewPCG(20260912, 88))
	for iter := 0; iter < 64; iter++ {
		a := store.AuthoritySnapshotBundle{Facts: slices.Clone(pool[:14]), UserAuthorities: slices.Clone(userPool[:8])}
		b := store.AuthoritySnapshotBundle{Facts: slices.Clone(pool[7:]), UserAuthorities: slices.Clone(userPool[5:])}
		rng.Shuffle(len(a.Facts), func(i, j int) { a.Facts[i], a.Facts[j] = a.Facts[j], a.Facts[i] })
		rng.Shuffle(len(b.Facts), func(i, j int) { b.Facts[i], b.Facts[j] = b.Facts[j], b.Facts[i] })
		rng.Shuffle(len(a.UserAuthorities), func(i, j int) {
			a.UserAuthorities[i], a.UserAuthorities[j] = a.UserAuthorities[j], a.UserAuthorities[i]
		})
		rng.Shuffle(len(b.UserAuthorities), func(i, j int) {
			b.UserAuthorities[i], b.UserAuthorities[j] = b.UserAuthorities[j], b.UserAuthorities[i]
		})
		requireBundle(t, mustMerge(t, a, b), want, wantUsers)
		requireBundle(t, mustMerge(t, b, a), want, wantUsers)
	}
}

func TestMergeAuthoritySnapshotBundlesNeitherMutatesNorAliasesInputs(t *testing.T) {
	// Unsorted inputs with spare capacity: an in-place sort or an append into
	// either backing array would be visible below.
	aFacts := append(make([]store.AuthorizationFactRef, 0, 16), mergeFacts(t, 10, 3)...)
	slices.Reverse(aFacts)
	bFacts := append(make([]store.AuthorizationFactRef, 0, 16), mergeFacts(t, 11, 4)...)
	slices.Reverse(bFacts)
	aUsers := append(make([]store.UserAuthorityFactRef, 0, 16), mergeUser(9, 1), mergeUser(2, 1))
	bUsers := append(make([]store.UserAuthorityFactRef, 0, 16), mergeUser(5, 1), mergeUser(2, 1))
	a := store.AuthoritySnapshotBundle{Facts: aFacts, UserAuthorities: aUsers}
	b := store.AuthoritySnapshotBundle{Facts: bFacts, UserAuthorities: bUsers}
	aBefore := store.AuthoritySnapshotBundle{Facts: slices.Clone(aFacts), UserAuthorities: slices.Clone(aUsers)}
	bBefore := store.AuthoritySnapshotBundle{Facts: slices.Clone(bFacts), UserAuthorities: slices.Clone(bUsers)}

	out := mustMerge(t, a, b)
	second := mustMerge(t, a, b)
	requireBundle(t, a, aBefore.Facts, aBefore.UserAuthorities)
	requireBundle(t, b, bBefore.Facts, bBefore.UserAuthorities)
	if len(a.Facts[len(a.Facts):cap(a.Facts)]) > 0 && a.Facts[:cap(a.Facts)][len(a.Facts)] != (store.AuthorizationFactRef{}) {
		t.Fatal("merge wrote into the spare capacity of an input")
	}
	wantFacts := sortedMergeFacts(mergeFacts(t, 10, 5))
	wantUsers := []store.UserAuthorityFactRef{mergeUser(2, 1), mergeUser(5, 1), mergeUser(9, 1)}
	requireBundle(t, out, wantFacts, wantUsers)

	// Later caller mutation of either input, including its spare capacity, does
	// not reach the result.
	poison := mergePlain("test.poison", 99, 99)
	for _, facts := range [][]store.AuthorizationFactRef{a.Facts[:cap(a.Facts)], b.Facts[:cap(b.Facts)]} {
		for i := range facts {
			facts[i] = poison
		}
	}
	for _, users := range [][]store.UserAuthorityFactRef{a.UserAuthorities[:cap(a.UserAuthorities)], b.UserAuthorities[:cap(b.UserAuthorities)]} {
		for i := range users {
			users[i] = mergeUser(99, 99)
		}
	}
	requireBundle(t, out, wantFacts, wantUsers)

	// Mutating one result touches neither the other result nor the inputs.
	for i := range out.Facts {
		out.Facts[i] = mergePlain("test.edited", 1, 1)
	}
	for i := range out.UserAuthorities {
		out.UserAuthorities[i] = mergeUser(1, 1)
	}
	requireBundle(t, second, wantFacts, wantUsers)
	if a.Facts[0] != poison || b.Facts[0] != poison || a.UserAuthorities[0] != mergeUser(99, 99) {
		t.Fatal("editing a result reached a caller-owned input")
	}
}

// An oversized input is refused before its elements are read or copied: the
// work of refusing 65 references and 65536 references is the same.
func TestMergeAuthoritySnapshotBundlesRefusesOversizedInputWithoutProportionalWork(t *testing.T) {
	good := store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{mergePlain("test.fact", 1, 1)}}
	const huge = 1 << 16
	cases := map[string]func(n int) store.AuthoritySnapshotBundle{
		"facts": func(n int) store.AuthoritySnapshotBundle {
			return store.AuthoritySnapshotBundle{Facts: make([]store.AuthorizationFactRef, n)}
		},
		"users": func(n int) store.AuthoritySnapshotBundle {
			return store.AuthoritySnapshotBundle{
				Facts: good.Facts, UserAuthorities: make([]store.UserAuthorityFactRef, n),
			}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			small, large := build(65), build(huge)
			requireMergeRefused(t, small, good)
			requireMergeRefused(t, large, good)
			// Bytes, not allocation counts: the race runtime's count wobbles by one,
			// while a single proportional copy of the input is over a megabyte.
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			for range 8 {
				_, _ = auth.MergeAuthoritySnapshotBundles(large, good)
				_, _ = auth.MergeAuthoritySnapshotBundles(good, large)
			}
			runtime.ReadMemStats(&after)
			if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<10 {
				t.Fatalf("refusing %d oversized references allocated %d bytes over 16 calls", huge, grew)
			}
		})
	}
}
