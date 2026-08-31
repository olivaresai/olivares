// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// selfTest builds throwaway trees and asserts what this gate does with each. A gate
// nobody has watched fail reports green because it looked at nothing, so the RED cases
// run first and on every push.
//
// Each case fixes ONE thing, and the two that carry the task are the pair at the top:
// `added-variable` (a name enters the code and the docs are not regenerated ⇒ RED, and
// the finding NAMES the variable) and `in-sync` (nothing changed ⇒ GREEN). Without the
// second, the first would be satisfied by a gate that fails on everything; without the
// first, by a gate that fails on nothing. The greens are as load-bearing as the reds.
//
// The CANNOT-LOOK column is separate on purpose: a missing registry, a missing catalog,
// a page whose markers were deleted and a tree below the population floor must each
// exit 2, not 0. The regression they forbid is the one that reads "no findings" when
// the truth is "nothing was examined".
func selfTest() int {
	base, err := os.MkdirTemp("", "config-env-docs-selftest")
	if err != nil {
		fmt.Printf("config-env-docs: CANNOT LOOK — no scratch dir: %v\n", err)
		return exitCannotLook
	}
	defer os.RemoveAll(base)

	type testCase struct {
		name string
		want int
		// wantNames must all appear in the output. For the added/removed cases this is
		// what proves the gate NAMES the offender instead of just going red.
		wantNames []string
		mutate    func(t *tree)
		floor     int
	}

	cases := []testCase{
		// ── RED: the regression this gate exists for, in both directions ──────────
		{
			name: "added-variable-not-regenerated", want: exitDrift,
			wantNames: []string{"OLIVARES_NEWLY_ADDED_KNOB", "no row in"},
			mutate: func(t *tree) {
				t.appendGo("pkg/app/extra.go", `package app

func extra(getenv func(string) string) string { return getenv("OLIVARES_NEWLY_ADDED_KNOB") }
`)
			},
		},
		{
			name: "removed-variable-still-documented", want: exitDrift,
			wantNames: []string{"OLIVARES_GONE_AWAY", "no non-test Go source declares it"},
			mutate: func(t *tree) {
				t.appendCatalog("OLIVARES_GONE_AWAY\tno\t\tA knob that no longer exists.")
			},
		},
		{
			name: "page-edited-by-hand", want: exitDrift,
			wantNames: []string{"is not what the current tree produces"},
			mutate: func(t *tree) {
				t.replaceInPage("Bind address for the demo listener.", "Something an editor typed instead.")
			},
		},
		{
			name: "summary-left-as-todo", want: exitDrift,
			wantNames: []string{"OLIVARES_DEMO_LISTEN", "write the sentence"},
			mutate: func(t *tree) {
				t.replaceInCatalog("Bind address for the demo listener.", catalogTODO)
			},
		},
		{
			name: "summary-uses-forbidden-word", want: exitDrift,
			wantNames: []string{"OLIVARES_DEMO_LISTEN", "forbidden word"},
			mutate: func(t *tree) {
				t.replaceInCatalog("Bind address for the demo listener.", "A tamper-proof listener address.")
			},
		},
		// ── CANNOT LOOK: four ways to see nothing, none of which may read green ────
		{
			name: "registry-missing", want: exitCannotLook,
			wantNames: []string{"CANNOT LOOK", registryPath},
			mutate:    func(t *tree) { t.remove(registryPath) },
		},
		{
			name: "registry-declares-no-test-only-keys", want: exitCannotLook,
			wantNames: []string{"CANNOT LOOK", "testOnlyConfigEnvKeys"},
			mutate: func(t *tree) {
				t.replaceInRegistry(`"OLIVARES_DEMO_TEST_SENTINEL",`, "")
			},
		},
		{
			name: "catalog-missing", want: exitCannotLook,
			wantNames: []string{"CANNOT LOOK", catalogPath},
			mutate:    func(t *tree) { t.remove(catalogPath) },
		},
		{
			name: "page-lost-its-markers", want: exitCannotLook,
			wantNames: []string{"CANNOT LOOK", "no generated region"},
			mutate:    func(t *tree) { t.replaceInPage(beginMarker, "") },
		},
		{
			name: "tree-below-population-floor", want: exitCannotLook,
			wantNames: []string{"CANNOT LOOK", "population floor"},
			floor:     1200, // the real one, against a fixture tree of a handful of files
			mutate:    func(t *tree) {},
		},
		// ── GREEN: without these, "fail on everything" would pass the red column ───
		{
			name: "in-sync", want: exitClean,
			wantNames: []string{"OK config-env-docs"},
			mutate:    func(t *tree) {},
		},
		{
			name: "name-only-in-a-comment", want: exitClean,
			wantNames: []string{"OK config-env-docs"},
			mutate: func(t *tree) {
				t.appendGo("pkg/app/prose.go", `package app

// The closed side reads OLIVARES_ONLY_IN_A_COMMENT from getenv; this side does not.
func prose() {}
`)
			},
		},
		{
			name: "name-only-in-a-test-file", want: exitClean,
			wantNames: []string{"OK config-env-docs"},
			mutate: func(t *tree) {
				t.appendGo("pkg/app/app_test.go", `package app

func helper(getenv func(string) string) string { return getenv("OLIVARES_ONLY_IN_A_TEST") }
`)
			},
		},
		{
			// A member the code never spells out — the shape the embeddings and
			// key-wrapping families use. The registered prefix already has a row, and
			// the member is not a literal, so nothing new is demanded.
			name: "runtime-built-family-member", want: exitClean,
			wantNames: []string{"OK config-env-docs"},
			mutate: func(t *tree) {
				t.appendGo("pkg/app/family.go", `package app

import "strings"

func family(getenv func(string) string, provider string) string {
	return getenv("OLIVARES_DEMO_FAMILY_" + strings.ToUpper(provider))
}
`)
			},
		},
		{
			// A new test-only sentinel: the registry declares the prefix, so it is not
			// product configuration and the page must not grow a row for it.
			name: "new-test-only-sentinel", want: exitClean,
			wantNames: []string{"OK config-env-docs"},
			mutate: func(t *tree) {
				t.appendGo("pkg/app/sentinel.go", `package app

func sentinel(getenv func(string) string) string { return getenv("OLIVARES_DEMO_TEST_ANOTHER") }
`)
			},
		},
	}

	failures := 0
	realFloor := populationFloor
	for _, tc := range cases {
		t := newTree(filepath.Join(base, tc.name))
		if err := t.build(); err != nil {
			fmt.Printf("selftest FAIL: fixture %q could not be built: %v\n", tc.name, err)
			failures++
			continue
		}
		tc.mutate(t)

		populationFloor = 1
		if tc.floor != 0 {
			populationFloor = tc.floor
		}
		var out, errOut bytes.Buffer
		got := run(t.root, false, false, &out, &errOut)
		populationFloor = realFloor

		combined := out.String() + errOut.String()
		if got != tc.want {
			fmt.Printf("selftest FAIL: %s exited %d, want %d\n%s\n", tc.name, got, tc.want, indent(combined))
			failures++
			continue
		}
		missing := ""
		for _, needle := range tc.wantNames {
			if !strings.Contains(combined, needle) {
				missing = needle
				break
			}
		}
		if missing != "" {
			fmt.Printf("selftest FAIL: %s exited %d as expected but never said %q — a verdict that does not name the offender is not a finding\n%s\n",
				tc.name, got, missing, indent(combined))
			failures++
			continue
		}
		fmt.Printf("selftest ok: %-36s -> %d (%s)\n", tc.name, got, firstLine(combined))
	}

	if failures > 0 {
		fmt.Printf("\nconfig-env-docs selftest: %d of %d cases FAILED\n", failures, len(cases))
		return exitDrift
	}
	fmt.Printf("config-env-docs selftest: %d cases, every red case red and every green case green\n", len(cases))
	return exitClean
}

