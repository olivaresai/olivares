// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The session worktree's diff (K4.A2), over the real routes: a session opened at the
// work a handoff names shows what that branch holds against where it began.

func TestWorktreeDiffFileHonorsWorkspaceReadLimit(t *testing.T) {
	repo := gitRepoFixture(t)
	old := strings.Repeat("a", 8192)
	newText := strings.Repeat("b", 8192)
	multi := strings.Repeat("c", 1023) + "€" + strings.Repeat("d", 8192)
	for name, text := range map[string]string{"changed.txt": old, "deleted.txt": old, "utf8.txt": multi} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	worktreeGitOut(t, repo, "add", ".")
	worktreeGitOut(t, repo, "commit", "-qm", "large base files")
	sha := branchWithCommit(t, repo, "work/limited", map[string]string{
		"changed.txt": newText, "added.txt": newText, "utf8.txt": multi + "end",
	}, "deleted.txt")
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{initSID: "sess-limit"}), WithCredentialSource(staticCred()))
	if err := m.UseSessionWorktrees(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wt-limit")
	ws := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": repo, "name": "limited", "max_read_bytes": 1024,
	}, tenantHdr(tenant))
	if ws.code != http.StatusCreated {
		t.Fatalf("workspace = %d %s", ws.code, ws.raw)
	}
	run := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant),
		"transport":            "stream-json", "permission_mode": "default", "isolation": "native",
		"workspace_ref": ws.body["workspace_ref"], "worktree": true, "worktree_from": sha,
	}, tenantHdr(tenant))
	if run.code != http.StatusCreated {
		t.Fatalf("run = %d %s", run.code, run.raw)
	}
	for name, want := range map[string][2]string{
		"changed.txt": {old[:1024], newText[:1024]},
		"added.txt":   {"", newText[:1024]},
		"deleted.txt": {old[:1024], ""},
		"utf8.txt":    {multi[:1023], multi[:1023]},
	} {
		t.Run(name, func(t *testing.T) {
			r := h.do("GET", fmt.Sprintf("/v1/m/sessions/runs/%s/diff/file?path=%s", run.body["run_ref"], name), admin, tenantHdr(tenant))
			original, _ := r.body["original"].(string)
			modified, _ := r.body["modified"].(string)
			if r.code != http.StatusOK || original != want[0] || modified != want[1] || r.body["truncated"] != true || r.body["binary"] != false || !utf8.ValidString(original) || !utf8.ValidString(modified) {
				t.Fatalf("diff = %d, original bytes=%d modified bytes=%d truncated=%v binary=%v; want %d/%d bytes, truncated text", r.code, len(original), len(modified), r.body["truncated"], r.body["binary"], len(want[0]), len(want[1]))
			}
		})
	}
}

