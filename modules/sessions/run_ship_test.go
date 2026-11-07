// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// CommitRun is what a worktree session's "ship" does locally: commit every
// change of the run's folder as the caller, through the same confined Git and
// audit as the Git routes, and name the branch, commit and tree to publish.
func TestCommitRunCommitsTheWorktreeAndNamesWhatToPublish(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("Landlock unavailable")
	}
	repo := gitRepoFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	worktreeGitOut(t, repo, "worktree", "add", "-b", "olivares/ship", "--", linked, "main")
	m := New(WithConfinement([]string{t.TempDir()}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ship-git")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)
	wsRef := registerTestWorkspace(t, m, tenant, repo)
	seedLaunchTargetRun(t, m, tenant, workspace, "ship-run", nil, func(rec model.Record) {
		rec[colRunWorkspacePath] = linked
		rec[colWorkspaceRef] = wsRef
		rec[colRunWorktreeBranch] = "olivares/ship"
		rec[colState] = stateStopped
	})
	user := auth.Principal{Kind: auth.KindUser, DisplayName: "Ship Author", Email: "ship@example.invalid"}
	ctx := t.Context()

	fixtureWrite(t, linked, "README", "shipped\n")
	head, err := m.CommitRun(ctx, user, tenant, workspace, "ship-run", "Ship the work")
	if err != nil {
		t.Fatal(err)
	}
	if !head.Committed || head.Branch != "olivares/ship" ||
		head.Commit != worktreeGitOut(t, linked, "rev-parse", "HEAD") || head.Tree != worktreeGitOut(t, linked, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("head = %+v", head)
	}
	if got := worktreeGitOut(t, linked, "log", "-1", "--format=%an <%ae>|%s"); got != "Ship Author <ship@example.invalid>|Ship the work" {
		t.Fatalf("commit = %q", got)
	}
	// The mutation is sealed in the workspace's audit before Git runs, like the Git routes.
	sealed := false
	if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			sealed = sealed || ev.Action == "sessions.workspace.git-ship"
			return nil
		})
	}); err != nil || !sealed {
		t.Fatalf("ship not audited: %v", err)
	}
	// A clean folder ships what it holds, with no new commit.
	again, err := m.CommitRun(ctx, user, tenant, workspace, "ship-run", "")
	if err != nil || again.Committed || again.Commit != head.Commit {
		t.Fatalf("clean ship = %+v %v", again, err)
	}

	t.Run("a run outside the workspace is not found", func(t *testing.T) {
		// launchTargetWorkspaces already created its slugs for this tenant; add a third.
		var other model.ID
		if err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
			ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Ship other", Slug: "ship-other", Status: model.StatusActive})
			if err != nil {
				return err
			}
			other = ws.ID
			return nil
		}); err != nil {
			t.Fatalf("workspace: %v", err)
		}
		if _, err := m.CommitRun(ctx, user, tenant, other, "ship-run", "m"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v, want store.ErrNotFound", err)
		}
	})
	t.Run("a detached HEAD has no branch to ship", func(t *testing.T) {
		worktreeGitOut(t, linked, "checkout", "--detach")
		defer worktreeGitOut(t, linked, "checkout", "olivares/ship")
		if _, err := m.CommitRun(ctx, user, tenant, workspace, "ship-run", "m"); !errors.Is(err, gitpublish.ErrNoBranch) {
			t.Fatalf("err = %v, want ErrNoBranch", err)
		}
	})
	t.Run("changes need the caller's profile and a message", func(t *testing.T) {
		fixtureWrite(t, linked, "README", "more\n")
		if _, err := m.CommitRun(ctx, auth.Principal{Kind: auth.KindUser}, tenant, workspace, "ship-run", "m"); !errors.Is(err, gitpublish.ErrLocalInput) {
			t.Fatalf("no profile: err = %v, want ErrLocalInput", err)
		}
		if _, err := m.CommitRun(ctx, user, tenant, workspace, "ship-run", ""); !errors.Is(err, gitpublish.ErrLocalInput) {
			t.Fatalf("no message: err = %v, want ErrLocalInput", err)
		}
	})
}

