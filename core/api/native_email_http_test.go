// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

func TestNativeAccountEmailValidationHTTP(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.InviteSender = &capturingInviteSender{} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "email-validation")
	usersBefore := h.do("GET", "/v1/users", admin, nil, nil).raw
	membersBefore := h.do("GET", "/v1/members", admin, nil, tenantHdr(tenant)).raw
	for _, email := range []string{"bad@", "@example.test", "bad@@example.test", "not-an-email", "Person <person@example.test>", "person name@example.test"} {
		t.Run(email, func(t *testing.T) {
			for _, route := range []string{"/v1/onboard", "/v1/users", "/v1/auth/login"} {
				body := map[string]any{"email": email, "password": "fixture-password-123"}
				bearer, headers := admin, tenantHdr(tenant)
				switch route {
				case "/v1/onboard":
					body["role"], body["mode"] = auth.RoleViewer, "password"
				case "/v1/auth/login":
					bearer, headers = "", nil
				}
				r := h.do("POST", route, bearer, body, headers)
				if r.code != http.StatusBadRequest {
					t.Errorf("%s accepted malformed email: HTTP %d", route, r.code)
					continue
				}
				if err := r.body["error"].(map[string]any); err["message"] != "Enter a valid email address." {
					t.Errorf("%s refusal = %v", route, err)
				}
			}
		})
	}
	if got := h.do("GET", "/v1/users", admin, nil, nil).raw; got != usersBefore {
		t.Error("malformed email created an account")
	}
	if got := h.do("GET", "/v1/members", admin, nil, tenantHdr(tenant)).raw; got != membersBefore {
		t.Error("malformed email granted a membership")
	}
	// Existing normalization, internal domains and plus addresses remain usable.
	for _, email := range []string{" Person+test@Example.test ", "local@directory"} {
		body := map[string]any{"email": email, "password": "fixture-password-123", "role": auth.RoleViewer}
		if r := h.do("POST", "/v1/onboard", admin, body, tenantHdr(tenant)); r.code != http.StatusCreated {
			t.Fatalf("valid onboard = %d %s", r.code, r.raw)
		}
		if r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "fixture-password-123"}, nil); r.code != http.StatusOK {
			t.Fatalf("valid login = %d", r.code)
		}
	}
	t.Run("invite_without_sender", func(t *testing.T) {
		h := newHarness(t)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "invite-email-validation")
		before := make(map[string]string)
		for _, route := range []string{"/v1/users", "/v1/members", "/v1/invites"} {
			before[route] = h.do("GET", route, admin, nil, tenantHdr(tenant)).raw
		}
		for _, c := range []struct {
			email  string
			status int
			code   string
		}{
			{"bad@", http.StatusBadRequest, "invalid_email"},
			{"valid@internal", http.StatusConflict, "invite_delivery_unavailable"},
		} {
			r := h.do("POST", "/v1/onboard", admin, map[string]any{
				"email": c.email, "role": auth.RoleViewer, "mode": "invite",
			}, tenantHdr(tenant))
			if r.code != c.status {
				t.Fatalf("invite HTTP%d, want%d", r.code, c.status)
			}
			errBody := r.body["error"].(map[string]any)
			if errBody["code"] != c.code {
				t.Errorf("invite refusal code = %v, want %s", errBody["code"], c.code)
			}
			if c.status == http.StatusBadRequest && errBody["message"] != "Enter a valid email address." {
				t.Error("malformed invite did not show the native email sentence")
			}
		}
		for route, expected := range before {
			if h.do("GET", route, admin, nil, tenantHdr(tenant)).raw != expected {
				t.Errorf("refused invite changed %s", route)
			}
		}
	})
}

func TestFirstAdminMalformedEmailLeavesSetupOpenHTTP(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"token": h.setupTok, "email": "bad@", "password": "fixture-password-123"}
	if r := h.do("POST", "/v1/setup", "", body, nil); r.code != http.StatusBadRequest ||
		r.body["error"].(map[string]any)["message"] != "Enter a valid email address." {
		t.Fatalf("invalid first admin = %d %s", r.code, r.raw)
	}
	body["email"] = "first@example.test"
	if r := h.do("POST", "/v1/setup", "", body, nil); r.code != http.StatusCreated {
		t.Fatalf("corrected first admin = %d %s", r.code, r.raw)
	}
}
