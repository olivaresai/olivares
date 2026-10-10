// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogActivationReachesGovernedDeploy(t *testing.T) {
	h := newHarness(t)
	spec := map[string]any{"image": "catalog-agent:1"}
	entry := h.catalogCreate(t, "/entries", map[string]any{
		"kind": "agent", "name": "catalog agent", "slug": "catalog-agent", "version": "1.0.0", "spec": spec,
	})
	if code, raw := h.req("POST", "/v1/m/catalog/entries/"+entry+"/approve", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, raw)
	}
	var def struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/deploy/definitions", h.adminToken, h.tenantA, map[string]any{
		"subject_kind": "agent", "subject_ref": "catalog-agent", "name": "catalog-agent", "environment": "test",
		"target": "docker.host/local", "runtime": "docker", "source_ref": "catalog entry " + entry, "spec": spec,
	}, &def); code != http.StatusCreated {
		t.Fatalf("declare = %d", code)
	}
	instance := h.catalogCreate(t, "/entries/"+entry+"/instantiate", map[string]any{"name": "catalog-agent", "target_ref": "deployment:" + def.ID})
	path := "/v1/m/catalog/instances/" + instance + "/transition"
	if code, raw := h.req("POST", path, h.adminToken, h.tenantA, map[string]any{"status": "approved"}); code != http.StatusOK {
		t.Fatalf("approve instance = %d %s", code, raw)
	}
	// The production composition has no executor in this harness. The dispatch
	// must reach deploy's own fail-closed response, without fabricating active.
	if code, raw := h.req("POST", path, h.adminToken, h.tenantA, map[string]any{"status": "active"}); code != http.StatusServiceUnavailable {
		t.Fatalf("unwired deployment = %d %s", code, raw)
	}
	assertEq(t, "instance retained", h.getJSON(h.adminToken, h.tenantA, "/v1/m/catalog/instances/"+instance)["status"], "approved")
	for _, origin := range []string{"http://catalog.test", "https://catalog.test"} {
		t.Run(origin, func(t *testing.T) {
			login := httptest.NewRequest("POST", origin+"/v1/auth/login", strings.NewReader(`{"email":"admin@e2e.test","password":"supersecret-e2e"}`))
			login.Header.Set("X-Olivares-Session", "cookie")
			login.Header.Set("Origin", origin)
			login.Header.Set("Content-Type", "application/json")
			if strings.HasPrefix(origin, "https:") {
				login.TLS = &tls.ConnectionState{}
			}
			out := httptest.NewRecorder()
			h.h.ServeHTTP(out, login)
			if out.Code != 200 {
				t.Fatalf("cookie login = %d", out.Code)
			}
			var session struct {
				CSRF string `json:"csrf_token"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			for _, foreign := range []bool{false, true} {
				req := httptest.NewRequest("POST", origin+path, strings.NewReader(`{"status":"active"}`))
				req.TLS = login.TLS
				req.Header.Set("Origin", origin)
				if foreign {
					req.Header.Set("Origin", "https://foreign.test")
				}
				req.Header.Set("X-Olivares-Session", "cookie")
				req.Header.Set("X-CSRF-Token", session.CSRF)
				req.Header.Set("X-Olivares-Tenant", h.tenantA)
				req.Header.Set("Content-Type", "application/json")
				for _, cookie := range out.Result().Cookies() {
					req.AddCookie(cookie)
				}
				got := httptest.NewRecorder()
				h.h.ServeHTTP(got, req)
				want := http.StatusServiceUnavailable
				if foreign {
					want = http.StatusForbidden
				}
				if got.Code != want {
					t.Fatalf("cookie dispatch foreign=%v: %d %s, want %d", foreign, got.Code, got.Body.String(), want)
				}
			}
		})
	}
	// A revision no longer matching the approved source must be refused before
	// even the fail-closed executor runs.
	if code, raw := h.req("PUT", "/v1/m/deploy/definitions/"+def.ID, h.adminToken, h.tenantA, map[string]any{"spec": map[string]any{"image": "another-agent:1"}}); code != http.StatusOK {
		t.Fatalf("revise = %d %s", code, raw)
	}
	if code, raw := h.req("POST", path, h.adminToken, h.tenantA, map[string]any{"status": "active"}); code != http.StatusConflict {
		t.Fatalf("changed source = %d %s", code, raw)
	}
	if code, raw := h.req("POST", path, h.adminToken, h.tenantB, map[string]any{"status": "active"}); code != http.StatusNotFound {
		t.Fatalf("foreign instance = %d %s", code, raw)
	}
}

func (h *harness) catalogCreate(t *testing.T, path string, body map[string]any) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/catalog"+path, h.adminToken, h.tenantA, body, &created); code != http.StatusCreated || created.ID == "" {
		t.Fatalf("create %s = %d", path, code)
	}
	return created.ID
}
