// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The providers surface must turn red when the catalog and a published page disagree,
// name what moved, and cover a translated copy as well as the English page.
func TestProvidersSurfaceFailsOnDrift(t *testing.T) {
	root := t.TempDir()
	page := "---\ntitle: x\n---\n\n" + beginMarker(providersRegionID) + "\n\n" + endMarker(providersRegionID) + "\n"
	for _, rel := range []string{providersPageRel, "docs-site/src/content/docs/es/reference/providers-and-tools.md"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(page), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rows := func(n int, baseURL string) *providersSurface {
		s := &providersSurface{}
		for i := 0; i < n; i++ {
			ref := "vendor-" + string(rune('a'+i))
			s.rows = append(s.rows, providerRow{Ref: ref, Title: "Vendor " + ref, Monogram: "V", BaseURL: baseURL + ref, Auth: "bearer",
				DocsURL: "https://docs.example/" + ref, Tools: []providerTool{{Key: "claude", Name: "Claude Code"}}})
		}
		return s
	}
	apply := func(s *providersSurface, write bool) []string {
		t.Helper()
		got, err := applyProviders(root, s, write)
		if err != nil {
			t.Fatal(err)
		}
		return got.findings
	}

	if f := apply(rows(6, "https://api.example/"), false); len(f) == 0 {
		t.Fatal("an empty region matched a catalog of 6 presets")
	}
	apply(rows(6, "https://api.example/"), true)
	if f := apply(rows(6, "https://api.example/"), false); len(f) != 0 {
		t.Fatalf("a regenerated tree still differs: %v", f)
	}
	if f := strings.Join(apply(rows(6, "https://moved.example/"), false), "\n"); !strings.Contains(f, "es/reference/providers-and-tools.md") || !strings.Contains(f, "no longer matches") {
		t.Fatalf("a changed endpoint was not reported on the translated copy: %q", f)
	}
	if f := strings.Join(apply(rows(5, "https://api.example/"), false), "\n"); !strings.Contains(f, "`vendor-f` is published") || !strings.Contains(f, "removed or renamed") {
		t.Fatalf("a removed preset was not named: %q", f)
	}
	noTool := rows(1, "https://api.example/")
	noTool.rows[0].Tools = nil
	if !strings.Contains(noTool.region(), "| None |") {
		t.Fatalf("a row that no tool runs on shows an empty cell:\n%s", noTool.region())
	}
}

// A dump that cannot be trusted is CANNOT LOOK, never an empty roster that matches.
func TestLoadProvidersRefusesBrokenDump(t *testing.T) {
	write := func(v any) string {
		b, _ := json.Marshal(v)
		p := filepath.Join(t.TempDir(), "providers.json")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, path := range map[string]string{
		"no path":        "",
		"missing file":   filepath.Join(t.TempDir(), "nope.json"),
		"foreign schema": write(map[string]any{"schema": "other/1", "presets": []any{}}),
		"below floor":    write(map[string]any{"schema": providersSchema, "presets": []providerRow{{Ref: "a"}}}),
	} {
		if _, err := loadProviders(path); !isCannotLook(err) {
			t.Errorf("%s: want CANNOT LOOK, got %v", name, err)
		}
	}
}
