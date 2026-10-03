// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// HU-28: adding a provider key follows the deployment's administrative step-up, as adding a
// person does. Under the passkey policy a password-only session is refused with
// step_up_required and nothing is stored; under the default policy (none) it is added as before.
func TestAddingAProviderFollowsTheStepUpPolicy(t *testing.T) {
	add := func(h *harness, admin string, tenant model.TenantID) resp {
		return h.doJSON("POST", "/v1/m/sessions/providers", admin,
			map[string]any{"kind": "anthropic", "display_name": "Team key", "api_key": testProviderKey}, tenantHdr(tenant))
	}

	strict := newHarness(t, New(WithProviderSecretVault(newFakeVault())))
	if err := strict.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		_, err := as.AuthPolicy().Create(context.Background(), model.AuthPolicy{AdminStepUp: auth.StepUpPasskey})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	admin := strict.adminLogin()
	tenant := strict.createOrg(admin, "acme")
	r := add(strict, admin, tenant)
	if r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("add a provider without the passkey step-up = %d %s, want 403 step_up_required", r.code, r.raw)
	}
	list := strict.doJSON("GET", "/v1/m/sessions/providers", admin, nil, tenantHdr(tenant))
	if items, _ := list.body["items"].([]any); list.code != http.StatusOK || len(items) != 0 {
		t.Fatalf("a refused add stored a provider: %d %s", list.code, list.raw)
	}

	// Replacing the key or the address asks for it too; a rename does not.
	rec := mustCreateRecord(t, strict.m, tenant, anthropicInput("Team key"))
	patch := func(h *harness, admin string, tenant model.TenantID, ref string, body map[string]any) resp {
		return h.doJSON("PATCH", "/v1/m/sessions/providers/"+ref, admin, body, tenantHdr(tenant))
	}
	for name, body := range map[string]map[string]any{
		"rotate the key":     {"api_key": "sk-ant-api03-ROTATED-0123456789-WXYZ"},
		"change the address": {"base_url": "https://anthropic-gw.example.com"},
	} {
		if r := patch(strict, admin, tenant, rec.Ref, body); r.code != http.StatusForbidden || r.body["error"].(map[string]any)["code"] != "step_up_required" {
			t.Fatalf("%s without the passkey step-up = %d %s, want 403 step_up_required", name, r.code, r.raw)
		}
	}
	if r := patch(strict, admin, tenant, rec.Ref, map[string]any{"display_name": "Renamed"}); r.code != http.StatusOK {
		t.Fatalf("rename without the step-up = %d %s, want 200", r.code, r.raw)
	}

	open := newHarness(t, New(WithProviderSecretVault(newFakeVault())))
	admin = open.adminLogin()
	tenant = open.createOrg(admin, "acme")
	r = add(open, admin, tenant)
	if r.code != http.StatusCreated {
		t.Fatalf("add a provider under the default policy = %d %s, want 201", r.code, r.raw)
	}
	if r := patch(open, admin, tenant, r.body["provider_ref"].(string), map[string]any{"api_key": "sk-ant-api03-ROTATED-0123456789-WXYZ"}); r.code != http.StatusOK {
		t.Fatalf("rotate the key under the default policy = %d %s, want 200", r.code, r.raw)
	}
}
