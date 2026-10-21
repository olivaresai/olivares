// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func gitMountsOf(t *testing.T, folder string) (ro, pin []string, note string) {
	t.Helper()
	mounts, note := New().GitMounts(folder)
	return mounts.ReadOnly, mounts.Pin, note
}

func createdBy(m confine.Mounts) []string {
	var paths []string
	for _, n := range m.Create {
		paths = append(paths, n.Path)
	}
	return paths
}

func TestGitMountsOfALinkedWorktreeGuardEveryLinkAndPinTheEntries(t *testing.T) {
	repo, folder, _, _ := worktreeFixture(t)
	common := realPath(t, filepath.Join(repo, ".git"))
	entry := filepath.Join(common, "worktrees", filepath.Base(folder))
	mounts, note := New().GitMounts(folder)
	// Names git reads from a Git directory that the session could create are made
	// and bound too: a commondir in the repository's own, and config.worktree.
	wantRO := []string{filepath.Join(common, "hooks"), filepath.Join(common, "config"), filepath.Join(common, "commondir"),
		filepath.Join(common, "config.worktree"), filepath.Join(entry, "config.worktree"), filepath.Join(entry, "commondir"),
		filepath.Join(entry, "gitdir"), filepath.Join(realPath(t, folder), ".git")}
	wantCreate := []string{filepath.Join(common, "commondir"), filepath.Join(common, "config.worktree"), filepath.Join(entry, "config.worktree")}
	wantPin := []string{filepath.Dir(entry), entry}
	if !slices.Equal(mounts.ReadOnly, wantRO) || !slices.Equal(mounts.Pin, wantPin) || !slices.Equal(createdBy(mounts), wantCreate) || note != "" {
		t.Fatalf("ro=%q pin=%q create=%q note=%q, want ro=%q pin=%q create=%q", mounts.ReadOnly, mounts.Pin, createdBy(mounts), note, wantRO, wantPin, wantCreate)
	}
}

func TestGitMountsOfAPlainCloneGuardNamesGitReadsFromItsGitDirectory(t *testing.T) {
	repo, _, _, _ := worktreeFixture(t)
	git := realPath(t, filepath.Join(repo, ".git"))
	mounts, note := New().GitMounts(repo)
	wantRO := []string{filepath.Join(git, "hooks"), filepath.Join(git, "config"), filepath.Join(git, "commondir"), filepath.Join(git, "config.worktree")}
	if !slices.Equal(mounts.ReadOnly, wantRO) || !slices.Equal(mounts.Pin, []string{git}) || note != "" {
		t.Fatalf("ro=%q pin=%q note=%q, want ro=%q", mounts.ReadOnly, mounts.Pin, note, wantRO)
	}
	// Each one the session could create is made by the launch, with git's own
	// meaning: a commondir that names the directory itself, an empty settings file.
	want := []confine.Node{{Path: filepath.Join(git, "commondir"), Content: ".\n"}, {Path: filepath.Join(git, "config.worktree")}}
	if !slices.Equal(mounts.Create, want) {
		t.Fatalf("create = %+v, want %+v", mounts.Create, want)
	}
	// Existing ones are bound as they are, and nothing is made.
	writeTestFile(t, filepath.Join(git, "config.worktree"), "[core]\n")
	mounts, _ = New().GitMounts(repo)
	if !slices.Contains(mounts.ReadOnly, filepath.Join(git, "config.worktree")) || slices.Contains(createdBy(mounts), filepath.Join(git, "config.worktree")) {
		t.Fatalf("an existing config.worktree: ro=%q create=%q", mounts.ReadOnly, createdBy(mounts))
	}
}

