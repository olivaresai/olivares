// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// newSecretsHarness wires a server WITH the runtime secret store over the
// reversible test sealer (fakeSealer, from handlers_console_test.go).
func newSecretsHarness(t *testing.T) *harness {
	return newHarnessOpts(t, func(o *api.Options) {
		o.SecretStore = auth.NewSecretStore(o.Store, fakeSealer{})
	})
}

func TestSecretsGlobalScopeRefusesSessionEnvWrites(t *testing.T) {
	h := newSecretsHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	for _, name := range []string{"env/x", " \t env/x \n"} {
		t.Run(name, func(t *testing.T) {
			got := h.do("PUT", "/v1/console/secrets", admin, map[string]any{
				"name": name, "value": "fixture-session-value",
			}, nil)
			if got.code != http.StatusBadRequest {
				t.Fatalf("global env/ PUT = %d %s, want 400", got.code, got.raw)
			}
			errBody := got.body["error"].(map[string]any)
			message, _ := errBody["message"].(string)
			if errBody["code"] != "bad_request" || !strings.Contains(message, "New session > More options > Manage session secrets") ||
				!strings.Contains(message, "scope=tenant") {
				t.Fatalf("refusal must name the tenant store: %s", got.raw)
			}
			if strings.Contains(got.raw, "fixture-session-value") {
				t.Fatal("refusal leaked the value")
			}
		})
	}
	listed := h.do("GET", "/v1/console/secrets", admin, nil, nil)
	if listed.code != http.StatusOK || len(listed.body["secrets"].([]any)) != 0 {
		t.Fatalf("refused write persisted a global secret: %s", listed.raw)
	}
}

func TestSecretsGlobalLegacySessionEnvRemainsReadableAndDeletable(t *testing.T) {
	h := newSecretsHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	// Seed through the store to represent an existing deployment-wide connector
	// reference. The API guard must not change store:<name> resolution or DELETE.
	svc := auth.NewSecretStore(h.st, fakeSealer{})
	ctx := context.Background()
	actor := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), Superadmin: true}
	view, err := svc.Put(ctx, actor, auth.GlobalSecretScope, "env/legacy", "fixture-legacy-value", "existing connector")
	if err != nil {
		t.Fatal(err)
	}
	listed := h.do("GET", "/v1/console/secrets", admin, nil, nil)
	items, _ := listed.body["secrets"].([]any)
	if listed.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["name"] != view.Name {
		t.Fatalf("legacy secret is not listed: %s", listed.raw)
	}
	if strings.Contains(listed.raw, "fixture-legacy-value") {
		t.Fatal("list leaked the value")
	}
	got := h.do("PUT", "/v1/console/secrets", admin, map[string]any{
		"name": view.Name, "value": "fixture-replacement-value",
	}, nil)
	if got.code != http.StatusBadRequest {
		t.Fatalf("global env/ update = %d %s, want 400", got.code, got.raw)
	}
	value, err := svc.Resolve(ctx, auth.GlobalSecretScope, view.Name)
	if err != nil || string(value) != "fixture-legacy-value" {
		t.Fatal("refused update changed legacy resolution")
	}
	deleted := h.do("DELETE", "/v1/console/secrets", admin, map[string]any{"name": view.Name}, nil)
	if deleted.code != http.StatusNoContent {
		t.Fatalf("legacy DELETE = %d %s, want 204", deleted.code, deleted.raw)
	}
	if _, err := svc.Resolve(ctx, auth.GlobalSecretScope, view.Name); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Fatalf("deleted secret still resolves: %v", err)
	}
}

