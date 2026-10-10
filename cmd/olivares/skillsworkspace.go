// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/skills"
	"io/fs"
)

// Composition only: skills receives a verified snapshot, never a sessions owner
// or host filesystem capability. Callback bytes remain staged until success.
type skillsWorkspaceReader struct{ owner *sessions.Module }

var _ skills.WorkspaceImporter = skillsWorkspaceReader{}

func (r skillsWorkspaceReader) ReadSkillsWorkspace(ctx context.Context, mc api.ModuleContext, ref, dir string) (skills.WorkspaceSnapshot, error) {
	if r.owner == nil {
		return skills.WorkspaceSnapshot{}, store.ErrConflict
	}
	var files []skills.WorkspaceFile
	version, err := r.owner.ReadWorkspaceSnapshot(ctx, mc.Tenant, mc.Principal, ref, dir, func(path string, mode fs.FileMode, data []byte) error {
		files = append(files, skills.WorkspaceFile{Path: path, Mode: mode, Bytes: append([]byte(nil), data...)})
		return nil
	})
	if err != nil {
		return skills.WorkspaceSnapshot{}, err
	}
	if version < 1 {
		return skills.WorkspaceSnapshot{}, store.ErrConflict
	}
	return skills.WorkspaceSnapshot{RegistrationVersion: version, Files: files}, nil
}
