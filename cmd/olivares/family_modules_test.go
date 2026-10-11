// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// withAddonModules makes the edition seam give each add-on of byAddon its
// modules for the test, as the commercial build does from its catalog.
func withAddonModules(t *testing.T, byAddon map[string][]string) {
	t.Helper()
	prev := thisEdition.activationModules
	thisEdition.activationModules = func(addon string) []string { return byAddon[addon] }
	t.Cleanup(func() {
		thisEdition.activationModules = prev
		setActivationOverlayForTest(nil)
	})
}

// The modules an active add-on names run with their requirements, without being
// selected; a staged add-on (pending, or without a config to overlay) runs none,
// and a name the catalog does not have is ignored.
func TestAnActiveAddonRunsItsModules(t *testing.T) {
	withAddonModules(t, map[string][]string{"reporting": {"reporting", "no-such-module"}, "rtbf-depth": {"compliance"}})

	p, err := resolveModuleProfileWith(standardModuleSelection(), activationModules(activationWith(reportingActive, rtbfNeedsSecret)))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Active("reporting") || !p.Active("compliance") {
		t.Fatalf("active add-on reporting: running %v, want reporting and its requirement compliance", p.ActiveNames())
	}
	if slices.Contains(p.Selected(), "reporting") {
		t.Fatalf("selection = %v: an add-on's module is not selected", p.Selected())
	}
	if got := p.ActivatedBy("reporting"); !slices.Equal(got, []string{"reporting"}) {
		t.Fatalf("reporting activated by %v, want [reporting]", got)
	}
	if got := p.ActivatedBy("compliance"); got != nil {
		t.Fatalf("compliance activated by %v: the pending rtbf-depth runs nothing", got)
	}

	noConfig := reportingActive
	noConfig.Value = ""
	staged, _ := resolveModuleProfileWith(standardModuleSelection(), activationModules(activationWith(noConfig)))
	if staged.Active("reporting") {
		t.Fatal("an active entry without a config to overlay runs its modules")
	}
}

// serveStart builds an engine on dir and reconciles it with the deployment
// settings, as `olivares serve` does before any listener starts. A
// selfRestartError is returned as is.
func serveStart(t *testing.T, dir string) (*engine, *productSettings, error) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: version, ApplyModuleProfile: true, Logger: log})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	mods := &moduleReconcile{booted: eng.moduleProfile, used: func(ctx context.Context) ([]string, error) { return usedModules(ctx, eng.store, eng.census) }}
	settings := newProductSettings(eng.store, eng.dataDir)
	settings.used = mods.used
	return eng, settings, reconcileSettings(context.Background(), settings, mods, log)
}

// signIn signs the administrator in. On a new installation (tenant "") it first
// creates the administrator and returns the new tenant.
func signIn(t *testing.T, eng *engine, tenant string) (h http.Handler, admin, tenantOut string) {
	t.Helper()
	h = eng.api.Handler()
	creds := map[string]any{"email": "root@x.io", "password": "supersecret1"}
	if tenant == "" {
		tok, _, err := eng.setupTok.Ensure()
		if err != nil {
			t.Fatal(err)
		}
		code, setup, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/setup", "", "", map[string]any{"token": tok, "email": creds["email"], "password": creds["password"]})
		if code != http.StatusCreated {
			t.Fatalf("setup = %d: %s", code, raw)
		}
		org, _ := setup["organization"].(map[string]any)
		tenant, _ = org["tenant_id"].(string)
	}
	code, login, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/auth/login", "", "", creds)
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	admin, _ = login["token"].(string)
	return h, admin, tenant
}

// The engine's own restart re-executes its binary (selfrestart.go, reexec_unix.go):
// a new process image, the same PID, every process-wide registration fresh. The
// restart test runs each start the same way, as one child process of the test
// binary per start (TestFamilyRestartChild), never as a second boot in one process.
const (
	familyRestartPhaseEnv = "OLIVARES_TEST_FAMILY_RESTART_PHASE"
	familyRestartDirEnv   = "OLIVARES_TEST_FAMILY_RESTART_DIR"
)

// Enabling a family is one action: `enterprise enable reporting` records the
// activation, and after the restart it asks for, the reporting routes answer.
// Disabling it takes the module out again (it holds no data here).
func TestEnablingReportingServesItAfterTheRestartAndDisablingStopsIt(t *testing.T) {
	dir := t.TempDir()
	for _, phase := range []string{"before-enable", "after-enable", "after-disable"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFamilyRestartChild$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "OLIVARES_CLI_TRAMPOLINE=", familyRestartPhaseEnv+"="+phase, familyRestartDirEnv+"="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestFamilyRestartChild") {
			t.Fatalf("start %q (its own process) = %v:\n%s", phase, err, out)
		}
	}
}

