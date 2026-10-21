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

// --mode sends exactly what the person typed as mount_mode, so a new mode needs no
// CLI table, and the default stays rw.
func TestWorkspaceAddSendsTheChosenMountMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"write without delete", []string{"--mode", "write_no_delete"}, "write_no_delete"},
		{"read only", []string{"--mode", "ro"}, "ro"},
		{"default", nil, "rw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/m/sessions/workspaces" {
					t.Errorf("workspace add used %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["mount_mode"] != tc.want || body["root_path"] != "/srv/project" {
					t.Errorf("POST body = %v, want mount_mode=%s root_path=/srv/project", body, tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"workspace_ref": "ws-mode", "root_path": "/srv/project", "mount_mode": body["mount_mode"], "dlp_mode": "label"})
			}))
			defer server.Close()
			args := append([]string{"agent", "workspace", "add", "/srv/project", "--server", server.URL, "--token", "olvs_workspace-fixture", "--tenant", "tenant-a", "--insecure", "-o", "json"}, tc.flags...)
			out, stderr, err := execRoot(t, args...)
			if err != nil {
				t.Fatalf("workspace add: %v; %s", err, stderr)
			}
			if !strings.Contains(out, `"mount_mode": "`+tc.want+`"`) && !strings.Contains(out, `"mount_mode":"`+tc.want+`"`) {
				t.Fatalf("output does not show mount_mode %s: %s", tc.want, out)
			}
			if calls != 1 {
				t.Fatalf("POST calls=%d want 1", calls)
			}
		})
	}
}

func TestWorkspaceAddHelpNamesWriteNoDelete(t *testing.T) {
	out, stderr, err := execRoot(t, "agent", "workspace", "add", "--help")
	if err != nil || !strings.Contains(out+stderr, "write_no_delete") {
		t.Fatalf("workspace add --help does not name write_no_delete: %v\n%s%s", err, out, stderr)
	}
}
