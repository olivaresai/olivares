// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const consoleRegionID = "olivares-console-routes"

// consoleDump is stage 1's output. Stage 1 owns the TypeScript; this half owns the
// verdict. See console-dump.mjs for why the split exists.
type consoleDump struct {
	Schema     string           `json:"schema"`
	HubOrder   []string         `json:"hubOrder"`
	Census     []string         `json:"census"`
	Views      []consoleView    `json:"views"`
	Standalone []consoleMounted `json:"standalone"`
}

type consoleView struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Hub        string `json:"hub"`
	Permission string `json:"permission"`
	HelpHref   string `json:"helpHref"`
	HideInNav  bool   `json:"hideInNav"`
	Where      string `json:"where"`
}

type consoleMounted struct {
	Path          string `json:"path"`
	Parent        string `json:"parent"`
	Authenticated bool   `json:"authenticated"`
	Where         string `json:"where"`
}

// navCatalog is the console's own English strings — the product's words, already
// translated into seven locales. The guide reuses them instead of inventing a second
// description per screen, because two descriptions of one screen drift and the operator
// then reads one sentence in the sidebar and a different one in the docs.
type navCatalog struct {
	Items        map[string]string `json:"items"`
	Descriptions map[string]string `json:"descriptions"`
	Hubs         map[string]string `json:"hubs"`
}

// consoleRoster is the enumerated surface: one row per census path, joined with whatever
// the console says about it.
type consoleRoster struct {
	dump    consoleDump
	nav     navCatalog
	catalog map[string]catalogRow
	rows    []consoleRow
	// preFindings are defects found while joining — they belong to the surface's verdict
	// but are known before any page is read.
	preFindings []string
	notes       []string
}

type consoleRow struct {
	Path        string
	ID          string
	Hub         string // "" for the routes mounted outside the registry
	Title       string
	Summary     string
	Permission  string
	HelpHref    string
	DeepLink    bool // hideInNav: reachable only by deep link
	Public      bool // mounted on the unauthenticated root, not the app shell
	TitleFrom   string
	SummaryFrom string
}

// standaloneID maps a path mounted outside the registry onto the key the console's own
// translation catalog would use for it: the path without its leading slash. `/settings`
// is the case that matters — nav.json describes it, and a guide that ignored that would
// print a hand-written sentence next to the console's own, different one.
func standaloneID(p string) string {
	return strings.TrimPrefix(p, "/")
}

func loadConsole(root, dumpPath string) (*consoleRoster, error) {
	if strings.TrimSpace(dumpPath) == "" {
		return nil, cannot("no -dump path was given, so the console's routes were never enumerated")
	}
	raw, err := os.ReadFile(dumpPath)
	if err != nil {
		return nil, cannot("the console dump was not written to %s (%v); stage 1 (scripts/guide-docs/console-dump.mjs) did not run or did not finish", dumpPath, err)
	}
	var d consoleDump
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, cannot("the console dump at %s is not readable JSON: %v", dumpPath, err)
	}
	if d.Schema != dumpSchema {
		return nil, cannot("the console dump declares schema %q but this gate speaks %q", d.Schema, dumpSchema)
	}
	if len(d.Census) < consoleFloor {
		return nil, cannot("the console dump lists %d routes, below the floor of %d; the console cannot have shrunk that far, so the enumeration is broken and no page may be regenerated from it", len(d.Census), consoleFloor)
	}
	if len(d.HubOrder) == 0 {
		return nil, cannot("the console dump carries no hub order, so the guide has no section order")
	}

	navRaw, err := os.ReadFile(filepath.Join(root, navEnRel))
	if err != nil {
		return nil, cannot("%s could not be read (%v), so the console's own labels were never consulted", navEnRel, err)
	}
	var nav navCatalog
	if err := json.Unmarshal(navRaw, &nav); err != nil {
		return nil, cannot("%s is not readable JSON: %v", navEnRel, err)
	}
	if len(nav.Items) == 0 || len(nav.Hubs) == 0 {
		return nil, cannot("%s declares no nav items or no hubs; without them the guide would publish a table of bare URLs", navEnRel)
	}

	catalog, err := loadCatalog(root, consoleCatalogRel, 3)
	if err != nil {
		return nil, err
	}

	r := &consoleRoster{dump: d, nav: nav, catalog: catalog}
	r.join(root)
	return r, nil
}

