// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

const testProfileRevision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeExecutionProfileResolver struct {
	profiles map[string]models.ExecutionProfile
	calls    int
}

func profileKey(tenant model.TenantID, ref, revision string) string {
	return tenant.String() + "\x00" + ref + "\x00" + revision
}

func (r *fakeExecutionProfileResolver) ResolveExecutionProfile(_ context.Context, tenant model.TenantID, ref, revision string) (models.ExecutionProfile, error) {
	r.calls++
	p, ok := r.profiles[profileKey(tenant, ref, revision)]
	if !ok {
		return models.ExecutionProfile{}, models.ErrExecutionProfileUnavailable
	}
	return p, nil
}

func testExecutionProfile(tenant model.TenantID) models.ExecutionProfile {
	return models.ExecutionProfile{
		Tenant: tenant, Ref: "chat-primary", Revision: testProfileRevision,
		Action: models.ExecutionActionTextGenerate, Protocol: models.ExecutionProtocolChatTextV1,
		AdapterID: models.ExecutionAdapterModelProviderChat, AdapterVersion: models.ExecutionAdapterVersion1,
		ProviderRef: "anthropic", ModelRef: "claude-opus-4-8",
		Endpoint: "https://gateway.example.invalid/v1/chat/completions",
		Surface:  "direct", InferenceGeo: "us", CredentialAudience: "https://gateway.example.invalid",
		AuthScheme: "bearer", TransportKey: "opaque-test-key", MaxRequestBytes: 1 << 20,
		MaxResponseBytes: 8 << 20, Timeout: 30 * time.Second,
	}
}

func profilePolicyBody(overrides map[string]any) map[string]any {
	body := map[string]any{
		"name": "profiled", "enabled": true, "strategy": "pinned",
		"pinned_model": "claude-opus-4-8", "execution_profile_ref": "chat-primary",
		"execution_profile_revision": testProfileRevision,
	}
	for k, v := range overrides {
		body[k] = v
	}
	return body
}

func responseErrorCode(r resp) string {
	errorObject, _ := r.body["error"].(map[string]any)
	code, _ := errorObject["code"].(string)
	return code
}

func TestExecutionProfilePolicyPinsAreAtomicTenantScopedAndExact(t *testing.T) {
	resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
	m := models.New(models.WithExecutionProfileResolver(resolver))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "profile-owner")
	foreign := h.createOrg(admin, "profile-foreign")
	p := testExecutionProfile(tenant)
	resolver.profiles[profileKey(tenant, p.Ref, p.Revision)] = p

	tests := []struct {
		name   string
		tenant model.TenantID
		body   map[string]any
		status int
		code   string
	}{
		{"ref_only", tenant, profilePolicyBody(map[string]any{"execution_profile_revision": ""}), http.StatusBadRequest, "execution_profile_pin_required"},
		{"revision_only", tenant, profilePolicyBody(map[string]any{"execution_profile_ref": ""}), http.StatusBadRequest, "execution_profile_pin_required"},
		{"malformed_revision", tenant, profilePolicyBody(map[string]any{"execution_profile_revision": "sha256:ABC"}), http.StatusBadRequest, "execution_profile_revision_invalid"},
		{"unknown", tenant, profilePolicyBody(map[string]any{"execution_profile_ref": "removed"}), http.StatusServiceUnavailable, "execution_profile_unavailable"},
		{"foreign_tenant", foreign, profilePolicyBody(nil), http.StatusServiceUnavailable, "execution_profile_unavailable"},
		{"model_mismatch", tenant, profilePolicyBody(map[string]any{"pinned_model": "claude-sonnet-4-6"}), http.StatusUnprocessableEntity, "profile_binding_mismatch"},
		{"endpoint_mismatch", tenant, profilePolicyBody(map[string]any{"gateway_endpoint": "https://other.example.invalid/v1/chat/completions"}), http.StatusUnprocessableEntity, "profile_binding_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := h.do("POST", "/v1/m/models/routing-policies", admin, tt.body, tenantHdr(tt.tenant))
			if r.code != tt.status || responseErrorCode(r) != tt.code {
				t.Fatalf("create = %d %s, want %d/%s", r.code, r.raw, tt.status, tt.code)
			}
		})
	}

	r := h.do("POST", "/v1/m/models/routing-policies", admin, profilePolicyBody(nil), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("valid exact pin = %d %s", r.code, r.raw)
	}
	if r.body["execution_profile_ref"] != p.Ref || r.body["execution_profile_revision"] != p.Revision {
		t.Fatalf("stored policy lost exact pin: %s", r.raw)
	}
	id := r.body["id"].(string)
	badUpdate := profilePolicyBody(map[string]any{"execution_profile_revision": ""})
	updated := h.do("PUT", "/v1/m/models/routing-policies/"+id, admin, badUpdate, tenantHdr(tenant))
	if updated.code != http.StatusBadRequest || responseErrorCode(updated) != "execution_profile_pin_required" {
		t.Fatalf("partial update = %d %s", updated.code, updated.raw)
	}
	unchanged := h.do("GET", "/v1/m/models/routing-policies/"+id, admin, nil, tenantHdr(tenant))
	if unchanged.code != http.StatusOK || unchanged.body["execution_profile_ref"] != p.Ref ||
		unchanged.body["execution_profile_revision"] != p.Revision {
		t.Fatalf("rejected update mutated exact pin: %d %s", unchanged.code, unchanged.raw)
	}
}

