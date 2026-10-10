// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkspaceRegistrationWriteAccess(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user and Unix directory permissions")
	}
	for _, tc := range []struct {
		name  string
		mode  os.FileMode
		mount string
		link  bool
		want  int
	}{
		{"unwritable_rw", 0o555, mountRW, false, http.StatusForbidden},
		{"unwritable_default", 0o555, "", false, http.StatusForbidden},
		{"unsearchable_rw", 0o600, mountRW, false, http.StatusForbidden},
		{"unwritable_symlink", 0o555, mountRW, true, http.StatusForbidden},
		{"unwritable_ro", 0o555, mountRO, false, http.StatusCreated},
		{"writable_rw", 0o700, mountRW, false, http.StatusCreated},
		{"writable_default", 0o700, "", false, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, New())
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "write-access")
			root := t.TempDir()
			if err := os.Chmod(root, tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(root, 0o700); err != nil {
					t.Error(err)
				}
			})
			folder := root
			if tc.link {
				folder = filepath.Join(t.TempDir(), "entered-folder")
				if err := os.Symlink(root, folder); err != nil {
					t.Fatal(err)
				}
			}
			r := h.doJSON(http.MethodPost, "/v1/m/sessions/workspaces", admin,
				map[string]any{"root_path": folder, "mount_mode": tc.mount}, tenantHdr(tenant))
			if r.code != tc.want {
				t.Fatalf("registration = %d %s, want %d", r.code, r.raw, tc.want)
			}
			if tc.want == http.StatusForbidden {
				body, _ := r.body["error"].(map[string]any)
				message, _ := body["message"].(string)
				for _, part := range []string{folder, "not writable", fmt.Sprintf("UID %d", os.Geteuid()), "Grant", "write and search access"} {
					if !strings.Contains(message, part) {
						t.Errorf("refusal %q must contain %q", message, part)
					}
				}
				if tc.link && strings.Contains(message, root) {
					t.Errorf("refusal exposes the symlink target: %q", message)
				}
			} else {
				mount := tc.mount
				if mount == "" {
					mount = mountRW
				}
				if r.body["mount_mode"] != mount || r.body["root_path"] != root {
					t.Fatalf("registration changed the requested mount or root: %s", r.raw)
				}
			}
			list := h.do(http.MethodGet, "/v1/m/sessions/workspaces", admin, tenantHdr(tenant))
			items, _ := list.body["items"].([]any)
			wantRows := 0
			if tc.want == http.StatusCreated {
				wantRows = 1
			}
			if list.code != http.StatusOK || len(items) != wantRows {
				t.Fatalf("registry after registration = %d %s, want %d rows", list.code, list.raw, wantRows)
			}
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("registration left filesystem entries: %v, err=%v", entries, err)
			}
		})
	}
}
