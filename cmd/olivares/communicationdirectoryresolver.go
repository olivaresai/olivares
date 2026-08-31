// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// ⛔ THE EPOCH FENCE NEEDS THREE SEPARATE TRANSACTIONS, AND THE NATURAL FORM IS DEAD.
//
// The addendum (§3.2) requires: read epoch-before, then roster and tombstones,
// then epoch-after; return only when before == after >= 1; on a change retry
// with a bound and then answer 503. That guarantee only means something if the
// two epoch reads CAN differ.
//
// The port header (core/store/directory.go) says callers take the three steps
// "without opening a nested Store transaction", which reads naturally as "do it
// all inside one View". It cannot be that: Store.View is documented as "one
// consistent read-only transaction snapshot" and is implemented as
// sql.LevelRepeatableRead on Postgres and SQLite's default stable snapshot
// (core/internal/store/sqlstore/store.go, viewTxOptions). Inside a single View
// the two reads are GUARANTEED equal, so the retry and the 503 become
// unreachable code — a guard that compiles, reviews clean, and never fires.
//
// Adjudicated 2026-08-26: the addendum governs, "without nesting" means DO NOT
// NEST rather than "use one transaction", and this resolver therefore takes the
// three reads in three separate Views so a concurrent bump is observable
// between them. The acceptance criterion is the discriminating mutant: inject an
// epoch bump between the roster read and epoch-after; a correct resolver retries
// and then answers UNKNOWN, and the single-View form does NOT fire on it.
type communicationDirectoryResolver struct {
	view        directoryScopeRunner
	now         func() time.Time
	freshness   time.Duration
	maxAttempts int
}

// directoryScopeRunner is Store.View bound by the composition root AND already
// narrowed to the one capability this adapter needs. The adapter never sees a
// store.Scope, let alone a store.Store: the type assertion for the optional
// DirectorySnapshotReader capability happens once, at the binding site, so a
// scope that lacks it fails there instead of inside every read.
//
// It also keeps the fence honest under test: a double implements two methods,
// not the fifteen of store.Scope.
type directoryScopeRunner func(
	ctx context.Context, tenant model.TenantID, fn func(store.DirectorySnapshotReader) error,
) error

const (
	directoryResolverDefaultFreshness = 5 * time.Minute
	directoryResolverDefaultAttempts  = 3
)

func newCommunicationDirectoryResolver(
	view directoryScopeRunner,
	now func() time.Time,
) *communicationDirectoryResolver {
	return &communicationDirectoryResolver{
		view:        view,
		now:         now,
		freshness:   directoryResolverDefaultFreshness,
		maxAttempts: directoryResolverDefaultAttempts,
	}
}

// readDirectoryEpoch opens its OWN View. That is the point: a second call can
// observe a different committed state, which is what makes the fence able to
// fire at all.
func (r *communicationDirectoryResolver) readDirectoryEpoch(
	ctx context.Context, tenant model.TenantID,
) (int64, error) {
	var version int64
	err := r.view(ctx, tenant, func(reader store.DirectorySnapshotReader) error {
		epoch, err := reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		version = epoch.Version
		return nil
	})
	if err != nil {
		return 0, err
	}
	if version < 1 {
		return 0, fmt.Errorf("%w: directory epoch below one", store.ErrDirectoryUnavailable)
	}
	return version, nil
}

// fenced runs read between two epoch observations taken in their own
// transactions and accepts the result only when both agree. A disagreement is a
// concurrent directory mutation: the roster just read may already describe a
// membership that no longer holds, so it is discarded and retried rather than
// labelled with the later epoch — "never read the roster first and label it with
// a later epoch".
func (r *communicationDirectoryResolver) fenced(
	ctx context.Context,
	tenant model.TenantID,
	read func(ctx context.Context, epoch int64) error,
) (int64, error) {
	attempts := r.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		before, err := r.readDirectoryEpoch(ctx, tenant)
		if err != nil {
			return 0, err
		}
		if err := read(ctx, before); err != nil {
			return 0, err
		}
		after, err := r.readDirectoryEpoch(ctx, tenant)
		if err != nil {
			return 0, err
		}
		if before == after {
			return before, nil
		}
	}
	// Exhausted: the directory moved under every attempt. UNKNOWN, never a
	// snapshot and never a business denial.
	return 0, fmt.Errorf("%w: directory epoch changed under %d fenced attempts",
		store.ErrDirectoryUnavailable, attempts)
}

// ResolveRecipient answers whether one canonical recipient is currently
// eligible, under the same fence.
func (r *communicationDirectoryResolver) ResolveRecipient(
	ctx context.Context,
	scope sessions.DirectoryScopeRef,
	recipient sessions.RecipientRef,
) (sessions.RecipientSnapshot, error) {
	if err := scope.Validate(); err != nil {
		return sessions.RecipientSnapshot{}, err
	}
	if err := recipient.Validate(); err != nil {
		return sessions.RecipientSnapshot{}, err
	}

	var tombstone *store.DirectoryTombstoneWitness
	epoch, err := r.fenced(ctx, scope.TenantID, func(ctx context.Context, _ int64) error {
		return r.view(ctx, scope.TenantID, func(reader store.DirectorySnapshotReader) error {
			ref, refErr := directoryPrincipalRefFor(scope, recipient)
			if refErr != nil {
				return refErr
			}
			witness, found, readErr := reader.ReadDirectoryTombstone(ctx, ref)
			if readErr != nil {
				return readErr
			}
			if found {
				copied := witness
				tombstone = &copied
			} else {
				tombstone = nil
			}
			return nil
		})
	})
	if err != nil {
		return sessions.RecipientSnapshot{}, err
	}

	snapshot := sessions.RecipientSnapshot{
		Scope:          scope,
		Recipient:      recipient,
		RecipientEpoch: epoch,
		DirectoryEpoch: epoch,
		Eligible:       tombstone == nil,
		Tombstone:      tombstone,
	}
	return snapshot, nil
}

// directoryPrincipalRefFor maps a K3 recipient onto the core tombstone lookup
// key. A session recipient has no core tombstone: its liveness is the Claim's
// job, not the directory's.
func directoryPrincipalRefFor(
	scope sessions.DirectoryScopeRef, recipient sessions.RecipientRef,
) (store.DirectoryPrincipalRef, error) {
	switch recipient.Kind {
	case sessions.RecipientUser:
		return store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser,
			PrincipalRef:  model.ID(recipient.Ref),
		}, nil
	case sessions.RecipientAgent:
		return store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  model.ID(recipient.Ref),
			WorkspaceRef:  scope.WorkspaceID,
		}, nil
	default:
		return store.DirectoryPrincipalRef{}, fmt.Errorf(
			"%w: recipient kind %q has no core directory tombstone",
			store.ErrDirectoryUnavailable, recipient.Kind)
	}
}
