// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SES-RESUME-003: exercise the HTTP contract with a real store and filesystem;
// only the external tool process and its credential source are stand-ins.
func TestRuntimeAPIResumeOwnDirectory(t *testing.T) {
	for _, scenario := range []string{"existing", "removed", "root_moved"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeRunner{initSID: "resume-own-directory"}
			m := New(WithSessionWorkspaceRoot(root), WithRunner(runner), WithCredentialSource(staticCred()))
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "resume-directory")
			created := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant)}, tenantHdr(tenant))
			if created.code != http.StatusCreated {
				t.Fatalf("create = %d %s", created.code, created.raw)
			}
			ref, _ := created.body["run_ref"].(string)
			if ref == "" {
				t.Fatalf("missing run_ref: %s", created.raw)
			}
			dir := filepath.Join(root, ref)
			path := "/v1/m/sessions/runs/" + ref
			if created.body["workspace_path"] != dir || runner.lastSpec().Dir != dir {
				t.Fatalf("initial workspace = %v, runner directory = %q; want %q", created.body["workspace_path"], runner.lastSpec().Dir, dir)
			}
			if stopped := h.doJSON("POST", path+"/stop", admin, nil, tenantHdr(tenant)); stopped.code != http.StatusOK {
				t.Fatalf("stop = %d %s", stopped.code, stopped.raw)
			}
			if scenario == "removed" {
				if err := os.Remove(dir); err != nil {
					t.Fatalf("remove empty session directory: %v", err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("work to continue"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "root_moved" {
				if err := m.UseSessionWorkspaceRoot(t.TempDir()); err != nil {
					t.Fatalf("move session workspace root: %v", err)
				}
			}

			resumed := h.doJSON("POST", path+"/resume", admin, nil, tenantHdr(tenant))
			wantState := stateRunning
			wantLaunches := 2
			if scenario == "root_moved" {
				wantState, wantLaunches = stateStopped, 1
				refusal, _ := resumed.body["error"].(map[string]any)
				message, _ := refusal["message"].(string)
				if resumed.code != http.StatusConflict || !strings.Contains(message, "resume is refused rather than continued somewhere else") {
					t.Fatalf("resume after root moved = %d %s; want directory conflict", resumed.code, resumed.raw)
				}
			} else if resumed.code != http.StatusOK || resumed.body["run_ref"] != ref || resumed.body["workspace_path"] != dir || resumed.body["state"] != stateRunning {
				t.Fatalf("resume = %d %s; want same running session and directory", resumed.code, resumed.raw)
			}
			if got := launchCount(runner); got != wantLaunches {
				t.Fatalf("runner launches = %d, want %d", got, wantLaunches)
			}
			if got := runner.lastSpec().Dir; got != dir {
				t.Fatalf("runner directory = %q, want original %q", got, dir)
			}
			stored := h.do("GET", path, admin, tenantHdr(tenant))
			if stored.code != http.StatusOK || stored.body["state"] != wantState || stored.body["workspace_path"] != dir {
				t.Fatalf("stored run = %d %s; want %s in %q", stored.code, stored.raw, wantState, dir)
			}
			record, err := m.loadRun(context.Background(), tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			if !record.Bool(colRunWorkspaceDirOwned) {
				t.Fatal("resume lost the session directory ownership")
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("session directory after resume: %v", err)
			}
			if !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("session directory mode = %v, want directory with mode 0700", info.Mode())
			}
			if scenario != "removed" {
				contents, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
				if err != nil || string(contents) != "work to continue" {
					t.Fatalf("original session file changed: %q, %v", contents, err)
				}
			}
		})
	}
}
