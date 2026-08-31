// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// TestMetricsAuth_NoConfig verifies the default: no MetricsConfig = unauthenticated
// access, the historical default.
func TestMetricsAuth_NoConfig(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/metrics", "", nil, nil)
	if r.code != 200 {
		t.Fatalf("/metrics without config = %d, want 200", r.code)
	}
}

// TestMetricsAuth_BearerToken verifies that a configured token gates /metrics.
func TestMetricsAuth_BearerToken(t *testing.T) {
	const token = "scrape-secret-42"
	h := newHarnessOpts(t, func(o *api.Options) {
		o.MetricsAuth = &api.MetricsConfig{Token: token}
	})

	// No token → 401.
	r := h.do("GET", "/metrics", "", nil, nil)
	if r.code != 401 {
		t.Errorf("no token = %d, want 401", r.code)
	}

	// Wrong token → 403.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer wrong-token")
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("wrong token = %d, want 403", rec.Code)
	}

	// Correct token → 200.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("correct token = %d, want 200", rec.Code)
	}
}

// TestMetricsAuth_CIDRAllowlist verifies CIDR-based access control on /metrics.
func TestMetricsAuth_CIDRAllowlist(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.MetricsAuth = &api.MetricsConfig{AllowedCIDRs: []string{"192.168.1.0/24"}}
	})

	// The test harness sets RemoteAddr to 10.0.0.1:1234 by default → blocked.
	r := h.do("GET", "/metrics", "", nil, nil)
	if r.code != 403 {
		t.Errorf("out-of-range peer = %d, want 403", r.code)
	}

	// Peer within the allowed CIDR → 200.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.168.1.50:9090"
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("in-range peer = %d, want 200", rec.Code)
	}
}

// TestMetricsAuth_BothTokenAndCIDR verifies AND logic: both must pass.
func TestMetricsAuth_BothTokenAndCIDR(t *testing.T) {
	const token = "both-required"
	h := newHarnessOpts(t, func(o *api.Options) {
		o.MetricsAuth = &api.MetricsConfig{
			Token:        token,
			AllowedCIDRs: []string{"10.0.0.0/8"},
		}
	})

	// Right CIDR, no token → 401.
	r := h.do("GET", "/metrics", "", nil, nil)
	if r.code != 401 {
		t.Errorf("right CIDR no token = %d, want 401", r.code)
	}

	// Right CIDR, right token → 200.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("right CIDR right token = %d, want 200", rec.Code)
	}

	// Wrong CIDR, right token → 403 (CIDR checked first).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.168.1.1:5555"
	req.Header.Set("Authorization", "Bearer "+token)
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("wrong CIDR right token = %d, want 403", rec.Code)
	}
}

// TestMetricsAuth_InvalidCIDR verifies that a malformed CIDR fails api.New.
func TestMetricsAuth_InvalidCIDR(t *testing.T) {
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(context.Background()); return e }); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	_, _, _ = tok.Ensure()
	_, err = api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test",
		MetricsAuth: &api.MetricsConfig{AllowedCIDRs: []string{"not-a-cidr"}},
	})
	if err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
}

// TestMetricsAuth_OtherEndpointsUnaffected verifies the gate only applies to /metrics.
func TestMetricsAuth_OtherEndpointsUnaffected(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.MetricsAuth = &api.MetricsConfig{Token: "locked"}
	})
	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		r := h.do("GET", path, "", nil, nil)
		if r.code != 200 {
			t.Errorf("%s with metrics auth = %d, want 200", path, r.code)
		}
	}
}
