// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A generated region is delimited by a marker pair that NAMES the command that rewrites
// it. The instruction lives in the file a human opens, not in a commit message they will
// not read: an editor who lands inside the region should be told there, in place, that
// their edit is about to be overwritten.
func beginMarker(id string) string {
	return fmt.Sprintf(
		"<!-- BEGIN GENERATED %s — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->", id)
}

func endMarker(id string) string {
	return fmt.Sprintf("<!-- END GENERATED %s -->", id)
}

// readPage reads a published page. Its ABSENCE is CANNOT LOOK and not a finding,
// deliberately: a page this gate is supposed to keep in sync that is not in the tree at
// all means the surface is unpublished, and "the docs match the code" is not a claim
// anyone may make about a page that does not exist.
func readPage(root, rel string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "", cannot("%s could not be read (%v), so nothing was compared against the tree", rel, err)
	}
	if len(raw) == 0 {
		return "", cannot("%s is empty", rel)
	}
	return string(raw), nil
}

// splitRegion returns the text before and after the generated region.
//
// EVERY defect in the markers is CANNOT LOOK. A page whose markers were deleted, doubled
// or reordered cannot be compared, and the failure mode that must never happen is the one
// where "no region found" quietly becomes "the region matches": that reads as green
// forever on a page somebody flattened by hand.
func splitRegion(page, rel, id string) (before, after string, err error) {
	bm, em := beginMarker(id), endMarker(id)
	if n := strings.Count(page, bm); n != 1 {
		return "", "", cannot("%s carries %d copies of the %s begin marker (expected exactly 1), so the generated region could not be located", rel, n, id)
	}
	if n := strings.Count(page, em); n != 1 {
		return "", "", cannot("%s carries %d copies of the %s end marker (expected exactly 1), so the generated region could not be located", rel, n, id)
	}
	i := strings.Index(page, bm)
	j := strings.Index(page, em)
	if j < i {
		return "", "", cannot("%s has the %s end marker before its begin marker", rel, id)
	}
	return page[:i], page[j+len(em):], nil
}

// currentRegion returns the region as published, markers included.
func currentRegion(page, rel, id string) (string, error) {
	before, after, err := splitRegion(page, rel, id)
	if err != nil {
		return "", err
	}
	return page[len(before) : len(page)-len(after)], nil
}

// renderRegion wraps a body in its markers. The body is normalised to exactly one
// leading and one trailing newline so a regeneration can never produce a diff that is
// only whitespace — the kind of churn that trains a reader to stop reading the diff.
func renderRegion(id, body string) string {
	body = strings.Trim(body, "\n")
	return beginMarker(id) + "\n\n" + body + "\n\n" + endMarker(id)
}

// keySpec lets a surface say which column of its published table carries the identity of
// a row, so a drift can be reported as "this route is missing" rather than as "N bytes
// differ". A byte count sends the reader to diff the page by hand and guess what moved;
// the whole reason this gate exists is that nobody was doing that.
type keySpec struct {
	// noun is what one row is, singular ("route", "rpc", "channel").
	noun string
	// want is the roster, in rendered order.
	want []string
	// col is the 0-based table column whose cell carries the key.
	col int
	// fix is the one-line instruction printed with a missing key.
	fix string
}

// applyRegion compares (or in --write mode replaces) one generated region and returns
// the finding, if any.
func applyRegion(root, rel, id, body string, write bool, ks *keySpec) ([]string, error) {
	page, err := readPage(root, rel)
	if err != nil {
		return nil, err
	}
	before, after, err := splitRegion(page, rel, id)
	if err != nil {
		return nil, err
	}
	want := renderRegion(id, body)
	got, err := currentRegion(page, rel, id)
	if err != nil {
		return nil, err
	}
	if got == want {
		return nil, nil
	}
	if write {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(before+want+after), 0o644); err != nil {
			return nil, cannot("could not rewrite %s: %v", rel, err)
		}
		return nil, nil
	}

	var findings []string
	if ks != nil {
		published := map[string]bool{}
		for _, cell := range tableColumn(got, ks.col) {
			published[cell] = true
		}
		wanted := map[string]bool{}
		for _, k := range ks.want {
			wanted[k] = true
			if !published[k] {
				findings = append(findings, fmt.Sprintf(
					"%s is not published in %s — the tree has this %s and the page does not. %s",
					k, rel, ks.noun, ks.fix))
			}
		}
		for _, cell := range tableColumn(got, ks.col) {
			if !wanted[cell] {
				findings = append(findings, fmt.Sprintf(
					"%s is published in %s and the tree has no such %s — it was removed or renamed. %s",
					cell, rel, ks.noun, ks.fix))
			}
		}
	}
	// The roster can match perfectly while a description, a permission or a message type
	// moved. That is drift too, and it must not disappear just because the named check
	// found nothing to name.
	if len(findings) == 0 {
		findings = append(findings, fmt.Sprintf(
			"the generated region %s in %s no longer matches what the tree produces (%d published bytes vs %d rendered); the roster is unchanged, so a description, a permission or a type moved — or the region was edited by hand",
			id, rel, len(got), len(want)))
	}
	return findings, nil
}

