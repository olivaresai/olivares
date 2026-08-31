// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// --- test doubles for the managed SSO config --------------------------

// fakeSealer is a reversible, scope-bound sealer for tests (NOT secure — it only
// exercises the seal/open round trip and the scope binding).
type fakeSealer struct{}

func (fakeSealer) Seal(_ context.Context, scope model.TenantID, pt []byte) (string, error) {
	return "sealed:" + scope.String() + ":" + base64.StdEncoding.EncodeToString(pt), nil
}

func (fakeSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, "sealed:"+scope.String()+":")
	if !ok {
		return nil, errors.New("fakeSealer: wrong scope")
	}
	return base64.StdEncoding.DecodeString(rest)
}

// fakeBuilder asserts the opened secret flows through to the build (an oidc build
// with no client secret is an error), then returns a stub provider (the *fakeFed
// from handlers_federation_test.go).
func fakeBuilder(_ context.Context, p auth.FederationParams) (auth.Federation, error) {
	if p.Protocol == auth.ProtocolOIDC && p.OIDCClientSecret == "" {
		return nil, errors.New("fakeBuilder: oidc build without client secret")
	}
	return &fakeFed{proto: p.Protocol}, nil
}

// newConsoleHarnessFallback wires a server WITH the managed SSO config service
// (sealer + builder) and a chosen no-config fallback provider (the env-configured
// provider in production).
func newConsoleHarnessFallback(t *testing.T, fallback auth.Federation) *harness {
	return newHarnessOpts(t, func(o *api.Options) {
		// nil MultiIDP = the open (single-IdP) build: the single-IdP cap is enforced.
		o.FederationService = auth.NewFederationService(o.Store, fakeSealer{}, fakeBuilder, fallback, nil)
	})
}

// newConsoleHarness wires a server WITH the managed SSO config service over a
// NoFederation fallback (no env SSO).
func newConsoleHarness(t *testing.T) *harness {
	return newConsoleHarnessFallback(t, auth.NoFederation{})
}

// --- Workspaces --------------------------------------------------------------

