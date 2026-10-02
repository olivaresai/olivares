// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestReadyzCommandExitContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		wantCode   int
		wantPhrase string
	}{
		{name: "ready", status: http.StatusOK, wantCode: exitcode.OK, wantPhrase: "ready:"},
		{name: "not ready", status: http.StatusServiceUnavailable, wantCode: exitcode.Err, wantPhrase: "not ready:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			cmd := newReadyzCmd()
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{"--server", server.URL, "--timeout", "1s"})
			err := cmd.Execute()
			code := exitcode.OK
			if err != nil {
				code = exitcode.From(err)
			}
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v)", code, tc.wantCode, err)
			}
			if output := stdout.String() + stderr.String(); !strings.Contains(output, tc.wantPhrase) {
				t.Fatalf("output = %q, want %q", output, tc.wantPhrase)
			}
		})
	}
}

func TestReadyzCommandCannotLookIsExitTwo(t *testing.T) {
	var stderr bytes.Buffer
	cmd := newReadyzCmd()
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--server", "https://192.0.2.1:8443"})
	err := cmd.Execute()
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want 2 (err=%v)", got, err)
	}
	if got := stderr.String(); !strings.Contains(got, "cannot inspect readiness") || strings.Contains(got, "Error:") {
		t.Fatalf("stderr = %q, want one classified, silent-wrapper verdict", got)
	}
}

// TestReadyzCommandAppendsTheLocalDiagnosis is the command-path half of the
// contract the installer depends on: the verdict line and the exit code are
// exactly what they were, the fixed sentences follow them on stderr, and a
// body that is not recognized adds nothing at all.
func TestReadyzCommandAppendsTheLocalDiagnosis(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-do-not-print"
	body := func(code string) string {
		return `{"status":"setup_blocked","code":"` + code + `","detail":"PGPASSWORD=` + secret + `"}`
	}
	tests := []struct {
		name     string
		body     string
		wantHint bool
	}{
		{name: "recognized first-boot state", body: body("cross_tenant_admin_pool_not_configured"), wantHint: true},
		{name: "state this build does not know", body: body("brand_new_code")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			cmd := newReadyzCmd()
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{"--server", server.URL, "--timeout", "5s"})
			err := cmd.Execute()
			if got := exitcode.From(err); got != exitcode.Err {
				t.Fatalf("exit code = %d, want 1 (err=%v)", got, err)
			}
			printed := stdout.String() + stderr.String()
			if !strings.HasPrefix(stderr.String(), "not ready: ") {
				t.Fatalf("stderr = %q, want the unchanged verdict line first", stderr.String())
			}
			if strings.Contains(printed, secret) {
				t.Fatalf("output carried the response body: %q", printed)
			}
			hasHint := strings.Contains(printed, "diagnosis: ") && strings.Contains(printed, "remedy: ")
			if hasHint != tc.wantHint {
				t.Fatalf("output = %q, want diagnosis present=%v", printed, tc.wantHint)
			}
		})
	}
}

// TestReadyzFollowsTheActiveContextOnThisHost: with no --server, readyz probes the
// engine the active context names when it runs on this host (N1 RU-04: an engine
// on another port than 8443 read as down), with localhost read as 127.0.0.1.
func TestReadyzFollowsTheActiveContextOnThisHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(cliConfigOverrideEnv, config)
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	port := server.URL[strings.LastIndex(server.URL, ":")+1:]
	if err := writeCLIConfig(config, cliConfig{CurrentContext: "local", Contexts: []cliContext{{
		Name: "local", Server: "http://localhost:" + port, Token: "t",
	}}}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cmd := newReadyzCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--timeout", "1s"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("readyz with a local context: %v", err)
	}
	if !strings.Contains(stdout.String(), "ready: http://127.0.0.1:"+port) {
		t.Fatalf("stdout = %q, want the context's engine", stdout.String())
	}
}
