// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// E5b: sso_configured must derive from the STORED posture. Before the fix
// it checked fedSvc != nil — vacuously true on every boot — so the tile was
// green on deployments with no IdP at all.
func TestHealthSummarySSOConfiguredDerivesFromStoredPosture(t *testing.T) {
	h := newConsoleHarness(t) // federation service WIRED, no IdP configured
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("health-summary = %d %s", r.code, r.raw)
	}
	if r.body["sso_configured"] != false {
		t.Fatalf("sso_configured = %v with no stored IdP, want false: %s", r.body["sso_configured"], r.raw)
	}

	// Configure a real IdP → the tile turns true.
	h.elevate(admin)
	cfg := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "shhh-secret"}
	if r := h.do("PUT", "/v1/console/sso", admin, cfg, nil); r.code != http.StatusOK {
		t.Fatalf("put sso = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("health-summary = %d %s", r.code, r.raw)
	}
	if r.body["sso_configured"] != true {
		t.Fatalf("sso_configured = %v with a stored IdP, want true: %s", r.body["sso_configured"], r.raw)
	}
}

func TestHealthSummarySSOConfiguredIncludesEnvironmentFallback(t *testing.T) {
	h := newConsoleHarnessFallback(t, &fakeFed{proto: auth.ProtocolOIDC})
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("health-summary = %d %s", r.code, r.raw)
	}
	if r.body["sso_configured"] != true {
		t.Fatalf("sso_configured = %v with environment OIDC fallback, want true: %s", r.body["sso_configured"], r.raw)
	}

	// A managed delete writes the authoritative default tombstone. It must keep
	// both login and health off instead of exposing the environment fallback.
	h.elevate(admin)
	cfg := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "secret"}
	if r := h.do("PUT", "/v1/console/sso", admin, cfg, nil); r.code != http.StatusOK {
		t.Fatalf("put sso = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/console/sso", admin, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete sso = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.body["sso_configured"] != false {
		t.Fatalf("sso_configured = %v after managed delete, want false: %s", r.body["sso_configured"], r.raw)
	}
}

// E5c: connectors_error was declared in the DTO but never computed (always
// 0). It must count ENABLED roster sources whose live status is "failed" — the
// same criterion the public status page aggregates.
func TestHealthSummaryConnectorsErrorComputedFromRoster(t *testing.T) {
	roster := &stubSourceRoster{
		sources: []api.SourceRosterEntry{
			{Name: "ok", Kind: "aws", Status: "running", Enabled: true},
			{Name: "broken", Kind: "gcp-audit", Status: "failed", Enabled: true},
			{Name: "off", Kind: "azure", Status: "failed", Enabled: false}, // disabled → not counted
		},
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SourceRoster = roster
	})
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("health-summary = %d %s", r.code, r.raw)
	}
	if r.body["connectors_error"] != float64(1) {
		t.Fatalf("connectors_error = %v, want 1 (one enabled failed source): %s", r.body["connectors_error"], r.raw)
	}
}

// E5a: the public status page grows an audit_spool component when a spool
// budget is declared (absent otherwise — the default-harness test already pins
// the 5-component unconfigured shape), so `olivares status` renders it.
func TestPublicStatusAuditSpoolComponentWhenConfigured(t *testing.T) {
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine:             store.EngineSQLite,
		DSN:                filepath.Join(t.TempDir(), "status-spool.db"),
		AuditSpoolMaxBytes: 10 << 30,
		AuditSpoolOnFull:   store.AuditSpoolDegrade,
	}, nil)
	if err != nil {
		t.Fatalf("open configured audit spool store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(context.Background())
		return err
	}); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	h := newHarnessOpts(t, func(o *api.Options) { o.Store = st })

	r := h.do("GET", "/status", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", r.code, r.raw)
	}
	components, _ := r.body["components"].([]any)
	var spool map[string]any
	for _, c := range components {
		m := c.(map[string]any)
		if m["name"] == "audit_spool" {
			spool = m
		}
	}
	if spool == nil {
		t.Fatalf("configured spool missing from public status components: %s", r.raw)
	}
	if spool["status"] != "operational" {
		t.Fatalf("audit_spool status = %v, want operational (not engaged): %s", spool["status"], r.raw)
	}
	// The public page must stay coarse: no byte counts, no per-tenant data.
	for _, forbidden := range []string{"max_bytes", "used_bytes", "pending_drop_tenants", "pending_drops"} {
		if _, present := spool[forbidden]; present {
			t.Errorf("public audit_spool component leaked %q: %v", forbidden, spool)
		}
	}
}