func TestWorktreeDiffShowsTheBranchAgainstItsBase(t *testing.T) {
	repo := gitRepoFixture(t)
	big := strings.Repeat("0123456789abcdef\n", 5000) // 85 000 bytes: past the 64 KiB read cap
	many := map[string]string{"big.txt": big}
	for i := 0; i < worktreeDiffMaxFiles+1; i++ {
		many[fmt.Sprintf("many/f%03d.txt", i)] = "x\n"
	}
	handed := branchWithCommit(t, repo, "work/handoff",
		map[string]string{"feature.txt": "handed over\n", "README": "base\nmore\n", "blob.bin": "a\x00b"}, "sub/inner.txt")
	crowded := branchWithCommit(t, repo, "work/crowded", many)
	mainSHA := worktreeGitOut(t, repo, "rev-parse", "main")

	fr := &fakeRunner{initSID: "sess-diff"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	if err := m.UseSessionWorktrees(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wt-diff")
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "repo"}, tenantHdr(tenant))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace = %d %s", wr.code, wr.raw)
	}
	ws, _ := wr.body["workspace_ref"].(string)
	create := func(extra map[string]any) resp {
		body := map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": ws}
		for k, v := range extra {
			body[k] = v
		}
		return h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	}
	open := func(from string) (ref, dir string) {
		r := create(map[string]any{"worktree": true, "worktree_from": from})
		if r.code != http.StatusCreated {
			t.Fatalf("create from %q = %d %s", from, r.code, r.raw)
		}
		ref, _ = r.body["run_ref"].(string)
		dir, _ = r.body["workspace_path"].(string)
		return ref, dir
	}
	diff := func(ref string) resp {
		return h.do("GET", "/v1/m/sessions/runs/"+ref+"/diff", admin, tenantHdr(tenant))
	}
	file := func(ref, p string) resp {
		return h.do("GET", "/v1/m/sessions/runs/"+ref+"/diff/file?path="+url.QueryEscape(p), admin, tenantHdr(tenant))
	}

	ref, dir := open(handed)
	if got := worktreeGitOut(t, dir, "rev-parse", "HEAD"); got != handed {
		t.Fatalf("the session opened at %s, want the handoff's %s", got, handed)
	}

	t.Run("lists what the branch changed, with its status", func(t *testing.T) {
		r := diff(ref)
		if r.code != http.StatusOK || r.body["base"] != mainSHA || r.body["head"] != handed || r.body["truncated"] != false {
			t.Fatalf("diff = %d %s, want base %s head %s", r.code, r.raw, mainSHA, handed)
		}
		got := map[string]string{}
		files, _ := r.body["files"].([]any)
		for _, f := range files {
			e, _ := f.(map[string]any)
			got[fmt.Sprint(e["path"])] = fmt.Sprint(e["status"])
		}
		want := map[string]string{"feature.txt": "added", "README": "modified", "sub/inner.txt": "deleted", "blob.bin": "added"}
		if len(got) != len(want) {
			t.Fatalf("files = %v, want %v", got, want)
		}
		for p, s := range want {
			if got[p] != s {
				t.Fatalf("files = %v, want %v", got, want)
			}
		}
	})

	t.Run("gives a path's text at the base and at the branch tip", func(t *testing.T) {
		for p, want := range map[string][3]string{
			"feature.txt":   {"added", "", "handed over\n"},
			"README":        {"modified", "base\n", "base\nmore\n"},
			"sub/inner.txt": {"deleted", "inner\n", ""},
		} {
			r := file(ref, p)
			if r.code != http.StatusOK || r.body["status"] != want[0] || r.body["original"] != want[1] ||
				r.body["modified"] != want[2] || r.body["binary"] != false || r.body["truncated"] != false {
				t.Fatalf("%s = %d %s, want %v", p, r.code, r.raw, want)
			}
		}
		if r := file(ref, "blob.bin"); r.code != http.StatusOK || r.body["binary"] != true || r.body["modified"] != "" {
			t.Fatalf("binary file = %d %s", r.code, r.raw)
		}
	})

	t.Run("refuses what is not a file of the repository", func(t *testing.T) {
		for name, c := range map[string]struct {
			path string
			code int
		}{
			"no path":          {"", http.StatusBadRequest},
			"parent":           {"../outside", http.StatusBadRequest},
			"absolute":         {"/etc/passwd", http.StatusBadRequest},
			"not clean":        {"sub//inner.txt", http.StatusBadRequest},
			"dot":              {".", http.StatusBadRequest},
			"a folder":         {"sub", http.StatusBadRequest},
			"absent":           {"nothing.txt", http.StatusNotFound},
			"a revision":       {"HEAD", http.StatusNotFound},
			"an object syntax": {"README^{tree}", http.StatusNotFound},
		} {
			if r := file(ref, c.path); r.code != c.code {
				t.Errorf("%s: %q = %d %s, want %d", name, c.path, r.code, r.raw, c.code)
			}
		}
	})

	t.Run("shows committed work only, including the session's own commits", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("not committed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if r := diff(ref); strings.Contains(r.raw, "scratch.txt") {
			t.Fatalf("an uncommitted file is in the diff: %s", r.raw)
		}
		worktreeGitOut(t, dir, "add", "scratch.txt")
		worktreeGitOut(t, dir, "commit", "-qm", "the receiver's own work")
		own := worktreeGitOut(t, dir, "rev-parse", "HEAD")
		if r := diff(ref); r.body["head"] != own || !strings.Contains(r.raw, "scratch.txt") {
			t.Fatalf("the session's commit is not in the diff: %s", r.raw)
		}
	})

	t.Run("says when a listing or a text is cut", func(t *testing.T) {
		crowdedRef, _ := open(crowded)
		r := diff(crowdedRef)
		files, _ := r.body["files"].([]any)
		if r.code != http.StatusOK || len(files) != worktreeDiffMaxFiles || r.body["truncated"] != true {
			t.Fatalf("a branch of %d files listed %d (truncated=%v)", len(many), len(files), r.body["truncated"])
		}
		r = file(crowdedRef, "big.txt")
		text, _ := r.body["modified"].(string)
		if r.code != http.StatusOK || r.body["truncated"] != true || len(text) != worktreeGitOutputLimit {
			t.Fatalf("an 85 000-byte file came back as %d bytes (truncated=%v, %d)", len(text), r.body["truncated"], r.code)
		}
	})

	t.Run("a session without a worktree, an unknown one and another tenant have none", func(t *testing.T) {
		plain := create(nil)
		plainRef, _ := plain.body["run_ref"].(string)
		if r := diff(plainRef); r.code != http.StatusNotFound {
			t.Fatalf("a session without a worktree = %d %s, want 404", r.code, r.raw)
		}
		if r := diff("no-such-run"); r.code != http.StatusNotFound {
			t.Fatalf("an unknown run = %d %s, want 404", r.code, r.raw)
		}
		other := h.createOrg(admin, "wt-diff-other")
		for _, route := range []string{"/diff", "/diff/file?path=README"} {
			if r := h.do("GET", "/v1/m/sessions/runs/"+ref+route, admin, tenantHdr(other)); r.code != http.StatusNotFound {
				t.Fatalf("another tenant read %s = %d %s, want 404", route, r.code, r.raw)
			}
		}
	})
}

