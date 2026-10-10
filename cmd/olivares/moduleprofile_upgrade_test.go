// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func moduleUpgradeBackends(t *testing.T, run func(*testing.T, bootConfig, enginetest.DSNs)) {
	t.Helper()
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			cfg := bootConfig{DataDir: t.TempDir(), Engine: backend, Version: version, Logger: discardLogger()}
			var pg enginetest.DSNs
			if backend == "postgres" {
				pg = enginetest.IsolatedPostgresSplitOwner(t)
				cfg.DSN, cfg.OwnerDSN = pg.App, pg.Owner
			}
			run(t, cfg, pg)
		})
	}
}

func startModuleUpgradeEngine(t *testing.T, cfg bootConfig) *engine {
	t.Helper()
	eng, err := boot(t.Context(), cfg)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// Run the same reconciliation and single restart that serve performs before
// opening a listener, so the assertion observes the modules actually served.
func reconcileModuleUpgradeEngine(t *testing.T, cfg bootConfig, eng *engine) *engine {
	t.Helper()
	for attempt := 0; attempt < 2; attempt++ {
		err := reconcileBeforeServing(t.Context(), eng, discardLogger())
		if err == nil {
			return eng
		}
		var restart *selfRestartError
		if !errors.As(err, &restart) || attempt == 1 {
			t.Fatalf("reconcile before serving: %v", err)
		}
		_ = eng.Close()
		eng = startModuleUpgradeEngine(t, cfg)
	}
	panic("unreachable")
}

func moduleUpgradeAdministrator(t *testing.T, eng *engine) (string, string) {
	t.Helper()
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	h := eng.api.Handler()
	code, setup, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "module-upgrade@olivares.ai", "password": "fixture-password1",
	})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	org := setup["organization"].(map[string]any)
	code, login, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": "module-upgrade@olivares.ai", "password": "fixture-password1",
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	return login["token"].(string), org["tenant_id"].(string)
}

func assertEmptyRedteamRoutes(t *testing.T, eng *engine, admin, tenant string, want int) {
	t.Helper()
	if want == http.StatusOK && thisEdition.name == "community" {
		want = http.StatusNotImplemented
	}
	for _, path := range []string{"/v1/m/redteam/targets", "/v1/m/redteam/runs", "/v1/m/redteam/catalog"} {
		if code, _, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, path, admin, tenant, nil); code != want {
			t.Errorf("%s = %d: %s, want %d", path, code, raw, want)
		}
	}
}

func assertModuleSelectionImportedOnce(t *testing.T, eng *engine) {
	t.Helper()
	var imports int
	err := eng.store.Custody(t.Context(), model.SystemTenantID, func(sc store.CustodyScope) error {
		return sc.Audit().Walk(t.Context(), 1, func(ev model.AuditEvent) error {
			if ev.Action == "deployment.settings.modules.import" {
				imports++
			}
			return nil
		})
	})
	if err != nil || imports != 1 {
		t.Fatalf("module selection import events = %d (err %v), want exactly one", imports, err)
	}
}