func TestWorkspaceCRUD(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Create requires AAL3 step-up: at AAL1 the privileged action is refused.
	r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Payments", "slug": "payments"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("create at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	h.elevate(admin)
	r = h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Payments", "slug": "payments"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", r.code, r.raw)
	}
	wsID := r.body["id"].(string)
	if r.body["is_default"] != false || r.body["status"] != "active" {
		t.Fatalf("new workspace shape = %v", r.body)
	}

	// A reserved slug is rejected.
	if r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "x", "slug": "default"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("reserved slug = %d, want 400", r.code)
	}

	// List shows the default + the new one.
	r = h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	var defaultID string
	for _, it := range items {
		m := it.(map[string]any)
		if m["is_default"] == true {
			defaultID = m["id"].(string)
		}
	}
	if defaultID == "" || len(items) != 2 {
		t.Fatalf("list = %d items, want 2 incl. default; %v", len(items), items)
	}

	// Rename + archive the non-default workspace.
	if r := h.do("PATCH", "/v1/workspaces/"+wsID, admin, map[string]any{"name": "Payments EU", "status": "inactive"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("patch = %d %s", r.code, r.raw)
	}
	// The default workspace cannot be archived.
	if r := h.do("PATCH", "/v1/workspaces/"+defaultID, admin, map[string]any{"status": "inactive"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("archive default = %d, want 400", r.code)
	}
}

func TestWorkspaceCreateOwnerOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A tenant ADMIN (not owner) is denied workspace creation even with admin RBAC
	// and AAL3 — minting a workspace is owner-only.
	adminTok := h.mkMember(admin, "wsadmin@acme.io", "wsadminpass1", auth.RoleAdmin, tenant)
	h.elevate(adminTok)
	if r := h.do("POST", "/v1/workspaces", adminTok, map[string]any{"name": "x", "slug": "ws-x"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("admin (non-owner) create workspace = %d %s, want 403", r.code, r.raw)
	}

	// An OWNER can create one.
	ownerTok := h.mkMember(admin, "wsowner@acme.io", "wsownerpass1", auth.RoleOwner, tenant)
	h.elevate(ownerTok)
	if r := h.do("POST", "/v1/workspaces", ownerTok, map[string]any{"name": "ok", "slug": "ws-ok"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("owner create workspace = %d %s, want 201", r.code, r.raw)
	}
}

// --- Agent-groups ------------------------------------------------------------

func TestAgentGroupCRUD(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Create a group (agent:write, no AAL3 for operational CRUD).
	r := h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": "Payments bots", "slug": "payments-bots"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", r.code, r.raw)
	}
	groupID := r.body["id"].(string)

	// Create an agent to add to the group.
	ar := h.do("POST", "/v1/agents", admin, map[string]any{"name": "bot", "kind": "claude-code"}, tenantHdr(tenant))
	if ar.code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", ar.code, ar.raw)
	}
	agentID := ar.body["id"].(string)

	// Add the member (idempotent: a second add is 200, not a duplicate).
	if r := h.do("PUT", "/v1/agent-groups/"+groupID+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("add member = %d %s", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/agent-groups/"+groupID+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("re-add member = %d, want 200 (idempotent)", r.code)
	}
	// Adding a non-existent agent is a 404 (no dangling membership).
	if r := h.do("PUT", "/v1/agent-groups/"+groupID+"/members/"+model.NewID().String(), admin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("add missing agent = %d, want 404", r.code)
	}

	// Roster shows the one member.
	r = h.do("GET", "/v1/agent-groups/"+groupID+"/members", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("roster = %d %s", r.code, r.raw)
	}

	// Remove the member, then delete the group.
	if r := h.do("DELETE", "/v1/agent-groups/"+groupID+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("remove member = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/agent-groups/"+groupID, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete group = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/agent-groups/"+groupID, admin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("get deleted group = %d, want 404", r.code)
	}
}

// --- Onboarding + invites ----------------------------------------------------

func TestOnboardPasswordMode(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// At AAL1 the privileged onboarding action is refused.
	body := map[string]any{"email": "new@acme.io", "display_name": "New", "role": auth.RoleEditor, "mode": "password", "password": "newuserpass1"}
	if r := h.do("POST", "/v1/onboard", admin, body, tenantHdr(tenant)); r.code != http.StatusForbidden ||
		r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("onboard at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	h.elevate(admin)
	r := h.do("POST", "/v1/onboard", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("onboard = %d %s", r.code, r.raw)
	}
	if r.body["created"] != true {
		t.Fatalf("expected created=true; %v", r.body)
	}
	if u := r.body["user"].(map[string]any); u["is_superadmin"] != false {
		t.Fatalf("onboarded user must never be superadmin; %v", u)
	}
	// The onboarded user can log in and is a member of the tenant.
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "new@acme.io", "password": "newuserpass1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("onboarded login = %d %s", lr.code, lr.raw)
	}
	if g := h.do("GET", "/v1/agents", lr.body["token"].(string), nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("onboarded user acting in tenant = %d, want 200 (editor member)", g.code)
	}
}

func TestOnboardInviteMode(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.elevate(admin)

	r := h.do("POST", "/v1/onboard", admin,
		map[string]any{"email": "invitee@acme.io", "role": auth.RoleViewer, "mode": "invite"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("invite onboard = %d %s", r.code, r.raw)
	}
	inv := r.body["invite"].(map[string]any)
	token := inv["token"].(string)
	if token == "" {
		t.Fatalf("invite token must be returned show-once; %v", inv)
	}
	acceptURL, _ := inv["accept_url"].(string)
	if !strings.Contains(acceptURL, "/accept-invite#token="+token) || strings.Contains(acceptURL, "?token=") {
		t.Fatalf("invite accept_url must keep the bearer out of HTTP query logs: %q", acceptURL)
	}

	// It appears in the pending list (no token material).
	lr := h.do("GET", "/v1/invites", admin, nil, tenantHdr(tenant))
	if lr.code != http.StatusOK || len(lr.body["items"].([]any)) != 1 {
		t.Fatalf("pending invites = %d %s", lr.code, lr.raw)
	}
	if first := lr.body["items"].([]any)[0].(map[string]any); first["token"] != nil {
		t.Fatalf("pending invite must not expose token material; %v", first)
	}

	// Accept is unauthenticated and mints a session.
	ar := h.do("POST", "/v1/invites/accept", "", map[string]any{"token": token, "password": "inviteepass1"}, nil)
	if ar.code != http.StatusOK || ar.body["token"] == "" {
		t.Fatalf("accept = %d %s", ar.code, ar.raw)
	}
	// The invitee can now log in with the password they set.
	if lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "invitee@acme.io", "password": "inviteepass1"}, nil); lr.code != http.StatusOK {
		t.Fatalf("invitee login = %d %s", lr.code, lr.raw)
	}
	// The invite is single-use: a second accept fails.
	if ar := h.do("POST", "/v1/invites/accept", "", map[string]any{"token": token, "password": "inviteepass1"}, nil); ar.code != http.StatusBadRequest {
		t.Fatalf("second accept = %d, want 400 invite_invalid", ar.code)
	}
	// A garbage token is invalid (no oracle).
	if ar := h.do("POST", "/v1/invites/accept", "", map[string]any{"token": "olvi_bad_token", "password": "whatever12"}, nil); ar.code != http.StatusBadRequest {
		t.Fatalf("bad token accept = %d, want 400", ar.code)
	}
}