// Working out the mounts changes nothing in the repository: a host that cannot
// apply them (or a session that is read-only) is left as it was.
func TestGitMountsNameWhatTheyCannotGuardAndChangeNothing(t *testing.T) {
	repo, _, _, _ := worktreeFixture(t)
	git := realPath(t, filepath.Join(repo, ".git"))
	if err := os.RemoveAll(filepath.Join(git, "hooks")); err != nil {
		t.Fatal(err)
	}
	mounts, note := New().GitMounts(repo)
	for _, name := range []string{"hooks", "commondir", "config.worktree"} {
		if _, err := os.Lstat(filepath.Join(git, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("GitMounts made %s: %v", name, err)
		}
	}
	if !slices.Contains(mounts.ReadOnly, filepath.Join(git, "hooks")) || !slices.Contains(createdBy(mounts), filepath.Join(git, "hooks")) || note != "" {
		t.Fatalf("missing hooks directory: ro=%q create=%q note=%q", mounts.ReadOnly, createdBy(mounts), note)
	}
	// A symbolic link is not guarded (the session could replace it) and is named.
	if err := os.Symlink(t.TempDir(), filepath.Join(git, "hooks")); err != nil {
		t.Fatal(err)
	}
	mounts, note = New().GitMounts(repo)
	if slices.Contains(mounts.ReadOnly, filepath.Join(git, "hooks")) || !strings.Contains(note, "hooks is a symbolic link") {
		t.Fatalf("symlinked hooks: ro=%q note=%q", mounts.ReadOnly, note)
	}
}

func TestGitMountsOfAFolderWithoutGitOrWithAnUnreadableLayout(t *testing.T) {
	if ro, pin, note := gitMountsOf(t, t.TempDir()); len(ro)+len(pin) != 0 || note != "" {
		t.Fatalf("a folder without .git: ro=%q pin=%q note=%q", ro, pin, note)
	}
	// A pointer to a Git directory that is not a worktree entry (a submodule, a
	// separate Git directory): the pointer is guarded and the session is told.
	folder := t.TempDir()
	writeTestFile(t, filepath.Join(folder, ".git"), "gitdir: "+t.TempDir()+"\n")
	ro, _, note := gitMountsOf(t, folder)
	if !slices.Equal(ro, []string{filepath.Join(realPath(t, folder), ".git")}) || !strings.Contains(note, "not one this launch can read") {
		t.Fatalf("unreadable layout: ro=%q note=%q", ro, note)
	}
}

// A session that can write its folder is guarded; a read-only one cannot write it
// at all and gets no mounts. Nothing else sets ConfinementMounts, so this is the
// test that a launch asks for them.
func TestChildSpecGuardsTheGitFolderUnlessTheSessionIsReadOnly(t *testing.T) {
	repo, _, data, _ := worktreeFixture(t)
	m := New(WithConfinement([]string{data}, true))
	for _, tc := range []struct {
		mode    string
		guarded bool
	}{{"default", true}, {"acceptEdits", true}, {"plan", false}} {
		p := CreateRunParams{PermissionMode: tc.mode, Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceDir: repo, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
		spec := m.childSpec(p, childDecision{})
		if got := slices.Contains(spec.ConfinementMounts.ReadOnly, filepath.Join(realPath(t, filepath.Join(repo, ".git")), "hooks")); got != tc.guarded {
			t.Errorf("permission mode %s: hooks guarded = %v, want %v (mounts %+v)", tc.mode, got, tc.guarded, spec.ConfinementMounts)
		}
	}
}

// What the mounts could not cover reaches the run's confinement reason, so the
// person who reads it knows what stays writable.
func TestMountsNoteReachesTheConfinementReason(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	folder := t.TempDir()
	m := New(WithConfinement([]string{t.TempDir()}, true))
	_, _, state, release, err := (&procRunner{}).command(t.Context(), LaunchSpec{
		Program: "/bin/true", Dir: folder, Confinement: m.sessionConfinement(folder, nil, PresetAsk), ConfinementRequired: true,
		ConfinementMountsNote: "hooks is a symbolic link",
	})
	if release != nil {
		release()
	}
	if err != nil || !strings.Contains(state.Reason, "some of the folder's Git hooks and configuration stay writable (hooks is a symbolic link)") {
		t.Fatalf("err=%v reason=%q", err, state.Reason)
	}
}

// A main repository's commondir that names another directory is not bound as if
// it were ours: it would be a redirect that the session could not remove.
func TestGitMountsNameAForeignCommondirOfTheMainRepository(t *testing.T) {
	repo, folder, _, _ := worktreeFixture(t)
	common := realPath(t, filepath.Join(repo, ".git"))
	writeTestFile(t, filepath.Join(common, "commondir"), t.TempDir()+"\n")
	mounts, note := New().GitMounts(folder)
	if slices.Contains(mounts.ReadOnly, filepath.Join(common, "commondir")) || !strings.Contains(note, "commondir does not name its own directory") {
		t.Fatalf("ro=%q note=%q", mounts.ReadOnly, note)
	}
}

// No folder is no Git folder: an empty one must not be read as ".git" in the
// engine's own working directory.
func TestGitMountsOfNoFolderGuardsNothing(t *testing.T) {
	repo, _, _, _ := worktreeFixture(t)
	t.Chdir(repo)
	mounts, note := New().GitMounts("")
	if len(mounts.ReadOnly)+len(mounts.Pin)+len(mounts.Create) != 0 || note != "" {
		t.Fatalf("mounts = %+v, note = %q, want none", mounts, note)
	}
}
