// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
)

// handleRunWorktreeMerge fast-forwards the workspace's current branch to the
// stopped session's owned branch. Dirty folders and diverged histories are
// refused; no commit, worktree removal or forced update occurs. It uses the
// existing confined Git runner and seals the normal workspace mutation intent.
func (m *Module) handleRunWorktreeMerge(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return
	}
	var body struct{}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	g, rec, release, err := m.openRunGit(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRuntimeControlErr(w, err)
		return
	}
	defer release()
	fail := func(err error) { writeSessionGitErr(w, err) }
	if rec.String(colState) != stateStopped {
		fail(conflictErr("stop this session before keeping its branch"))
		return
	}
	if err := m.refuseUncollectedProcess(mc.Tenant, rec.String(colRunRef), stateStopped, "merge"); err != nil {
		fail(err)
		return
	}
	branch := rec.String(colRunWorktreeBranch)
	if branch == "" || g.workspace == nil {
		fail(conflictErr("this session has no owned worktree branch to keep"))
		return
	}
	// Different sessions can share this destination. Serialize their keep actions
	// in addition to the source run lock already held by openRunGit.
	unlock, err := m.rt.lockRunContext(r.Context(), "worktree-merge|"+mc.Tenant.String()+"|"+rec.String(colWorkspaceRef))
	if err != nil {
		fail(err)
		return
	}
	defer unlock()
	source, err := g.status(r.Context())
	if err != nil {
		fail(err)
		return
	}
	if source.Branch != branch {
		fail(conflictErr("the session is no longer on its owned branch"))
		return
	}
	if len(source.Files) != 0 || source.Truncated {
		fail(conflictErr("commit the session's changes before keeping its branch"))
		return
	}
	g.dir = g.workspace.rootReal
	target, err := g.status(r.Context())
	if err != nil {
		fail(err)
		return
	}
	if target.Branch == "" {
		fail(conflictErr("the workspace needs a checked-out branch before keeping a result"))
		return
	}
	// ponytail: refuse target names with an equals sign until mergeOptions
	// uses native key/value overrides; -c splits at the first equals sign.
	if strings.Contains(target.Branch, "=") {
		fail(conflictErr("keeping a result is unavailable on a workspace branch containing an equals sign; rename the branch or merge with Git"))
		return
	}
	if target.Branch == branch {
		fail(conflictErr("the workspace already has the session's branch checked out"))
		return
	}
	if len(target.Files) != 0 || target.Truncated {
		fail(conflictErr("the workspace has uncommitted changes; commit or stash them before keeping a result"))
		return
	}
	head, err := g.exec(r.Context(), []string{"rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/" + branch + "^{commit}"}, "", nil)
	if err != nil {
		fail(err)
		return
	}
	head = strings.TrimSpace(head)
	if !validGitObjectID(head) {
		fail(conflictErr("Git did not name a commit for the session's branch"))
		return
	}
	intent := map[string]string{"branch": branch, "commit": head, "target": target.Branch}
	if err := m.beginRunGitWrite(r.Context(), mc.Tenant, rec, g, mc.Principal, "merge", intent, nil); err != nil {
		writeRuntimeControlErr(w, err)
		return
	}
	// A full immutable object id is the only variable merge argument. The runner
	// disables hooks and transports and confines every child, including a filter
	// introduced by another writer after status inspected the configuration.
	_, err = g.exec(r.Context(), []string{"-c", "submodule.recurse=false", "-c", "merge.autostash=false", "-c", "branch." + target.Branch + ".mergeOptions=", "merge", "--ff-only", "--no-squash", "--no-verify-signatures", "--no-autostash", "--no-overwrite-ignore", "--no-edit", "--no-stat", "--no-verify", "--no-gpg-sign", "--", head}, "", nil)
	if err != nil {
		fail(err)
		return
	}
	// Git must confirm the selected commit is now included, including an
	// already-merged result whose destination has advanced since the race.
	if _, err = g.exec(r.Context(), []string{"merge-base", "--is-ancestor", head, "HEAD"}, "", nil); err != nil {
		fail(fmt.Errorf("Git did not confirm that the workspace includes the selected result: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
