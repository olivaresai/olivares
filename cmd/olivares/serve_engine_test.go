// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestServeEngineValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{name: "unknown", args: []string{"--engine", "postgre"}},
		{name: "empty", args: []string{"--engine="}},
		{name: "case", args: []string{"--engine", "SQLite"}},
		{name: "whitespace", args: []string{"--engine", " sqlite "}},
		{name: "default", valid: true},
		{name: "sqlite", args: []string{"--engine", "sqlite"}, valid: true},
		{name: "postgres", args: []string{"--engine", "postgres"}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			t.Setenv("OLIVARES_TEST_ENGINE_DSN", "")
			// A missing DSN reference stops accepted engines before any database or
			// listener opens, and makes an invalid engine's silent fallback observable.
			args := append([]string{"serve", "--data-dir", dir, "--dsn", "env:OLIVARES_TEST_ENGINE_DSN"}, tc.args...)
			code, _, _, err := runCLIExit(t, args...)
			if tc.valid {
				if err == nil || !strings.Contains(err.Error(), "--dsn:") || !strings.Contains(err.Error(), "OLIVARES_TEST_ENGINE_DSN") {
					t.Fatalf("valid engine did not reach DSN resolution: %v", err)
				}
				return
			}
			if code != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "--engine") || !strings.Contains(err.Error(), "sqlite or postgres") {
				t.Errorf("invalid engine: exit=%d err=%v; want exit 2 and supported engines", code, err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("invalid engine touched the data directory: %v", err)
			}
		})
	}
}
