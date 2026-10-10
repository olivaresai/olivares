// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestModuleProfileLiveIngestSelectionCompatibility(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	// Reuse the provider protocol fixture; the readiness executable is not run.
	t.Setenv(envSessionClaudeBin, os.Args[0])
	t.Run("fresh-default-and-administrator-off", func(t *testing.T) {
		moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
			cfg.ApplyModuleProfile = true
			restarts := 0
			cfg.Restart = &selfRestart{cancel: func() { restarts++ }}
			eng := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
			if !eng.moduleProfile.Active("liveingest") || !slices.Contains(eng.moduleProfile.Selected(), "liveingest") {
				t.Errorf("fresh default does not explicitly select and run liveingest: %v", eng.moduleProfile.Selected())
			}
			e := &consentEstate{t: t, engine: cfg.Engine, eng: eng, h: eng.api.Handler()}
			e.setupRoot()
			e.tT = e.createOrg("liveingest-off")
			selected := slices.DeleteFunc(standardModuleSelection(), func(name string) bool { return name == "liveingest" })
			changed := e.do(http.MethodPut, "/v1/console/modules", e.admin, "", map[string]any{"selected": selected})
			if changed.code != http.StatusOK || changed.body["restarting"] != true || restarts != 1 {
				t.Fatalf("administrator OFF did not request one restart: %d %s, restarts=%d", changed.code, changed.raw, restarts)
			}
			_ = eng.Close()
			eng = reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
			e.eng, e.h = eng, eng.api.Handler()
			if got := moduleStatus(eng, "olivares.liveingest"); got != runtime.StatusDormant {
				t.Errorf("explicit OFF runtime = %q, want dormant", got)
			}
			if got := moduleStatus(eng, "olivares.sessions"); got != runtime.StatusRunning {
				t.Fatalf("native sessions runtime = %q, want running", got)
			}
			runner := &managedCostTurnRunner{}
			sessions.WithRunner(runner)(eng.sessionsMod)
			pep, err := buildClaudeHookPEPServer(eng, discardLog())
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(pep.Handler)
			t.Cleanup(server.Close)
			if err := eng.sessionHooks.bindEndpoint(server.Listener.Addr().String()); err != nil {
				t.Fatal(err)
			}
			profile := e.do(http.MethodPost, "/v1/m/sessions/provider-profiles", e.admin, e.tT, map[string]any{
				"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir(),
			})
			if profile.code != http.StatusCreated {
				t.Fatalf("provider profile = %d %s", profile.code, profile.raw)
			}
			run := e.do(http.MethodPost, "/v1/m/sessions/runs", e.admin, e.tT, map[string]any{
				"transport": "stream-json", "permission_mode": "plan", "isolation": "native", "provider_profile_ref": profile.body["profile_ref"],
			})
			if run.code != http.StatusCreated || runner.process == nil {
				t.Fatalf("native launch = %d %s", run.code, run.raw)
			}
			runRef := run.body["run_ref"].(string)
			t.Cleanup(func() { _ = runner.process.Stop(t.Context()) })
			runner.process.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"managed-cost-provider","result":"ok","num_turns":1,"total_cost_usd":0.000017,"usage":{"input_tokens":7,"output_tokens":3},"modelUsage":{"managed-cost-test":{"inputTokens":7,"outputTokens":3,"costUSD":0.000017}}}`)}
			deadline := time.Now().Add(5 * time.Second)
			for {
				live := e.do(http.MethodGet, "/v1/m/sessions/live", e.admin, e.tT, nil)
				items, _ := live.body["items"].([]any)
				if live.code == http.StatusOK && len(items) == 1 {
					row := items[0].(map[string]any)
					if row["run_ref"] == runRef && row["cost_micro_usd"] == float64(17) && row["input_tokens"] == float64(7) && row["output_tokens"] == float64(3) {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("native cost counters with liveingest OFF = %d %s", live.code, live.raw)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if stop := e.do(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", e.admin, e.tT, nil); stop.code != http.StatusOK {
				t.Fatalf("native stop = %d %s", stop.code, stop.raw)
			}
			_ = eng.Close()
			again := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
			if again.moduleProfile.Active("liveingest") {
				t.Error("administrator OFF was lost on a subsequent boot")
			}
		})
	})
	t.Run("26100-stored-data-upgrade", func(t *testing.T) {
		moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
			legacy := startModuleUpgradeEngine(t, cfg)
			admin, tenant := moduleUpgradeAdministrator(t, legacy)
			if code, _, raw := doDemoViewJSON(t, legacy.api.Handler(), http.MethodPost, "/v1/m/consoleviews/views", admin, tenant, map[string]any{
				"feature_id": "audit", "name": "before-upgrade", "params": map[string]any{},
			}); code != http.StatusCreated && code != http.StatusOK {
				t.Fatalf("legacy stored view = %d %s", code, raw)
			}
			_ = legacy.Close()
			cfg.ApplyModuleProfile = true
			eng := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
			if !eng.moduleProfile.Active("liveingest") || !slices.Contains(recordedSelection(t, newProductSettings(eng.store, eng.dataDir)), "liveingest") {
				t.Fatal("26.10.0 stored-data upgrade turned liveingest OFF")
			}
			if code, body, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, "/v1/m/consoleviews/views", admin, tenant, nil); code != http.StatusOK || len(body["items"].([]any)) != 1 {
				t.Fatalf("stored view after upgrade = %d %s", code, raw)
			}
		})
	})
	t.Run("administrator-off-during-legacy-upgrade", func(t *testing.T) {
		for _, source := range []string{"node", "record"} {
			t.Run(source, func(t *testing.T) {
				moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
					eng := startModuleUpgradeEngine(t, cfg)
					p := newProductSettings(eng.store, eng.dataDir)
					legacy := moduleSelectionDoc{Version: "olivares.module.profile.v1", Selected: []string{"eventing"}}
					if source == "record" {
						if err := p.update(t.Context(), operator(t), "deployment.settings.modules", nil, func(doc *productSettingsDoc) { doc.Modules = &legacy }); err != nil {
							t.Fatal(err)
						}
					} else {
						doc, _, err := p.load(t.Context())
						if err != nil || doc.Modules != nil {
							t.Fatalf("legacy node fixture already has stored Modules: %v", err)
						}
						raw, err := json.Marshal(legacy)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(moduleProfilePath(cfg.DataDir), raw, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					// Another node records OFF after reconciliation reads the old state,
					// before import or upgrade writes. Use the existing clock seam.
					p.now = func() time.Time {
						p.now = time.Now
						if err := p.writeModules(t.Context(), operator(t), "deployment.settings.modules", []string{}); err != nil {
							t.Fatal(err)
						}
						return time.Now()
					}
					if _, err := p.reconcileModules(t.Context(), moduleReconcile{booted: eng.moduleProfile, used: usedReturns()}, discardLogger()); err != nil {
						t.Fatal(err)
					}
					if got := recordedSelection(t, p); len(got) != 0 {
						t.Fatalf("legacy %s reconciliation overwrote the administrator's newer OFF selection: %v", source, got)
					}
					if node, found, err := loadNodeModuleDocument(cfg.DataDir); err != nil || !found || node.Version != moduleProfileVersion || len(node.Selected) != 0 {
						t.Fatalf("newer OFF was not copied to the node: found=%v node=%+v err=%v", found, node, err)
					}
				})
			})
		}
	})

	for _, source := range []string{"node", "record"} {
		for _, version := range []string{"", "olivares.module.profile.v1"} {
			for _, selected := range [][]string{{}, {"eventing"}} {
				t.Run(source+"/"+version+"/"+fmt.Sprint(selected), func(t *testing.T) {
					moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
						legacy := startModuleUpgradeEngine(t, cfg)
						p := newProductSettings(legacy.store, legacy.dataDir)
						doc := moduleSelectionDoc{Version: version, Selected: selected}
						if source == "record" {
							if err := p.update(t.Context(), operator(t), "deployment.settings.modules", nil, func(p *productSettingsDoc) { p.Modules = &doc }); err != nil {
								t.Fatal(err)
							}
						} else {
							raw, err := json.Marshal(doc)
							if err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(moduleProfilePath(cfg.DataDir), raw, 0o600); err != nil {
								t.Fatal(err)
							}
						}
						_ = legacy.Close()
						cfg.ApplyModuleProfile = true
						eng := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
						if !eng.moduleProfile.Active("liveingest") || !slices.Contains(recordedSelection(t, newProductSettings(eng.store, eng.dataDir)), "liveingest") {
							t.Error("legacy selection did not preserve liveingest ON")
						}
						if node, found, err := loadNodeModuleDocument(cfg.DataDir); err != nil || !found || node.Version != "olivares.module.profile.v2" {
							t.Fatalf("legacy node was not upgraded: found=%v version=%q err=%v", found, node.Version, err)
						}
						_ = eng.Close()
						again := reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
						if !again.moduleProfile.Active("liveingest") {
							t.Error("legacy ON was lost on a subsequent boot")
						}
					})
				})
			}
		}
	}
}
