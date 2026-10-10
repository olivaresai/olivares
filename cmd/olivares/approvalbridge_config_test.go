// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestBootApprovalBridgeConfig(t *testing.T) {
	const tenant = "01900000-0000-7000-8000-000000000001"
	for _, tc := range []struct {
		name      string
		config    string
		missing   bool
		wantError string
	}{
		{name: "unset"},
		{name: "missing file", missing: true, wantError: "cannot be read"},
		{name: "malformed JSON", config: "{", wantError: "invalid JSON"},
		{name: "configured", config: `{"expires_in_seconds":900,"escalate_in_seconds":60,"tenants":[{"tenant":"` + tenant + `","token":"test-invalid-service-token","expires_in_seconds":120}]}`},
		{name: "invalid tenant", config: `{"tenants":[{"tenant":"invalid","token":"test-invalid-service-token"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := ""
			if tc.config != "" || tc.missing {
				path = filepath.Join(t.TempDir(), "approval-bridge.json")
				if !tc.missing {
					if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Setenv("OLIVARES_APPROVAL_BRIDGE_CONFIG", path)
			eng, err := boot(context.Background(), bootConfig{
				DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLog(),
			})
			if eng != nil {
				t.Cleanup(func() { _ = eng.Close() })
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), "OLIVARES_APPROVAL_BRIDGE_CONFIG") || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("boot error = %v, want actionable configuration error containing %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b := eng.approvalBridge
			if b == nil {
				t.Fatal("approval bridge missing")
			}
			if tc.name == "invalid tenant" {
				if _, ok := b.Cred(model.TenantID(tenant)); ok {
					t.Fatal("invalid explicit configuration acquired fallback authority")
				}
				return
			}
			if tc.name == "unset" {
				if b.LocalProposer == nil {
					t.Fatal("unconfigured install lost the local approval queue")
				}
				return
			}
			cred, ok := b.Cred(model.TenantID(tenant))
			if !ok || cred.Token != "test-invalid-service-token" || cred.ExpiresIn != 120 || cred.EscalateIn != 60 {
				t.Error("configured credential and approval windows were not preserved")
			}
			other := model.TenantID("01900000-0000-7000-8000-000000000002")
			if _, status, _, err := b.Request(context.Background(), other, "deploy.apply", "deployment", "test", "plan", "test", "test"); err != nil || status != nbNoGate {
				t.Errorf("unconfigured tenant status=%q err=%v, want no_gate", status, err)
			}
			// A configured token must pass the real API authentication chain. It
			// must never inherit the engine's local proposer authority.
			if _, _, _, err := b.Request(context.Background(), model.TenantID(tenant), "deploy.apply", "deployment", "test", "plan", "test", "test"); err == nil || !strings.Contains(err.Error(), "401") {
				t.Errorf("invalid configured token error=%v, want authentication failure", err)
			}
		})
	}
}

// bootBridgeTenant boots an unconfigured SQLite engine with one administrator
// and one organization, then reboots it with the given bridge configuration
// (written by write, which receives the harness to read its tenant and tokens).
func bootBridgeTenant(t *testing.T, write func(*harness) approvalBridgeConfig) (*engine, *harness) {
	t.Helper()
	t.Setenv("OLIVARES_APPROVAL_BRIDGE_CONFIG", "")
	cfg := bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", Logger: discardLog()}
	eng, err := boot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, h: eng.api.Handler(), authr: eng.authr}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := h.req("POST", "/v1/setup", "", "", map[string]any{
		"token": setup, "email": "bridge@boot.test", "password": "test-bridge-password",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	var login struct{ Token string }
	if code := h.reqInto("POST", "/v1/auth/login", "", "", map[string]any{
		"email": "bridge@boot.test", "password": "test-bridge-password",
	}, &login); code != http.StatusOK || login.Token == "" {
		t.Fatalf("login = %d", code)
	}
	h.adminToken = login.Token
	h.tenantA = h.createOrg("Bridge test", "bridge-test")
	raw, err := json.Marshal(write(h))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "approval-bridge.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_APPROVAL_BRIDGE_CONFIG", path)
	eng, err = boot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h.h, h.authr = eng.api.Handler(), eng.authr
	return eng, h
}

// Provider permission prompts propose as the session principal through the
// local queue. The outbound tenant list (or an all-invalid one) must not refuse
// them in a tenant it does not name, and must not open module gates there.
func TestBootProviderApprovalIgnoresOutboundTenantList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenants []approvalBridgeTenant
	}{
		{name: "other tenant listed", tenants: []approvalBridgeTenant{{Tenant: "01900000-0000-7000-8000-000000000001", Token: "test-invalid-service-token"}}},
		{name: "every entry invalid", tenants: []approvalBridgeTenant{{Tenant: "invalid", Token: "test-invalid-service-token"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, h := bootBridgeTenant(t, func(*harness) approvalBridgeConfig { return approvalBridgeConfig{Tenants: tc.tenants} })
			tenant := model.TenantID(h.tenantA)
			if eng.approvalBridge == nil {
				t.Fatal("outbound configuration removed the provider approval queue")
			}
			if _, status, _, err := eng.approvalBridge.Request(t.Context(), tenant, "deploy.apply", "deployment", "test", "plan", "test", "test"); err != nil || status != nbNoGate {
				t.Fatalf("unlisted tenant module gate status=%q err=%v, want no_gate", status, err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			request := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: "osn_test", SessionRef: "osn_session_test", Principal: auth.Principal{SessionIdentity: "osn_session_test"}, TurnID: "turn-one", Method: "item/permissions/requestApproval", Kind: "permissions", Requested: []string{"fs:write:/project"}}
			done := make(chan sessions.ProviderApprovalDecision, 1)
			go func() {
				decision, _ := (providerApprovalAdapter{bridge: eng.approvalBridge}).Approve(ctx, tenant, request)
				done <- decision
			}()
			for {
				items, _, err := eng.engineApprovals.List(ctx, tenant, "sessions.provider.approval", nbPending, "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) > 0 {
					break
				}
				select {
				case decision := <-done:
					t.Fatalf("provider approval refused before the queue: %q", decision.Reason)
				case <-time.After(10 * time.Millisecond):
				}
			}
			cancel()
			<-done
		})
	}
}

func TestBootApprovalBridgeConfiguredRequest(t *testing.T) {
	var token string
	eng, h := bootBridgeTenant(t, func(h *harness) approvalBridgeConfig {
		token = h.mintBoundToken(t, auth.RoleEditor)
		return approvalBridgeConfig{
			Tenants:          []approvalBridgeTenant{{Tenant: h.tenantA, Token: token}},
			ExpiresInSeconds: 900, EscalateInSeconds: 60,
		}
	})
	principal, err := eng.authr.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := eng.approvalBridge.DeployGate().Request(t.Context(), deploy.ApprovalRequest{
		Tenant: model.TenantID(h.tenantA), Action: "deploy.apply", SubjectKind: "deployment", SubjectRef: "bridge-test", PlanHash: "test-plan",
	})
	if err != nil || decision.Status != deploy.StatusPending || decision.ApprovalRef == "" || decision.Allowed() {
		t.Fatalf("configured request status=%q err=%v, want pending approval", decision.Status, err)
	}
	approval := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+decision.ApprovalRef)
	if approval["requested_by"] != principal.Actor() {
		t.Fatal("approval did not use the configured service identity")
	}
	expires, err := time.Parse(time.RFC3339Nano, approval["expires_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	escalates, err := time.Parse(time.RFC3339Nano, approval["escalate_at"].(string))
	if err != nil || expires.Sub(escalates) != 840*time.Second {
		t.Fatalf("approval did not retain configured time windows: %v", err)
	}
	if code, _ := h.decide(t, token, decision.ApprovalRef, "approve"); code != http.StatusForbidden {
		t.Fatalf("service token decision = %d, want 403", code)
	}
}
