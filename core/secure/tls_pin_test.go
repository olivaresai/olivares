// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"path/filepath"
	"testing"
)

// TestCertificateLoaderPinIsTheFilePin: the pin server-info reports (the loader's served
// certificate) and the pin the start line logs (SPKIPin of the file) are one value.
func TestCertificateLoaderPinIsTheFilePin(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCert(cert, key, false); err != nil {
		t.Fatal(err)
	}
	loader, err := NewCertificateLoader(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	served, err := loader.Pin()
	if err != nil {
		t.Fatal(err)
	}
	logged, err := SPKIPin(cert)
	if err != nil {
		t.Fatal(err)
	}
	if served == "" || served != logged {
		t.Fatalf("loader pin %q, file pin %q", served, logged)
	}
}
