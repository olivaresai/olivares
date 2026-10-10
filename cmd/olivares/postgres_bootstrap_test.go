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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

func TestPostgresBootstrapTLSDefaults(t *testing.T) {
	for _, tc := range []struct{ raw, mode string }{
		{"postgres://postgres@localhost/postgres", "prefer"},
		{"postgres://postgres@127.0.0.1/postgres", "prefer"},
		{"postgres://postgres@[::1]/postgres", "prefer"},
		{"postgres:///postgres?host=/tmp/postgres-fixture", "prefer"},
		{"postgres://postgres@db.example.invalid/postgres", "verify-full"},
		{"postgres://postgres@localhost/postgres?sslmode=require", "require"},
		{"postgres://postgres@localhost/postgres?ssl=true", "require"},
		{"host=localhost user=postgres dbname=postgres", "prefer"},
		{"host=localhost user=postgres password='fixture\\' with space' dbname=postgres sslmode=require", "require"},
		{"host=/tmp/postgres-fixture user=postgres dbname=postgres", "prefer"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			u, err := parseQuickstartPostgresURL(tc.raw)
			if err != nil {
				t.Fatalf("valid maintenance target refused: %v", err)
			}
			if got := u.Query().Get("sslmode"); got != tc.mode {
				t.Fatalf("sslmode = %q, want %q", got, tc.mode)
			}
		})
	}
}

func TestPostgresBootstrapRejectsAmbiguousTLSMode(t *testing.T) {
	for _, query := range []string{"sslmode=prefer&sslmode=require", "ssl=true&sslmode=prefer", "ssl=true&ssl=false"} {
		if _, err := parseQuickstartPostgresURL("postgres://postgres@localhost/postgres?" + query); err == nil {
			t.Fatalf("ambiguous TLS selection must be refused: %s", query)
		}
	}
}

func TestDBInitDoesNotReplaceAnUnknownNamedRolePassword(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-install")
	t.Setenv("N2_NAMED_ROLE_MAINTENANCE", "postgres://postgres@127.0.0.1/postgres")
	if _, err := runDB(t, "init", "--data-dir", dir, "--superuser-dsn", "env:N2_NAMED_ROLE_MAINTENANCE", "--app-role", "existing_app"); err == nil || !strings.Contains(err.Error(), "--app-password-file") {
		t.Fatal("adopting a named role must request its credential before replacing an unknown password")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("refusing unknown named-role credentials must not create installation state")
	}
}

func TestDBInitWritesPrivateCredentialsAndReusesQuickstartConfig(t *testing.T) {
	maintenance := os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN")
	if maintenance == "" {
		t.Skip("requires task-local PostgreSQL")
	}
	u, err := url.Parse(maintenance)
	if err != nil {
		t.Fatal("fixture DSN is not a URL")
	}
	q := u.Query()
	q.Del("sslmode") // default local PG has no TLS: prefer must fall back safely.
	u.RawQuery = q.Encode()
	t.Setenv("N2_DB_INIT_MAINTENANCE", u.String())
	dir := filepath.Join(t.TempDir(), "database path 'quoted'")
	args := []string{"init", "--superuser-dsn", "env:N2_DB_INIT_MAINTENANCE", "--data-dir", dir}
	output, err := runDB(t, args...)
	if err != nil {
		t.Fatalf("db init should provision a usable local install: %v", err)
	}
	t.Logf("db init terminal output:\n%s", output)
	if strings.Count(output, "Next:") != 1 || !strings.Contains(output, "Next: olivares quickstart --data-dir ") ||
		strings.Contains(output, "store each password") || !strings.Contains(output, "sslmode=prefer") {
		t.Fatalf("db init must name one runnable next step and the chosen TLS mode: %s", output)
	}
	// Parse the actual printed command with a POSIX shell, without starting an
	// engine. The directory must survive quoting as one unchanged argument.
	next := strings.TrimSpace(strings.SplitN(output, "Next: ", 2)[1])
	parsed, err := exec.Command("sh", "-c", "set -- "+next+"; printf '%s' \"$4\"").Output()
	if err != nil || string(parsed) != dir {
		t.Fatal("printed Next command does not preserve the installation directory")
	}
	info, err := os.Stat(filepath.Join(dir, "postgres"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("credential directory must be private")
	}
	before := map[string][]byte{}
	for _, role := range []string{"app", "owner"} {
		file := filepath.Join(dir, "postgres", role+".dsn")
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s DSN must be 0600", role)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		before[role] = data
		roleURL, err := url.Parse(strings.TrimSpace(string(data)))
		if err != nil || roleURL.Query().Get("sslmode") != "prefer" {
			t.Fatalf("%s DSN must select local TLS fallback", role)
		}
		password, _ := roleURL.User.Password()
		if len(password) < 32 || strings.Contains(output, password) {
			t.Fatalf("%s credential must be generated and absent from stdout", role)
		}
		posture, err := coreengine.ProbeRole(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: strings.TrimSpace(string(data))})
		if err != nil || !posture.Reachable || posture.Superuser || posture.BypassRLS {
			t.Fatalf("%s pool must be reachable and unprivileged", role)
		}
	}
	if _, err := runDB(t, args...); err != nil {
		t.Fatalf("db init retry: %v", err)
	}
	q.Set("sslmode", "require")
	u.RawQuery = q.Encode()
	t.Setenv("N2_DB_INIT_MAINTENANCE", u.String())
	if _, err := runDB(t, args...); err == nil || !strings.Contains(err.Error(), "TLS mode differs") {
		t.Fatal("retry must not silently downgrade an explicit maintenance TLS requirement")
	}
	cfg, err := quickstartPostgresConfig(context.Background(), dir, "")
	if err != nil || cfg.Engine != store.EnginePostgres || cfg.DSN != "file:"+filepath.Join(dir, "postgres/app.dsn") || cfg.OwnerDSN != "file:"+filepath.Join(dir, "postgres/owner.dsn") {
		t.Fatal("quickstart must reuse exactly the db init configuration without the maintenance DSN")
	}
	for role, want := range before {
		got, err := os.ReadFile(filepath.Join(dir, "postgres", role+".dsn"))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("retry changed %s credentials", role)
		}
	}
	// Cross-tenant administration can be enabled later without recreating the
	// application database or changing its existing role credentials.
	q.Del("sslmode")
	u.RawQuery = q.Encode()
	t.Setenv("N2_DB_INIT_MAINTENANCE", u.String())
	// An installation saved before db init provisioned an admin role has no
	// admin.dsn and gets the optional role later; simulate one.
	if err := os.Remove(filepath.Join(dir, "postgres/admin.dsn")); err != nil {
		t.Fatal(err)
	}
	adminPWFile := filepath.Join(dir, "admin.password")
	if err := os.WriteFile(adminPWFile, []byte("n2-later-admin-password-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runDB(t, append(args, "--admin-role", "bad role", "--admin-password-file", adminPWFile)...); err == nil {
		t.Fatal("invalid admin role must be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "postgres/admin.dsn")); !os.IsNotExist(err) {
		t.Fatal("invalid optional admin must not change the saved working configuration")
	}
	for role, want := range before {
		got, err := os.ReadFile(filepath.Join(dir, "postgres", role+".dsn"))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("invalid optional admin changed %s credentials", role)
		}
	}
	if _, err := runDB(t, append(args, "--admin-role", "n2_later_admin", "--admin-password-file", adminPWFile)...); err != nil {
		t.Fatalf("adding the optional admin role: %v", err)
	}
	cfg, err = quickstartPostgresConfig(context.Background(), dir, "")
	if err != nil || cfg.AdminDSN != "file:"+filepath.Join(dir, "postgres/admin.dsn") {
		t.Fatal("quickstart must load a subsequently configured admin pool")
	}
}