func TestOnboardCeilingAndAuthority(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A tenant ADMIN (rank 3) onboards within their ceiling but not above it.
	adminTok := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)
	h.elevate(adminTok)

	// Onboarding an OWNER (rank 4) exceeds the admin's ceiling -> 403 role_ceiling
	// (NOT step_up_required: AAL3 already satisfied; and NOT the generic
	// "forbidden", which would tell an admin they lack a permission they in fact
	// hold — the ceiling is about RANK, and the fix is a more senior human, not
	// a different grant).
	r := h.do("POST", "/v1/onboard", adminTok,
		map[string]any{"email": "wantowner@acme.io", "role": auth.RoleOwner, "mode": "password", "password": "ownerpass12"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "role_ceiling" {
		t.Fatalf("onboard above ceiling = %d %s, want 403 role_ceiling", r.code, r.raw)
	}

	// Onboarding an editor (rank 2) is within the ceiling.
	if r := h.do("POST", "/v1/onboard", adminTok,
		map[string]any{"email": "ed2@acme.io", "role": auth.RoleEditor, "mode": "password", "password": "editor2pass1"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("onboard within ceiling = %d %s, want 201", r.code, r.raw)
	}

	// A plain EDITOR has no membership:write -> the onboard route is RBAC-denied.
	editorTok := h.mkMember(admin, "plain@acme.io", "plainpass123", auth.RoleEditor, tenant)
	h.elevate(editorTok)
	if r := h.do("POST", "/v1/onboard", editorTok,
		map[string]any{"email": "z@acme.io", "role": auth.RoleViewer, "mode": "password", "password": "zpassword12"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor onboard = %d, want 403 (no membership:write)", r.code)
	}
}

// --- Managed SSO config ------------------------------------------------------

func TestSSOConfigLifecycle(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()

	// GET (superadmin, no AAL3 for a read) before any config.
	r := h.do("GET", "/v1/console/sso", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("get sso = %d %s", r.code, r.raw)
	}
	if r.body["configured"] != false || r.body["provider_available"] != true || r.body["redirect_uri"] == "" {
		t.Fatalf("initial sso view = %v", r.body)
	}

	// PUT without AAL3 is refused.
	cfg := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "shhh-secret"}
	if r := h.do("PUT", "/v1/console/sso", admin, cfg, nil); r.code != http.StatusForbidden ||
		r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("put sso at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	h.elevate(admin)
	r = h.do("PUT", "/v1/console/sso", admin, cfg, nil)
	if r.code != http.StatusOK {
		t.Fatalf("put sso = %d %s", r.code, r.raw)
	}
	if r.body["configured"] != true || r.body["protocol"] != "oidc" {
		t.Fatalf("put sso view = %v", r.body)
	}
	// The secret is NEVER returned; only a non-secret hint is.
	if _, leaked := r.body["oidc_client_secret"]; leaked {
		t.Fatalf("PUT response leaked the client secret: %v", r.body)
	}
	hint, _ := r.body["oidc_client_secret_hint"].(string)
	if hint == "" {
		t.Fatalf("expected a client secret hint; %v", r.body)
	}

	// The managed config now drives login: /federation/start redirects to the IdP.
	if r := h.do("GET", "/v1/auth/federation/start", "", nil, nil); r.code != http.StatusFound {
		t.Fatalf("federation start with managed config = %d %s, want 302", r.code, r.raw)
	}

	// Editing with an EMPTY secret keeps the sealed value (hint unchanged).
	cfg2 := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://idp.example", "oidc_client_id": "cid2", "oidc_client_secret": ""}
	r = h.do("PUT", "/v1/console/sso", admin, cfg2, nil)
	if r.code != http.StatusOK || r.body["oidc_client_secret_hint"].(string) != hint || r.body["oidc_client_id"] != "cid2" {
		t.Fatalf("edit keeping secret = %d %s (hint changed?)", r.code, r.raw)
	}

	// test-connection succeeds (the opened secret flows to the fake builder).
	if r := h.do("POST", "/v1/console/sso/test", admin, cfg2, nil); r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("test sso = %d %s", r.code, r.raw)
	}

	// DELETE reverts login to 501 (no managed config -> NoFederation fallback).
	if r := h.do("DELETE", "/v1/console/sso", admin, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete sso = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/federation/start", "", nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("federation start after delete = %d, want 501", r.code)
	}
}

// TestSSODeleteAuthoritativeOverEnvFallback covers the review's HIGH finding:
// DELETE must turn SSO OFF (501) even when an env-configured provider exists as
// the fallback — it must not silently revert login to the env IdP.
func TestSSODeleteAuthoritativeOverEnvFallback(t *testing.T) {
	// A LIVE env fallback provider (what an enterprise build with OLIVARES_OIDC_*
	// set would inject).
	h := newConsoleHarnessFallback(t, &fakeFed{proto: "oidc"})
	admin := h.adminLogin()
	h.elevate(admin)

	// With no managed config, login uses the env fallback (redirects to the IdP).
	if r := h.do("GET", "/v1/auth/federation/start", "", nil, nil); r.code != http.StatusFound {
		t.Fatalf("start with env fallback = %d, want 302", r.code)
	}
	// Configure managed SSO, then delete it.
	cfg := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "s"}
	if r := h.do("PUT", "/v1/console/sso", admin, cfg, nil); r.code != http.StatusOK {
		t.Fatalf("put = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/console/sso", admin, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	// After delete the tombstone is authoritative: login is OFF (501), NOT the
	// env fallback's 302. GET reports unconfigured again.
	if r := h.do("GET", "/v1/auth/federation/start", "", nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("start after delete = %d, want 501 (tombstone overrides env fallback)", r.code)
	}
	if r := h.do("GET", "/v1/console/sso", admin, nil, nil); r.code != http.StatusOK || r.body["configured"] != false {
		t.Fatalf("get after delete = %d configured=%v, want configured=false", r.code, r.body["configured"])
	}
}

// TestSSOSamlValidation covers the review's MEDIUM finding: a SAML config missing
// fields the builder needs must be REJECTED, not stored as active-but-dead.
func TestSSOSamlValidation(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	// entity_id + metadata only (missing acs_url + idp_sso_url) → 400.
	incomplete := map[string]any{"protocol": "saml", "enabled": true, "saml_entity_id": "sp", "saml_metadata_url": "https://idp/meta"}
	if r := h.do("PUT", "/v1/console/sso", admin, incomplete, nil); r.code != http.StatusBadRequest {
		t.Fatalf("incomplete saml = %d, want 400", r.code)
	}
}

func TestSSOConfigSuperadminOnly(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A tenant admin (not superadmin) cannot read or write the GLOBAL SSO config.
	adminTok := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)
	if r := h.do("GET", "/v1/console/sso", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin get sso = %d, want 403 (superadmin-only global config)", r.code)
	}
	h.elevate(adminTok)
	if r := h.do("PUT", "/v1/console/sso", adminTok, map[string]any{"protocol": "oidc", "enabled": true,
		"oidc_issuer": "https://idp.example", "oidc_client_id": "c", "oidc_client_secret": "s"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin put sso = %d, want 403", r.code)
	}
}

// TestSSOConfigPerTenant covers U6: the per-tenant admin surface. A superadmin
// manages a specific tenant's IdP via /v1/console/sso/tenants/{tenant}, distinct from
// the global config, and the view carries target_tenant. (A per-tenant IdP only
// RESOLVES at login in an enterprise build; here we prove the admin surface + scope.)
func TestSSOConfigPerTenant(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.elevate(admin)

	// PUT a per-tenant IdP (the only active config, so the open-build cap allows it).
	cfg := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://acme.idp", "oidc_client_id": "cid", "oidc_client_secret": "s"}
	r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String(), admin, cfg, nil)
	if r.code != http.StatusOK {
		t.Fatalf("put per-tenant sso = %d %s", r.code, r.raw)
	}
	if r.body["configured"] != true || r.body["target_tenant"] != tenant.String() {
		t.Fatalf("per-tenant view = %v, want configured + target_tenant=%s", r.body, tenant)
	}

	// GET the per-tenant config back.
	r = h.do("GET", "/v1/console/sso/tenants/"+tenant.String(), admin, nil, nil)
	if r.code != http.StatusOK || r.body["configured"] != true || r.body["target_tenant"] != tenant.String() || r.body["oidc_client_id"] != "cid" {
		t.Fatalf("get per-tenant = %d %v", r.code, r.body)
	}

	// The GLOBAL config is separate and still empty; it carries no target_tenant.
	r = h.do("GET", "/v1/console/sso", admin, nil, nil)
	if r.code != http.StatusOK || r.body["configured"] != false {
		t.Fatalf("global still unconfigured = %d %v", r.code, r.body)
	}
	if _, ok := r.body["target_tenant"]; ok {
		t.Fatalf("global config must not carry target_tenant: %v", r.body)
	}

	// DELETE the per-tenant config.
	if r := h.do("DELETE", "/v1/console/sso/tenants/"+tenant.String(), admin, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete per-tenant = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/console/sso/tenants/"+tenant.String(), admin, nil, nil); r.code != http.StatusOK || r.body["configured"] != false {
		t.Fatalf("per-tenant after delete = %d %v", r.code, r.body)
	}
}

// TestSSOConfigPerTenantCapOpenBuild proves the honest single-IdP cap holds through
// the per-tenant surface: with a global IdP already active, the open build (nil
// MultiIDP) REFUSES activating a second, per-tenant IdP — 403 multi_idp_requires_enterprise,
// not silent success. The enterprise MultiIDP capability is what lifts this.
func TestSSOConfigPerTenantCapOpenBuild(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.elevate(admin)

	global := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://global.idp", "oidc_client_id": "g", "oidc_client_secret": "s"}
	if r := h.do("PUT", "/v1/console/sso", admin, global, nil); r.code != http.StatusOK {
		t.Fatalf("put global = %d %s", r.code, r.raw)
	}
	// Activating a SECOND (per-tenant) IdP is capped in the open build.
	perTenant := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://acme.idp", "oidc_client_id": "a", "oidc_client_secret": "s"}
	r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String(), admin, perTenant, nil)
	if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "multi_idp_requires_enterprise" {
		t.Fatalf("second active idp in open build = %d %s, want 403 multi_idp_requires_enterprise", r.code, r.raw)
	}
	// But STAGING it as disabled is allowed (the cap is on activation only).
	perTenant["enabled"] = false
	if r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String(), admin, perTenant, nil); r.code != http.StatusOK {
		t.Fatalf("staging disabled per-tenant idp = %d %s, want 200", r.code, r.raw)
	}
}

