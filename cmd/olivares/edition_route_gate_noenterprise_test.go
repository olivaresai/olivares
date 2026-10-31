// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
)

// TestTheEditionRouteGateAnswersEveryAuthenticatedCaller: a Business build's licence gate
// for its write routes (editionPorts routeGate) sits inside the engine router, after
// authentication, and decides by the route the router matched:
//   - activating a catalog instance applies its deployment through the router in-process,
//     and that call meets the gate as a request from outside does;
//   - a path the router resolves to the same route by a dot segment meets it too;
//   - an anonymous request is not asked about: the route's own 401 answers it.
//
// Community fills no gate; the test installs one that refuses the deploy apply route.
func TestTheEditionRouteGateAnswersEveryAuthenticatedCaller(t *testing.T) {
	const apply = "POST /v1/m/deploy/definitions/{id}/apply"
	saved := thisEdition.routeGate
	t.Cleanup(func() { thisEdition.routeGate = saved })
	var asked []string
	thisEdition.routeGate = func(method, pattern string) error {
		asked = append(asked, method+" "+pattern)
		if method+" "+pattern == apply {
			return license.AddonRequired("deploy", "apply-deployment")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	eng, err := boot(ctx, bootConfig{
		Engine: "sqlite", DSN: filepath.Join(dir, "gate.db"), DataDir: dir,
		Version: "gate-test", Logger: discardLog(), NoIngest: true, ServeMode: true,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, err := eng.authr.BootstrapSuperadmin(ctx, "gate@example.invalid", "gate-fixture-1234"); err != nil {
		t.Fatal(err)
	}
	token, _, err := eng.authr.Login(ctx, "gate@example.invalid", "gate-fixture-1234", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, h: eng.api.Handler(), adminToken: token,
		tenantA: createOrganizationFixture(t, eng.store, "Gate", "gate").TenantID.String()}
	refused := func(what string, code int, raw []byte) {
		t.Helper()
		if code != http.StatusForbidden || !strings.Contains(string(raw), "addon_requires_license") {
			t.Fatalf("%s = %d %s; want the gate's 403 addon_requires_license", what, code, raw)
		}
	}

	applyPath := "/v1/m/deploy/definitions/" + model.NewID().String() + "/apply"
	if code, raw := h.req("POST", applyPath, "", h.tenantA, map[string]any{}); code != http.StatusUnauthorized || len(asked) != 0 {
		t.Fatalf("anonymous apply = %d %s, gate asked %v; want the route's 401 before the gate", code, raw, asked)
	}
	code, raw := h.req("POST", applyPath, h.adminToken, h.tenantA, map[string]any{})
	refused("apply", code, raw)
	code, raw = h.req("POST", "/v1/m/deploy/definitions/../apply", h.adminToken, h.tenantA, map[string]any{})
	refused("apply through a dot segment", code, raw)

	entry := h.catalogCreate(t, "/entries", map[string]any{
		"kind": "agent", "name": "catalog agent", "slug": "catalog-agent", "version": "1.0.0", "spec": map[string]any{"image": "catalog-agent:1"},
	})
	if code, raw := h.req("POST", "/v1/m/catalog/entries/"+entry+"/approve", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, raw)
	}
	instance := h.catalogCreate(t, "/entries/"+entry+"/instantiate", map[string]any{"name": "catalog-agent", "target_ref": "deployment:" + entry})
	path := "/v1/m/catalog/instances/" + instance + "/transition"
	if code, raw := h.req("POST", path, h.adminToken, h.tenantA, map[string]any{"status": "approved"}); code != http.StatusOK {
		t.Fatalf("approve instance = %d %s", code, raw)
	}
	asked = nil
	code, raw = h.req("POST", path, h.adminToken, h.tenantA, map[string]any{"status": "active"})
	refused("catalog activation (its in-process apply)", code, raw)
	if len(asked) != 2 || asked[1] != apply {
		t.Fatalf("gate asked %v; want the transition, then the in-process apply", asked)
	}
	assertEq(t, "instance retained", h.getJSON(h.adminToken, h.tenantA, "/v1/m/catalog/instances/"+instance)["status"], "approved")
}