func TestDBInitOperatorPasswordFilesAndUnixSocket(t *testing.T) {
	maintenance := os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN")
	if maintenance == "" || !filepath.IsAbs(os.Getenv("PGHOST")) {
		t.Skip("requires task-local PostgreSQL with its Unix socket directory")
	}
	u, err := url.Parse(maintenance)
	if err != nil {
		t.Fatal("fixture DSN is not a URL")
	}
	u.Host = ":" + u.Port()
	q := u.Query()
	q.Del("sslmode")
	q.Set("host", os.Getenv("PGHOST"))
	u.RawQuery = q.Encode()
	t.Setenv("N2_DB_INIT_SOCKET", u.String())
	dir := t.TempDir()
	args := []string{"init", "--superuser-dsn", "env:N2_DB_INIT_SOCKET", "--data-dir", dir, "--admin-role", "n2_db_init_admin"}
	passwords := map[string]string{"app": "n2-application-password-fixture", "owner": "n2-owner-password-fixture", "admin": "n2-admin-password-fixture"}
	for _, role := range []string{"app", "owner", "admin"} {
		file := filepath.Join(dir, role+".password")
		if err := os.WriteFile(file, []byte(passwords[role]), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--"+role+"-password-file", file)
	}
	output, err := runDB(t, args...)
	if err != nil {
		t.Fatalf("Unix socket db init: %v", err)
	}
	for _, role := range []string{"app", "owner", "admin"} {
		file := filepath.Join(dir, "postgres", role+".dsn")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s credential file is not private", role)
		}
		roleURL, err := url.Parse(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal("saved role URL is invalid")
		}
		password, _ := roleURL.User.Password()
		if password != passwords[role] || strings.Contains(output, password) || roleURL.Query().Get("sslmode") != "prefer" {
			t.Fatalf("%s password-file or socket TLS selection was not preserved privately", role)
		}
		posture, err := coreengine.ProbeRole(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: strings.TrimSpace(string(data))})
		if err != nil || !posture.Reachable || posture.Superuser || posture.BypassRLS != (role == "admin") {
			t.Fatalf("%s role is not reachable with its expected least-privilege posture", role)
		}
	}
	cfg, err := quickstartPostgresConfig(context.Background(), dir, "")
	if err != nil || cfg.AdminDSN != "file:"+filepath.Join(dir, "postgres", "admin.dsn") {
		t.Fatal("quickstart must load the optional admin credential from the same installation")
	}
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "\\'") + "'"
	}
	password, _ := u.User.Password()
	keywordDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres", quote(os.Getenv("PGHOST")), u.Port(), quote(u.User.Username()), quote(password))
	t.Setenv("N2_DB_INIT_KEYWORD", keywordDSN)
	if _, err := runDB(t, "init", "--superuser-dsn", "env:N2_DB_INIT_KEYWORD", "--data-dir", filepath.Join(dir, "keyword-install")); err != nil {
		t.Fatalf("existing libpq keyword/value maintenance DSN compatibility: %v", err)
	}
}
