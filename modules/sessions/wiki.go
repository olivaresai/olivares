// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/skills"
)

// WikiInitResult identifies the governed folder and its immutable native skill pin.
type wikiSkillsError struct{ err error }

func (e *wikiSkillsError) Error() string { return e.err.Error() }
func (e *wikiSkillsError) Unwrap() error { return e.err }

type WikiInitResult struct {
	WorkspaceRef string            `json:"workspace_ref"`
	Created      []string          `json:"created"`
	Assignment   skills.Assignment `json:"assignment"`
}

// UseWikiSkills binds the existing skills publisher at composition, before serving.
func (m *Module) UseWikiSkills(assign func(context.Context, api.ModuleContext, skills.Target) (skills.Assignment, error)) {
	m.wikiSkills = assign
}

func (m *Module) handleInitWiki(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.workspaceAllowed(w, r, mc, permWsAdmin, auth.TrusteeModify) {
		return
	}
	if !mc.Admits(r.Context(), permWsWrite, mc.Resource) {
		writeRunErr(w, forbiddenErr("workspace file write permission is required"))
		return
	}
	out, err := m.initWiki(r.Context(), mc, chi.URLParam(r, "ref"))
	if err != nil {
		var skillErr *wikiSkillsError
		if errors.As(err, &skillErr) {
			skills.WriteOperationError(w, skillErr.err)
			return
		}
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) initWiki(ctx context.Context, mc api.ModuleContext, ref string) (WikiInitResult, error) {
	out := WikiInitResult{WorkspaceRef: ref, Created: []string{}}
	if m.wikiSkills == nil {
		return out, &runErr{http.StatusServiceUnavailable, "wiki skills are unavailable"}
	}
	ws, err := m.resolveWorkspace(ctx, mc.Tenant, ref)
	if err != nil {
		return out, err
	}
	if ws.mountMode == mountRO {
		return out, forbiddenErr("workspace is read-only")
	}
	schema, err := skills.WikiSchema(ctx)
	if err != nil {
		return out, &runErr{http.StatusServiceUnavailable, "wiki schema is unavailable"}
	}
	if int64(len(schema)) > ws.maxReadBytes {
		return out, badRequest("wiki schema exceeds workspace size limit")
	}
	root, err := os.OpenRoot(ws.rootReal)
	if err != nil {
		return out, mapFSErr(err)
	}
	defer root.Close()
	names := []string{"raw", "wiki", "index.md", "log.md", "AGENTS.md"}
	missing := map[string]bool{}
	// Validate every path before publishing a pin or making any filesystem change.
	for _, name := range names {
		if _, err := ws.jail(name, false); err != nil {
			return out, mapFSErr(err)
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			missing[name] = true
			continue
		}
		if err != nil {
			return out, mapFSErr(err)
		}
		directory := name == "raw" || name == "wiki"
		if info.Mode()&os.ModeSymlink != 0 || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
			return out, conflictErr("wiki path must be a regular file or directory: " + name)
		}
	}
	out.Assignment, err = m.wikiSkills(ctx, mc, skills.Target{Kind: "folder", ID: ref})
	if err != nil {
		return out, &wikiSkillsError{err}
	}
	actor, kind := mc.Principal.Actor(), mc.Principal.ActorKind()
	for _, name := range names[:2] {
		if !missing[name] {
			continue
		}
		if _, err := m.mkdir(ctx, mc.Tenant, ref, name, actor, kind); err != nil {
			return out, err
		}
		out.Created = append(out.Created, name)
	}
	initial := map[string][]byte{"index.md": []byte("# Wiki index\n\n"), "log.md": []byte("# Wiki log\n\n"), "AGENTS.md": schema}
	for _, name := range names[2:] {
		if !missing[name] {
			continue
		}
		written, err := m.writeWorkspaceFile(ctx, mc.Tenant, ref, name, initial[name], actor, kind, true)
		if err != nil {
			return out, err
		}
		if written.Created {
			out.Created = append(out.Created, name)
		}
	}
	return out, nil
}