// TestSSOConfigPerTenantSuperadminOnly proves D8: the per-tenant IdP CONFIG is
// superadmin-gated (a tenant:admin manages only its group→role mapping, elsewhere).
func TestSSOConfigPerTenantSuperadminOnly(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)

	if r := h.do("GET", "/v1/console/sso/tenants/"+tenant.String(), adminTok, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin get per-tenant sso = %d, want 403", r.code)
	}
	h.elevate(adminTok)
	if r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String(), adminTok, map[string]any{"protocol": "oidc", "enabled": true,
		"oidc_issuer": "https://x", "oidc_client_id": "c", "oidc_client_secret": "s"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin put per-tenant sso = %d, want 403", r.code)
	}
}

// TestSSOConfigPerTenantScopeValidation: a malformed tenant id is a 400, and the
// global scope must be reached via the base route (not disguised as per-tenant).
func TestSSOConfigPerTenantScopeValidation(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()

	if r := h.do("GET", "/v1/console/sso/tenants/not-a-uuid", admin, nil, nil); r.code != http.StatusBadRequest {
		t.Fatalf("malformed tenant = %d, want 400", r.code)
	}
	if r := h.do("GET", "/v1/console/sso/tenants/ffffffff-ffff-ffff-ffff-ffffffffffff", admin, nil, nil); r.code != http.StatusBadRequest {
		t.Fatalf("system tenant via per-tenant route = %d, want 400 (use global route)", r.code)
	}
}

