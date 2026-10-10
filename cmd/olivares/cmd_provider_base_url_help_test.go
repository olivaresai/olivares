// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The --base-url help must promise exactly the endpoint rule the engine enforces:
// https for every kind, plain http only at a loopback or private-network address and
// only for the kinds of the engine's plain-http branch. The kinds are read from that
// branch (modules/sessions/provider_record.go, validProviderBaseURL) the way
// web/src/features/providers/kinds.test.ts reads it, so a change to the Go rule
// without the help text fails here, and so does a help text that names a kind the
// engine would refuse plain http for (#527: the help said https only).
func TestProviderAddBaseURLHelpStatesEnginePlainHTTPRule(t *testing.T) {
	all, plainHTTP := engineProviderPlainHTTPKinds(t)
	flag := newProviderAddCmd().Flags().Lookup("base-url")
	if flag == nil {
		t.Fatal("provider add has no --base-url flag")
	}
	usage := flag.Usage
	for _, want := range []string{"https", "plain http", "loopback", "private-network"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--base-url usage %q does not state %q", usage, want)
		}
	}
	for _, kind := range all {
		named := regexp.MustCompile(`\b` + regexp.QuoteMeta(kind) + `\b`).MatchString(usage)
		if named != slices.Contains(plainHTTP, kind) {
			t.Errorf("--base-url usage %q names %q; the engine's plain-http kinds are %v", usage, kind, plainHTTP)
		}
	}
}

// engineProviderPlainHTTPKinds reads the provider kind constants and the plain-http
// branch of validProviderBaseURL from the engine's source. A renamed branch or a
// moved constant block is "could not look", never a pass.
func engineProviderPlainHTTPKinds(t *testing.T) (all, plainHTTP []string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's own file")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "modules", "sessions", "provider_record.go"))
	if err != nil {
		t.Fatalf("read the engine rule: %v", err)
	}
	values := map[string]string{}
	for _, m := range regexp.MustCompile(`(ProviderKind\w+)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		values[m[1]] = m[2]
		all = append(all, m[2])
	}
	if len(values) == 0 {
		t.Fatal("no ProviderKind constants found; the reader is stale, not the rule")
	}
	branches := regexp.MustCompile(`if \(([^)]*)\) && strings\.HasPrefix\(lower, "http://"\)`).FindAllStringSubmatch(string(src), -1)
	if len(branches) == 0 {
		t.Fatal("no plain-http branch in validProviderBaseURL; the reader is stale, not the rule")
	}
	// Every branch, not only the first: a rule split across two conditions must
	// widen, never shrink, the kinds the texts promise (the review of #527).
	for _, branch := range branches {
		for _, m := range regexp.MustCompile(`kind == (ProviderKind\w+)`).FindAllStringSubmatch(branch[1], -1) {
			v, ok := values[m[1]]
			if !ok {
				t.Fatalf("the plain-http branch names %s, which has no constant", m[1])
			}
			if !slices.Contains(plainHTTP, v) {
				plainHTTP = append(plainHTTP, v)
			}
		}
	}
	if len(plainHTTP) == 0 {
		t.Fatal("the plain-http branch names no kind; the reader is stale, not the rule")
	}
	slices.Sort(all)
	slices.Sort(plainHTTP)
	return all, plainHTTP
}
