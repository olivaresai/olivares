// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise || !addon_ids

package api_test

import (
	"crypto/x509"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

func TestPIVCommunityRejectsConfiguredVerifier(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PIV = &auth.PIVConfig{Roots: x509.NewCertPool(), AllowOCSPUnknown: true}
	})
	token := h.adminLogin()
	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/auth/piv/status"}, {"POST", "/v1/auth/piv/elevate"},
	} {
		r := h.do(route.method, route.path, token, nil, nil)
		if r.code != http.StatusNotImplemented || r.body["error"].(map[string]any)["code"] != "piv_not_configured" {
			t.Errorf("%s %s = %d %s, want 501 piv_not_configured", route.method, route.path, r.code, r.raw)
		}
	}
	r := h.do("GET", "/v1/auth/whoami", token, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("whoami = %d %s", r.code, r.raw)
	}
	if r.body["authentication_configuration"].(map[string]any)["piv_configured"] != false {
		t.Fatal("Community advertised a configured smart-card verifier")
	}
	if r.body["aal"] != float64(auth.AAL1) {
		t.Fatal("Community elevated the session")
	}
}
