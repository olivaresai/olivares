// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

func TestQuickstartPostgresRejectsInvalidURLWithoutPersistingSecrets(t *testing.T) {
	const password = "postgres-invalid-url-fixture"
	for _, suffix := range []string{"@bad host/postgres", "@localhost/postgres?database=postgres", "@localhost/postgres?host=remote.example.invalid", "@localhost/postgres?port=5433", "@localhost/postgres?%20host%20=remote.example.invalid", "@localhost/postgres?%20database%20=postgres"} {
		t.Run(suffix, func(t *testing.T) {
			t.Setenv("N2_QUICKSTART_POSTGRES", "postgres://postgres:"+password+suffix)
			dir := filepath.Join(t.TempDir(), "new-install")
			cmd := newQuickstartCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--postgres", "env:N2_QUICKSTART_POSTGRES", "--data-dir", dir})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "PostgreSQL URL") {
				t.Fatalf("invalid maintenance URL must be refused clearly, got %v", err)
			}
			if strings.Contains(err.Error()+out.String(), password) {
				t.Fatal("invalid PostgreSQL URL disclosed its password")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid maintenance URL created installation state: %v", err)
			}
		})
	}
}

func TestQuickstartPostgresCreatesPrivateRolesAndReusesThem(t *testing.T) {
	maintenance := os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN")
	if maintenance == "" {
		t.Skip("requires the task-local PostgreSQL fixture")
	}
	dir := filepath.Join(t.TempDir(), "data")
	args := func() []string {
		return []string{"--data-dir", dir,
			"--listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)),
			"--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))}
	}
	first := runQuickstart(t, setupTokenShape, append(args(), "--postgres", "env:OLIVARES_TEST_POSTGRES_SUPERUSER_DSN")...)
	if strings.Contains(first, "level=") || !strings.Contains(first, firstHourWelcomeNextSteps) {
		t.Fatal("PostgreSQL quickstart did not print the quiet guided console panel")
	}
	if _, err := os.Stat(filepath.Join(dir, "olivares.db")); !os.IsNotExist(err) {
		t.Fatalf("PostgreSQL quickstart created a SQLite database: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "postgres"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("PostgreSQL credential directory must be private: %v", err)
	}
	files := map[string][]byte{}
	passwords := map[string]string{}
	log, err := os.ReadFile(filepath.Join(dir, "olivares.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"app", "owner"} {
		file := filepath.Join(dir, "postgres", role+".dsn")
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s DSN must be private: %v", role, err)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		files[role] = data
		u, err := url.Parse(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatalf("%s DSN is invalid", role)
		}
		passwords[role], _ = u.User.Password()
		if len(passwords[role]) < 32 || strings.Contains(first+string(log), passwords[role]) {
			t.Fatalf("%s password must be generated and absent from terminal/log output", role)
		}
		posture, err := coreengine.ProbeRole(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: strings.TrimSpace(string(data))})
		if err != nil || !posture.Reachable || posture.Superuser || posture.BypassRLS {
			t.Fatalf("%s role must be reachable, NOSUPERUSER NOBYPASSRLS", role)
		}
	}
	if passwords["app"] == passwords["owner"] || bytes.Equal(files["app"], files["owner"]) {
		t.Fatal("application and owner credentials must be distinct")
	}
	second := runQuickstart(t, regexp.MustCompile("Setup is still pending"), args()...)
	if strings.Contains(second, "level=") || setupTokenShape.MatchString(second) {
		t.Fatal("returning PostgreSQL quickstart did not preserve pending setup guidance")
	}
	for role, before := range files {
		after, err := os.ReadFile(filepath.Join(dir, "postgres", role+".dsn"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("restart changed %s credentials or asked for the maintenance DSN", role)
		}
	}
	// A packaged service uses `serve`, so it must reuse the same backend too.
	runEngineCommand(t, newServeCmd(), regexp.MustCompile("SETUP STILL PENDING"), args()...)
	if _, err := os.Stat(filepath.Join(dir, "olivares.db")); !os.IsNotExist(err) {
		t.Fatalf("serve switched the PostgreSQL installation to SQLite: %v", err)
	}
	cmd := newServeCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append(args(), "--engine", "sqlite"))
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "uses PostgreSQL") {
		t.Fatal("explicit SQLite selection must refuse mixing backends in one data directory")
	}
	reader := newAuditCmd()
	reader.SetOut(&bytes.Buffer{})
	reader.SetErr(&bytes.Buffer{})
	reader.SetArgs([]string{"verify", "--data-dir", dir, "--engine", "sqlite", "--tenant", "10000000-0000-0000-0000-000000000001"})
	if err := reader.Execute(); err == nil || !strings.Contains(err.Error(), "uses PostgreSQL") {
		t.Fatalf("read-only CLI must preserve explicit engine selection too: %v", err)
	}
}
