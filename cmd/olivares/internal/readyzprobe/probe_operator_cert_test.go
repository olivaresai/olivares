// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package readyzprobe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// operatorLeaf is a certificate an operator brings with --tls-cert: it names the
// host other machines type, never the loopback address the in-container probe dials.
// TEST-NET and .test names are never assigned to a machine.
func operatorLeaf(t *testing.T, dns string, ip net.IP, issuer ...tls.Certificate) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: ip.String()},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{ip},
	}
	if dns != "" {
		template.DNSNames = []string{dns}
	}
	parent, signer := template, key
	if len(issuer) > 0 {
		var err error
		if parent, err = x509.ParseCertificate(issuer[0].Certificate[0]); err != nil {
			t.Fatal(err)
		}
		signer = issuer[0].PrivateKey.(*ecdsa.PrivateKey)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// operatorCA is the private CA an organization signs its server certificates with.
func operatorCA(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "operator test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func serveOperatorLeaf(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.URL
}

func anchorFile(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tls.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The shipped Compose healthcheck probes 127.0.0.1 and trusts the certificate the
// engine serves. An operator certificate for the host address has no
// loopback name, and the container stayed unhealthy on a hostname mismatch.
func TestCheckTrustsAnOperatorCertificateThatDoesNotNameLoopback(t *testing.T) {
	for _, tc := range []struct{ name, dns string }{
		{"DNS name and address", "olivares.example.test"},
		{"address only", ""},
		// Checking the wildcard as written works because Go matches a "*" label; pinned so a
		// change in the standard library cannot silently leave an operator's container unhealthy.
		{"wildcard name", "*.corp.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert := operatorLeaf(t, tc.dns, net.ParseIP("192.0.2.17"))
			got, err := Check(t.Context(), Config{Origin: serveOperatorLeaf(t, cert), CACert: anchorFile(t, cert), Timeout: time.Second})
			if err != nil || got.Outcome != Ready {
				t.Fatalf("operator certificate as the trust anchor: result=%+v err=%v, want ready", got, err)
			}
		})
	}
}

// Naming the anchor's own host must not become "skip verification": a server whose
// certificate carries the same names but is not the trusted one stays refused.
func TestCheckRefusesACertificateThatIsNotTheTrustAnchor(t *testing.T) {
	trusted := operatorLeaf(t, "olivares.example.test", net.ParseIP("192.0.2.17"))
	impostor := operatorLeaf(t, "olivares.example.test", net.ParseIP("192.0.2.17"))
	got, err := Check(t.Context(), Config{Origin: serveOperatorLeaf(t, impostor), CACert: anchorFile(t, trusted), Timeout: time.Second})
	if err == nil || got.Outcome != Unmeasurable {
		t.Fatalf("a certificate other than the anchor: result=%+v err=%v, want unmeasurable with an error", got, err)
	}
}

// An organization signs its server certificate with its own CA, and the chain the
// engine publishes and serves starts with that certificate, not with a self-signed one.
func TestCheckTrustsACASignedOperatorCertificate(t *testing.T) {
	cert := operatorLeaf(t, "olivares.example.test", net.ParseIP("192.0.2.17"), operatorCA(t))
	got, err := Check(t.Context(), Config{Origin: serveOperatorLeaf(t, cert), CACert: anchorFile(t, cert), Timeout: time.Second})
	if err != nil || got.Outcome != Ready {
		t.Fatalf("CA-signed certificate as the trust anchor: result=%+v err=%v, want ready", got, err)
	}
}
