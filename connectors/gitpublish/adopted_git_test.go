// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

// Tests adopted from the independent construction read (READ-J10-S3-
// CONSTRUCTION.md, be056547), renamed so the reader's overlay files still
// load beside them. The dot-git test checks that the commit never lands on
// the redirected destination (the reader's version seeded that remote with
// the base first, so its "ref is empty" check could not pass either way).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A `.git` gitfile planted inside the managed bare repository makes git
// discover ANOTHER repository, whose config the allowlist never reads.
func TestAdoptedDotGitFileInManagedRepoRefused(t *testing.T) {
	f := newGitFixture(t)
	alt := filepath.Join(filepath.Dir(f.server), "alt.git")
	if err := f.x.InitManaged(context.Background(), alt); err != nil {
		t.Fatal(err)
	}
	run(t, f.server, "push", "-q", "file://"+alt, "refs/heads/*:refs/heads/*")
	// The attacker-side remote mirrors the target's state so the lease passes there.
	run(t, f.server, "push", "-q", "file://"+f.evil, f.base+":refs/heads/main")
	run(t, alt, "config", "url.file://"+f.evil+".pushInsteadOf", "file://"+f.target)
	if err := os.WriteFile(filepath.Join(f.server, ".git"), []byte("gitdir: "+alt+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.x.CheckManagedConfig(context.Background(), f.server); err != nil {
		t.Logf("CheckManagedConfig refused: %v", err)
	} else {
		t.Logf("CheckManagedConfig(server) = nil although git will read %s/config", alt)
	}
	res, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base))
	evil, target := f.refAt(f.evil, "refs/heads/main"), f.refAt(f.target, "refs/heads/main")
	t.Logf("push = %+v err=%v; evil main=%q target main=%q (commit %s)", res, err, evil, target, f.commit)
	if evil == f.commit {
		t.Fatalf("the push (and its http.extraHeader) went to the pushInsteadOf destination of the redirected repository")
	}
	if !errors.Is(err, ErrRepositoryConfig) {
		t.Fatalf("err = %v, want ErrRepositoryConfig for a redirected repository", err)
	}
}

// Inherited GIT_*, trace, proxy and config variables never reach git; a
// hostile HOME/.gitconfig or XDG config in the executor home is ignored.
func TestAdoptedInheritedVariablesAndHomeConfigIgnored(t *testing.T) {
	f := newGitFixture(t)
	tr := t.TempDir()
	hostileCfg := filepath.Join(tr, "hostile.gitconfig")
	cfg := "[url \"file://" + f.evil + "\"]\n\tpushInsteadOf = file://" + f.target + "\n"
	if err := os.WriteFile(hostileCfg, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// The engine-owned home is supposed to be empty; defence in depth must hold if it is not.
	if err := os.WriteFile(filepath.Join(f.x.home, ".gitconfig"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.x.home, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.x.home, "git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"GIT_TRACE": filepath.Join(tr, "trace"), "GIT_TRACE_CURL": filepath.Join(tr, "curl"), "GIT_TRACE2": filepath.Join(tr, "trace2"),
		"GIT_TRACE_PACKET": filepath.Join(tr, "packet"), "GIT_CURL_VERBOSE": "1", "GIT_TRACE_REDACT": "0",
		"GIT_CONFIG_SYSTEM": hostileCfg, "GIT_CONFIG_GLOBAL": hostileCfg, "GIT_CONFIG_NOSYSTEM": "0",
		"GIT_EXEC_PATH": tr, "GIT_SSH_COMMAND": "/bin/false", "GIT_ASKPASS": "/bin/false", "SSH_ASKPASS": "/bin/false",
		"GIT_OBJECT_DIRECTORY": tr, "GIT_ALTERNATE_OBJECT_DIRECTORIES": tr, "GIT_NAMESPACE": "x", "GIT_WORK_TREE": tr,
		"http_proxy": "http://127.0.0.1:9", "https_proxy": "http://127.0.0.1:9", "ALL_PROXY": "http://127.0.0.1:9",
		"GIT_SSL_NO_VERIFY": "1", "GIT_ALLOW_PROTOCOL": "file:ext", "GIT_PROTOCOL_FROM_USER": "1", "GIT_CEILING_DIRECTORIES": "",
	} {
		t.Setenv(k, v)
	}
	res, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base))
	// Check the trace files BEFORE refAt: refAt runs git with the test's own
	// (tainted) environment and would write them itself.
	for _, n := range []string{"trace", "curl", "trace2", "packet"} {
		if _, err := os.Stat(filepath.Join(tr, n)); err == nil {
			t.Errorf("git wrote trace output %s from an inherited variable", n)
		}
	}
	if err != nil || res.Class != Applied {
		t.Fatalf("push = %+v %v", res, err)
	}
	for _, k := range []string{"GIT_TRACE", "GIT_TRACE2", "GIT_TRACE_CURL", "GIT_TRACE_PACKET", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_WORK_TREE", "GIT_NAMESPACE", "GIT_EXEC_PATH"} {
		os.Unsetenv(k)
	}
	if f.refAt(f.evil, "refs/heads/main") != "" || f.refAt(f.target, "refs/heads/main") != f.commit {
		t.Fatal("inherited or home configuration redirected the push")
	}
}

