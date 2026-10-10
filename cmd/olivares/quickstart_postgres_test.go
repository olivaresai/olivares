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
	for _, suffix := range []string{"@bad host/postgres", "@localhost/postgres?database=postgres", "@localhost/postgres?host=remote.example.invalid", "@localhost/postgres?port=5433", "@localhost/postgres?%20host%20=remote.example.invalid", "@localhost/postgres?%20database%20=postgres", "@/postgres?host=/tmp/postgres-fixture&port=not-a-port", "@/postgres?host=/tmp/postgres-fixture&port=65536", "@/postgres?host=/tmp/postgres-fixture&port=5432&port=5433", "@/postgres?host=/tmp/postgres-fixture&user=other"} {
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
	// The same preparation gives a new installation the admin role its backups need.
	if info, err := os.Stat(filepath.Join(dir, "postgres", "admin.dsn")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("a new PostgreSQL installation must save a private admin DSN: %v", err)
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

// The driver chooses the socket port; the CLI must not reject its native URL form.
func TestQuickstartPostgresSocketQueryPortReachesDriver(t *testing.T) {
	for _, prefix := range []string{"postgresql:///postgres", "postgres://postgres@/postgres"} {
		t.Run(prefix, func(t *testing.T) {
			socket, err := os.MkdirTemp("", "qs-socket-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(socket) })
			u := prefix + "?host=" + url.QueryEscape(socket) + "&port=54329"
			t.Setenv("QUICKSTART_SOCKET_DSN", u)
			cmd := newQuickstartCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--postgres", "env:QUICKSTART_SOCKET_DSN", "--data-dir", filepath.Join(t.TempDir(), "data")})
			err = cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), filepath.Join(socket, ".s.PGSQL.54329")) {
				t.Fatalf("socket query port must reach the native driver's selected socket, got %v", err)
			}
		})
	}
}

func TestQuickstartPostgresSocketReuseChecksEffectiveAddress(t *testing.T) {
	t.Setenv("PGPORT", "5432")
	socket, err := os.MkdirTemp("", "qs-reuse-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socket) })
	for _, tc := range []struct {
		name, savedAuthority, savedPort, maintenanceAuthority, maintenancePort string
		refuse                                                                 bool
	}{
		{"changed query port", "", "5432", "", "5433", true},
		{"changed authority port", "", "5432", ":5433", "", true},
		{"same query port", "", "5432", "", "5432", false},
		{"query and authority equivalent", "", "5432", ":5432", "", false},
		{"authority and query equivalent", ":5432", "", "", "5432", false},
		{"implicit default matches query", "", "", "", "5432", false},
		{"query matches implicit default", "", "5432", "", "", false},
		{"changed fallback port", "", "5432,5434", "", "5432,5433", true},
		{"same fallback ports", "", "5432,5433", "", "5432,5433", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			credentialDir := filepath.Join(dir, "postgres")
			if err := os.MkdirAll(credentialDir, 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{}
			for _, role := range []string{"app", "owner"} {
				u := &url.URL{Scheme: "postgres", Host: tc.savedAuthority, Path: "/saved_db",
					User: url.UserPassword("saved_"+role, "saved-port-fixture-password")}
				q := url.Values{"host": {socket}, "sslmode": {"disable"}}
				if strings.Contains(tc.savedPort, ",") {
					q.Set("host", socket+","+socket)
				}
				if tc.savedPort != "" {
					q.Set("port", tc.savedPort)
				}
				u.RawQuery = q.Encode()
				files[role] = []byte(u.String() + "\n")
				if err := os.WriteFile(filepath.Join(credentialDir, role+".dsn"), files[role], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			u := &url.URL{Scheme: "postgres", Host: tc.maintenanceAuthority, Path: "/postgres", User: url.User("postgres")}
			q := url.Values{"host": {socket}, "sslmode": {"disable"}}
			if strings.Contains(tc.maintenancePort, ",") {
				q.Set("host", socket+","+socket)
			}
			if tc.maintenancePort != "" {
				q.Set("port", tc.maintenancePort)
			}
			u.RawQuery = q.Encode()
			t.Setenv("QUICKSTART_SOCKET_DSN", u.String())
			cmd := newQuickstartCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--postgres", "env:QUICKSTART_SOCKET_DSN", "--data-dir", dir})
			err := cmd.Execute()
			if tc.refuse {
				if err == nil || !strings.Contains(err.Error(), "another PostgreSQL address") {
					t.Fatalf("changed port must refuse before provisioning, got %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), filepath.Join(socket, ".s.PGSQL.5432")) {
				t.Fatalf("equivalent port must pass the address guard and reach the selected driver socket, got %v", err)
			}
			for role, before := range files {
				after, err := os.ReadFile(filepath.Join(credentialDir, role+".dsn"))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("address check changed saved %s credentials", role)
				}
			}
		})
	}
}

func TestQuickstartPostgresSocketQueryPortProvisionsUsableRoles(t *testing.T) {
	socket, port := os.Getenv("OLIVARES_PG_SOCKET_DIR"), os.Getenv("PGPORT")
	if socket == "" || port == "" {
		t.Skip("requires task-local PostgreSQL Unix socket")
	}
	for _, prefix := range []string{"postgresql:///postgres", "postgres://postgres@/postgres"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv("QUICKSTART_SOCKET_DSN", prefix+"?host="+url.QueryEscape(socket)+"&port="+port)
			dir := filepath.Join(t.TempDir(), "data")
			output := runQuickstart(t, setupTokenShape, "--postgres", "env:QUICKSTART_SOCKET_DSN", "--data-dir", dir,
				"--listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)),
				"--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)))
			files := map[string][]byte{}
			for _, role := range []string{"app", "owner"} {
				path := filepath.Join(dir, "postgres", role+".dsn")
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("%s DSN must remain private: %v", role, err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				files[role] = raw
				dsn := strings.TrimSpace(string(raw))
				u, err := url.Parse(dsn)
				if err != nil || u.Query().Get("host") != socket || u.Query().Get("port") != port {
					t.Fatalf("%s DSN must preserve the selected socket and query port", role)
				}
				password, _ := u.User.Password()
				if len(password) < 32 || strings.Contains(output, password) {
					t.Fatalf("%s generated password must stay out of command output", role)
				}
				posture, err := coreengine.ProbeRole(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: dsn})
				if err != nil || !posture.Reachable || posture.Superuser || posture.BypassRLS {
					t.Fatalf("%s generated role must connect through the socket without elevated privileges", role)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "olivares.db")); !os.IsNotExist(err) {
				t.Fatal("socket PostgreSQL quickstart must not fall back to SQLite")
			}
			// Reuse the same address with pgx's equivalent authority-port spelling.
			u, err := url.Parse(prefix)
			if err != nil {
				t.Fatal(err)
			}
			u.Host = ":" + port
			u.RawQuery = url.Values{"host": {socket}}.Encode()
			t.Setenv("QUICKSTART_SOCKET_DSN", u.String())
			runQuickstart(t, regexp.MustCompile("Setup is still pending"), "--postgres", "env:QUICKSTART_SOCKET_DSN", "--data-dir", dir,
				"--listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)),
				"--grpc-listen", fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)))
			for role, before := range files {
				after, err := os.ReadFile(filepath.Join(dir, "postgres", role+".dsn"))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("equivalent socket port reuse changed saved %s credentials", role)
				}
			}
		})
	}
}
