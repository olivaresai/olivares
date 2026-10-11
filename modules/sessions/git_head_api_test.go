// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A file's committed text is one query away from its current text,
// on the workspace file read and on the session's changed-file read, so the console can
// show what changed against HEAD before anything is published.

// committedRepo is a git repository with one commit: README.md, sub/a.txt and, when
// secret is not empty, secret.txt holding it.
func committedRepo(t *testing.T, secret string) string {
	t.Helper()
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	fixtureWrite(t, dir, "README.md", "# Title\n")
	fixtureWrite(t, dir, "sub/a.txt", "tracked\n")
	if secret != "" {
		fixtureWrite(t, dir, "secret.txt", secret+"\n")
	}
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func TestRevHeadOnTheWorkspaceFileRead(t *testing.T) {
	h := newHarness(t, New(WithClassifier(fakeClassifier{trigger: "SECRET"})))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	register := func(root string, body map[string]any) string {
		t.Helper()
		body["root_path"], body["name"] = root, filepath.Base(root)
		r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, body, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("register = %d %s", r.code, r.raw)
		}
		return r.body["workspace_ref"].(string)
	}
	read := func(ref, path, rev string) resp {
		t.Helper()
		q := url.Values{"path": {path}}
		if rev != "" {
			q.Set("rev", rev)
		}
		return h.do("GET", "/v1/m/sessions/workspaces/"+ref+"/files/raw?"+q.Encode(), admin, tenantHdr(tenant))
	}

	repo := committedRepo(t, "")
	ref := register(repo, map[string]any{})
	fixtureWrite(t, repo, "README.md", "# Title\nedited in the console\n")
	fixtureWrite(t, repo, "new.txt", "never committed\n")
	if err := os.Remove(filepath.Join(repo, "sub", "a.txt")); err != nil {
		t.Fatal(err)
	}

	if r := read(ref, "README.md", ""); r.code != http.StatusOK || r.body["content"] != "# Title\nedited in the console\n" {
		t.Fatalf("current read changed: %d %s", r.code, r.raw)
	}
	r := read(ref, "README.md", "HEAD")
	if r.code != http.StatusOK || r.body["content"] != "# Title\n" || r.body["encoding"] != "utf-8" {
		t.Fatalf("HEAD read = %d %s, want the committed text", r.code, r.raw)
	}
	if r := read(ref, "sub/a.txt", "HEAD"); r.code != http.StatusOK || r.body["content"] != "tracked\n" {
		t.Fatalf("HEAD read of a file deleted from the working tree = %d %s", r.code, r.raw)
	}
	if r := read(ref, "new.txt", "HEAD"); r.code != http.StatusNotFound {
		t.Fatalf("HEAD read of an untracked file = %d %s, want 404", r.code, r.raw)
	}
	if r := read(ref, "README.md", "main~1"); r.code != http.StatusBadRequest {
		t.Fatalf("an unsupported rev = %d %s, want 400", r.code, r.raw)
	}
	for _, bad := range []string{"../x", "/etc/passwd"} {
		if r := read(ref, bad, "HEAD"); r.code != http.StatusForbidden {
			t.Errorf("HEAD read of %q = %d %s, want 403 like the current read", bad, r.code, r.raw)
		}
	}
	plain := register(t.TempDir(), map[string]any{})
	if r := read(plain, "x", "HEAD"); r.code != http.StatusNotFound {
		t.Fatalf("HEAD read where there is no repository = %d %s, want 404", r.code, r.raw)
	}

	// The committed text is held to the workspace's own policy: DLP...
	leaky := committedRepo(t, "SECRET token")
	denying := register(leaky, map[string]any{"dlp_mode": "deny"})
	fixtureWrite(t, leaky, "secret.txt", "scrubbed since\n")
	if r := read(denying, "secret.txt", ""); r.code != http.StatusOK {
		t.Fatalf("the scrubbed current file = %d %s", r.code, r.raw)
	}
	if r := read(denying, "secret.txt", "HEAD"); r.code != http.StatusForbidden {
		t.Fatalf("HEAD read of committed secret under DLP deny = %d %s, want 403", r.code, r.raw)
	}
	// ...and its allowed subpaths.
	narrow := register(committedRepo(t, ""), map[string]any{"allow_subpaths": []string{"sub"}})
	if r := read(narrow, "README.md", "HEAD"); r.code != http.StatusForbidden {
		t.Fatalf("HEAD read outside the allowed subpaths = %d %s, want 403", r.code, r.raw)
	}
	if r := read(narrow, "sub/a.txt", "HEAD"); r.code != http.StatusOK || r.body["content"] != "tracked\n" {
		t.Fatalf("HEAD read inside the allowed subpaths = %d %s", r.code, r.raw)
	}
}

