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
	// The engine makes its data directory private; a temp directory follows the umask.
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
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

// A cloned repository (or an unpacked archive) with an olivares-data under the working
// directory chose the engine the anonymous status (`olivares` alone, and the overview
// `olivares status` prints) probed, trusted through the certificate planted next to it.
func TestAnonymousStatusIgnoresADataDirectoryUnderTheWorkingDirectory(t *testing.T) {
	asked := make(chan string, 8)
	planted := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		asked <- r.URL.Path
	}))
	t.Cleanup(planted.Close)
	work := t.TempDir()
	dir := filepath.Join(work, legacyDataDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install-id"), []byte("planted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: planted.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, tlsCertFile), crt, 0o644); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(planted.URL)
	if err := writeConsoleState(dir, consoleState{Version: consoleStateVersion, Listen: u.Host}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	for _, name := range []string{"OLIVARES_DATA_DIR", "OLIVARES_SERVER_URL", "OLIVARES_TOKEN", "OLIVARES_TENANT", cliConfigOverrideEnv} {
		t.Setenv(name, "")
	}
	t.Chdir(work)
	_, _, _ = execRoot(t)
	if len(asked) != 0 {
		t.Fatalf("a data directory a cloned repository put under the working directory chose the engine: it was asked %s", <-asked)
	}
}