// TestSSOConfigPerAliasIdPs covers U4: the per-IdP admin surface. A scope lists its
// IdPs (default first), the /idps/{alias} routes CRUD an additional IdP by alias (open
// build: staged inactive, since a 2nd ACTIVE is capped), a malformed alias 400s before
// the store, the list never leaks a secret, and the same routes exist under the per-tenant
// subtree.
func TestSSOConfigPerAliasIdPs(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.elevate(admin)

	// Empty scope: the IdP list is empty (never null).
	r := h.do("GET", "/v1/console/sso/idps", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("list idps = %d %s", r.code, r.raw)
	}
	if idps, _ := r.body["idps"].([]any); len(idps) != 0 {
		t.Fatalf("initial idps = %v, want empty", r.body["idps"])
	}

	// The base route configures the primary ("default") IdP, active.
	def := map[string]any{"protocol": "oidc", "enabled": true, "oidc_issuer": "https://default.idp", "oidc_client_id": "d", "oidc_client_secret": "s"}
	if r := h.do("PUT", "/v1/console/sso", admin, def, nil); r.code != http.StatusOK {
		t.Fatalf("put default = %d %s", r.code, r.raw)
	}
	// A named additional IdP, STAGED inactive (the open build caps a 2nd ACTIVE one).
	backup := map[string]any{"protocol": "oidc", "enabled": false, "oidc_issuer": "https://backup.idp", "oidc_client_id": "b", "oidc_client_secret": "s"}
	if r := h.do("PUT", "/v1/console/sso/idps/backup", admin, backup, nil); r.code != http.StatusOK || r.body["alias"] != "backup" {
		t.Fatalf("put idps/backup = %d %s (alias=%v)", r.code, r.raw, r.body["alias"])
	}

	// The list now has both, default first, and never a secret.
	r = h.do("GET", "/v1/console/sso/idps", admin, nil, nil)
	idps, _ := r.body["idps"].([]any)
	if len(idps) != 2 {
		t.Fatalf("idps = %v, want 2", r.body["idps"])
	}
	if idps[0].(map[string]any)["alias"] != "default" {
		t.Fatalf("first idp alias = %v, want default", idps[0].(map[string]any)["alias"])
	}
	for _, it := range idps {
		if _, leaked := it.(map[string]any)["oidc_client_secret"]; leaked {
			t.Fatalf("idp list leaked a secret: %v", it)
		}
	}

	// GET a specific alias.
	if r := h.do("GET", "/v1/console/sso/idps/backup", admin, nil, nil); r.code != http.StatusOK ||
		r.body["configured"] != true || r.body["alias"] != "backup" || r.body["oidc_issuer"] != "https://backup.idp" {
		t.Fatalf("get idps/backup = %d %v", r.code, r.body)
	}

	// A malformed alias is a 400 BEFORE any store round-trip.
	if r := h.do("GET", "/v1/console/sso/idps/Bad%20Alias", admin, nil, nil); r.code != http.StatusBadRequest {
		t.Fatalf("malformed alias = %d, want 400", r.code)
	}
	// A whitespace-only alias segment is malformed (400), NOT silently the primary.
	if r := h.do("GET", "/v1/console/sso/idps/%20", admin, nil, nil); r.code != http.StatusBadRequest {
		t.Fatalf("whitespace alias = %d, want 400", r.code)
	}

	// The per-tenant surface has the SAME per-alias routes (U6 × U4).
	pt := map[string]any{"protocol": "oidc", "enabled": false, "oidc_issuer": "https://acme.idp", "oidc_client_id": "a", "oidc_client_secret": "s"}
	if r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String()+"/idps/azure", admin, pt, nil); r.code != http.StatusOK ||
		r.body["alias"] != "azure" || r.body["target_tenant"] != tenant.String() {
		t.Fatalf("put per-tenant idps/azure = %d %v", r.code, r.body)
	}

	// DELETE the non-default alias hard-removes it (dropped from the list).
	if r := h.do("DELETE", "/v1/console/sso/idps/backup", admin, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete idps/backup = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/console/sso/idps", admin, nil, nil); r.code != http.StatusOK {
		t.Fatalf("list after delete = %d", r.code)
	} else if idps, _ := r.body["idps"].([]any); len(idps) != 1 {
		t.Fatalf("after delete idps = %v, want 1 (default)", r.body["idps"])
	}
}

