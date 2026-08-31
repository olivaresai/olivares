// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workspaceInitializationScopeAdapter retains only method values from a
// workspace-confined Scope. Its dynamic type cannot be asserted back to
// store.Scope, so a module initializer cannot recover core repositories or
// another module namespace from this engine-authoritative bootstrap path.
type workspaceInitializationScopeAdapter struct {
	tenant    model.TenantID
	workspace model.Workspace
	namespace string
	now       func(context.Context) (model.Timestamp, error)
	lock      func(context.Context, string) error
	ext       func(model.Kind) (store.GenericRepo, error)
}

var _ store.WorkspaceInitializationScope = (*workspaceInitializationScopeAdapter)(nil)

func (s *workspaceInitializationScopeAdapter) Tenant() model.TenantID {
	return s.tenant
}

func (s *workspaceInitializationScopeAdapter) Workspace() model.Workspace {
	return s.workspace
}

func (s *workspaceInitializationScopeAdapter) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.now(ctx)
}

func (s *workspaceInitializationScopeAdapter) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.lock(ctx, key)
}

func (s *workspaceInitializationScopeAdapter) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	if kind.Namespace() != s.namespace {
		return nil, store.ErrUnknownEntity
	}
	return s.ext(kind)
}

// initializeWorkspace invokes every registered initializer in stable key order
// through a workspace-confined and namespace-confined adapter. It is called
// only from a transaction that has already inserted workspace.
func (sc *tenantScope) initializeWorkspace(ctx context.Context, workspace model.Workspace) error {
	if sc == nil || sc.s == nil || sc.tx == nil {
		return fmt.Errorf("sqlstore: workspace initializer has no transaction")
	}
	if workspace.ID.IsZero() || workspace.TenantID != sc.tenant {
		return fmt.Errorf(
			"sqlstore: workspace initializer scope mismatch: tenant=%s workspace=%s/%s",
			sc.tenant, workspace.TenantID, workspace.ID,
		)
	}
	initializers := sc.s.reg.registeredWorkspaceInitializers()
	if len(initializers) == 0 {
		return nil
	}
	confined, err := store.ConfineWorkspace(ctx, sc, workspace.ID)
	if err != nil {
		return fmt.Errorf("confine workspace initializer: %w", err)
	}
	clock, ok := confined.(store.TransactionClock)
	if !ok {
		return fmt.Errorf("sqlstore: confined workspace initializer has no transaction clock")
	}
	locker, ok := confined.(store.TransactionLocker)
	if !ok {
		return fmt.Errorf("sqlstore: confined workspace initializer has no transaction locker")
	}
	for _, initializer := range initializers {
		namespace := controlNamespace(initializer.Key)
		adapter := &workspaceInitializationScopeAdapter{
			tenant: sc.tenant, workspace: workspace, namespace: namespace,
			now: clock.TransactionNow, lock: locker.LockTransaction, ext: confined.Ext,
		}
		if err := initializer.Initialize(ctx, adapter); err != nil {
			return fmt.Errorf("workspace initializer %q: %w", initializer.Key, err)
		}
	}
	return nil
}
