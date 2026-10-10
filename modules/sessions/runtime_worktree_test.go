// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The opt-in worktree per session (K4.A1). Every test drives the real git CLI on a
// real repository through createRun / resumeRun / cleanupRunWith, the module's own
// interface; nothing here stands in for git.

type worktreeHarness struct {
	t      *testing.T
	m      *Module
	st     store.Store
	tenant model.TenantID
	fr     *fakeRunner
	repo   string
	root   string // the worktree directory root
	ws     string // the registered workspace ref
}

func worktreeGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "user.name=K4A1", "-c", "user.email=k4a1@example.invalid", "-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitRepoFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable: " + err.Error())
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, repo, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"README": "base\n", "sub/inner.txt": "inner\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	worktreeGitOut(t, repo, "add", ".")
	worktreeGitOut(t, repo, "commit", "-qm", "base")
	return repo
}

func newWorktreeHarness(t *testing.T, opts ...Option) (*worktreeHarness, context.Context) {
	t.Helper()
	repo := gitRepoFixture(t)
	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-worktree"}
	m, st, tenant, _ := newRuntimeHarness(t, append([]Option{
		WithRunner(fr), WithCredentialSource(staticCred()),
	}, opts...)...)
	if err := m.UseSessionWorktrees(root, ""); err != nil {
		t.Fatal(err)
	}
	h := &worktreeHarness{t: t, m: m, st: st, tenant: tenant, fr: fr, repo: repo, root: root}
	h.ws = registerTestWorkspace(t, m, tenant, repo)
	return h, context.Background()
}

func (h *worktreeHarness) params(worktree bool) CreateRunParams {
	return CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: h.ws,
		Worktree: worktree, Actor: "user:u1", ActorKind: "user",
	}
}

func (h *worktreeHarness) launch(ctx context.Context, worktree bool) runDTO {
	h.t.Helper()
	sessionID := "sess-worktree-" + model.NewID().String()
	h.fr.initSID = sessionID
	dto, err := createProfiledTestRun(h.t, h.m, ctx, h.tenant, h.params(worktree))
	if err != nil {
		h.t.Fatalf("createRun(worktree=%v): %v", worktree, err)
	}
	waitFor(h.t, "worktree provider identity", func() bool {
		stored, err := h.m.getRun(ctx, h.tenant, dto.RunRef)
		return err == nil && stored.ClaudeSessionID == sessionID
	})
	return dto
}

func (h *worktreeHarness) stop(ctx context.Context, ref string) {
	h.t.Helper()
	if _, err := h.m.stopRun(ctx, h.tenant, ref, "user:u1", "user"); err != nil {
		h.t.Fatalf("stopRun: %v", err)
	}
}

func (h *worktreeHarness) resume(ctx context.Context, ref string) (runDTO, error) {
	stored, err := h.m.getRun(ctx, h.tenant, ref)
	if err != nil {
		return runDTO{}, err
	}
	h.fr.initSID = stored.ClaudeSessionID
	return h.m.resumeRun(ctx, h.tenant, ref, "user:u1", "user", "")
}

func (h *worktreeHarness) release(ctx context.Context, ref string, discard bool) (runDTO, error) {
	return h.m.cleanupRunWith(ctx, h.tenant, ref, "user:u1", "user", cleanupOptions{DiscardWorktree: discard})
}

func (h *worktreeHarness) branches() string {
	return worktreeGitOut(h.t, h.repo, "branch", "--format=%(refname:short)")
}

func (h *worktreeHarness) worktreeCount() int {
	return len(strings.Split(worktreeGitOut(h.t, h.repo, "worktree", "list", "--porcelain"), "\n\n"))
}

// branchSuffix is the random tail of a run reference: a UUIDv7 starts with a
// timestamp, so its head is shared by every session started in the same minute.
func branchSuffix(runRef string) string { return runRef[len(runRef)-8:] }

func wantRunErr(t *testing.T, err error, status int, mention string) {
	t.Helper()
	var re *runErr
	if !errors.As(err, &re) || re.status != status {
		t.Fatalf("want a %d refusal, got %v", status, err)
	}
	if mention != "" && !strings.Contains(err.Error(), mention) {
		t.Fatalf("the refusal %q must mention %q", err.Error(), mention)
	}
}

