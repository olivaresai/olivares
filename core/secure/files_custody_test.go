// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSecret is the single sink for this engine's secret material: the TLS private key
// (tls.go), the audit/catalog/policy signing keys (keys.go), the first-boot setup token
// (setup.go) and sealed envelopes (envelope.go). docs/SECURITY-HARDENING.md cites it as the
// evidence for "strict file perms (0600) on keys/secrets", so what it actually guarantees
// is a published claim, not an implementation detail.
//
// THE DEFECT THESE PIN, measured on the sibling `license keygen` ceremony and then found
// here by looking for the same shape: os.WriteFile applies its perm ONLY on create. The
// staging file used to be `path + ".tmp"` — a PREDICTABLE name in the data directory — so
// anything already sitting there at a wider mode was truncated, KEPT that mode, and was
// then renamed over the target. The signing key landed world-readable and the function
// returned nil.

func TestWriteSecretCreatesAtTheModeItPromises(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "signing.key")
	if err := writeSecret(p, []byte("k\n")); err != nil {
		t.Fatalf("writeSecret on a clean directory: %v", err)
	}
	mustPerm(t, p, 0o600)
}

// THE REGRESSION. A leftover staging file at a lax mode — from a restore, a copy, an
// interrupted run or an older version — used to decide the mode of the installed secret.
func TestWriteSecretIgnoresALeftoverStagingFileAtAWiderMode(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "signing.key")
	planted := p + ".tmp"
	if err := os.WriteFile(planted, []byte("planted\n"), 0o644); err != nil { //nolint:gosec // the point of the fixture
		t.Fatal(err)
	}

	if err := writeSecret(p, []byte("real-secret\n")); err != nil {
		t.Fatalf("writeSecret: %v", err)
	}
	// Under the old implementation this was 0644: the planted file was truncated, kept its
	// mode and was renamed into place.
	mustPerm(t, p, 0o600)

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "real-secret\n" {
		t.Fatalf("installed contents = %q, want the secret just written", got)
	}
	// And the predictable name is no longer part of the mechanism at all: a planted file
	// there must be left exactly as it was found rather than consumed.
	if b, rerr := os.ReadFile(planted); rerr != nil || string(b) != "planted\n" {
		t.Fatalf("the predictable staging path was used after all (read %q, err %v)", b, rerr)
	}
}

// The staging file must not survive a successful write under any name: a secret left in a
// second file is a second copy nobody is tracking.
func TestWriteSecretLeavesNoStagingFileBehind(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "signing.key")
	if err := writeSecret(p, []byte("k\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "signing.key" {
			t.Fatalf("writeSecret left %q behind beside the installed secret", e.Name())
		}
	}
}

// assertPerm is the load-bearing half: without it every guarantee above is a comment. This
// proves it can refuse, so the tests above are not resting on a check that never fires.
func TestAssertPermRefusesAModeThatDoesNotMatch(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil { //nolint:gosec // the point of the fixture
		t.Fatal(err)
	}
	err := assertPerm(p, 0o600)
	if err == nil {
		t.Fatal("assertPerm accepted 0644 where 0600 was promised")
	}
	if !strings.Contains(err.Error(), "0644") || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("the refusal does not name both modes: %v", err)
	}
	if err := assertPerm(p, 0o644); err != nil {
		t.Fatalf("assertPerm rejected the mode the file actually has: %v", err)
	}
}

func mustPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Fatalf("%s has mode %04o, want %04o", path, got, want)
	}
}
