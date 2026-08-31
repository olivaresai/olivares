// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// ErrDirectoryWriterActivationIndeterminate means the activation transaction
// reached a commit boundary but a fresh, locked read could not establish which
// durable state survived. It must never be reported as either activated or
// rolled back; the operator has to reopen and verify the database.
var ErrDirectoryWriterActivationIndeterminate = sqlstore.ErrDirectoryWriterActivationIndeterminate

// ErrDirectoryWriterActivationAssertion means a caller omitted one of the two
// external rollout facts the database cannot inspect: every writer carries the
// new wrapper, and old writer activity has been drained.
var ErrDirectoryWriterActivationAssertion = errors.New("directory writer activation assertion missing")

// DirectoryWriterActivationRequest is the explicit, one-way Slice-C cutover.
// The two assertions are deliberately independent: an upgraded process may
// still have an old transaction in flight, and the database cannot enumerate
// either fact on the caller's behalf.
type DirectoryWriterActivationRequest struct {
	ExpectedGeneration int64
	WritersUpgraded    bool
	WritersDrained     bool
	Actor              string
	Reason             string
}

// DirectoryWriterActivationResult reports the durable transition observed by
// the ceremony. ReopenRequired is true on every success and every indeterminate
// commit because DirectoryStatus is an immutable boot witness; activation never
// hot-enables a running process, and an uncertain boundary must be reopened and
// verified before an operator can classify its durable outcome.
type DirectoryWriterActivationResult struct {
	Before         store.DirectoryStatus
	After          store.DirectoryStatus
	Changed        bool
	ReopenRequired bool
}

// ActivateDirectoryWriter moves core.directory.writer from staged generation N
// to enforced generation N+1 through the raw engine-owned seam. raw must be the
// undecorated Store returned by Open; residency/suspension wrappers intentionally
// do not forward this authority. cfg must be the same already-resolved Config
// used for that Open so the transient owner authority can be re-established
// without retaining an owner credential in the Store.
func ActivateDirectoryWriter(
	ctx context.Context,
	raw store.Store,
	cfg store.Config,
	req DirectoryWriterActivationRequest,
) (DirectoryWriterActivationResult, error) {
	if !req.WritersUpgraded || !req.WritersDrained {
		return DirectoryWriterActivationResult{}, fmt.Errorf(
			"%w: writers_upgraded=%t writers_drained=%t",
			ErrDirectoryWriterActivationAssertion,
			req.WritersUpgraded,
			req.WritersDrained,
		)
	}
	if req.ExpectedGeneration <= 0 || req.ExpectedGeneration == math.MaxInt64 {
		return DirectoryWriterActivationResult{}, fmt.Errorf(
			"%w: expected generation must be between 1 and %d",
			ErrDirectoryWriterActivationAssertion,
			int64(math.MaxInt64-1),
		)
	}
	if strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.Reason) == "" {
		return DirectoryWriterActivationResult{}, fmt.Errorf(
			"%w: actor and reason are required", ErrDirectoryWriterActivationAssertion,
		)
	}

	before, after, changed, err := sqlstore.ActivateDirectoryWriter(
		ctx, raw, cfg, req.ExpectedGeneration,
	)
	result := DirectoryWriterActivationResult{
		Before: before, After: after, Changed: changed,
		ReopenRequired: directoryWriterActivationRequiresReopen(err),
	}
	if err == nil {
		slog.Info("directory writer activation ceremony completed",
			"actor", strings.TrimSpace(req.Actor),
			"reason", strings.TrimSpace(req.Reason),
			"changed", changed,
			"mode", after.ControlMode,
			"expected_generation", after.ExpectedGeneration,
			"reopen_required", true,
		)
	}
	return result, err
}

func directoryWriterActivationRequiresReopen(err error) bool {
	return err == nil || errors.Is(err, ErrDirectoryWriterActivationIndeterminate)
}