// A launch without the option behaves exactly as today: the child runs in the
// registered workspace itself, and no branch or worktree appears.
func TestLaunchWithoutTheWorktreeOptionRunsInTheWorkspace(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	branchesBefore := h.branches()

	dto := h.launch(ctx, false)

	if got := h.fr.lastSpec().Dir; got != h.repo {
		t.Fatalf("child directory = %q, want the workspace %q", got, h.repo)
	}
	if dto.WorktreeBranch != "" || dto.WorkspacePath != h.repo {
		t.Fatalf("record names a worktree without being asked: branch=%q path=%q", dto.WorktreeBranch, dto.WorkspacePath)
	}
	if h.branches() != branchesBefore || h.worktreeCount() != 1 {
		t.Fatalf("a launch without the option created git state: %s / %d worktrees", h.branches(), h.worktreeCount())
	}
	entries, _ := os.ReadDir(h.root)
	if len(entries) != 0 {
		t.Fatalf("a launch without the option wrote under the worktree root: %v", entries)
	}
}

// A launch with the option gets its own worktree and branch under the configured
// directory; the workspace keeps its checkout, and the row records both.
func TestLaunchWithTheWorktreeOptionGetsItsOwnWorktreeAndBranch(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	headBefore := worktreeGitOut(t, h.repo, "rev-parse", "HEAD")

	dto := h.launch(ctx, true)

	wantDir := filepath.Join(h.root, dto.RunRef)
	if got := h.fr.lastSpec().Dir; got != wantDir {
		t.Fatalf("child directory = %q, want its worktree %q", got, wantDir)
	}
	wantBranch := "olivares/" + branchSuffix(dto.RunRef)
	if dto.WorktreeBranch != wantBranch {
		t.Fatalf("branch = %q, want %q", dto.WorktreeBranch, wantBranch)
	}
	if got := worktreeGitOut(t, wantDir, "rev-parse", "--abbrev-ref", "HEAD"); got != wantBranch {
		t.Fatalf("the worktree is on %q, want %q", got, wantBranch)
	}
	if got := worktreeGitOut(t, wantDir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("the worktree starts at %s, want the workspace HEAD %s", got, headBefore)
	}
	if got := worktreeGitOut(t, h.repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("the workspace checkout moved to %q", got)
	}
	if info, err := os.Stat(wantDir); err != nil || info.Mode().Perm() != sessionWorkspaceDirMode {
		t.Fatalf("the worktree directory is not private: %v %v", info, err)
	}
	stored, err := h.m.getRun(ctx, h.tenant, dto.RunRef)
	if err != nil || stored.WorktreeBranch != wantBranch || stored.WorkspacePath != wantDir {
		t.Fatalf("stored run = %+v, err=%v", stored, err)
	}
	// The directory is the session's worktree and never "its own directory" in the
	// sense of the release purge: the ownership column stays false.
	rec, err := h.m.loadRun(ctx, h.tenant, dto.RunRef)
	if err != nil || rec.Bool(colRunWorkspaceDirOwned) {
		t.Fatalf("a worktree run claims the purge-owned directory fact: %v", err)
	}
}

// Two sessions on one workspace work in two worktrees at once.
func TestTwoWorktreeSessionsOnOneWorkspaceDoNotShareFiles(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)

	a, b := h.launch(ctx, true), h.launch(ctx, true)

	if a.WorkspacePath == b.WorkspacePath || a.WorktreeBranch == b.WorktreeBranch {
		t.Fatalf("two sessions share a worktree: %q %q", a.WorkspacePath, b.WorkspacePath)
	}
	if err := os.WriteFile(filepath.Join(a.WorkspacePath, "only-in-a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{b.WorkspacePath, h.repo} {
		if _, err := os.Stat(filepath.Join(other, "only-in-a.txt")); !os.IsNotExist(err) {
			t.Fatalf("a file written in one worktree is visible in %s", other)
		}
	}
	if n := h.worktreeCount(); n != 3 {
		t.Fatalf("worktrees = %d, want the workspace and two sessions", n)
	}
}

