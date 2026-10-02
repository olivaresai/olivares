// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSessionListTellsApartSessionsWithTheSameName is HU 036 on refresh 08b: two sessions
// named "notes" were two identical rows in `session ls`. The console gives a row whose name
// another row of the same list also carries the short tail of its reference; the CLI does
// the same with the run's id, and a name nobody else carries stays as it is.
func TestSessionListTellsApartSessionsWithTheSameName(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "01a0f99f-3b69-71a6-9c03-4ee43fcc157e", "name": "notes", "state": "running", "provider_driver": "claude"},
		{"run_ref": "01a0f99f-569b-7d77-a064-041945d8cedb", "name": "notes", "state": "stopped", "provider_driver": "claude"},
		{"run_ref": "01a0f99f-56c1-793d-95f4-3fa76388cd64", "name": "docs", "state": "running", "provider_driver": "codex"},
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "-o", "text"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	for _, want := range []string{"notes …4ee43fcc157e", "notes …041945d8cedb"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls does not tell the two notes sessions apart (%q):\n%s", want, out)
		}
	}
	if strings.Contains(out, "3fa76388cd64") {
		t.Fatalf("a name no other row carries got a tail:\n%s", out)
	}
}

// TestRefTailIsTheConsolesRule: the tail is the console's refTail
// (web/src/features/shared/entity-names.ts), not a second rule. Every expectation the
// console's own test writes for refTail holds here with the same input, and the type
// prefixes the console drops are the ones the CLI drops.
func TestRefTailIsTheConsolesRule(t *testing.T) {
	shared := filepath.Join("..", "..", "web", "src", "features", "shared")
	spec, err := os.ReadFile(filepath.Join(shared, "entity-names.test.tsx"))
	if err != nil {
		t.Fatalf("the console's refTail test: %v", err)
	}
	cases := regexp.MustCompile(`expect\(refTail\('([^']*)'\)\)\.toBe\(\s*'([^']*)',?\s*\)`).FindAllStringSubmatch(string(spec), -1)
	if len(cases) < 8 {
		t.Fatalf("read %d refTail expectations from the console's test; its form changed, so parity is not checked", len(cases))
	}
	for _, c := range cases {
		if got := refTail(c[1]); got != c[2] {
			t.Errorf("refTail(%q) = %q; the console's is %q", c[1], got, c[2])
		}
	}

	source, err := os.ReadFile(filepath.Join(shared, "entity-names.ts"))
	if err != nil {
		t.Fatalf("the console's refTail: %v", err)
	}
	m := regexp.MustCompile(`id\.replace\(/\^\(([a-z|]+)\)\[_-\]/i`).FindSubmatch(source)
	if m == nil {
		t.Fatal("the console's refTail no longer drops a type prefix the way this test reads it")
	}
	for _, prefix := range strings.Split(string(m[1]), "|") {
		for _, sep := range []string{"_", "-"} {
			ref := prefix + sep + "01a0f99f-3b69-71a6-9c03-4ee43fcc157e"
			if got := refTail(ref); got != "…4ee43fcc157e" {
				t.Errorf("refTail(%q) = %q; the console drops the %q prefix", ref, got, prefix)
			}
		}
	}
}