// join builds one row per census path. Every disagreement between the four inputs is a
// finding with the offending name in it; nothing is dropped quietly.
func (r *consoleRoster) join(root string) {
	byPath := map[string]consoleView{}
	for _, v := range r.dump.Views {
		if prev, dup := byPath[v.Path]; dup {
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"two FEATURE_VIEWS entries mount %s (%q at %s and %q at %s); the router aborts the whole tree on a duplicate id",
				v.Path, prev.ID, prev.Where, v.ID, v.Where))
			continue
		}
		byPath[v.Path] = v
	}
	mounted := map[string]consoleMounted{}
	for _, m := range r.dump.Standalone {
		mounted[m.Path] = m
	}

	census := map[string]bool{}
	for _, p := range r.dump.Census {
		census[p] = true
	}
	// The other direction, and it is the one a census cannot catch on its own: a route
	// the application mounts today that nobody appended to the census. Left unreported it
	// grows into a screen no guide covers and no conservation test protects.
	for _, v := range r.dump.Views {
		if !census[v.Path] {
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"FEATURE_VIEWS mounts %s (%s, %s) and %s does not record it — append the path there first",
				v.Path, v.ID, v.Where, censusRel))
		}
	}
	for _, m := range r.dump.Standalone {
		if !census[m.Path] {
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"%s mounts %s and %s does not record it — append the path there first",
				m.Where, m.Path, censusRel))
		}
	}

	hubKnown := map[string]bool{}
	for _, h := range r.dump.HubOrder {
		hubKnown[h] = true
		if _, ok := r.nav.Hubs[h]; !ok {
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"hub %q has no label in %s, so its section would be published with a raw identifier as its heading", h, navEnRel))
		}
	}

	used := map[string]bool{}
	for _, p := range r.dump.Census {
		row := consoleRow{Path: p}
		if v, ok := byPath[p]; ok {
			row.ID = v.ID
			row.Hub = v.Hub
			row.Permission = v.Permission
			row.HelpHref = v.HelpHref
			row.DeepLink = v.HideInNav
			if !hubKnown[v.Hub] {
				r.preFindings = append(r.preFindings, fmt.Sprintf(
					"view %q (%s) declares hub %q, which is not in HUB_ORDER; it would be published under no heading at all", v.ID, p, v.Hub))
			}
		} else if m, ok := mounted[p]; ok {
			row.ID = standaloneID(p)
			row.Public = !m.Authenticated
		} else {
			// A census path the application no longer mounts is exactly the loss the
			// census exists to make visible. It is a finding here too, not a skipped row:
			// a guide that silently omitted it would erase the evidence.
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"%s records %s and nothing in the console mounts it — the route was lost, or it needs a declared alias in ROUTE_ALIASES", censusRel, p))
			continue
		}

		cat, hasCat := r.catalog[p]
		if hasCat {
			used[p] = true
		}

		// Title: the console's own nav label wins. The catalog may only speak where the
		// console is silent, and it must SAY that it is silent (a "-"), so a label added
		// to nav.json later collides with the row instead of quietly being shadowed.
		if label, ok := r.nav.Items[row.ID]; ok && strings.TrimSpace(label) != "" {
			row.Title = label
			row.TitleFrom = "nav"
			if hasCat && cat.fields[0] != placeholder {
				r.preFindings = append(r.preFindings, fmt.Sprintf(
					"%s:%d gives %s a title, but the console already names it %q in %s — set that column to %q",
					consoleCatalogRel, cat.line, p, label, navEnRel, placeholder))
			}
		} else if hasCat && cat.fields[0] != placeholder && cat.fields[0] != "" {
			row.Title = cat.fields[0]
			row.TitleFrom = "catalog"
		} else {
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"%s has no label: %s declares no nav.items.%s and %s supplies no title — add one",
				p, navEnRel, row.ID, consoleCatalogRel))
			continue
		}

		if desc, ok := r.nav.Descriptions[row.ID]; ok && strings.TrimSpace(desc) != "" {
			row.Summary = desc
			row.SummaryFrom = "nav"
			if hasCat && cat.fields[1] != placeholder {
				r.preFindings = append(r.preFindings, fmt.Sprintf(
					"%s:%d describes %s, but the console already describes it in %s — set that column to %q",
					consoleCatalogRel, cat.line, p, navEnRel, placeholder))
			}
		} else if hasCat && cat.fields[1] != placeholder && cat.fields[1] != "" {
			if isTODO(cat.fields[1]) {
				r.preFindings = append(r.preFindings, fmt.Sprintf(
					"%s:%d leaves %s described as %q; regenerating would publish the placeholder", consoleCatalogRel, cat.line, p, cat.fields[1]))
				continue
			}
			if len(cat.fields[1]) < minSummary {
				r.preFindings = append(r.preFindings, fmt.Sprintf(
					"%s:%d describes %s in %d characters, below the %d a reader can use", consoleCatalogRel, cat.line, p, len(cat.fields[1]), minSummary))
				continue
			}
			row.Summary = cat.fields[1]
			row.SummaryFrom = "catalog"
		} else {
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"%s has no description: %s declares no nav.descriptions.%s and %s supplies none — add a row",
				p, navEnRel, row.ID, consoleCatalogRel))
			continue
		}

		// The help link the console's own topbar offers for this screen. Checking that it
		// resolves from THIS side matters: registry-help.test.ts pins the same slug from
		// the console's side, and it is the docs page — not the console — that a docs
		// session can delete.
		if row.HelpHref != "" && !docsPageExists(root, row.HelpHref) {
			r.preFindings = append(r.preFindings, fmt.Sprintf(
				"%s points its in-product help at %s and no page publishes that route", p, row.HelpHref))
		}

		r.rows = append(r.rows, row)
	}

	// A catalog row for a path that is not in the census is the second direction, and the
	// one a page can never reveal: it publishes a screen the console retired.
	// Sorted, because map iteration order is random and a gate whose output reorders on
	// every run trains its reader to stop comparing runs.
	var orphans []string
	for k := range r.catalog {
		if !used[k] {
			orphans = append(orphans, k)
		}
	}
	sort.Strings(orphans)
	for _, k := range orphans {
		r.preFindings = append(r.preFindings, fmt.Sprintf(
			"%s:%d describes %s, which %s does not record — the console does not have that screen", consoleCatalogRel, r.catalog[k].line, k, censusRel))
	}

	sort.Slice(r.rows, func(i, j int) bool { return r.rows[i].Path < r.rows[j].Path })

	// Printed on every run, never fatal: how much of the guide is the product's own voice
	// and how much is hand-written here. It is the number that tells a future session
	// whether this surface is drifting back towards prose.
	navTitles, navSummaries := 0, 0
	for _, row := range r.rows {
		if row.TitleFrom == "nav" {
			navTitles++
		}
		if row.SummaryFrom == "nav" {
			navSummaries++
		}
	}
	r.notes = append(r.notes, fmt.Sprintf(
		"console: %d of %d rows take their title and %d of %d take their description from the console's own %s; the rest come from %s",
		navTitles, len(r.rows), navSummaries, len(r.rows), navEnRel, consoleCatalogRel))
}

