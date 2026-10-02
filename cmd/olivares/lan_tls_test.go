// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

func TestCLIProductCAVerifiesConsoleLANAddress(t *testing.T) {
	addresses := hostConsoleAddresses(":8443", "https")
	var host string
	for _, address := range addresses {
		if !address.IsLoopback() {
			host = address.Host
			break
		}
	}
	if host == "" {
		t.Skip("host has no non-loopback console address")
	}
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := secure.EnsureTLSCert(cert, key); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/login" {
			t.Errorf("wrong login request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.Listener.Close()
	server.Listener = listener
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	// Public product CA, as copied to a client on another machine; no pin or TLS bypass.
	ca := filepath.Join(dir, "client-ca.crt")
	public, err := os.ReadFile(cert + ".ca")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, public, 0o644); err != nil {
		t.Fatal(err)
	}
	client, _, err := cliTransport(cliTransportOptions{Resolved: cliResolvedConfig{Server: server.URL, CACert: ca}, CarriesSecret: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err := client.Post(server.URL+"/v1/auth/login", "application/json", nil)
	if err != nil {
		t.Fatalf("verified CLI login transport by LAN IP: %v", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status %s", response.Status)
	}
}
