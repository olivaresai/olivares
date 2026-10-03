// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

func errorCode(r resp) any {
	e, _ := r.body["error"].(map[string]any)
	return e["code"]
}

// A fresh install demands nothing beyond the sign-in: a password administrator
// creates a workspace at once, and whoami tells the console so.
func TestFreshInstallAdministratorActsAtSignInStrength(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "fresh")

	if r := h.do("GET", "/v1/auth/whoami", admin, nil, nil); r.code != http.StatusOK ||
		r.body["admin_step_up"] != "none" || r.body["step_up_satisfied"] != true || r.body["aal"] != float64(1) {
		t.Fatalf("whoami = %d %s, want admin_step_up none, step_up_satisfied true, aal 1", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Payments", "slug": "payments"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("create workspace at AAL1 under the default policy = %d %s, want 201", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/step-up-policy", admin, nil, nil); r.code != http.StatusOK || r.body["admin_step_up"] != "none" {
		t.Fatalf("GET step-up-policy = %d %s, want none", r.code, r.raw)
	}

	// An API token still carries no human assurance: a step-up route refuses it.
	it := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "ci", "superadmin": true}, nil)
	if it.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", it.code, it.raw)
	}
	tok := it.body["token"].(string)
	if r := h.do("POST", "/v1/workspaces", tok, map[string]any{"name": "Ops", "slug": "ops"}, tenantHdr(tenant)); r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
		t.Fatalf("token create workspace = %d %s, want 403 step_up_required", r.code, r.raw)
	}
}

// Raising the policy refuses unless this session already meets the new level;
// lowering it always works, so an administrator cannot lock themselves out.
func TestStepUpPolicyRaiseRefusesLowerAlwaysWorks(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "policy")

	if r := h.do("PUT", "/v1/auth/step-up-policy", admin, map[string]any{"admin_step_up": "passkey"}, nil); r.code != http.StatusConflict || errorCode(r) != "passkey_not_enrolled" {
		t.Fatalf("raise to passkey without one = %d %s, want 409 passkey_not_enrolled", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/auth/step-up-policy", admin, map[string]any{"admin_step_up": "totp"}, nil); r.code != http.StatusConflict || errorCode(r) != "totp_sign_in_required" {
		t.Fatalf("raise to totp from a password session = %d %s, want 409 totp_sign_in_required", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/auth/step-up-policy", admin, map[string]any{"admin_step_up": "always"}, nil); r.code != http.StatusBadRequest {
		t.Fatalf("unknown value = %d %s, want 400", r.code, r.raw)
	}

	h.requirePasskeyStepUp()
	if r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "A", "slug": "a"}, tenantHdr(tenant)); r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
		t.Fatalf("create under the passkey policy at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", admin, nil, nil); r.body["admin_step_up"] != "passkey" || r.body["step_up_satisfied"] != false {
		t.Fatalf("whoami under passkey = %s", r.raw)
	}
	if r := h.do("PUT", "/v1/auth/step-up-policy", admin, map[string]any{"admin_step_up": "none"}, nil); r.code != http.StatusOK || r.body["admin_step_up"] != "none" {
		t.Fatalf("turn off at AAL1 = %d %s, want 200 none", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "A", "slug": "a"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("create after turning it off = %d %s, want 201", r.code, r.raw)
	}
}

// SR2 FH-02: lowering is a lowering whatever it lowers to: passkey -> totp from a
// password session needs no TOTP proof.
func TestStepUpPolicyLowerPasskeyToTOTPAtCurrentStrength(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	h.requirePasskeyStepUp()
	if r := h.do("PUT", "/v1/auth/step-up-policy", root, map[string]any{"admin_step_up": "totp"}, nil); r.code != http.StatusOK {
		t.Fatalf("lower passkey -> totp at AAL1 = %d %s, want 200", r.code, r.raw)
	}
}

// Policy propagation is bounded: a raise on one node takes effect
// on another node once that node's policy cache expires, and a tenant owner on any
// node cannot change the deployment-wide policy.
func TestStepUpPolicyRaiseReachesAnotherNodeWithinTheCacheWindow(t *testing.T) {
	defer auth.SetStepUpCacheTTLForTest(50 * time.Millisecond)()
	h := newHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "two-nodes")
	if r := h.do("POST", "/v1/users", root, map[string]any{"email": "owner@two.invalid", "password": "two-nodes-owner-pw", "tenant": tenant.String(), "role": auth.RoleOwner}, nil); r.code != http.StatusCreated {
		t.Fatalf("owner = %d %s", r.code, r.raw)
	}
	peer := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: h.st}, nil)
	r := peer.do("POST", "/v1/auth/login", "", map[string]any{"email": "owner@two.invalid", "password": "two-nodes-owner-pw"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("peer login = %d", r.code)
	}
	owner := r.body["token"].(string)
	if r := peer.do("GET", "/v1/auth/whoami", owner, nil, nil); r.body["admin_step_up"] != "none" {
		t.Fatalf("peer policy = %v, want none", r.body["admin_step_up"])
	}
	h.elevate(root)
	if r := h.do("PUT", "/v1/auth/step-up-policy", root, map[string]any{"admin_step_up": "passkey"}, nil); r.code != http.StatusOK {
		t.Fatalf("raise = %d %s", r.code, r.raw)
	}
	if r := peer.do("PUT", "/v1/auth/step-up-policy", owner, map[string]any{"admin_step_up": "none"}, nil); r.code != http.StatusForbidden {
		t.Fatalf("a tenant owner changed the deployment-wide policy: %d", r.code)
	}
	time.Sleep(100 * time.Millisecond) // the peer's cache window
	if r := peer.do("POST", "/v1/workspaces", owner, map[string]any{"name": "After the raise", "slug": "after-raise"}, tenantHdr(tenant)); r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
		t.Fatalf("peer workspace create after the raise = %d %s, want 403 step_up_required", r.code, r.raw)
	}
}
