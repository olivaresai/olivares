// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package secure

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A token the engine cannot read is not a wrong token (#530): Match says so, with the
// path and the reason, while a wrong, missing or consumed token stays a plain false.
func TestSetupTokenMatchTellsAnUnreadableFileFromAWrongToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.token")
	st := NewSetupToken(path)
	tok, _, err := st.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.Match("olst_wrong"); ok || err != nil {
		t.Fatalf("Match(wrong) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := st.Match(tok); !ok || err != nil {
		t.Fatalf("Match(token) = (%v, %v), want (true, nil)", ok, err)
	}

	// A mode the engine refuses to read a secret at (an operator's chmod).
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	ok, err := st.Match(tok)
	if ok || !errors.Is(err, ErrSetupTokenUnreadable) || !strings.Contains(err.Error(), path) {
		t.Fatalf("Match at 0640 = (%v, %v), want ErrSetupTokenUnreadable naming %s", ok, err, path)
	}
	if st.Verify(tok) {
		t.Fatal("Verify accepted a token it refused to read")
	}

	// A file the engine account may not open (a package install that wrote it as another
	// account, #514). Root opens any file, so this half needs another account.
	if os.Geteuid() != 0 {
		if err := os.Chmod(path, 0o200); err != nil {
			t.Fatal(err)
		}
		ok, err = st.Match(tok)
		if ok || !errors.Is(err, ErrSetupTokenUnreadable) || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), path) {
			t.Fatalf("Match at 0200 = (%v, %v), want ErrSetupTokenUnreadable wrapping EACCES and naming %s", ok, err, path)
		}
	}

	// A file that holds no SHA-256 (emptied, truncated, hand-edited) can match nothing
	// either, and is the deployment's to fix the same way.
	for _, body := range []string{"", "not base32!\n", "MFRGG\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		ok, err := st.Match(tok)
		if ok || !errors.Is(err, ErrSetupTokenUnreadable) || !strings.Contains(err.Error(), path) {
			t.Fatalf("Match over %q = (%v, %v), want ErrSetupTokenUnreadable naming %s", body, ok, err, path)
		}
	}

	// A directory this account may not search: the state is unknown, not "no token".
	if os.Geteuid() != 0 {
		dir := filepath.Dir(path)
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		ok, err = st.Match(tok)
		if cerr := os.Chmod(dir, 0o700); cerr != nil {
			t.Fatal(cerr)
		}
		if ok || !errors.Is(err, ErrSetupTokenUnreadable) || !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("Match in an unsearchable directory = (%v, %v), want ErrSetupTokenUnreadable wrapping EACCES", ok, err)
		}
	}

	if err := st.Consume(); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.Match(tok); ok || err != nil {
		t.Fatalf("Match(consumed) = (%v, %v), want (false, nil)", ok, err)
	}
}
