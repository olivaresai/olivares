// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeneratedTLSCertVerifiesEveryConsoleLANAddress(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCert(cert, key); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	bundle, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	if !roots.AppendCertsFromPEM(bundle) {
		t.Fatal("no product trust certificate")
	}
	names := []string{"localhost", "127.0.0.1", "::1"}
	if host, err := os.Hostname(); err == nil && host != "" {
		names = append(names, host)
	}
	// Independent OS address oracle: the same exclusions as the console contract.
	names = append(names, testLANAddresses(t)...)
	for _, name := range names {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("product CA does not verify console address %q: %v", name, err)
		}
	}
}

func TestLocalTLSRenewalKeepsCAAndClientTrust(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	dns := []string{"localhost", "server.example"}
	first := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1"), net.ParseIP("192.168.1.20")}
	if _, _, err := ensureTLSCert(cert, key, dns, first); err != nil {
		t.Fatal(err)
	}
	trusted := readTLSFixture(t, cert)
	caBefore, caKeyBefore, keyBefore := readTLSFixture(t, cert+".ca"), readTLSFixture(t, key+".ca"), readTLSFixture(t, key)
	pin, err := SPKIPin(cert)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a DHCP address change and a hostname change at the OS boundary.
	second := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1"), net.ParseIP("192.168.1.21")}
	dns = []string{"localhost", "renamed.example"}
	changed, _, err := ensureTLSCert(cert, key, dns, second)
	if err != nil || !changed {
		t.Fatalf("renewal: changed=%v, %v", changed, err)
	}
	for path, before := range map[string][]byte{cert + ".ca": caBefore, key + ".ca": caKeyBefore, key: keyBefore} {
		if !bytes.Equal(before, readTLSFixture(t, path)) {
			t.Errorf("renewal changed trust/key file %s", path)
		}
	}
	pinAfter, err := SPKIPin(cert)
	if err != nil || pinAfter != pin {
		t.Fatalf("SPKI pin changed: %s -> %s, %v", pin, pinAfter, err)
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("192.168.1.20"); err == nil {
		t.Error("removed address still in renewed certificate")
	}
	if err := leaf.VerifyHostname("server.example"); err == nil {
		t.Error("old hostname still in renewed certificate")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(trusted) // Client retains its original trust bundle.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "192.168.1.21", MinVersion: tls.VersionTLS12}}}
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client's unchanged product CA refuses new LAN identity: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.Status)
	}
	changed, _, err = ensureTLSCert(cert, key, dns, second)
	if err != nil || changed {
		t.Fatalf("unchanged host re-minted certificate: %v, %v", changed, err)
	}
	for _, path := range []string{key, key + ".ca"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private key custody: %v, %v", info, err)
		}
	}
}

func TestLegacyLocalTLSUpgradeKeepsPublishedClientTrust(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeOldTLSFixture(t, cert, key, "olivares", "Olivares AI", 825*24*time.Hour)
	before, keyBefore := readTLSFixture(t, cert), readTLSFixture(t, key)
	clientCA := filepath.Join(dir, "published-client-ca.crt")
	if err := os.WriteFile(clientCA, before, 0o644); err != nil {
		t.Fatal(err)
	}
	pin, err := SPKIPin(cert)
	if err != nil {
		t.Fatal(err)
	}
	changed, _, err := EnsureTLSCert(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	clientTLS, err := ClientTLSConfig(clientCA, "", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("26.10.0 client's unchanged published CA refuses upgraded server: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.Status)
	}
	after, err := SPKIPin(cert)
	if err != nil || pin != after {
		t.Fatalf("legacy pin changed: %s -> %s, %v", pin, after, err)
	}
	if changed || !bytes.Equal(before, readTLSFixture(t, cert)) || !bytes.Equal(keyBefore, readTLSFixture(t, key)) {
		t.Fatal("upgrade replaced the published certificate/key pair")
	}
	if !bytes.Equal(before, readTLSFixture(t, clientCA)) {
		t.Fatal("client trust file was changed")
	}
	for _, path := range []string{cert + ".ca", key + ".ca"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("upgrade created a replacement trust anchor: %s: %v", path, err)
		}
	}
}

func TestOperatorTLSCertificateIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeOldTLSFixture(t, cert, key, "operator.example", "Operator")
	before, keyBefore := readTLSFixture(t, cert), readTLSFixture(t, key)
	changed, _, err := ensureTLSCert(cert, key, []string{"localhost"}, []net.IP{net.ParseIP("192.168.1.20")})
	if err != nil || changed {
		t.Fatalf("operator pair was renewed: %v, %v", changed, err)
	}
	if !bytes.Equal(before, readTLSFixture(t, cert)) || !bytes.Equal(keyBefore, readTLSFixture(t, key)) {
		t.Fatal("operator pair was changed")
	}
	if _, err := os.Stat(cert + ".ca"); !os.IsNotExist(err) {
		t.Fatalf("created a CA for an operator certificate: %v", err)
	}
}

func TestBrandedOperatorTLSCertificateIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	// Same branding and loopback names, but the operator's own validity period.
	writeOldTLSFixture(t, cert, key, "olivares", "Olivares AI")
	before := readTLSFixture(t, cert)
	changed, _, err := EnsureTLSCert(cert, key)
	if err != nil || changed || !bytes.Equal(before, readTLSFixture(t, cert)) {
		t.Fatalf("branded operator certificate replaced: %v, %v", changed, err)
	}
}

func TestExplicitTLSCertificatePreservesHistoricalGeneratedPair(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeOldTLSFixture(t, cert, key, "olivares", "Olivares AI", 825*24*time.Hour)
	before := readTLSFixture(t, cert)
	changed, _, err := EnsureTLSCert(cert, key, true)
	if err != nil || changed || !bytes.Equal(before, readTLSFixture(t, cert)) {
		t.Fatalf("explicit pair replaced: %v, %v", changed, err)
	}
}

func TestLocalTLSRenewalRefusesToReplaceMissingCAKey(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := ensureTLSCert(cert, key, []string{"localhost"}, nil); err != nil {
		t.Fatal(err)
	}
	before := readTLSFixture(t, cert+".ca")
	if err := os.Remove(key + ".ca"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureTLSCert(cert, key, []string{"localhost"}, []net.IP{net.ParseIP("192.168.1.20")}); err == nil {
		t.Fatal("missing CA key silently replaced")
	}
	if !bytes.Equal(before, readTLSFixture(t, cert+".ca")) {
		t.Fatal("changed established CA")
	}
}

func TestOperatorTLSReplacementIgnoresUnusedLocalCA(t *testing.T) {
	for _, state := range []string{"missing key", "invalid certificate"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
			if _, _, err := EnsureTLSCert(cert, key); err != nil {
				t.Fatal(err)
			}
			writeOldTLSFixture(t, cert, key, "operator.example", "Operator")
			before, keyBefore := readTLSFixture(t, cert), readTLSFixture(t, key)
			if state == "missing key" {
				if err := os.Remove(key + ".ca"); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(cert+".ca", []byte("obsolete CA"), 0o644); err != nil {
				t.Fatal(err)
			}
			changed, _, err := EnsureTLSCert(cert, key)
			if err != nil || changed {
				t.Fatalf("unused CA blocked operator replacement: %v, %v", changed, err)
			}
			if !bytes.Equal(before, readTLSFixture(t, cert)) || !bytes.Equal(keyBefore, readTLSFixture(t, key)) {
				t.Fatal("operator certificate was changed")
			}
		})
	}
}

