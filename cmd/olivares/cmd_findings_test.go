// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestFindingsExportCLIWritesSARIFToStdout(t *testing.T) {
	prepareFindingsCLITest(t)
	const sarif = `{"version":"2.1.0","runs":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != findingsExportPath {
			t.Errorf("request = %s %s, want GET %s", r.Method, r.URL.Path, findingsExportPath)
		}
		if got := r.URL.Query().Get("format"); got != "sarif" {
			t.Errorf("format = %q, want sarif", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Olivares-Tenant"); got != "tenant-a" {
			t.Errorf("X-Olivares-Tenant = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/sarif+json" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/sarif+json")
		_, _ = io.WriteString(w, sarif)
	}))
	t.Cleanup(srv.Close)

	out, stderr, err := execRoot(t,
		"findings", "export",
		"--server", srv.URL,
		"--token", "secret-token",
		"--tenant", "tenant-a",
		"--format", "sarif",
	)
	if err != nil {
		t.Fatalf("findings export: %v (stderr %q)", err, stderr)
	}
	if out != sarif {
		t.Fatalf("stdout = %q, want exact SARIF %q", out, sarif)
	}
	// stderr carries the argv-exposure warning and NOTHING else: this invocation
	// passes the bearer as a flag, and since 2026-08-16 that says so out loud
	// (cmd_auth.go, warnTokenInArgv). The assertion stays an exact match — the
	// point of this test is that the clean export path adds no other noise, so a
	// truncation warning or a stray print still fails it.
	if stderr != cliTokenArgvWarning+"\n" {
		t.Fatalf("stderr = %q, want exactly the argv warning", stderr)
	}
}

func TestFindingsExportCLIWritesFileAndWarnsWhenTruncated(t *testing.T) {
	prepareFindingsCLITest(t)
	const sarif = `{"version":"2.1.0","runs":[{"results":[{}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Olivares-Truncated", "true")
		_, _ = io.WriteString(w, sarif)
	}))
	t.Cleanup(srv.Close)
	outPath := filepath.Join(t.TempDir(), "findings.sarif")

	out, stderr, err := execRoot(t,
		"findings", "export",
		"--server", srv.URL,
		"--token", "token",
		"--tenant", "tenant-a",
		"--out", outPath,
	)
	if err != nil {
		t.Fatalf("findings export --out: %v (stderr %q)", err, stderr)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty with --out", out)
	}
	if !strings.Contains(stderr, "truncated at 25000 findings") {
		t.Fatalf("stderr does not surface truncation: %q", stderr)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(raw) != sarif {
		t.Fatalf("file = %q, want exact SARIF %q", raw, sarif)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output mode = %04o, want 0600", got)
	}
}

func TestFindingsExportCLIRejectsUnknownFormatBeforeRequest(t *testing.T) {
	prepareFindingsCLITest(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(srv.Close)

	_, _, err := execRoot(t,
		"findings", "export",
		"--server", srv.URL,
		"--token", "token",
		"--tenant", "tenant-a",
		"--format", "csv",
	)
	if err == nil || !strings.Contains(err.Error(), `valid values: sarif`) {
		t.Fatalf("error = %v, want valid SARIF formats", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want validation before HTTP", got)
	}
}

// The exit-code contract (exitcode.go: scripts and CI pipelines branch on these
// values) must survive the error path — a redactor that rebuilds a plain error
// collapses every HTTP failure to the generic code 1, which is what the old
// redactCLIError did and what redactCoded exists to prevent.
func TestFindingsExportCLIPropagatesHTTPExitCodes(t *testing.T) {
	prepareFindingsCLITest(t)
	for _, tc := range []struct {
		status int
		want   int
	}{
		{http.StatusUnauthorized, exitcode.Auth},
		{http.StatusNotFound, exitcode.NotFound},
		{http.StatusInternalServerError, exitcode.Server},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "export rejected", tc.status)
		}))
		_, _, err := execRoot(t,
			"findings", "export",
			"--server", srv.URL,
			"--token", "secret-token",
			"--tenant", "tenant-a",
		)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: want an error", tc.status)
		}
		if got := exitcode.From(err); got != tc.want {
			t.Fatalf("status %d: exit code = %d, want %d (err %v)", tc.status, got, tc.want, err)
		}
	}
}

func prepareFindingsCLITest(t *testing.T) {
	t.Helper()
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
}
