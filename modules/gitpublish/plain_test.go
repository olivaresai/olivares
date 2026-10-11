// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7 are disclaimers of warranty: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A plain-git target answers push only: the pull-request and merge effects
// are refused with unsupported_requirement before anything is minted, and the
// push keeps the whole admitted flow.
func TestPlainGitTargetRefusesChangeEffects(t *testing.T) {
	h := newHarness(t)
	h.custody.hostKind = "git"
	ctx := context.Background()
	_, err := h.m.OpenPullRequest(ctx, h.admin(), PullRequestInput{Target: h.target.ID, OperationID: "op-pr", HeadRef: "olivares/x", Base: "main", Commit: shaCommit, Title: "t"})
	if codeOf(err) != "unsupported_requirement" {
		t.Fatalf("pull request err = %v (%s), want unsupported_requirement", err, codeOf(err))
	}
	_, err = h.m.Merge(ctx, h.admin(), MergeInput{Target: h.target.ID, OperationID: "op-m", Number: 1, ExpectedHead: shaCommit, Method: "merge"})
	if codeOf(err) != "unsupported_requirement" {
		t.Fatalf("merge err = %v (%s), want unsupported_requirement", err, codeOf(err))
	}
	if h.host.mints != 0 {
		t.Fatalf("a refused effect minted %d tokens", h.host.mints)
	}
	if _, err := h.push(h.user(), "op-1", "refs/heads/olivares/one", ""); err != nil {
		t.Fatalf("push err = %v", err)
	}
	h.balanced()
}

// A plain-git remote names no host, so a push checks both CI configuration
// paths before it is dispatched.
func TestPlainGitTargetChecksBothCIPaths(t *testing.T) {
	h := newHarness(t)
	h.custody.hostKind = "git"
	if _, err := h.push(h.user(), "op-ci", "refs/heads/olivares/ci", ""); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range h.git.checkedPaths {
		seen[p] = true
	}
	if !seen[".github/workflows"] || !seen[".gitlab-ci.yml"] {
		t.Fatalf("checked CI paths = %v, want both .github/workflows and .gitlab-ci.yml", h.git.checkedPaths)
	}
}

// The ssh transport authenticates with the binding's own key: the minted
// token carries it and the dispatched request carries the key, not a header.
func TestPushDispatchCarriesTheSSHKey(t *testing.T) {
	h := newHarness(t)
	h.host.sshTarget, h.host.mintSecret = true, "-----BEGIN "+"OPENSSH PRIVATE KEY-----"
	if _, err := h.push(h.user(), "op-ssh", "refs/heads/olivares/ssh", ""); err != nil {
		t.Fatal(err)
	}
	req := h.git.lastReq
	if req.Scheme != "ssh" || !req.Header.IsZero() || req.Key.Reveal() != h.host.mintSecret {
		t.Fatalf("request = scheme %q header %v key %v, want ssh with the minted key and no header", req.Scheme, req.Header, req.Key)
	}
	h.balanced()
}

// A transport that cannot even start (no ssh client, no key file) is a proven
// no-dispatch, never an uncertain intent that blocks the ref scope.
func TestPlainGitTransportStartRefusalIsNotUncertain(t *testing.T) {
	h := newHarness(t)
	h.custody.hostKind = "git"
	h.git.localErr = gp.ErrTransportStart
	rec, err := h.push(h.user(), "op-start", "refs/heads/olivares/x", "")
	if err != nil {
		t.Fatalf("push err = %v", err)
	}
	if rec.Intent.State != StateNotDispatched || rec.Intent.Reason != "destination_refused" {
		t.Fatalf("intent = %s %s, want not_dispatched destination_refused", rec.Intent.State, rec.Intent.Reason)
	}
}

// plainCustody approves one plain-git binding and opens the real adapter.
type plainCustody struct {
	host  gp.Host
	local string
}

func (c plainCustody) CredentialBinding(context.Context, model.TenantID, model.ID, string) (CredentialBinding, error) {
	return CredentialBinding{ID: "cb1", Version: 1, Host: "git", AllowedOwners: []string{"acme"}}, nil
}

func (c plainCustody) RepositoryBinding(context.Context, model.TenantID, model.ID, string) (RepositoryBinding, error) {
	return RepositoryBinding{ID: "rb1", Version: 1, RepoID: "example.test/acme/widgets", Owner: "acme", Name: "widgets", LocalPath: c.local}, nil
}

func (c plainCustody) OpenHost(context.Context, model.TenantID, CredentialBinding, RepositoryBinding) (gp.Host, error) {
	return c.host, nil
}

// plainFixture is one plain-git module over the real adapter and executor.
type plainFixture struct {
	m              *Module
	c              Caller
	tg             Target
	commit, tree   string // the published change
	other, otherTr string // a later change, for lease checks
	remote         string // the actual receive repository
}

