// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"github.com/olivaresai/olivares/core/auth"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"net/http"
	"testing"
)

func TestTracingSettingsRequiresSystemAdminAndCurrentStepUp(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tracing")
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": "owner@tracing.test", "password": "tenantowner1", "tenant": tenant.String(), "role": auth.RoleOwner}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("owner = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "owner@tracing.test", "password": "tenantowner1"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login = %d %s", r.code, r.raw)
	}
	owner := r.body["token"].(string)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, tc := range []struct {
			token  string
			status int
		}{{"", 401}, {owner, 403}, {admin, 503}} {
			r = h.do(method, "/v1/system/tracing", tc.token, obstrace.DefaultSettings(), nil)
			if r.code != tc.status {
				t.Fatalf("%s expected %d, got %d %s", method, tc.status, r.code, r.raw)
			}
		}
	}
	h.requirePasskeyStepUp()
	r = h.do(http.MethodPut, "/v1/system/tracing", admin, obstrace.DefaultSettings(), nil)
	if r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
		t.Fatalf("write without step-up = %d %s", r.code, r.raw)
	}
	h.elevate(admin)
	r = h.do(http.MethodPut, "/v1/system/tracing", admin, obstrace.DefaultSettings(), nil)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("unwired service = %d %s", r.code, r.raw)
	}
}
