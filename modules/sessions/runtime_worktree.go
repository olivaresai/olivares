// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/gitlayout"
)

// runtime_worktree.go is the opt-in "new worktree" launch option: a session on a
// git workspace gets a git worktree and a branch of its own, under a configured
// directory, instead of the workspace folder itself.
//
// The default does not change. A launch that does not ask for it runs where it ran
// before, in the workspace folder, and none of this code is reached.
//
// What it reuses: the git CLI, for everything about git (creating, validating a
// branch name, deciding whether a branch is merged, removing); the validated
// <root>/<run_ref> join and the root validation of runtime_workspace_dir.go; the
// session git confinement (runtime_confinement_git.go), which already grants a
// linked worktree its own git metadata; and the release verb (cleanupRun), which is
// where the session's own directory is already removed.
//
// The branch is a fact on the row (colRunWorktreeBranch), written by this module
// alone: resume and release act on what the row says this module made, never on a
// path or a branch name the child could have changed.
//
// ⛔ ENGINE-SIDE GIT RUNS NEXT TO AN UNTRUSTED CHILD. A session in a linked worktree
// can write the repository's shared git directory (runtime_confinement_git.go), so
// its config and hooks are not trusted when the engine runs git afterwards, outside
// the session's confinement. Three things are done about it:
//
//   - every call switches repository hooks and the file-system monitor off, ignores
//     the system and user git configuration, and is bounded in time and output;
//   - the filters a repository configures (clean, smudge, process) run a command
//     whenever git checks a file out or compares its content, which `worktree add`
//     and a non-forced `worktree remove` do, and so does an included config file that
//     could name one. A repository whose config names any of them is refused for a
//     new or returning worktree (gitConfigCommand), and its release asks for the
//     confirmation, which removes the worktree without comparing anything;
//   - release judges a worktree by its own git state, never by a path or a name the
//     child could have changed (isRunWorktree, worktreeOnBranch).
//
// ponytail: a session that is still running can change the config between the check
// and the call; the release needs a stopped session, a launch has none yet.
const (
	defaultWorktreeBranchPrefix = "olivares/"
	worktreeGitTimeout          = 2 * time.Minute
	// worktreeGitWaitDelay bounds how long a git whose helper outlived it can hold
	// the call, after the timeout has already killed git itself.
	worktreeGitWaitDelay = 5 * time.Second
	// worktreeGitOutputLimit caps what one git call may hand back; a message that
	// reaches a caller or the ledger is cut to worktreeGitMessageLimit.
	worktreeGitOutputLimit  = 64 << 10
	worktreeGitMessageLimit = 300
	// worktreeBranchSuffixLen is the length of the run reference's random tail used
	// in the branch name. A run reference is a UUIDv7, whose head is a timestamp
	// shared by every session started in the same minute.
	worktreeBranchSuffixLen = 8
)

// UseSessionWorktrees names the directory under which a session launched with the
// worktree option gets its worktree (<root>/<run_ref>) and the prefix of the branch
// it is given (default "olivares/"), and returns the refusal. The root is validated
// where it is wired, like the session workspace root. An empty root is ignored, so a
// partial wiring cannot erase a configured one. Git judges the branch prefix when a
// worktree is made, because git owns what a branch name may be.
func (m *Module) UseSessionWorktrees(root, branchPrefix string) error {
	if branchPrefix = strings.TrimSpace(branchPrefix); branchPrefix == "" {
		branchPrefix = defaultWorktreeBranchPrefix
	}
	m.rt.worktreeBranchPrefix = branchPrefix
	if strings.TrimSpace(root) == "" {
		return nil
	}
	clean, err := validateSessionWorkspaceRoot(root, m.dirOwnerCheck())
	if err != nil {
		m.rt.worktreeRoot = ""
		m.rt.worktreeRootRefusal = &runErr{http.StatusServiceUnavailable,
			"the session worktree directory was refused: " + err.Error()}
		return m.rt.worktreeRootRefusal
	}
	m.rt.worktreeRoot, m.rt.worktreeRootRefusal = clean, nil
	return nil
}

