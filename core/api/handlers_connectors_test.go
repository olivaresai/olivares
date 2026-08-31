// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// fakeOnboarding is a capturing test double for the console connector-onboarding
// surface: it records the inputs the handlers pass and returns canned outputs, so the
// handler tests exercise transport + the superadmin/AAL3 gate without a runtime.
type fakeOnboarding struct {
	connectors []api.ConnectorInfo
	testErr    error
	putErr     error
	gotPut     api.ConnectorOnboardInput
	gotTest    api.ConnectorOnboardInput
	gotDelete  string
}

func (f *fakeOnboarding) ListConnectors(context.Context) ([]api.ConnectorInfo, error) {
	return f.connectors, nil
}

func (f *fakeOnboarding) TestConnector(_ context.Context, _ auth.Principal, in api.ConnectorOnboardInput) error {
	f.gotTest = in
	return f.testErr
}

func (f *fakeOnboarding) PutConnector(_ context.Context, _ auth.Principal, in api.ConnectorOnboardInput) (api.SourceApplyResult, error) {
	f.gotPut = in
	if f.putErr != nil {
		return api.SourceApplyResult{}, f.putErr
	}
	return api.SourceApplyResult{Name: in.Name, Action: "added", Persisted: true, Applied: true}, nil
}

func (f *fakeOnboarding) DeleteConnector(_ context.Context, _ auth.Principal, name string) (api.SourceApplyResult, error) {
	f.gotDelete = name
	return api.SourceApplyResult{Name: name, Action: "removed", Persisted: true, Applied: true}, nil
}

func newConnectorsHarness(t *testing.T, fake *fakeOnboarding) *harness {
	return newHarnessOpts(t, func(o *api.Options) { o.ConnectorOnboarding = fake })
}

func TestConnectorsCatalogAndCRUD(t *testing.T) {
	fake := &fakeOnboarding{connectors: []api.ConnectorInfo{
		{Kind: "vault", Title: "Vault", Transport: "in_process", FieldsKnown: true, Fields: []api.ConnectorField{
			{Key: "base_url", Type: "string", Required: true},
			{Key: "token", Type: "string", Secret: true},
		}},
		{Kind: "claude", Transport: "plugin", FieldsKnown: false},
	}}
	h := newConnectorsHarness(t, fake)
	admin := h.adminLogin()

	// GET catalog (superadmin, NO AAL3 for a read): returns the connector kinds + fields.
	r := h.do("GET", "/v1/console/connectors", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("get catalog = %d %s", r.code, r.raw)
	}
	conns, _ := r.body["connectors"].([]any)
	if len(conns) != 2 {
		t.Fatalf("catalog = %v, want 2", conns)
	}
	first := conns[0].(map[string]any)
	if first["kind"] != "vault" || first["fields_known"] != true {
		t.Fatalf("catalog[0] = %v", first)
	}
	if flds, _ := first["fields"].([]any); len(flds) != 2 {
		t.Fatalf("vault fields = %v, want 2", first["fields"])
	}

	// PUT without AAL3 is refused (secret-bearing write).
	put := map[string]any{"name": "vault-prod", "kind": "vault", "tenant": "acme", "enabled": true,
		"config": map[string]any{"base_url": "https://v:8200"}, "secrets": map[string]any{"token": "hvs.secret"}}
	if r := h.do("PUT", "/v1/console/connectors", admin, put, nil); r.code != http.StatusForbidden ||
		r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("put at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	h.elevate(admin)
	r = h.do("PUT", "/v1/console/connectors", admin, put, nil)
	if r.code != http.StatusOK || r.body["persisted"] != true {
		t.Fatalf("put connector = %d %s", r.code, r.raw)
	}
	// The handler forwarded the inline secret to the onboarding service (which seals it);
	// the response itself carries no secret value.
	if fake.gotPut.Secrets["token"] != "hvs.secret" || fake.gotPut.Kind != "vault" {
		t.Fatalf("onboarding did not receive the input: %+v", fake.gotPut)
	}
	if _, leaked := r.body["secrets"]; leaked {
		t.Fatalf("put response leaked secrets: %v", r.body)
	}

	// POST /test (AAL3) returns ok on success.
	if r := h.do("POST", "/v1/console/connectors/test", admin, put, nil); r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("test connector = %d %s", r.code, r.raw)
	}

	// A failed test surfaces the generic 422 (no secret echoed).
	fake.testErr = api.ErrConnectorTestFailed
	r = h.do("POST", "/v1/console/connectors/test", admin, put, nil)
	if r.code != http.StatusUnprocessableEntity || r.body["error"].(map[string]any)["code"] != "connector_test_failed" {
		t.Fatalf("failed test = %d %s, want 422 connector_test_failed", r.code, r.raw)
	}

	// DELETE forwards the name.
	if r := h.do("DELETE", "/v1/console/connectors", admin, map[string]any{"name": "vault-prod"}, nil); r.code != http.StatusOK {
		t.Fatalf("delete connector = %d %s", r.code, r.raw)
	}
	if fake.gotDelete != "vault-prod" {
		t.Fatalf("delete got name %q", fake.gotDelete)
	}
}

func TestConnectorsSuperadminOnly(t *testing.T) {
	h := newConnectorsHarness(t, &fakeOnboarding{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A tenant admin (not superadmin) cannot read the deployment-wide catalog.
	adminTok := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)
	if r := h.do("GET", "/v1/console/connectors", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin get catalog = %d, want 403 (superadmin-only)", r.code)
	}
	h.elevate(adminTok)
	put := map[string]any{"name": "x", "kind": "vault", "tenant": tenant.String()}
	if r := h.do("PUT", "/v1/console/connectors", adminTok, put, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin put connector = %d, want 403", r.code)
	}
}

func TestConnectorsUnavailableWithoutService(t *testing.T) {
	// The default harness wires no ConnectorOnboarding: the endpoints answer 501, never 500.
	h := newHarness(t)
	admin := h.adminLogin()
	if r := h.do("GET", "/v1/console/connectors", admin, nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("get catalog with no service = %d, want 501", r.code)
	}
	h.elevate(admin)
	if r := h.do("PUT", "/v1/console/connectors", admin, map[string]any{"name": "x", "kind": "vault", "tenant": "acme"}, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("put with no service = %d, want 501", r.code)
	}
}