func TestRevHeadOnTheSessionChangedFileRead(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)
	repo := committedRepo(t, "")
	plain := t.TempDir()
	fixtureWrite(t, plain, "x.txt", "no repository here\n")
	folder := func(dir string) func(model.Record) {
		return func(rec model.Record) { rec[colRunWorkspacePath] = dir }
	}
	seedLaunchTargetRun(t, m, tenant, workspace, "run-with-repo", nil, folder(repo))
	seedLaunchTargetRun(t, m, tenant, workspace, "run-plain", nil, folder(plain))
	fixtureWrite(t, repo, "README.md", "# Title\nchanged by the agent\n")
	fixtureWrite(t, repo, "new.txt", "created by the agent\n")

	read := func(run, path, rev string) resp {
		t.Helper()
		q := url.Values{"path": {path}}
		if rev != "" {
			q.Set("rev", rev)
		}
		return h.do("GET", "/v1/m/sessions/runs/"+run+"/changes/file?"+q.Encode(), admin, tenantHdr(tenant))
	}
	if r := read("run-with-repo", "README.md", ""); r.code != http.StatusOK || r.body["text"] != "# Title\nchanged by the agent\n" {
		t.Fatalf("current read changed: %d %s", r.code, r.raw)
	}
	r := read("run-with-repo", "README.md", "HEAD")
	if r.code != http.StatusOK || r.body["text"] != "# Title\n" || r.body["binary"] != false {
		t.Fatalf("HEAD read = %d %s, want the committed text", r.code, r.raw)
	}
	if r := read("run-with-repo", "new.txt", "HEAD"); r.code != http.StatusNotFound {
		t.Fatalf("HEAD read of a file the agent created = %d %s, want 404", r.code, r.raw)
	}
	if r := read("run-plain", "x.txt", "HEAD"); r.code != http.StatusNotFound {
		t.Fatalf("HEAD read in a folder with no repository = %d %s, want 404", r.code, r.raw)
	}
	if r := read("run-with-repo", "README.md", "abc"); r.code != http.StatusBadRequest {
		t.Fatalf("an unsupported rev = %d %s, want 400", r.code, r.raw)
	}
	if r := read("run-with-repo", "../x", "HEAD"); r.code != http.StatusBadRequest {
		t.Fatalf("HEAD read outside the folder = %d %s, want 400", r.code, r.raw)
	}
}

