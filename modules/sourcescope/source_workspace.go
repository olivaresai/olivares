// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrSourceWorkspace refuses a source whose enabled allow bindings do not name
// exactly one existing workspace. Forbids remain the existing resolver's concern.
var ErrSourceWorkspace = errors.New("source must be confined to exactly one workspace")

// SourceWorkspace derives an exclusive workspace from stored allow bindings. A
// role, user, group or second-workspace allow would widen it and is refused. The
// scope is already tenant-pinned; no workspace comes from the ingest request.
func SourceWorkspace(ctx context.Context, sc store.Scope, sourceType, sourceRef string) (model.Workspace, error) {
	bindings, err := loadEnabledBindings(ctx, sc, sourceType, sourceRef)
	if err != nil {
		return model.Workspace{}, err
	}
	var selected model.Workspace
	for _, b := range bindings {
		if b.isForbid() {
			continue
		}
		if b.scopeTree != scopeWorkspace {
			return model.Workspace{}, ErrSourceWorkspace
		}
		slug := b.scopeRef
		if slug == "" {
			slug = model.DefaultWorkspaceSlug
		}
		ws, found, err := findWorkspaceBySlug(ctx, sc, slug)
		if err != nil {
			return model.Workspace{}, err
		}
		if !found || b.workspaceID != ws.ID || (!selected.ID.IsZero() && selected.ID != ws.ID) {
			return model.Workspace{}, ErrSourceWorkspace
		}
		selected = ws
	}
	if selected.ID.IsZero() {
		return model.Workspace{}, ErrSourceWorkspace
	}
	return selected, nil
}
