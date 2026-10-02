// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	setupInventoryDirEnv   = "OLIVARES_TEST_SETUP_INVENTORY_DIR"
	setupInventoryAppEnv   = "OLIVARES_TEST_SETUP_INVENTORY_APP"
	setupInventoryOwnerEnv = "OLIVARES_TEST_SETUP_INVENTORY_OWNER"
)

// setup provisions the roles it asked for and, as db init does, applies the engine's
// schema and installs the tenant inventory. The first start on the application and
// owner roles alone (no admin connection) then runs every job that must cover every
// tenant. The start runs in its own process, as the engine's restart re-executes its
// binary.
func TestSetupProvisionInstallsTheTenantInventory(t *testing.T) {
	maintenance := pgMaintenanceForTest(t, "ARCH_SETUP_INVENTORY_MAINTENANCE")
	u, err := url.Parse(maintenance)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(suffix[:])
	db, app, owner, admin := "olv_setup_"+id, "olv_setup_app_"+id, "olv_setup_owner_"+id, "olv_setup_admin_"+id
	answers := []string{
		"3", u.Hostname(), u.Port(), db, "disable",
		app, "setup-app-fixture", owner, "setup-owner-fixture", admin, "setup-admin-fixture",
		"y", maintenance,
		"", "", "", "/tls.crt", "/tls.key", "/ca.crt", "/audit.key", "",
	}
	p, out := scriptedPrompter(answers)
	if _, err := buildPlanInteractive(dummyCmd(), p, t.TempDir()); err != nil || p.err != nil {
		t.Fatalf("setup = %v, prompter %v\n%s", err, p.err, out)
	}
	if !strings.Contains(out.String(), dbInitInventoryInstalledLine) {
		t.Fatalf("setup does not say the tenant inventory is installed:\n%s", out)
	}
	dsn := func(role, pw string) string {
		return (&url.URL{Scheme: "postgres", User: url.UserPassword(role, pw), Host: u.Host, Path: "/" + db, RawQuery: "sslmode=disable"}).String()
	}
	dir := t.TempDir()
	if err := saveNodeModuleSelection(dir, []string{"compliance"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupInventoryFirstStartChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "OLIVARES_CLI_TRAMPOLINE=", setupInventoryDirEnv+"="+dir,
		setupInventoryAppEnv+"="+dsn(app, "setup-app-fixture"), setupInventoryOwnerEnv+"="+dsn(owner, "setup-owner-fixture"))
	child, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(child), "--- PASS: TestSetupInventoryFirstStartChild") {
		t.Fatalf("first start (its own process) = %v:\n%s", err, child)
	}
}

// TestSetupInventoryFirstStartChild is the first start of
// TestSetupProvisionInstallsTheTenantInventory, in its own process, on the
// application and owner roles setup provisioned. Alone it skips.
func TestSetupInventoryFirstStartChild(t *testing.T) {
	dir := os.Getenv(setupInventoryDirEnv)
	if dir == "" {
		t.Skip("the first start of TestSetupProvisionInstallsTheTenantInventory")
	}
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: filepath.Clean(dir), Engine: "postgres", DSN: os.Getenv(setupInventoryAppEnv),
		OwnerDSN: os.Getenv(setupInventoryOwnerEnv), Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatalf("first start after setup: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	_, info, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, "/v1/server-info", "", "", nil)
	if _, listed := info["jobs_not_running"]; listed {
		t.Fatalf("server-info after setup lists jobs not running: %s", raw)
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		t.Fatalf("audit checkpoints after setup: %v", err)
	}
}
