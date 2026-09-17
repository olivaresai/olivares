// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// the login-enforcement errors map to a distinct 403 each, so the login UI can
// route to the SSO button / surface the perimeter denial instead of a generic 401/403.
// They are wrapped here to prove the mapping survives a fmt.Errorf("%w") chain (the
// enterprise engine returns wrapped fail-closed errors).
func TestStatusFor_LoginEnforcement(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"sso_required", auth.ErrSSORequired, http.StatusForbidden, "sso_required"},
		{"sso_required wrapped", fmt.Errorf("policy: %w", auth.ErrSSORequired), http.StatusForbidden, "sso_required"},
		{"network_not_allowed", auth.ErrNetworkNotAllowed, http.StatusForbidden, "network_not_allowed"},
		{"network wrapped", fmt.Errorf("posture read: %w", auth.ErrNetworkNotAllowed), http.StatusForbidden, "network_not_allowed"},
		// R5: a build without the recorded enforcement component refuses a new session as a
		// deployment state, not as an authentication failure or an edition prompt.
		{"login_enforcement_unavailable", auth.ErrLoginEnforcementComponentAbsent, http.StatusServiceUnavailable, "login_enforcement_unavailable"},
		{"login_enforcement_unavailable wrapped", fmt.Errorf("mint session: %w", auth.ErrLoginEnforcementComponentAbsent), http.StatusServiceUnavailable, "login_enforcement_unavailable"},
		{"invalid credentials stay 401", auth.ErrInvalidCredentials, http.StatusUnauthorized, "unauthenticated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := statusFor(tc.err)
			if status != tc.wantCode || code != tc.wantBody {
				t.Fatalf("statusFor(%v) = (%d, %q), want (%d, %q)", tc.err, status, code, tc.wantCode, tc.wantBody)
			}
		})
	}
}