// docsPageExists resolves a site-relative slug against the content tree the way Starlight
// does: `foo/bar` is foo/bar.md(x) or foo/bar/index.md(x), and `/` is the site index.
func docsPageExists(root, href string) bool {
	slug := strings.Trim(href, "/")
	base := filepath.Join(root, "docs-site", "src", "content", "docs")
	cands := []string{}
	if slug == "" {
		cands = append(cands, filepath.Join(base, "index.md"), filepath.Join(base, "index.mdx"))
	} else {
		for _, ext := range []string{".md", ".mdx"} {
			cands = append(cands, filepath.Join(base, filepath.FromSlash(slug)+ext))
			cands = append(cands, filepath.Join(base, filepath.FromSlash(slug), "index"+ext))
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

func (r *consoleRoster) print(w io.Writer) {
	fmt.Fprintf(w, "# console — %d routes\n", len(r.dump.Census))
	for _, row := range r.rows {
		perm := row.Permission
		if perm == "" {
			perm = "(any signed-in user)"
		}
		if row.Public {
			perm = "(no sign-in)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row.Path, row.ID, perm, row.Title)
	}
}

// region renders the console table. Deterministic: hub sections in the console's own
// HUB_ORDER, rows by path inside each.
func (r *consoleRoster) region() string {
	var b strings.Builder

	byHub := map[string][]consoleRow{}
	var outside []consoleRow
	for _, row := range r.rows {
		if row.Hub == "" {
			outside = append(outside, row)
			continue
		}
		byHub[row.Hub] = append(byHub[row.Hub], row)
	}

	fmt.Fprintf(&b, "The console publishes **%d routes**. Every one of them is in the tables below, with the\n", len(r.rows))
	fmt.Fprintf(&b, "permission it requires and the reference page its in-product help link opens.\n")

	for _, hub := range r.dump.HubOrder {
		rows := byHub[hub]
		if len(rows) == 0 {
			continue
		}
		label := r.nav.Hubs[hub]
		if label == "" {
			label = hub
		}
		fmt.Fprintf(&b, "\n### %s\n\n", mdCell(label))
		writeConsoleTable(&b, rows)
	}

	if len(outside) > 0 {
		fmt.Fprintf(&b, "\n### Sign-in, setup and account\n\n")
		fmt.Fprintf(&b, "These are mounted outside the feature registry. The ones marked **no sign-in** are\n")
		fmt.Fprintf(&b, "served before there is a session — they are the only console routes that are.\n\n")
		writeConsoleTable(&b, outside)
	}

	return b.String()
}

// consolePathCell renders the Path cell. It is a function and not an inline expression
// because the drift reporter has to produce the SAME string to compare against what is
// published; two places that build one cell is how a gate starts naming keys that never
// appear in the page it is reading.
func consolePathCell(row consoleRow) string {
	cell := "`" + row.Path + "`"
	if row.DeepLink {
		cell += " (deep link only)"
	}
	return cell
}

func writeConsoleTable(b *strings.Builder, rows []consoleRow) {
	b.WriteString("| Screen | Path | What it is | Requires | Reference |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, row := range rows {
		perm := "any signed-in user"
		switch {
		case row.Public:
			perm = "**no sign-in**"
		case row.Permission != "":
			perm = "`" + row.Permission + "`"
		}
		path := consolePathCell(row)
		ref := "—"
		if row.HelpHref != "" {
			ref = fmt.Sprintf("[%s](%s)", mdCell(strings.TrimPrefix(row.HelpHref, "/")), docsLink(row.HelpHref))
			if row.HelpHref == "/" {
				ref = "[docs home](/)"
			}
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n",
			mdCell(row.Title), mdCell(path), mdCell(row.Summary), perm, ref)
	}
}

// docsLink normalises a site-relative slug to the trailing-slash form the rest of the
// site links with, so the built-site link check sees one shape.
func docsLink(href string) string {
	if href == "/" {
		return "/"
	}
	return "/" + strings.Trim(href, "/") + "/"
}

func applyConsole(root string, r *consoleRoster, write bool) (*surface, error) {
	s := &surface{name: "console", findings: append([]string{}, r.preFindings...), notes: r.notes}
	// A roster that did not survive the join must not be rendered: --write would publish
	// a page that is missing exactly the screens the findings are about.
	if len(s.findings) > 0 && write {
		return s, nil
	}
	// The identity of a console row is its PATH, in the second column. Naming it is the
	// difference between "add /agentcore-export" and "the region changed by 812 bytes".
	keys := make([]string, 0, len(r.rows))
	for _, row := range r.rows {
		keys = append(keys, consolePathCell(row))
	}
	f, err := applyRegion(root, consolePageRel, consoleRegionID, r.region(), write, &keySpec{
		noun: "route", want: keys, col: 1,
		fix: "Regenerate with `bash scripts/check-guide-docs.sh --write`.",
	})
	if err != nil {
		return nil, err
	}
	s.findings = append(s.findings, f...)
	return s, nil
}
