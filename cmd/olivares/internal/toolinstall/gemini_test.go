// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func geminiArchive(t *testing.T, extra string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range map[string]string{"gemini.js": "#!/usr/bin/env node\nconsole.log('1.2.3')\n", "chunk-fixture.js": "export const fixture = true;\n", extra: "fixture"} {
		if name == "" {
			continue
		}
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestGeminiReleaseBundlePlacement(t *testing.T) {
	payload := geminiArchive(t, "docs/readme.md")
	const repo = "google-gemini/gemini-cli"
	const asset = "gemini-cli-bundle.zip"
	meta := []byte(`{"tag_name":"v1.2.3","assets":[{"name":"` + asset + `","size":` + strconv.Itoa(len(payload)) + `,"digest":"sha256:` + sha256Hex(payload) + `","browser_download_url":"https://github.com/` + repo + `/releases/download/v1.2.3/` + asset + `"}]}`)
	client := &http.Client{Transport: archiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := meta
		if strings.HasSuffix(r.URL.Path, asset) {
			body = payload
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	p := newReleaseArchive("gemini-cli", ReleaseArchiveOptions{Client: client})
	dir := t.TempDir()
	plan, _, err := p.ResolveV2(context.Background(), RequestV2{Driver: "gemini-cli", Version: "1.2.3", Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: filepath.Join(dir, "tools")})
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "payload"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	inv, err := p.PlaceV2(context.Background(), plan, root, "stage", "payload")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin/gemini", "lib/gemini/gemini.js", "lib/gemini/chunk-fixture.js"} {
		if _, err := root.Stat(filepath.Join("stage", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if len(inv.Members) < 5 {
		t.Fatalf("incomplete bundle inventory: %+v", inv)
	}
}

func TestGeminiBundleRejectsUnsafePathsAndLinks(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "docs/../../escape", "bad\\path"} {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		payload := geminiArchive(t, name)
		if err := os.WriteFile(filepath.Join(dir, "payload"), payload, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := extractGeminiBundle(t.Context(), root, "stage", "payload", releaseArchiveLayout(DriverGemini)); KindOf(err) != KindManifestInvalid {
			t.Fatalf("unsafe path %q was not rejected as an invalid bundle before placement: %v", name, err)
		}
		root.Close()
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "gemini.js"}
	h.SetMode(os.ModeSymlink | 0777)
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, "../../escape"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "payload"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := extractGeminiBundle(t.Context(), root, "stage", "payload", releaseArchiveLayout(DriverGemini)); KindOf(err) != KindManifestInvalid {
		t.Fatalf("ZIP symlink was not rejected as an invalid bundle: %v", err)
	}
}