// A replace ref in the managed repository makes CommitTree and
// WorkflowsChanged read a substitute commit, while git push sends the real
// object: the tree binding and the workflow refusal check a different commit.
func TestAdoptedReplaceRefsDoNotSubstituteCheckedCommit(t *testing.T) {
	f := newGitFixture(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "w")
	run(t, dir, "clone", "-q", f.server, work)
	run(t, work, "checkout", "-q", f.commit)
	if err := os.WriteFile(filepath.Join(work, ".github", "workflows", "ci.yml"), []byte("on: [push]\njobs: {x: {runs-on: self-hosted, steps: [{run: 'env'}]}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "commit", "-q", "-am", "workflow change")
	wf := run(t, work, "rev-parse", "HEAD")
	wfTree := run(t, work, "rev-parse", "HEAD^{tree}")
	run(t, work, "push", "-q", f.server, "HEAD:refs/heads/wf")
	// Replace the workflow commit by the benign commit inside the managed repository.
	run(t, f.server, "update-ref", "refs/replace/"+wf, f.commit)

	tree, err := f.x.CommitTree(context.Background(), f.server, wf)
	changed, werr := f.x.WorkflowsChanged(context.Background(), f.server, f.base, wf)
	t.Logf("CommitTree(wf)=%s (real %s, benign %s) err=%v; WorkflowsChanged(base, wf)=%v err=%v", tree, wfTree, f.tree, err, changed, werr)
	res, perr := f.x.Push(context.Background(), PushRequest{RepoPath: f.server, URL: "file://" + f.target, Scheme: "file", Ref: "refs/heads/main", ExpectedOld: f.base, Commit: wf})
	landed := f.refAt(f.target, "refs/heads/main")
	realTree := run(t, f.target, "rev-parse", landed+"^{tree}")
	t.Logf("push = %+v %v; target main = %s with tree %s", res, perr, landed, realTree)
	if tree != wfTree || !changed {
		t.Fatalf("local checks read the replacement: tree %s != pushed tree %s, workflow change detected=%v", tree, realTree, changed)
	}
}

func TestClosedEnvironmentAndGuardAreExact(t *testing.T) {
	x := &Executor{git: "/usr/bin/git", home: "/srv/home"}
	env := x.closedEnv(Secret{}, "/srv/repo.git")
	sort.Strings(env)
	want := []string{
		"GIT_CEILING_DIRECTORIES=/srv", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_DIR=/srv/repo.git",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_PROTOCOL_FROM_USER=0", "GIT_TERMINAL_PROMPT=0", "HOME=/srv/home", "LC_ALL=C",
		"PATH=/usr/bin", "XDG_CONFIG_HOME=/srv/home",
	}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("closed environment =\n%s\nwant\n%s", strings.Join(env, "\n"), strings.Join(want, "\n"))
	}
	withHeader := x.closedEnv(NewSecret("Authorization: Basic eA=="), "/srv/repo.git")
	if len(withHeader) != len(want)+3 || withHeader[len(withHeader)-3] != "GIT_CONFIG_COUNT=1" || withHeader[len(withHeader)-2] != "GIT_CONFIG_KEY_0=http.extraHeader" {
		t.Fatalf("credential variables = %v", withHeader[len(want):])
	}
	g := strings.Join(guard("https"), " ")
	for _, w := range []string{"-c core.hooksPath=/dev/null", "-c credential.helper=", "-c core.askPass=", "-c http.followRedirects=false", "-c protocol.allow=never", "-c protocol.https.allow=always"} {
		if !strings.Contains(g, w) {
			t.Fatalf("guard %q lacks %q", g, w)
		}
	}
}

func TestRedirectingRepositoryStateRefused(t *testing.T) {
	for name, plant := range map[string]func(f *gitFixture){
		"alternates": func(f *gitFixture) {
			writeFile(t, filepath.Join(f.server, "objects", "info", "alternates"), filepath.Join(f.evil, "objects")+"\n")
		},
		"grafts":      func(f *gitFixture) { writeFile(t, filepath.Join(f.server, "info", "grafts"), f.commit+"\n") },
		"commondir":   func(f *gitFixture) { writeFile(t, filepath.Join(f.server, "commondir"), f.evil+"\n") },
		"dot git dir": func(f *gitFixture) { _ = os.Mkdir(filepath.Join(f.server, ".git"), 0o755) },
		"replace ref": func(f *gitFixture) { run(t, f.server, "update-ref", "refs/replace/"+f.commit, f.base) },
		"packed replace ref": func(f *gitFixture) {
			run(t, f.server, "update-ref", "refs/replace/"+f.commit, f.base)
			run(t, f.server, "pack-refs", "--all")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newGitFixture(t)
			plant(f)
			if _, err := f.x.Push(context.Background(), f.req("refs/heads/main", f.base)); !errors.Is(err, ErrRepositoryConfig) {
				t.Fatalf("push err = %v, want ErrRepositoryConfig", err)
			}
			tree, err := f.x.CommitTree(context.Background(), f.server, f.commit)
			if strings.Contains(name, "replace") {
				// Reads ignore replace refs and see the real object.
				if err != nil || tree != f.tree {
					t.Fatalf("commit tree under a replace ref = %q %v, want the real tree %s", tree, err, f.tree)
				}
			} else if !errors.Is(err, ErrRepositoryConfig) {
				t.Fatalf("commit tree err = %v, want ErrRepositoryConfig", err)
			}
			if f.refAt(f.target, "refs/heads/main") != f.base {
				t.Fatal("a refused push reached the target")
			}
		})
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
