// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Several entries of a folder, or a whole subfolder, come back as one zip through the same
// jail, size limit and DLP posture as a single read.
func TestWorkspaceFilesArchive(t *testing.T) {
	m := New(WithClassifier(fakeClassifier{trigger: "SECRET"}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	register := func(root string, body map[string]any) string {
		t.Helper()
		body["root_path"], body["name"] = root, filepath.Base(root)
		r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, body, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("register = %d %s", r.code, r.raw)
		}
		return r.body["workspace_ref"].(string)
	}
	archive := func(ref string, paths ...string) resp {
		t.Helper()
		q := url.Values{"path": paths}
		return h.do("GET", "/v1/m/sessions/workspaces/"+ref+"/files/archive?"+q.Encode(), admin, tenantHdr(tenant))
	}
	entries := func(r resp) map[string]string {
		t.Helper()
		if r.code != http.StatusOK || r.header.Get("Content-Type") != "application/zip" {
			t.Fatalf("archive = %d %q %s, want a 200 zip", r.code, r.header.Get("Content-Type"), r.raw)
		}
		zr, err := zip.NewReader(strings.NewReader(r.raw), int64(len(r.raw)))
		if err != nil {
			t.Fatalf("not a zip: %v", err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			out[f.Name] = string(b)
		}
		return out
	}
	names := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}

	root := t.TempDir()
	fixtureWrite(t, root, "a.txt", "alpha")
	fixtureWrite(t, root, "docs/b.txt", "bravo")
	fixtureWrite(t, root, "docs/deep/c.bin", "\xff\xfe\x00charlie")
	fixtureWrite(t, root, "other/b.txt", "dup")
	outside := t.TempDir()
	fixtureWrite(t, outside, "host-secret.txt", "not yours")
	if err := os.Symlink(outside, filepath.Join(root, "docs", "escape")); err != nil {
		t.Fatal(err)
	}
	ref := register(root, map[string]any{})

	t.Run("several entries and a folder, named from their own folder, links skipped", func(t *testing.T) {
		got := entries(archive(ref, "a.txt", "docs"))
		want := []string{"a.txt", "docs/", "docs/b.txt", "docs/deep/", "docs/deep/c.bin"}
		if g := names(got); strings.Join(g, ",") != strings.Join(want, ",") {
			t.Fatalf("entries = %v, want %v", g, want)
		}
		if got["a.txt"] != "alpha" || got["docs/deep/c.bin"] != "\xff\xfe\x00charlie" {
			t.Fatalf("content changed on the way: %q", got)
		}
		// One audit entry per file read, naming the path and never the content.
		reads := acctAuditsOf(t, m, tenant, "sessions.workspace.archive")
		if len(reads) != 3 || strings.Contains(fmt.Sprint(reads), "alpha") || !strings.Contains(fmt.Sprint(reads), "docs/deep/c.bin") {
			t.Fatalf("archive audit = %v, want one entry per file read", reads)
		}
	})

	t.Run("a read-only workspace still downloads", func(t *testing.T) {
		ro := register(root, map[string]any{"mount_mode": "ro"})
		if got := entries(archive(ro, "a.txt")); got["a.txt"] != "alpha" {
			t.Fatalf("entries = %v", got)
		}
	})

	t.Run("the request is refused, not shortened", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			paths []string
			want  int
		}{
			{"nothing named", nil, http.StatusBadRequest},
			{"outside the root", []string{"../" + filepath.Base(outside)}, http.StatusForbidden},
			{"through a link", []string{"docs/escape/host-secret.txt"}, http.StatusForbidden},
			{"missing", []string{"nope.txt"}, http.StatusNotFound},
			{"two entries with one name", []string{"docs/b.txt", "other/b.txt"}, http.StatusBadRequest},
			{"same entry twice", []string{"a.txt", "a.txt"}, http.StatusBadRequest},
		} {
			if r := archive(ref, c.paths...); r.code != c.want {
				t.Errorf("%s = %d %s, want %d", c.name, r.code, r.raw, c.want)
			}
		}
	})

	t.Run("a name a Windows extractor reads as a path is refused", func(t *testing.T) {
		odd := t.TempDir()
		fixtureWrite(t, odd, "d/..\\x.exe", "x")
		oref := register(odd, map[string]any{})
		for _, paths := range [][]string{{"d"}, {"d/..\\x.exe"}} {
			if r := archive(oref, paths...); r.code != http.StatusBadRequest {
				t.Errorf("archive %v = %d %s, want 400", paths, r.code, r.raw)
			}
		}
	})

	t.Run("a file past the workspace size limit refuses the whole archive", func(t *testing.T) {
		small := t.TempDir()
		fixtureWrite(t, small, "ok.txt", "tiny")
		fixtureWrite(t, small, "big.txt", strings.Repeat("x", 64))
		sref := register(small, map[string]any{"max_read_bytes": 32})
		if r := archive(sref, "ok.txt", "big.txt"); r.code != http.StatusRequestEntityTooLarge || !strings.Contains(r.raw, "big.txt") {
			t.Fatalf("archive = %d %s, want 413 naming big.txt", r.code, r.raw)
		}
	})

	t.Run("DLP applies as on a read", func(t *testing.T) {
		sens := t.TempDir()
		fixtureWrite(t, sens, "clean.txt", "fine")
		fixtureWrite(t, sens, "key.txt", "SECRET token")
		dref := register(sens, map[string]any{"dlp_mode": "deny"})
		readsBefore := len(acctAuditsOf(t, m, tenant, "sessions.workspace.archive"))
		if r := archive(dref, "clean.txt", "key.txt"); r.code != http.StatusForbidden || !strings.Contains(r.raw, "secret.credential") || strings.Contains(r.raw, "SECRET token") {
			t.Fatalf("deny mode = %d %s, want 403 naming the class and never the content", r.code, r.raw)
		}
		// The refusal is audited with its class; nothing was delivered, so no read is.
		denied := acctAuditsOf(t, m, tenant, "sessions.workspace.archive-denied")
		if len(denied) != 1 || !strings.Contains(fmt.Sprint(denied), "key.txt") || strings.Contains(fmt.Sprint(denied), "SECRET token") {
			t.Fatalf("archive-denied audit = %v, want one entry naming the file and its class", denied)
		}
		if reads := acctAuditsOf(t, m, tenant, "sessions.workspace.archive"); len(reads) != readsBefore {
			t.Fatalf("a refused archive left read audits: %d entries, had %d before it", len(reads), readsBefore)
		}
		if got := entries(archive(dref, "clean.txt")); got["clean.txt"] != "fine" {
			t.Fatalf("a clean file in deny mode = %v", got)
		}
		lref := register(sens, map[string]any{"dlp_mode": "label"})
		labeled := archive(lref, "clean.txt", "key.txt")
		if got := entries(labeled); got["key.txt"] != "SECRET token" {
			t.Fatalf("label mode = %v, want the file kept", got)
		}
		// The reader of a zip gets the labels a single read shows: classes only, never values.
		if got := labeled.header.Get("X-Olivares-Sensitivity"); got != "secret.credential" {
			t.Fatalf("X-Olivares-Sensitivity = %q, want the class found", got)
		}
		if got := archive(lref, "clean.txt").header.Get("X-Olivares-Sensitivity"); got != "" {
			t.Fatalf("a clean zip carries label %q", got)
		}
	})
}
