// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

func TestHookPathNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		ref    string
		root   string
		want   string
		wantOK bool
	}{
		{name: "absolute traversal is lexical", ref: "/a/b/../../etc/x", want: "/etc/x", wantOK: true},
		{name: "relative with absolute root", ref: "src/../go.mod", root: "/workspace/project", want: "/workspace/project/go.mod", wantOK: true},
		{name: "relative without root", ref: "src/main.go", wantOK: false},
		{name: "relative with relative root", ref: "src/main.go", root: "workspace/project", wantOK: false},
		{name: "empty", ref: "", wantOK: false},
		{name: "nul", ref: "/tmp/a\x00b", wantOK: false},
		{name: "home shorthand", ref: "~/secret", wantOK: false},
		{name: "star meta", ref: "/repo/*.go", wantOK: false},
		{name: "question meta", ref: "/repo/file?.go", wantOK: false},
		{name: "class meta", ref: "/repo/[ab].go", wantOK: false},
		{name: "brace meta", ref: "/repo/{a,b}.go", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizePath(tc.ref, tc.root)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("normalizePath(%q, %q) = %q, %v; want %q, %v", tc.ref, tc.root, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestHookPathGlobMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		glob string
		abs  string
		want bool
	}{
		{name: "doublestar crosses directories", glob: "/etc/secrets/**", abs: "/etc/secrets/prod/db.key", want: true},
		{name: "doublestar matches zero directories", glob: "/repo/**/go.mod", abs: "/repo/go.mod", want: true},
		{name: "star matches one segment", glob: "/repo/*.go", abs: "/repo/main.go", want: true},
		{name: "star does not cross slash", glob: "/repo/*.go", abs: "/repo/cmd/main.go", want: false},
		{name: "question matches one char", glob: "/repo/file?.go", abs: "/repo/file1.go", want: true},
		{name: "question does not match two chars", glob: "/repo/file?.go", abs: "/repo/file12.go", want: false},
		{name: "literal segment is case sensitive", glob: "/repo/Readme.md", abs: "/repo/README.md", want: false},
		{name: "relative glob is not a path glob", glob: "repo/**", abs: "/repo/x", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathGlobMatch(tc.glob, tc.abs); got != tc.want {
				t.Fatalf("pathGlobMatch(%q, %q) = %v; want %v", tc.glob, tc.abs, got, tc.want)
			}
		})
	}
}

func TestHookPathInSubtree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		abs     string
		subtree string
		want    bool
	}{
		{name: "exact", abs: "/a/b", subtree: "/a/b", want: true},
		{name: "inside", abs: "/a/b/c", subtree: "/a/b", want: true},
		{name: "segment boundary", abs: "/a/bc", subtree: "/a/b", want: false},
		{name: "outside", abs: "/a/c", subtree: "/a/b", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathInSubtree(tc.abs, tc.subtree); got != tc.want {
				t.Fatalf("pathInSubtree(%q, %q) = %v; want %v", tc.abs, tc.subtree, got, tc.want)
			}
		})
	}
}
