// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// publishTLSCertificate encodes only the validated served chain. Reading and
// copying the source PEM could publish a private block from an operator bundle.
// The engine owns the parent data directory; the dedicated public directory
// contains no keys and must never be a symlink or writable by another account.
func publishTLSCertificate(path string, chain [][]byte) error {
	for _, der := range chain {
		if _, err := x509.ParseCertificate(der); err != nil {
			return fmt.Errorf("server chain contains non-certificate material")
		}
	}
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir, 0o755); err == nil {
		if err := os.Chmod(dir, 0o755); err != nil {
			return err
		}
	} else if !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("public certificate directory is unsafe")
	}
	file, err := os.CreateTemp(dir, ".olivares-cert-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o644); err != nil {
		return err
	}
	for _, der := range chain {
		if err := pem.Encode(file, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
