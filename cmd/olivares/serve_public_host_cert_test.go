// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/readyzprobe"
)

// serveCertPublicHost is a TEST-NET-1 address: never assigned to a machine, so no
// interface of the test host can name it by accident.
const serveCertPublicHost = "192.0.2.17"

func serveLeaf(t *testing.T, dataDir string) *x509.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(dataDir, "tls.crt"), filepath.Join(dataDir, "tls.key"))
	if err != nil {
		t.Fatalf("the engine wrote no usable first-boot certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// TestServeFirstBootCertificateNamesTheDeclaredPublicURLHost is the Docker case of
// dev#2763: the engine inside a container sees only its own interfaces, so the
// address operators type is a name only OLIVARES_PUBLIC_URL can supply. It drives the
// real runEngine, the one place the declared address meets the certificate.
func TestServeFirstBootCertificateNamesTheDeclaredPublicURLHost(t *testing.T) {
	for _, tc := range []struct {
		name, publicURL string
		covered         bool
	}{
		{"declared", "https://" + serveCertPublicHost + ":8443", true},
		{"not declared", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(publicURLEnv, tc.publicURL)
			dataDir := t.TempDir()
			opts := serveOptions{
				listen: bindAnnounceFreeAddr(t), grpcListen: bindAnnounceFreeAddr(t), dataDir: dataDir,
				engine: "sqlite",
			}
			// The Compose healthcheck: readyz trusting the chain the engine publishes.
			var probeErr error
			var probe readyzprobe.Result
			res := runBindAnnounce(t, opts, &bindAnnounceBuffer{}, serveAnnounce(false), func(base string) {
				probe, probeErr = readyzprobe.Check(t.Context(), readyzprobe.Config{
					Origin: base, CACert: filepath.Join(dataDir, "public", "tls.crt"), Timeout: 5 * time.Second,
				})
			})
			if !res.answered {
				t.Fatalf("the engine never answered over HTTPS: %v", res.err)
			}
			if probeErr != nil || probe.Outcome == readyzprobe.Unmeasurable {
				t.Errorf("readyz does not trust the chain the engine published: %+v, %v", probe, probeErr)
			}
			leaf := serveLeaf(t, dataDir)
			if got := leaf.VerifyHostname(serveCertPublicHost) == nil; got != tc.covered {
				t.Errorf("certificate covers %s = %v, want %v (DNSNames=%v IPAddresses=%v)",
					serveCertPublicHost, got, tc.covered, leaf.DNSNames, leaf.IPAddresses)
			}
			if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
				t.Errorf("the certificate lost its loopback name: %v", err)
			}
		})
	}
}
