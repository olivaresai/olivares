// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SkillsTarget is the stored fact a skills assignment is pinned to: the row's
// stored ID (the resource the native routes authorize), its version, the core
// workspace it belongs to (zero when it declares none) and the native permission
// that governs reading or writing it.
type SkillsTarget struct {
	ID          model.ID
	Version     int64
	WorkspaceID model.ID
	Permission  auth.Permission
}

// ReadSkillsTarget reads the profile, folder, template or session row named by
// a skills assignment, inside the caller's transaction. Profiles use profile_ref,
// folders workspace_ref, sessions run_ref, and templates their row ID. A write
// touches the row to fence its version until commit. A confined scope reaches
// only its workspace's rows; profiles and templates declare no workspace.
func (m *Module) ReadSkillsTarget(ctx context.Context, sc store.Scope, kind, id string, write bool) (SkillsTarget, error) {
	var target SkillsTarget
	var entity model.Kind
	switch kind {
	case "profile":
		entity, target.Permission = providerProfileKind, permProfileRead
		if write {
			target.Permission = permProfileWrite
		}
	case "folder":
		entity, target.Permission = workspaceKind, permWsRead
		if write {
			target.Permission = permWsAdmin
		}
	case "template":
		entity, target.Permission = templateKind, permTemplateRead
		if write {
			target.Permission = permTemplateWrite
		}
	case "session":
		entity, target.Permission = runKind, permRunRead
		if write {
			target.Permission = permRunWrite
		}
	default:
		return SkillsTarget{}, store.ErrNotFound
	}
	repo, err := sc.Ext(entity)
	if err != nil {
		return SkillsTarget{}, concealConfined(err)
	}
	var rec model.Record
	if kind == "template" {
		rec, err = repo.Get(ctx, model.ID(id))
	} else {
		var rows []model.Record
		locator := colRunRef
		if kind == "folder" {
			locator = colWsRef
		}
		if kind == "profile" {
			locator = colPPRef
		}
		rows, _, err = repo.List(ctx, model.Query{Filters: []model.Filter{eq(locator, id)}, Limit: 2})
		switch {
		case err != nil:
		case len(rows) == 0:
			err = store.ErrNotFound
		case len(rows) > 1:
			err = store.ErrStoreUnavailable
		default:
			rec = rows[0]
		}
	}
	if err != nil {
		return SkillsTarget{}, concealConfined(err)
	}
	if kind == "template" {
		if write && rec.String(colTplArchivedAt) != "" {
			return SkillsTarget{}, store.ErrConflict
		}
	} else if kind == "folder" {
		if write && rec.String(colWsState) != wsActive {
			return SkillsTarget{}, store.ErrConflict
		}
	} else if ws := rec.String(colRunAuthzWorkspaceID); ws != "" {
		target.WorkspaceID = model.ID(ws)
	}
	if write {
		if rec, err = repo.Update(ctx, rec); err != nil {
			return SkillsTarget{}, err
		}
	}
	target.ID, target.Version = model.ID(rec.String(model.ColID)), rec.Int(model.ColVersion)
	return target, nil
}

// concealConfined answers a confined scope's refusal of an entity with no
// workspace lineage the way a missing row is answered.
func concealConfined(err error) error {
	if errors.Is(err, store.ErrWorkspaceLineageRequired) {
		return auth.ErrRouteDenied
	}
	return err
}
