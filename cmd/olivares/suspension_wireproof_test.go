// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestSuspendedTenantIsNotServedByTheRealBinary is the wire proof: it
// boots the REAL composition root and shows that withdrawing a tenant's service
// actually stops the tenant being served — through the HTTP surface a customer
// uses, not through a double.
//
// It exists because the guard is wired in boot.go, BELOW the api package: a
// core/api test cannot see it, and every one of them would keep passing if the
// wiring were deleted. This test fails if the guard is not wired.
//
// It also asserts the two halves the brief separates: that a suspended tenant is
// refused, and that restoring it brings back the estate it had (the request that
// was refused now succeeds and returns the same agent).
func TestSuspendedTenantIsNotServedByTheRealBinary(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test"})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := eng.api.Handler()

	do := func(method, path, token string, body any, tenant string) (int, string) {
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
		return rec.Code, rec.Body.String()
	}

	tok, _, terr := eng.setupTok.Ensure()
	if terr != nil {
		t.Fatalf("setup token: %v", terr)
	}
	if code, body := do("POST", "/v1/setup", "", map[string]any{
		"token": tok, "email": "root@olivares.test", "password": "Sup3r-secret-passphrase!",
	}, ""); code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}
	code, body := do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@olivares.test", "password": "Sup3r-secret-passphrase!",
	}, "")
	if code != http.StatusOK {
		t.Fatalf("login: %d %s", code, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &login); err != nil || login.Token == "" {
		t.Fatalf("login token: %v %s", err, body)
	}

	code, body = do("POST", "/v1/system/orgs", login.Token, map[string]any{"slug": "acme", "name": "Acme"}, "")
	if code != http.StatusCreated {
		t.Fatalf("create org: %d %s", code, body)
	}
	var org struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal([]byte(body), &org); err != nil || org.TenantID == "" {
		t.Fatalf("org tenant_id: %v %s", err, body)
	}
	tenant, err := model.ParseTenantID(org.TenantID)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}

	// Baseline: the tenant is served, and holds one agent.
	if code, body = do("POST", "/v1/agents", login.Token, map[string]any{"name": "alpha", "kind": "assistant"}, org.TenantID); code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", code, body)
	}
	if code, body = do("GET", "/v1/agents", login.Token, nil, org.TenantID); code != http.StatusOK {
		t.Fatalf("baseline list agents: %d %s", code, body)
	}
	if !bytes.Contains([]byte(body), []byte(`"alpha"`)) {
		t.Fatalf("baseline list did not return the seeded agent: %s", body)
	}

	// Withdraw service through the System path (the same call the API handler and
	// the cloud control plane make).
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgStatus(ctx, tenant, model.StatusSuspended)
		return e
	}); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// The customer's request is now REFUSED — not answered with an empty list,
	// which is what a missing guard looks like and what the reproduction
	// measured against a deleted tenant.
	code, body = do("GET", "/v1/agents", login.Token, nil, org.TenantID)
	if code != http.StatusLocked {
		t.Fatalf("suspended tenant list agents = %d %s, want 423 (the store guard is not wired into boot)", code, body)
	}
	if !bytes.Contains([]byte(body), []byte("tenant_suspended")) {
		t.Fatalf("suspended tenant response lacks the tenant_suspended code: %s", body)
	}
	// Writes too — and reads are refused as hard as writes on purpose: in this
	// product reading IS the service.
	if code, body = do("POST", "/v1/agents", login.Token, map[string]any{"name": "beta", "kind": "assistant"}, org.TenantID); code != http.StatusLocked {
		t.Fatalf("suspended tenant create agent = %d %s, want 423", code, body)
	}

	// The background pumps stop enumerating it: this is the "other way in" — a
	// pump that kept the tenant in its roster would keep advancing its workflows
	// and egressing its events on a schedule nobody asked for.
	served, err := servedBusinessTenants(ctx, eng.store)
	if err != nil {
		t.Fatalf("enumerate served tenants: %v", err)
	}
	for _, tid := range served {
		if tid == tenant {
			t.Fatal("a suspended tenant is still enumerated for background work")
		}
	}

	// Restore: the SAME request that was refused now succeeds, with the estate the
	// tenant had. Nothing was rebuilt — nothing was destroyed.
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgStatus(ctx, tenant, model.StatusActive)
		return e
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	code, body = do("GET", "/v1/agents", login.Token, nil, org.TenantID)
	if code != http.StatusOK {
		t.Fatalf("restored tenant list agents = %d %s, want 200", code, body)
	}
	if !bytes.Contains([]byte(body), []byte(`"alpha"`)) {
		t.Fatalf("restore was lossy: the seeded agent is gone: %s", body)
	}
	if bytes.Contains([]byte(body), []byte(`"beta"`)) {
		t.Fatalf("a write refused during suspension was applied anyway: %s", body)
	}

	// And it is back in the pump roster.
	served, err = servedBusinessTenants(ctx, eng.store)
	if err != nil {
		t.Fatalf("enumerate served tenants after restore: %v", err)
	}
	var back bool
	for _, tid := range served {
		if tid == tenant {
			back = true
		}
	}
	if !back {
		t.Fatal("a restored tenant did not return to the background-work roster")
	}
}
