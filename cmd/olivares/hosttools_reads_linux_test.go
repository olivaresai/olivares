// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/modules/sessions"
)

// hostToolsBytesRead is what this process has asked read(2) for so far (rchar),
// served from the page cache or not: a hash of an installed tool shows here either way.
func hostToolsBytesRead(t *testing.T) int64 {
	t.Helper()
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Skipf("/proc/self/io is not readable: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "rchar:"); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Skip("/proc/self/io has no rchar line")
	return 0
}

// observedClaude installs a fake Claude Code whose executable is padding bytes longer
// through the real installer, and returns an observer over that tools root together
// with the executable the install placed.
func observedClaude(t *testing.T, padding int) (*hostToolObserver, string) {
	t.Helper()
	f := newAgentToolFixture(t)
	previous := verifiedToolReleases
	verifiedToolReleases = toolinstall.NewVerifiedCache()
	t.Cleanup(func() { verifiedToolReleases = previous })
	// The padding is one trailing shell comment: the program still reports its version.
	exe := append(toolinstalltest.Executable("2.1.261", f.marker), bytes.Repeat([]byte("#"), padding)...)
	f.srv.Publish(t, f.key, "2.1.261", map[string][]byte{"linux-x64": exe})
	f.srv.SetPointer("latest", "2.1.261")
	if code, out, errOut := f.run(f.installArgs("--version", "2.1.261", "--yes", "-o", "json")...); code != 0 {
		t.Fatalf("install = %d: %s %s", code, out, errOut)
	}
	inv, err := toolInstallEngine(context.Background()).List(context.Background(), f.root)
	if err != nil || len(inv.Installed) != 1 {
		t.Fatalf("inventory: %+v %v", inv, err)
	}
	return newHostToolObserverAt(f.root, nil, filepath.Join(t.TempDir(), "empty-home"), ""), inv.Installed[0].Executable
}

// observeManaged returns the match of the one managed candidate the observer reports.
func observeManaged(t *testing.T, o *hostToolObserver, program string) string {
	t.Helper()
	obs, err := o.ObserveHostTools(context.Background(), "claude", program)
	if err != nil {
		t.Fatal(err)
	}
	var managed []sessions.HostToolCandidate
	for _, c := range obs.Candidates {
		if c.Origin == "managed" {
			managed = append(managed, c)
		}
	}
	if len(managed) != 1 {
		t.Fatalf("managed candidates: %+v", obs.Candidates)
	}
	return managed[0].Match
}

// dev#2609: GET provider-profiles/{ref}/host-tools asks the observer about Claude Code. The
// observer's verifier is unavailable on purpose, so its answer is "unverified" and the cache
// keeps only installed releases: every call read the whole executable (251 MB in the release
// container) and kept nothing. An executable that has not changed is read once.
func TestHostToolObservationDoesNotReadAnUnchangedClaudeExecutableAgain(t *testing.T) {
	const padding = 4 << 20
	observer, exe := observedClaude(t, padding)
	beforeFirst := hostToolsBytesRead(t)
	if got := observeManaged(t, observer, exe); got != "unverified" {
		t.Fatalf("the first observation answered %q, want unverified", got)
	}
	// The first observation does read the executable once; without that this test measures nothing.
	if n := hostToolsBytesRead(t) - beforeFirst; n < padding {
		t.Fatalf("the first observation read only %d bytes; rchar does not show the hash of its %d-byte executable", n, padding)
	}
	before := hostToolsBytesRead(t)
	for i := 0; i < 3; i++ {
		if got := observeManaged(t, observer, exe); got != "unverified" {
			t.Fatalf("observation %d answered %q, want unverified", i+2, got)
		}
	}
	if n := hostToolsBytesRead(t) - before; n >= padding {
		t.Fatalf("three observations of an unchanged Claude read %d bytes; a hash of its %d-byte executable per call is dev#2609", n, padding)
	}
}

// The answer does not depend on how often it is asked: bytes that no longer match the
// receipt are "damaged", whether the executable was kept or rewritten in place with the
// same size (the identity, not the path or the size, decides whether a read is repeated).
func TestHostToolObservationStillNamesAChangedClaudeExecutableDamaged(t *testing.T) {
	observer, exe := observedClaude(t, 1<<10)
	if got := observeManaged(t, observer, exe); got != "unverified" {
		t.Fatalf("before the change the observation answered %q, want unverified", got)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-2] ^= 1
	rewriteInPlace(t, exe, body)
	for i := 0; i < 2; i++ {
		if got := observeManaged(t, observer, exe); got != "damaged" {
			t.Fatalf("observation %d after a same-size rewrite answered %q, want damaged", i+1, got)
		}
	}
}

// rewriteInPlace writes body over path keeping the inode and the mode: the bytes change,
// the size does not, the ctime moves.
func rewriteInPlace(t *testing.T, path string, body []byte) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fi.Mode().Perm()|0o200); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

// dev#2614: Detect hashed every vendor-default file and every PATH entry it found, and the
// host observer reads only their origin, match, executable bit and identity, never the digest.
// A native install (a version file, the symlink to it on the PATH) cost two hashes of a
// 263 MB executable per observation, because the symlink and its target were hashed
// separately before the dedupe by resolved path. The observation itself must not change.
func TestHostToolObservationDoesNotReadTheExecutablesItFinds(t *testing.T) {
	const padding = 4 << 20
	const observations = 3
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	vendor := filepath.Join(home, ".local", "share", "claude", "versions", "2.1.292")
	linked := filepath.Join(dir, "linked")
	copied := filepath.Join(dir, "copied")
	body := append([]byte("#!/bin/sh\n"), bytes.Repeat([]byte("#"), padding)...)
	for _, d := range []string{filepath.Dir(vendor), linked, copied} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{vendor, filepath.Join(copied, "claude")} {
		if err := os.WriteFile(p, body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(vendor, filepath.Join(linked, "claude")); err != nil {
		t.Fatal(err)
	}
	pathEnv := linked + string(os.PathListSeparator) + copied
	observer := newHostToolObserverAt(filepath.Join(dir, "tools"), nil, home, pathEnv)
	program := filepath.Join(copied, "claude")

	// The symlink resolves to the vendor file, so it is that candidate's alias; the PATH copy
	// is the configured program.
	want := map[hostToolTuple]int{
		{"vendor-default", "unregistered-observed", true, "different"}: 1,
		{"path", "unregistered-observed", true, "same"}:                1,
	}
	before := processBytesRead(t)
	for i := 0; i < observations; i++ {
		res, err := observer.ObserveHostTools(context.Background(), "claude", program)
		if err != nil || res.UnsupportedDriver {
			t.Fatalf("observation %d: %+v %v", i+1, res, err)
		}
		if got := tallyHostTools(res); len(res.Candidates) != 2 || !equalTally(got, want) {
			t.Fatalf("observation %d candidates %+v, want %v", i+1, res.Candidates, want)
		}
	}
	if n := processBytesRead(t) - before; n >= padding {
		t.Fatalf("%d observations of three %d-byte executables read %d bytes; the observer needs none of their digests, and one hash of one of them is already %d", observations, padding, n, padding)
	}
}
