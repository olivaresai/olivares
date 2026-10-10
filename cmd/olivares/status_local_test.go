// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// HU-R36 (26.10.2 binary 1): on the engine's host, before signing in, `olivares status`
// said "Not signed in" although /status is public and `olivares login` found the same
// engine. It reads the engine this host recorded, and sends no credential.
func TestStatusOnTheEnginesHostNeedsNoSignIn(t *testing.T) {
	paths := make(chan string, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		if r.Header.Get("Authorization") != "" {
			t.Error("status sent a credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"operational","components":[],"timestamp":"2026-10-03T10:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	dataDir := t.TempDir()
	u, _ := url.Parse(srv.URL)
	if err := writeConsoleState(dataDir, consoleState{Version: 1, Listen: u.Host}); err != nil {
		t.Fatal(err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dataDir, "tls.crt"), crt, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_DATA_DIR", dataDir)
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	if _, stderr, err := execRoot(t, "status"); err != nil {
		t.Fatalf("status on the engine's host before sign-in: %v\n%s", err, stderr)
	}
	if got := <-paths; got != "/status" {
		t.Fatalf("status asked %q, want /status", got)
	}
}
