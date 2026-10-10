// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"bytes"
	"context"
	"io/fs"
	"path"

	"github.com/olivaresai/olivares/core/api"
)

// WorkspaceImporter is an authorized, stable snapshot from the existing
// registered-workspace file owner. No host path crosses this port. The adapter
// retains native source-scope, jail, DLP and stricter read limits; these files
// are validated again by skills before catalog publication.
type WorkspaceImporter interface {
	ReadSkillsWorkspace(context.Context, api.ModuleContext, string, string) (WorkspaceSnapshot, error)
}
type WorkspaceSnapshot struct {
	RegistrationVersion int64
	Files               []WorkspaceFile
}
type WorkspaceFile struct {
	Path  string
	Bytes []byte
	Mode  fs.FileMode
}

func (m *Module) importWorkspace(ctx context.Context, mc api.ModuleContext, ref, dir string) (*ValidatedPack, int64, error) {
	if !canonicalID(ref) || !safePath(dir) {
		return nil, 0, errInvalidRequest
	}
	if m.opts.Workspace == nil {
		return nil, 0, refuse("source_unavailable", "registered workspace snapshot not connected")
	}
	snapshot, err := m.opts.Workspace.ReadSkillsWorkspace(ctx, mc, ref, dir)
	if err != nil {
		return nil, 0, err
	}
	if snapshot.RegistrationVersion < 1 {
		return nil, 0, refuse("source_changed", "workspace registration version")
	}
	if len(snapshot.Files) > MaxFiles {
		return nil, 0, refuse("import_limit", "workspace file count")
	}
	tree := newTree()
	prefix := ""
	for _, file := range snapshot.Files {
		if file.Path == "SKILL.md" {
			prefix = path.Base(dir)
			break
		}
	}
	for _, file := range snapshot.Files {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if !file.Mode.IsRegular() {
			return nil, 0, refuse("unsafe_pack", file.Path)
		}
		if !safePath(file.Path) {
			return nil, 0, refuse("unsafe_pack", file.Path)
		}
		name := file.Path
		if prefix != "" {
			name = prefix + "/" + name
		}
		if err := tree.entry(name, false); err != nil {
			return nil, 0, err
		}
		if err := tree.file(name, bytes.Clone(file.Bytes), file.Mode); err != nil {
			return nil, 0, err
		}
	}
	pack, err := tree.validate("")
	if err != nil {
		return nil, 0, err
	}
	pack.SourceDigest = pack.ManifestDigest
	return pack, snapshot.RegistrationVersion, nil
}
