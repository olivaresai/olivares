// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// pgMaintenanceForTest is the fixture superuser DSN without an sslmode (the local
// test cluster has no TLS), set in env so commands take it as a reference.
func pgMaintenanceForTest(t *testing.T, env string) string {
	t.Helper()
	maintenance := os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN")
	if maintenance == "" {
		t.Skip("requires task-local PostgreSQL")
	}
	u, err := url.Parse(maintenance)
	if err != nil {
		t.Fatal("fixture DSN is not a URL")
	}
	q := u.Query()
	q.Del("sslmode")
	u.RawQuery = q.Encode()
	t.Setenv(env, u.String())
	return u.String()
}

// db init installs the tenant inventory on the database it provisions, so the
// engine's first start lists every tenant without the administration role. The
// install-only mode then finds it with the names db init saved and verifies it:
// installing again is safe, it grants nothing. (A database without the inventory,
// the upgrade case, is TestTenantInventoryUpgradeJourneyOnDefaultPostgres.)
func TestDBInitInstallsTheTenantInventoryOnTheSavedConfiguration(t *testing.T) {
	pgMaintenanceForTest(t, "ARCH_DB_INIT_MAINTENANCE")
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "inventory install")
	if output, err := runDB(t, "init", "--superuser-dsn", "env:ARCH_DB_INIT_MAINTENANCE", "--data-dir", dir); err != nil {
		t.Fatalf("db init: %v\n%s", err, output)
	}
	// The first start: the engine migrates the database db init provisioned.
	eng, err := boot(ctx, bootConfig{DataDir: dir, Version: version, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("first start on the saved configuration: %v", err)
	}
	defer eng.Close()
	list := func() error {
		return eng.store.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.ListOrgs(ctx)
			return err
		})
	}
	if err := list(); err != nil {
		t.Fatalf("ListOrgs after db init, which installs the inventory: %v", err)
	}
	install := []string{"init", "--superuser-dsn", "env:ARCH_DB_INIT_MAINTENANCE", "--data-dir", dir, "--install-directory-inventory"}
	for round := 1; round <= 2; round++ {
		output, err := runDB(t, install...)
		if err != nil || !strings.Contains(output, "directory inventory installed and attested") || !strings.Contains(output, ": true") {
			t.Fatalf("install-only round %d = %v:\n%s", round, err, output)
		}
		if err := list(); err != nil {
			t.Fatalf("ListOrgs after install round %d: %v", round, err)
		}
	}
}

// quickstart --postgres provisions a fresh database and installs the tenant inventory
// before it serves, so the engine lists every tenant without the administration role.
func TestQuickstartPostgresInstallsTheTenantInventory(t *testing.T) {
	maintenance := pgMaintenanceForTest(t, "ARCH_QUICKSTART_MAINTENANCE")
	dir := filepath.Join(t.TempDir(), "quickstart inventory")
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Version: version, Logger: discardLogger(), quickstartPostgres: &maintenance})
	if err != nil {
		t.Fatalf("boot with quickstart PostgreSQL: %v", err)
	}
	defer eng.Close()
	if !eng.estateEnumerable {
		t.Fatal("the engine quickstart provisioned cannot list every tenant: the tenant inventory was not installed")
	}
	if err := eng.store.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.ListOrgs(context.Background())
		return err
	}); err != nil {
		t.Fatalf("ListOrgs on the quickstart engine: %v", err)
	}
}

// doctor names the background jobs the running engine cannot run, with the
// remedy, as an optional warning; it passes when every job runs.
func TestDoctorBackgroundJobsCheckNamesJobsThatDoNotRun(t *testing.T) {
	serve := func(body string) doctorDeps {
		return doctorDeps{httpGet: func(context.Context, string, string, time.Duration) (int, []byte, error) {
			return http.StatusOK, []byte(body), nil
		}}
	}
	o := doctorOptions{server: "https://127.0.0.1:8443", timeout: time.Second}
	c := doctorBackgroundJobsCheck(context.Background(), serve(`{"version":"x","jobs_not_running":[{"job":"retention","reason":"no_tenant_inventory"},{"job":"audit_checkpoints","reason":"no_tenant_inventory"}]}`), o, "")
	if c.Status != "warn" || c.Required || !strings.Contains(c.Detail, "retention (no_tenant_inventory)") || !strings.Contains(c.Detail, "audit_checkpoints") ||
		!strings.Contains(c.Remediation, "olivares db init") {
		t.Fatalf("check with jobs not running = %+v", c)
	}
	if c := doctorBackgroundJobsCheck(context.Background(), serve(`{"version":"x"}`), o, ""); c.Status != "pass" {
		t.Fatalf("check with every job running = %+v", c)
	}
}
