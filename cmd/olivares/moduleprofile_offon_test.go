// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/modulespec"
	"github.com/olivaresai/olivares/core/runtime"
)

// Every catalog module of the spec turns off and back on through the console's
// module selection and the restart it requests, on one installation's data:
//
//   - off: after the restart the engine is ready, nothing failed, every other
//     module still answers its reads, and the module and every module that
//     requires it are dormant and answer each of their routes with
//     module_not_enabled;
//   - on: after the next restart they run again with the routes they had and
//     answer their reads (an event consumer such as liveingest has no routes;
//     its runtime proves it).
//
// The kernel is not in the table: it runs whatever the selection says.
func TestEveryCatalogModuleTurnsOffAndOnAcrossARestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	moduleUpgradeBackends(t, func(t *testing.T, cfg bootConfig, _ enginetest.DSNs) {
		cfg.ApplyModuleProfile = true
		restarts := 0
		cfg.Restart = &selfRestart{cancel: func() { restarts++ }}
		all := allModuleSelection()
		if err := saveNodeModuleSelection(cfg.DataDir, all, time.Now()); err != nil {
			t.Fatal(err)
		}
		e := &consentEstate{t: t, engine: cfg.Engine}
		e.eng = reconcileModuleUpgradeEngine(t, cfg, startModuleUpgradeEngine(t, cfg))
		e.h = e.eng.api.Handler()
		// Each restarted engine outlives the row that started it, like a served
		// one: it runs on this test's context and the last one closes with it.
		ctx := t.Context()
		t.Cleanup(func() { _ = e.eng.Close() })
		e.setupRoot()
		tenant := e.createOrg("module-off-on")
		baseline := mountedModuleRoutes(t, e.h)
		assertEngineHealthy(t, e)

		// selectAndRestart records selected as an administrator does and serves
		// the engine the requested restart starts on the same data.
		selectAndRestart := func(t *testing.T, selected []string) {
			t.Helper()
			before := restarts
			r := e.do(http.MethodPut, "/v1/console/modules", e.admin, "", map[string]any{"selected": selected})
			if r.code != http.StatusOK || r.body["restarting"] != true || restarts != before+1 {
				t.Fatalf("select %v = %d %s, restarts %d -> %d; want 200, restarting, one restart", selected, r.code, r.raw, before, restarts)
			}
			if err := e.eng.Close(); err != nil {
				t.Fatalf("stop for the restart: %v", err)
			}
			eng, err := boot(ctx, cfg)
			if err != nil {
				t.Fatalf("boot after the restart: %v", err)
			}
			e.eng, e.h = eng, eng.api.Handler()
			// The selection was copied to this node before the restart, so the
			// reconcile serve runs before listening finds nothing left to restart.
			mods := &moduleReconcile{booted: eng.moduleProfile, used: func(ctx context.Context) ([]string, error) {
				return usedModules(ctx, eng.store, eng.census)
			}}
			if err := reconcileSettings(ctx, newProductSettings(eng.store, eng.dataDir), mods, discardLogger()); err != nil {
				t.Fatalf("reconcile after the restart: %v", err)
			}
			assertEngineHealthy(t, e)
		}

		rows := 0
		for _, spec := range modulespec.All() {
			if !spec.Selectable || spec.Kind != "catalog" {
				continue
			}
			rows++
			t.Run(spec.Namespace, func(t *testing.T) {
				off := modulesOffWithout(t, spec.Namespace)

				selectAndRestart(t, slices.DeleteFunc(slices.Clone(all), func(n string) bool { return slices.Contains(off, n) }))
				if got := notEnabledOf(t, e); !slices.Equal(got, off) {
					t.Fatalf("modules_not_enabled = %v, want %v", got, off)
				}
				// What still runs keeps serving without what was turned off.
				var running []string
				for ns := range baseline {
					if !slices.Contains(off, ns) {
						running = append(running, ns)
					}
				}
				slices.Sort(running)
				assertModulesServe(t, e, tenant.String(), baseline, running)
				mounted := mountedModuleRoutes(t, e.h)
				for _, ns := range off {
					if got := moduleStatus(e.eng, moduleSpec(t, ns).Name); got != runtime.StatusDormant {
						t.Errorf("%s runtime = %q, want dormant", ns, got)
					}
					if len(mounted[ns]) != 0 {
						t.Errorf("%s still mounts %v", ns, mounted[ns])
					}
					// A module without routes of its own (an event consumer) still
					// answers its namespace.
					for _, route := range append([]string{http.MethodGet + " /v1/m/" + ns}, baseline[ns]...) {
						method, path, _ := strings.Cut(route, " ")
						r := e.do(method, concretePath(path), e.admin, tenant, nil)
						errBody, _ := r.body["error"].(map[string]any)
						if r.code != http.StatusNotFound || errBody["code"] != "module_not_enabled" || errBody["module"] != ns {
							t.Errorf("%s = %d %s, want 404 module_not_enabled for %s", route, r.code, r.raw, ns)
						}
					}
				}

				selectAndRestart(t, all)
				if got := notEnabledOf(t, e); len(got) != 0 {
					t.Fatalf("modules_not_enabled = %v after selecting every module", got)
				}
				mounted = mountedModuleRoutes(t, e.h)
				for _, ns := range off {
					if got := moduleStatus(e.eng, moduleSpec(t, ns).Name); got != runtime.StatusRunning {
						t.Errorf("%s runtime = %q, want running", ns, got)
					}
					if !slices.Equal(mounted[ns], baseline[ns]) {
						t.Errorf("%s mounts %v, want %v", ns, mounted[ns], baseline[ns])
					}
				}
				assertModulesServe(t, e, tenant.String(), baseline, off)
			})
		}
		if rows == 0 {
			t.Fatal("the module spec has no catalog module to turn off")
		}
	})
}

