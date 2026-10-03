// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgservicefile"
)

func TestPostgresDSNsStayOutOfProcessArguments(t *testing.T) {
	t.Setenv("PGSERVICEFILE", "parent-service-file")
	for _, tc := range []struct {
		name, dsn string
		settings  map[string]string
	}{
		{"uri", "postgres://backup:fixture%40password@localhost:5440/olivares?sslmode=verify-full&application_name=dump+job", map[string]string{"host": "localhost", "port": "5440", "dbname": "olivares", "user": "backup", "password": "fixture@password", "sslmode": "verify-full", "application_name": "dump+job"}},
		{"keywords", `host=localhost port=5440 dbname='fixture db' user=backup password='fixture \'password' sslmode=disable options='-c statement_timeout=10'`, map[string]string{"dbname": "fixture db", "password": "fixture 'password", "options": "-c statement_timeout=10"}},
		{"socket", "postgresql://backup:fixture@/olivares?host=%2Fsrv%2Fexample%2Fsocket&sslmode=disable", map[string]string{"host": "/srv/example/socket", "dbname": "olivares"}},
		{"hosts", "postgres://backup:fixture@localhost:5440,127.0.0.1:5441/olivares?target_session_attrs=read-write", map[string]string{"host": "localhost,127.0.0.1", "port": "5440,5441", "target_session_attrs": "read-write"}},
		{"query-overrides", "postgres://other:fixture@localhost/wrong?dbname=olivares&user=backup&password=final%2Bfixture", map[string]string{"dbname": "olivares", "user": "backup", "password": "final+fixture"}},
	} {
		for _, kind := range []string{"dump", "restore"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("DSN_FIXTURE_OUTPUT", dir)
				bin := filepath.Join(dir, "pg-client")
				stub := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$DSN_FIXTURE_OUTPUT/argv"
printf '%s' "${PGSERVICEFILE:-}" > "$DSN_FIXTURE_OUTPUT/path"
if [ -f "${PGSERVICEFILE:-}" ]; then
  cp "$PGSERVICEFILE" "$DSN_FIXTURE_OUTPUT/service"
  ls -ld "$PGSERVICEFILE" > "$DSN_FIXTURE_OUTPUT/file-mode"
  ls -ld "$(dirname "$PGSERVICEFILE")" > "$DSN_FIXTURE_OUTPUT/dir-mode"
fi
`
				if err := os.WriteFile(bin, []byte(stub), 0o700); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "dump" {
					err = runPgDump(context.Background(), bin, tc.dsn, filepath.Join(dir, "dump"))
				} else {
					err = runPgRestore(context.Background(), bin, tc.dsn, filepath.Join(dir, "dump"))
				}
				if err != nil {
					t.Fatalf("PostgreSQL client invocation failed: %v", err)
				}
				argv, err := os.ReadFile(filepath.Join(dir, "argv"))
				if err != nil {
					t.Fatal(err)
				}
				args := strings.Split(strings.TrimSpace(string(argv)), "\n")
				for i, arg := range args {
					if arg == "--dbname" && i+1 < len(args) && args[i+1] != "service=olivares" {
						t.Error("dbname exposes the connection instead of a service name")
					}
					if strings.Contains(arg, "fixture") || strings.Contains(arg, "postgres://") {
						t.Error("credential-bearing DSN reached child argv")
					}
				}
				if kind == "restore" && !strings.Contains(string(argv), "--single-transaction\n") {
					t.Error("restore lost its transaction guard")
				}
				if kind == "dump" && !strings.Contains(string(argv), "--format=custom\n") {
					t.Error("dump lost its custom format")
				}
				path, _ := os.ReadFile(filepath.Join(dir, "path"))
				if string(path) == "parent-service-file" || len(path) == 0 {
					t.Fatal("child did not get its own service file")
				}
				if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
					t.Error("temporary service file survived child exit")
				}
				for name, want := range map[string]string{"file-mode": "-rw-------", "dir-mode": "drwx------"} {
					b, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil || (len(strings.Fields(string(b))) == 0 || strings.Fields(string(b))[0][:10] != want) {
						t.Errorf("private %s was not enforced", name)
					}
				}
				file, err := pgservicefile.ReadServicefile(filepath.Join(dir, "service"))
				if err != nil {
					t.Fatal("no readable service entry was captured")
				}
				service, err := file.GetService("olivares")
				if err != nil {
					t.Fatal(err)
				}
				for key, want := range tc.settings {
					if service.Settings[key] != want {
						t.Errorf("connection setting %s changed", key)
					}
				}
				if os.Getenv("PGSERVICEFILE") != "parent-service-file" {
					t.Error("child configuration changed the parent environment")
				}
			})
		}
	}
	for _, kind := range []string{"dump", "restore"} {
		t.Run("failed-child/"+kind, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DSN_FIXTURE_OUTPUT", dir)
			bin := filepath.Join(dir, "pg-client")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' \"$PGSERVICEFILE\" > \"$DSN_FIXTURE_OUTPUT/path\"\nexit 23\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "dump" {
				err = runPgDump(context.Background(), bin, "postgres://backup:fixture@localhost/db", filepath.Join(dir, "dump"))
			} else {
				err = runPgRestore(context.Background(), bin, "postgres://backup:fixture@localhost/db", filepath.Join(dir, "dump"))
			}
			if err == nil {
				t.Fatal("failed PostgreSQL client was reported successful")
			}
			path, err := os.ReadFile(filepath.Join(dir, "path"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
				t.Fatal("temporary credentials survived child failure")
			}
		})
	}
	for name, dsn := range map[string]string{
		"nested-service":  "service=existing password=do-not-disclose",
		"line-break":      "postgres://user:do-not-disclose%0Avalue@localhost/db",
		"trailing-space":  "password='do-not-disclose ' host=localhost",
		"long-entry":      "password=" + strings.Repeat("x", 1024),
		"invalid-port":    "postgres://user:do-not-disclose@localhost:bad,localhost:5440/db",
		"dangling-escape": "password=do-not-disclose\\",
		"ldap-key":        "ldap://example.invalid/do-not-disclose=bad",
		"nul":             "password='do-not-disclose\x00'",
	} {
		t.Run("refusal/"+name, func(t *testing.T) {
			err := runPgDump(context.Background(), "/no-such-fixture-client", dsn, "unused")
			if err == nil || strings.Contains(err.Error(), "do-not-disclose") || !strings.Contains(err.Error(), "cannot be represented safely") {
				t.Fatal("unrepresentable DSN did not get a safe refusal")
			}
		})
	}
	for _, kind := range []string{"dump", "restore"} {
		for _, owner := range []bool{false, true} {
			t.Run("script/"+kind+map[bool]string{false: "/single", true: "/owner-split"}[owner], func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("DSN_FIXTURE_OUTPUT", dir)
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("OLIVARES_DSN", "postgres://app:do-not-disclose@localhost/db")
				t.Setenv("OLIVARES_ADMIN_DSN", "postgres://admin:do-not-disclose@localhost/db")
				t.Setenv("OLIVARES_OWNER_DSN", "")
				if owner {
					t.Setenv("OLIVARES_OWNER_DSN", "postgres://owner:do-not-disclose@localhost/db")
				}
				t.Setenv("OLIVARES_DATA_DIR", filepath.Join(dir, "data"))
				t.Setenv("OLIVARES_DR_PASSPHRASE_FILE", filepath.Join(dir, "passphrase"))
				stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DSN_FIXTURE_OUTPUT/argv\"\n"
				if err := os.WriteFile(filepath.Join(dir, "olivares"), []byte(stub), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "pg_dump"), []byte("#!/bin/sh\nexit 88\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("sh", "../../deploy/postgres/backup/pg-"+kind+".sh", filepath.Join(dir, "backup.drbundle"))
				if err := cmd.Run(); err != nil {
					t.Fatalf("backup script did not use the existing DR command: %v", err)
				}
				argv, err := os.ReadFile(filepath.Join(dir, "argv"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(argv), "do-not-disclose") || !strings.Contains(string(argv), "--dsn=env:OLIVARES_DSN") {
					t.Error("script connection was expanded into argv")
				}
				if strings.Contains(string(argv), "--owner-dsn=env:OLIVARES_OWNER_DSN") != owner {
					t.Error("optional owner posture changed")
				}
				if !strings.Contains(string(argv), "--admin-dsn=env:OLIVARES_ADMIN_DSN") {
					t.Error("admin connection reference was lost")
				}
			})
		}
	}

}
