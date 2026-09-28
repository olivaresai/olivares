// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitFixture is a server repository (the managed, bare push source), a
// target remote and an attacker remote, all local bare repositories.
type gitFixture struct {
	t                    *testing.T
	x                    *Executor
	server, target, evil string
	commit, tree, base   string
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_AUTHOR_DATE=2026-09-27T10:00:00Z", "GIT_COMMITTER_DATE=2026-09-27T10:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	abs, _ := filepath.Abs(p)
	return abs
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	dir := t.TempDir()
	f := &gitFixture{t: t, server: filepath.Join(dir, "server.git"), target: filepath.Join(dir, "target.git"), evil: filepath.Join(dir, "evil.git")}
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	x, err := NewExecutor(gitPath(t), home)
	if err != nil {
		t.Fatal(err)
	}
	f.x = x
	ctx := context.Background()
	for _, r := range []string{f.server, f.target, f.evil} {
		if err := x.InitManaged(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(dir, "work")
	run(t, dir, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-A")
	run(t, work, "commit", "-q", "-m", "base")
	f.base = run(t, work, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "commit", "-q", "-am", "change")
	f.commit = run(t, work, "rev-parse", "HEAD")
	f.tree = run(t, work, "rev-parse", "HEAD^{tree}")
	// The server repository receives the content; the target starts at base.
	run(t, work, "push", "-q", f.server, "HEAD:refs/heads/work", f.base+":refs/heads/main")
	run(t, work, "push", "-q", f.target, f.base+":refs/heads/main")
	return f
}

func (f *gitFixture) req(ref, old string) PushRequest {
	return PushRequest{RepoPath: f.server, URL: "file://" + f.target, Scheme: "file", Ref: ref, ExpectedOld: old, Commit: f.commit}
}

func (f *gitFixture) refAt(repo, ref string) string {
	cmd := exec.Command("git", "--git-dir", repo, "rev-parse", "--verify", "--quiet", ref)
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func TestPushWithLeaseLandsExactCommit(t *testing.T) {
	f := newGitFixture(t)
	tree, err := f.x.CommitTree(context.Background(), f.server, f.commit)
	if err != nil || tree != f.tree {
		t.Fatalf("tree = %q %v, want %q", tree, err, f.tree)
	}
	res, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base))
	if err != nil || res.Class != Applied {
		t.Fatalf("push = %+v %v", res, err)
	}
	if got := f.refAt(f.target, "refs/heads/main"); got != f.commit {
		t.Fatalf("target main = %s, want %s", got, f.commit)
	}
}

func TestPushStaleLeaseRejected(t *testing.T) {
	f := newGitFixture(t)
	res, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.commit)) // wrong expected old
	if err != nil || res.Class != Rejected || res.Reason != "stale_lease" {
		t.Fatalf("push = %+v %v, want rejected stale_lease", res, err)
	}
	if got := f.refAt(f.target, "refs/heads/main"); got != f.base {
		t.Fatalf("target moved to %s", got)
	}
}

func TestPushNewRefRequiresAbsence(t *testing.T) {
	f := newGitFixture(t)
	res, err := f.x.Push(context.Background(), f.req("refs/heads/main", ""))
	if err != nil || res.Class != Rejected || res.Reason != "stale_lease" {
		t.Fatalf("push onto an existing ref with an empty lease = %+v %v", res, err)
	}
	res, err = f.x.Push(context.Background(), f.req("refs/heads/olivares/new", ""))
	if err != nil || res.Class != Applied {
		t.Fatalf("new ref = %+v %v", res, err)
	}
}

func TestRepoLocalPushInsteadOfRefused(t *testing.T) {
	f := newGitFixture(t)
	run(t, f.server, "config", "url.file://"+f.evil+".pushInsteadOf", "file://"+f.target)
	_, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base))
	if !errors.Is(err, ErrRepositoryConfig) {
		t.Fatalf("err = %v, want ErrRepositoryConfig", err)
	}
	if f.refAt(f.evil, "refs/heads/main") != "" || f.refAt(f.target, "refs/heads/main") != f.base {
		t.Fatal("a refused push reached a remote")
	}
	for _, kv := range [][2]string{{"credential.helper", "store"}, {"include.path", "/etc/passwd"}, {"core.hooksPath", "/x"}, {"http.extraHeader", "X: y"}} {
		f2 := newGitFixture(t)
		run(t, f2.server, "config", kv[0], kv[1])
		if _, err := f2.x.Push(context.Background(), f2.req("refs/heads/main", f2.base)); !errors.Is(err, ErrRepositoryConfig) {
			t.Fatalf("%s: err = %v", kv[0], err)
		}
	}
}

func TestLocalPrePushHookNeverRuns(t *testing.T) {
	f := newGitFixture(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(f.server, "hooks", "pre-push")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nenv > "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base))
	if err != nil || res.Class != Applied {
		t.Fatalf("push = %+v %v", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's pre-push hook ran")
	}
}

func TestInheritedConfigAndTransportIgnored(t *testing.T) {
	f := newGitFixture(t)
	t.Setenv("GIT_CONFIG_PARAMETERS", "'url.file://"+f.evil+".insteadof'='file://"+f.target+"'")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+f.evil+".pushInsteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "file://"+f.target)
	t.Setenv("GIT_DIR", f.evil)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	res, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base))
	if err != nil || res.Class != Applied {
		t.Fatalf("push = %+v %v", res, err)
	}
	if f.refAt(f.evil, "refs/heads/main") != "" {
		t.Fatal("inherited configuration redirected the push")
	}
	if f.refAt(f.target, "refs/heads/main") != f.commit {
		t.Fatal("the admitted target did not receive the commit")
	}
}

