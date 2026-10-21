// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceSetupCLIStoresReadsAndApprovesExactHash(t *testing.T) {
	const command = "npm ci "
	const hash = "6a52db35cac4899fdf7ec2cfeea6900950b810f8b2e6044b5b044432901ac2c9"
	for _, tc := range []struct {
		args                     []string
		method, path, key, value string
	}{
		{[]string{"setup", "ws-project", "--command", command}, "PUT", "/setup-command", "command", command},
		{[]string{"setup", "ws-project"}, "GET", "", "", ""},
		{[]string{"approve-setup", "ws-project", "--sha256", hash}, "POST", "/setup-command/approve", "sha256", hash},
	} {
		t.Run(tc.method, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != "/v1/m/sessions/workspaces/ws-project"+tc.path {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				if tc.key != "" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 1 || body[tc.key] != tc.value {
						t.Errorf("body=%v", body)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"workspace_ref": "ws-project", "setup_command": command, "setup_command_sha256": hash})
			}))
			defer server.Close()
			args := append([]string{"agent", "workspace"}, tc.args...)
			args = append(args, "--server", server.URL, "--token", "olvs_setup-fixture", "--tenant", "tenant-a", "--insecure", "-o", "json")
			out, stderr, err := execRoot(t, args...)
			if err != nil {
				t.Fatalf("CLI: %v %s", err, stderr)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(out), &body); err != nil || body["setup_command"] != command {
				t.Fatalf("readback=%s err=%v", out, err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestSessionStartNoSetupIsAnExplicitOneStartOption(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, stderr, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--worktree", "--no-setup"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("start: %v %s", err, stderr)
	}
	posts := f.postsTo(sessionRunsPath)
	if len(posts) != 1 || posts[0]["no_setup"] != true {
		t.Fatalf("start=%v", posts)
	}
}

func TestWorkspaceSetupCLIRejectsInvalidUTF8BeforeContactingEngine(t *testing.T) {
	_, _, err := execRoot(t, "agent", "workspace", "setup", "ws-project", "--command", "echo \xff")
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid command was not rejected before resolution: %v", err)
	}
}

func TestWorkspaceSetupCLIEmptyCommandClearsInsteadOfReading(t *testing.T) {
	var method string
	var body map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"workspace_ref": "ws-project", "setup_command": "", "setup_command_sha256": ""})
	}))
	defer server.Close()
	out, stderr, err := execRoot(t, "agent", "workspace", "setup", "ws-project", "--command", "", "--server", server.URL, "--token", "olvs_setup-fixture", "--tenant", "tenant-a", "--insecure")
	if err != nil {
		t.Fatalf("CLI: %v %s", err, stderr)
	}
	if method != http.MethodPut || body["command"] != "" || len(body) != 1 || !strings.Contains(out, "No setup command.") {
		t.Fatalf("method=%s body=%v out=%q", method, body, out)
	}
}

func TestWorkspaceSetupCLIShowsApprovalOnlyWhileTheServerSaysItCoversTheText(t *testing.T) {
	for _, tc := range []struct {
		approved bool
		want     string
	}{
		{true, "Approval: approved by fran at 2026-10-14T10:00:00Z"},
		{false, "Approval: required"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"workspace_ref": "ws-project", "setup_command": "npm ci", "setup_command_sha256": "00",
					"setup_approved": tc.approved,
					"setup_approval": map[string]any{"sha256": "00", "actor": "fran", "at": "2026-10-14T10:00:00Z"},
				})
			}))
			defer server.Close()
			out, stderr, err := execRoot(t, "agent", "workspace", "setup", "ws-project", "--server", server.URL, "--token", "olvs_setup-fixture", "--tenant", "tenant-a", "--insecure")
			if err != nil {
				t.Fatalf("CLI: %v %s", err, stderr)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("out=%q, want %q", out, tc.want)
			}
		})
	}
}