// SessionWorktreesConfigured reports whether this node can give a session a
// worktree, so a boot can report the state the module is actually in.
func (m *Module) SessionWorktreesConfigured() bool { return m.rt.worktreeRoot != "" }

func (m *Module) worktreeRootOrErr() (string, error) {
	if m.rt.worktreeRoot != "" {
		return m.rt.worktreeRoot, nil
	}
	if m.rt.worktreeRootRefusal != nil {
		return "", m.rt.worktreeRootRefusal
	}
	return "", &runErr{http.StatusServiceUnavailable,
		"session worktrees are not available on this node: no worktree directory is configured"}
}

// cappedBuffer keeps the first limit bytes and drops the rest without failing the
// writer, so a chatty git cannot grow the engine's memory or block on a full pipe.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// gitMessage is git's own first line of explanation, cut and stripped of control
// characters, safe to hand to a caller and to the ledger.
func gitMessage(texts ...string) string {
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				line = strings.Map(func(r rune) rune {
					if !unicode.IsPrint(r) { // controls, and the format characters that reorder text
						return ' '
					}
					return r
				}, line)
				if len(line) > worktreeGitMessageLimit {
					line = strings.ToValidUTF8(line[:worktreeGitMessageLimit], "") + "…"
				}
				return line
			}
		}
	}
	return "git gave no reason"
}