func TestExecutionProfileRejectsUnsupportedResolvedMetadata(t *testing.T) {
	executor := &stubExecutor{}
	resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
	m := models.New(models.WithExecutionProfileResolver(resolver), models.WithExecutor(executor))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "profile-metadata")
	base := testExecutionProfile(tenant)
	key := profileKey(tenant, base.Ref, base.Revision)

	tests := []struct {
		name   string
		mutate func(*models.ExecutionProfile)
		code   string
	}{
		{"action", func(p *models.ExecutionProfile) { p.Action = "images.generate" }, "unsupported_operation"},
		{"protocol", func(p *models.ExecutionProfile) { p.Protocol = "responses.text.v1" }, "unsupported_protocol"},
		{"adapter", func(p *models.ExecutionProfile) { p.AdapterVersion = "2" }, "unsupported_protocol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base
			tt.mutate(&p)
			resolver.profiles[key] = p
			body := profilePolicyBody(map[string]any{"name": "unsupported-" + tt.name})
			r := h.do("POST", "/v1/m/models/routing-policies", admin, body, tenantHdr(tenant))
			if r.code != http.StatusUnprocessableEntity || responseErrorCode(r) != tt.code {
				t.Fatalf("create = %d %s, want 422/%s", r.code, r.raw, tt.code)
			}
			if executor.calls != 0 {
				t.Fatalf("executor called %d times", executor.calls)
			}
		})
	}
}

func TestExecutionProfileValidSelectionStopsBeforeLegacyExecutor(t *testing.T) {
	executor := &stubExecutor{res: models.ExecuteResult{Text: "must not run"}}
	resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
	m := models.New(models.WithExecutionProfileResolver(resolver), models.WithExecutor(executor))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "profile-valid")
	p := testExecutionProfile(tenant)
	resolver.profiles[profileKey(tenant, p.Ref, p.Revision)] = p
	seedModel(t, h, tenant, p.ProviderRef, p.ModelRef)

	created := h.do("POST", "/v1/m/models/routing-policies", admin, profilePolicyBody(nil), tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := created.body["id"].(string)

	for _, tc := range []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"implicit_profile_operation", map[string]any{"input": "hello"}, http.StatusServiceUnavailable, "chat_execution_unavailable"},
		{"explicit_profile_operation", map[string]any{"input": "hello", "operation": models.ExecutionActionTextGenerate, "surface": p.Surface}, http.StatusServiceUnavailable, "chat_execution_unavailable"},
		{"unsupported_operation", map[string]any{"input": "hello", "operation": "embeddings.create"}, http.StatusUnprocessableEntity, "unsupported_operation"},
		{"non_exact_operation", map[string]any{"input": "hello", "operation": " text.generate "}, http.StatusUnprocessableEntity, "unsupported_operation"},
		{"surface_mismatch", map[string]any{"input": "hello", "surface": "bedrock"}, http.StatusUnprocessableEntity, "profile_binding_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin, tc.body, tenantHdr(tenant))
			if r.code != tc.status || responseErrorCode(r) != tc.code {
				t.Fatalf("execute = %d %s, want %d/%s", r.code, r.raw, tc.status, tc.code)
			}
			if executor.calls != 0 {
				t.Fatalf("legacy executor called %d times for profiled request", executor.calls)
			}
		})
	}
}

