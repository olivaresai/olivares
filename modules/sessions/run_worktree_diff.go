// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// THE SESSION WORKTREE'S DIFF: what the branch of a session's worktree holds that the
// workspace's current commit does not, as the changed paths and, for one path, its text
// at the point the branch left the workspace and at the branch tip. This is what a
// person sees after opening the work a handoff names (runtime_worktree.go): the commits
// of that branch against the workspace, beside the session. The base is the merge base
// with the workspace's CURRENT commit, so work that was merged into it since is no longer
// listed.
//
// Read-only, from git's committed objects of the workspace repository and nothing
// else: the session's files are not read (they are what run_changes.go shows), so
// uncommitted edits are not here. Only plumbing runs (merge-base, diff-tree, rev-parse,
// cat-file) through the hardened gitRun of runtime_worktree.go, which switches hooks,
// the file-system monitor and every transport off, ignores user and system
// configuration, and bounds time and output. None of these runs a filter, a textconv or
// an external diff.
//
// The authority is the run's own read (permRunRead, concealed as a 404 for another
// workspace), and the text is the repository's history, so it also keeps the workspace's
// own file rules: only the allowed subpaths are listed or read, a DLP posture that
// denies denies, and a read is audited like a workspace file read (workspace.go
// readFile).
const (
	worktreeDiffMaxFiles = 200
	// worktreeDiffMaxBlob is the largest file read at all: git streams a blob whole, and a
	// session can commit a huge one, so a bigger one is refused before it is read.
	worktreeDiffMaxBlob = 16 << 20
)

type worktreeDiffFile struct {
	Path string `json:"path"`
	// Status is added, modified or deleted, from the base to the branch tip.
	Status string `json:"status"`
}

type runWorktreeDiffDTO struct {
	Branch string `json:"branch"`
	// Base is the commit the branch left the workspace's current commit at (their
	// merge base); Head is the branch tip. Both are full commit ids.
	Base  string             `json:"base"`
	Head  string             `json:"head"`
	Files []worktreeDiffFile `json:"files"`
	// Truncated is true when the branch changed more paths than are listed.
	Truncated bool `json:"truncated"`
}

