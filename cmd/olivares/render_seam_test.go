// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// render.go says renderTo is "the ONE place the command layer builds a renderer".
// Until this file existed that was a comment, and it was measured false in two
// ways at once on 2026-09-18: rendererFor and renderFieldsOut had
// ZERO callers, while renderTableOut built its own renderer four lines below the
// paragraph claiming nothing did.
//
// A seam nobody uses is not a seam, and a claim nothing checks is not a rule. The
// two tests below are what hold it now.

// commandLayerSources reads the .go files of package main — the command layer
// itself, not its internal packages, which are separate packages with their own
// writers.
func commandLayerSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = string(body)
	}
	// The floor every scan in this tree has: a walk that stopped seeing the
	// package would report zero findings and look like success.
	if len(out) < 200 {
		t.Fatalf("the scan sees %d non-test .go files and this package has hundreds; "+
			"a scan that cannot see the package cannot declare it clean", len(out))
	}
	return out
}

// TestTheCommandLayerBuildsItsRendererInOnePlace is the guard rendererFor was
// supposed to be. It is a guard and not a helper, because the defect it stops is
// a call site building its own renderer — and a helper cannot stop that.
func TestTheCommandLayerBuildsItsRendererInOnePlace(t *testing.T) {
	found := map[string][]int{}
	for name, body := range commandLayerSources(t) {
		for i, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, "termrender.New(") {
				found[name] = append(found[name], i+1)
			}
		}
	}
	if len(found) != 1 || len(found["render.go"]) != 1 {
		t.Fatalf("termrender.New is built at %v. The command layer builds its renderer in "+
			"renderTo (render.go) and nowhere else: the colour and width decisions have to "+
			"have one place to be taught.", found)
	}
}

// TestEveryRenderHelperHasACaller: a render helper with no caller is a paragraph
// of documentation with a function attached, and both of the ones added alongside
// renderTo were exactly that when they were measured on 2026-09-18.
func TestEveryRenderHelperHasACaller(t *testing.T) {
	sources := commandLayerSources(t)
	for _, helper := range []string{"renderFieldsOut", "renderTableOut", "renderTo", "renderListOut"} {
		callers := 0
		for name, body := range sources {
			if name == "render.go" {
				continue
			}
			callers += strings.Count(body, helper+"(")
		}
		if callers == 0 {
			t.Errorf("%s has no caller outside render.go. Wire it or remove it: a seam with no "+
				"callers documents an intention instead of holding one.", helper)
		}
	}
}
