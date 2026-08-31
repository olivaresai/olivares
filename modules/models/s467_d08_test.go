// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"crypto/ed25519"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/models"
)

// TestD08DeploymentAdmissionBypass proves the D-08 fix: signed-model admission can
// no longer be evaded by creating/editing a deployment WITHOUT a version_ref. The gate is
// discriminated by an EXPLICIT deployment_type, never by the absence of refs.
//
//	require_signed=true + active deployment with no version → DENIED (unclassified, 422)
//	a version of another model (A owns, version of B)       → DENIED (membership, 400)
//	a full-replace PUT dropping the refs of a local-active  → REJECTED, version NOT blanked
//	an explicit brokered deployment                         → still ALLOWED (never gated)
func TestD08DeploymentAdmissionBypass(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	_, priv, _ := ed25519.GenerateKey(nil)
	bundle, pubPEM, _ := omsBareKeyBundle(t, "weights.bin", "w", priv)

	// Opt INTO deny-closed enforcement (the estate that D-08 must protect).
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{
		"require_signed": true, "trusted_keys": []string{pubPEM},
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("set policy = %d %s", r.code, r.raw)
	}

	// (1) THE BYPASS: {name, runtime, active} with no refs used to return 201 and skip the
	// gate. It now resolves to "unclassified" and is deny-closed under require_signed.
	if r := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "bypass-attempt", "runtime": "vllm", "status": "active",
	}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("no-version active deploy under require_signed = %d, want 422 (the D-08 bypass) %s", r.code, r.raw)
	}

	// (2) LINEAGE FALSIFICATION: model A + admitted version of model B; associating A with
	// B's version is refused by the membership check (independent of B being admitted).
	ownedA, _ := h.seedOwnedAndVersion(editor, tenant, "model-A")
	ownedB, verB := h.seedOwnedAndVersion(editor, tenant, "model-B")
	if r := h.do("POST", mbase+"/model-versions/"+verB+"/admit", editor, map[string]any{"bundle": bundle}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit B = %d admitted=%v %s", r.code, r.body["admitted"], r.raw)
	}
	if r := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "forged-lineage", "runtime": "vllm", "deployment_type": "local",
		"owned_ref": ownedA, "version_ref": verB,
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("A-owns-B's-admitted-version = %d, want 400 membership (%s)", r.code, r.raw)
	}
	// Sanity: the SAME admitted version under its true owner B is accepted.
	if r := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "true-lineage", "runtime": "vllm", "deployment_type": "local",
		"owned_ref": ownedB, "version_ref": verB,
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("B-owns-B's-admitted-version = %d, want 201 (%s)", r.code, r.raw)
	}

	// (3) ANTI-BLANKING: create a local deployment, admit its version, then a full-replace
	// PUT that DROPS the refs must be refused — never silently blank the served version.
	ownedC, verC := h.seedOwnedAndVersion(editor, tenant, "model-C")
	if r := h.do("POST", mbase+"/model-versions/"+verC+"/admit", editor, map[string]any{"bundle": bundle}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("admit C = %d %s", r.code, r.raw)
	}
	dep := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "svc-c", "runtime": "vllm", "deployment_type": "local", "owned_ref": ownedC, "version_ref": verC,
	}, tenantHdr(tenant))
	if dep.code != http.StatusCreated {
		t.Fatalf("create local C = %d %s", dep.code, dep.raw)
	}
	depID := dep.body["id"].(string)
	if dep.body["deployment_type"] != "local" {
		t.Errorf("stored deployment_type = %v, want local", dep.body["deployment_type"])
	}
	// PUT full-replace without refs → 400, and the version_ref stays intact.
	if r := h.do("PUT", mbase+"/inference-deployments/"+depID, editor, map[string]any{
		"name": "svc-c", "runtime": "vllm", "status": "active",
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("full-replace PUT dropping refs = %d, want 400 anti-blank (%s)", r.code, r.raw)
	}
	if g := h.do("GET", mbase+"/inference-deployments?status=active", editor, nil, tenantHdr(tenant)); g.code == http.StatusOK {
		found := false
		for _, it := range items(g) {
			row := it.(map[string]any)
			if row["id"] == depID {
				found = true
				if row["version_ref"] != verC {
					t.Errorf("version_ref after refused PUT = %v, want %s (must NOT be blanked)", row["version_ref"], verC)
				}
			}
		}
		if !found {
			t.Errorf("deployment %s missing after refused PUT", depID)
		}
	}

	// (4) BROKERED still works: a hosted-provider deployment carries no version and is never
	// gated, even under require_signed — it must name its provider explicitly.
	if r := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "claude-broker", "runtime": "other", "deployment_type": "brokered",
		"endpoint_ref": "https://api.anthropic.com", "status": "active",
	}, tenantHdr(tenant)); r.code != http.StatusCreated || r.body["deployment_type"] != "brokered" {
		t.Fatalf("brokered deploy under require_signed = %d type=%v, want 201 brokered (%s)", r.code, r.body["deployment_type"], r.raw)
	}
	// A brokered deployment may NOT smuggle a self-hosted version_ref (contract contradiction).
	if r := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "broker-smuggle", "runtime": "other", "deployment_type": "brokered",
		"endpoint_ref": "https://api.anthropic.com", "version_ref": verC,
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("brokered with version_ref = %d, want 400 contradiction (%s)", r.code, r.raw)
	}
	// A brokered deployment with no provider is not a valid positive declaration.
	if r := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "broker-empty", "runtime": "other", "deployment_type": "brokered",
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("brokered with no endpoint_ref = %d, want 400 (%s)", r.code, r.raw)
	}
}