// Resume returns to the same worktree and the work in it, also when the directory
// was removed in between and only the branch is left.
func TestResumeReturnsToTheSessionsWorktree(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	dir := dto.WorkspacePath
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.stop(ctx, dto.RunRef)

	if _, err := h.resume(ctx, dto.RunRef); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := h.fr.lastSpec().Dir; got != dir {
		t.Fatalf("resume started in %q, want the worktree %q", got, dir)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "work.txt")); err != nil || string(b) != "work\n" {
		t.Fatalf("resume lost the work in the worktree: %v", err)
	}
	h.stop(ctx, dto.RunRef)

	// The directory is gone (an operator's cleanup); the committed work is on the branch.
	worktreeGitOut(t, dir, "add", "work.txt")
	worktreeGitOut(t, dir, "commit", "-qm", "work")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := h.resume(ctx, dto.RunRef); err != nil {
		t.Fatalf("resume after the directory was removed: %v", err)
	}
	if got := h.fr.lastSpec().Dir; got != dir {
		t.Fatalf("resume started in %q, want %q", got, dir)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "work.txt")); err != nil || string(b) != "work\n" {
		t.Fatalf("the re-created worktree lacks the committed work: %v", err)
	}
	if got := worktreeGitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != dto.WorktreeBranch {
		t.Fatalf("the re-created worktree is on %q, want %q", got, dto.WorktreeBranch)
	}
}

// A resume refuses a directory that is not this session's worktree instead of
// continuing in it.
func TestResumeRefusesAWorktreePathThatIsNotAWorktree(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	h.stop(ctx, dto.RunRef)
	if err := os.RemoveAll(dto.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dto.WorkspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dto.WorkspacePath, "not-a-worktree"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := h.resume(ctx, dto.RunRef)

	wantRunErr(t, err, http.StatusConflict, "worktree")
}

// Releasing a session whose branch is not merged keeps the worktree and the
// branch until the person confirms; confirming removes both.
func TestReleaseOfAnUnmergedWorktreeNeedsConfirmation(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	dir, branch := dto.WorkspacePath, dto.WorktreeBranch
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, dir, "add", "work.txt")
	worktreeGitOut(t, dir, "commit", "-qm", "unmerged work")
	// An uncommitted file as well: confirming is what discards it, and git removes a
	// worktree holding one only when forced.
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.stop(ctx, dto.RunRef)

	_, err := h.release(ctx, dto.RunRef, false)

	wantRunErr(t, err, http.StatusConflict, branch)
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("an unconfirmed release removed the worktree: %v", statErr)
	}
	rec, _ := h.m.loadRun(ctx, h.tenant, dto.RunRef)
	if rec.String(colState) != stateStopped {
		t.Fatalf("a refused release changed the state to %q", rec.String(colState))
	}

	cleaned, err := h.release(ctx, dto.RunRef, true)
	if err != nil || cleaned.State != stateCleaned {
		t.Fatalf("confirmed release: %v %+v", err, cleaned)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("the confirmed release left the worktree: %v", statErr)
	}
	if strings.Contains(h.branches(), branch) || h.worktreeCount() != 1 {
		t.Fatalf("the confirmed release left git state: %s / %d worktrees", h.branches(), h.worktreeCount())
	}
}

