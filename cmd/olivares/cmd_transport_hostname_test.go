// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestAnAddressTheCertificateDoesNotCoverIsOneSentence is J7 on refresh 05: from the
// engine's LAN address, `olivares login` printed Go's "tls: failed to verify certificate:
// x509: certificate is valid for 127.0.0.1, ::1, not 172.21.0.2". The engine's self-signed
// certificate covers only loopback. The CLI now says which names it covers and the next
// step; trusting the certificate or pinning it cannot fix a name it does not cover.
func TestAnAddressTheCertificateDoesNotCoverIsOneSentence(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the request reached the engine: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "tls.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("correct horse battery staple\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliConfigOverrideEnv, filepath.Join(dir, "config.yaml"))
	// The test certificate covers example.com, *.example.com, 127.0.0.1 and ::1; "localhost" is not one of them.
	server := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	_, _, err := execRoot(t, "login", "--server", server, "--ca-cert", ca, "--email", "ana@example.com", "--password-file", pw)
	if err == nil || exitcode.From(err) != exitcode.Server {
		t.Fatalf("err = %v (exit %d), want a transport failure (exit 6)", err, exitcode.From(err))
	}
	first := strings.SplitN(err.Error(), "\n", 2)[0]
	want := "The engine's certificate covers example.com, *.example.com, 127.0.0.1, ::1, not localhost. Use an address it covers, or start " +
		"the engine with a certificate for localhost: olivares serve --tls-cert <file> --tls-key <file>"
	if first != want {
		t.Fatalf("first line = %q\nwant         %q", first, want)
	}
}
