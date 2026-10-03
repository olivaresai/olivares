// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

const dbInitInventoryDirEnv = "OLIVARES_TEST_DBINIT_INVENTORY_DIR"

// db init provisions a fresh database with the application and owner roles, applies
// the engine's schema as the owner and installs the tenant inventory. The first
// start then reaches readiness and runs every job that must cover every tenant:
// server-info lists none as not running and the audit checkpoints anchor every
// tenant, with no administration role. The start runs in its own process, as the
// engine's restart re-executes its binary.
func TestDBInitInstallsTheTenantInventorySoTheFirstStartRunsEveryJob(t *testing.T) {
	pgMaintenanceForTest(t, "ARCH_DB_INIT_DEFAULT_INVENTORY")
	dir := filepath.Join(t.TempDir(), "default inventory")
	output, err := runDB(t, "init", "--superuser-dsn", "env:ARCH_DB_INIT_DEFAULT_INVENTORY", "--data-dir", dir)
	if err != nil {
		t.Fatalf("db init: %v\n%s", err, output)
	}
	if !strings.Contains(output, dbInitInventoryInstalledLine) {
		t.Fatalf("db init output does not say the tenant inventory is installed:\n%s", output)
	}
	if err := saveNodeModuleSelection(dir, []string{"compliance"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDBInitInventoryFirstStartChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "OLIVARES_CLI_TRAMPOLINE=", dbInitInventoryDirEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestDBInitInventoryFirstStartChild") {
		t.Fatalf("first start (its own process) = %v:\n%s", err, out)
	}
}

// TestDBInitInventoryFirstStartChild is the first start of
// TestDBInitInstallsTheTenantInventorySoTheFirstStartRunsEveryJob, in its own
// process, on the data directory db init saved. Alone it skips.
func TestDBInitInventoryFirstStartChild(t *testing.T) {
	dir := os.Getenv(dbInitInventoryDirEnv)
	if dir == "" {
		t.Skip("the first start of TestDBInitInstallsTheTenantInventorySoTheFirstStartRunsEveryJob")
	}
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatalf("first start after db init: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := eng.api.Handler()
	if code, _, raw := doDemoViewJSON(t, h, http.MethodGet, "/readyz", "", "", nil); code != http.StatusOK {
		t.Fatalf("readiness = %d %s, want 200", code, raw)
	}
	_, info, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/server-info", "", "", nil)
	if _, listed := info["jobs_not_running"]; listed {
		t.Fatalf("server-info after db init lists jobs not running: %s", raw)
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		t.Fatalf("audit checkpoints after db init: %v", err)
	}

	// The console's organization list (a system administrator's GET /v1/system/orgs)
	// answers from the same inventory: 200 with the setup org, not 501.
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	const orgName = "Olivares Inventory Fixture"
	if code, _, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "root@x.io", "password": "supersecret1", "organization": orgName,
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	code, login, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/auth/login", "", "", map[string]any{"email": "root@x.io", "password": "supersecret1"})
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	admin, _ := login["token"].(string)
	code, orgs, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/system/orgs", admin, "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/system/orgs after db init = %d %s, want 200", code, raw)
	}
	items, _ := orgs["items"].([]any)
	for _, item := range items {
		if org, _ := item.(map[string]any); org["name"] == orgName {
			return
		}
	}
	t.Fatalf("GET /v1/system/orgs after db init does not list the setup org: %s", raw)
}

// The install-only mode says what installing the inventory changes for the jobs
// that need it: they start with the next start of the engine. The directory-writer
// activation is not this command's next step, so its wording is not here.
func TestDBInitInstallOnlySaysRestartTheEngineToStartTheJobs(t *testing.T) {
	var out strings.Builder
	printInitResult(&out, store.PgProvisionSpec{InstallDirectoryInventory: true, Database: "olivares"},
		store.PgProvisionResult{DirectoryInventoryInstalled: true}, "")
	const want = "directory inventory installed and attested on \"olivares\": true; restart the engine to start these jobs: " +
		"retention, legal hold, audit checkpoints and archival\n"
	if out.String() != want {
		t.Fatalf("install-only output = %q, want %q", out.String(), want)
	}
}
