// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	inventoryJourneyPhaseEnv = "OLIVARES_TEST_INVENTORY_JOURNEY_PHASE"
	inventoryJourneyDirEnv   = "OLIVARES_TEST_INVENTORY_JOURNEY_DIR"
	inventoryJourneyAppEnv   = "OLIVARES_TEST_INVENTORY_JOURNEY_APP"
	inventoryJourneyOwnerEnv = "OLIVARES_TEST_INVENTORY_JOURNEY_OWNER"
)

// The upgrade journey of a default PostgreSQL installation (application and owner
// roles, no administration role) that runs compliance:
//  1. without the tenant inventory, server-info lists retention as not running and
//     the audit checkpoints refuse to claim every tenant;
//  2. the one-time install (what db init --install-directory-inventory does);
//  3. after the restart, nothing is listed and the checkpoints anchor every tenant,
//     a suspended one and SYSTEM included.
//
// Each start runs in its own process (TestTenantInventoryJourneyChild), as the
// engine's restart re-executes its binary.
func TestTenantInventoryUpgradeJourneyOnDefaultPostgres(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	dir := t.TempDir()
	if err := saveNodeModuleSelection(dir, []string{"compliance"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	start := func(phase string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestTenantInventoryJourneyChild$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "OLIVARES_CLI_TRAMPOLINE=",
			inventoryJourneyPhaseEnv+"="+phase, inventoryJourneyDirEnv+"="+dir,
			inventoryJourneyAppEnv+"="+pg.App, inventoryJourneyOwnerEnv+"="+pg.Owner)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestTenantInventoryJourneyChild") {
			t.Fatalf("start %q (its own process) = %v:\n%s", phase, err, out)
		}
	}
	start("before-install")

	roleOf := func(dsn string) string {
		u, err := url.Parse(dsn)
		if err != nil || u.User == nil {
			t.Fatal("fixture DSN is not a URL with a user")
		}
		return u.User.Username()
	}
	spec := store.PgProvisionSpec{Database: pg.Database, App: store.PgRole{Name: roleOf(pg.App)}, Owner: store.PgRole{Name: roleOf(pg.Owner)}}
	if err := installTenantInventory(context.Background(), pg.Superuser, spec); err != nil {
		t.Fatalf("install the tenant inventory on the existing database: %v", err)
	}

	start("after-install")
}

// TestTenantInventoryJourneyChild is one start of TestTenantInventoryUpgradeJourneyOnDefaultPostgres,
// in its own process. Alone it skips.
func TestTenantInventoryJourneyChild(t *testing.T) {
	phase, dir := os.Getenv(inventoryJourneyPhaseEnv), os.Getenv(inventoryJourneyDirEnv)
	if phase == "" || dir == "" {
		t.Skip("a start of TestTenantInventoryUpgradeJourneyOnDefaultPostgres")
	}
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "postgres", DSN: os.Getenv(inventoryJourneyAppEnv), OwnerDSN: os.Getenv(inventoryJourneyOwnerEnv),
		Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatalf("start %s: %v", phase, err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	_, info, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, "/v1/server-info", "", "", nil)
	jobs, _ := info["jobs_not_running"].([]any)
	tenantsFile := filepath.Join(dir, "inventory-journey-tenants")

	switch phase {
	case "before-install":
		var active, suspended model.TenantID
		if err := eng.store.System(ctx, func(sys store.SystemScope) error {
			if _, err := sys.EnsureSystemTenant(ctx); err != nil {
				return err
			}
			a, err := sys.CreateOrg(ctx, model.Org{Name: "journey-active", Slug: "journey-active", Status: model.StatusActive})
			if err != nil {
				return err
			}
			b, err := sys.CreateOrg(ctx, model.Org{Name: "journey-suspended", Slug: "journey-suspended", Status: model.StatusActive})
			if err != nil {
				return err
			}
			if _, err := sys.SetOrgStatus(ctx, b.TenantID, model.StatusSuspended); err != nil {
				return err
			}
			active, suspended = a.TenantID, b.TenantID
			return nil
		}); err != nil {
			t.Fatalf("provision the tenants: %v", err)
		}
		if err := os.WriteFile(tenantsFile, []byte(active.String()+"\n"+suspended.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		listed := false
		for _, j := range jobs {
			if job, _ := j.(map[string]any); job["job"] == "retention" && job["reason"] == "no_tenant_inventory" {
				listed = true
			}
		}
		if !listed {
			t.Fatalf("server-info without the inventory = %s, want retention listed with no_tenant_inventory", raw)
		}
		if err := eng.signer.CheckpointAll(ctx, eng.store); !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
			t.Fatalf("checkpoints without the inventory = %v, want the non-authoritative refusal", err)
		}
	case "after-install":
		if len(jobs) != 0 {
			t.Fatalf("server-info with the inventory lists jobs not running: %s", raw)
		}
		ids, err := os.ReadFile(tenantsFile)
		if err != nil {
			t.Fatal(err)
		}
		tenants := []model.TenantID{model.SystemTenantID}
		for _, id := range strings.Split(string(ids), "\n") {
			tenants = append(tenants, model.TenantID(id))
		}
		headSeq := func(tenant model.TenantID) int64 {
			t.Helper()
			var head store.HeadRef
			if err := eng.store.Custody(ctx, tenant, func(sc store.CustodyScope) error {
				var err error
				head, _, err = sc.Audit().Head(ctx)
				return err
			}); err != nil {
				t.Fatalf("read the chain head of tenant %s: %v", tenant, err)
			}
			return head.Seq
		}
		before := map[model.TenantID]int64{}
		for _, tenant := range tenants {
			before[tenant] = headSeq(tenant)
		}
		if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
			t.Fatalf("checkpoints with the inventory: %v", err)
		}
		// One checkpoint event appended to every chain, the suspended tenant's included.
		for _, tenant := range tenants {
			if got := headSeq(tenant); got != before[tenant]+1 {
				t.Fatalf("chain of tenant %s went from seq %d to %d, want one checkpoint", tenant, before[tenant], got)
			}
		}
	default:
		t.Fatalf("unknown phase %q", phase)
	}
}
