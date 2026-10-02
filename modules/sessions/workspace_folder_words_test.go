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

// SC 09b item 7: a folder that does not exist answered "root_path does not exist or
// is not resolvable" on the New session form. The refusal names the folder the
// person typed, in their terms; the field name never reaches the sentence.
func TestWorkspaceRefusalsNameTheFolder(t *testing.T) {
	t.Parallel()
	m, _, tenant, _ := newRuntimeHarness(t)
	missing := filepath.Join(t.TempDir(), "no-such-folder")
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for root, want := range map[string]string{
		missing: "The folder " + missing + " does not exist or cannot be opened on this server.",
		file:    file + " is a file, not a folder.",
	} {
		_, err := m.createWorkspace(context.Background(), tenant, CreateWorkspaceParams{RootPath: root, Actor: "user:u1", ActorKind: "user"})
		if statusOf(err) != http.StatusBadRequest || err.Error() != want || strings.Contains(err.Error(), "root_path") {
			t.Fatalf("folder %s = %v, want 400 %q", root, err, want)
		}
	}
}
