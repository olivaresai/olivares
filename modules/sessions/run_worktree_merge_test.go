// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestWorktreeMergeHTTP(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("Landlock unavailable")
	}
	repo := gitRepoFixture(t)
	linked := filepath.Join(t.TempDir(), "winner")
	worktreeGitOut(t, repo, "worktree", "add", "-b", "owned-winner", "--", linked, "main")
	fixtureWrite(t, linked, "README", "winning change\n")
	worktreeGitOut(t, linked, "add", "README")
	worktreeGitOut(t, linked, "commit", "-qm", "Winning result")
	winner := worktreeGitOut(t, linked, "rev-parse", "HEAD")
	// The repository cannot turn Keep into an apparently successful --quit or
	// squash, nor ask a configured signature helper to execute.
	worktreeGitOut(t, repo, "config", "--", "branch.main.mergeOptions", "--quit")
	worktreeGitOut(t, repo, "config", "merge.verifySignatures", "true")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "post-merge"), []byte("#!/bin/sh\nprintf executed > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	m := New(WithConfinement([]string{t.TempDir()}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "merge-race")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)
	wsRef := registerTestWorkspace(t, m, tenant, repo)
	seedLaunchTargetRun(t, m, tenant, workspace, "winner", nil, func(rec model.Record) {
		rec[colRunWorkspacePath], rec[colWorkspaceRef], rec[colRunWorktreeBranch] = linked, wsRef, "owned-winner"
		rec[colState], rec[colPermissionMode] = stateStopped, "dontAsk"
	})
	url := "/v1/m/sessions/runs/winner/git/merge"
	reader := h.viewerToken(admin, tenant, "merge-reader@example.invalid")
	if got := h.doJSON("POST", url, reader, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("reader: %d %s", got.code, got.raw)
	}
	other := h.createOrg(admin, "merge-other")
	if got := h.doJSON("POST", url, admin, map[string]any{}, tenantHdr(other)); got.code != http.StatusNotFound {
		t.Fatalf("foreign: %d %s", got.code, got.raw)
	}
	if got := h.doJSON("POST", url, admin, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("merge: %d %s", got.code, got.raw)
	} else {
		assertPublished(t, "merge", runControlOperation(t, api.ModuleOpenAPIDocument([]api.Module{m}), "/runs/{ref}/git/merge"), got)
	}
	if got := worktreeGitOut(t, repo, "rev-parse", "HEAD"); got != winner {
		t.Fatalf("HEAD=%s want=%s", got, winner)
	}
	if raw, err := os.ReadFile(filepath.Join(repo, "README")); err != nil || string(raw) != "winning change\n" {
		t.Fatalf("working file: %q %v", raw, err)
	}
	if _, err := os.Stat(linked); err != nil {
		t.Fatalf("winner worktree removed: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("post-merge hook ran")
	}
	found := false
	if err := m.Data.View(t.Context(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(t.Context(), 0, func(ev model.AuditEvent) error {
			if ev.Action == "sessions.workspace.git-merge" {
				found = true
			}
			return nil
		})
	}); err != nil || !found {
		t.Fatalf("merge audit missing: %v", err)
	}
}

func TestWorktreeMergeRefusesUnsafeState(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("Landlock unavailable")
	}
	for _, name := range []string{"running", "dirty winner", "dirty target", "diverged", "filter", "switched winner", "detached target", "read-only", "equals target", "merge in progress"} {
		t.Run(name, func(t *testing.T) {
			repo := gitRepoFixture(t)
			linked := filepath.Join(t.TempDir(), "winner")
			worktreeGitOut(t, repo, "worktree", "add", "-b", "owned-winner", "--", linked, "main")
			fixtureWrite(t, linked, "README", "winning change\n")
			worktreeGitOut(t, linked, "add", "README")
			worktreeGitOut(t, linked, "commit", "-qm", "winner")
			m := New(WithConfinement([]string{t.TempDir()}, true))
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "merge-refusal")
			workspace, _ := launchTargetWorkspaces(t, m, tenant)
			wsRef := registerTestWorkspace(t, m, tenant, repo)
			state, mode := stateStopped, "dontAsk"
			switch name {
			case "running":
				state = stateRunning
			case "dirty winner":
				fixtureWrite(t, linked, "uncommitted", "preserve\n")
			case "dirty target":
				fixtureWrite(t, repo, "uncommitted", "preserve\n")
			case "diverged":
				fixtureWrite(t, repo, "main-only", "preserve\n")
				worktreeGitOut(t, repo, "add", ".")
				worktreeGitOut(t, repo, "commit", "-qm", "main advanced")
			case "filter":
				worktreeGitOut(t, repo, "config", "filter.hostile.smudge", "false")
			case "switched winner":
				worktreeGitOut(t, linked, "switch", "-c", "unowned")
			case "detached target":
				worktreeGitOut(t, repo, "switch", "--detach")
			case "read-only":
				mode = "plan"
			case "equals target":
				worktreeGitOut(t, repo, "branch", "-m", "foo=bar")
				worktreeGitOut(t, repo, "config", "--", "branch.foo=bar.mergeOptions", "--quit")
			case "merge in progress":
				fixtureWrite(t, repo, ".git/MERGE_HEAD", worktreeGitOut(t, repo, "rev-parse", "HEAD")+"\n")
			}
			seedLaunchTargetRun(t, m, tenant, workspace, "winner", nil, func(rec model.Record) {
				rec[colRunWorkspacePath], rec[colWorkspaceRef], rec[colRunWorktreeBranch] = linked, wsRef, "owned-winner"
				rec[colState], rec[colPermissionMode] = state, mode
			})
			before := worktreeGitOut(t, repo, "rev-parse", "HEAD")
			got := h.doJSON("POST", "/v1/m/sessions/runs/winner/git/merge", admin, map[string]any{}, tenantHdr(tenant))
			if got.code < 400 || got.code == http.StatusNotFound {
				t.Fatalf("unsafe merge %s: %d %s", name, got.code, got.raw)
			}
			if after := worktreeGitOut(t, repo, "rev-parse", "HEAD"); after != before {
				t.Fatal("refusal changed target HEAD")
			}
			if _, err := os.Stat(linked); err != nil {
				t.Fatalf("refusal removed worktree: %v", err)
			}
			if name == "merge in progress" {
				if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); err != nil {
					t.Fatalf("refusal changed existing merge: %v", err)
				}
			}
		})
	}
}

