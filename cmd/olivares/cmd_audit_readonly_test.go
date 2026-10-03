// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
)

func TestAuditVerifyReadsWithoutRuntimeBoot(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := eng.demoTenant.String()
	if _, _, err := eng.signer.Checkpoint(t.Context(), eng.store, eng.demoTenant); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	// Verification must need only the ledger and audit keys, not other runtime custody.
	for _, name := range []string{"catalog-signing.key", "policy-signing.key"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := newAuditCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"verify", "--tenant", tenant, "--data-dir", dir, "--strict"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("offline verify: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"status": "ok"`) {
		t.Fatalf("verify did not prove the signed chain: %s", out.String())
	}
	// Explicit DSNs select their own ledger even with unused PostgreSQL settings.
	if err := os.Mkdir(filepath.Join(dir, "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd = newAuditCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"verify", "--tenant", tenant, "--data-dir", dir, "--dsn", filepath.Join(dir, "olivares.db"), "--strict"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("offline verify with explicit DSN: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"status": "ok"`) {
		t.Fatalf("explicit ledger not verified: %s", out.String())
	}
	after, err := os.ReadFile(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("offline verification changed the store")
	}
	for _, name := range []string{"catalog-signing.key", "policy-signing.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("offline verification created %s: %v", name, err)
		}
	}
}

func TestAuditVerifyPostgresSplitRolesReadOnly(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	dir := t.TempDir()
	eng, err := boot(t.Context(), bootConfig{DataDir: dir, Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, DemoSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	tenants := []model.TenantID{eng.demoTenant, model.SystemTenantID}
	for _, tenant := range tenants {
		if _, ok, err := eng.signer.Checkpoint(t.Context(), eng.store, tenant); err != nil || !ok {
			t.Fatalf("checkpoint %s: %t %v", tenant, ok, err)
		}
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	// The installation saves its role pair for subsequent commands without DSN flags.
	pgDir := filepath.Join(dir, "postgres")
	if err := os.Mkdir(pgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for role, dsn := range map[string]string{"app": pg.App, "owner": pg.Owner} {
		if err := os.WriteFile(filepath.Join(pgDir, role+".dsn"), []byte(dsn+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	maintenance, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	for _, dsn := range []string{pg.App, pg.Owner} {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("fixture DSN malformed")
		}
		if _, err := maintenance.ExecContext(t.Context(), "ALTER ROLE "+pgx.Identifier{cfg.User}.Sanitize()+" IN DATABASE "+pgx.Identifier{cfg.Database}.Sanitize()+" SET default_transaction_read_only = on"); err != nil {
			t.Fatal(err)
		}
	}
	for _, selection := range []struct {
		name string
		args []string
	}{
		{"explicit DSNs", []string{"--engine", "postgres", "--dsn", pg.App, "--owner-dsn", pg.Owner}},
		{"saved DSNs", nil},
		{"saved DSNs with explicit postgres", []string{"--engine", "postgres"}},
	} {
		t.Run(selection.name, func(t *testing.T) {
			for _, tenant := range tenants {
				cmd := newAuditCmd()
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SetArgs(append([]string{"verify", "--tenant", tenant.String(), "--data-dir", dir, "--strict"}, selection.args...))
				if err := cmd.Execute(); err != nil {
					t.Fatalf("read-only split-role verification: %v\n%s", err, out.String())
				}
				if !strings.Contains(out.String(), `"status": "ok"`) {
					t.Fatalf("signed chain not verified: %s", out.String())
				}
			}
		})
	}
	for role, dsn := range map[string]string{"app": pg.App, "owner": pg.Owner} {
		data, err := os.ReadFile(filepath.Join(pgDir, role+".dsn"))
		if err != nil || !bytes.Equal(data, []byte(dsn+"\n")) {
			t.Fatalf("offline verification changed %s credentials", role)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "olivares.db")); !os.IsNotExist(err) {
		t.Fatalf("offline verification created a SQLite store: %v", err)
	}
}
