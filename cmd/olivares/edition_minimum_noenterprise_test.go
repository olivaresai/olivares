// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/modulespec"
)

// The other tests of this package exercise each module at the edition that holds it, so
// the package runs them without the Community route gate (TestMain calls beforeTests after
// every init); TestCommunityRefusesEngineerAuthoringKeepsStoredReads installs the gate
// this build ships.
func init() { beforeTests = func() { thisEdition.routeGate = nil } }

// TestCommunityRefusesEngineerAuthoringKeepsStoredReads: knowledge is placed in Olivares AI
// Engineer (modules.json). In the Community build a new knowledge base is refused with
// 403 edition_required naming Engineer, while the one stored before stays readable.
func TestCommunityRefusesEngineerAuthoringKeepsStoredReads(t *testing.T) {
	communityRouteGate := editionPortsForBuild().routeGate
	if communityRouteGate == nil {
		t.Fatal("the Community build has no route gate: a capability placed above Community is served")
	}
	armed := false
	saved := thisEdition.routeGate
	t.Cleanup(func() { thisEdition.routeGate = saved })
	thisEdition.routeGate = func(method, pattern string) error {
		if !armed {
			return nil
		}
		return communityRouteGate(method, pattern)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	dir := t.TempDir()
	eng, err := boot(ctx, bootConfig{
		Engine: "sqlite", DSN: filepath.Join(dir, "edition.db"), DataDir: dir,
		Version: "edition-test", Logger: discardLog(), NoIngest: true, ServeMode: true,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, err := eng.authr.BootstrapSuperadmin(ctx, "edition@example.invalid", "edition-fixture-1234"); err != nil {
		t.Fatal(err)
	}
	token, _, err := eng.authr.Login(ctx, "edition@example.invalid", "edition-fixture-1234", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, h: eng.api.Handler(), adminToken: token,
		tenantA: createOrganizationFixture(t, eng.store, "Edition", "edition").TenantID.String()}

	// A knowledge base stored before the placement (an upgraded 0.9 server).
	var kb struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA, map[string]any{
		"name": "stored-before", "classification": "public",
	}, &kb); code != http.StatusCreated || kb.ID == "" {
		t.Fatalf("create the stored knowledge base = %d id=%q", code, kb.ID)
	}

	armed = true
	code, raw := h.req("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA, map[string]any{
		"name": "new-one", "classification": "public",
	})
	var refusal struct {
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			Edition    string `json:"edition"`
			Capability string `json:"capability"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &refusal)
	if code != http.StatusForbidden || refusal.Error.Code != "edition_required" || refusal.Error.Edition != "engineer" ||
		refusal.Error.Capability != "knowledge" || refusal.Error.Message != "Knowledge is part of Olivares AI Engineer." {
		t.Fatalf("new knowledge base in Community = %d %s; want 403 edition_required, edition engineer, capability knowledge", code, raw)
	}
	if code, raw := h.req("GET", "/v1/m/knowledge/kbs/"+kb.ID, h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("read of the stored knowledge base = %d %s; want 200", code, raw)
	}
}

// TestEveryModuleRouteMeetsItsPlacement walks every route the production module set mounts and
// asks the Community gate about it: a module placed above Community (modules.json: a title and a
// minimum edition) refuses it, except GET, HEAD and DELETE of a module that keeps stored data
// below its edition; every other module, and a module row without a title (it answers with
// its own refusal), is left open. A route whose namespace has no row in modules.json would be
// served ungated, so it is red too.
func TestEveryModuleRouteMeetsItsPlacement(t *testing.T) {
	rows := map[string]modulespec.Spec{}
	for _, spec := range modulespec.All() {
		rows[spec.Namespace] = spec
	}
	walked, refused := 0, 0
	for _, m := range liveModules(t).all {
		var routes []catalogRoute
		m.APIRoutes(catalogRecorder{ns: m.APINamespace(), out: &routes})
		spec, ok := rows[m.APINamespace()]
		if !ok && len(routes) > 0 {
			t.Errorf("module %q mounts routes and has no row in modules.json: its routes are served ungated", m.APINamespace())
			continue
		}
		for _, r := range routes {
			walked++
			pattern := strings.TrimSuffix("/v1/m/"+r.ns+r.pattern, "/")
			stored := r.method == http.MethodGet || r.method == http.MethodHead || r.method == http.MethodDelete
			wantRefused := spec.Title != "" && spec.Edition != license.EditionCommunity && !(spec.ReadsBelow && stored)
			err := api.EditionRouteRefusal(license.EditionCommunity, r.method, pattern)
			if (err != nil) != wantRefused {
				t.Errorf("%s %s in Community: refusal = %v, want refused = %v", r.method, pattern, err, wantRefused)
			}
			if err != nil {
				refused++
			}
		}
	}
	if walked < 200 || refused == 0 {
		t.Fatalf("walked %d routes and %d were refused: the walk is partial and this test would pass vacuously", walked, refused)
	}
}
