// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The "Providers and tools" page: one table of the provider catalog, which vendor
// endpoint each tool can be held to. GENERATED from connectors/modelprovider's preset
// table and from the engine's own rule for which tool serves which record
// (modules/sessions recordServesDriver), so a new vendor row or a changed rule shows up
// here or turns the gate red. Both live in Go, so stage 1b of check-guide-docs.sh runs
// TestProvidersDump (modules/sessions/providers_dump_test.go) and hands this file its
// JSON, the way check-cli-ref-docs.sh does for the command tree.
//
// Every copy of the page carries the same region. Like the CLI reference, the region is
// not translated: its cells are refs, URLs and tool names. Translated copies keep their
// own prose, and a locale without the page is lint:docs-parity's finding, not this one.
const (
	providersPageRel    = "docs-site/src/content/docs/reference/providers-and-tools.md"
	providersLocaleGlob = "docs-site/src/content/docs/*/reference/providers-and-tools.md"
	providersRegionID   = "olivares-providers-tools"
	providersSchema     = "olivares.provider-presets/1"

	// providersFloor: today's catalog has 11 rows that run a tool. An empty or tiny dump
	// must not match a page regenerated from the same breakage.
	providersFloor = 5
)

type providerTool struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type providerRow struct {
	Ref      string         `json:"ref"`
	Title    string         `json:"title"`
	Monogram string         `json:"monogram"`
	BaseURL  string         `json:"base_url"`
	Auth     string         `json:"auth"`
	DocsURL  string         `json:"docs_url"`
	KeyURL   string         `json:"key_url"`
	Governed bool           `json:"governed"`
	Tools    []providerTool `json:"tools"`
}

type providersSurface struct{ rows []providerRow }

// loadProviders reads the stage-1b dump. An absent, unreadable, foreign-schema or tiny
// dump is CANNOT LOOK: "I could not enumerate" is never "in sync".
func loadProviders(path string) (*providersSurface, error) {
	if path == "" {
		return nil, cannot("no provider dump was given (-providers), so the catalog was never read")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, cannot("the provider dump could not be read (%v)", err)
	}
	var dump struct {
		Schema  string        `json:"schema"`
		Presets []providerRow `json:"presets"`
	}
	if err := json.Unmarshal(raw, &dump); err != nil {
		return nil, cannot("the provider dump is not JSON (%v)", err)
	}
	if dump.Schema != providersSchema {
		return nil, cannot("the provider dump declares schema %q, this gate reads %q", dump.Schema, providersSchema)
	}
	s := &providersSurface{}
	for _, r := range dump.Presets {
		if !r.Governed {
			s.rows = append(s.rows, r)
		}
	}
	if len(s.rows) < providersFloor {
		return nil, cannot("the provider dump lists %d tool rows, below the floor of %d; the walk is broken", len(s.rows), providersFloor)
	}
	return s, nil
}

func (s *providersSurface) keys() []string {
	keys := make([]string, 0, len(s.rows))
	for _, r := range s.rows {
		keys = append(keys, "`"+r.Ref+"`")
	}
	return keys
}

func providerAuth(scheme string) string {
	switch scheme {
	case "bearer":
		return "Bearer token"
	case "x-api-key":
		return "`x-api-key` header"
	}
	return "`" + scheme + "`"
}

func (s *providersSurface) region() string {
	var b strings.Builder
	b.WriteString("| Vendor | Preset | Runs in | Endpoint | Auth | Links |\n|---|---|---|---|---|---|\n")
	for _, r := range s.rows {
		tools := make([]string, 0, len(r.Tools))
		for _, t := range r.Tools {
			tools = append(tools, fmt.Sprintf("%s (`%s`)", mdCell(t.Name), t.Key))
		}
		if len(tools) == 0 {
			tools = append(tools, "None") // a row no tool can be held to still shows, and visibly
		}
		links := []string{"[Docs](" + r.DocsURL + ")"}
		if r.KeyURL != "" {
			links = append(links, "[Get a key]("+r.KeyURL+")")
		}
		fmt.Fprintf(&b, "| `%s` %s | `%s` | %s | `%s` | %s | %s |\n",
			r.Monogram, mdCell(r.Title), r.Ref, strings.Join(tools, ", "), r.BaseURL, providerAuth(r.Auth), strings.Join(links, ", "))
	}
	return b.String()
}

// applyProviders checks (or with write regenerates) the English page and every
// translated copy that exists.
func applyProviders(root string, s *providersSurface, write bool) (*surface, error) {
	out := &surface{name: "providers"}
	pages := []string{providersPageRel}
	locales, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(providersLocaleGlob)))
	if err != nil {
		return nil, cannot("could not list the translated copies of %s: %v", providersPageRel, err)
	}
	for _, p := range locales {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil, cannot("could not place %s under %s: %v", p, root, err)
		}
		pages = append(pages, filepath.ToSlash(rel))
	}
	for _, rel := range pages {
		f, err := applyRegion(root, rel, providersRegionID, s.region(), write, &keySpec{
			noun: "preset", want: s.keys(), col: 1,
			fix: "Regenerate with `bash scripts/check-guide-docs.sh --write`.",
		})
		if err != nil {
			return nil, err
		}
		out.findings = append(out.findings, f...)
	}
	out.notes = append(out.notes, fmt.Sprintf("providers: %d presets that run a tool, %d page(s)", len(s.rows), len(pages)))
	return out, nil
}

// runProviders is the providers surface of one gate run: 0 clean, 1 drift, error CANNOT LOOK.
func runProviders(root, dumpPath string, write, list bool) (int, error) {
	s, err := loadProviders(dumpPath)
	if err != nil {
		return 0, err
	}
	if list {
		fmt.Printf("providers: %d presets that run a tool\n", len(s.rows))
		for _, k := range s.keys() {
			fmt.Printf("  %s\n", k)
		}
		return 0, nil
	}
	surf, err := applyProviders(root, s, write)
	if err != nil {
		return 0, err
	}
	for _, n := range surf.notes {
		fmt.Printf("guide-docs: NOTE — %s\n", n)
	}
	for _, f := range surf.findings {
		fmt.Fprintf(os.Stderr, "guide-docs: %s: %s\n", surf.name, f)
	}
	if len(surf.findings) > 0 {
		fmt.Fprintf(os.Stderr, "guide-docs: %d provider difference(s). Regenerate with: bash scripts/check-guide-docs.sh --write\n", len(surf.findings))
		return 1, nil
	}
	fmt.Printf("guide-docs: OK — providers %d preset(s) published and in sync.\n", len(s.rows))
	return 0, nil
}
