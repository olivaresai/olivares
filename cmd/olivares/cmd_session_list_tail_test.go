// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSessionListDefaultView: `session ls` printed every session the engine
// returned (500 on a busy engine), pasted the id's tail onto a name another row shared
// (the id column already carries it) and switched STARTED from "2d ago" to a date after a week.
// The plain list now holds every open session plus the newest 20 finished ones, says how many it
// left out, and --limit or --all widens it; -o json keeps every session.
func TestSessionListDefaultView(t *testing.T) {
	now := time.Now()
	stamp := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(time.RFC3339Nano) }
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "01a0f99f-0000-7000-8000-00000000a001", "name": "live", "state": "running", "provider_driver": "claude", "created_at": stamp(time.Minute)},
		// An open session started long ago is still open: the limit never hides it.
		{"run_ref": "01a0f99f-0000-7000-8000-00000000a002", "name": "waiting", "state": "idle", "provider_driver": "claude", "created_at": stamp(40 * 24 * time.Hour)},
	}
	for i := 1; i <= 30; i++ {
		f.runs = append(f.runs, map[string]any{
			"run_ref": fmt.Sprintf("01a0f99f-0000-7000-8000-%012x", i), "name": "notes", "state": "stopped",
			"provider_driver": "claude", "created_at": stamp(time.Duration(i) * 24 * time.Hour),
		})
	}
	list := func(extra ...string) string {
		t.Helper()
		out, _, err := execSessionCLI(t, nil, append(append([]string{"session", "ls", "-o", "text"}, extra...), sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("ls %v: %v", extra, err)
		}
		return out
	}

	out := list()
	if got := strings.Count(out, " stopped "); got != 20 {
		t.Fatalf("the plain list holds %d finished sessions, want the newest 20:\n%s", got, out)
	}
	for _, open := range []string{"live ", "waiting "} {
		if !strings.Contains(out, open) {
			t.Fatalf("the plain list left out the open session %q:\n%s", open, out)
		}
	}
	if !strings.Contains(out, "Showing 22 of 32 sessions") || !strings.Contains(out, "--all") {
		t.Fatalf("the plain list does not say it left sessions out:\n%s", out)
	}
	if strings.Contains(out, "notes …") {
		t.Fatalf("a shared name got the id's tail pasted on (the id column has it):\n%s", out)
	}
	if regexp.MustCompile(`\d{4}-\d{2}-\d{2}`).MatchString(out) || !strings.Contains(out, "20d ago") {
		t.Fatalf("STARTED mixes relative and absolute forms:\n%s", out)
	}

	if got := strings.Count(list("--limit", "5"), " stopped "); got != 5 {
		t.Fatalf("--limit 5 holds %d finished sessions", got)
	}
	all := list("--all")
	if got := strings.Count(all, " stopped "); got != 30 || strings.Contains(all, "Showing") {
		t.Fatalf("--all must list every session and say nothing was left out:\n%s", all)
	}
	raw, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("ls -o json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 32 {
		t.Fatalf("-o json must keep every session (a script reads it): %d rows, err %v", len(rows), err)
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