func TestSecretsConsoleLifecycle(t *testing.T) {
	h := newSecretsHarness(t)
	h.requirePasskeyStepUp()
	admin := h.adminLogin()

	// GET (superadmin, no AAL3 for a read) before any secret.
	r := h.do("GET", "/v1/console/secrets", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("get secrets = %d %s", r.code, r.raw)
	}
	if r.body["sealer_available"] != true {
		t.Fatalf("sealer_available = %v, want true", r.body["sealer_available"])
	}
	if secs, _ := r.body["secrets"].([]any); len(secs) != 0 {
		t.Fatalf("initial secrets = %v, want empty", secs)
	}

	// PUT without AAL3 is refused (privilege-shaped, secret-bearing).
	put := map[string]any{"name": "gdrive/token", "value": "s3cr3t-value", "description": "GDrive ingest"}
	if r := h.do("PUT", "/v1/console/secrets", admin, put, nil); r.code != http.StatusForbidden ||
		r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("put secret at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	h.elevate(admin)
	r = h.do("PUT", "/v1/console/secrets", admin, put, nil)
	if r.code != http.StatusOK {
		t.Fatalf("put secret = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "gdrive/token" || r.body["description"] != "GDrive ingest" {
		t.Fatalf("put view = %v", r.body)
	}
	// The value is NEVER returned; only a non-secret hint.
	if _, leaked := r.body["value"]; leaked {
		t.Fatalf("PUT response leaked the secret value: %v", r.body)
	}
	hint, _ := r.body["hint"].(string)
	if hint == "" {
		t.Fatalf("expected a hint; %v", r.body)
	}

	// GET lists it (no value).
	r = h.do("GET", "/v1/console/secrets", admin, nil, nil)
	secs, _ := r.body["secrets"].([]any)
	if len(secs) != 1 {
		t.Fatalf("secrets list = %v, want 1", secs)
	}
	one := secs[0].(map[string]any)
	if one["name"] != "gdrive/token" || one["hint"] != hint {
		t.Fatalf("list entry = %v", one)
	}
	if _, leaked := one["value"]; leaked {
		t.Fatalf("list leaked the secret value: %v", one)
	}

	// Editing with an EMPTY value keeps the stored secret (hint unchanged), updates
	// the description.
	edit := map[string]any{"name": "gdrive/token", "value": "", "description": "edited"}
	r = h.do("PUT", "/v1/console/secrets", admin, edit, nil)
	if r.code != http.StatusOK || r.body["hint"].(string) != hint || r.body["description"] != "edited" {
		t.Fatalf("empty-value edit = %d %s (hint changed?)", r.code, r.raw)
	}

	// Rotating the value changes the hint.
	rot := map[string]any{"name": "gdrive/token", "value": "rotated-value", "description": "edited"}
	r = h.do("PUT", "/v1/console/secrets", admin, rot, nil)
	if r.code != http.StatusOK || r.body["hint"].(string) == hint {
		t.Fatalf("rotate did not change the hint: %s", r.raw)
	}

	// DELETE removes it.
	if r := h.do("DELETE", "/v1/console/secrets", admin, map[string]any{"name": "gdrive/token"}, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete secret = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/console/secrets", admin, nil, nil)
	if secs, _ := r.body["secrets"].([]any); len(secs) != 0 {
		t.Fatalf("after delete secrets = %v, want empty", secs)
	}
}

func TestSecretsConsoleSuperadminOnly(t *testing.T) {
	h := newSecretsHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A tenant admin (not superadmin) cannot read or write the deployment-wide store.
	adminTok := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)
	if r := h.do("GET", "/v1/console/secrets", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin get secrets = %d, want 403 (superadmin-only)", r.code)
	}
	h.elevate(adminTok)
	if r := h.do("PUT", "/v1/console/secrets", adminTok, map[string]any{"name": "x", "value": "v"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin put secret = %d, want 403", r.code)
	}
}

func TestSecretsConsoleUnavailableWithoutService(t *testing.T) {
	// The default harness wires no SecretStore: the endpoints answer 501, never 500.
	h := newHarness(t)
	admin := h.adminLogin()
	if r := h.do("GET", "/v1/console/secrets", admin, nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("get secrets with no store = %d, want 501", r.code)
	}
}

func TestSecretsTenantScopeDoesNotGrantGlobalAuthority(t *testing.T) {
	h := newSecretsHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "mcp-secrets")
	foreign := h.createOrg(root, "mcp-secrets-foreign")
	admin := h.mkMember(root, "mcp-secret-admin@test.io", "fixturepass1", auth.RoleAdmin, tenant)
	viewer := h.mkMember(root, "mcp-secret-viewer@test.io", "fixturepass1", auth.RoleViewer, tenant)
	// The AAL1 refusal below is the passkey step-up's (the default policy asks for none);
	// members are added first because adding a person asks for the same step-up.
	h.requirePasskeyStepUp()
	path := "/v1/console/secrets?scope=tenant"
	body := map[string]any{"name": "mcp/example", "value": "Bearer fixture-tenant-only"}
	if got := h.do("PUT", path, admin, body, tenantHdr(tenant)); got.code != 403 {
		t.Fatalf("AAL1 write=%d", got.code)
	}
	h.elevate(admin)
	if got := h.do("PUT", path, viewer, body, tenantHdr(tenant)); got.code != 403 {
		t.Fatal("viewer write admitted")
	}
	if got := h.do("PUT", path, admin, body, tenantHdr(foreign)); got.code != 403 {
		t.Fatalf("foreign tenant write=%d", got.code)
	}
	if got := h.do("PUT", "/v1/console/secrets", admin, body, tenantHdr(tenant)); got.code != 403 {
		t.Fatal("tenant admin gained global write")
	}
	if got := h.do("PUT", path, admin, body, tenantHdr(tenant)); got.code != 200 {
		t.Fatalf("own tenant put=%d %s", got.code, got.raw)
	}
	own := h.do("GET", path, admin, nil, tenantHdr(tenant))
	if own.code != 200 || len(own.body["secrets"].([]any)) != 1 {
		t.Fatalf("tenant refs=%d %s", own.code, own.raw)
	}
	global := h.do("GET", "/v1/console/secrets", root, nil, nil)
	if global.code != 200 || len(global.body["secrets"].([]any)) != 0 {
		t.Fatal("tenant secret entered global scope")
	}
	other := h.do("GET", path, root, nil, tenantHdr(foreign))
	if other.code != 200 || len(other.body["secrets"].([]any)) != 0 {
		t.Fatal("tenant secret crossed scopes")
	}
	if _, leaked := own.body["secrets"].([]any)[0].(map[string]any)["value"]; leaked {
		t.Fatal("secret value returned")
	}
	if got := h.do("GET", "/v1/console/secrets?scope=unknown", admin, nil, tenantHdr(tenant)); got.code != 400 {
		t.Fatalf("unknown scope=%d", got.code)
	}
}

// Design FH 016: the tenant scope also holds the secrets sessions receive as
// environment variables (env/…), managed by the same tenant administrator. Every
// other tenant namespace (provider records under an internal design note (not shipped)) stays out.
func TestSecretsTenantScopeHoldsSessionEnvSecrets(t *testing.T) {
	h := newSecretsHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "env-secrets")
	admin := h.mkMember(root, "env-secret-admin@test.io", "fixturepass1", auth.RoleAdmin, tenant)
	h.elevate(admin)
	path := "/v1/console/secrets?scope=tenant"
	put := h.do("PUT", path, admin, map[string]any{"name": "env/github", "value": "ghp_fixture_not_a_token_0123"}, tenantHdr(tenant))
	if put.code != 200 {
		t.Fatalf("env/ put=%d %s", put.code, put.raw)
	}
	if got := h.do("PUT", path, admin, map[string]any{"name": "sessions/provider/x", "value": "v-0123456789"}, tenantHdr(tenant)); got.code != 400 {
		t.Fatalf("provider namespace put=%d, want 400", got.code)
	}
	listed := h.do("GET", path, admin, nil, tenantHdr(tenant))
	items, _ := listed.body["secrets"].([]any)
	if listed.code != 200 || len(items) != 1 || items[0].(map[string]any)["name"] != "env/github" {
		t.Fatalf("tenant list=%d %s", listed.code, listed.raw)
	}
	if _, leaked := items[0].(map[string]any)["value"]; leaked {
		t.Fatal("secret value returned")
	}
}
