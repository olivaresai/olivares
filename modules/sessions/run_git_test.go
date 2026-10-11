// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func gitPanelFixture(t *testing.T) sessionGit {
	t.Helper()
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("Landlock unavailable")
	}
	repo := gitRepoFixture(t)
	m := New(WithConfinement([]string{t.TempDir()}, true))
	return sessionGit{module: m, dir: repo, preset: PresetEditsAndCommands}
}

func TestSessionGitActions(t *testing.T) {
	g := gitPanelFixture(t)
	ctx := context.Background()
	principal := auth.Principal{Kind: auth.KindUser, DisplayName: "Session Author", Email: "session@example.invalid"}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(g.dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("README", "changed\n")
	write("new file.txt", "new\n")
	status, err := g.status(ctx)
	if err != nil || len(status.Files) != 2 || status.Branch != "main" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if err := g.stage(ctx, []string{"README", "new file.txt"}, false); err != nil {
		t.Fatal(err)
	}
	status, err = g.status(ctx)
	if err != nil || len(status.Files) != 2 || status.Files[0].Index == " " {
		t.Fatalf("staged=%+v err=%v", status, err)
	}
	if err := g.stage(ctx, []string{"README"}, true); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, g.dir, "diff", "--cached", "--name-only"); got != "new file.txt" {
		t.Fatalf("unstage changed other paths: %q", got)
	}
	if raw, err := os.ReadFile(filepath.Join(g.dir, "README")); err != nil || string(raw) != "changed\n" {
		t.Fatalf("unstage discarded working contents: %q %v", raw, err)
	}
	if err := g.commit(ctx, "Add the session file", principal); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, g.dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%s"); got != "Session Author <session@example.invalid>|Session Author <session@example.invalid>|Add the session file" {
		t.Fatalf("profile identity: %q", got)
	}
	if err := g.branch(ctx, "session/topic", true); err != nil {
		t.Fatal(err)
	}
	status, err = g.status(ctx)
	if err != nil || status.Branch != "session/topic" || strings.Join(status.Branches, ",") != "main,session/topic" {
		t.Fatalf("branches=%+v err=%v", status, err)
	}
	if err := g.branch(ctx, "main", false); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, g.dir, "branch", "--show-current"); got != "main" {
		t.Fatalf("switch=%q", got)
	}
	if err := g.branch(ctx, "--force", true); err == nil {
		t.Fatal("option accepted as a branch")
	}
	if err := g.stage(ctx, []string{"../escape"}, false); err == nil {
		t.Fatal("path left session")
	}
	if err := g.commit(ctx, "", principal); err == nil {
		t.Fatal("empty message accepted")
	}
	if err := g.commit(ctx, "message", auth.Principal{}); err == nil {
		t.Fatal("commit without user identity accepted")
	}
}

func TestSessionGitUnbornAndLiteralPaths(t *testing.T) {
	g := gitPanelFixture(t)
	g.dir = t.TempDir()
	worktreeGitOut(t, g.dir, "init", "-q", "-b", "main")
	for _, name := range []string{"one.txt", "two.txt", ":(glob)*", "line\nbreak.txt"} {
		if err := os.WriteFile(filepath.Join(g.dir, name), []byte("new\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.stage(t.Context(), []string{":(glob)*"}, false); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, g.dir, "diff", "--cached", "--name-only"); got != ":(glob)*" {
		t.Fatalf("pathspec expanded: %q", got)
	}
	if err := g.stage(t.Context(), []string{":(glob)*"}, true); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, g.dir, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("unborn unstage=%q", got)
	}
	if err := g.stage(t.Context(), []string{":(glob)*", "two.txt"}, false); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, g.dir, ":(glob)*", "edited after staging\n")
	if err := g.stage(t.Context(), []string{":(glob)*"}, true); err != nil {
		t.Fatalf("unstage edited unborn file: %v", err)
	}
	if got := worktreeGitOut(t, g.dir, "diff", "--cached", "--name-only"); got != "two.txt" {
		t.Fatalf("unstage changed unrelated staged file: %q", got)
	}
	if got := worktreeGitOut(t, g.dir, "show", ":two.txt"); got != "new" {
		t.Fatalf("unrelated staged contents changed: %q", got)
	}
	if raw, err := os.ReadFile(filepath.Join(g.dir, ":(glob)*")); err != nil || string(raw) != "edited after staging\n" {
		t.Fatalf("unborn unstage discarded later edit: %q %v", raw, err)
	}
	if err := g.stage(t.Context(), []string{":(glob)*"}, false); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, g.dir, ":(glob)*", "edited again after staging\n")
	if err := g.stage(t.Context(), []string{":(glob)*", "two.txt"}, true); err != nil {
		t.Fatalf("unstage mixed unborn selection: %v", err)
	}
	if got := worktreeGitOut(t, g.dir, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("mixed unborn selection remains staged: %q", got)
	}
	if raw, err := os.ReadFile(filepath.Join(g.dir, ":(glob)*")); err != nil || string(raw) != "edited again after staging\n" {
		t.Fatalf("mixed unborn unstage discarded later edit: %q %v", raw, err)
	}
	status, err := g.status(t.Context())
	if err != nil || len(status.Files) != 4 || status.Branch != "main" {
		t.Fatalf("unborn status=%+v err=%v", status, err)
	}
}