// tableColumn returns the trimmed cell values of one column of every Markdown table BODY
// row in a region.
//
// The HEADER is excluded, and it has to be excluded structurally rather than by
// recognising its labels. Measured while writing this gate's own battery: counting the
// header made every table report its column title ("Path") as a published route, so a
// changed DESCRIPTION was reported as six phantom routes removed — a red for the wrong
// reason, which the battery treats as a failure exactly like a green for the wrong reason.
// A header is the row immediately above a `|---|` delimiter, per GFM; that is what is
// skipped here.
//
// Escaped pipes are honoured: `\|` inside a cell is content, and splitting naively on `|`
// would shear a cell in two and report a phantom key.
func tableColumn(region string, col int) []string {
	lines := strings.Split(region, "\n")
	isRow := func(i int) bool {
		return i >= 0 && i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|")
	}
	isDelimiter := func(i int) bool {
		if !isRow(i) {
			return false
		}
		for _, c := range splitRow(strings.TrimSpace(lines[i])) {
			c = strings.TrimSpace(c)
			if c == "" || strings.Trim(c, "-:") != "" {
				return false
			}
		}
		return true
	}
	var out []string
	for i := range lines {
		if !isRow(i) || isDelimiter(i) || isDelimiter(i+1) {
			continue
		}
		cells := splitRow(strings.TrimSpace(lines[i]))
		if len(cells) <= col {
			continue
		}
		if v := strings.TrimSpace(cells[col]); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func splitRow(row string) []string {
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(strings.TrimSpace(row), "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(row); i++ {
		if row[i] == '\\' && i+1 < len(row) && row[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if row[i] == '|' {
			cells = append(cells, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(row[i])
	}
	cells = append(cells, cur.String())
	return cells
}

// ── catalogs ───────────────────────────────────────────────────────────────────────────

// catalogRow is one hand-written line. The catalogs are the ONLY hand-written input to
// the generated surfaces, and they never decide the roster: a key in the tree with no row
// is a finding, and a row with no key in the tree is the SAME finding in the other
// direction. A one-directional catalog check is how a page keeps publishing a screen that
// was deleted two releases ago.
type catalogRow struct {
	key    string
	fields []string
	line   int
}

// placeholder marks a field the product itself supplies, so the catalog must stay silent
// about it. It is a single hyphen: an empty column is indistinguishable from a typo in a
// TSV, and the difference between "nothing to say" and "I forgot" is the whole point.
const placeholder = "-"

// todoRe-like guard: a summary left as a seeded TODO is worse than a missing one, because
// --write would happily publish it and turn the gate green over a placeholder sentence.
func isTODO(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	return u == "TODO" || strings.HasPrefix(u, "TODO ") || strings.HasPrefix(u, "TODO:") || u == "FIXME" || strings.HasPrefix(u, "FIXME ")
}

// minSummary is the length below which a cell is a fragment rather than a sentence a
// reader can use. Measured against the console's own descriptions, the shortest of which
// ("Estate overview and health at a glance") is 38 characters.
const minSummary = 20

// loadCatalog reads a tab-separated catalog with a fixed column count. A missing file is
// CANNOT LOOK, never an empty catalog: "the prose file is gone" and "no row is needed"
// are opposite facts, and treating the first as the second reports a page with no
// descriptions as fully documented.
func loadCatalog(root, rel string, cols int) (map[string]catalogRow, error) {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, cannot("the hand-written catalog %s could not be read (%v), so the pages were not checked against it", rel, err)
	}
	out := map[string]catalogRow{}
	for i, line := range strings.Split(string(raw), "\n") {
		n := i + 1
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != cols {
			return nil, cannot("%s:%d has %d tab-separated fields, expected %d", rel, n, len(parts), cols)
		}
		for j := range parts {
			parts[j] = strings.TrimSpace(parts[j])
		}
		key := parts[0]
		if key == "" {
			return nil, cannot("%s:%d has an empty key", rel, n)
		}
		if prev, dup := out[key]; dup {
			return nil, cannot("%s:%d repeats the key %q already declared at line %d; one of the two rows would be silently discarded", rel, n, key, prev.line)
		}
		out[key] = catalogRow{key: key, fields: parts[1:], line: n}
	}
	if len(out) == 0 {
		return nil, cannot("%s carries no rows at all; a catalog that says nothing cannot be the source of every published sentence", rel)
	}
	return out, nil
}
