// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

// An activation turns its modules on and off in the running engine: the routes
// answer, server-info and the module page follow, and nothing restarts.
func TestAnActivationTurnsItsModulesOnAndOffWithoutARestart(t *testing.T) {
	withAddonModules(t, map[string][]string{"reporting": {"reporting"}})
	ctx := context.Background()
	eng, settings, err := serveStart(t, t.TempDir())
	if err != nil {
		t.Fatalf("serveStart = %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	prepareCompliancePacksTestEntitlement(t)
	h, admin, tenant := signIn(t, eng, "")
	reports := func() (int, string) {
		code, body, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/m/reporting/reports", admin, tenant, nil)
		if code == http.StatusNotFound && errorCode(body) == "module_not_enabled" {
			return code, "module_not_enabled"
		}
		return code, raw
	}
	notEnabled := func() []string {
		_, info, _ := doDemoViewJSON(t, h, http.MethodGet, "/v1/server-info", "", "", nil)
		return anyStrings(info["modules_not_enabled"])
	}
	if code, raw := reports(); code != http.StatusNotFound || raw != "module_not_enabled" {
		t.Fatalf("precondition: reporting before enable = %d %s", code, raw)
	}
	if !slices.Contains(notEnabled(), "reporting") {
		t.Fatalf("precondition: server-info modules_not_enabled = %v", notEnabled())
	}
	if osGetenv(reportingActive.Env) != "" {
		t.Fatalf("precondition: %s is already set", reportingActive.Env)
	}
	epoch := activationEpoch()

	if err := settings.SaveActivation(ctx, operator(t), activationWith(reportingActive), nil); err != nil {
		t.Fatal(err)
	}
	if err := eng.live.Apply(ctx); err != nil {
		t.Fatalf("live apply (enable) = %v", err)
	}
	if code, raw := reports(); code == http.StatusNotFound && raw == "module_not_enabled" {
		t.Fatalf("reporting after a live enable still answers module_not_enabled: %s", raw)
	}
	if got := osGetenv(reportingActive.Env); got != reportingActive.Value || activationEpoch() == epoch {
		t.Fatalf("after a live enable %s = %q (epoch %d, was %d): the configuration the add-on reads must follow", reportingActive.Env, got, activationEpoch(), epoch)
	}
	if slices.Contains(notEnabled(), "reporting") || slices.Contains(notEnabled(), "compliance") {
		t.Fatalf("server-info after enable still lists the family modules: %v", notEnabled())
	}
	code, mods, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/console/modules", admin, tenant, nil)
	if st := moduleState(mods, "reporting"); code != http.StatusOK || st["running"] != true {
		t.Fatalf("console modules after enable = %d %s, want reporting running", code, raw)
	}

	if err := settings.SaveActivation(ctx, operator(t), activationWith(), nil); err != nil {
		t.Fatal(err)
	}
	if err := eng.live.Apply(ctx); err != nil {
		t.Fatalf("live apply (disable) = %v", err)
	}
	if code, raw := reports(); code != http.StatusNotFound || raw != "module_not_enabled" {
		t.Fatalf("reporting after a live disable = %d %s, want module_not_enabled", code, raw)
	}
	if got := osGetenv(reportingActive.Env); got != "" {
		t.Fatalf("after a live disable %s = %q, want it withdrawn", reportingActive.Env, got)
	}
}