func TestSessionGitPolicyAndHostileConfiguration(t *testing.T) {
	g := gitPanelFixture(t)
	g.preset = PresetReadOnly
	if err := g.stage(t.Context(), []string{"README"}, false); err == nil {
		t.Fatal("read-only stage accepted")
	}
	if err := g.branch(t.Context(), "other", true); err == nil {
		t.Fatal("read-only branch mutation accepted")
	}
	if _, err := g.status(t.Context()); err != nil {
		t.Fatalf("read-only status: %v", err)
	}
	g.preset = PresetEditsAndCommands
	sentinel := filepath.Join(t.TempDir(), "executed")
	worktreeGitOut(t, g.dir, "config", "core.fsmonitor", "touch "+sentinel)
	if _, err := g.status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("fsmonitor executed")
	}
	filterSentinel := filepath.Join(g.dir, "filter-executed")
	worktreeGitOut(t, g.dir, "config", "filter.hostile.clean", "touch "+filterSentinel+"; cat")
	if err := os.WriteFile(filepath.Join(g.dir, ".gitattributes"), []byte("* filter=hostile\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.stage(t.Context(), []string{"README"}, false); err == nil {
		t.Fatal("command filter accepted")
	}
	if _, err := os.Stat(filterSentinel); !os.IsNotExist(err) {
		t.Fatal("repository filter ran")
	}
	g.module = New()
	if _, err := g.status(t.Context()); err == nil {
		t.Fatal("unconfined git accepted")
	}
}

func TestSessionGitHTTP(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("Landlock unavailable")
	}
	repo := gitRepoFixture(t)
	m := New(WithConfinement([]string{t.TempDir()}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "git-panel")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)
	ws := registerTestWorkspace(t, m, tenant, repo)
	seedLaunchTargetRun(t, m, tenant, workspace, "git-run", nil, func(rec model.Record) {
		rec[colRunWorkspacePath] = repo
		rec[colWorkspaceRef] = ws
		rec[colState] = stateStopped
		rec[colPermissionMode] = "dontAsk"
	})
	fixtureWrite(t, repo, "README", "changed in session\n")
	base := "/v1/m/sessions/runs/git-run/git"
	doc := api.ModuleOpenAPIDocument([]api.Module{m})
	statusOp := doc["paths"].(map[string]any)["/v1/m/sessions/runs/{ref}/git"].(map[string]any)["get"].(map[string]any)
	if got := h.do("GET", base, admin, tenantHdr(tenant)); got.code != http.StatusOK || got.body["branch"] != "main" {
		t.Fatalf("status: %d %s", got.code, got.raw)
	} else {
		assertPublished(t, "Git status", statusOp, got)
	}
	reader := h.viewerToken(admin, tenant, "git-reader@example.invalid")
	if got := h.do("GET", base, reader, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("reader status: %d %s", got.code, got.raw)
	}
	for _, action := range []string{"stage", "unstage", "commit", "branch"} {
		if got := h.doJSON("POST", base+"/"+action, reader, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusForbidden {
			t.Fatalf("reader %s: %d %s", action, got.code, got.raw)
		}
	}
	for _, action := range []struct {
		verb string
		body map[string]any
	}{
		{"stage", map[string]any{"paths": []string{"README"}}},
		{"unstage", map[string]any{"paths": []string{"README"}}},
		{"stage", map[string]any{"paths": []string{"README"}}},
		{"commit", map[string]any{"message": "Session commit"}},
		{"branch", map[string]any{"name": "topic", "create": true}},
		{"branch", map[string]any{"name": "main", "create": false}},
	} {
		if got := h.doJSON("POST", base+"/"+action.verb, admin, action.body, tenantHdr(tenant)); got.code != http.StatusOK {
			t.Fatalf("%s: %d %s", action.verb, got.code, got.raw)
		} else {
			assertPublished(t, action.verb, runControlOperation(t, doc, "/runs/{ref}/git/"+action.verb), got)
		}
	}
	other := h.createOrg(admin, "other-git-tenant")
	if got := h.do("GET", base, admin, tenantHdr(other)); got.code != http.StatusNotFound {
		t.Fatalf("foreign status: %d %s", got.code, got.raw)
	}
	if got := h.doJSON("POST", base+"/stage", admin, map[string]any{"paths": []string{"README"}}, tenantHdr(other)); got.code != http.StatusNotFound {
		t.Fatalf("foreign stage: %d %s", got.code, got.raw)
	}
	if got := h.doJSON("POST", base+"/stage", admin, map[string]any{"paths": []string{"../outside"}}, tenantHdr(tenant)); got.code != http.StatusBadRequest {
		t.Fatalf("traversal: %d %s", got.code, got.raw)
	}
	if got := h.doJSON("POST", base+"/branch", admin, map[string]any{"name": "--force", "create": true}, tenantHdr(tenant)); got.code != http.StatusBadRequest {
		t.Fatalf("branch injection: %d %s", got.code, got.raw)
	}
}