// A merged branch with a clean worktree is removed without asking; a merged branch
// with uncommitted files still needs the confirmation, because they would be lost.
func TestReleaseOfAMergedCleanWorktreeNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	dir, branch := dto.WorkspacePath, dto.WorktreeBranch
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, dir, "add", "work.txt")
	worktreeGitOut(t, dir, "commit", "-qm", "merged work")
	worktreeGitOut(t, h.repo, "merge", "-q", "--ff-only", branch)
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.stop(ctx, dto.RunRef)

	_, err := h.release(ctx, dto.RunRef, false)
	wantRunErr(t, err, http.StatusConflict, "uncommitted")
	if _, statErr := os.Stat(filepath.Join(dir, "scratch.txt")); statErr != nil {
		t.Fatalf("the refused release lost an uncommitted file: %v", statErr)
	}

	if err := os.Remove(filepath.Join(dir, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	cleaned, err := h.release(ctx, dto.RunRef, false)
	if err != nil || cleaned.State != stateCleaned {
		t.Fatalf("release of a merged clean worktree: %v %+v", err, cleaned)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("the merged worktree was not removed: %v", statErr)
	}
	if strings.Contains(h.branches(), branch) || h.worktreeCount() != 1 {
		t.Fatalf("the merged release left git state: %s / %d worktrees", h.branches(), h.worktreeCount())
	}
	if b, err := os.ReadFile(filepath.Join(h.repo, "work.txt")); err != nil || string(b) != "work\n" {
		t.Fatalf("the merged work is not in the workspace: %v", err)
	}
}

// A session that did nothing leaves nothing: its branch has no commits of its own.
func TestReleaseOfAnUntouchedWorktreeRemovesItWithoutConfirmation(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	h.stop(ctx, dto.RunRef)

	if _, err := h.release(ctx, dto.RunRef, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(dto.WorkspacePath); !os.IsNotExist(err) || h.worktreeCount() != 1 {
		t.Fatalf("an untouched worktree survived its release: %v / %d", err, h.worktreeCount())
	}
}

// The option is refused where a worktree cannot honestly serve the launch.
func TestWorktreeLaunchRefusals(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	plainWS := registerTestWorkspace(t, h.m, h.tenant, t.TempDir())
	subfolderWS := registerTestWorkspace(t, h.m, h.tenant, filepath.Join(h.repo, "sub"))
	lockedRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lockedFolder := filepath.Join(lockedRoot, "tools")
	if err := os.Mkdir(lockedFolder, 0o700); err != nil {
		t.Fatal(err)
	}
	locked, err := h.m.createWorkspace(ctx, h.tenant, CreateWorkspaceParams{
		RootPath: lockedRoot, ReadOnlyFolders: []string{lockedFolder}, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	ro, err := h.m.createWorkspace(ctx, h.tenant, CreateWorkspaceParams{
		RootPath: t.TempDir(), MountMode: mountRO, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		edit    func(*CreateRunParams)
		mention string
	}{
		"folder that is not a git repository": {func(p *CreateRunParams) { p.WorkspaceRef = plainWS }, "git"},
		"folder inside a repository":          {func(p *CreateRunParams) { p.WorkspaceRef = subfolderWS }, "top folder"},
		"no workspace at all":                 {func(p *CreateRunParams) { p.WorkspaceRef = "" }, "workspace"},
		"workspace with read-only folders":    {func(p *CreateRunParams) { p.WorkspaceRef = locked.WorkspaceRef }, "read-only"},
		"read-only workspace":                 {func(p *CreateRunParams) { p.WorkspaceRef = ro.WorkspaceRef }, "read-only"},
		"container isolation":                 {func(p *CreateRunParams) { p.Isolation = IsolationContainer }, "native"},
	} {
		p := h.params(true)
		c.edit(&p)
		_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, p)
		t.Run(name, func(t *testing.T) { wantRunErr(t, err, http.StatusUnprocessableEntity, c.mention) })
	}
	if len(h.fr.specs) != 0 || h.worktreeCount() != 1 {
		t.Fatalf("a refused launch reached the runner or created a worktree: %d specs, %d worktrees", len(h.fr.specs), h.worktreeCount())
	}
}

// A node with no worktree directory refuses the option and still serves every
// launch that does not ask for it.
func TestWorktreeLaunchWithoutAConfiguredRootIsRefused(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	h.m.rt.worktreeRoot = ""

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))

	wantRunErr(t, err, http.StatusServiceUnavailable, "worktree")
	h.launch(ctx, false)
}

// A launch refused after its worktree was made, and before its row exists, leaves
// no worktree and no branch. The gate is asked after the worktree is made, so the
// refusal is the one that reaches the clean-up.
func TestRefusedWorktreeLaunchLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	gate := &spyGate{inner: LaunchDecision{Allowed: false, Reason: "policy: not now"}}
	h, ctx := newWorktreeHarness(t, WithLaunchGate(gate))
	branchesBefore := h.branches()

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))

	if err == nil || len(gate.seen) != 1 {
		t.Fatalf("the launch should have been refused by the gate (seen %d): %v", len(gate.seen), err)
	}
	if h.branches() != branchesBefore || h.worktreeCount() != 1 {
		t.Fatalf("a refused launch left git state: %s / %d worktrees", h.branches(), h.worktreeCount())
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 0 {
		t.Fatalf("a refused launch left a directory under the worktree root: %v", entries)
	}
}

// The configured branch prefix is used, and git itself judges whether the name is valid.
func TestWorktreeBranchPrefixIsConfigurableAndCheckedByGit(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	if err := h.m.UseSessionWorktrees(h.root, "team/ai-"); err != nil {
		t.Fatal(err)
	}
	dto := h.launch(ctx, true)
	if want := "team/ai-" + branchSuffix(dto.RunRef); dto.WorktreeBranch != want {
		t.Fatalf("branch = %q, want %q", dto.WorktreeBranch, want)
	}

	if err := h.m.UseSessionWorktrees(h.root, "bad prefix ~"); err != nil {
		t.Fatal(err)
	}
	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "branch")
}