type runWorktreeDiffFileDTO struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	// Original is the file at Base and Modified the file at Head; each is empty when the
	// file does not exist there, and both are empty for a binary file.
	Original  string `json:"original"`
	Modified  string `json:"modified"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

// worktreeCompare is what a run's worktree is compared at: the workspace whose repository
// holds it, the session's branch tip and the merge base of that tip with the workspace's
// current commit.
type worktreeCompare struct {
	ws         *resolvedWorkspace
	branch     string
	base, head string
}

func (c worktreeCompare) repo() string { return c.ws.rootReal }

// inScope says whether a repository path lies under the workspace's allowed subpaths.
func (c worktreeCompare) inScope(p string) bool {
	return underAllowedSubpath(c.ws.rootReal, c.ws.allowSubpaths, filepath.Join(c.ws.rootReal, filepath.FromSlash(p)))
}

func (m *Module) worktreeDiffRange(ctx context.Context, tenant model.TenantID, runRef string) (worktreeCompare, error) {
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return worktreeCompare{}, err
	}
	branch := rec.String(colRunWorktreeBranch)
	if branch == "" {
		return worktreeCompare{}, &runErr{http.StatusNotFound, "this session has no worktree of its own to compare"}
	}
	ws, err := m.resolveWorkspace(ctx, tenant, rec.String(colWorkspaceRef))
	if err != nil {
		return worktreeCompare{}, err
	}
	repo := ws.rootReal

	headOut, code, stderr, err := gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+branch+"^{commit}")
	switch {
	case err != nil:
		return worktreeCompare{}, err
	case code == 1:
		return worktreeCompare{}, conflictErr("this session's branch " + branch + " is gone")
	case code != 0:
		return worktreeCompare{}, conflictErr("git could not read this session's branch " + branch + ": " + gitMessage(stderr))
	}
	head := strings.TrimSpace(headOut)
	baseOut, code, stderr, err := gitRun(ctx, repo, "merge-base", "HEAD", head)
	switch {
	case err != nil:
		return worktreeCompare{}, err
	case code == 1:
		return worktreeCompare{}, conflictErr("this session's branch " + branch + " shares no history with the workspace, so there is no base to compare")
	case code != 0:
		return worktreeCompare{}, conflictErr("git could not compare this session's branch " + branch + ": " + gitMessage(stderr))
	}
	base := strings.TrimSpace(baseOut)
	if !validGitObjectID(head) || !validGitObjectID(base) {
		return worktreeCompare{}, conflictErr("git did not name the commits of branch " + branch)
	}
	return worktreeCompare{ws: ws, branch: branch, base: base, head: head}, nil
}

// worktreeStatusWord maps git's one-letter change status. A type change is a
// modification to a person.
func worktreeStatusWord(letter string) string {
	switch letter {
	case "A":
		return "added"
	case "D":
		return "deleted"
	}
	return "modified"
}

// handleRunWorktreeDiff lists the paths the run's worktree branch changed since it left
// the workspace's current commit. Nothing is executed in the session's folder.
func (m *Module) handleRunWorktreeDiff(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	cmp, err := m.worktreeDiffRange(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	out, code, stderr, err := gitRun(r.Context(), cmp.repo(), "diff-tree", "-r", "-z", "--name-status", "--no-renames", cmp.base, cmp.head)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	if code != 0 {
		writeRunErr(w, conflictErr("git could not list the changes of branch "+cmp.branch+": "+gitMessage(stderr)))
		return
	}
	dto := runWorktreeDiffDTO{Branch: cmp.branch, Base: cmp.base, Head: cmp.head, Files: []worktreeDiffFile{}}
	// -z: status NUL path NUL, repeated. Output past the cap of gitRun is cut, so a
	// listing that fills it is incomplete, says so, and drops the record the cut may have
	// split.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(out) >= worktreeGitOutputLimit {
		dto.Truncated = true
		if len(fields) > 0 {
			fields = fields[:len(fields)-1]
		}
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if !cmp.inScope(fields[i+1]) {
			continue
		}
		if len(dto.Files) == worktreeDiffMaxFiles {
			dto.Truncated = true
			break
		}
		dto.Files = append(dto.Files, worktreeDiffFile{Path: fields[i+1], Status: worktreeStatusWord(fields[i])})
	}
	writeJSON(w, http.StatusOK, dto)
}

// worktreeBlobAt reads path at commit as text. exists is false when the commit has no
// such path; a path that is a directory or a submodule is refused, as is a file past
// worktreeDiffMaxBlob or a read git could not complete. The text is at most
// min(maxReadBytes, worktreeGitOutputLimit) bytes.
func worktreeBlobAt(ctx context.Context, repo, commit, file string, maxReadBytes int64) (text string, exists, binary, truncated bool, err error) {
	idOut, code, stderr, err := gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+":"+file)
	switch {
	case err != nil:
		return "", false, false, false, err
	case code == 1:
		return "", false, false, false, nil
	case code != 0:
		return "", false, false, false, conflictErr("git could not read " + file + ": " + gitMessage(stderr))
	}
	id := strings.TrimSpace(idOut)
	if !validGitObjectID(id) {
		return "", false, false, false, conflictErr("git did not name an object for " + file)
	}
	kind, err := gitOK(ctx, repo, "cat-file", "-t", id)
	if err != nil {
		return "", false, false, false, conflictErr("git could not read " + file + ": " + err.Error())
	}
	if strings.TrimSpace(kind) != "blob" {
		return "", false, false, false, &runErr{http.StatusBadRequest, "path must name a file, not a folder or a submodule"}
	}
	sizeOut, err := gitOK(ctx, repo, "cat-file", "-s", id)
	if err != nil {
		return "", false, false, false, conflictErr("git could not read " + file + ": " + err.Error())
	}
	size, err := strconv.Atoi(strings.TrimSpace(sizeOut))
	if err != nil {
		return "", false, false, false, conflictErr("git did not give the size of " + file)
	}
	if size > worktreeDiffMaxBlob {
		return "", false, false, false, &runErr{http.StatusRequestEntityTooLarge, "this file is too large to compare here"}
	}
	body, err := gitOK(ctx, repo, "cat-file", "blob", id)
	if err != nil {
		return "", false, false, false, conflictErr("git could not read " + file + ": " + err.Error())
	}
	if int64(len(body)) > maxReadBytes {
		body = body[:maxReadBytes]
	}
	truncated = size > len(body)
	if truncated {
		// The cap may have cut a character in two.
		for i := 0; i < utf8.UTFMax && !utf8.ValidString(body); i++ {
			body = body[:len(body)-1]
		}
	}
	if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return "", true, true, truncated, nil
	}
	return body, true, false, truncated, nil
}

// handleRunWorktreeDiffFile returns one path of the run's worktree branch at the base and
// at the branch tip (?path=, relative to the repository's top folder; each side keeps
// the workspace's read limit and the 64 KiB Git output cap).
func (m *Module) handleRunWorktreeDiffFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	file := r.URL.Query().Get("path")
	if clean := path.Clean(file); file == "" || clean != file || path.IsAbs(file) || clean == "." ||
		clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(file, 0) {
		writeJSON(w, http.StatusBadRequest, errorBody("path must name a file inside the repository"))
		return
	}
	cmp, err := m.worktreeDiffRange(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	// Outside the workspace's allowed subpaths a path is not there, as it is not for the
	// workspace's own file read.
	if !cmp.inScope(file) {
		writeJSON(w, http.StatusNotFound, errorBody("no such file at the base or at the branch tip"))
		return
	}
	original, hadBase, binBase, cutBase, err := worktreeBlobAt(r.Context(), cmp.repo(), cmp.base, file, cmp.ws.maxReadBytes)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	modified, hasHead, binHead, cutHead, err := worktreeBlobAt(r.Context(), cmp.repo(), cmp.head, file, cmp.ws.maxReadBytes)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	status := "modified"
	switch {
	case !hadBase && !hasHead:
		writeJSON(w, http.StatusNotFound, errorBody("no such file at the base or at the branch tip"))
		return
	case !hadBase:
		status = "added"
	case !hasHead:
		status = "deleted"
	}
	binary := binBase || binHead

	// The workspace's DLP posture applies to history as it does to the files. A binary
	// file cannot be scanned, which a denying posture refuses, as the file read does.
	hitsOld, denyOld := m.classifyContent(r.Context(), cmp.ws.dlpMode, []byte(original))
	hitsNew, denyNew := m.classifyContent(r.Context(), cmp.ws.dlpMode, []byte(modified))
	classes := append(dlpClasses(hitsOld), dlpClasses(hitsNew)...)
	audit := wsMutationInput{
		workspaceID: cmp.ws.id, workspaceRef: cmp.ws.ref, path: file,
		actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), classes: classes,
	}
	if denyOld || denyNew || (binary && cmp.ws.dlpMode == dlpDeny) {
		audit.op = "read-denied"
		m.auditWorkspaceRead(r.Context(), mc.Tenant, audit)
		writeRunErr(w, forbiddenErr("read denied by workspace DLP policy"+classSuffix(classes)))
		return
	}
	audit.op = "read-diff"
	m.auditWorkspaceRead(r.Context(), mc.Tenant, audit)

	if binary {
		original, modified = "", ""
	}
	writeJSON(w, http.StatusOK, runWorktreeDiffFileDTO{
		Path: file, Status: status, Original: original, Modified: modified,
		Binary: binary, Truncated: cutBase || cutHead,
	})
}
