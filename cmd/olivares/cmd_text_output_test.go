// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestTextOnlyCommandsRejectJSONBeforeSideEffects(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dir := seedConsoleState(t, true)
	tokenPath := filepath.Join(dir, "setup.token")
	original, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "bundle.tar.gz")
	logs := filepath.Join(t.TempDir(), "engine.log")
	if err := os.WriteFile(logs, []byte("engine started\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"readyz", "--server", server.URL},
		{"first-boot", "--data-dir", dir},
		{"first-boot", "--data-dir", dir, "--new-token"},
		{"support", "bundle", "--data-dir", dir, "--offline", "--include", "logs", "--logs", logs, "--out", archive},
	}
	for _, args := range commands {
		for _, flags := range [][]string{{"-o", "json"}, {"--output=json"}, {"-o", "text", "-o", "json"}} {
			for _, before := range []bool{false, true} {
				argv := append(append([]string{}, args...), flags...)
				if before {
					argv = append(append([]string{}, flags...), args...)
				}
				t.Run(strings.Join(argv, " "), func(t *testing.T) {
					stdout, _, err := execRoot(t, argv...)
					if err == nil {
						t.Fatal("JSON request succeeded; want usage refusal with exit 2")
					}
					if exitcode.From(err) != exitcode.Usage {
						t.Fatalf("exit = %d, want 2 (err=%v)", exitcode.From(err), err)
					}
					if !strings.Contains(err.Error(), "has no JSON form") || !strings.Contains(err.Error(), "-o text") {
						t.Fatalf("refusal = %v, want no JSON form and the supported output mode", err)
					}
					if stdout != "" {
						t.Fatal("JSON refusal printed a report on stdout")
					}
					if requests.Load() != 0 {
						t.Fatal("JSON refusal made a readiness probe")
					}
					current, readErr := os.ReadFile(tokenPath)
					if readErr != nil || string(current) != string(original) {
						t.Fatal("JSON refusal changed the setup token")
					}
					if _, statErr := os.Stat(archive); !os.IsNotExist(statErr) {
						t.Fatalf("JSON refusal created an archive, stat err=%v", statErr)
					}
				})
			}
		}
	}
}

func TestTextOnlyCommandsKeepTextOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dir := seedConsoleState(t, true)
	archive := filepath.Join(t.TempDir(), "bundle.tar.gz")
	commands := []struct {
		args []string
		want string
	}{
		{[]string{"readyz", "--server", server.URL}, "ready: " + server.URL + "/readyz returned HTTP 200\n"},
		{[]string{"first-boot", "--data-dir", dir}, "=== OLIVARES AI — THIS INSTALLATION ==="},
		{[]string{"support", "bundle", "--data-dir", dir, "--offline", "--include", "logs", "--out", archive}, "support bundle written to " + archive + "\nmanifest.json sha256:"},
	}
	for _, tc := range commands {
		for _, flags := range [][]string{nil, {"-o", "text"}, {"-o", "json", "-o", "text"}} {
			argv := append(append([]string{}, tc.args...), flags...)
			t.Run(strings.Join(argv, " "), func(t *testing.T) {
				stdout, stderr, err := execRoot(t, argv...)
				if err != nil || stderr != "" || !strings.HasPrefix(stdout, tc.want) {
					t.Fatalf("text output: err=%v stderr=%q; expected prefix present=%v", err, stderr, strings.HasPrefix(stdout, tc.want))
				}
			})
		}
	}
}