func TestExecutionProfileRejectsRemovedOrMismatchedPrimary(t *testing.T) {
	t.Run("profile_removed_after_policy_authoring", func(t *testing.T) {
		executor := &stubExecutor{}
		resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
		m := models.New(models.WithExecutionProfileResolver(resolver), models.WithExecutor(executor))
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "profile-removed")
		p := testExecutionProfile(tenant)
		key := profileKey(tenant, p.Ref, p.Revision)
		resolver.profiles[key] = p
		seedModel(t, h, tenant, p.ProviderRef, p.ModelRef)
		created := h.do("POST", "/v1/m/models/routing-policies", admin, profilePolicyBody(nil), tenantHdr(tenant))
		delete(resolver.profiles, key)
		r := h.do("POST", "/v1/m/models/routing-policies/"+created.body["id"].(string)+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
		if r.code != http.StatusServiceUnavailable || responseErrorCode(r) != "execution_profile_unavailable" {
			t.Fatalf("removed profile = %d %s", r.code, r.raw)
		}
		if executor.calls != 0 {
			t.Fatalf("executor called %d times", executor.calls)
		}
	})

	t.Run("primary_removed_from_estate", func(t *testing.T) {
		executor := &stubExecutor{}
		resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
		m := models.New(models.WithExecutionProfileResolver(resolver), models.WithExecutor(executor))
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "primary-removed")
		p := testExecutionProfile(tenant)
		resolver.profiles[profileKey(tenant, p.Ref, p.Revision)] = p
		seedModel(t, h, tenant, p.ProviderRef, p.ModelRef)
		created := h.do("POST", "/v1/m/models/routing-policies", admin, profilePolicyBody(nil), tenantHdr(tenant))
		if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			records, _, err := sc.Models().List(context.Background(), model.Query{Limit: 10})
			if err != nil {
				return err
			}
			return sc.Models().Delete(context.Background(), records[0].ID)
		}); err != nil {
			t.Fatal(err)
		}
		r := h.do("POST", "/v1/m/models/routing-policies/"+created.body["id"].(string)+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
		if r.code != http.StatusUnprocessableEntity || responseErrorCode(r) != "profile_binding_mismatch" {
			t.Fatalf("removed primary = %d %s", r.code, r.raw)
		}
		if executor.calls != 0 {
			t.Fatalf("executor called %d times", executor.calls)
		}
	})

	t.Run("provider_mismatch", func(t *testing.T) {
		executor := &stubExecutor{}
		resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
		m := models.New(models.WithExecutionProfileResolver(resolver), models.WithExecutor(executor))
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "provider-mismatch")
		p := testExecutionProfile(tenant)
		resolver.profiles[profileKey(tenant, p.Ref, p.Revision)] = p
		seedModel(t, h, tenant, "not-anthropic", p.ModelRef)
		created := h.do("POST", "/v1/m/models/routing-policies", admin, profilePolicyBody(nil), tenantHdr(tenant))
		r := h.do("POST", "/v1/m/models/routing-policies/"+created.body["id"].(string)+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
		if r.code != http.StatusUnprocessableEntity || responseErrorCode(r) != "profile_binding_mismatch" {
			t.Fatalf("provider mismatch = %d %s", r.code, r.raw)
		}
		if executor.calls != 0 {
			t.Fatalf("executor called %d times", executor.calls)
		}
	})
}

type denyProfileModelScopeGate struct{ modelRef string }

func (g denyProfileModelScopeGate) Allowed(_ context.Context, _ model.TenantID, q models.ScopeQuery) (models.ScopeVerdict, error) {
	return models.ScopeVerdict{Allowed: q.ModelRef != g.modelRef}, nil
}

func TestExecutionProfileRejectsGovernancePromotion(t *testing.T) {
	executor := &stubExecutor{}
	resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
	m := models.New(
		models.WithExecutionProfileResolver(resolver), models.WithExecutor(executor),
		models.WithScopeGate(denyProfileModelScopeGate{modelRef: "claude-opus-4-8"}),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "profile-promotion")
	p := testExecutionProfile(tenant)
	resolver.profiles[profileKey(tenant, p.Ref, p.Revision)] = p
	seedModel(t, h, tenant, p.ProviderRef, p.ModelRef)
	seedModel(t, h, tenant, "fallback-provider", "fallback-model")
	created := h.do("POST", "/v1/m/models/routing-policies", admin, profilePolicyBody(nil), tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	r := h.do("POST", "/v1/m/models/routing-policies/"+created.body["id"].(string)+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
	if r.code != http.StatusUnprocessableEntity || responseErrorCode(r) != "profile_binding_mismatch" {
		t.Fatalf("promoted primary = %d %s", r.code, r.raw)
	}
	if executor.calls != 0 {
		t.Fatalf("executor called %d times", executor.calls)
	}
}

func TestExecutionOperationCannotSelectLegacyExecutor(t *testing.T) {
	executor := &stubExecutor{res: models.ExecuteResult{Text: "legacy"}}
	m := models.New(models.WithExecutor(executor))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "legacy-operation")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin,
		map[string]any{"input": "hello", "operation": models.ExecutionActionTextGenerate}, tenantHdr(tenant))
	if r.code != http.StatusUnprocessableEntity || responseErrorCode(r) != "execution_profile_required" {
		t.Fatalf("operation without profile = %d %s", r.code, r.raw)
	}
	if executor.calls != 0 {
		t.Fatalf("executor called %d times", executor.calls)
	}

	legacy := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin,
		map[string]any{"input": "hello"}, tenantHdr(tenant))
	if legacy.code != http.StatusOK || legacy.body["output"] != "legacy" || executor.calls != 1 {
		t.Fatalf("legacy execute changed = %d %s, calls=%d", legacy.code, legacy.raw, executor.calls)
	}
	if strings.Contains(legacy.raw, "execution_profile") || strings.Contains(legacy.raw, "operation") {
		t.Fatalf("legacy response gained profile fields: %s", legacy.raw)
	}
}
