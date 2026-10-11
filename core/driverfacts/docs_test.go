// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package driverfacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The how-to must describe the same kinds and endpoint restrictions as binding
// and launch. Check the published Markdown in every locale, not a fixture copy.
func TestAddProviderDocsMatchBindings(t *testing.T) {
	type readers struct{ any, custom []string }
	want := map[string]readers{}
	for _, facts := range All() {
		for _, binding := range facts.Bindings {
			row := want[binding.Kind]
			row.any = append(row.any, "`"+facts.Key+"`")
			if binding.Egress == EgressConfigured {
				row.custom = append(row.custom, "`"+facts.Key+"`")
			}
			want[binding.Kind] = row
		}
	}
	root := filepath.Join("..", "..", "docs-site", "src", "content", "docs")
	locales, err := filepath.Glob(filepath.Join(root, "*", "how-to", "add-a-provider.md"))
	if err != nil {
		t.Fatal(err)
	}
	pages := append([]string{filepath.Join(root, "how-to", "add-a-provider.md")}, locales...)
	for _, page := range pages {
		t.Run(page, func(t *testing.T) {
			data, err := os.ReadFile(page)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			start := strings.Index(text, "| `anthropic` |")
			if start < 0 {
				t.Fatal("provider compatibility table is missing")
			}
			prefix := strings.Split(strings.TrimSuffix(text[:start], "\n"), "\n")
			if len(prefix) < 2 || strings.Count(prefix[len(prefix)-2], "|") != 4 ||
				!strings.Contains(prefix[len(prefix)-2], "`base_url`") || strings.Count(prefix[len(prefix)-1], "|") != 4 {
				t.Error("compatibility header must include three columns with custom base_url")
			}
			seen := map[string]bool{}
			for _, line := range strings.Split(text[start:], "\n") {
				if !strings.HasPrefix(line, "|") {
					break
				}
				cells := strings.Split(line, "|")
				if len(cells) != 4 && len(cells) != 5 {
					t.Errorf("compatibility row needs kind, drivers and custom base_url drivers: %s", line)
					continue
				}
				kind := strings.Trim(strings.TrimSpace(cells[1]), "`")
				row, ok := want[kind]
				if !ok || seen[kind] {
					t.Errorf("unknown or duplicate provider kind %q", kind)
					continue
				}
				seen[kind] = true
				for i, drivers := range [][]string{row.any, row.custom} {
					if i+2 == len(cells)-1 {
						t.Errorf("%s omits custom base_url drivers", kind)
						continue
					}
					if got, expected := strings.TrimSpace(cells[i+2]), strings.Join(drivers, ", "); got != expected {
						t.Errorf("%s column %d = %q, want %q from Bindings", kind, i+2, got, expected)
					}
				}
			}
			for kind := range want {
				if !seen[kind] {
					t.Errorf("missing compatibility row for %s", kind)
				}
			}
			// The provider glossary precedes the compatibility table.
			glossaryFound := false
			for _, line := range strings.Split(text[:start], "\n") {
				if strings.HasPrefix(line, "| **") && strings.Contains(line, "`anthropic`") {
					glossaryFound = true
					for kind := range want {
						if !strings.Contains(line, "`"+kind+"`") {
							t.Errorf("provider glossary omits %s", kind)
						}
					}
				}
			}
			if !glossaryFound {
				t.Error("provider glossary is missing")
			}
		})
	}
}
