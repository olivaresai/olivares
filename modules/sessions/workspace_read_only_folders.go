// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Additional host access is a workspace administrator's choice. Store canonical
// directories so replacing a symlink cannot redirect a later launch's authority.
func (m *Module) normalizeWorkspaceReadOnlyFolders(paths []string) ([]string, error) {
	out := []string{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
			return nil, badRequest("read_only_folders entries must be absolute folder paths")
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, badRequest(fmt.Sprintf("The read-only folder %s is unavailable on this server.", path))
		}
		if err := m.checkWorkspaceReadOnlyFolder(real); err != nil {
			return nil, err
		}
		if !slices.Contains(out, real) {
			out = append(out, real)
		}
	}
	return out, nil
}

func (m *Module) checkWorkspaceReadOnlyFolder(path string) error {
	real, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || !info.IsDir() || real != path {
		return badRequest(fmt.Sprintf("The read-only folder %s is unavailable or has changed on this server.", path))
	}
	for _, protected := range m.rt.confineProtect {
		if pathsOverlap(path, protected) {
			return badRequest("read_only_folders cannot overlap protected engine folders")
		}
	}
	return nil
}

// NULL is the upgrade default: no extra access. Corrupt stored configuration
// refuses resolution rather than silently widening or dropping authority.
func decodeWorkspaceReadOnlyFolders(rec model.Record) ([]string, error) {
	if rec.IsNull(colWsReadOnlyFolders) {
		return []string{}, nil
	}
	raw, ok := rec[colWsReadOnlyFolders].(string)
	var paths []*string
	if !ok || json.Unmarshal([]byte(raw), &paths) != nil || paths == nil {
		return nil, &runErr{http.StatusFailedDependency, "workspace read_only_folders configuration is invalid"}
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == nil || !filepath.IsAbs(*path) || filepath.Clean(*path) != *path || strings.ContainsRune(*path, '\x00') {
			return nil, &runErr{http.StatusFailedDependency, "workspace read_only_folders configuration is invalid"}
		}
		out = append(out, *path)
	}
	return out, nil
}

func (m *Module) patchWorkspaceReadOnlyFolders(ctx context.Context, tenant model.TenantID, ref string, paths []string, actor, actorKind string) (workspaceDTO, error) {
	folders, err := m.normalizeWorkspaceReadOnlyFolders(paths)
	if err != nil {
		return workspaceDTO{}, err
	}
	raw, _ := json.Marshal(folders)
	var out workspaceDTO
	err = m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		rec, err := findWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		rec[colWsReadOnlyFolders] = string(raw)
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		if err := appendWorkspaceAudit(ctx, sc, wsMutationInput{
			workspaceID: model.ID(rec.String(model.ColID)), workspaceRef: ref, op: "configure",
			path: rec.String(colWsRootPath), contentHash: sha256Hex(raw), actor: actor, actorKind: actorKind,
		}); err != nil {
			return err
		}
		out, err = toWorkspaceDTO(updated)
		return err
	})
	return out, err
}
