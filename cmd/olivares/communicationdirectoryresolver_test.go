// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// fakeDirectory serves a scripted epoch per READ, which is what lets a test
// distinguish "three separate transactions" from "one snapshot": under a single
// View every read in one resolution would observe the same value, so a script
// that changes between reads can only be seen by an implementation that opens
// more than one transaction.
type fakeDirectory struct {
	epochs    []int64 // one entry per ReadDirectoryEpoch call, last value repeats
	reads     int
	tombstone *store.DirectoryTombstoneWitness
	tombErr   error
}

func (f *fakeDirectory) ReadDirectoryEpoch(context.Context) (model.DirectoryEpoch, error) {
	v := f.epochs[len(f.epochs)-1]
	if f.reads < len(f.epochs) {
		v = f.epochs[f.reads]
	}
	f.reads++
	return model.DirectoryEpoch{BaseFields: model.BaseFields{Version: v}}, nil
}

func (f *fakeDirectory) ReadDirectoryTombstone(
	context.Context, store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	if f.tombErr != nil {
		return store.DirectoryTombstoneWitness{}, false, f.tombErr
	}
	if f.tombstone == nil {
		return store.DirectoryTombstoneWitness{}, false, nil
	}
	return *f.tombstone, true, nil
}

func directoryResolverForTest(f *fakeDirectory) *communicationDirectoryResolver {
	return newCommunicationDirectoryResolver(
		func(ctx context.Context, _ model.TenantID, fn func(store.DirectorySnapshotReader) error) error {
			return fn(f)
		},
		func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	)
}

// ⛔ THIS IS THE ACCEPTANCE CRITERION PLAN RATIFIED ON 2026-08-26, and it is the
// defect that would have shipped, not an imagined property: written the natural
// way — all three reads inside ONE store.View — the two epoch observations are
// GUARANTEED equal (RepeatableRead on Postgres, SQLite's stable snapshot), so
// the retry and the UNKNOWN below become unreachable code and the fence protects
// nothing while looking correct.
//
// Direction 2 is what discriminates: it only fails when the implementation can
// observe a bump BETWEEN reads, which requires separate transactions.
func TestDirectoryResolverEpochFenceCanActuallyFire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := sessions.DirectoryScopeRef{
		TenantID:    model.TenantID("11111111-1111-7111-8111-111111111111"),
		WorkspaceID: model.ID("44444444-4444-7444-8444-444444444444"),
	}
	recipient := sessions.RecipientRef{
		Kind: sessions.RecipientUser, Ref: "22222222-2222-7222-8222-222222222222",
	}

	// Direction 1 — the directory is still: before == after on the first attempt.
	t.Run("stable directory resolves", func(t *testing.T) {
		t.Parallel()
		f := &fakeDirectory{epochs: []int64{7}}
		got, err := directoryResolverForTest(f).ResolveRecipient(ctx, scope, recipient)
		if err != nil {
			t.Fatalf("stable directory must resolve: %v", err)
		}
		if got.DirectoryEpoch != 7 || !got.Eligible || got.Tombstone != nil {
			t.Fatalf("snapshot = %+v, want epoch 7, eligible, no tombstone", got)
		}
		if f.reads < 2 {
			t.Fatalf("epoch reads = %d; the fence needs TWO observations, so one read "+
				"means the fence is not being taken at all", f.reads)
		}
	})

	// Direction 2 — the directory moves under EVERY attempt: never a snapshot.
	// Under a single-View implementation this cannot happen and the test dies.
	t.Run("directory moving under every attempt is UNKNOWN never a snapshot", func(t *testing.T) {
		t.Parallel()
		// 3 attempts x (before, after) with a bump inside each pair.
		f := &fakeDirectory{epochs: []int64{1, 2, 3, 4, 5, 6}}
		got, err := directoryResolverForTest(f).ResolveRecipient(ctx, scope, recipient)
		if err == nil {
			t.Fatalf("a directory that moves under every attempt must NOT return a "+
				"snapshot; got %+v", got)
		}
		if !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("err = %v, want ErrDirectoryUnavailable (UNKNOWN, never a business denial)", err)
		}
		if got.DirectoryEpoch != 0 {
			t.Fatalf("a refused resolution must carry no epoch, got %+v", got)
		}
	})

	// Direction 3 — it moves once and then settles: the RETRY is what saves it.
	// Without a retry this would be an UNKNOWN, so this branch proves the bound
	// is a retry and not merely a failure.
	t.Run("one bump then quiet resolves on retry", func(t *testing.T) {
		t.Parallel()
		f := &fakeDirectory{epochs: []int64{1, 2, 9, 9}}
		got, err := directoryResolverForTest(f).ResolveRecipient(ctx, scope, recipient)
		if err != nil {
			t.Fatalf("a settled directory must resolve on retry: %v", err)
		}
		if got.DirectoryEpoch != 9 {
			t.Fatalf("epoch = %d, want 9 (the value BOTH observations agreed on)", got.DirectoryEpoch)
		}
	})

	// Negative control on the harness itself: if the script did not actually
	// change between reads, direction 2 would pass for the wrong reason.
	t.Run("control: the script really does change between reads", func(t *testing.T) {
		t.Parallel()
		f := &fakeDirectory{epochs: []int64{1, 2}}
		a, _ := f.ReadDirectoryEpoch(ctx)
		b, _ := f.ReadDirectoryEpoch(ctx)
		if a.Version == b.Version {
			t.Fatalf("the fake serves a constant epoch (%d); direction 2 would be vacuous", a.Version)
		}
	})
}

// A retired recipient is not eligible and carries its typed tombstone. Absence
// is NOT retirement, which is why direction 1 above asserts Tombstone == nil
// rather than merely Eligible.
func TestDirectoryResolverRetiredRecipientIsNotEligible(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := sessions.DirectoryScopeRef{
		TenantID:    model.TenantID("11111111-1111-7111-8111-111111111111"),
		WorkspaceID: model.ID("44444444-4444-7444-8444-444444444444"),
	}
	recipient := sessions.RecipientRef{
		Kind: sessions.RecipientUser, Ref: "22222222-2222-7222-8222-222222222222",
	}
	f := &fakeDirectory{
		epochs: []int64{4},
		tombstone: &store.DirectoryTombstoneWitness{
			TombstoneKind:    model.UserTombstoneKind,
			TombstoneID:      model.ID("33333333-3333-7333-8333-333333333333"),
			TombstoneVersion: 1,
			Principal: store.DirectoryPrincipalRef{
				PrincipalKind: model.DirectoryPrincipalUser,
				PrincipalRef:  model.ID(recipient.Ref),
			},
			RetirementEpoch: 2,
		},
	}
	got, err := directoryResolverForTest(f).ResolveRecipient(ctx, scope, recipient)
	if err != nil {
		t.Fatalf("a retired recipient still resolves (with evidence): %v", err)
	}
	if got.Eligible {
		t.Fatalf("a recipient with a tombstone must NOT be eligible: %+v", got)
	}
	if got.Tombstone == nil || got.Tombstone.RetirementEpoch != 2 {
		t.Fatalf("the typed tombstone must travel with the snapshot: %+v", got.Tombstone)
	}
}
