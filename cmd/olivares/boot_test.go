// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootWiresLogBrokerBuffer(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: log,
	})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("ensure setup token: %v", err)
	}
	h := eng.api.Handler()
	if code, _, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "root@x.io", "password": "supersecret1",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	code, login, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": "root@x.io", "password": "supersecret1",
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	admin, ok := login["token"].(string)
	if !ok || admin == "" {
		t.Fatalf("login returned no token: %s", raw)
	}

	eng.log.Info("log broker boot wire proof", "module", "boot-test")
	code, body, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/console/logs/buffer", admin, "", nil)
	if code != http.StatusOK {
		t.Fatalf("log buffer = %d, want 200: %s", code, raw)
	}
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("log buffer items have unexpected shape: %s", raw)
	}
	total, ok := body["total"].(float64)
	if !ok || int(total) != len(items) {
		t.Fatalf("log buffer total=%v items=%d: %s", body["total"], len(items), raw)
	}
	for _, item := range items {
		entry, _ := item.(map[string]any)
		if entry["message"] == "log broker boot wire proof" && entry["module"] == "boot-test" {
			return
		}
	}
	t.Fatalf("log buffer did not capture the boot logger entry: %s", raw)
}

// TestCompositionRootBootsAndWiresModules boots the REAL engine (in-memory store) and
// proves the Fase C composition root works end-to-end:
//   - boot() succeeds ⇒ all 19 modules registered their schema (a bad descriptor would
//     fail engine.Open) and mounted their routes (api.New validates/rejects a bad or
//     duplicate namespace), and the governance ABAC evaluator wired into the authorizer.
//   - every module's routes are reachable through the real auth/tenant chain.
//   - the XII↔XVII adapter wired in wire.go (sandbox.Scorer → evals.ScoreOutputs) scores
//     a real sandbox run — the production form of the integration, not the test stub.
func TestCompositionRootBootsAndWiresModules(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test"})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := eng.api.Handler()

	do := func(method, path, token string, body any, tenant string) (int, map[string]any, string) {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.RemoteAddr = "10.0.0.1:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if tenant != "" {
			req.Header.Set("X-Olivares-Tenant", tenant)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m, rec.Body.String()
	}

	// First-boot setup → login → org (owner has every module permission tier).
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, raw := do("POST", "/v1/setup", "", map[string]any{"token": tok, "email": "root@x.io", "password": "supersecret1"}, ""); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	code, body, raw := do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, "")
	if code != http.StatusOK {
		t.Fatalf("login = %d %s", code, raw)
	}
	admin := body["token"].(string)
	code, body, raw = do("POST", "/v1/system/orgs", admin, map[string]any{"name": "acme", "slug": "acme"}, "")
	if code != http.StatusCreated {
		t.Fatalf("create org = %d %s", code, raw)
	}
	tenant := body["tenant_id"].(string)

	// boot() succeeding already proves all 19 modules registered their schema and
	// mounted their namespaces (engine.Open / api.New reject a bad descriptor or
	// namespace). Here we additionally confirm the verified routes are reachable
	// through the real auth+tenant chain (200, not 404) — the modules plus a
	// neighbor (redteam), the compliance module and the health/notify
	// modules to prove the chain end-to-end.
	for _, p := range []string{
		"/v1/m/evals/suites", "/v1/m/evals/runs", "/v1/m/evals/scorecards",
		"/v1/m/sandbox/scenarios", "/v1/m/sandbox/runs", "/v1/m/sandbox/comparisons",
		"/v1/m/redteam/catalog",
		"/v1/m/compliance/frameworks", "/v1/m/compliance/capabilities",
		"/v1/m/health/status", "/v1/m/health/dependencies", "/v1/m/health/incidents",
		"/v1/m/notify/routes", "/v1/m/notify/destinations", "/v1/m/notify/deliveries",
	} {
		if code, _, raw := do("GET", p, admin, nil, tenant); code != http.StatusOK {
			t.Errorf("GET %s = %d (%s): not mounted/reachable through the composition root", p, code, raw)
		}
	}

	// Control: an unknown namespace is NOT mounted (404) — mounting is selective.
	if code, _, _ := do("GET", "/v1/m/doesnotexist/x", admin, nil, tenant); code != http.StatusNotFound {
		t.Errorf("unknown namespace = %d, want 404", code)
	}

	// XII↔XVII through the PRODUCTION adapter (wire.go): a sandbox scenario scored by
	// the real evals module. Step keys match the eval case keys.
	code, body, raw = do("POST", "/v1/m/evals/suites", admin, map[string]any{
		"name": "greet", "subject_kind": "sandbox_run", "scorer": "exact",
	}, tenant)
	if code != http.StatusCreated {
		t.Fatalf("create suite = %d %s", code, raw)
	}
	suiteID := body["id"].(string)
	if c, _, r := do("POST", "/v1/m/evals/suites/"+suiteID+"/cases", admin,
		map[string]any{"case_key": "c1", "input": "n/a", "expected": "hello"}, tenant); c != http.StatusCreated {
		t.Fatalf("add case = %d %s", c, r)
	}
	code, body, raw = do("POST", "/v1/m/sandbox/scenarios", admin, map[string]any{
		"name": "greet-scn", "subject_kind": "agent",
		"steps": []map[string]any{{"key": "c1", "input": "r1"}},
		"mocks": []map[string]any{{"resource": "r1", "response": "hello"}},
	}, tenant)
	if code != http.StatusCreated {
		t.Fatalf("create scenario = %d %s", code, raw)
	}
	scnID := body["id"].(string)
	code, body, raw = do("POST", "/v1/m/sandbox/scenarios/"+scnID+"/run", admin, map[string]any{"suite_ref": suiteID}, tenant)
	if code != http.StatusCreated {
		t.Fatalf("run scenario = %d %s", code, raw)
	}
	if body["score"] != float64(1) || body["passed"] != true {
		t.Fatalf("composition-wired evals scoring failed: score=%v passed=%v (%s)", body["score"], body["passed"], raw)
	}
}