// A branch that was deleted under a running session is a 409 that says so, not an empty
// diff that reads as "nothing changed".
func TestWorktreeDiffOfAGoneBranchIsRefused(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	dto := h.launch(ctx, true)
	worktreeGitOut(t, filepath.Join(h.root, dto.RunRef), "checkout", "-q", "--detach")
	worktreeGitOut(t, h.repo, "branch", "-D", dto.WorktreeBranch)

	_, err := h.m.worktreeDiffRange(ctx, h.tenant, dto.RunRef)

	wantRunErr(t, err, http.StatusConflict, "is gone")
}

// The base is the merge base with the workspace's CURRENT commit, not a commit recorded
// when the session started: the workspace moving on does not move where the branch left
// it, and a branch that shares no history has no base to compare, a 409, not an empty diff.
func TestWorktreeDiffBaseIsTheMergeBaseWithTheWorkspace(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	sha := handoffBranch(t, h)
	forkPoint := worktreeGitOut(t, h.repo, "rev-parse", "main")
	dto, err := h.launchFrom(sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.repo, "later.txt"), []byte("main moved on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGitOut(t, h.repo, "add", "later.txt")
	worktreeGitOut(t, h.repo, "commit", "-qm", "the workspace moves on")

	cmp, err := h.m.worktreeDiffRange(ctx, h.tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.base != forkPoint || cmp.head != sha || cmp.branch != dto.WorktreeBranch {
		t.Fatalf("compared %s..%s on %s, want %s..%s on %s", cmp.base, cmp.head, cmp.branch, forkPoint, sha, dto.WorktreeBranch)
	}

	root := worktreeGitOut(t, h.repo, "commit-tree", worktreeGitOut(t, h.repo, "rev-parse", "HEAD^{tree}"), "-m", "unrelated history")
	worktreeGitOut(t, h.repo, "branch", "unrelated/root", root)
	other, err := h.launchFrom("unrelated/root")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.m.worktreeDiffRange(ctx, h.tenant, other.RunRef)
	wantRunErr(t, err, http.StatusConflict, "shares no history")
}

// The diff keeps the workspace's own rules for its files: only the allowed subpaths are
// listed or read, a posture that denies denies, and a file past the read limit is refused.
func TestWorktreeDiffKeepsTheWorkspacesFileRules(t *testing.T) {
	repo := gitRepoFixture(t)
	handed := branchWithCommit(t, repo, "work/handoff", map[string]string{
		"feature.txt": "outside the allowed subpath\n", "sub/new.txt": "inside\n",
		"huge.bin.txt": strings.Repeat("x", worktreeDiffMaxBlob+1),
	}, "sub/inner.txt")

	fr := &fakeRunner{initSID: "sess-diff-rules"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	if err := m.UseSessionWorktrees(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wt-diff-rules")
	open := func(workspace map[string]any) string {
		wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, workspace, tenantHdr(tenant))
		if wr.code != http.StatusCreated {
			t.Fatalf("workspace = %d %s", wr.code, wr.raw)
		}
		ref, _ := wr.body["workspace_ref"].(string)
		r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
			"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant),
			"transport":            "stream-json", "permission_mode": "default", "isolation": "native",
			"workspace_ref": ref, "worktree": true, "worktree_from": handed,
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create = %d %s", r.code, r.raw)
		}
		run, _ := r.body["run_ref"].(string)
		return run
	}
	get := func(run, route string) resp {
		return h.do("GET", "/v1/m/sessions/runs/"+run+route, admin, tenantHdr(tenant))
	}

	scoped := open(map[string]any{"root_path": repo, "name": "scoped", "allow_subpaths": []string{"sub"}})
	list := get(scoped, "/diff")
	if list.code != http.StatusOK || strings.Contains(list.raw, "feature.txt") ||
		!strings.Contains(list.raw, "sub/new.txt") || !strings.Contains(list.raw, "sub/inner.txt") {
		t.Fatalf("a workspace limited to sub/ lists %s", list.raw)
	}
	if r := get(scoped, "/diff/file?path=feature.txt"); r.code != http.StatusNotFound {
		t.Fatalf("a path outside the allowed subpaths = %d %s, want 404", r.code, r.raw)
	}
	if r := get(scoped, "/diff/file?path=sub/new.txt"); r.code != http.StatusOK || r.body["modified"] != "inside\n" {
		t.Fatalf("a path inside = %d %s", r.code, r.raw)
	}

	whole := open(map[string]any{"root_path": repo, "name": "whole"})
	if r := get(whole, "/diff/file?path=huge.bin.txt"); r.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a file past the limit = %d, want 413", r.code)
	}

	denied := open(map[string]any{"root_path": repo, "name": "denying", "dlp_mode": "deny"})
	if r := get(denied, "/diff/file?path=sub/new.txt"); r.code != http.StatusForbidden {
		t.Fatalf("a workspace whose posture denies = %d %s, want 403", r.code, r.raw)
	}
}

// A session can write the repository's git config. A partial clone it plants would make
// git fetch a missing object through a remote of its choosing and run the ssh command it
// names, as the engine: the engine's git has no transport, so a missing object is an
// error and nothing runs.
func TestWorktreeDiffRunsNoTransportCommandFromTheRepositoryConfig(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	sha := handoffBranch(t, h)
	if _, err := h.launchFrom(sha); err != nil {
		t.Fatal(err)
	}
	blob := worktreeGitOut(t, h.repo, "rev-parse", sha+":feature.txt")
	if err := os.Remove(filepath.Join(h.repo, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "ssh.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran >> "+marker+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"core.repositoryformatversion": "1", "extensions.partialClone": "lazy",
		"remote.lazy.url": "ssh://example.invalid/repo", "remote.lazy.promisor": "true",
		"core.sshCommand": script,
	} {
		worktreeGitOut(t, h.repo, "config", k, v)
	}

	// The plant works: plain git asks the remote for the missing object.
	plain := exec.Command("git", "-C", h.repo, "cat-file", "-t", blob)
	plain.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	_ = plain.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the planted partial clone does not make plain git run its ssh command, so this test proves nothing: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if _, _, _, _, err := worktreeBlobAt(ctx, h.repo, sha, "feature.txt", worktreeGitOutputLimit); err == nil {
		t.Fatal("a missing object was read without error")
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("the engine's git ran a command the repository config named:\n%s", b)
	}
}
