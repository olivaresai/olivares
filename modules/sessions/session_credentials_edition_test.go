// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// Brokering a managed credential into a session is a Business capability. A Community
// engine (no broker wired) answers 501 business_required to a launch that names one, and
// starts nothing.
func TestCredentials_ACommunityEngineAnswers501NamingBusiness(t *testing.T) {
	runner := &fakeRunner{initSID: "community-credentials"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true}}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "community-credentials")
	if r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": "cred-admin@fh.invalid", "password": "synthetic-password1", "tenant": tenant.String(), "role": auth.RoleAdmin}, nil); r.code != http.StatusCreated {
		t.Fatalf("tenant admin = %d %s", r.code, r.raw)
	}
	login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "cred-admin@fh.invalid", "password": "synthetic-password1"}, nil)
	token, _ := login.body["token"].(string)

	got := h.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{
		"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "name": "community credentials", "credentials": []string{"prod-db"},
	}, tenantHdr(tenant))
	errBody, _ := got.body["error"].(map[string]any)
	msg, _ := errBody["message"].(string)
	if got.code != http.StatusNotImplemented || errBody["code"] != "business_required" || !strings.Contains(msg, "Business") {
		t.Fatalf("launch naming a credential = %d %s, want 501 business_required naming Business", got.code, got.raw)
	}
	if n := launchCount(runner); n != 0 {
		t.Fatalf("a refused launch reached the runner %d times", n)
	}
}
