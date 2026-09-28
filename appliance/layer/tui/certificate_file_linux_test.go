// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package tui

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// This public-only fixture has an independent OpenSSL x509 SHA-256 fingerprint.
const publicCertificateFixture = `-----BEGIN CERTIFICATE-----
MIIBmDCCAT2gAwIBAgIUIYrW+1ENnvYMQvcxB5kHm/Hk27AwCgYIKoZIzj0EAwIw
ITEfMB0GA1UEAwwWcmVwYWlyLWNvbnNvbGUuaW52YWxpZDAeFw0yNjA5MjcyMTI2
MThaFw0yNjA5MjgyMTI2MThaMCExHzAdBgNVBAMMFnJlcGFpci1jb25zb2xlLmlu
dmFsaWQwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAAR7T7sNOah7Mzvob0aR5Vp3
8U6+7xAPOR3b8g4zA27TAPnTDsNR9UYaSHI3hvwDrNM0nmRJxW24Xong0AoWRmG5
o1MwUTAdBgNVHQ4EFgQUQifVHBlaj9WbNG5zck8RsWJ2YbswHwYDVR0jBBgwFoAU
QifVHBlaj9WbNG5zck8RsWJ2YbswDwYDVR0TAQH/BAUwAwEB/zAKBggqhkjOPQQD
AgNJADBGAiEA9eQCn5MfBW/82HaHtayC8807JSOr8iGLSaFb5Z6PXGoCIQDNGa3D
Xiti6CgsflIGZH9zvk/Ou1lbmAjWGRBnOVJ2fg==
-----END CERTIFICATE-----
`
const otherPublicCertificateFixture = `-----BEGIN CERTIFICATE-----
MIIBojCCAUmgAwIBAgIURR/e8EVIBgxzzm+XakuwphKY2qowCgYIKoZIzj0EAwIw
JzElMCMGA1UEAwwcb3RoZXItcmVwYWlyLWNvbnNvbGUuaW52YWxpZDAeFw0yNjA5
MjcyMTMwMTZaFw0yNjA5MjgyMTMwMTZaMCcxJTAjBgNVBAMMHG90aGVyLXJlcGFp
ci1jb25zb2xlLmludmFsaWQwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAASmVWuD
xlsltFk06orlqq+yRb9ccauS7k2DfrQL0t3etcKg9/zsJQJVCQIXfIIydbkzOkmi
Nw2xMEsADPHN+ONjo1MwUTAdBgNVHQ4EFgQUFNW/abqJjWsq6JZ944g5qh9jDZ4w
HwYDVR0jBBgwFoAUFNW/abqJjWsq6JZ944g5qh9jDZ4wDwYDVR0TAQH/BAUwAwEB
/zAKBggqhkjOPQQDAgNHADBEAiA5Wsg356piVy5KXRE4B9r1t53u177GUThSvLAA
CdnavwIgWm9uokZ5HRAy5CGUd3smqeo0O52wyE0C6gWhQIDPE18=
-----END CERTIFICATE-----
`

const publicCertificateSHA256 = "845c7ec2761753b006d3d8da18879c3e022ce04d097da67222870cff1df993a4"

func TestConsole_PublicCertificateFingerprintDoesNotNeedAPrivateKey(t *testing.T) {
	for _, keyKind := range []string{"absent", "fifo"} {
		t.Run(keyKind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte(publicCertificateFixture), 0o644); err != nil {
				t.Fatal(err)
			}
			if keyKind == "fifo" {
				if err := syscall.Mkfifo(filepath.Join(dir, "tls.key"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fingerprint, ok, reason := ReadCertificateFingerprint(dir)
			if !ok || reason != "" || fingerprint != publicCertificateSHA256 {
				t.Fatalf("public fingerprint = %q, %v, %q; want %s", fingerprint, ok, reason, publicCertificateSHA256)
			}
		})
	}
}

func TestConsole_PublicCertificateFingerprintRejectsUnsafeFiles(t *testing.T) {
	cases := map[string]func(*testing.T, string){
		"empty": func(t *testing.T, dir string) { writePublicCertificate(t, dir, "") },
		"directory": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "tls.crt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "tls.crt"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"malformed before valid certificate": func(t *testing.T, dir string) {
			writePublicCertificate(t, dir, "-----BEGIN CERTIFICATE-----\n???\n-----END CERTIFICATE-----\n"+publicCertificateFixture)
		},
		"missing": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "tls.crt")); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "other.crt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other.crt", filepath.Join(dir, "tls.crt")); err != nil {
				t.Fatal(err)
			}
		},
		"fifo": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "tls.crt")); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(filepath.Join(dir, "tls.crt"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"writable file": func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "tls.crt"), 0o666); err != nil {
				t.Fatal(err)
			}
		},
		"writable directory": func(t *testing.T, dir string) {
			if err := os.Chmod(dir, 0o777); err != nil {
				t.Fatal(err)
			}
		},
		"oversized": func(t *testing.T, dir string) {
			writePublicCertificate(t, dir, publicCertificateFixture+strings.Repeat(" ", 65536))
		},
		"invalid DER": func(t *testing.T, dir string) {
			writePublicCertificate(t, dir, "-----BEGIN CERTIFICATE-----\nYWJj\n-----END CERTIFICATE-----\n")
		},
		"leading junk": func(t *testing.T, dir string) {
			writePublicCertificate(t, dir, "untrusted input\n"+publicCertificateFixture)
		},
		"trailing junk": func(t *testing.T, dir string) {
			writePublicCertificate(t, dir, publicCertificateFixture+"untrusted input")
		},
		"non-certificate block": func(t *testing.T, dir string) {
			block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("abc")})
			writePublicCertificate(t, dir, publicCertificateFixture+string(block))
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writePublicCertificate(t, dir, publicCertificateFixture)
			prepare(t, dir)
			fingerprint, ok, reason := ReadCertificateFingerprint(dir)
			if ok || fingerprint != "" || reason == "" || strings.Contains(reason, dir) {
				t.Fatalf("unsafe input returned %q, %v, %q", fingerprint, ok, reason)
			}
		})
	}
}

func TestConsole_PublicCertificateFingerprintUsesTheLeafOfAChain(t *testing.T) {
	dir := t.TempDir()
	writePublicCertificate(t, dir, publicCertificateFixture+otherPublicCertificateFixture)
	fingerprint, ok, reason := ReadCertificateFingerprint(dir)
	if !ok || reason != "" || fingerprint != publicCertificateSHA256 {
		t.Fatalf("chain returned %q, %v, %q", fingerprint, ok, reason)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(dir, linked); err != nil {
		t.Fatal(err)
	}
	if fingerprint, ok, _ := ReadCertificateFingerprint(linked); ok || fingerprint != "" {
		t.Fatal("symlink directory accepted")
	}
}

func writePublicCertificate(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
