// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorkspaceRegistrationReadOnlyFilesystem(t *testing.T) {
	// Linux exposes sysfs read-only in the test containers. Check the actual mount,
	// so this test never attempts a write on a writable system directory.
	var stat unix.Statfs_t
	if err := unix.Statfs("/sys", &stat); err != nil || stat.Flags&unix.ST_RDONLY == 0 {
		t.Skip("requires a read-only /sys mount")
	}
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "readonly-filesystem")
	r := h.doJSON(http.MethodPost, "/v1/m/sessions/workspaces", admin,
		map[string]any{"root_path": "/sys", "mount_mode": mountRW}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("read-only filesystem registration = %d %s, want 400", r.code, r.raw)
	}
	body, _ := r.body["error"].(map[string]any)
	message, _ := body["message"].(string)
	if !strings.Contains(message, "/sys") || !strings.Contains(message, "filesystem") || strings.Contains(message, "Grant") {
		t.Fatalf("read-only filesystem refusal must name the folder and filesystem remedy: %q", message)
	}
	_, err := h.m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: "/sys", MountMode: mountRW})
	if !errors.Is(err, unix.EROFS) {
		t.Fatalf("read-only filesystem refusal discarded the OS error: %v", err)
	}
}