// This drives the complete server lifecycle with two normal session launches and
// real Git worktrees. The existing protocol fixture replaces only the provider;
// worktree creation, input admission, diffs, stop, merge and release are real.
func TestWorktreeMergeRaceJourney(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("Landlock unavailable")
	}
	repo := gitRepoFixture(t)
	fr := &fakeRunner{}
	m := New(WithConfinement([]string{t.TempDir()}, true), WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()))
	if err := m.UseSessionWorktrees(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "race-journey")
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "race-repo"}, tenantHdr(tenant))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace: %d %s", wr.code, wr.raw)
	}
	type candidate struct{ ref, dir, branch string }
	candidates := []candidate{}
	prompt := "Improve the README and commit the result."
	for _, name := range []string{"first", "second"} {
		fr.initSID = "race-" + model.NewID().String()
		run := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
			"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "transport": "stream-json", "permission_mode": "default", "isolation": "native",
			"workspace_ref": wr.body["workspace_ref"], "worktree": true, "name": name,
		}, tenantHdr(tenant))
		if run.code != http.StatusCreated {
			t.Fatalf("launch: %d %s", run.code, run.raw)
		}
		c := candidate{ref: run.body["run_ref"].(string), dir: run.body["workspace_path"].(string), branch: run.body["worktree_branch"].(string)}
		candidates = append(candidates, c)
		proc := fr.lastProc()
		q := queueRun{h: h, fr: fr, admin: admin, tenant: tenant, ref: c.ref, proc: proc}
		q.add(t, prompt)
		waitFor(t, "the race prompt reached the child", func() bool { return proc.sentCount() == 1 })
		if got := q.sentTexts(t); len(got) != 1 || got[0] != prompt {
			t.Fatalf("prompt changed: %v", got)
		}
		fixtureWrite(t, c.dir, "README", name+" result\n")
		worktreeGitOut(t, c.dir, "add", "README")
		worktreeGitOut(t, c.dir, "commit", "-qm", name+" result")
		diff := h.do("GET", "/v1/m/sessions/runs/"+c.ref+"/diff", admin, tenantHdr(tenant))
		if diff.code != http.StatusOK || diff.body["branch"] != c.branch {
			t.Fatalf("diff: %d %s", diff.code, diff.raw)
		}
		files, _ := diff.body["files"].([]any)
		if len(files) != 1 || files[0].(map[string]any)["path"] != "README" {
			t.Fatalf("changed files: %s", diff.raw)
		}
	}
	if candidates[0].dir == candidates[1].dir || candidates[0].branch == candidates[1].branch {
		t.Fatal("race sessions shared a worktree or branch")
	}
	for _, c := range candidates {
		stopped := h.doJSON("POST", "/v1/m/sessions/runs/"+c.ref+"/stop", admin, map[string]any{}, tenantHdr(tenant))
		if stopped.code != http.StatusOK || stopped.body["state"] != stateStopped {
			t.Fatalf("stop: %d %s", stopped.code, stopped.raw)
		}
	}
	winner, loser := candidates[0], candidates[1]
	merged := h.doJSON("POST", "/v1/m/sessions/runs/"+winner.ref+"/git/merge", admin, map[string]any{}, tenantHdr(tenant))
	if merged.code != http.StatusOK {
		t.Fatalf("keep: %d %s", merged.code, merged.raw)
	}
	cleanup := h.doJSON("POST", "/v1/m/sessions/runs/"+loser.ref+"/cleanup", admin, map[string]any{"discard_worktree": true}, tenantHdr(tenant))
	if cleanup.code != http.StatusOK || cleanup.body["state"] != stateCleaned {
		t.Fatalf("discard: %d %s", cleanup.code, cleanup.raw)
	}
	if raw, err := os.ReadFile(filepath.Join(repo, "README")); err != nil || string(raw) != "first result\n" {
		t.Fatalf("winner missing: %q %v", raw, err)
	}
	if _, err := os.Stat(loser.dir); !os.IsNotExist(err) {
		t.Fatalf("loser worktree remains: %v", err)
	}
	if exists, err := gitRefExists(t.Context(), repo, "refs/heads/"+loser.branch); err != nil || exists {
		t.Fatalf("loser branch remains: %v", err)
	}
	if _, err := os.Stat(winner.dir); err != nil {
		t.Fatalf("winner worktree lost: %v", err)
	}
}