// TestRevHeadIsAudited: a read of committed text is a governed read of the workspace, so
// it is audited as one (the classes found, never the content), a denial is audited, a 404
// leaves nothing, and the ordinary read keeps its own audit.
func TestRevHeadIsAudited(t *testing.T) {
	m := New(WithClassifier(fakeClassifier{trigger: "SECRET"}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	register := func(root, dlp string) string {
		t.Helper()
		r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": root, "name": filepath.Base(root), "dlp_mode": dlp}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("register = %d %s", r.code, r.raw)
		}
		return r.body["workspace_ref"].(string)
	}
	get := func(ref, path, rev string) resp {
		t.Helper()
		q := url.Values{"path": {path}}
		if rev != "" {
			q.Set("rev", rev)
		}
		return h.do("GET", "/v1/m/sessions/workspaces/"+ref+"/files/raw?"+q.Encode(), admin, tenantHdr(tenant))
	}
	count := func(action string) int { return len(acctAuditsOf(t, m, tenant, action)) }

	repo := committedRepo(t, "SECRET token")
	labelled := register(repo, "label")
	r := get(labelled, "secret.txt", "HEAD")
	if hits, _ := r.body["sensitivity"].([]any); r.code != http.StatusOK || len(hits) == 0 {
		t.Fatalf("labelled HEAD read = %d %s, want 200 with the class found", r.code, r.raw)
	}
	events := acctAuditsOf(t, m, tenant, "sessions.workspace.read-head")
	if len(events) != 1 || !strings.Contains(fmt.Sprint(events[0].meta), "secret.txt") || strings.Contains(fmt.Sprint(events[0].meta), "SECRET token") {
		t.Fatalf("read-head audit = %v, want one entry naming the path and never the content", events)
	}
	if r := get(labelled, "new.txt", "HEAD"); r.code != http.StatusNotFound || count("sessions.workspace.read-head") != 1 {
		t.Fatalf("a 404 must leave no audit: %d %s, %d entries", r.code, r.raw, count("sessions.workspace.read-head"))
	}
	if r := get(labelled, "secret.txt", ""); r.code != http.StatusOK || count("sessions.workspace.read") != 1 || count("sessions.workspace.read-head") != 1 {
		t.Fatalf("the ordinary read keeps its own audit: %d, read=%d read-head=%d", r.code, count("sessions.workspace.read"), count("sessions.workspace.read-head"))
	}

	denying := register(committedRepo(t, "SECRET token"), "deny")
	if r := get(denying, "secret.txt", "HEAD"); r.code != http.StatusForbidden || count("sessions.workspace.read-head-denied") != 1 || strings.Contains(r.raw, "SECRET token") {
		t.Fatalf("denied HEAD read = %d %s, audit entries %d", r.code, r.raw, count("sessions.workspace.read-head-denied"))
	}
}

// TestRevHeadHonorsAllowedSubpathsForTheWrittenPath: HEAD is looked up by the path as
// written. The agent can replace a directory with a link into an allowed one, so a path
// outside the allowed subpaths must not pass because the link resolves inside them.
func TestRevHeadHonorsAllowedSubpathsForTheWrittenPath(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	repo := t.TempDir()
	gitFixture(t, repo, "init", "-q", "-b", "main")
	fixtureWrite(t, repo, "secrets/x.txt", "committed outside the allowed folder\n")
	fixtureWrite(t, repo, "sub/x.txt", "committed inside\n")
	gitFixture(t, repo, "add", ".")
	gitFixture(t, repo, "commit", "-q", "-m", "first")
	if err := os.RemoveAll(filepath.Join(repo, "secrets")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(repo, "secrets")); err != nil {
		t.Fatal(err)
	}
	r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "repo", "allow_subpaths": []string{"sub"}}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}
	ref := r.body["workspace_ref"].(string)
	read := func(path string) resp {
		return h.do("GET", "/v1/m/sessions/workspaces/"+ref+"/files/raw?path="+url.QueryEscape(path)+"&rev=HEAD", admin, tenantHdr(tenant))
	}
	if got := read("secrets/x.txt"); got.code != http.StatusForbidden || strings.Contains(got.raw, "outside the allowed") {
		t.Fatalf("a link into an allowed folder opened committed text outside it: %d %s", got.code, got.raw)
	}
	if got := read("sub/x.txt"); got.code != http.StatusOK || got.body["content"] != "committed inside\n" {
		t.Fatalf("the allowed path = %d %s", got.code, got.raw)
	}
}