// plainGitModule drives the real module over the real closed executor and the
// real plain-git adapter, with the file test transport standing in for the
// network: everything except the dial is production code. defaultBranch names
// the remote's HEAD and mergeBase its integration branch.
func plainGitModule(t *testing.T, defaultBranch, mergeBase string) plainFixture {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	abs, _ := filepath.Abs(git)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	x, err := gp.NewExecutor(abs, home)
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
			"GIT_AUTHOR_DATE=2026-10-06T10:00:00Z", "GIT_COMMITTER_DATE=2026-10-06T10:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	server, remote, work := filepath.Join(dir, "server.git"), filepath.Join(dir, "remote.git"), filepath.Join(dir, "work")
	ctx := context.Background()
	if err := x.InitManaged(ctx, server); err != nil {
		t.Fatal(err)
	}
	run(dir, "init", "-q", "--bare", "-b", defaultBranch, remote)
	run(dir, "init", "-q", "-b", defaultBranch, work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "-A")
	run(work, "commit", "-q", "-m", "base")
	base := run(work, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "commit", "-q", "-am", "change")
	commit, tree := run(work, "rev-parse", "HEAD"), run(work, "rev-parse", "HEAD^{tree}")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "commit", "-q", "-am", "later")
	other, otherTree := run(work, "rev-parse", "HEAD"), run(work, "rev-parse", "HEAD^{tree}")
	// The managed server repository holds both commits; the remote starts at
	// base. Feeding the server from a session is #420, not this change.
	run(work, "push", "-q", server, "HEAD:refs/heads/feed", base+":refs/heads/"+defaultBranch)
	args := []string{"push", "-q", remote, base + ":refs/heads/" + defaultBranch}
	if mergeBase != defaultBranch {
		args = append(args, base+":refs/heads/"+mergeBase)
	}
	run(work, args...)

	adapter, err := gp.NewPlainGit(gp.PlainGitConfig{Remote: "file://" + remote, Credential: gp.NewSecret("test-credential")}, x)
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{Custody: plainCustody{host: adapter, local: server}, Git: x, Authority: &fakeAuthority{deny: map[model.ID]error{}}, DispatchTimeout: 20 * time.Second})
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	c := Caller{Tenant: model.SystemTenantID, Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), AAL: 3}}
	tg, err := m.CreateTarget(ctx, c, TargetInput{Workspace: model.NewID(), CredentialBinding: "cb1", RepositoryBinding: "rb1", PushPrefix: "olivares/", MergeBases: []string{mergeBase}})
	if err != nil {
		t.Fatal(err)
	}
	return plainFixture{m: m, c: c, tg: tg, commit: commit, tree: tree, other: other, otherTr: otherTree, remote: remote}
}

func TestPlainGitTargetPublishesAnAgentBranch(t *testing.T) {
	f := plainGitModule(t, "main", "main")
	rec, err := f.m.Push(context.Background(), f.c, PushInput{Target: f.tg.ID, OperationID: "op-1", Ref: "refs/heads/olivares/agent", Commit: f.commit, Tree: f.tree})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Intent.State != StateApplied || !rec.Intent.Observed.Present || rec.Intent.Observed.SHA != f.commit {
		t.Fatalf("intent = %s observed %+v, want applied with the commit on the remote", rec.Intent.State, rec.Intent.Observed)
	}
	// A replay by operation id returns the stored receipt.
	replay, err := f.m.Push(context.Background(), f.c, PushInput{Target: f.tg.ID, OperationID: "op-1", Ref: "refs/heads/olivares/agent", Commit: f.commit, Tree: f.tree})
	if err != nil || replay.Intent.State != StateApplied {
		t.Fatalf("replay = %s %v", replay.Intent.State, err)
	}
}

func TestPlainGitTargetRefusesTheDefaultBranchByName(t *testing.T) {
	// The remote's default branch sits under the push prefix and is not a
	// merge base: only the default-branch rule refuses it, by name.
	f := plainGitModule(t, "olivares/prod", "release")
	_, err := f.m.Push(context.Background(), f.c, PushInput{Target: f.tg.ID, OperationID: "op-def", Ref: "refs/heads/olivares/prod", Commit: f.commit, Tree: f.tree})
	if codeOf(err) != "default_branch_refused" {
		t.Fatalf("err = %v (%s), want default_branch_refused", err, codeOf(err))
	}
}

func TestPlainGitTargetLeaseHolds(t *testing.T) {
	f := plainGitModule(t, "main", "main")
	ctx := context.Background()
	if _, err := f.m.Push(ctx, f.c, PushInput{Target: f.tg.ID, OperationID: "op-a", Ref: "refs/heads/olivares/lease", Commit: f.commit, Tree: f.tree}); err != nil {
		t.Fatal(err)
	}
	// The branch now exists at the first commit: an empty lease requires
	// absence, so a later commit under a new operation id is a stale lease.
	_, err := f.m.Push(ctx, f.c, PushInput{Target: f.tg.ID, OperationID: "op-b", Ref: "refs/heads/olivares/lease", Commit: f.other, Tree: f.otherTr})
	if codeOf(err) != "stale_lease" {
		t.Fatalf("err = %v (%s), want stale_lease", err, codeOf(err))
	}
}