func TestPushCredentialOnlyInCommandScopeEnv(t *testing.T) {
	f := newGitFixture(t)
	dir := t.TempDir()
	logf := filepath.Join(dir, "log")
	shim := filepath.Join(dir, "git")
	script := "#!/bin/sh\n{ echo ARGV \"$@\"; /usr/bin/env | /usr/bin/sed 's/^/ENV /'; } >> " + logf + "\nexec " + gitPath(t) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	x, err := NewExecutor(shim, f.x.home)
	if err != nil {
		t.Fatal(err)
	}
	r := f.req("refs/heads/main", f.base)
	r.Header = NewSecret("Authorization: Basic U0VOVElORUw=")
	if _, err := x.Push(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logf)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ARGV") && strings.Contains(line, "U0VOVElORUw") {
			t.Fatalf("credential in argv: %s", line)
		}
		if strings.HasPrefix(line, "ENV ") {
			kv := strings.TrimPrefix(line, "ENV ")
			if strings.Contains(kv, "U0VOVElORUw") && !strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") {
				t.Fatalf("credential outside command-scope config: %s", kv)
			}
			if strings.HasPrefix(kv, "GIT_DIR=") && kv != "GIT_DIR="+f.server {
				t.Fatalf("git is not pinned to the managed repository: %s", kv)
			}
			for _, bad := range []string{"GIT_CONFIG_PARAMETERS=", "HTTPS_PROXY=", "GIT_ASKPASS=/", "SSH_ASKPASS=/"} {
				if strings.HasPrefix(kv, bad) {
					t.Fatalf("inherited or unsafe variable reached git: %s", kv)
				}
			}
		}
	}
	if !strings.Contains(string(b), "ENV GIT_CONFIG_VALUE_0=Authorization: Basic U0VOVElORUw=") {
		t.Fatal("the credential header was not passed as command-scope config")
	}
}

func TestPushSchemeMustBeAdmitted(t *testing.T) {
	f := newGitFixture(t)
	r := f.req("refs/heads/main", f.base)
	r.Scheme = "https"
	if _, err := f.x.Push(context.Background(), r); !errors.Is(err, ErrDestination) {
		t.Fatalf("err = %v, want ErrDestination", err)
	}
	r = f.req("refs/tags/v1", f.base)
	if _, err := f.x.Push(context.Background(), r); !errors.Is(err, ErrDestination) {
		t.Fatalf("tag push err = %v, want ErrDestination", err)
	}
}

func TestWorkflowFileChangeDetected(t *testing.T) {
	f := newGitFixture(t)
	changed, err := f.x.WorkflowsChanged(context.Background(), f.server, f.base, f.commit)
	if err != nil || changed {
		t.Fatalf("a commit that leaves .github/workflows alone: %v %v", changed, err)
	}
	dir := t.TempDir()
	work := filepath.Join(dir, "w")
	run(t, dir, "clone", "-q", f.server, work)
	run(t, work, "checkout", "-q", f.commit)
	if err := os.WriteFile(filepath.Join(work, ".github", "workflows", "ci.yml"), []byte("on: [push, pull_request]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "commit", "-q", "-am", "ci")
	wf := run(t, work, "rev-parse", "HEAD")
	run(t, work, "push", "-q", f.server, "HEAD:refs/heads/wf")
	changed, err = f.x.WorkflowsChanged(context.Background(), f.server, f.commit, wf)
	if err != nil || !changed {
		t.Fatalf("workflow change not detected: %v %v", changed, err)
	}
}

func TestPorcelainClassification(t *testing.T) {
	ref := "refs/heads/main"
	cases := []struct {
		out    string
		class  Class
		reason string
	}{
		{"To x\n \trefs/heads/w:refs/heads/main\ta..b\nDone\n", Applied, ""},
		{"To x\n*\tc:refs/heads/main\t[new branch]\nDone\n", Applied, ""},
		{"To x\n=\tc:refs/heads/main\t[up to date]\nDone\n", Applied, "up_to_date"},
		{"To x\n!\tc:refs/heads/main\t[rejected] (stale info)\nDone\n", Rejected, "stale_lease"},
		{"To x\n!\tc:refs/heads/main\t[remote rejected] (refusing to allow a GitHub App to create or update workflow `.github/workflows/ci.yml` without `workflows` permission)\n", Rejected, "workflow_permission_required"},
		{"To x\n!\tc:refs/heads/main\t[remote rejected] (protected branch hook declined)\n", Rejected, "remote_rejected"},
		{"To x\n!\tc:refs/heads/main\t[remote failure] (remote failed to report status)\n", Ambiguous, "remote_failure"},
		{"", Ambiguous, "no_status"},
	}
	for _, c := range cases {
		r := classifyPorcelain(c.out, ref)
		if r.Class != c.class || r.Reason != c.reason {
			t.Fatalf("%q => %+v, want %v %s", c.out, r, c.class, c.reason)
		}
	}
}

// N5: a push killed by its deadline is a transport ambiguity, not no_status.
func TestPushKilledByDeadlineIsTransport(t *testing.T) {
	f := newGitFixture(t)
	dir := t.TempDir()
	shim := filepath.Join(dir, "git")
	script := "#!/bin/sh\ncase \" $* \" in *\" push \"*) exec /bin/sleep 5;; esac\nexec " + gitPath(t) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	x, err := NewExecutor(shim, f.x.home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res, err := x.Push(ctx, f.req("refs/heads/main", f.base))
	if err != nil || res.Class != Ambiguous || res.Reason != "transport" {
		t.Fatalf("killed push = %+v %v, want ambiguous transport", res, err)
	}
}