// gitRun runs the git CLI in dir with the hardening above and returns stdout, the
// exit code and stderr. A non-zero exit is an answer, not an error; err is for git
// not running, being stopped (a timeout, a cancelled request, a signal) or not
// finishing, none of which is an answer to the question asked.
func gitRun(ctx context.Context, dir string, args ...string) (stdout string, code int, stderr string, err error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", -1, "", &runErr{http.StatusServiceUnavailable,
			"git is not available on this node, so a session cannot get a worktree"}
	}
	ctx, cancel := context.WithTimeout(ctx, worktreeGitTimeout)
	defer cancel()
	// No transport: a repository the child configured as a partial clone would make git
	// fetch a missing object through a remote of the child's choosing, and run the
	// ssh command it named, as the engine. The engine's git reads local objects only.
	cmd := exec.CommandContext(ctx, git, append([]string{
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
	}
	cmd.WaitDelay = worktreeGitWaitDelay
	out, errb := &cappedBuffer{limit: worktreeGitOutputLimit}, &cappedBuffer{limit: worktreeGitOutputLimit}
	cmd.Stdout, cmd.Stderr = out, errb
	waitErr := cmd.Run()
	var exit *exec.ExitError
	switch {
	case waitErr == nil:
		return out.buf.String(), 0, errb.buf.String(), nil
	case errors.As(waitErr, &exit) && exit.ExitCode() >= 0:
		return out.buf.String(), exit.ExitCode(), errb.buf.String(), nil
	default:
		// Killed by the timeout or a signal (exit code -1), or a helper held the pipes
		// past WaitDelay: git did not answer.
		return "", -1, errb.buf.String(), fmt.Errorf("git was stopped: %w", waitErr)
	}
}

// gitOK is gitRun for a call that must succeed; git's own words are the error.
func gitOK(ctx context.Context, dir string, args ...string) (string, error) {
	out, code, stderr, err := gitRun(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", errors.New(gitMessage(stderr, out))
	}
	return out, nil
}

// gitRefExists asks `rev-parse --verify --quiet`, which answers 0 (there) or 1 (not
// there); any other answer is git failing, not the ref being absent.
func gitRefExists(ctx context.Context, dir, ref string) (bool, error) {
	_, code, stderr, err := gitRun(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
	switch {
	case err != nil:
		return false, err
	case code == 0:
		return true, nil
	case code == 1:
		return false, nil
	}
	return false, errors.New(gitMessage(stderr))
}

// gitConfigCommand names the first setting of the repository's configuration, with
// the files it includes, that makes git run a command on a checkout or a comparison
// ("" when none does). Git's own regexp lookup is the question; nothing is parsed.
func gitConfigCommand(ctx context.Context, repo string) (string, error) {
	out, code, stderr, err := gitRun(ctx, repo, "config", "--includes", "--name-only", "--get-regexp",
		`^(filter\..*\.(clean|smudge|process)|include\.|includeif\.)`)
	switch {
	case err != nil:
		return "", err
	case code == 0:
		return gitMessage(out), nil
	case code == 1:
		return "", nil
	}
	return "", errors.New(gitMessage(stderr))
}

func worktreeRefusal(format string, args ...any) error {
	return &runErr{http.StatusUnprocessableEntity, fmt.Sprintf(format, args...)}
}

// worktreeWorkspaceProblem is why a workspace cannot honestly serve a worktree ("" when
// it can). It is asked at launch and again at resume, because a workspace may gain
// read-only folders or become read-only after the session was made, and the copy would
// not keep them.
func worktreeWorkspaceProblem(ws *resolvedWorkspace, isolation Isolation) string {
	switch {
	case ws == nil:
		return "a new worktree needs a workspace: choose a registered workspace that is a git repository"
	case isolation != IsolationNative:
		return "a new worktree is available for native sessions only (isolation=native)"
	case ws.mountMode == mountRO:
		return "a read-only workspace cannot take a new worktree"
	case len(ws.readOnlyFolders) > 0:
		return "a workspace with read-only folders cannot take a new worktree: the copy would not keep them read-only"
	}
	return ""
}

// resolveWorktreeStart turns the start a person named into the full commit id git holds
// for it: a full object id, or a local branch of the repository. Git decides what exists
// (rev-parse) and what a branch name may be (check-ref-format), and only the id it prints
// reaches `worktree add`, so nothing the caller wrote can be read there as an option or
// as a revision expression (main~3, @{-1} and :/text are refused as branch names).
// "Not there" is a refusal the person can act on; git not running is an error.
func resolveWorktreeStart(ctx context.Context, repo, from string) (string, error) {
	if len(from) > 512 || strings.ContainsRune(from, 0) {
		return "", worktreeRefusal("the worktree start is not a commit id or a branch name")
	}
	isID := validGitObjectID(from)
	rev := from
	if !isID {
		if _, code, _, err := gitRun(ctx, repo, "check-ref-format", "refs/heads/"+from); err != nil {
			return "", err
		} else if code != 0 {
			return "", worktreeRefusal("git does not accept %q as a branch name", from)
		}
		rev = "refs/heads/" + from
	}
	out, code, stderr, err := gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	switch {
	case err != nil:
		return "", err
	case code == 1 && isID:
		return "", worktreeRefusal("commit %s is not in the workspace repository; fetch it there first, or start from the workspace's current commit", from)
	case code == 1:
		return "", worktreeRefusal("the workspace repository has no local branch %q", from)
	case code != 0:
		return "", worktreeRefusal("git could not read %q: %s", from, gitMessage(stderr))
	}
	id := strings.TrimSpace(out)
	if !validGitObjectID(id) {
		return "", worktreeRefusal("git did not name a commit for %q", from)
	}
	if isID {
		// An id the object store still holds but no branch, tag or remote branch reaches
		// (an abandoned commit, a dangling object) is not work anyone handed over.
		held, err := gitOK(ctx, repo, "for-each-ref", "--count=1", "--format=%(refname)", "--contains", id)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(held) == "" {
			return "", worktreeRefusal("no branch or tag of the workspace repository holds commit %s", from)
		}
	}
	return id, nil
}

// prepareRunWorktree makes the worktree and branch of a launch that asked for them
// and returns the directory the child starts in. It runs before the claim, the
// gates, any credential and the row, so a launch it refuses leaves nothing durable
// behind, and a launch refused later is cleaned up by discardRunWorktree.
//
// from is where the worktree starts: "" is the workspace's HEAD, as it has always been;
// otherwise a commit id or a local branch of the workspace's repository, which is how a
// person opens the work a handoff names (resolveWorktreeStart).
func (m *Module) prepareRunWorktree(ctx context.Context, runRef string, ws *resolvedWorkspace, isolation Isolation, from string) (dir, branch string, err error) {
	root, err := m.worktreeRootOrErr()
	if err != nil {
		return "", "", err
	}
	if problem := worktreeWorkspaceProblem(ws, isolation); problem != "" {
		return "", "", worktreeRefusal("%s", problem)
	}
	out, err := gitOK(ctx, ws.rootReal, "rev-parse", "--is-inside-work-tree", "--show-prefix")
	if err != nil {
		return "", "", worktreeRefusal("git cannot use the workspace folder for a new worktree: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[0] != "true" {
		return "", "", worktreeRefusal("the workspace folder is not a git work tree, so it cannot take a new worktree")
	}
	if len(lines) > 1 && lines[1] != "" {
		return "", "", worktreeRefusal("a new worktree needs the workspace to be the repository's top folder, not %s inside it", strings.TrimSuffix(lines[1], "/"))
	}
	if hasCommit, err := gitRefExists(ctx, ws.rootReal, "HEAD^{commit}"); err != nil {
		return "", "", worktreeRefusal("git cannot read the workspace repository: %v", err)
	} else if !hasCommit {
		return "", "", worktreeRefusal("the workspace repository has no commit yet; make one before starting a session in a new worktree")
	}
	if key, err := gitConfigCommand(ctx, ws.rootReal); err != nil {
		return "", "", worktreeRefusal("git cannot read the workspace repository's configuration: %v", err)
	} else if key != "" {
		return "", "", worktreeRefusal("the workspace repository's git configuration names a command (%s) that the engine will not run for a session's worktree; remove it, or work in the folder itself", key)
	}
	start := "HEAD"
	if from != "" {
		if start, err = resolveWorktreeStart(ctx, ws.rootReal, from); err != nil {
			return "", "", err
		}
	}
	branch = m.rt.worktreeBranchPrefix + runRef[len(runRef)-worktreeBranchSuffixLen:]
	if _, code, _, err := gitRun(ctx, ws.rootReal, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", "", err
	} else if code != 0 {
		return "", "", worktreeRefusal("git does not accept %q as a branch name; change the worktree branch prefix", branch)
	}
	dir, err = joinRunDir(root, runRef)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(root, sessionWorkspaceDirMode); err != nil {
		return "", "", &runErr{http.StatusServiceUnavailable, "the session worktree directory could not be created on this node"}
	}
	if _, err := gitOK(ctx, ws.rootReal, "worktree", "add", "--quiet", "-b", branch, dir, start); err != nil {
		// Git leaves nothing behind when it refuses; a git that was stopped may have.
		m.warnf("sessions: git could not make a session's worktree; if one was started it is at this directory",
			"dir", dir, "branch", branch, "err", redactErr(err))
		return "", "", worktreeRefusal("git could not make the worktree for branch %s: %v", branch, err)
	}
	if err := os.Chmod(dir, sessionWorkspaceDirMode); err != nil {
		m.discardRunWorktree(ctx, ws.rootReal, dir, branch)
		return "", "", &runErr{http.StatusServiceUnavailable, "the session's worktree could not be made private on this node"}
	}
	return dir, branch, nil
}

// discardRunWorktree removes a worktree and branch this launch made and nothing ever
// used. It is for a launch that was refused after prepareRunWorktree, so the
// worktree is fresh and a forced removal loses nothing.
func (m *Module) discardRunWorktree(ctx context.Context, repo, dir, branch string) {
	ctx = context.WithoutCancel(ctx)
	if _, err := gitOK(ctx, repo, "worktree", "remove", "--force", dir); err != nil {
		m.warnf("sessions: a refused launch's worktree could not be removed",
			"dir", dir, "branch", branch, "err", redactErr(err))
	}
	if _, err := gitOK(ctx, repo, "branch", "-D", "--", branch); err != nil {
		m.warnf("sessions: a refused launch's worktree branch could not be removed",
			"dir", dir, "branch", branch, "err", redactErr(err))
	}
}

// isRunWorktree says why dir is not a linked worktree of the repository at repo (nil
// when it is): a real directory (never a link), whose .git pointer and backlink agree
// (gitlayout.Read) and whose shared git directory is the repository's own.
func (m *Module) isRunWorktree(ctx context.Context, repo, dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err != nil:
		return errors.New("it cannot be inspected")
	case !info.IsDir():
		return errors.New("it is not a directory")
	}
	layout, ok := gitlayout.Read(dir)
	if !ok || !layout.Linked() {
		return errors.New("its .git pointer does not lead to a linked worktree of a repository")
	}
	out, err := gitOK(ctx, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	common := strings.TrimSpace(out)
	if !filepath.IsAbs(common) {
		common = filepath.Join(repo, common)
	}
	if real, err := filepath.EvalSymlinks(common); err != nil || real != layout.CommonDir {
		return errors.New("it belongs to another repository")
	}
	return nil
}

// worktreeOnBranch reports whether the worktree has the session's branch checked out.
// A child that detached its HEAD or switched branches can commit work no named branch
// of the session holds, and removing the clean worktree would lose it.
func worktreeOnBranch(ctx context.Context, dir, branch string) (bool, error) {
	out, code, stderr, err := gitRun(ctx, dir, "symbolic-ref", "-q", "HEAD")
	switch {
	case err != nil:
		return false, err
	case code == 0:
		return strings.TrimSpace(out) == "refs/heads/"+branch, nil
	case code == 1:
		return false, nil // detached
	}
	return false, errors.New(gitMessage(stderr))
}

// ensureRunWorktree returns the directory a resumed worktree session continues in:
// the worktree it already has, or, when only the directory was removed, a new one on
// the branch the row names. Anything else at that path is refused, never continued
// in, exactly as a session whose own directory moved is.
func (m *Module) ensureRunWorktree(ctx context.Context, rec model.Record, runRef string, ws *resolvedWorkspace) (string, error) {
	branch := rec.String(colRunWorktreeBranch)
	root, err := m.worktreeRootOrErr()
	if err != nil {
		return "", err
	}
	if problem := worktreeWorkspaceProblem(ws, Isolation(rec.String(colIsolation))); problem != "" {
		return "", conflictErr("this session cannot continue in its worktree any more: " + problem)
	}
	dir, err := joinRunDir(root, runRef)
	if err != nil {
		return "", err
	}
	if stored := rec.String(colRunWorkspacePath); stored != "" && stored != dir {
		return "", conflictErr("this session ran in a worktree this node no longer resolves to; " +
			"resume is refused rather than continued somewhere else")
	}
	_, statErr := os.Lstat(dir)
	switch {
	case statErr == nil:
		if err := m.isRunWorktree(ctx, ws.rootReal, dir); err != nil {
			return "", conflictErr("the directory of this session's worktree is not its worktree any more (" +
				err.Error() + "); resume is refused rather than continued there")
		}
		return dir, nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return "", conflictErr("this session's worktree directory could not be inspected")
	}
	if exists, err := gitRefExists(ctx, ws.rootReal, "refs/heads/"+branch); err != nil {
		return "", conflictErr("git could not look for this session's branch " + branch + ": " + err.Error())
	} else if !exists {
		return "", conflictErr("this session's worktree and its branch " + branch + " are gone; resume is refused rather than started over")
	}
	if key, err := gitConfigCommand(ctx, ws.rootReal); err != nil {
		return "", conflictErr("git cannot read the workspace repository's configuration: " + err.Error())
	} else if key != "" {
		return "", conflictErr("the workspace repository's git configuration names a command (" + key +
			") that the engine will not run to bring this session's worktree back; remove it to resume")
	}
	if err := os.MkdirAll(root, sessionWorkspaceDirMode); err != nil {
		return "", &runErr{http.StatusServiceUnavailable, "the session worktree directory could not be created on this node"}
	}
	if _, err := gitOK(ctx, ws.rootReal, "worktree", "add", "--quiet", dir, branch); err != nil {
		// Git may still hold the record of the removed directory, which makes `add`
		// refuse. Forget THIS worktree's record and nothing else (a repository-wide
		// prune would also drop the records of other worktrees whose directories are
		// merely away, with their staged state), then try again. When there was no
		// record the removal has nothing to do, and the second `add` says what is wrong.
		_, _ = gitOK(ctx, ws.rootReal, "worktree", "remove", "--force", "--force", dir)
		if _, err := gitOK(ctx, ws.rootReal, "worktree", "add", "--quiet", dir, branch); err != nil {
			return "", conflictErr("git could not bring back this session's worktree on " + branch + ": " + err.Error())
		}
	}
	if err := os.Chmod(dir, sessionWorkspaceDirMode); err != nil {
		return "", &runErr{http.StatusServiceUnavailable, "the session's worktree could not be made private on this node"}
	}
	return dir, nil
}

// cleanupOptions are the choices a release can carry.
type cleanupOptions struct {
	// DiscardWorktree is the person's confirmation that the session's worktree and
	// branch may be removed although the branch is not merged, the worktree has
	// uncommitted files or its HEAD is off the branch, and that a worktree this node
	// cannot reach may be left in place.
	DiscardWorktree bool
}

// releaseRunWorktree removes the worktree and branch a session made, when that loses
// nothing or the person confirmed. Its answer is a ledger phrase ("" when the run
// has no worktree) or a 409 that names what would be lost and how to confirm.
//
// A worktree this release cannot reach or judge is REFUSED, not skipped: the row is
// deleted next, and a silent skip would leak the directory and the branch with
// nothing that names them. Confirming releases the session and leaves the worktree
// where it is, and says where.
//
// The merged test, the removal and the record of what is where are git's own:
// `merge-base --is-ancestor`, and `worktree remove`, which refuses a worktree with
// uncommitted or untracked files unless forced (git ignores files its ignore rules
// name, such as build output, and removes them with the directory).
func (m *Module) releaseRunWorktree(ctx context.Context, tenant model.TenantID, rec model.Record, runRef string, discard bool) (string, error) {
	branch := rec.String(colRunWorktreeBranch)
	if branch == "" {
		return "", nil
	}
	where := rec.String(colRunWorkspacePath)
	unreachable := func(why string) (string, error) {
		if !discard {
			return "", conflictErr("the worktree of branch " + branch + " cannot be released: " + why +
				"; fix that, or confirm to release the session and leave the worktree where it is")
		}
		m.warnf("sessions: a session was released and its worktree left in place",
			"run_ref", runRef, "dir", where, "branch", branch, "why", why)
		return "its worktree on branch " + branch + " was left in place at " + where + ": " + why, nil
	}
	root, err := m.worktreeRootOrErr()
	if err != nil {
		return unreachable("this node has no worktree directory configured")
	}
	dir, err := joinRunDir(root, runRef)
	if err != nil || where != dir {
		return unreachable("the recorded directory is not the one this node would make")
	}
	ws, err := m.resolveLaunchWorkspace(ctx, tenant, rec.String(colWorkspaceRef))
	if err != nil || ws == nil {
		return unreachable("its workspace is not available")
	}
	defer ws.closeReadOnlyHandles()
	repo := ws.rootReal

	_, statErr := os.Lstat(dir)
	present := statErr == nil
	if !present && !errors.Is(statErr, fs.ErrNotExist) {
		return unreachable("its directory could not be inspected")
	}
	if present {
		if err := m.isRunWorktree(ctx, repo, dir); err != nil {
			return unreachable("the directory is not its worktree any more (" + err.Error() + ")")
		}
	}
	branchExists, err := gitRefExists(ctx, repo, "refs/heads/"+branch)
	if err != nil {
		return unreachable("git could not look for the branch: " + err.Error())
	}
	merged := !branchExists
	if branchExists {
		_, code, stderr, err := gitRun(ctx, repo, "merge-base", "--is-ancestor", "refs/heads/"+branch, "HEAD")
		switch {
		case err != nil:
			return unreachable("git could not compare the branch: " + err.Error())
		case code == 0:
			merged = true
		case code != 1:
			return unreachable("git could not compare the branch: " + gitMessage(stderr))
		}
	}
	var risks []string
	if !merged {
		risks = append(risks, "branch "+branch+" has work that is not merged into the workspace's current branch")
	}
	if present {
		onBranch, err := worktreeOnBranch(ctx, dir, branch)
		if err != nil {
			return unreachable("git could not read the worktree's HEAD: " + err.Error())
		}
		if !onBranch {
			risks = append(risks, "the worktree is not on branch "+branch+" (its HEAD is detached or on another branch), so its commits may be on no branch")
		}
		if !discard {
			// From inside the worktree: with extensions.worktreeConfig it has a config of
			// its own that only git run there reads, and the status check runs there.
			key, err := gitConfigCommand(ctx, dir)
			if err != nil {
				return unreachable("git could not read the repository's configuration: " + err.Error())
			}
			if key != "" {
				risks = append(risks, "the repository's git configuration names a command ("+key+"), so the engine will not check that the worktree is clean")
			}
		}
	}
	if len(risks) > 0 && !discard {
		return "", conflictErr(strings.Join(risks, "; ") + "; merge or commit the work, or confirm to discard the session's worktree and branch")
	}

	tip := ""
	if branchExists {
		if out, err := gitOK(ctx, repo, "rev-parse", "--short=12", "refs/heads/"+branch); err == nil {
			tip = strings.TrimSpace(out)
		}
	}
	if present {
		args := []string{"worktree", "remove"}
		if discard {
			// Twice: a worktree the child locked needs it, and a forced removal compares nothing.
			args = append(args, "--force", "--force")
		}
		if _, err := gitOK(ctx, repo, append(args, dir)...); err != nil {
			return "", conflictErr("the worktree of branch " + branch + " has uncommitted changes or git keeps it (" +
				err.Error() + "); commit them, or confirm to discard")
		}
	} else if _, err := gitOK(ctx, repo, "worktree", "remove", "--force", "--force", dir); err != nil {
		// No record of it any more (git forgot it): nothing to forget. A branch a record
		// still holds shows at the delete below, which says so without losing the release.
		m.warnf("sessions: git had no record of a released session's removed worktree",
			"run_ref", runRef, "dir", dir, "branch", branch, "git", redactErr(err))
	}
	m.warnf("sessions: a session's worktree was removed on release",
		"run_ref", runRef, "dir", dir, "branch", branch, "tip", tip, "confirmed_discard", discard && len(risks) > 0)
	if !branchExists {
		return "its worktree was removed", nil
	}
	flag := "-d"
	if !merged {
		flag = "-D"
	}
	if _, err := gitOK(ctx, repo, "branch", flag, "--", branch); err != nil {
		return "its worktree was removed; branch " + branch + " (at " + tip + ") was kept: " + err.Error(), nil
	}
	detail := "its worktree and branch " + branch + " (at " + tip + ") were removed"
	if len(risks) > 0 {
		detail += " after the person confirmed discarding work that was not merged or not on the branch"
	}
	return detail, nil
}