// TestRevHeadBoundsAndKindsThroughTheRoutes: the size limits, the truncation flags and the
// binary answer are the ones of the ordinary read, on both routes.
func TestRevHeadBoundsAndKindsThroughTheRoutes(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)

	repo := t.TempDir()
	gitFixture(t, repo, "init", "-q", "-b", "main")
	fixtureWrite(t, repo, "long.txt", strings.Repeat("0123456789", 60)+"\n")     // 601 bytes
	fixtureWrite(t, repo, "big.txt", strings.Repeat("a", changesMaxFileSize+10)) // past the changes limit
	fixtureWrite(t, repo, "bin.dat", "\xff\xfe\x00\x01")
	fixtureWrite(t, repo, "huge.txt", strings.Repeat("b", headMaxObject+1))
	gitFixture(t, repo, "add", ".")
	gitFixture(t, repo, "commit", "-q", "-m", "first")

	r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "repo", "max_read_bytes": 100}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}
	ref := r.body["workspace_ref"].(string)
	wsRead := func(path string) resp {
		return h.do("GET", "/v1/m/sessions/workspaces/"+ref+"/files/raw?rev=HEAD&path="+url.QueryEscape(path), admin, tenantHdr(tenant))
	}
	if got := wsRead("long.txt"); got.code != http.StatusOK || got.body["truncated"] != true || got.body["size"] != float64(601) || len(got.body["content"].(string)) != 100 {
		t.Fatalf("workspace HEAD read past max_read_bytes = %d %s, want truncated at 100 bytes with the full size", got.code, got.raw)
	}
	if got := wsRead("bin.dat"); got.code != http.StatusOK || got.body["encoding"] != "base64" {
		t.Fatalf("workspace HEAD read of a binary blob = %d %s, want base64", got.code, got.raw)
	}
	if got := wsRead("huge.txt"); got.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("workspace HEAD read past the ceiling = %d, want 413", got.code)
	}

	seedLaunchTargetRun(t, m, tenant, workspace, "run-bounds", nil, func(rec model.Record) { rec[colRunWorkspacePath] = repo })
	seedLaunchTargetRun(t, m, tenant, workspace, "run-no-folder", nil, nil)
	runRead := func(run, path string) resp {
		return h.do("GET", "/v1/m/sessions/runs/"+run+"/changes/file?rev=HEAD&path="+url.QueryEscape(path), admin, tenantHdr(tenant))
	}
	if got := runRead("run-bounds", "big.txt"); got.code != http.StatusOK || got.body["truncated"] != true || got.body["size"] != float64(changesMaxFileSize+10) || len(got.body["text"].(string)) != changesMaxFileSize {
		t.Fatalf("run HEAD read past the changes limit = %d, want truncated at %d bytes with the full size", got.code, changesMaxFileSize)
	}
	if got := runRead("run-bounds", "bin.dat"); got.code != http.StatusOK || got.body["binary"] != true || got.body["text"] != "" {
		t.Fatalf("run HEAD read of a binary blob = %d %s", got.code, got.raw)
	}
	if got := runRead("run-bounds", "huge.txt"); got.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("run HEAD read past the ceiling = %d, want 413", got.code)
	}
	if got := runRead("run-no-folder", "x"); got.code != http.StatusNotFound {
		t.Fatalf("run HEAD read with no folder = %d, want 404", got.code)
	}
}

// TestRevHeadStaysInsideTheTenant: another tenant's workspace and run answer 404 on the
// HEAD read, as they do on the ordinary one, and nothing of their history is returned.
func TestRevHeadStaysInsideTheTenant(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	mine := h.createOrg(admin, "acme")
	theirs := h.createOrg(admin, "globex")
	repo := committedRepo(t, "their private history")
	r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "theirs"}, tenantHdr(theirs))
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}
	ref := r.body["workspace_ref"].(string)
	theirWorkspace, _ := launchTargetWorkspaces(t, m, theirs)
	seedLaunchTargetRun(t, m, theirs, theirWorkspace, "run-theirs", nil, func(rec model.Record) { rec[colRunWorkspacePath] = repo })

	own := h.do("GET", "/v1/m/sessions/workspaces/"+ref+"/files/raw?rev=HEAD&path=secret.txt", admin, tenantHdr(theirs))
	if own.code != http.StatusOK || !strings.Contains(own.raw, "their private history") {
		t.Fatalf("the owner's HEAD read = %d %s", own.code, own.raw)
	}
	for _, path := range []string{
		"/v1/m/sessions/workspaces/" + ref + "/files/raw?rev=HEAD&path=secret.txt",
		"/v1/m/sessions/runs/run-theirs/changes/file?rev=HEAD&path=secret.txt",
	} {
		got := h.do("GET", path, admin, tenantHdr(mine))
		if (got.code != http.StatusNotFound && got.code != http.StatusForbidden) || strings.Contains(got.raw, "their private history") {
			t.Errorf("%s from another tenant = %d %s, want a refusal that shows nothing of the history", path, got.code, got.raw)
		}
	}
}