// Engine-side git never runs what the repository's own config names: the shared git
// directory is writable by a session in a worktree, so its hooks and fsmonitor
// command are not trusted on create, resume or release.
func TestEngineGitDoesNotRunRepositoryConfiguredCommands(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran >> "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\necho ran >> "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, h.repo, "config", "core.hooksPath", hooks)
	worktreeGitOut(t, h.repo, "config", "core.fsmonitor", script)

	dto := h.launch(ctx, true)
	h.stop(ctx, dto.RunRef)
	if _, err := h.resume(ctx, dto.RunRef); err != nil {
		t.Fatal(err)
	}
	h.stop(ctx, dto.RunRef)
	if _, err := h.release(ctx, dto.RunRef, true); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("engine-side git ran a command from the repository config:\n%s", b)
	}
}

// plantGitFilter writes what a session in a linked worktree can write into the shared
// git directory: clean and smudge filters, selected for every path, that record a
// marker when git runs them.
func plantGitFilter(t *testing.T, repo string) (marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "filter.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran >> "+marker+"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, repo, "config", "filter.x.smudge", script)
	worktreeGitOut(t, repo, "config", "filter.x.clean", script)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte("* filter=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return marker
}

func wantNoMarker(t *testing.T, marker string) {
	t.Helper()
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("engine-side git ran a filter from the repository config:\n%s", b)
	}
}