// TestFamilyRestartChild is one start of the engine, in its own process, for
// TestEnablingReportingServesItAfterTheRestartAndDisablingStopsIt. Alone it skips.
func TestFamilyRestartChild(t *testing.T) {
	phase, dir := os.Getenv(familyRestartPhaseEnv), os.Getenv(familyRestartDirEnv)
	if phase == "" || dir == "" {
		t.Skip("a start of TestEnablingReportingServesItAfterTheRestartAndDisablingStopsIt")
	}
	withAddonModules(t, map[string][]string{"reporting": {"reporting"}})
	ctx := context.Background()
	tenantFile := filepath.Join(dir, "family-restart-tenant")

	eng, settings, err := serveStart(t, dir)
	if err != nil {
		t.Fatalf("start %s = %v, want serve without a second restart", phase, err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	prepareCompliancePacksTestEntitlement(t)
	var tenant string
	if phase != "before-enable" {
		raw, err := os.ReadFile(tenantFile)
		if err != nil {
			t.Fatal(err)
		}
		tenant = string(raw)
	}
	h, admin, tenant := signIn(t, eng, tenant)
	reports := func() (int, map[string]any, string) {
		return doDemoViewJSON(t, h, http.MethodGet, "/v1/m/reporting/reports", admin, tenant, nil)
	}
	switch phase {
	case "before-enable":
		if err := os.WriteFile(tenantFile, []byte(tenant), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, body, raw := reports(); code != http.StatusNotFound || errorCode(body) != "module_not_enabled" {
			t.Fatalf("precondition: reporting before enable = %d %s, want 404 module_not_enabled", code, raw)
		}
		// What `olivares enterprise enable reporting` and the console write.
		if err := settings.SaveActivation(ctx, operator(t), activationWith(reportingActive), nil); err != nil {
			t.Fatal(err)
		}
	case "after-enable":
		want := http.StatusOK
		if thisEdition.name == "community" {
			want = http.StatusNotImplemented
		}
		if code, _, raw := reports(); code != want {
			t.Fatalf("reporting after enable and restart = %d %s, want %d", code, raw, want)
		}
		code, mods, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/console/modules", admin, tenant, nil)
		if code != http.StatusOK {
			t.Fatalf("console modules = %d %s", code, raw)
		}
		if st := moduleState(mods, "reporting"); st["running"] != true || st["selected"] == true || !slices.Equal(anyStrings(st["activated_by"]), []string{"reporting"}) {
			t.Fatalf("reporting module state = %v, want running, not selected, activated_by [reporting]", st)
		}
		if err := settings.SaveActivation(ctx, operator(t), activationWith(), nil); err != nil {
			t.Fatal(err)
		}
	case "after-disable":
		if code, body, raw := reports(); code != http.StatusNotFound || errorCode(body) != "module_not_enabled" {
			t.Fatalf("reporting after disable = %d %s, want 404 module_not_enabled", code, raw)
		}
	default:
		t.Fatalf("unknown phase %q", phase)
	}
}

// Disabling a family keeps its modules that hold data: they join the selection
// in the same write, and this node's copy, so the data stays served until an
// administrator deselects them. When usage cannot be read, every module the
// family ran is kept.
func TestDisablingAFamilyKeepsItsModulesThatHoldData(t *testing.T) {
	withAddonModules(t, map[string][]string{"reporting": {"reporting"}})
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		used func(context.Context) ([]string, error)
		kept []string
	}{
		{"no data", usedReturns(), nil},
		{"reporting holds data", usedReturns("reporting", "finops"), []string{"reporting"}},
		{"accessmap holds data", usedReturns("accessmap", "finops"), []string{"accessmap"}},
		// Reporting runs compliance, which in turn runs accessmap: retain all three when usage is unknown.
		{"usage unreadable", func(context.Context) ([]string, error) { return nil, errors.New("census unavailable") }, []string{"accessmap", "compliance", "reporting"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := settingsOnBareStore(t)
			p.used = tc.used
			base := sortedUnion(standardModuleSelection(), nil)
			if err := p.writeModules(ctx, operator(t), "deployment.settings.modules", base); err != nil {
				t.Fatal(err)
			}
			if err := p.SaveActivation(ctx, operator(t), activationWith(reportingActive), nil); err != nil {
				t.Fatal(err)
			}
			if got := recordedSelection(t, p); !slices.Equal(got, base) {
				t.Fatalf("enable changed the selection to %v", got)
			}
			if err := p.SaveActivation(ctx, operator(t), activationWith(), nil); err != nil {
				t.Fatal(err)
			}
			want := sortedUnion(base, tc.kept)
			if got := recordedSelection(t, p); !slices.Equal(got, want) {
				t.Fatalf("selection after disable = %v, want %v", got, want)
			}
			if tc.kept == nil {
				return
			}
			node, _, err := loadNodeModuleSelection(p.dataDir)
			if err != nil || !slices.Equal(node, want) {
				t.Fatalf("node copy after disable = %v (%v), want %v", node, err, want)
			}
		})
	}
}

// settingsOnBareStore is the deployment settings record on a store opened
// without booting an engine. The commercial build's boot installs process-wide
// add-on license sources and refuses a second install in one process, so a test
// that needs only the record does not boot.
func settingsOnBareStore(t *testing.T) *productSettings {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db")},
		func(store.ExtensionRegistry) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := newProductSettings(st, dir)
	p.now = func() time.Time { return productSettingsT }
	return p
}

func errorCode(body map[string]any) any {
	e, _ := body["error"].(map[string]any)
	return e["code"]
}

func moduleState(dto map[string]any, name string) map[string]any {
	list, _ := dto["modules"].([]any)
	for _, m := range list {
		if st, _ := m.(map[string]any); st["name"] == name {
			return st
		}
	}
	return nil
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, s := range list {
		str, _ := s.(string)
		out = append(out, str)
	}
	return out
}