// TestD08UnclassifiedObserveMode proves the migration doctrine: an unclassified deployment
// (the pre ambiguous shape) is TOLERATED under observe mode (the existing all-unsigned
// estate never breaks) but deny-closed the moment require_signed is enforced.
func TestD08UnclassifiedObserveMode(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	// Observe mode (no policy): a ref-less active deployment is allowed but stored as the
	// honest "unclassified" — never silently promoted to a gate-skipping type.
	obs := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "legacy-shape", "runtime": "vllm", "status": "active",
	}, tenantHdr(tenant))
	if obs.code != http.StatusCreated || obs.body["deployment_type"] != "unclassified" {
		t.Fatalf("observe-mode ref-less deploy = %d type=%v, want 201 unclassified (%s)", obs.code, obs.body["deployment_type"], obs.raw)
	}

	_, priv, _ := ed25519.GenerateKey(nil)
	_, pubPEM, _ := omsBareKeyBundle(t, "weights.bin", "w", priv)
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{
		"require_signed": true, "trusted_keys": []string{pubPEM},
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("set policy = %d %s", r.code, r.raw)
	}
	// Under enforcement, a NEW unclassified active row is deny-closed.
	if r := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "legacy-shape-2", "runtime": "vllm", "status": "active",
	}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("unclassified active under require_signed = %d, want 422 (%s)", r.code, r.raw)
	}
}

// TestD08ReverseRefDelete proves a model / version still referenced by a deployment cannot
// be deleted (409) — the reverse-ref integrity that stops a deployment from being left with
// a dangling model/version reference the admission gate could no longer re-check.
func TestD08ReverseRefDelete(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	ownedID, verID := h.seedOwnedAndVersion(editor, tenant, "pinned")
	dep := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "svc", "runtime": "vllm", "deployment_type": "local", "owned_ref": ownedID, "version_ref": verID,
	}, tenantHdr(tenant))
	if dep.code != http.StatusCreated {
		t.Fatalf("create local = %d %s", dep.code, dep.raw)
	}
	depID := dep.body["id"].(string)

	if r := h.do("DELETE", mbase+"/model-versions/"+verID, editor, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("delete referenced version = %d, want 409 (%s)", r.code, r.raw)
	}
	if r := h.do("DELETE", mbase+"/owned-models/"+ownedID, editor, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("delete referenced model = %d, want 409 (%s)", r.code, r.raw)
	}
	// Remove the deployment, then the references delete cleanly (order: version, then model).
	if r := h.do("DELETE", mbase+"/inference-deployments/"+depID, editor, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete deployment = %d, want 204 (%s)", r.code, r.raw)
	}
	if r := h.do("DELETE", mbase+"/model-versions/"+verID, editor, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete now-unreferenced version = %d, want 204 (%s)", r.code, r.raw)
	}
	if r := h.do("DELETE", mbase+"/owned-models/"+ownedID, editor, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete now-unreferenced model = %d, want 204 (%s)", r.code, r.raw)
	}
}
