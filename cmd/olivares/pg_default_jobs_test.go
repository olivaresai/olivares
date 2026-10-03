// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// bootPostgresWithCompliance boots a fresh data directory whose module profile runs
// compliance (so the retention sweep is composed) on the given PostgreSQL roles and
// returns server-info's jobs_not_running.
func bootPostgresWithCompliance(t *testing.T, dsn, owner, admin string) []any {
	t.Helper()
	dir := t.TempDir()
	if err := saveNodeModuleSelection(dir, []string{"compliance"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "postgres", DSN: dsn, OwnerDSN: owner, AdminDSN: admin,
		Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	code, info, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, "/v1/server-info", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("server-info = %d %s", code, raw)
	}
	jobs, _ := info["jobs_not_running"].([]any)
	return jobs
}

// The retention sweep must cover every tenant. On PostgreSQL with only the
// application and owner roles and no tenant inventory, it cannot, so it stays
// fail-closed and server-info says it does not run, and why. Each test boots one
// engine: the commercial build refuses a second boot in one process.
func TestDefaultPostgresServerInfoSaysRetentionDoesNotRun(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	jobs := bootPostgresWithCompliance(t, pg.App, pg.Owner, "")
	found := false
	for _, j := range jobs {
		if job, _ := j.(map[string]any); job["job"] == "retention" && job["reason"] == "no_tenant_inventory" {
			found = true
		}
	}
	if !found {
		t.Fatalf("server-info jobs_not_running = %v, want retention with reason no_tenant_inventory", jobs)
	}
}

// With the administration role the retention sweep runs: server-info says nothing.
func TestPostgresWithTheAdminRoleListsNoJobNotRunning(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	if jobs := bootPostgresWithCompliance(t, pg.App, pg.Owner, pg.Admin); len(jobs) != 0 {
		t.Fatalf("server-info jobs_not_running with the administration role = %v, want none", jobs)
	}
}

// SQLite can enumerate every tenant: server-info lists no job that does not run.
func TestSQLiteServerInfoListsNoJobNotRunning(t *testing.T) {
	_, h, _, _ := bootWithModuleProfile(t, []string{"compliance"})
	_, info, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/server-info", "", "", nil)
	if _, present := info["jobs_not_running"]; present {
		t.Fatalf("server-info on SQLite lists jobs_not_running: %s", raw)
	}
}