// TestSSOConfigClaimedDomains covers U5 home-realm domain admin: domains are
// normalized + stored, the open build reports routed_by=unavailable (stores but never
// routes), a globally-duplicate domain is 409 domain_claimed, and a malformed one is 400.
func TestSSOConfigClaimedDomains(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.elevate(admin)

	// A per-tenant IdP (staged inactive — the open build caps a 2nd ACTIVE one) with domains.
	cfg := map[string]any{
		"protocol": "oidc", "enabled": false, "oidc_issuer": "https://acme.idp",
		"oidc_client_id": "c", "oidc_client_secret": "s",
		"claimed_domains": []string{"Acme.COM ", "acme.io"},
	}
	r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String()+"/idps/corp", admin, cfg, nil)
	if r.code != http.StatusOK {
		t.Fatalf("put with domains = %d %s", r.code, r.raw)
	}
	doms, _ := r.body["claimed_domains"].([]any)
	if len(doms) != 2 || doms[0] != "acme.com" {
		t.Fatalf("normalized domains = %v, want [acme.com acme.io]", r.body["claimed_domains"])
	}
	// Open build: it STORES the domains but never routes by them.
	if r.body["routed_by"] != "unavailable" {
		t.Fatalf("open-build routed_by = %v, want unavailable", r.body["routed_by"])
	}

	// Another IdP (the global default) claiming an already-claimed domain → 409.
	dup := map[string]any{
		"protocol": "oidc", "enabled": false, "oidc_issuer": "https://g.idp",
		"oidc_client_id": "g", "oidc_client_secret": "s", "claimed_domains": []string{"acme.com"},
	}
	if r := h.do("PUT", "/v1/console/sso", admin, dup, nil); r.code != http.StatusConflict ||
		r.body["error"].(map[string]any)["code"] != "domain_claimed" {
		t.Fatalf("duplicate domain = %d %s, want 409 domain_claimed", r.code, r.raw)
	}

	// A malformed domain → 400.
	bad := map[string]any{
		"protocol": "oidc", "enabled": false, "oidc_issuer": "https://b.idp",
		"oidc_client_id": "b", "oidc_client_secret": "s", "claimed_domains": []string{"nodot"},
	}
	if r := h.do("PUT", "/v1/console/sso/tenants/"+tenant.String()+"/idps/other", admin, bad, nil); r.code != http.StatusBadRequest {
		t.Fatalf("malformed domain = %d, want 400", r.code)
	}
}

func TestSSOConfigUnavailableWithoutService(t *testing.T) {
	// The default harness wires no FederationService: the config endpoints answer
	// 501 (managed SSO not available), never 500.
	h := newHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	if r := h.do("GET", "/v1/console/sso", admin, nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("get sso without service = %d, want 501", r.code)
	}
}

