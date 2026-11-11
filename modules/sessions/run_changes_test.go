// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestRunChangesStayInsideTheFolder: the session's changes are the files modified since
// the run started; reads never leave the folder, by path or by symlink.
func TestRunChangesStayInsideTheFolder(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write := func(p, body string, at time.Time) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, at, at); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().Add(-time.Hour)
	write("old.txt", "before the run", start.Add(-time.Hour))
	write("notes.md", "- first\n", start.Add(time.Minute))
	write("src/main.go", "package main\n", start.Add(2*time.Minute))
	write(".git/index", "vcs", start.Add(3*time.Minute))
	write("blob.bin", "a\x00b", start.Add(4*time.Minute))
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("engine secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	files, truncated, err := listChanges(root, start)
	if err != nil || truncated {
		t.Fatalf("listChanges = %v, %v", truncated, err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	want := []string{"blob.bin", "src/main.go", "notes.md"} // newest first; no .git, no old file, no link
	if len(got) != len(want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("changes = %v, want %v", got, want)
		}
	}

	if f, err := readChangedFile(root, "notes.md"); err != nil || f.Text != "- first\n" || f.Binary {
		t.Fatalf("notes.md = %+v, %v", f, err)
	}
	if f, err := readChangedFile(root, "blob.bin"); err != nil || !f.Binary || f.Text != "" {
		t.Fatalf("blob.bin = %+v, %v", f, err)
	}
	for _, bad := range []string{"", "/etc/passwd", "../" + filepath.Base(outside) + "/secret", "..", "link"} {
		if _, err := readChangedFile(root, bad); err == nil {
			t.Errorf("read %q left the folder or followed a link", bad)
		} else if bad == "link" && !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("symlink read error = %v, want invalid", err)
		}
	}
}

// A session's outputs are the files it made or changed in its folder (blueprint API 9:
// name, size and modification time), without the Markdown copy the engine writes beside a
// document it converts for a message, so a result list shows what the person asked for.
func TestRunOutputsListWhatTheSessionMade(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	workspace, _ := launchTargetWorkspaces(t, m, tenant)
	dir := t.TempDir()
	fixtureWrite(t, dir, "before.txt", "there before the session\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "before.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	seedLaunchTargetRun(t, m, tenant, workspace, "run-out", nil, func(rec model.Record) { rec[colRunWorkspacePath] = dir })
	copyName := "brief.pdf." + strings.Repeat("Q", 26) + ".md"
	fixtureWrite(t, dir, "brief.pdf", "%PDF the person's document")
	fixtureWrite(t, dir, copyName, "# the engine's copy")
	fixtureWrite(t, dir, "out/informe.xlsx", "PK the result")
	// The same shape with no document beside it is the session's own file.
	ownName := "plan." + strings.Repeat("A", 26) + ".md"
	fixtureWrite(t, dir, ownName, "# written by the agent")

	r := h.do(http.MethodGet, "/v1/m/sessions/runs/run-out/outputs", admin, tenantHdr(tenant))
	files, _ := r.body["files"].([]any)
	var names []string
	for _, f := range files {
		e, _ := f.(map[string]any)
		if size, _ := e["size"].(float64); size <= 0 || e["modified_at"] == "" {
			t.Errorf("output %v lacks its size or time", e)
		}
		names = append(names, e["name"].(string))
	}
	slices.Sort(names)
	if want := []string{"brief.pdf", "out/informe.xlsx", ownName}; r.code != http.StatusOK || !slices.Equal(names, want) {
		t.Fatalf("outputs = %d %v, want 200 %v (%s)", r.code, names, want, r.raw)
	}
	if r := h.do(http.MethodGet, "/v1/m/sessions/runs/no-such-run/outputs", admin, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("outputs of a missing run = %d, want 404", r.code)
	}
}
