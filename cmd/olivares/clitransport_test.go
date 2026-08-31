// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLITransportTLSVerificationModes(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer transport-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Olivares-Tenant"); got != "transport-tenant" {
			t.Errorf("X-Olivares-Tenant = %q", got)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	cert := srv.Certificate()
	caPath := filepath.Join(t.TempDir(), "server-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	spki := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	correctPin := base64.StdEncoding.EncodeToString(spki[:])
	wrongPin := base64.StdEncoding.EncodeToString(make([]byte, sha256.Size))

	tests := []struct {
		name        string
		resolved    cliResolvedConfig
		insecure    bool
		wantOK      bool
		wantErrText string
		wantWarning bool
	}{
		{name: "default trust rejects ephemeral CA", resolved: cliResolvedConfig{Server: srv.URL, Token: "transport-token", Tenant: "transport-tenant"}},
		{name: "custom CA", resolved: cliResolvedConfig{Server: srv.URL, Token: "transport-token", Tenant: "transport-tenant", CACert: caPath}, wantOK: true},
		{name: "matching SPKI pin", resolved: cliResolvedConfig{Server: srv.URL, Token: "transport-token", Tenant: "transport-tenant", PinSHA256: []string{correctPin}}, wantOK: true},
		{name: "mismatched SPKI pin", resolved: cliResolvedConfig{Server: srv.URL, Token: "transport-token", Tenant: "transport-tenant", PinSHA256: []string{wrongPin}}, wantErrText: "SPKI pin mismatch"},
		{name: "explicit insecure", resolved: cliResolvedConfig{Server: srv.URL, Token: "transport-token", Tenant: "transport-tenant"}, insecure: true, wantOK: true, wantWarning: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			client, headers, err := cliTransport(cliTransportOptions{
				Resolved: tc.resolved,
				Insecure: tc.insecure,
				Timeout:  2 * time.Second,
				Stderr:   &stderr,
			})
			if err != nil {
				t.Fatalf("construct transport: %v", err)
			}
			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header = headers.Clone()
			resp, doErr := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tc.wantOK && doErr != nil {
				t.Fatalf("request: %v", doErr)
			}
			if !tc.wantOK && doErr == nil {
				t.Fatal("request unexpectedly trusted the server")
			}
			if tc.wantErrText != "" && !strings.Contains(doErr.Error(), tc.wantErrText) {
				t.Fatalf("error = %q, want %q", doErr, tc.wantErrText)
			}
			warning := stderr.String()
			if tc.wantWarning && !strings.Contains(warning, "TLS verification disabled — never use against production") {
				t.Fatalf("warning missing: %q", warning)
			}
			if !tc.wantWarning && warning != "" {
				t.Fatalf("unexpected warning: %q", warning)
			}
		})
	}
}

func TestCLITransportAcceptsOpenSSHPinNotation(t *testing.T) {
	raw := make([]byte, sha256.Size)
	spec := "SHA256:" + base64.RawStdEncoding.EncodeToString(raw)
	decoded, err := decodeSPKIPin(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("decoded pin = %x, want %x", decoded, raw)
	}
}