// TestAgentGroupPatchPreservesFields covers the review's HIGH finding: a partial
// PATCH must not wipe description/metadata that the request omitted.
func TestAgentGroupPatchPreservesFields(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("POST", "/v1/agent-groups", admin,
		map[string]any{"name": "Bots", "slug": "bots", "description": "the payment bots"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	// PATCH only the name — description must survive.
	if r := h.do("PATCH", "/v1/agent-groups/"+id, admin, map[string]any{"name": "Payment bots"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("patch = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/agent-groups/"+id, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["name"] != "Payment bots" || r.body["description"] != "the payment bots" {
		t.Fatalf("partial patch wiped a field: %v", r.body)
	}
}

// TestAgentGroupPatchRescopesWorkspace covers the gap: the edit form lets an
// operator change a group's workspace scope, so PATCH must accept workspace_id —
// set it (deny-closed against an unknown ref) and clear it back to tenant-wide.
func TestAgentGroupPatchRescopesWorkspace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.elevate(admin)

	r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Eng", "slug": "eng"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", r.code, r.raw)
	}
	wsID := r.body["id"].(string)

	// A tenant-wide group to start.
	r = h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": "Bots", "slug": "bots"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	// PATCH scopes it to the workspace — the filtered list must now find it.
	if p := h.do("PATCH", "/v1/agent-groups/"+id, admin, map[string]any{"workspace_id": wsID}, tenantHdr(tenant)); p.code != http.StatusOK {
		t.Fatalf("scope patch = %d %s", p.code, p.raw)
	}
	if g := h.do("GET", "/v1/agent-groups/"+id, admin, nil, tenantHdr(tenant)); g.body["workspace_id"] != wsID {
		t.Fatalf("workspace_id after scope = %v, want %s", g.body["workspace_id"], wsID)
	}
	if fl := h.do("GET", "/v1/agent-groups?workspace_id="+wsID, admin, nil, tenantHdr(tenant)); len(fl.body["items"].([]any)) != 1 {
		t.Fatalf("scoped filter = %d groups, want 1", len(fl.body["items"].([]any)))
	}

	// An unknown workspace ref is deny-closed (404), never a silent no-op.
	if bad := h.do("PATCH", "/v1/agent-groups/"+id, admin, map[string]any{"workspace_id": model.NewID().String()}, tenantHdr(tenant)); bad.code != http.StatusNotFound {
		t.Fatalf("patch with unknown workspace = %d, want 404", bad.code)
	}

	// An explicit empty ref clears the scope back to tenant-wide.
	if c := h.do("PATCH", "/v1/agent-groups/"+id, admin, map[string]any{"workspace_id": ""}, tenantHdr(tenant)); c.code != http.StatusOK {
		t.Fatalf("clear patch = %d %s", c.code, c.raw)
	}
	if g := h.do("GET", "/v1/agent-groups/"+id, admin, nil, tenantHdr(tenant)); g.body["workspace_id"] != nil && g.body["workspace_id"] != "" {
		t.Fatalf("workspace_id after clear = %v, want empty", g.body["workspace_id"])
	}
}

// TestMembershipCeilingExistingRole covers the review's MEDIUM finding: a
// lower-ranked admin must not be able to modify a member who already outranks it
// (the ceiling must also consider the target's CURRENT role).
func TestMembershipCeilingExistingRole(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Create a user and make them an OWNER (via superadmin).
	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": "boss@acme.io", "password": "bosspass1234"}, nil)
	if cr.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleOwner}, nil); g.code != http.StatusCreated {
		t.Fatalf("grant owner = %d %s", g.code, g.raw)
	}

	// A tenant ADMIN tries to demote the OWNER to editor → 403 (ceiling on the
	// target's current owner role).
	adminTok := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)
	if r := h.do("POST", "/v1/memberships", adminTok, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleEditor}, nil); r.code != http.StatusForbidden {
		t.Fatalf("admin demoting owner = %d, want 403", r.code)
	}
}

// --- helpers -----------------------------------------------------------------

// mkMember creates a user, grants it role in tenant, logs it in, and returns the
// session token.
func (h *harness) mkMember(admin, email, pass, role string, tenant model.TenantID) string {
	h.t.Helper()
	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass}, nil)
	if cr.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); g.code != http.StatusCreated {
		h.t.Fatalf("grant %s = %d %s", email, g.code, g.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	return lr.body["token"].(string)
}

// --- Workspace-aware filtering and summary ----------------------------

func TestAgentListWorkspaceFilter(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Create a workspace.
	h.elevate(admin)
	r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Payments", "slug": "payments"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", r.code, r.raw)
	}
	wsID := r.body["id"].(string)

	// Create two agents: one in the workspace, one without (default workspace).
	a1 := h.do("POST", "/v1/agents", admin, map[string]any{"name": "agent-in-ws", "kind": "claude-code", "workspace_id": wsID}, tenantHdr(tenant))
	if a1.code != http.StatusCreated {
		t.Fatalf("create agent-in-ws = %d %s", a1.code, a1.raw)
	}
	a2 := h.do("POST", "/v1/agents", admin, map[string]any{"name": "agent-default", "kind": "claude-code"}, tenantHdr(tenant))
	if a2.code != http.StatusCreated {
		t.Fatalf("create agent-default = %d %s", a2.code, a2.raw)
	}

	// Unfiltered list returns both.
	r = h.do("GET", "/v1/agents", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list all = %d %s", r.code, r.raw)
	}
	allItems := r.body["items"].([]any)
	if len(allItems) != 2 {
		t.Fatalf("list all = %d agents, want 2", len(allItems))
	}

	// Filtered list returns only the workspace agent.
	r = h.do("GET", "/v1/agents?workspace_id="+wsID, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list filtered = %d %s", r.code, r.raw)
	}
	filtered := r.body["items"].([]any)
	if len(filtered) != 1 {
		t.Fatalf("list filtered = %d agents, want 1", len(filtered))
	}
	if filtered[0].(map[string]any)["name"] != "agent-in-ws" {
		t.Fatalf("filtered agent = %v, want agent-in-ws", filtered[0])
	}
}

