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
	"testing"
	"time"
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
