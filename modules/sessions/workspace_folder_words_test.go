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

func TestWorkspaceRefusalsNameTheFolder(t *testing.T) {
	t.Parallel()
	m, _, tenant, _ := newRuntimeHarness(t)
	missing := filepath.Join(t.TempDir(), "no-such-folder")
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for root, want := range map[string]string{
		missing: "The folder " + missing + " does not exist or cannot be opened on this server. Choose an existing folder.",
		file:    file + " is a file, not a folder.",
	} {
		_, err := m.createWorkspace(context.Background(), tenant, CreateWorkspaceParams{RootPath: root, Actor: "user:u1", ActorKind: "user"})
		if statusOf(err) != http.StatusBadRequest || err.Error() != want || strings.Contains(err.Error(), "root_path") {
			t.Fatalf("folder %s = %v, want 400 %q", root, err, want)
		}
	}
}

// Even a dangling symlink must name only the entered folder, never its private
// server target, and explain how to recover from the failed registration.
func TestWorkspaceFolderRefusalIsActionableWithoutServerPath(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "folder-refusal")
	missing := filepath.Join(t.TempDir(), "entered-folder")
	serverPath := filepath.Join(t.TempDir(), "private-server-folder", "missing")
	link := filepath.Join(t.TempDir(), "entered-link")
	if err := os.Symlink(serverPath, link); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{missing, link} {
		r := h.doJSON(http.MethodPost, "/v1/m/sessions/workspaces", admin,
			map[string]any{"root_path": folder}, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Fatalf("folder refusal = %d %s, want 400", r.code, r.raw)
		}
		body, _ := r.body["error"].(map[string]any)
		want := "The folder " + folder + " does not exist or cannot be opened on this server. Choose an existing folder."
		if body["message"] != want || strings.Contains(r.raw, serverPath) || strings.Contains(r.raw, "root_path") {
			t.Fatalf("folder refusal = %s, want only the entered folder and recovery step", r.raw)
		}
	}
}