func TestLocalTLSRenewalRefusesMissingCACertificate(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCert(cert, key); err != nil {
		t.Fatal(err)
	}
	before := readTLSFixture(t, cert)
	if err := os.Remove(cert + ".ca"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureTLSCert(cert, key, []string{"localhost", "renamed.example"}, []net.IP{net.ParseIP("192.168.1.20")}); err == nil {
		t.Fatal("missing CA silently disabled managed renewal")
	}
	if !bytes.Equal(before, readTLSFixture(t, cert)) {
		t.Fatal("changed certificate without the established CA")
	}
}

func TestLocalTLSRenewalRefusesAnotherInstallationsCA(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	otherCert, otherKey := filepath.Join(other, "tls.crt"), filepath.Join(other, "tls.key")
	for _, pair := range [][2]string{{cert, key}, {otherCert, otherKey}} {
		if _, _, err := EnsureTLSCert(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(cert+".ca", readTLSFixture(t, otherCert+".ca"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key+".ca", readTLSFixture(t, otherKey+".ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, path := range []string{cert, key, cert + ".ca", key + ".ca"} {
		before[path] = readTLSFixture(t, path)
	}
	if _, _, err := ensureTLSCert(cert, key, []string{"localhost", "renamed.example"}, []net.IP{net.ParseIP("192.168.1.20")}); err == nil {
		t.Fatal("renewal silently adopted another installation's CA")
	}
	for path, data := range before {
		if !bytes.Equal(data, readTLSFixture(t, path)) {
			t.Errorf("CA mismatch changed %s", path)
		}
	}
}

func readTLSFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeOldTLSFixture(t *testing.T, certPath, keyPath, commonName, organization string, validity ...time.Duration) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now, duration := time.Now(), 24*time.Hour
	if len(validity) > 0 {
		duration = validity[0]
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: commonName, Organization: []string{organization}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(duration), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testLANAddresses(t *testing.T) []string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			ip, ok := address.(*net.IPNet)
			if !ok || ip.IP.IsLoopback() || ip.IP.IsUnspecified() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsMulticast() {
				continue
			}
			names = append(names, ip.IP.String())
		}
	}
	return names
}

// An explicitly supplied operator certificate remains operator-owned even if
// its configured private-key path is absent. Refuse; never issue local material
// over that certificate or create a replacement trust anchor beside it.
func TestSR2ExplicitOperatorCertificateSurvivesMissingKey(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "operator.crt"), filepath.Join(dir, "operator.key")
	writeOldTLSFixture(t, cert, key, "operator.example", "Operator")
	before, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	created, _, err := EnsureTLSCert(cert, key, true)
	if err == nil || created {
		t.Errorf("missing explicit operator key must refuse without local issuance: created=%v err=%v", created, err)
	}
	after, readErr := os.ReadFile(cert)
	if readErr != nil || !bytes.Equal(before, after) {
		t.Errorf("explicit operator certificate overwritten: read=%v", readErr)
	}
	for _, path := range []string{key, cert + ".ca", key + ".ca"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("refused operator startup created or obscured material at %s: %v", filepath.Base(path), err)
		}
	}
}

func TestExplicitOperatorTLSIncompletePairPreservesFilesAndNamesMissingPath(t *testing.T) {
	for _, missing := range []string{"key", "certificate", "both"} {
		t.Run(missing, func(t *testing.T) {
			dir := t.TempDir()
			cert, key := filepath.Join(dir, "operator.crt"), filepath.Join(dir, "operator.key")
			writeOldTLSFixture(t, cert, key, "operator.example", "Operator")
			for _, path := range []string{cert + ".ca", key + ".ca"} {
				if err := os.WriteFile(path, []byte("pre-existing operator material"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			missingPath := cert
			if missing == "key" || missing == "both" {
				missingPath = key
				if err := os.Remove(key); err != nil {
					t.Fatal(err)
				}
			}
			if missing == "certificate" || missing == "both" {
				if err := os.Remove(cert); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string][]byte{}
			for _, path := range []string{cert, key, cert + ".ca", key + ".ca"} {
				data, err := os.ReadFile(path)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				before[path] = data
			}

			created, fingerprint, err := EnsureTLSCert(cert, key, true)
			if created || fingerprint != "" || !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete pair must refuse: created=%v fingerprint=%q err=%v", created, fingerprint, err)
			}
			if !strings.Contains(err.Error(), missingPath) || strings.ContainsAny(err.Error(), "\r\n") {
				t.Errorf("error must name the missing file on one line: %v", err)
			}
			for path, data := range before {
				after, readErr := os.ReadFile(path)
				if data == nil {
					if !os.IsNotExist(readErr) {
						t.Errorf("refusal created missing file %s: %v", filepath.Base(path), readErr)
					}
				} else if readErr != nil || !bytes.Equal(data, after) {
					t.Errorf("refusal changed existing file %s: %v", filepath.Base(path), readErr)
				}
			}
		})
	}
}