func TestSessionGitAIAttribution(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Fatal("Landlock required for attribution proof")
	}
	repo := gitRepoFixture(t)
	m := New(WithConfinement([]string{t.TempDir()}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "attribution-git")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)
	wsRef := registerTestWorkspace(t, m, tenant, repo)
	seedLaunchTargetRun(t, m, tenant, workspace, "attribution-run", nil, func(rec model.Record) {
		rec[colRunWorkspacePath] = repo
		rec[colWorkspaceRef] = wsRef
		rec[colState] = stateStopped
		rec[colRunProfileDriver] = "claude"
		rec[colRunModelRef] = "claude-sonnet-4-5"
	})
	m.GitAttribution = func(ctx context.Context, tenant model.TenantID, templateID string) (string, error) {
		var mode string
		err := h.st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			mode, err = ReadGitAttribution(ctx, sc, templateID)
			return err
		})
		return mode, err
	}
	user := auth.Principal{Kind: auth.KindUser, DisplayName: "Human Author", Email: "human@example.invalid"}
	defaults := h.do("GET", "/v1/m/sessions/git-attribution-settings", admin, tenantHdr(tenant))
	if defaults.code != 200 || defaults.body["ai_attribution"] != "off" {
		t.Fatalf("default attribution: %d %s", defaults.code, defaults.raw)
	}
	catalog := h.do("GET", "/v1/m/sessions/templates?builtin=true", admin, tenantHdr(tenant))
	if catalog.code != 200 {
		t.Fatalf("templates: %d %s", catalog.code, catalog.raw)
	}
	university := ""
	for _, item := range catalog.body["items"].([]any) {
		row := item.(map[string]any)
		if row["name"] == "University" || row["name"] == "Studio" {
			settings := row["body"].(map[string]any)["settings"].(map[string]any)
			if settings["ai_attribution"] != "assisted-by" {
				t.Fatalf("template attribution: %v", row)
			}
			if row["name"] == "University" {
				university = row["id"].(string)
			}
		}
	}
	if university == "" {
		t.Fatal("University template missing")
	}
	rec, err := m.loadRun(t.Context(), tenant, "attribution-run")
	if err != nil {
		t.Fatal(err)
	}
	rec[colTemplateID] = university
	if err := m.Data.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(t.Context(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, repo, "README", "template change")
	if _, err := m.CommitRun(t.Context(), user, tenant, workspace, "attribution-run", "Template change"); err != nil {
		t.Fatal(err)
	}
	if got := worktreeGitOut(t, repo, "log", "-1", "--format=%B"); !strings.Contains(got, "Assisted-by: claude (claude-sonnet-4-5)") {
		t.Fatalf("template commit: %q", got)
	}
	for i, mode := range []string{"assisted-by", "co-authored-by", "off"} {
		setting := h.doJSON("PUT", "/v1/m/sessions/git-attribution-settings", admin, map[string]any{"ai_attribution": mode}, tenantHdr(tenant))
		if setting.code != 200 {
			t.Fatalf("save attribution %s: %d %s", mode, setting.code, setting.raw)
		}
		fixtureWrite(t, repo, "README", mode)
		if i == 1 {
			g, _, release, err := m.openRunGit(t.Context(), tenant, "attribution-run")
			if err != nil {
				t.Fatal(err)
			}
			err = g.stage(t.Context(), []string{"README"}, false)
			if err == nil {
				err = g.commit(t.Context(), "Session change", user)
			}
			release()
			if err != nil {
				t.Fatal(err)
			}
		} else if _, err := m.CommitRun(t.Context(), user, tenant, workspace, "attribution-run", "Session change"); err != nil {
			t.Fatal(err)
		}
		message := worktreeGitOut(t, repo, "log", "-1", "--format=%B")
		want := ""
		if mode == "assisted-by" {
			want = "Assisted-by: claude (claude-sonnet-4-5)"
		}
		if mode == "co-authored-by" {
			want = "Co-authored-by: claude (claude-sonnet-4-5)"
		}
		if want != "" && strings.Count(message, want) != 1 {
			t.Fatalf("mode %s: message %q lacks one %q", mode, message, want)
		}
		if strings.Contains(strings.ToLower(message), "signed-off-by:") || (mode == "off" && (strings.Contains(message, "Assisted-by:") || strings.Contains(message, "Co-authored-by:"))) {
			t.Fatalf("unexpected trailer: %q", message)
		}
		if got := worktreeGitOut(t, repo, "log", "-1", "--format=%an <%ae>"); got != "Human Author <human@example.invalid>" {
			t.Fatal(got)
		}
	}
	fixtureWrite(t, repo, "README", "scoped commit")
	g, _, release, err := m.openRunGit(t.Context(), tenant, "attribution-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.stage(t.Context(), []string{"README"}, false); err != nil {
		release()
		t.Fatal(err)
	}
	original := m.Data
	m.Data = confinedLaunchTargetData{inner: original, ws: workspace}
	err = g.commit(t.Context(), "Scoped user's commit\n\nCo-authored-by: Collaborator <collaborator@example.invalid>", user)
	m.Data = original
	release()
	if err != nil {
		t.Fatalf("workspace-scoped commit: %v", err)
	}
	if got := worktreeGitOut(t, repo, "log", "-1", "--format=%B"); !strings.Contains(got, "Co-authored-by: Collaborator <collaborator@example.invalid>") {
		t.Fatalf("human coauthor lost: %q", got)
	}

	fixtureWrite(t, repo, "README", "no agent signoff")
	if _, err := m.CommitRun(t.Context(), user, tenant, workspace, "attribution-run", "Change\n\nSigned-off-by: Agent <agent@example.invalid>"); err == nil {
		t.Fatal("session accepted an agent signoff")
	}
}
