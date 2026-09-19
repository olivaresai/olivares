// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests pin where doctor looks: `quickstart` says "Confirm this host with
// olivares doctor", and before this change doctor could not find what quickstart
// created.

// TestDoctorHonoursTheDataDirectoryEnvironment pins the first half.
//
// MEASURED 2026-09-18: `quickstart --data-dir D` started an engine in D and
// doctor looked in $HOME/.local/share/olivares, reporting four required checks
// failed or unknown. defaultDataDir() (boot.go) documents OLIVARES_DATA_DIR as
// step 1 of the binary's own precedence; doctor did not implement step 1.
func TestDoctorHonoursTheDataDirectoryEnvironment(t *testing.T) {
	_, deps := doctorFixture(t)
	declared := t.TempDir()
	home := t.TempDir()
	deps.getenv = func(k string) string {
		switch k {
		case "OLIVARES_DATA_DIR":
			return declared
		}
		return ""
	}
	deps.homeDir = func() (string, error) { return home, nil }

	o := &doctorOptions{mode: "user", init: "systemd", timeout: time.Second, server: "https://127.0.0.1:8443"}
	report, _, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if report.Paths.DataDir != declared {
		t.Fatalf("data dir = %q, want the declared %q", report.Paths.DataDir, declared)
	}
	if report.Paths.Manifest != filepath.Join(declared, "install-manifest.json") {
		t.Fatalf("the manifest is looked for beside the wrong directory: %q", report.Paths.Manifest)
	}

	// An EXPLICIT --data-dir still wins over the environment: the flag is the
	// operator saying it now, and the variable is the shell saying it earlier.
	explicit := t.TempDir()
	o2 := &doctorOptions{mode: "user", init: "systemd", dataDir: explicit, timeout: time.Second, server: "https://127.0.0.1:8443"}
	report2, _, err := runDoctor(context.Background(), o2, deps)
	if err != nil {
		t.Fatal(err)
	}
	if report2.Paths.DataDir != explicit {
		t.Fatalf("--data-dir must win over OLIVARES_DATA_DIR: got %q", report2.Paths.DataDir)
	}
}

// TestDoctorProbesTheOriginTheEngineRecorded pins the second half.
//
// MEASURED 2026-09-18: quickstart wrote its bind into <data-dir>/console.json —
// `olivares first-boot` reads that same file and printed the right address — and
// doctor probed the compiled-in :8443, reporting livez, readyz and store as
// "start the service" against a service that was running.
func TestDoctorProbesTheOriginTheEngineRecorded(t *testing.T) {
	_, deps := doctorFixture(t)
	data := t.TempDir()
	writeConsoleFixture(t, data, consoleState{
		Version: consoleStateVersion, BootedAt: time.Now().UTC(),
		Browse: "https://olivares.example.com", Declared: true,
		Listen: "127.0.0.1:18443", GRPCListen: "127.0.0.1:18444",
	})
	var probed []string
	deps.httpGet = func(_ context.Context, url, _ string, _ time.Duration) (int, []byte, error) {
		probed = append(probed, url)
		return 200, []byte(`{"status":"operational"}`), nil
	}

	o := &doctorOptions{mode: "user", init: "systemd", dataDir: data, timeout: time.Second,
		server: "https://127.0.0.1:8443"}
	if _, _, err := runDoctor(context.Background(), o, deps); err != nil {
		t.Fatal(err)
	}
	if len(probed) == 0 {
		t.Fatal("nothing was probed; the assertion below would prove nothing")
	}
	for _, u := range probed {
		if got := u[:len("https://127.0.0.1:18443")]; got != "https://127.0.0.1:18443" {
			t.Fatalf("probed %q, want the recorded bind; the DECLARED browse URL is a proxy this host may not resolve", u)
		}
	}

	// An explicit --server is the operator's decision and is never substituted.
	probed = nil
	o2 := &doctorOptions{mode: "user", init: "systemd", dataDir: data, timeout: time.Second,
		server: "https://127.0.0.1:9999", serverExplicit: true}
	if _, _, err := runDoctor(context.Background(), o2, deps); err != nil {
		t.Fatal(err)
	}
	for _, u := range probed {
		if got := u[:len("https://127.0.0.1:9999")]; got != "https://127.0.0.1:9999" {
			t.Fatalf("--server was overridden by the recorded state: %q", u)
		}
	}
}