// COMPATB module-case-b001: 26.1000 booted every module but wrote no module
// profile. Redteam has no persisted targets or runs, yet its routes must remain
// available after the first candidate serving boot, including its reconciliation.
func TestCOMPATB26_10_0ModuleProfileUpgradeKeepsEmptyRedteam(t *testing.T) {
	moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
		published := startModuleUpgradeEngine(t, cfg) // legacy all-module boot
		admin, tenant := moduleUpgradeAdministrator(t, published)
		assertEmptyRedteamRoutes(t, published, admin, tenant, http.StatusOK)
		if _, err := os.Stat(moduleProfilePath(cfg.DataDir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy fixture module profile exists or cannot be inspected: %v", err)
		}
		_ = published.Close()

		cfg.ApplyModuleProfile = true
		candidate := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		assertEmptyRedteamRoutes(t, candidate, admin, tenant, http.StatusOK)
		assertModuleSelectionImportedOnce(t, candidate)
		if serverInfoHidesPreviews(t, candidate) {
			t.Error("a 26.1000 upgrade's console navigation hides the pages it listed")
		}
		p := newProductSettings(candidate.store, candidate.dataDir)
		// The published selection is fixed; later catalog additions stay opt-in.
		want := []string{
			"accessmap", "adoption", "capabilities", "catalog", "claude-agents",
			"claude-policy", "compliance", "consoleviews", "deploy", "evals", "eventing",
			"finops", "gitpublish", "governance", "health", "identity", "inferenceproxy",
			"inventory", "knowledge", "liveingest", "models", "notify", "observability",
			"orchestration", "posture", "recording", "redteam", "reporting", "sandbox",
			"security", "sessions", "siemforward", "sourcescope", "voice",
		}
		if got := recordedSelection(t, p); !slices.Equal(got, want) {
			t.Errorf("upgrade selection = %v, want published selection %v", got, want)
		}
		nodeBefore, err := os.ReadFile(moduleProfilePath(cfg.DataDir))
		if err != nil {
			t.Fatal(err)
		}
		_ = candidate.Close()
		again := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		assertEmptyRedteamRoutes(t, again, admin, tenant, http.StatusOK)
		assertModuleSelectionImportedOnce(t, again)
		nodeAfter, err := os.ReadFile(moduleProfilePath(cfg.DataDir))
		if err != nil || string(nodeAfter) != string(nodeBefore) {
			t.Fatalf("second boot rewrote the imported node profile: %v", err)
		}
	})
}

// serverInfoHidesPreviews reads server-info's previews_hidden: whether this
// installation's console navigation lists the first job only.
func serverInfoHidesPreviews(t *testing.T, eng *engine) bool {
	t.Helper()
	code, info, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, "/v1/server-info", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("server-info = %d: %s", code, raw)
	}
	hidden, _ := info["previews_hidden"].(bool)
	return hidden
}

// A new installation's console navigation lists the first job only, across
// restarts. An installation whose deployment settings an earlier release
// recorded keeps listing every page (the 26.1000 upgrade is checked in
// TestCOMPATB26_10_0ModuleProfileUpgradeKeepsEmptyRedteam).
func TestNewInstallationHidesPreviewsAndAnUpgradeKeepsThem(t *testing.T) {
	moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
		cfg.ApplyModuleProfile = true
		eng := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		if !serverInfoHidesPreviews(t, eng) {
			t.Fatal("a new installation's console navigation lists its preview pages")
		}
		_ = eng.Close()
		eng = reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		if !serverInfoHidesPreviews(t, eng) {
			t.Fatal("a restart lists the preview pages of a new installation")
		}

		// The record as a release without the field wrote it: the same document
		// with no previews_hidden.
		err := eng.store.AuthMutate(t.Context(), func(as store.AuthScope) error {
			rows, _, err := as.DeploymentSettings().List(t.Context(), model.Query{Filters: []model.Filter{}})
			if err != nil || len(rows) != 1 {
				return fmt.Errorf("deployment settings rows = %d: %v", len(rows), err)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(rows[0].Doc), &doc); err != nil {
				return err
			}
			delete(doc, "previews_hidden")
			raw, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			rows[0].Doc = string(raw)
			_, err = as.DeploymentSettings().Update(t.Context(), rows[0])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = eng.Close()
		eng = reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		if serverInfoHidesPreviews(t, eng) {
			t.Fatal("an upgraded installation's console navigation hides the pages it listed")
		}
	})
}

// An administrator's own module selection ends the first run: from the next
// start the console lists every page.
func TestAnAdministratorsModuleSelectionListsEveryPage(t *testing.T) {
	moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
		cfg.ApplyModuleProfile = true
		eng := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		if !serverInfoHidesPreviews(t, eng) {
			t.Fatal("a new installation's console navigation lists its preview pages")
		}
		p := newProductSettings(eng.store, eng.dataDir)
		if err := p.writeModules(t.Context(), operator(t), "deployment.settings.modules", standardModuleSelection()); err != nil {
			t.Fatal(err)
		}
		_ = eng.Close()
		eng = reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		if serverInfoHidesPreviews(t, eng) {
			t.Fatal("after an administrator chose the modules, the console still lists the first job only")
		}
	})
}

