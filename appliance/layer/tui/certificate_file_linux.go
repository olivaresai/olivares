// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package tui

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"os"
	"syscall"
)

// ReadCertificateFingerprint reads only the stored tls.crt in dir. It returns the
// leaf certificate's SHA-256 fingerprint, not a claim about key possession, trust,
// validity or the certificate currently served by the portal. It never reads a key.
func ReadCertificateFingerprint(dir string) (string, bool, string) {
	before, err := os.Lstat(dir)
	if err != nil || !before.IsDir() || !protectedCertificatePath(before) {
		return "", false, "certificate directory is not protected"
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", false, "certificate directory cannot be opened"
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) || !protectedCertificatePath(opened) {
		return "", false, "certificate directory changed"
	}
	measured, err := root.Lstat("tls.crt")
	if err != nil || !measured.Mode().IsRegular() || !protectedCertificatePath(measured) {
		return "", false, "certificate is not a protected regular file"
	}
	// O_NONBLOCK prevents a replaced FIFO from holding the repair console. Check
	// both the opened inode and the path again before reading any bytes.
	file, err := root.OpenFile("tls.crt", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false, "certificate cannot be opened"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(measured, info) || !protectedCertificatePath(info) {
		return "", false, "certificate changed or is not protected"
	}
	current, err := root.Lstat("tls.crt")
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return "", false, "certificate path changed"
	}
	const limit = 64 << 10
	if info.Size() > limit {
		return "", false, "certificate exceeds the size limit"
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return "", false, "certificate cannot be read within the size limit"
	}
	var fingerprint string
	rest := bytes.TrimSpace(data)
	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return "", false, "certificate file contains invalid certificate data"
		}
		endMarker := []byte("-----END CERTIFICATE-----")
		end := bytes.Index(rest, endMarker)
		if end < 0 {
			return "", false, "certificate file contains invalid certificate data"
		}
		end += len(endMarker)
		block, trailing := pem.Decode(rest[:end])
		if block == nil || len(bytes.TrimSpace(trailing)) != 0 || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return "", false, "certificate file contains invalid certificate data"
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", false, "certificate file contains invalid certificate data"
		}
		if fingerprint == "" {
			sum := sha256.Sum256(cert.Raw)
			fingerprint = hex.EncodeToString(sum[:])
		}
		rest = bytes.TrimSpace(rest[end:])
	}
	if fingerprint == "" {
		return "", false, "certificate file is empty"
	}
	return fingerprint, true, ""
}

// protectedCertificatePath admits the service owner (root in production) or root,
// with no group or other write access. It does not inspect any private key.
func protectedCertificatePath(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == 0 || st.Uid == uint32(os.Geteuid())) && info.Mode().Perm()&0o022 == 0
}
