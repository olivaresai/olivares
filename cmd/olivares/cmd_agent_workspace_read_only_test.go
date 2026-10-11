// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceReadOnlyFoldersCLIReplacesAndClears(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		folders []string
		status  int
	}{
		{"replace", []string{"--read-only-folder", "/srv/toolchains/go,stable", "--read-only-folder", "/srv/go/modules"}, []string{"/srv/toolchains/go,stable", "/srv/go/modules"}, http.StatusOK},
		{"clear", []string{"--read-only-folder="}, []string{}, http.StatusOK},
		{"administrator required", []string{"--read-only-folder", "/srv/go/modules"}, []string{"/srv/go/modules"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPatch || r.URL.Path != "/v1/m/sessions/workspaces/ws-tooling" || r.Header.Get("Authorization") != "Bearer olvs_workspace-fixture" || r.Header.Get("X-Olivares-Tenant") != "tenant-a" {
					t.Error("workspace update used the wrong route or authority")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var folders []string
				if err := json.Unmarshal(body["read_only_folders"], &folders); err != nil {
					t.Error(err)
					return
				}
				if len(body) != 1 || !reflect.DeepEqual(folders, tc.folders) {
					t.Errorf("PATCH body = %v, folders = %#v; want ONLY read_only_folders = %#v", body, folders, tc.folders)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status != http.StatusOK {
					_, _ = w.Write([]byte(`{"error":"workspace administrator permission required"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"workspace_ref": "ws-tooling", "root_path": "/srv/project", "mount_mode": "rw", "dlp_mode": "label", "read_only_folders": folders})
			}))
			defer server.Close()
			args := append([]string{"agent", "workspace", "ws-tooling", "--server", server.URL, "--token", "olvs_workspace-fixture", "--tenant", "tenant-a", "--insecure", "-o", "json"}, tc.flags...)
			out, stderr, err := execRoot(t, args...)
			if tc.status != http.StatusOK {
				if err == nil || !strings.Contains(err.Error(), "workspace administrator permission required") {
					t.Fatalf("server refusal lost: error=%v stdout=%s stderr=%s", err, out, stderr)
				}
			} else {
				if err != nil {
					t.Fatalf("workspace configuration: %v; %s", err, stderr)
				}
				var updated struct {
					Folders []string `json:"read_only_folders"`
				}
				if err := json.Unmarshal([]byte(out), &updated); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(updated.Folders, tc.folders) {
					t.Fatalf("returned folders=%#v want %#v", updated.Folders, tc.folders)
				}
			}
			if calls != 1 {
				t.Fatalf("PATCH calls=%d want 1", calls)
			}
		})
	}
}
