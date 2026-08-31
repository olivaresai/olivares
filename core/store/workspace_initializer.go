// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// WorkspaceInitializationScope is the deliberately narrow transaction surface
// handed to a module when a workspace is materialized. The engine has already
// confined Ext to workspace, so an initializer cannot seed another workspace or
// reach core repositories. Both optional capabilities are mandatory here:
// bootstrap state whose ordering or time matters must fail initialization-time
// integration rather than fall back to a process clock or an in-process mutex.
//
// The handle is valid only for the duration of WorkspaceInitializer.Initialize.
type WorkspaceInitializationScope interface {
	Tenant() model.TenantID
	Workspace() model.Workspace
	TransactionClock
	TransactionLocker
	Ext(model.Kind) (GenericRepo, error)
}

// WorkspaceInitializer is a module-owned, engine-invoked workspace bootstrap.
// It runs in the SAME transaction that inserts the Workspace row, after that row
// is visible and through a workspace-confined scope. Returning an error aborts
// the workspace creation; initializers therefore must be deterministic and
// idempotent under a transaction retry.
type WorkspaceInitializer struct {
	// Key is a stable, versioned identifier owned by the module namespace, e.g.
	// "sessions.communication_guard.v1". Changing bootstrap semantics requires a
	// new key so diagnostics identify the code that actually ran.
	Key string
	// Initialize seeds or verifies the module-owned rows for Workspace().
	Initialize func(context.Context, WorkspaceInitializationScope) error
}

// Validate checks the declaration before the concrete registry accepts it.
func (i WorkspaceInitializer) Validate() error {
	if !controlKeyRE.MatchString(i.Key) {
		return fmt.Errorf(
			"%w: workspace initializer key %q must be a dotted lower-case identifier ending in a version",
			ErrInvalidDescriptor, i.Key,
		)
	}
	if i.Initialize == nil {
		return fmt.Errorf(
			"%w: workspace initializer %q has no callback",
			ErrInvalidDescriptor, i.Key,
		)
	}
	return nil
}
