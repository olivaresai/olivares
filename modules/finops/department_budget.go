// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// resolveWorkspace keeps external workspace references exact, and expands a stored
// department to its descendants through the tenant's scope-tree lineage. References
// keep the budget key's form (slug or ID); a flat workspace keeps its equality filter.
// The result belongs to this read only, never to the stored policy.
func (s *budgetSpec) resolveWorkspace(ctx context.Context, sc store.Scope) error {
	if s.Dimension != "workspace" || s.workspaceRefs != nil {
		return nil
	}
	refs := []string{s.Key}
	rows, _, err := sc.Workspaces().List(ctx, model.Query{Filters: []model.Filter{eq("slug", s.Key)}, Limit: 1})
	if err != nil {
		return err
	}
	var root model.Workspace
	byID := false
	if len(rows) != 0 {
		root = rows[0]
	} else if id, parseErr := model.ParseID(s.Key); parseErr == nil {
		root, err = sc.Workspaces().Get(ctx, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		byID = err == nil
	}
	if root.ID.IsZero() {
		s.workspaceRefs = refs
		return nil
	}
	if _, _, err := store.WorkspaceLineage(ctx, sc, root.ID); err != nil {
		return err
	}
	err = store.WalkPages(ctx, sc.Workspaces().List, model.Query{Limit: listCap}, maxScanPages*listCap, func(rows []model.Workspace) error {
		for _, ws := range rows {
			if ws.ParentID.IsZero() {
				continue
			}
			_, above, err := store.WorkspaceLineage(ctx, sc, ws.ID)
			if err != nil {
				return err
			}
			if contains(above, root.Slug) {
				ref := ws.Slug
				if byID {
					ref = ws.ID.String()
				}
				refs = append(refs, ref)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(refs)
	s.workspaceRefs = refs
	return nil
}

// validWorkspaceRefs is the canonical shape used by both the strict cost read
// and the durable evidence: a nonempty, sorted, unique set with descendants.
func validWorkspaceRefs(refs []string) bool {
	if len(refs) < 2 || len(refs) > maxScanPages*listCap {
		return false
	}
	for i, ref := range refs {
		if ref == "" || (i > 0 && refs[i-1] >= ref) {
			return false
		}
	}
	return true
}