func TestSessionGitLinkedWorktreeAndSafeSwitch(t *testing.T) {
	g := gitPanelFixture(t)
	linked := filepath.Join(t.TempDir(), "worktree")
	worktreeGitOut(t, g.dir, "branch", "conflicting", "main")
	worktreeGitOut(t, g.dir, "worktree", "add", "-b", "session", "--", linked, "main")
	g.dir = linked
	fixtureWrite(t, g.dir, "README", "session edit\n")
	if err := g.stage(t.Context(), []string{"README"}, false); err != nil {
		t.Fatal(err)
	}
	if err := g.commit(t.Context(), "Linked worktree commit", auth.Principal{Kind: auth.KindUser, DisplayName: "Author", Email: "author@example.invalid"}); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, g.dir, "log", "-1", "--format=%s"); got != "Linked worktree commit" {
		t.Fatal(got)
	}
	if err := g.branch(t.Context(), "topic/next", true); err != nil {
		t.Fatal(err)
	}
	// Git refuses a switch which would overwrite a file. The panel cannot force it.
	fixtureWrite(t, g.dir, "README", "do not lose this\n")
	if err := g.branch(t.Context(), "conflicting", false); err == nil {
		t.Fatal("dirty branch switch overwrote work")
	}
	if body, err := os.ReadFile(filepath.Join(g.dir, "README")); err != nil || string(body) != "do not lose this\n" {
		t.Fatalf("working file lost: %q %v", body, err)
	}
	if err := os.Remove(filepath.Join(g.dir, "sub", "inner.txt")); err != nil {
		t.Fatal(err)
	}
	if err := g.stage(t.Context(), []string{"sub/inner.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, g.dir, "diff", "--cached", "--name-status"); got != "D\tsub/inner.txt" {
		t.Fatalf("deleted stage=%q", got)
	}
}

