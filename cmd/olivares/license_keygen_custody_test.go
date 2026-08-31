// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The custody promises of `license keygen`, each one pinned by the failure it is there to
// prevent. All four came from measuring the command BEFORE the fix, not from reading it:
//
//   - os.WriteFile applies its perm ONLY on create. A target that already existed at 0644
//     kept 0644 while the private key material was written into it, and the command exited
//     0. The flag help said "mode 0600". Measured with a probe: mode=644 before, mode=644
//     after, contents replaced by the key.
//   - There was no exclusive create and no --force, so re-running a ceremony against the
//     wrong path destroyed the previous signing anchor in silence.
//
// Whoever holds this key mints licenses, so "the command said 0600" is not a detail.

func runKeygen(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"license", "keygen"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestKeygenCreatesThePrivateKeyAtTheModeItPromises(t *testing.T) {
	d := t.TempDir()
	priv := filepath.Join(d, "p.key")
	pub := filepath.Join(d, "pub.key")

	if _, err := runKeygen(t, "--out-private", priv, "--out-public", pub); err != nil {
		t.Fatalf("keygen on a clean directory: %v", err)
	}
	assertPerm(t, priv, 0o600)
	assertPerm(t, pub, 0o644)
}

func TestKeygenRefusesToDestroyAnExistingKeyAndLeavesNoHalfPair(t *testing.T) {
	d := t.TempDir()
	priv := filepath.Join(d, "existing.key")
	pub := filepath.Join(d, "pub.key")
	// The custody file as an operator would really find it: already there, and with the
	// lax mode that made the old code's promise false.
	if err := os.WriteFile(priv, []byte("DO-NOT-OVERWRITE\n"), 0o644); err != nil { //nolint:gosec // the point of the fixture
		t.Fatal(err)
	}

	out, err := runKeygen(t, "--out-private", priv, "--out-public", pub)
	if err == nil {
		t.Fatal("keygen overwrote an existing private key and reported success")
	}
	if !strings.Contains(out+err.Error(), "already exists") {
		t.Fatalf("refusal does not name the reason; got %q / %v", out, err)
	}
	got, rerr := os.ReadFile(priv)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "DO-NOT-OVERWRITE\n" {
		t.Fatalf("the existing key was modified: %q", got)
	}
	// A refusal that still wrote the public half would leave the ceremony half-done.
	if _, serr := os.Stat(pub); serr == nil {
		t.Fatal("the public key was written even though the run was refused")
	}
}

func TestKeygenForceReplacesAndFIXESTheModeRatherThanInheritingIt(t *testing.T) {
	d := t.TempDir()
	priv := filepath.Join(d, "existing.key")
	pub := filepath.Join(d, "pub.key")
	if err := os.WriteFile(priv, []byte("DO-NOT-OVERWRITE\n"), 0o644); err != nil { //nolint:gosec // the point of the fixture
		t.Fatal(err)
	}

	if _, err := runKeygen(t, "--out-private", priv, "--out-public", pub, "--force"); err != nil {
		t.Fatalf("keygen --force: %v", err)
	}
	// THE regression: os.WriteFile would have truncated in place and left 0644 here.
	assertPerm(t, priv, 0o600)
	got, err := os.ReadFile(priv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "DO-NOT-OVERWRITE") {
		t.Fatal("--force did not replace the contents")
	}
}

func TestKeygenRemovesThePrivateKeyWhenThePublicHalfCannotLand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unwritable directory is still writable, so this case cannot be posed")
	}
	d := t.TempDir()
	priv := filepath.Join(d, "orphan.key")
	locked := filepath.Join(d, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}

	out, err := runKeygen(t, "--out-private", priv, "--out-public", filepath.Join(locked, "pub.key"))
	if err == nil {
		t.Fatal("keygen reported success while the public key could not be written")
	}
	// A private key whose public half never landed is an anchor that matches nothing, and
	// the operator has no way to know which of the two halves exists.
	if _, serr := os.Stat(priv); serr == nil {
		t.Fatal("the private key was left behind after the public half failed")
	}
	if !strings.Contains(out+err.Error(), "removed") {
		t.Fatalf("the failure does not say the private key was removed; got %q / %v", out, err)
	}
}

// assertMode is the load-bearing half of the fix: without it every guarantee is a comment.
// This proves it can actually refuse, so the three tests above are not resting on a check
// that never fires.
func TestAssertModeRefusesAModeThatDoesNotMatch(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil { //nolint:gosec // the point of the fixture
		t.Fatal(err)
	}
	if err := assertMode(p, 0o600, "key material"); err == nil {
		t.Fatal("assertMode accepted 0644 where 0600 was promised")
	}
	if err := assertMode(p, 0o644, "key material"); err != nil {
		t.Fatalf("assertMode rejected the mode the file actually has: %v", err)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Fatalf("%s has mode %04o, want %04o", path, got, want)
	}
}
