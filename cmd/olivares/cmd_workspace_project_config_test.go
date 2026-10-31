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

const projectConfigTestHash = "6a52db35cac4899fdf7ec2cfeea6900950b810f8b2e6044b5b044432901ac2c9"

// The CLI reads the report of a folder and approves exactly the hash it is
// given; each call is one request to the route the engine publishes.
func TestWorkspaceProjectConfigCLIReadsAndApprovesExactHash(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, path string
		body         map[string]any
		reply        map[string]any
		want         string
	}{
		{
			args: []string{"project-config", "ws-project"}, method: "GET", path: "/project-config",
			reply: map[string]any{"workspace_ref": "ws-project", "sha256": projectConfigTestHash, "approved": false, "files": []any{
				map[string]any{"file": ".claude/settings.json", "flagged": map[string]any{"hooks": map[string]any{"Stop": []any{}}, "env.ANTHROPIC_BASE_URL": "http://h\x1b]52;c;eA==\x07"}},
			}},
			want: ".claude/settings.json",
		},
		{
			// #3076: an entry no approval can let be used says why.
			args: []string{"project-config", "ws-project"}, method: "GET", path: "/project-config",
			reply: map[string]any{"workspace_ref": "ws-project", "sha256": projectConfigTestHash, "approved": false, "files": []any{
				map[string]any{"file": ".opencode", "refused": "holds more than 2048 files"},
			}},
			want: "cannot be approved: holds more than 2048 files",
		},
		{
			args: []string{"approve-project-config", "ws-project", "--sha256", projectConfigTestHash}, method: "POST", path: "/project-config/approve",
			body:  map[string]any{"sha256": projectConfigTestHash},
			reply: map[string]any{"sha256": projectConfigTestHash, "actor": "user:u1", "at": "2026-10-14T12:00:00Z"},
			want:  "Approved " + projectConfigTestHash,
		},
	} {
		t.Run(tc.method, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != "/v1/m/sessions/workspaces/ws-project"+tc.path {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				if tc.body != nil {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["sha256"] != tc.body["sha256"] {
						t.Errorf("body=%v err=%v", body, err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tc.reply)
			}))
			defer server.Close()
			args := append([]string{"agent", "workspace"}, tc.args...)
			args = append(args, "--server", server.URL, "--token", "olvs_project-config-fixture", "--tenant", "tenant-a", "--insecure")
			out, stderr, err := execRoot(t, args...)
			if err != nil {
				t.Fatalf("CLI: %v %s", err, stderr)
			}
			if !strings.Contains(out, tc.want) || !strings.Contains(out, projectConfigTestHash) || strings.ContainsRune(out, 0x1b) {
				t.Fatalf("output=%q", out)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestWorkspaceProjectConfigCLIRequiresTheHashToApprove(t *testing.T) {
	if _, _, err := execRoot(t, "agent", "workspace", "approve-project-config", "ws-project"); err == nil {
		t.Fatal("approve without --sha256 was accepted")
	}
}

// A folder with nothing flagged reads as such, and a refusal from the engine
// reaches the person as the engine wrote it.
func TestWorkspaceProjectConfigCLIShowsEmptyReportAndEngineRefusal(t *testing.T) {
	const sentence = "This folder carries claude configuration (.claude/settings.json) that can run programs or redirect traffic and no one approved it; review it with olivares agent workspace project-config ws-project, then run olivares agent workspace approve-project-config ws-project --sha256 " + projectConfigTestHash + "."
	reply := map[string]any{"workspace_ref": "ws-project", "sha256": "", "approved": false, "files": []any{}}
	status := http.StatusOK
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(reply)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": sentence})
	}))
	defer server.Close()
	args := []string{"agent", "workspace", "project-config", "ws-project", "--server", server.URL, "--token", "olvs_project-config-fixture", "--tenant", "tenant-a", "--insecure"}
	out, stderr, err := execRoot(t, args...)
	if err != nil || !strings.Contains(out, "No agent configuration") {
		t.Fatalf("empty report: out=%q err=%v %s", out, err, stderr)
	}
	status = http.StatusConflict
	_, stderr, err = execRoot(t, args...)
	if err == nil || !strings.Contains(stderr+err.Error(), "approve-project-config") {
		t.Fatalf("refusal not shown: err=%v stderr=%q", err, stderr)
	}
}