// A filter or an included config file runs a command on every checkout and comparison,
// so a repository that names one is refused for a new worktree, whether the setting is
// in the repository's config or in a file it includes, and nothing runs.
func TestWorktreeLaunchRefusesARepositoryWhoseConfigNamesAFilter(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	branches := h.branches()
	marker := plantGitFilter(t, h.repo)

	_, err := createProfiledTestRun(t, h.m, ctx, h.tenant, h.params(true))

	wantRunErr(t, err, http.StatusUnprocessableEntity, "filter.x")
	wantNoMarker(t, marker)
	if h.branches() != branches || h.worktreeCount() != 1 {
		t.Fatalf("a refused launch left git state: %s / %d worktrees", h.branches(), h.worktreeCount())
	}

	// The same through an included file: it is no less a command.
	h2, ctx2 := newWorktreeHarness(t)
	included := filepath.Join(t.TempDir(), "extra.cfg")
	if err := os.WriteFile(included, []byte("[core]\n\tsomething = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, h2.repo, "config", "include.path", included)
	_, err = createProfiledTestRun(t, h2.m, ctx2, h2.tenant, h2.params(true))
	wantRunErr(t, err, http.StatusUnprocessableEntity, "include.path")
}

// A session can plant a filter after its launch. Release then cannot compare the
// worktree safely, so it asks for the confirmation, which removes it without comparing;
// and bringing a removed worktree back refuses. Neither runs the filter.
func TestReleaseAndResumeWithAFilterInTheRepositoryRunNothing(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	h.stop(ctx, dto.RunRef)
	marker := plantGitFilter(t, h.repo)

	_, err := h.release(ctx, dto.RunRef, false)
	wantRunErr(t, err, http.StatusConflict, "git configuration")
	if _, statErr := os.Stat(dto.WorkspacePath); statErr != nil {
		t.Fatalf("a refused release removed the worktree: %v", statErr)
	}

	if err := os.RemoveAll(dto.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	_, err = h.resume(ctx, dto.RunRef)
	wantRunErr(t, err, http.StatusConflict, "git configuration")

	cleaned, err := h.release(ctx, dto.RunRef, true)
	if err != nil || cleaned.State != stateCleaned {
		t.Fatalf("confirmed release: %v %+v", err, cleaned)
	}
	wantNoMarker(t, marker)
}

// With extensions.worktreeConfig a linked worktree has a config of its own, which only
// git run INSIDE it reads, and the status check of a plain `worktree remove` runs
// there. A filter planted in it must be seen by the release's check, not only one in
// the repository's shared config.
func TestReleaseSeesAFilterInTheWorktreesOwnConfig(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	dir := dto.WorkspacePath
	h.stop(ctx, dto.RunRef)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "filter.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran >> "+marker+"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, h.repo, "config", "core.repositoryformatversion", "1")
	worktreeGitOut(t, h.repo, "config", "extensions.worktreeConfig", "true")
	gitDir := worktreeGitOut(t, dir, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte("[filter \"x\"]\n\tclean = "+script+"\n\tsmudge = "+script+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("BASE\n"), 0o600); err != nil { // same size as "base\n"
		t.Fatal(err)
	}
	// The shared config names nothing: only the worktree's own does.
	shared := exec.Command("git", "-C", h.repo, "config", "--includes", "--get-regexp", `^filter\.`)
	shared.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if out, err := shared.CombinedOutput(); err == nil {
		t.Fatalf("the fixture was meant to leave the shared config clean: %s", out)
	}

	_, err := h.release(ctx, dto.RunRef, false)

	wantRunErr(t, err, http.StatusConflict, "git configuration")
	wantNoMarker(t, marker)
	if _, err := h.release(ctx, dto.RunRef, true); err != nil {
		t.Fatalf("confirmed release: %v", err)
	}
	wantNoMarker(t, marker)
}

// Forgetting a removed worktree's git record must not touch the records of other
// worktrees whose directories are merely away (an unmounted disk, a moved checkout):
// a repository-wide prune would drop them with their staged state.
func TestResumeAndReleaseForgetOnlyTheirOwnWorktreeRecord(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	a, b := h.launch(ctx, true), h.launch(ctx, true)
	h.stop(ctx, a.RunRef)
	h.stop(ctx, b.RunRef)
	away := b.WorkspacePath + ".away"
	if err := os.Rename(b.WorkspacePath, away); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(a.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(worktreeGitOut(t, h.repo, "worktree", "list", "--porcelain"), b.WorkspacePath) {
		t.Fatal("the fixture should still have git's record of the worktree that is away")
	}

	if _, err := h.resume(ctx, a.RunRef); err != nil {
		t.Fatalf("resume with its directory gone: %v", err)
	}
	if !strings.Contains(worktreeGitOut(t, h.repo, "worktree", "list", "--porcelain"), b.WorkspacePath) {
		t.Fatal("bringing a worktree back dropped the record of another one that is away")
	}
	h.stop(ctx, a.RunRef)
	if err := os.RemoveAll(a.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	if _, err := h.release(ctx, a.RunRef, false); err != nil {
		t.Fatalf("release with its directory gone: %v", err)
	}
	if !strings.Contains(worktreeGitOut(t, h.repo, "worktree", "list", "--porcelain"), b.WorkspacePath) {
		t.Fatal("releasing a session dropped the record of another worktree that is away")
	}
	if strings.Contains(h.branches(), a.WorktreeBranch) {
		t.Fatalf("the released session's branch survived: %s", h.branches())
	}
}

// A child that detached its HEAD can commit work no branch of the session holds; the
// worktree is clean and the session's branch looks merged, so only the HEAD check
// stops the release from losing it.
func TestReleaseOfADetachedWorktreeNeedsConfirmation(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	dir := dto.WorkspacePath
	worktreeGitOut(t, dir, "checkout", "-q", "--detach")
	if err := os.WriteFile(filepath.Join(dir, "detached.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, dir, "add", "detached.txt")
	worktreeGitOut(t, dir, "commit", "-qm", "detached work")
	tip := worktreeGitOut(t, dir, "rev-parse", "HEAD")
	h.stop(ctx, dto.RunRef)

	_, err := h.release(ctx, dto.RunRef, false)

	wantRunErr(t, err, http.StatusConflict, "detached")
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("an unconfirmed release removed a worktree holding detached commits: %v", statErr)
	}
	if _, err := h.release(ctx, dto.RunRef, true); err != nil {
		t.Fatalf("confirmed release: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("the confirmed release left the worktree: %v", statErr)
	}
	// The ledger names where the branch stood, so removed work can be recovered.
	events := listRunEvents(t, h.st, h.tenant, dto.RunRef)
	if last := events[len(events)-1]; !strings.Contains(last.Detail, "confirmed") || !strings.Contains(last.Detail, "(at ") {
		t.Fatalf("the release does not record the confirmation and the branch tip: %q (detached tip %s)", last.Detail, tip)
	}
}

// A child can lock its own worktree; git then needs the removal forced twice, which only
// a confirmed discard does.
func TestReleaseOfALockedWorktreeCanBeDiscarded(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	h.stop(ctx, dto.RunRef)
	gitDir := worktreeGitOut(t, dto.WorkspacePath, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gitDir, "locked"), []byte("locked by the session"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := h.release(ctx, dto.RunRef, false)
	wantRunErr(t, err, http.StatusConflict, "git keeps it")

	if _, err := h.release(ctx, dto.RunRef, true); err != nil {
		t.Fatalf("confirmed release of a locked worktree: %v", err)
	}
	if _, statErr := os.Stat(dto.WorkspacePath); !os.IsNotExist(statErr) {
		t.Fatalf("the locked worktree survived a confirmed discard: %v", statErr)
	}
}

// A worktree this node cannot reach is refused, never silently skipped: the row is
// deleted next and a skip would leak the directory with nothing that names it.
// Confirming releases the session, leaves the worktree where it is and says where.
func TestReleaseOfAnUnreachableWorktreeIsRefusedUntilConfirmed(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	h.stop(ctx, dto.RunRef)
	moved := h.repo + ".moved"
	if err := os.Rename(h.repo, moved); err != nil {
		t.Fatal(err)
	}

	_, err := h.release(ctx, dto.RunRef, false)
	wantRunErr(t, err, http.StatusConflict, "cannot be released")
	rec, _ := h.m.loadRun(ctx, h.tenant, dto.RunRef)
	if rec.String(colState) != stateStopped {
		t.Fatalf("a refused release changed the state to %q", rec.String(colState))
	}

	cleaned, err := h.release(ctx, dto.RunRef, true)
	if err != nil || cleaned.State != stateCleaned {
		t.Fatalf("confirmed release: %v %+v", err, cleaned)
	}
	events := listRunEvents(t, h.st, h.tenant, dto.RunRef)
	if last := events[len(events)-1]; !strings.Contains(last.Detail, "left in place at "+dto.WorkspacePath) {
		t.Fatalf("the release does not say where the worktree was left: %q", last.Detail)
	}
	if _, statErr := os.Stat(dto.WorkspacePath); statErr != nil {
		t.Fatalf("leaving the worktree in place removed it: %v", statErr)
	}
}

// "Merged" is judged against what the workspace has checked out now.
func TestReleaseJudgesMergedAgainstTheWorkspacesCurrentBranch(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	dir := dto.WorkspacePath
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, dir, "add", "work.txt")
	worktreeGitOut(t, dir, "commit", "-qm", "work")
	worktreeGitOut(t, h.repo, "checkout", "-q", "-b", "other")
	h.stop(ctx, dto.RunRef)

	_, err := h.release(ctx, dto.RunRef, false)
	wantRunErr(t, err, http.StatusConflict, dto.WorktreeBranch)

	worktreeGitOut(t, h.repo, "merge", "-q", "--ff-only", dto.WorktreeBranch)
	if _, err := h.release(ctx, dto.RunRef, false); err != nil {
		t.Fatalf("release after merging into the workspace's current branch: %v", err)
	}
}

// A workspace that gains read-only folders after the session was made cannot keep them
// read-only in the copy, so the session does not continue in it.
func TestResumeRefusesAWorkspaceThatBecameReadOnlyForTheCopy(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	h.stop(ctx, dto.RunRef)
	if _, err := h.m.patchWorkspaceReadOnlyFolders(ctx, h.tenant, h.ws, []string{filepath.Join(h.repo, "sub")}, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}

	_, err := h.resume(ctx, dto.RunRef)

	wantRunErr(t, err, http.StatusConflict, "read-only")
	if _, err := h.m.patchWorkspaceReadOnlyFolders(ctx, h.tenant, h.ws, []string{}, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.resume(ctx, dto.RunRef); err != nil {
		t.Fatalf("resume after the folders were released: %v", err)
	}
}

// A git that was stopped (a timeout, a cancelled request, a signal) gave no answer;
// it must not read as "the ref is not there" or "the branch is not merged". The child
// is killed mid-run, so what comes back has an exit code of -1 for git to misreport.
func TestGitRunReportsAStoppedGitAsAnErrorNotAnAnswer(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, code, _, err := gitRun(ctx, t.TempDir(), "-c", "alias.nap=!sleep 30", "nap"); err == nil {
		t.Fatalf("a git killed by its timeout answered with exit code %d", code)
	}
	// Killed by a signal with the context still alive: nothing but the exit code says so.
	if _, code, _, err := gitRun(context.Background(), t.TempDir(), "-c", "alias.k=!kill -9 $PPID", "k"); err == nil {
		t.Fatalf("a git killed by a signal answered with exit code %d", code)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if exists, err := gitRefExists(cancelled, t.TempDir(), "HEAD"); err == nil || exists {
		t.Fatalf("a cancelled git said the ref exists=%v err=%v", exists, err)
	}
}

// The option and its confirmation reach the module through the real HTTP routes, and a
// cleanup body that carries anything but an explicit true confirms nothing, as the
// endpoint has always forgiven its body.
func TestWorktreeOptionOverHTTP(t *testing.T) {
	repo := gitRepoFixture(t)
	fr := &fakeRunner{initSID: "sess-http-wt"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	if err := m.UseSessionWorktrees(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wt-http")
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "repo"}, tenantHdr(tenant))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace = %d %s", wr.code, wr.raw)
	}
	ws, _ := wr.body["workspace_ref"].(string)
	create := func(extra map[string]any) resp {
		fr.initSID = "sess-http-wt-" + model.NewID().String()
		body := map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": ws, "name": "via-http"}
		for k, v := range extra {
			body[k] = v
		}
		return h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	}

	plain := create(nil)
	if plain.code != http.StatusCreated {
		t.Fatalf("create without the option = %d %s", plain.code, plain.raw)
	}
	if _, present := plain.body["worktree_branch"]; present {
		t.Fatalf("a run without the option reports a worktree: %s", plain.raw)
	}
	if r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "worktree": true, "transport": "stream-json", "isolation": "native"}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("worktree without a workspace = %d %s, want 422", r.code, r.raw)
	}

	r := create(map[string]any{"worktree": true, "name": "wt"})
	if r.code != http.StatusCreated {
		t.Fatalf("create with the option = %d %s", r.code, r.raw)
	}
	ref, _ := r.body["run_ref"].(string)
	branch, _ := r.body["worktree_branch"].(string)
	dir, _ := r.body["workspace_path"].(string)
	if !strings.HasPrefix(branch, "olivares/") || dir == repo {
		t.Fatalf("create with the option did not make a worktree: %s", r.raw)
	}
	got := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
	if got.code != http.StatusOK || got.body["worktree_branch"] != branch {
		t.Fatalf("get = %d %s", got.code, got.raw)
	}
	if l := h.do("GET", "/v1/m/sessions/runs", admin, tenantHdr(tenant)); !strings.Contains(l.raw, branch) {
		t.Fatalf("the list does not report the worktree branch: %s", l.raw)
	}

	// Work that is not merged: the release is refused unless the confirmation is explicit.
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, dir, "add", "work.txt")
	worktreeGitOut(t, dir, "commit", "-qm", "unmerged work")
	if s := h.do("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, tenantHdr(tenant)); s.code != http.StatusOK {
		t.Fatalf("stop = %d %s", s.code, s.raw)
	}
	cleanup := "/v1/m/sessions/runs/" + ref + "/cleanup"
	for name, c := range map[string]resp{
		"no body":        h.do("POST", cleanup, admin, tenantHdr(tenant)),
		"empty object":   h.doJSON("POST", cleanup, admin, map[string]any{}, tenantHdr(tenant)),
		"another field":  h.doJSON("POST", cleanup, admin, map[string]any{"force": true}, tenantHdr(tenant)),
		"wrong type":     h.doJSON("POST", cleanup, admin, map[string]any{"discard_worktree": "yes"}, tenantHdr(tenant)),
		"explicit false": h.doJSON("POST", cleanup, admin, map[string]any{"discard_worktree": false}, tenantHdr(tenant)),
		"not an object":  h.doJSON("POST", cleanup, admin, "discard", tenantHdr(tenant)),
	} {
		if c.code != http.StatusConflict || !strings.Contains(c.raw, branch) {
			t.Fatalf("cleanup with %s = %d %s, want 409 naming the branch", name, c.code, c.raw)
		}
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a refused cleanup removed the worktree: %v", err)
	}
	if c := h.doJSON("POST", cleanup, admin, map[string]any{"discard_worktree": true}, tenantHdr(tenant)); c.code != http.StatusOK || c.body["state"] != stateCleaned {
		t.Fatalf("confirmed cleanup = %d %s", c.code, c.raw)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the confirmed cleanup left the worktree: %v", err)
	}
}
