// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/finops"
)

func TestAdmissionHTTPVerticalSlice(t *testing.T) {
	m := finops.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "admit")
	tok := h.roleToken(admin, tenant, "ed@x.io", "editor")

	create := h.do("POST", "/v1/m/finops/budgets", tok, map[string]any{
		"name": "cap", "enabled": true, "dimension": "global", "period": "monthly",
		"limit_micro_usd": int64(2_000_000), "action": "block",
	}, tenantHdr(tenant))
	if create.code != http.StatusCreated {
		t.Fatalf("create budget = %d %s", create.code, create.raw)
	}

	reserve := h.do("POST", "/v1/m/finops/admission/reserve", tok, map[string]any{
		"scope": "model_gateway", "estimate_micro_usd": int64(1_000_000),
		"idempotency_key": "http-1",
	}, tenantHdr(tenant))
	if reserve.code != http.StatusOK {
		t.Fatalf("reserve = %d %s", reserve.code, reserve.raw)
	}
	if allowed, _ := reserve.body["allowed"].(bool); !allowed {
		t.Fatalf("reserve must admit under cap: %s", reserve.raw)
	}
	handle, _ := reserve.body["handle"].(string)

	retry := h.do("POST", "/v1/m/finops/admission/reserve", tok, map[string]any{
		"scope": "model_gateway", "estimate_micro_usd": int64(1_000_000),
		"idempotency_key": "http-1",
	}, tenantHdr(tenant))
	if retry.code != http.StatusOK {
		t.Fatalf("retry = %d %s", retry.code, retry.raw)
	}
	if replayed, _ := retry.body["replayed"].(bool); !replayed {
		t.Fatalf("retry must replay: %s", retry.raw)
	}

	deny := h.do("POST", "/v1/m/finops/admission/reserve", tok, map[string]any{
		"scope": "model_gateway", "estimate_micro_usd": int64(2_000_000),
		"idempotency_key": "http-2",
	}, tenantHdr(tenant))
	if deny.code != http.StatusPaymentRequired {
		t.Fatalf("over-cap deny = %d %s", deny.code, deny.raw)
	}

	commit := h.do("POST", "/v1/m/finops/admission/commit", tok, map[string]any{
		"handle": handle, "actual_micro_usd": int64(500_000),
	}, tenantHdr(tenant))
	if commit.code != http.StatusOK {
		t.Fatalf("commit = %d %s", commit.code, commit.raw)
	}

	recon := h.do("GET", "/v1/m/finops/admission/reconciliation", tok, nil, tenantHdr(tenant))
	if recon.code != http.StatusOK {
		t.Fatalf("reconciliation = %d %s", recon.code, recon.raw)
	}
}

// TestAdmissionReserveBodyIsTheNamesTheSchemaPublishes is the witness for the
// published request body. The handler decodes with DisallowUnknownFields, so a
// field name the schema does not carry is a 400 — which makes the schema the
// contract and not a sketch, and makes a silent rename of a DTO field a
// breaking change that this test reports instead of the first client to try it.
func TestAdmissionReserveBodyIsTheNamesTheSchemaPublishes(t *testing.T) {
	m := finops.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "names")
	tok := h.roleToken(admin, tenant, "ed@x.io", "editor")

	ok := h.do("POST", "/v1/m/finops/admission/reserve", tok, map[string]any{
		"scope": "model_gateway", "idempotency_key": "names-1",
		"estimate_micro_usd": int64(1_000),
		"dims": map[string]any{
			"provider_ref": "anthropic", "model_ref": "m-1", "workspace_ref": "w-1",
			"user_group_refs": []string{"g-1"},
		},
	}, tenantHdr(tenant))
	if ok.code != http.StatusOK {
		t.Fatalf("the names the schema publishes must be accepted: %d %s", ok.code, ok.raw)
	}

	// The Go field names are NOT the wire names, which is the whole point of the
	// tags: without them this body would be the accepted one.
	goNames := h.do("POST", "/v1/m/finops/admission/reserve", tok, map[string]any{
		"scope": "model_gateway", "idempotency_key": "names-2",
		"dims": map[string]any{"ProviderRef": "anthropic"},
	}, tenantHdr(tenant))
	if goNames.code != http.StatusBadRequest {
		t.Fatalf("an unpublished dims field must be refused: %d %s", goNames.code, goNames.raw)
	}
}
