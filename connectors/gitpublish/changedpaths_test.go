// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// changeOn commits mutate on top of parent in a scratch clone of the fixture's
// server repository and returns the new commit, fed into the server repository.
func changeOn(t *testing.T, f *gitFixture, parent string, mutate func(work string)) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "w")
	run(t, dir, "clone", "-q", f.server, work)
	run(t, work, "checkout", "-q", parent)
	mutate(work)
	run(t, work, "add", "-A")
	run(t, work, "commit", "-q", "--allow-empty", "-m", "change")
	commit := run(t, work, "rev-parse", "HEAD")
	run(t, work, "push", "-q", f.server, "HEAD:refs/heads/c-"+commit[:8])
	return commit
}

func TestChangedPathsNamesEveryPathBothSidesOfARename(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	// A file the base does not have in workflows, to rename in and out.
	base := changeOn(t, f, f.base, func(w string) {
		writeFile(t, filepath.Join(w, "docs", "ci.md"), "notes\nnotes\nnotes\nnotes\n")
	})
	cases := []struct {
		name   string
		mutate func(w string)
		want   []string
	}{
		{"modify", func(w string) { writeFile(t, filepath.Join(w, "a.txt"), "three\n") }, []string{"a.txt"}},
		{"add", func(w string) { writeFile(t, filepath.Join(w, "src", "new.go"), "package x\n") }, []string{"src/new.go"}},
		{"delete", func(w string) {
			if err := os.Remove(filepath.Join(w, ".github", "workflows", "ci.yml")); err != nil {
				t.Fatal(err)
			}
		}, []string{".github/workflows/ci.yml"}},
		{"rename into workflows", func(w string) {
			run(t, w, "mv", "docs/ci.md", ".github/workflows/ci2.yml")
		}, []string{".github/workflows/ci2.yml", "docs/ci.md"}},
		{"rename out of workflows", func(w string) {
			run(t, w, "mv", ".github/workflows/ci.yml", "docs/ci-old.yml")
		}, []string{".github/workflows/ci.yml", "docs/ci-old.yml"}},
		{"empty", func(string) {}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commit := changeOn(t, f, base, tc.mutate)
			got, err := f.x.ChangedPaths(ctx, f.server, base, commit, true)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paths = %q, want %q", got, tc.want)
			}
		})
	}
}

// A base that moved on since the commit left it does not count its own
// changes against the commit; a history with nothing in common does.
func TestChangedPathsAreTheCommitsOwnSinceTheMergeBase(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	moved := changeOn(t, f, f.base, func(w string) { writeFile(t, filepath.Join(w, "go.mod"), "module x\n") })
	got, err := f.x.ChangedPaths(ctx, f.server, moved, f.commit, true)
	if err != nil || !reflect.DeepEqual(got, []string{"a.txt"}) {
		t.Fatalf("paths against a moved base = %q %v, want [a.txt]", got, err)
	}
	dir := t.TempDir()
	work := filepath.Join(dir, "w")
	run(t, dir, "init", "-q", "-b", "other", work)
	writeFile(t, filepath.Join(work, "z.txt"), "z\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "-q", "-m", "unrelated")
	other := run(t, work, "rev-parse", "HEAD")
	run(t, work, "push", "-q", f.server, "HEAD:refs/heads/other")
	got, err = f.x.ChangedPaths(ctx, f.server, other, f.commit, true)
	if err != nil || !reflect.DeepEqual(got, []string{".github/workflows/ci.yml", "a.txt", "z.txt"}) {
		t.Fatalf("paths against an unrelated base = %q %v", got, err)
	}
}

// Moving a branch to a commit that does not descend from its tip takes away
// what only the tip had: the plain difference of the trees names it, the
// merge-base view of the new commit alone would not.
func TestChangedPathsOfALeasedMoveIncludeWhatTheOldTipHad(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	tip := changeOn(t, f, f.base, func(w string) { writeFile(t, filepath.Join(w, "CODEOWNERS"), "* @a\n") })
	rewind := f.base
	got, err := f.x.ChangedPaths(ctx, f.server, tip, rewind, true)
	if err != nil || len(got) != 0 {
		t.Fatalf("merge-base view of a rewind = %q %v, want none: nothing of rewind is new", got, err)
	}
	got, err = f.x.ChangedPaths(ctx, f.server, tip, rewind, false)
	if err != nil || !reflect.DeepEqual(got, []string{"CODEOWNERS"}) {
		t.Fatalf("tree difference of a rewind = %q %v, want [CODEOWNERS]", got, err)
	}
}

// A rename that also edits the file, and names git would quote, come back as
// the exact paths.
func TestChangedPathsKeepsExactNames(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	base := changeOn(t, f, f.base, func(w string) {
		writeFile(t, filepath.Join(w, "docs", "guide.md"), "one\ntwo\nthree\nfour\nfive\nsix\n")
	})
	commit := changeOn(t, f, base, func(w string) {
		run(t, w, "mv", "docs/guide.md", ".github/workflows/guide.yml")
		writeFile(t, filepath.Join(w, ".github", "workflows", "guide.yml"), "one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
		writeFile(t, filepath.Join(w, "sp ace", "caf\u00e9 \"q\".txt"), "x\n")
	})
	got, err := f.x.ChangedPaths(ctx, f.server, base, commit, true)
	want := []string{".github/workflows/guide.yml", "docs/guide.md", "sp ace/caf\u00e9 \"q\".txt"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q %v, want %q", got, err, want)
	}
}

func TestChangedPathsRefusesAnAbsentOrMalformedCommit(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	absent := "1111111111111111111111111111111111111111"
	for _, tc := range []struct{ name, from, to string }{
		{"absent to", f.base, absent},
		{"absent from", absent, f.commit},
		{"not a sha", f.base, "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.x.ChangedPaths(ctx, f.server, tc.from, tc.to, true); !errors.Is(err, ErrContent) {
				t.Fatalf("err = %v, want ErrContent", err)
			}
		})
	}
}
