// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/model"
)

// ErrDirectoryUnavailable means the store could not establish complete,
// internally consistent directory evidence. It covers a missing/invalid epoch,
// an illegible or contradictory tombstone and a torn snapshot. Callers must map
// it to an unavailable/unknown verdict, never to epoch zero, ErrNotFound or a
// business denial.
var ErrDirectoryUnavailable = errors.New("directory evidence unavailable")

// ErrDirectoryPrincipalRetired refuses a new or moved Agent binding whose
// stable identity and effective workspace already carry an irreversible core
// tombstone. A definitive retirement is one-way; ordinary directory writers
// may not resurrect that recipient by creating another binding later.
var ErrDirectoryPrincipalRetired = errors.New("directory principal is definitively retired")

// ErrDirectoryPrincipalHasBindings refuses Identity retirement while any
// physically recoverable Agent still names it. Removing the Identity first
// would strand those bindings and make their own definitive retirement
// impossible to prove.
var ErrDirectoryPrincipalHasBindings = errors.New("directory principal still has recoverable bindings")

// ErrDirectoryRetirementResidualAuthority marks a definitive User retirement
// that could not prove all user-bearing grants and credentials were removed in
// the same transaction. The whole ceremony rolls back on this sentinel.
var ErrDirectoryRetirementResidualAuthority = errors.New("directory retirement left residual authority")

// ErrDirectoryRetirementNotEnforced refuses irreversible retirement while the
// durable writer control is only staged. Until enforced, a legacy writer is not
// fenced by the database guard and could recreate a retired principal.
var ErrDirectoryRetirementNotEnforced = errors.New("directory retirement requires enforced writer control")

// DirectoryControlMode is the durable mode of the engine-owned directory
// writer control. Staged installs the complete guard surface without rejecting
// an older writer; enforced requires the exact expected generation.
type DirectoryControlMode string

const (
	DirectoryControlStaged   DirectoryControlMode = "staged"
	DirectoryControlEnforced DirectoryControlMode = "enforced"
)

// DirectoryWriterPosture states what boundary protects the raw writer control.
// Only SplitOwner is an independent database-role boundary. The other two are
// deliberately named capabilities: a principal able to issue arbitrary SQL as
// that same role can also alter its control.
type DirectoryWriterPosture string

const (
	DirectoryWriterSplitOwner           DirectoryWriterPosture = "split_owner"
	DirectoryWriterSingleRoleCapability DirectoryWriterPosture = "single_role_capability"
	DirectoryWriterSQLiteCapability     DirectoryWriterPosture = "sqlite_capability"
)

// DirectoryStatus is the non-secret boot witness for K3's directory fence.
// Enabled remains false until the later composition and activation cuts earn
// readiness; complete epoch coverage alone never enables communication.
type DirectoryStatus struct {
	Enabled               bool
	EpochCoverageComplete bool
	ControlMode           DirectoryControlMode
	WriterPosture         DirectoryWriterPosture
	ExpectedGeneration    int64
}

// DirectoryStatuser is an optional Store capability. The supported result is
// false when a decorator wraps a Store that cannot provide the witness, so a
// forwarding decorator never fabricates readiness.
type DirectoryStatuser interface {
	DirectoryStatus(ctx context.Context) (DirectoryStatus, bool, error)
}

// DirectoryPrincipalRef is the canonical lookup key for one possible K3
// recipient. WorkspaceRef is zero when workspace does not participate in the
// recipient identity; its durable tombstone column uses the corresponding
// non-NULL nil-UUID sentinel.
type DirectoryPrincipalRef struct {
	PrincipalKind model.DirectoryPrincipalKind
	PrincipalRef  model.ID
	WorkspaceRef  model.ID
}

// Validate rejects ambiguous or non-canonical lookup keys before a reader can
// turn them into SQL predicates. A User is global and therefore never carries
// a workspace dimension; Identity and Agent recipients may.
func (r DirectoryPrincipalRef) Validate() error {
	if !r.PrincipalKind.Valid() {
		return fmt.Errorf("%w: unknown directory principal kind %q",
			model.ErrInvalidDirectoryEvidence, r.PrincipalKind)
	}
	if err := validateDirectoryPrincipalID(r.PrincipalRef, "principal ref"); err != nil {
		return err
	}
	if r.PrincipalKind == model.DirectoryPrincipalUser && r.WorkspaceRef != "" {
		return fmt.Errorf("%w: a user principal cannot carry a workspace ref",
			model.ErrInvalidDirectoryEvidence)
	}
	if r.WorkspaceRef != "" {
		if err := validateDirectoryPrincipalID(r.WorkspaceRef, "workspace ref"); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectoryPrincipalID(id model.ID, name string) error {
	raw := id.String()
	u, err := uuid.Parse(raw)
	if err != nil || u.String() != raw || u.Version() != uuid.Version(7) ||
		u.Variant() != uuid.RFC4122 {
		return fmt.Errorf("%w: %s is not a canonical UUIDv7",
			model.ErrInvalidDirectoryEvidence, name)
	}
	return nil
}

// DirectoryTombstoneWitness is the typed, payload-free evidence a resolver may
// carry into sessions. TombstoneKind, TombstoneID and TombstoneVersion identify
// the immutable core row; RetirementEpoch is the resulting epoch for the scope's
// tenant (selected from the global map for a User tombstone).
type DirectoryTombstoneWitness struct {
	TombstoneKind    model.Kind
	TombstoneID      model.ID
	TombstoneVersion int64
	Principal        DirectoryPrincipalRef
	RetirementEpoch  int64
}

// DirectorySnapshotReader is an OPTIONAL, read-only capability of a
// tenant-bound Scope. Its implementation reads through the surrounding
// transaction: callers take epoch-before, read roster/tombstones, then take
// epoch-after without opening a nested Store transaction.
//
// ⛔ "WITHOUT NESTING" IS NOT "INSIDE ONE View", AND READING IT THAT WAY KILLS
// THE FENCE. Store.View is one consistent snapshot (RepeatableRead on Postgres,
// SQLite's stable transaction snapshot — see viewTxOptions), so two epoch reads
// inside a single View are GUARANTEED equal and the caller's retry/503 branches
// become unreachable code. The K3 addendum §3.2 requires the fence to be able to
// fire, so a caller takes epoch-before, the roster and epoch-after in SEPARATE
// transactions; only then is a concurrent bump observable between them. See
// cmd/olivares/communicationdirectoryresolver.go. Adjudicated 2026-08-26 after
// the dead form was caught by reading this header against View's contract,
// BEFORE it was written.
//
// ReadDirectoryEpoch returns ErrDirectoryUnavailable for an absent, duplicate,
// non-canonical or below-one epoch. ReadDirectoryTombstone may return found=false
// only after it has established a valid current epoch and a legible authoritative
// lookup. A malformed anchor, an epoch later than the current one, or incomplete
// global User evidence returns ErrDirectoryUnavailable instead.
//
// This interface exposes neither repositories nor retirement methods: only core
// can create tombstones, through the separate engine-owned retirement path.
type DirectorySnapshotReader interface {
	ReadDirectoryEpoch(context.Context) (model.DirectoryEpoch, error)
	ReadDirectoryTombstone(
		context.Context, DirectoryPrincipalRef,
	) (witness DirectoryTombstoneWitness, found bool, err error)
}