func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}

func firstLine(s string) string {
	line := strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
	if len(line) > 90 {
		return line[:90] + "…"
	}
	return line
}

// ── the fixture tree ───────────────────────────────────────────────────────────────

type tree struct {
	root string
	err  error
}

func newTree(root string) *tree { return &tree{root: root} }

// build writes a miniature repository: a registry with one exact key, one prefix
// family and one test-only sentinel; a source that reads the exact key; a catalog; and
// a page whose generated region is what the code produces. That last step runs the
// generator itself, so a fixture is never hand-transcribed out of date.
func (t *tree) build() error {
	t.write(registryPath, `package main

var exactConfigEnvKeys = []string{
	"OLIVARES_DEMO_LISTEN",
}

var prefixConfigEnvKeys = []string{
	"OLIVARES_DEMO_FAMILY_",
}

var (
	testOnlyConfigEnvKeys = []string{
		"OLIVARES_DEMO_TEST_SENTINEL",
	}
	testOnlyConfigEnvPrefixes = []string{
		"OLIVARES_DEMO_TEST_",
	}
)
`)
	t.write("pkg/app/app.go", `package app

func listen(getenv func(string) string) string {
	if v := getenv("OLIVARES_DEMO_LISTEN"); v != "" {
		return v
	}
	return getenv("OLIVARES_DEMO_TEST_SENTINEL")
}
`)
	t.write(catalogPath, "# fixture catalog\n"+
		"OLIVARES_DEMO_FAMILY_\tno\t\tFamily prefix for the demo settings.\n"+
		"OLIVARES_DEMO_LISTEN\tno\t127.0.0.1:8443\tBind address for the demo listener.\n")
	t.write(pagePath, "---\ntitle: fixture\n---\n\n"+beginMarker+"\n"+endMarker+"\n")
	if t.err != nil {
		return t.err
	}
	// Seed the page from the generator, then assert the fixture starts GREEN. A red
	// baseline would make every case below meaningless — the exit code would be 1 for
	// reasons that have nothing to do with what the case is testing.
	saved := populationFloor
	populationFloor = 1
	defer func() { populationFloor = saved }()
	var out, errOut bytes.Buffer
	if rc := run(t.root, true, false, &out, &errOut); rc != exitClean {
		return fmt.Errorf("seeding the fixture page exited %d: %s", rc, out.String()+errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if rc := run(t.root, false, false, &out, &errOut); rc != exitClean {
		return fmt.Errorf("the freshly generated fixture is not green (exit %d): %s", rc, out.String()+errOut.String())
	}
	return nil
}

func (t *tree) write(rel, content string) {
	if t.err != nil {
		return
	}
	path := filepath.Join(t.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.err = err
		return
	}
	t.err = os.WriteFile(path, []byte(content), 0o644)
}

func (t *tree) appendGo(rel, content string) { t.write(rel, content) }

func (t *tree) remove(rel string) {
	if err := os.Remove(filepath.Join(t.root, filepath.FromSlash(rel))); err != nil && t.err == nil {
		t.err = err
	}
}

func (t *tree) appendCatalog(row string) {
	path := filepath.Join(t.root, filepath.FromSlash(catalogPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.err = err
		return
	}
	t.err = os.WriteFile(path, append(raw, []byte(row+"\n")...), 0o644)
}

func (t *tree) replaceInCatalog(old, new string)  { t.replaceIn(catalogPath, old, new) }
func (t *tree) replaceInPage(old, new string)     { t.replaceIn(pagePath, old, new) }
func (t *tree) replaceInRegistry(old, new string) { t.replaceIn(registryPath, old, new) }

func (t *tree) replaceIn(rel, old, new string) {
	path := filepath.Join(t.root, filepath.FromSlash(rel))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.err = err
		return
	}
	if !strings.Contains(string(raw), old) {
		t.err = fmt.Errorf("fixture %s does not contain %q, so the mutation would be a no-op", rel, old)
		return
	}
	t.err = os.WriteFile(path, []byte(strings.Replace(string(raw), old, new, 1)), 0o644)
}
