// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresServiceFileCommandIsInternal(t *testing.T) {
	t.Setenv("OLIVARES_ADMIN_DSN", "postgres://backup:fixture@localhost/olivares?sslmode=disable")
	root := newDBCmd()
	var help bytes.Buffer
	root.SetOut(&help)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(help.String(), "pg-service-file") {
		t.Fatal("internal bridge appeared in public help")
	}
	out := filepath.Join(t.TempDir(), "private", "pg_service.conf")
	cmd := newDBCmd()
	cmd.SetArgs([]string{"pg-service-file", "--dsn=env:OLIVARES_ADMIN_DSN", out})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{out: 0600, filepath.Dir(out): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatal("internal service bridge did not create private custody")
		}
	}
	b, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(b), "password=fixture\n") {
		t.Fatal("internal bridge lost the resolved credential")
	}
	if err := cmd.Execute(); err == nil {
		t.Fatal("internal bridge overwrote existing custody")
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, b) {
		t.Fatal("refused overwrite changed existing custody")
	}
	for _, child := range root.Commands() {
		if child.Name() == "pg-service-file" && !child.Hidden {
			t.Fatal("internal bridge was registered as public")
		}
	}
	for _, scenario := range []string{"shared-directory", "unrepresentable-dsn"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			if scenario == "shared-directory" {
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("OLIVARES_ADMIN_DSN", "service=nested")
			}
			path := filepath.Join(dir, "pg_service.conf")
			refused := newDBCmd()
			refused.SetArgs([]string{"pg-service-file", "--dsn=env:OLIVARES_ADMIN_DSN", path})
			if err := refused.Execute(); err == nil {
				t.Fatal("internal bridge accepted unsafe credential custody")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("refused bridge left a credential file")
			}
		})
	}
}