func TestModuleProfileFreshStoreKeepsStandardDefaults(t *testing.T) {
	moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
		cfg.ApplyModuleProfile = true
		eng := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		admin, tenant := moduleUpgradeAdministrator(t, eng)
		assertEmptyRedteamRoutes(t, eng, admin, tenant, http.StatusNotFound)
		if got := recordedSelection(t, newProductSettings(eng.store, eng.dataDir)); !slices.Equal(got, standardModuleSelection()) {
			t.Fatalf("fresh store selection = %v, want standard defaults", got)
		}
	})
}

// SR3-MP-01/02: SYSTEM can be written before serve imports settings, and an
// unreadable node copy must still be repaired. Exercise both recovery paths on
// each backend, including an interrupted demo whose seeded modules must survive.
func TestModuleProfileFreshInitializationRecovery(t *testing.T) {
	for _, fault := range []string{"interrupted", "malformed", "interrupted-demo"} {
		t.Run(fault, func(t *testing.T) {
			moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, pg enginetest.DSNs) {
				cfg.ApplyModuleProfile = true
				cfg.DemoSeed = fault == "interrupted-demo"
				if cfg.DemoSeed && cfg.Engine == "postgres" {
					// Demo seeding enumerates tenants. Provision the closed inventory
					// after schema migration, as db init does, without an admin pool.
					provision := cfg
					provision.DemoSeed = false
					_ = startModuleUpgradeEngine(t, provision).Close()
					roleOf := func(dsn string) string {
						u, err := url.Parse(dsn)
						if err != nil || u.User == nil {
							t.Fatal("fixture DSN is not a URL with a user")
						}
						return u.User.Username()
					}
					spec := store.PgProvisionSpec{Database: pg.Database, App: store.PgRole{Name: roleOf(pg.App)}, Owner: store.PgRole{Name: roleOf(pg.Owner)}}
					if err := installTenantInventory(t.Context(), pg.Superuser, spec); err != nil {
						t.Fatalf("install the demo fixture's closed tenant inventory: %v", err)
					}
				}
				if fault == "malformed" {
					if err := os.WriteFile(moduleProfilePath(cfg.DataDir), []byte(`{"selected":[`), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				first := startModuleUpgradeEngine(t, cfg)
				var seeded []string
				if cfg.DemoSeed {
					var err error
					seeded, err = usedModules(t.Context(), first.store, first.census)
					if err != nil || len(seeded) == 0 {
						t.Fatalf("demo fixture modules holding data = %v (err %v)", seeded, err)
					}
				}
				if fault != "malformed" {
					_ = first.Close() // before the first settings reconciliation
					first = startModuleUpgradeEngine(t, cfg)
				}
				eng := reconcileModuleUpgradeEngine(t, cfg, first)
				admin, tenant := moduleUpgradeAdministrator(t, eng)
				assertEmptyRedteamRoutes(t, eng, admin, tenant, http.StatusNotFound)
				if !cfg.DemoSeed {
					if got := recordedSelection(t, newProductSettings(eng.store, eng.dataDir)); !slices.Equal(got, standardModuleSelection()) {
						t.Fatalf("recovered fresh selection = %v, want standard defaults", got)
					}
				} else {
					for _, name := range seeded {
						if !eng.moduleProfile.Active(name) {
							t.Errorf("interrupted demo stopped serving seeded module %s", name)
						}
					}
				}
				node, found, err := loadNodeModuleDocument(cfg.DataDir)
				if err != nil || !found || node.ImportPending {
					t.Fatalf("reconciled profile did not finish its bootstrap: found=%v pending=%v err=%v", found, node.ImportPending, err)
				}
				assertModuleSelectionImportedOnce(t, eng)
				// Still a new installation, whose console lists the first job only;
				// the seeded demo lists the pages it shows.
				if got := serverInfoHidesPreviews(t, eng); got != !cfg.DemoSeed {
					t.Fatalf("server-info previews_hidden = %v, want %v", got, !cfg.DemoSeed)
				}
			})
		})
	}
}
