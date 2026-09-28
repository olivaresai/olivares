// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Names of the TLS files in the operator's directory and in the service's credentials
// directory.
const (
	CertificateFile = "tls.crt"
	KeyFile         = "tls.key"
)

const maxCredentialBytes = 64 * 1024

// Custody is the verdict on the TLS material the console would serve.
type Custody struct {
	Verified bool
	// CertificateSHA256 is the hex SHA-256 of the leaf certificate, set only when
	// verified. It is what a browser shows, not a public-key pin.
	CertificateSHA256 string
	// Reason is a fixed phrase naming the failed check. It carries no path and no key
	// material.
	Reason string
}

// CheckTLSCustody measures the operator's TLS files in source and reads the pair the
// service manager delivered into delivered, normally the service's
// $CREDENTIALS_DIRECTORY. Custody is measured where the key lives: source must be a
// directory, not a symbolic link, owned by root or by this process and writable by its
// owner only; in it the certificate and the key must be regular files with such an
// owner, the certificate writable by its owner only and the key mode 0600 or 0400. The
// key is measured with lstat and never opened, which needs search permission on source
// and no read permission on it. Where SELinux is enabled, enforcing or permissive, the key
// must also carry PortalKeyType, read from its label, again without opening it. The
// delivered copies are read as regular files, never through a symbolic link, and their mode
// is not judged: the service manager chose it.
// The delivered certificate must be byte for byte the operator's certificate, which the
// service reads; as the delivered key must pair with it, a delivery can only verify with
// the private key of the operator's certificate. source and delivered may name one
// directory when the caller can read the operator's files itself. Nothing is created,
// repaired or logged.
func CheckTLSCustody(source, delivered string) (Custody, *tls.Certificate) {
	if source == "" {
		return Custody{Reason: "no TLS source directory"}, nil
	}
	if delivered == "" {
		return Custody{Reason: "no credentials directory"}, nil
	}
	if reason := measureSource(source); reason != "" {
		return Custody{Reason: reason}, nil
	}
	operatorPEM, err := readRegularFile(filepath.Join(source, CertificateFile))
	if err != nil {
		return Custody{Reason: "certificate " + err.Error()}, nil
	}
	certPEM, err := readRegularFile(filepath.Join(delivered, CertificateFile))
	if err != nil {
		return Custody{Reason: "delivered certificate " + err.Error()}, nil
	}
	if !bytes.Equal(certPEM, operatorPEM) {
		return Custody{Reason: "delivered certificate is not the operator's certificate"}, nil
	}
	keyPEM, err := readRegularFile(filepath.Join(delivered, KeyFile))
	if err != nil {
		return Custody{Reason: "delivered key " + err.Error()}, nil
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return Custody{Reason: "certificate and key do not form a pair"}, nil
	}
	sum := sha256.Sum256(pair.Certificate[0])
	return Custody{Verified: true, CertificateSHA256: hex.EncodeToString(sum[:])}, &pair
}

// measureSource returns why the operator's TLS directory fails custody, or "" when it
// holds.
func measureSource(dir string) string {
	info, err := os.Lstat(dir)
	switch {
	case err != nil:
		return "TLS source directory is absent or cannot be measured"
	case !info.IsDir():
		return "TLS source directory is not a directory"
	case !ownedByRootOrSelf(info):
		return "TLS source directory has an owner other than root or this service"
	case info.Mode().Perm()&0o022 != 0:
		return "TLS source directory is writable by group or others"
	}
	for _, file := range []struct {
		name, label, rule string
		allowed           func(os.FileMode) bool
	}{
		{CertificateFile, "certificate", "is writable by group or others", func(m os.FileMode) bool { return m&0o022 == 0 }},
		{KeyFile, "key", "is not mode 0600 or 0400", func(m os.FileMode) bool { return m == 0o600 || m == 0o400 }},
	} {
		info, err := os.Lstat(filepath.Join(dir, file.name))
		switch {
		case err != nil:
			return file.label + " is absent or cannot be measured"
		case !info.Mode().IsRegular():
			return file.label + " is not a regular file"
		case !ownedByRootOrSelf(info):
			return file.label + " has an owner other than root or this service"
		case !file.allowed(info.Mode().Perm()):
			return file.label + " " + file.rule
		}
	}
	return measureKeyLabel(filepath.Join(dir, KeyFile))
}

var (
	errUnreadable = errors.New("is absent or unreadable")
	errNotRegular = errors.New("is not a regular file")
	errTooLarge   = errors.New("exceeds 64 KiB")
	errChanged    = errors.New("changed while it was read")
)

// readRegularFile refuses a symbolic link or other non-regular file, an oversized file
// and a file replaced between the check and the read.
func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errUnreadable
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	if info.Size() > maxCredentialBytes {
		return nil, errTooLarge
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errUnreadable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errChanged
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil {
		return nil, errUnreadable
	}
	if len(data) > maxCredentialBytes {
		return nil, errTooLarge
	}
	return data, nil
}

// ownedByRootOrSelf reports whether the file is owned by root or by this process's user,
// as fileOwner reports the owner.
func ownedByRootOrSelf(info os.FileInfo) bool {
	uid, ok := fileOwner(info)
	return ok && (uid == 0 || uid == uint32(os.Getuid()))
}
