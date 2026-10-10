// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/base64"
	"github.com/olivaresai/olivares/core/release"
	"os"
	"path/filepath"
	"testing"
)

// writeBundleDeclaring writes the fixture's bundle with an "edition" field spliced into the
// signed manifest bytes, which are otherwise unchanged. With resign=false the signature over
// the UNDECLARED bytes is kept, so the only difference the verifier sees is the claim nobody
// signed.
func (f *updFixture) writeBundleDeclaring(t *testing.T, edition string, resign bool) string {
	t.Helper()
	dir := f.writeBundle(t)
	path := filepath.Join(dir, "manifest.json")
	mb, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(mb, []byte("{")) {
		t.Fatalf("fixture manifest is not a JSON object: %q", mb)
	}
	declared := append([]byte(`{"edition":"`+edition+`",`), mb[1:]...)
	if err := os.WriteFile(path, declared, 0o644); err != nil {
		t.Fatal(err)
	}
	if resign {
		sig := base64.StdEncoding.EncodeToString(release.SignManifest(declared, f.priv))
		if err := os.WriteFile(filepath.Join(dir, "manifest.json.sig"), []byte(sig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
