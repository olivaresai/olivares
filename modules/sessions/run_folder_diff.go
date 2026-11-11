// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/gitlayout"
)

// THE FOLDER SESSION'S DIFF: a session with no worktree of its own works in its Git
// folder itself, and what it changed is the working tree against the folder's HEAD:
// tracked files modified, deleted or added to the index, and untracked files the
// repository does not ignore.
//
// The list comes from the session's own confined Git (openRunGit, read-only), the same
// door as the Git panel's status: it runs under the session's OS boundary, so a filter
// or attribute the session configured runs only where the session could run it. Only
// the workspace's allowed subpaths are listed. A file's two sides are the workspace's
// own governed reads (readFileAtHead and readFile): its path rules, size limit, DLP
// posture and audit apply, so nothing is shown that the file browser would not show.

// folderDiff lists what the run's Git folder changed against its HEAD.
func (m *Module) folderDiff(ctx context.Context, tenant model.TenantID, runRef string) (runWorktreeDiffDTO, error) {
	g, release, err := m.openFolderGit(ctx, tenant, runRef)
	if err != nil {
		return runWorktreeDiffDTO{}, err
	}
	defer release()
	return folderChanges(ctx, g)
}

// folderChanges is the change list of a folder's working tree against its HEAD, in the
// allowed subpaths: the only paths the diff routes show.
func folderChanges(ctx context.Context, g sessionGit) (runWorktreeDiffDTO, error) {
	headOut, err := g.exec(ctx, []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}, "", nil)
	var exit *gitpublish.LocalCommandExit
	if errors.As(err, &exit) && exit.Code == 1 {
		return runWorktreeDiffDTO{}, &codedRunErr{conflictErr("this folder has no commit yet to compare with"), "no_commit"}
	}
	if err != nil {
		return runWorktreeDiffDTO{}, err
	}
	base := strings.TrimSpace(headOut)
	if !validGitObjectID(base) {
		return runWorktreeDiffDTO{}, &codedRunErr{conflictErr("git did not name the commit of this folder's HEAD"), "head_unreadable"}
	}
	// A detached HEAD has no branch to name.
	branch, _ := g.exec(ctx, []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, "", nil)
	tracked, err := g.exec(ctx, []string{"diff", "-z", "--name-status", "--no-renames", "HEAD", "--"}, "", nil)
	if err != nil {
		return runWorktreeDiffDTO{}, err
	}
	untracked, err := g.exec(ctx, []string{"ls-files", "-z", "--others", "--exclude-standard"}, "", nil)
	if err != nil {
		return runWorktreeDiffDTO{}, err
	}
	for _, p := range strings.Split(strings.TrimSuffix(untracked, "\x00"), "\x00") {
		if p != "" {
			tracked += "A\x00" + p + "\x00"
		}
	}
	inScope := func(p string) bool {
		return g.workspace == nil || underAllowedSubpath(g.dir, g.workspace.allowSubpaths, filepath.Join(g.dir, filepath.FromSlash(p)))
	}
	dto := runWorktreeDiffDTO{Branch: strings.TrimSpace(branch), Base: base, WorkingTree: true}
	dto.Files, dto.Truncated = worktreeDiffFiles(tracked, inScope)
	counts, err := g.exec(ctx, []string{"diff", "-z", "--numstat", "--no-renames", "HEAD", "--"}, "", nil)
	if err == nil {
		err = applyLineCounts(counts, dto.Files)
	}
	if err != nil {
		return runWorktreeDiffDTO{}, &codedRunErr{conflictErr("git could not count the changed lines of this folder: " + err.Error()), "diff_unreadable"}
	}
	return dto, nil
}

// folderDiffFile returns one path of the run's Git folder at HEAD and in the working
// tree, each read as the workspace's file browser reads it.
func (m *Module) folderDiffFile(ctx context.Context, mc api.ModuleContext, runRef, file string) (runWorktreeDiffFileDTO, error) {
	g, release, err := m.openFolderGit(ctx, mc.Tenant, runRef)
	if err != nil {
		return runWorktreeDiffFileDTO{}, err
	}
	defer release()
	if g.workspace == nil {
		return runWorktreeDiffFileDTO{}, errNoOwnWorktree
	}
	// The file browser's own folder read right, at the folder's resource.
	folder := auth.ResourceAttrs{Kind: "resource", ID: g.workspace.id.String()}
	if err := m.authorizeWorkspaceRight(ctx, mc, permWsRead, auth.TrusteeRead, folder); err != nil {
		return runWorktreeDiffFileDTO{}, err
	}
	// Only a listed change is read: never .git, an ignored file or an unchanged one.
	changes, err := folderChanges(ctx, g)
	if err != nil {
		return runWorktreeDiffFileDTO{}, err
	}
	if !slices.ContainsFunc(changes.Files, func(f worktreeDiffFile) bool { return f.Path == file }) {
		return runWorktreeDiffFileDTO{}, &runErr{http.StatusNotFound, "no such change in the folder"}
	}
	wsRef := g.record.String(colWorkspaceRef)
	actor, kind := mc.Principal.Actor(), mc.Principal.ActorKind()
	side := func(read func() (fileReadResponse, error)) (text string, exists, binary, truncated bool, err error) {
		got, err := read()
		var refusal *runErr
		if errors.As(err, &refusal) && refusal.status == http.StatusNotFound {
			return "", false, false, false, nil
		}
		if err != nil {
			return "", false, false, false, err
		}
		if got.Encoding != "utf-8" {
			return "", true, true, got.Truncated, nil
		}
		return got.Content, true, false, got.Truncated, nil
	}
	original, hadBase, binBase, cutBase, err := side(func() (fileReadResponse, error) {
		return m.readFileAtHead(ctx, mc.Tenant, wsRef, file, actor, kind)
	})
	if err != nil {
		return runWorktreeDiffFileDTO{}, err
	}
	modified, hasNow, binNow, cutNow, err := side(func() (fileReadResponse, error) {
		return m.readFile(ctx, mc.Tenant, wsRef, file, actor, kind)
	})
	if err != nil {
		return runWorktreeDiffFileDTO{}, err
	}
	status := "modified"
	switch {
	case !hadBase && !hasNow:
		return runWorktreeDiffFileDTO{}, &runErr{http.StatusNotFound, "no such file at HEAD or in the folder"}
	case !hadBase:
		status = "added"
	case !hasNow:
		status = "deleted"
	}
	return runWorktreeDiffFileDTO{Path: file, Status: status, Original: original, Modified: modified,
		Binary: binBase || binNow, Truncated: cutBase || cutNow}, nil
}

// openFolderGit is the run's confined Git, read-only, for a folder that is a Git
// repository; a folder that is not one answers 404 with its own code.
func (m *Module) openFolderGit(ctx context.Context, tenant model.TenantID, runRef string) (sessionGit, func(), error) {
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return sessionGit{}, nil, err
	}
	// Session Git runs only beside a native session; any other keeps its old answer.
	if isolation := rec.String(colIsolation); isolation != "" && isolation != string(IsolationNative) {
		return sessionGit{}, nil, errNoOwnWorktree
	}
	g, _, release, err := m.openRunGit(ctx, tenant, runRef)
	if err != nil {
		return sessionGit{}, nil, err
	}
	if _, ok := gitlayout.Read(g.dir); !ok {
		release()
		return sessionGit{}, nil, errFolderNotGit
	}
	g.preset = PresetReadOnly
	return g, release, nil
}
