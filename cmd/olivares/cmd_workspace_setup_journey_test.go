// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The CLI and booted engine use real HTTP, SQLite and git. Only the provider
// process is the existing labelled protocol fixture; setup never executes.
func TestWorkspaceSetupCLIJourneyUsesEngineApprovalAndStartGuard(t *testing.T) {
	s := bootStubSessionEngine(t, false)
	if err := os.WriteFile(filepath.Join(s.folder, "README"), []byte("setup journey\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "README"}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command("git", append([]string{"-C", s.folder, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	ws := s.do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": s.folder, "dlp_mode": "off"}, http.StatusCreated)
	ref := ws["workspace_ref"].(string)
	profile := s.do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "config_home": s.home, "user_home": s.home, "auth_source": "provider_account_home", "display_name": "setup fixture"}, http.StatusCreated)
	login := s.do("POST", "/v1/auth/login", map[string]any{"email": "dormant@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
	tokenFile := filepath.Join(t.TempDir(), "operator-token")
	if err := os.WriteFile(tokenFile, []byte(login["token"].(string)), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(s.eng.api.Handler())
	defer server.Close()
	run := func(args ...string) (string, error) {
		t.Helper()
		args = append(args, "--server", server.URL, "--tenant", s.tenant, "--token-file", tokenFile, "--insecure")
		out, stderr, err := execRoot(t, args...)
		if err != nil && stderr != "" {
			t.Log("CLI refused request")
		}
		return out, err
	}
	success := func(args ...string) map[string]any {
		t.Helper()
		args = append(args, "-o", "json")
		out, err := run(args...)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatalf("CLI response: %v %s", err, out)
		}
		return body
	}
	marker := filepath.Join(s.folder, "must-not-run")
	command := "touch " + marker
	set := func(text string) map[string]any {
		t.Helper()
		return success("agent", "workspace", "setup", ref, "--command", text)
	}
	set(command)
	read := success("agent", "workspace", "setup", ref)
	if read["setup_command"] != command {
		t.Fatalf("command changed: %v", read["setup_command"])
	}
	hash := read["setup_command_sha256"].(string)
	start := []string{"session", "start", s.folder, "--tool", "claude", "--profile", profile["profile_ref"].(string), "--worktree", "--permission", "ask"}
	refuse := func() {
		t.Helper()
		if _, err := run(start...); err == nil || exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "approve-setup") {
			t.Fatalf("unapproved command started or lost next step: %v", err)
		}
	}
	refuse()
	success("agent", "workspace", "approve-setup", ref, "--sha256", hash)
	started := success(start...)
	s.do("POST", "/v1/m/sessions/runs/"+started["run_ref"].(string)+"/stop", nil, http.StatusOK)
	changed := set(command + " ")
	if out, err := run("agent", "workspace", "setup", ref); err != nil || !strings.Contains(out, "Approval: required") {
		t.Fatalf("changed text still displayed as approved: %v %s", err, out)
	}
	refuse()
	if _, err := run("agent", "workspace", "approve-setup", ref, "--sha256", hash); err == nil || exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("stale text approved: %v", err)
	}
	success("agent", "workspace", "approve-setup", ref, "--sha256", changed["setup_command_sha256"].(string))
	started = success(start...)
	s.do("POST", "/v1/m/sessions/runs/"+started["run_ref"].(string)+"/stop", nil, http.StatusOK)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("setup command executed: %v", err)
	}
}
