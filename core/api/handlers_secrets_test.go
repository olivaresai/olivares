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

// newSecretsHarness wires a server WITH the runtime secret store over the
// reversible test sealer (fakeSealer, from handlers_console_test.go).
func newSecretsHarness(t *testing.T) *harness {
	return newHarnessOpts(t, func(o *api.Options) {
		o.SecretStore = auth.NewSecretStore(o.Store, fakeSealer{})
	})
}

func TestSecretsConsoleLifecycle(t *testing.T) {
	h := newSecretsHarness(t)
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