// modulesOffWithout is name plus every catalog module that cannot run without
// it: the modules whose own selection activates name.
func modulesOffWithout(t *testing.T, name string) []string {
	t.Helper()
	var off []string
	for _, other := range allModuleSelection() {
		p, err := resolveModuleProfile([]string{other})
		if err != nil {
			t.Fatal(err)
		}
		if p.Active(name) {
			off = append(off, other)
		}
	}
	return off
}

func moduleSpec(t *testing.T, namespace string) modulespec.Spec {
	t.Helper()
	spec, ok := moduleCatalog[namespace]
	if !ok {
		t.Fatalf("%s is not in the module catalog", namespace)
	}
	return spec
}

// mountedModuleRoutes maps each namespace to its mounted "METHOD /v1/m/ns/..."
// routes, sorted, read off the live router. The catch-all that answers for a
// module this node does not run is not one of its routes.
func mountedModuleRoutes(t *testing.T, h http.Handler) map[string][]string {
	t.Helper()
	router, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	out := map[string][]string{}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		rest, ok := strings.CutPrefix(route, "/v1/m/")
		if !ok {
			return nil
		}
		ns, sub, nested := strings.Cut(rest, "/")
		if !nested || sub == "*" {
			return nil
		}
		out[ns] = append(out[ns], method+" "+strings.TrimSuffix(route, "/"))
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	for ns := range out {
		slices.Sort(out[ns])
		out[ns] = slices.Compact(out[ns])
	}
	return out
}

var routeParam = regexp.MustCompile(`\{[^}]*\}`)

// concretePath fills each path parameter so the route can be requested.
func concretePath(pattern string) string { return routeParam.ReplaceAllString(pattern, "x") }

func notEnabledOf(t *testing.T, e *consentEstate) []string {
	t.Helper()
	r := e.do(http.MethodGet, "/v1/server-info", "", "", nil)
	if r.code != http.StatusOK {
		t.Fatalf("server-info = %d %s", r.code, r.raw)
	}
	var out []string
	list, _ := r.body["modules_not_enabled"].([]any)
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out
}

// assertEngineHealthy: the restarted engine is ready and no component failed.
func assertEngineHealthy(t *testing.T, e *consentEstate) {
	t.Helper()
	if r := e.do(http.MethodGet, "/readyz", "", "", nil); r.code != http.StatusOK {
		t.Fatalf("readyz = %d %s", r.code, r.raw)
	}
	for _, cs := range e.eng.rt.Status() {
		if cs.Status == runtime.StatusFailed {
			t.Errorf("component %s failed after the restart", cs.Name)
		}
	}
}

// assertModulesServe requests each namespace's parameterless GET routes as the
// administrator. A route fails when it is not the module that answers (module
// not enabled, no route), when the administrator is refused, or on a server
// error other than 501 or 503, a module's explicit deny-closed answer for a
// capability this build does not wire. A module with such routes must answer at
// least one of them.
func assertModulesServe(t *testing.T, e *consentEstate, tenant string, routes map[string][]string, namespaces []string) {
	t.Helper()
	for _, ns := range namespaces {
		reads, answered := 0, 0
		for _, route := range routes[ns] {
			path, ok := strings.CutPrefix(route, http.MethodGet+" ")
			if !ok || strings.Contains(path, "{") {
				continue
			}
			reads++
			code, errCode := getAsAdmin(t, e, tenant, path)
			switch {
			case errCode == "module_not_enabled" || errCode == "not_found" ||
				code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusMethodNotAllowed ||
				code >= http.StatusInternalServerError && code != http.StatusNotImplemented && code != http.StatusServiceUnavailable:
				t.Errorf("%s = %d %s, want the %s module's answer", route, code, errCode, ns)
			case code < http.StatusMultipleChoices || code == http.StatusNotImplemented || code == http.StatusServiceUnavailable:
				answered++
			}
		}
		if reads > 0 && answered == 0 {
			t.Errorf("%s answered none of its %d reads", ns, reads)
		}
	}
}

// getAsAdmin answers the status and error code of one administrator read. A
// stream is cut when it first flushes: its status is then written. Any other
// read runs to completion; one that answers nothing within the bound fails.
func getAsAdmin(t *testing.T, e *consentEstate, tenant, path string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.RemoteAddr = "10.0.0.2:4321"
	req.Header.Set("Authorization", "Bearer "+e.admin)
	req.Header.Set("X-Olivares-Tenant", tenant)
	rec := cutAtFlush{httptest.NewRecorder(), cancel}
	e.h.ServeHTTP(rec, req)
	if !rec.Flushed && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("GET %s answered nothing within the bound", path)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body) // a stream or an export is not JSON
	return rec.Code, body.Error.Code
}

type cutAtFlush struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cutAtFlush) Flush() {
	w.ResponseRecorder.Flush()
	w.cancel()
}