// TestDoctorOriginFromConsoleState pins the mapping itself, including the cases
// a live walk on one host cannot produce.
func TestDoctorOriginFromConsoleState(t *testing.T) {
	cases := []struct {
		name  string
		state consoleState
		want  string
	}{
		{"loopback bind", consoleState{Listen: "127.0.0.1:18443"}, "https://127.0.0.1:18443"},
		// A wildcard means "every interface"; the one this host can always reach
		// is loopback, and doctor's probes are local.
		{"wildcard port only", consoleState{Listen: ":8443"}, "https://127.0.0.1:8443"},
		{"wildcard v4", consoleState{Listen: "0.0.0.0:8443"}, "https://127.0.0.1:8443"},
		// SplitHostPort strips the brackets, so "::" is what reaches the mapping.
		{"wildcard v6", consoleState{Listen: "[::]:8443"}, "https://127.0.0.1:8443"},
		{"plain http engine", consoleState{Listen: "127.0.0.1:8080", Insecure: true}, "http://127.0.0.1:8080"},
		// This origin skips the --server check, so a bind it cannot spell as a
		// plain host:port is refused here instead of reaching a URL parser.
		{"control character in the host", consoleState{Listen: "127.0.0.1\n:8443"}, ""},
		{"a separator smuggled into the host", consoleState{Listen: "evil.example/path:8443"}, ""},
		{"credentials smuggled into the host", consoleState{Listen: "user@evil.example:8443"}, ""},
		{"control character in the port", consoleState{Listen: "127.0.0.1:84\t43"}, ""},
		{"a named bind is kept", consoleState{Listen: "olivares.internal:8443"}, "https://olivares.internal:8443"},
		// "" means "nothing usable was recorded"; the caller keeps its own
		// default rather than inventing an origin.
		{"nothing recorded", consoleState{}, ""},
		{"not a host:port", consoleState{Listen: "8443"}, ""},
		{"no port", consoleState{Listen: "127.0.0.1:"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := doctorOriginFromConsoleState(tc.state); got != tc.want {
				t.Fatalf("doctorOriginFromConsoleState = %q, want %q", got, tc.want)
			}
		})
	}
}

func writeConsoleFixture(t *testing.T, dataDir string, state consoleState) {
	t.Helper()
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, consoleStateFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorAcceptsTheInsecureEngineItRecorded pins the ORDER of two rules that
// were in each other's way.
//
// `--server` is https-only, and that rule is about what the OPERATOR may ask
// for. An engine started with --insecure records a plain-HTTP bind, so
// substituting the recorded origin BEFORE that check ended a legitimate insecure
// first hour in "--server must be an https origin" — a usage error naming a flag
// nobody passed. The substitution happens after the check; the origin builder is
// what keeps it safe.
func TestDoctorAcceptsTheInsecureEngineItRecorded(t *testing.T) {
	_, deps := doctorFixture(t)
	data := t.TempDir()
	writeConsoleFixture(t, data, consoleState{
		Version: consoleStateVersion, BootedAt: time.Now().UTC(),
		Browse: "http://127.0.0.1:18080", Listen: "127.0.0.1:18080", Insecure: true,
	})
	var probed []string
	deps.httpGet = func(_ context.Context, url, _ string, _ time.Duration) (int, []byte, error) {
		probed = append(probed, url)
		return 200, []byte(`{"status":"operational"}`), nil
	}

	o := &doctorOptions{mode: "user", init: "systemd", dataDir: data, timeout: time.Second,
		server: "https://127.0.0.1:8443"}
	if _, _, err := runDoctor(context.Background(), o, deps); err != nil {
		t.Fatalf("an insecure engine is a first hour, not a usage error: %v", err)
	}
	if len(probed) == 0 {
		t.Fatal("nothing was probed; the assertion below would prove nothing")
	}
	for _, u := range probed {
		if !strings.HasPrefix(u, "http://127.0.0.1:18080") {
			t.Fatalf("probed %q, want the recorded plain-HTTP bind", u)
		}
	}
}

// TestDoctorKeepsItsDefaultWhenTheRecordIsUnusable is the other side: a console
// record doctor cannot turn into an origin must leave the default standing, not
// blank the server or smuggle a bad one past the check above.
func TestDoctorKeepsItsDefaultWhenTheRecordIsUnusable(t *testing.T) {
	_, deps := doctorFixture(t)
	data := t.TempDir()
	writeConsoleFixture(t, data, consoleState{
		Version: consoleStateVersion, BootedAt: time.Now().UTC(), Listen: "user@evil.example/x:8443",
	})
	var probed []string
	deps.httpGet = func(_ context.Context, url, _ string, _ time.Duration) (int, []byte, error) {
		probed = append(probed, url)
		return 200, []byte(`{"status":"operational"}`), nil
	}

	o := &doctorOptions{mode: "user", init: "systemd", dataDir: data, timeout: time.Second,
		server: "https://127.0.0.1:8443"}
	if _, _, err := runDoctor(context.Background(), o, deps); err != nil {
		t.Fatal(err)
	}
	if len(probed) == 0 {
		t.Fatal("nothing was probed; the assertion below would prove nothing")
	}
	for _, u := range probed {
		if !strings.HasPrefix(u, "https://127.0.0.1:8443") {
			t.Fatalf("probed %q, want doctor's own default to stand", u)
		}
	}
}
