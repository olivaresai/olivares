// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestDoctorOnAClientHostChecksTheSignedInEngine: a host that only runs the CLI has no
// data directory, no unit and no local engine. `doctor` must judge the engine its saved
// sign-in names (reachable, sign-in accepted), not the local-install checks, and its
// next step must never be `olivares doctor` itself.
func TestDoctorOnAClientHostChecksTheSignedInEngine(t *testing.T) {
	for _, tc := range []struct {
		name       string
		whoami     int
		code       int
		overall    string
		nextPrefix string
	}{
		{"engine accepts the sign-in", http.StatusOK, exitcode.OK, "healthy", "olivares tool ls"},
		{"engine rejects the sign-in", http.StatusUnauthorized, exitcode.Err, "unhealthy", "olivares login --server "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/auth/whoami":
					w.WriteHeader(tc.whoami)
					_, _ = w.Write([]byte(`{"display_name":"Ada"}`))
				case "/v1/server-info":
					_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
				default:
					_, _ = w.Write([]byte("ok"))
				}
			}))
			defer engine.Close()

			cfg := filepath.Join(t.TempDir(), "config.yaml")
			body := "current-context: remote\ncontexts:\n- name: remote\n  server: " + engine.URL + "\n  token: not-a-real-token\n"
			if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OLIVARES_CLI_CONFIG", cfg)
			t.Setenv("OLIVARES_SERVER_URL", "")
			t.Setenv("OLIVARES_TOKEN", "")

			// The fixture's engine files are not on this host: the data directory is the
			// default one under an empty home, which does not exist.
			o, deps := doctorFixture(t)
			o.dataDir, o.config, o.unit = "", "", ""
			report, code, err := runDoctor(context.Background(), o, deps)
			if err != nil {
				t.Fatal(err)
			}
			if code != tc.code || report.Overall != tc.overall {
				t.Fatalf("doctor = code %d overall %q, want %d %q; checks=%+v", code, report.Overall, tc.code, tc.overall, report.Checks)
			}
			failed, open := 0, 0
			for _, c := range report.Checks {
				if c.Name == "data-directory" || c.Name == "livez" {
					t.Errorf("client host ran the local-install check %q", c.Name)
				}
				if c.Status == "fail" {
					failed++
				}
				if c.Required && c.Status != "pass" {
					open++
				}
			}
			if failed != open {
				t.Errorf("%d checks fail but %d required checks are not passing: the counts must agree", failed, open)
			}
			next := doctorNextCommand(report)
			if next == "olivares doctor" || !strings.HasPrefix(next, tc.nextPrefix) {
				t.Errorf("next = %q, want a command starting %q", next, tc.nextPrefix)
			}
		})
	}
}
