// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// MCPProbePolicy permits discovery in private scratch space. A filesystem
// server may inspect an argument directory only when it is inside an active
// folder registered by this tenant. Discovery cannot write that folder, reach
// another tenant's folder or bypass the node's protected paths.
func (m *Module) MCPProbePolicy(ctx context.Context, tenant model.TenantID, scratch, toolDir string, args []string) (*confine.Policy, error) {
	policy := m.ConfinementPolicy([]string{scratch}, []string{toolDir})
	if policy == nil {
		return nil, nil
	}
	paths := []string{}
	for _, arg := range args {
		if !filepath.IsAbs(arg) {
			continue
		}
		path, err := filepath.EvalSymlinks(arg)
		if err != nil {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return policy, nil
	}
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		query := model.Query{Limit: 200, Filters: []model.Filter{eq(colWsState, wsActive)}}
		for {
			rows, page, err := repo.List(ctx, query)
			if err != nil {
				return err
			}
			for _, row := range rows {
				root := row.String(colWsRootPath)
				for _, path := range paths {
					rel, err := filepath.Rel(root, path)
					if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						policy.ReadOnly = append(policy.ReadOnly, path)
					}
				}
			}
			if !page.HasMore {
				return nil
			}
			query.Cursor = page.Cursor
		}
	})
	return policy, err
}