func TestSessionGitHooksAndPinnedRoot(t *testing.T) {
	g := gitPanelFixture(t)
	outside := t.TempDir()
	fixtureWrite(t, outside, "README", "outside must stay unchanged\n")
	worktreeGitOut(t, g.dir, "config", "core.worktree", outside)
	// Even a repository hook that only writes INSIDE the permitted folder must
	// not run: confinement alone is not a replacement for disabling Git hooks.
	sentinel := filepath.Join(g.dir, "hook-executed")
	for _, hook := range []string{"pre-commit", "post-commit", "post-checkout"} {
		if err := os.WriteFile(filepath.Join(g.dir, ".git", "hooks", hook), []byte("#!/bin/sh\ntouch '"+sentinel+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_AUTHOR_NAME", "Inherited host author")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(g.dir, ".git", "hooks"))
	fixtureWrite(t, g.dir, "README", "only the session folder\n")
	if err := g.stage(t.Context(), []string{"README"}, false); err != nil {
		t.Fatal(err)
	}
	if err := g.commit(t.Context(), "Confined change", auth.Principal{Kind: auth.KindUser, DisplayName: "Profile", Email: "profile@example.invalid"}); err != nil {
		t.Fatal(err)
	}
	if err := g.branch(t.Context(), "safe-topic", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("repository hooks ran")
	}
	raw, err := os.ReadFile(filepath.Join(outside, "README"))
	if err != nil || string(raw) != "outside must stay unchanged\n" {
		t.Fatalf("core.worktree escaped: %q %v", raw, err)
	}
}

func TestSessionGitHTTPLinkedWorkspaceAndAudit(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("Landlock unavailable")
	}
	repo := gitRepoFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	worktreeGitOut(t, repo, "worktree", "add", "-b", "owned-session", "--", linked, "main")
	m := New(WithConfinement([]string{t.TempDir()}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "linked-git")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)
	wsRef := registerTestWorkspace(t, m, tenant, repo)
	ws, err := m.resolveWorkspace(t.Context(), tenant, wsRef)
	if err != nil {
		t.Fatal(err)
	}
	seedLaunchTargetRun(t, m, tenant, workspace, "linked-git-run", nil, func(rec model.Record) {
		rec[colRunWorkspacePath] = linked
		rec[colWorkspaceRef] = wsRef
		rec[colRunWorktreeBranch] = "owned-session"
		rec[colState] = stateStopped
	})
	base := "/v1/m/sessions/runs/linked-git-run/git"
	fixtureWrite(t, linked, "README", "linked change\n")
	for _, action := range []struct {
		name string
		body map[string]any
	}{{"stage", map[string]any{"paths": []string{"README"}}}, {"branch", map[string]any{"name": "other-topic", "create": true}}} {
		got := h.doJSON("POST", base+"/"+action.name, admin, action.body, tenantHdr(tenant))
		if got.code != 200 {
			t.Fatalf("%s: %d %s", action.name, got.code, got.raw)
		}
	}
	rec, err := m.loadRun(t.Context(), tenant, "linked-git-run")
	if err != nil || rec.String(colRunWorktreeBranch) != "owned-session" {
		t.Fatalf("cleanup ownership changed: %v %v", rec, err)
	}
	found := false
	err = m.Data.View(t.Context(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(t.Context(), 0, func(ev model.AuditEvent) error {
			if ev.Action == "sessions.workspace.git-stage" {
				found = true
				if ev.TargetID != ws.id {
					t.Fatalf("audit target=%s want=%s", ev.TargetID, ws.id)
				}
			}
			return nil
		})
	})
	if err != nil || !found {
		t.Fatalf("audit not sealed: %v", err)
	}
	setSnapshotRegistration(t, m, tenant, wsRef, colWsRootPath, gitRepoFixture(t))
	if got := h.do("GET", base, admin, tenantHdr(tenant)); got.code != http.StatusConflict {
		t.Fatalf("rebound status: %d %s", got.code, got.raw)
	}
	if got := h.doJSON("POST", base+"/branch", admin, map[string]any{"name": "must-not-create", "create": true}, tenantHdr(tenant)); got.code != http.StatusConflict {
		t.Fatalf("rebound action: %d %s", got.code, got.raw)
	}
	if got := worktreeGitOut(t, linked, "branch", "--show-current"); got != "other-topic" {
		t.Fatalf("rebound mutation: %q", got)
	}
}

func TestSessionGitChildCannotContactHost(t *testing.T) {
	g := gitPanelFixture(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Fatal("network probe requires curl:", err)
	}
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	// Execute a real child through precisely the Git runner's boundary. A config
	// race can dispatch another program, but cannot grant it host network access.
	_, err := g.exec(t.Context(), []string{"-c", "alias.network=!curl --noproxy '*' --silent --show-error --max-time 2 " + target.URL, "network"}, "", nil)
	if err == nil {
		t.Fatal("Git child reached the host network")
	}
	if calls.Load() != 0 {
		t.Fatalf("host received %d requests", calls.Load())
	}
	// The same boundary must still run Git, not pass by refusing every child.
	if _, err := g.status(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionGitStatusChildCannotWrite(t *testing.T) {
	g := gitPanelFixture(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A real child inserted after the config inspection stands in for a raced
	// clean filter. Read-authorized status must deny creation AND truncation.
	wrapper := "#!/bin/sh\nfor arg do\nif [ \"$arg\" = status ]; then\n" +
		"printf overwritten > README 2>/dev/null\n" +
		"printf created > status-child-wrote 2>/dev/null\nfi\ndone\nexec '" + gitPath + "' \"$@\"\n"
	bin := filepath.Join(g.dir, "probe-bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	status, err := g.status(t.Context())
	if err != nil || !status.Writable {
		t.Fatalf("writable session status: %+v %v", status, err)
	}
	if body, err := os.ReadFile(filepath.Join(g.dir, "README")); err != nil || string(body) == "overwritten" || len(body) == 0 {
		t.Fatalf("status child truncated the session file: %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(g.dir, "status-child-wrote")); !os.IsNotExist(err) {
		t.Fatal("status child wrote inside the session folder")
	}
}
