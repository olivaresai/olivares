// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"path/filepath"
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