func TestAgentGroupListWorkspaceFilter(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Create a workspace and two groups: one scoped, one unscoped.
	h.elevate(admin)
	r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Eng", "slug": "eng"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", r.code, r.raw)
	}
	wsID := r.body["id"].(string)

	h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": "eng-bots", "slug": "eng-bots", "workspace_id": wsID}, tenantHdr(tenant))
	h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": "global-bots", "slug": "global-bots"}, tenantHdr(tenant))

	// Unfiltered list returns both.
	r = h.do("GET", "/v1/agent-groups", admin, nil, tenantHdr(tenant))
	if len(r.body["items"].([]any)) != 2 {
		t.Fatalf("list all = %d groups, want 2", len(r.body["items"].([]any)))
	}

	// Filtered list returns only the workspace group.
	r = h.do("GET", "/v1/agent-groups?workspace_id="+wsID, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("filtered list = %d %s", r.code, r.raw)
	}
	filtered := r.body["items"].([]any)
	if len(filtered) != 1 || filtered[0].(map[string]any)["name"] != "eng-bots" {
		t.Fatalf("filtered = %v, want 1 eng-bots", filtered)
	}
}

func TestWorkspaceSummary(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Create a workspace and populate it with an agent.
	h.elevate(admin)
	r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Sales", "slug": "sales"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", r.code, r.raw)
	}
	wsID := r.body["id"].(string)

	h.do("POST", "/v1/agents", admin, map[string]any{"name": "sales-bot", "kind": "claude-code", "workspace_id": wsID}, tenantHdr(tenant))
	h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": "sales-group", "slug": "sales-group", "workspace_id": wsID}, tenantHdr(tenant))

	// Summary returns counts.
	r = h.do("GET", "/v1/workspaces/"+wsID+"/summary", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("summary = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "Sales" || r.body["slug"] != "sales" {
		t.Fatalf("summary name/slug = %v %v", r.body["name"], r.body["slug"])
	}
	// agent_count should be 1 (the agent we created in this workspace).
	if int(r.body["agent_count"].(float64)) != 1 {
		t.Fatalf("agent_count = %v, want 1", r.body["agent_count"])
	}
	if int(r.body["group_count"].(float64)) != 1 {
		t.Fatalf("group_count = %v, want 1", r.body["group_count"])
	}
	if r.body["is_default"] != false {
		t.Fatalf("is_default = %v, want false", r.body["is_default"])
	}

	// Summary of a non-existent workspace returns 404.
	if r := h.do("GET", "/v1/workspaces/"+model.NewID().String()+"/summary", admin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("summary missing = %d, want 404", r.code)
	}
}

func TestWorkspaceSummaryDefaultWorkspace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Find the default workspace id.
	r := h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	var defaultID string
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		if m["is_default"] == true {
			defaultID = m["id"].(string)
		}
	}
	if defaultID == "" {
		t.Fatal("no default workspace found")
	}

	// Summary of the default workspace.
	r = h.do("GET", "/v1/workspaces/"+defaultID+"/summary", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("default summary = %d %s", r.code, r.raw)
	}
	if r.body["is_default"] != true {
		t.Fatalf("is_default = %v, want true", r.body["is_default"])
	}
}

func TestAgentDTOExposesWorkspaceID(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.elevate(admin)
	r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Test", "slug": "test-ws"}, tenantHdr(tenant))
	wsID := r.body["id"].(string)

	ar := h.do("POST", "/v1/agents", admin, map[string]any{"name": "bot", "kind": "claude-code", "workspace_id": wsID}, tenantHdr(tenant))
	if ar.code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", ar.code, ar.raw)
	}
	agentID := ar.body["id"].(string)

	// GET returns workspace_id.
	r = h.do("GET", "/v1/agents/"+agentID, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("get agent = %d %s", r.code, r.raw)
	}
	if r.body["workspace_id"] != wsID {
		t.Fatalf("workspace_id = %v, want %s", r.body["workspace_id"], wsID)
	}
}
