// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The declared console host is a name no interface of the machine owns: inside a
// container the host address is only a published port, so the managed leaf has to
// take it from the operator. TEST-NET and .test names are never assigned to a host.
const (
	publicHostIPv4 = "192.0.2.17"
	publicHostIPv6 = "2001:db8::17"
	publicHostDNS  = "olivares.example.test"
)

func tlsLeaf(t *testing.T, cert, key string) *x509.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func TestManagedTLSCertCoversTheDeclaredPublicHost(t *testing.T) {
	for _, tc := range []struct{ name, declared, covered string }{
		{"IPv4 address", publicHostIPv4, publicHostIPv4},
		{"IPv6 address", publicHostIPv6, publicHostIPv6},
		{"DNS name", publicHostDNS, publicHostDNS},
		{"DNS name as written in a URL", "Olivares.Example.Test.", publicHostDNS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
			if _, _, err := EnsureTLSCertForHost(cert, key, tc.declared, false); err != nil {
				t.Fatal(err)
			}
			leaf := tlsLeaf(t, cert, key)
			if err := leaf.VerifyHostname(tc.covered); err != nil {
				t.Errorf("the managed certificate does not cover the declared host %q: %v", tc.covered, err)
			}
			// The names every install already had stay.
			for _, name := range []string{"localhost", "127.0.0.1", "::1"} {
				if err := leaf.VerifyHostname(name); err != nil {
					t.Errorf("declaring a public host dropped %q: %v", name, err)
				}
			}
		})
	}
}

func TestDeclaringOrChangingThePublicHostReissuesFromTheSameCA(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	// First boot, no declared host: the certificate an existing install already holds.
	if _, _, err := EnsureTLSCertForHost(cert, key, "", false); err != nil {
		t.Fatal(err)
	}
	if err := tlsLeaf(t, cert, key).VerifyHostname(publicHostIPv4); err == nil {
		t.Fatalf("%s is already covered with no declared host: the test proves nothing on this machine", publicHostIPv4)
	}
	trusted := readTLSFixture(t, cert) // the bundle a client was given at first boot
	caBefore, caKeyBefore, keyBefore := readTLSFixture(t, cert+".ca"), readTLSFixture(t, key+".ca"), readTLSFixture(t, key)
	pin, err := SPKIPin(cert)
	if err != nil {
		t.Fatal(err)
	}

	changed, _, err := EnsureTLSCertForHost(cert, key, publicHostIPv4, false)
	if err != nil || !changed {
		t.Fatalf("declaring the host: changed=%v, %v", changed, err)
	}
	for path, before := range map[string][]byte{cert + ".ca": caBefore, key + ".ca": caKeyBefore, key: keyBefore} {
		if !bytes.Equal(before, readTLSFixture(t, path)) {
			t.Errorf("declaring the host changed trust/key file %s", filepath.Base(path))
		}
	}
	if after, err := SPKIPin(cert); err != nil || after != pin {
		t.Fatalf("the --pin-sha256 value changed: %s -> %s, %v", pin, after, err)
	}

	// A client holding only the first-boot bundle reaches the engine at the new address.
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(trusted)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: publicHostIPv4, MinVersion: tls.VersionTLS12}}}
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("a client with the first-boot trust bundle refuses the declared address: %v", err)
	}
	response.Body.Close()

	if changed, _, err = EnsureTLSCertForHost(cert, key, publicHostIPv4, false); err != nil || changed {
		t.Fatalf("the same declared host re-issued the certificate: changed=%v, %v", changed, err)
	}

	// A changed value replaces the old name; clearing it returns to the default names.
	if changed, _, err = EnsureTLSCertForHost(cert, key, publicHostDNS, false); err != nil || !changed {
		t.Fatalf("changing the host: changed=%v, %v", changed, err)
	}
	leaf := tlsLeaf(t, cert, key)
	if leaf.VerifyHostname(publicHostDNS) != nil || leaf.VerifyHostname(publicHostIPv4) == nil {
		t.Errorf("a changed host must be covered and the old one dropped: DNSNames=%v IPAddresses=%v", leaf.DNSNames, leaf.IPAddresses)
	}
	if changed, _, err = EnsureTLSCertForHost(cert, key, "", false); err != nil || !changed {
		t.Fatalf("clearing the host: changed=%v, %v", changed, err)
	}
	if tlsLeaf(t, cert, key).VerifyHostname(publicHostDNS) == nil {
		t.Error("a cleared host is still covered")
	}
	if after, err := SPKIPin(cert); err != nil || after != pin {
		t.Fatalf("the --pin-sha256 value changed across reissues: %s -> %s, %v", pin, after, err)
	}
}

func TestPublicHostThatAddsNoNameLeavesTheCertificateAlone(t *testing.T) {
	// A wildcard bind is not an address anything can dial, and loopback is already named.
	for _, declared := range []string{"0.0.0.0", "::", "localhost", "LOCALHOST.", "127.0.0.1", "::1"} {
		t.Run(declared, func(t *testing.T) {
			dir := t.TempDir()
			cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
			if _, _, err := EnsureTLSCertForHost(cert, key, "", false); err != nil {
				t.Fatal(err)
			}
			before := readTLSFixture(t, cert)
			changed, _, err := EnsureTLSCertForHost(cert, key, declared, false)
			if err != nil || changed {
				t.Fatalf("%q must add no name: changed=%v, %v", declared, changed, err)
			}
			if !bytes.Equal(before, readTLSFixture(t, cert)) {
				t.Errorf("%q rewrote the certificate", declared)
			}
		})
	}
}

func TestOperatorTLSCertificateIgnoresThePublicHost(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "operator.crt"), filepath.Join(dir, "operator.key")
	writeOldTLSFixture(t, cert, key, "operator.example", "Operator")
	before := readTLSFixture(t, cert)
	created, _, err := EnsureTLSCertForHost(cert, key, publicHostIPv4, true)
	if err != nil || created {
		t.Fatalf("operator pair: created=%v, %v", created, err)
	}
	if !bytes.Equal(before, readTLSFixture(t, cert)) {
		t.Error("a declared public host rewrote an operator certificate")
	}
	for _, path := range []string{cert + ".ca", key + ".ca"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("a declared public host created local CA material beside an operator pair: %s", filepath.Base(path))
		}
	}
}

func TestEnsureTLSCertKeepsItsOwnNames(t *testing.T) {
	// The two-argument API is the declared-host API with no host.
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := EnsureTLSCert(cert, key); err != nil {
		t.Fatal(err)
	}
	if changed, _, err := EnsureTLSCertForHost(cert, key, "", false); err != nil || changed {
		t.Fatalf("EnsureTLSCert and EnsureTLSCertForHost(\"\") disagree on the names: changed=%v, %v", changed, err)
	}
}
