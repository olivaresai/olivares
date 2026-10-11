// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// Admission moves to registration, but each published surface keeps its refusal
// order and envelope. No optional service is installed for these requests.
func TestCoreRouteRefusalCompatibility(t *testing.T) {
	h := newHarness(t)
	h.adminLogin()
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/v1/agents", 401, `"code":"unauthenticated"`},
		{"GET", "/v1/agents/missing", 401, `"code":"unauthenticated"`},
		{"POST", "/v1/agents", 401, `"code":"unauthenticated"`},
		{"GET", "/v1/console/sso", 401, `"code":"unauthenticated"`},
		{"POST", "/v1/console/dr/backup", 401, `"code":"unauthenticated"`},
		{"GET", "/v1/console/secrets", 401, `"code":"unauthenticated"`},
		{"GET", "/v1/console/secrets?scope=tenant", 401, `"code":"unauthenticated"`},
		{"GET", "/v1/console/secrets?scope=invalid", 400, `scope must be tenant`},
		{"GET", "/v1/console/mcp-gateway", 401, `"code":"unauthenticated"`},
		{"POST", "/v1/auth/token-exchange", 401, `"error":"invalid_client"`},
		{"GET", "/v1/scim/v2/Users", 401, `"status":"401"`},
		{"POST", "/v1/ssf/events", 401, `"status":"401"`},
		{"POST", "/v1/auth/webauthn/register/options", 401, `"code":"unauthenticated"`},
		{"GET", "/v1/auth/browser-session", 401, `"code":"unauthenticated"`},
		{"POST", "/v1/auth/os-account-bindings", 403, `"code":"forbidden"`},
		{"GET", "/healthz", http.StatusOK, `"status":"ok"`},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := h.do(tc.method, tc.path, "", nil, nil)
			if r.code != tc.status || !strings.Contains(r.raw, tc.body) {
				t.Fatalf("got %d %s; want %d containing %s", r.code, r.raw, tc.status, tc.body)
			}
		})
	}
}

func TestCoreAuthzenExposurePrecedesAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config api.AuthZenConfig
		status int
	}{
		{"disabled", api.AuthZenConfig{Disabled: true}, http.StatusNotFound},
		{"network", api.AuthZenConfig{AllowedCIDRs: []string{"192.0.2.0/24"}}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOpts(t, func(o *api.Options) { o.AuthZen = &tc.config })
			h.adminLogin()
			for _, path := range []string{"/access/v1/evaluation", "/access/v1/evaluations", "/access/v1/search/subject", "/access/v1/search/resource", "/access/v1/search/action", "/access/v1/access-review/export"} {
				r := h.do("POST", path, "", nil, nil)
				if r.code != tc.status {
					t.Errorf("%s = %d %s; want %d before authentication", path, r.code, r.raw, tc.status)
				}
			}
		})
	}
}

func TestDisabledModuleRoutesKeepRefusal(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.NotEnabledModules = []string{"demo"}
	}, demoModule{})
	h.adminLogin()
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, path := range []string{"/v1/m/demo", "/v1/m/demo/", "/v1/m/demo/things", "/v1/m/demo/nested/missing"} {
			r := h.do(method, path, "", nil, nil)
			if r.code != http.StatusNotFound || !strings.Contains(r.raw, `"code":"module_not_enabled"`) || !strings.Contains(r.raw, `"module":"demo"`) {
				t.Errorf("%s %s = %d %s; want disabled-module refusal", method, path, r.code, r.raw)
			}
		}
	}
}
