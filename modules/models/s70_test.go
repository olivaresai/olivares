// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/models"
)

const mbase = "/v1/m/models"

// TestS70OwnModelRegistry exercises module XXIII end to end: an owned model, its
// versions (with lineage), a local inference deployment (the governable entity),
// and a fine-tune job RECORD whose succeeded transition links the version it
// produced. The control plane governs/inventories; it never trains.
func TestS70OwnModelRegistry(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	om := h.do("POST", mbase+"/owned-models", editor, map[string]any{
		"name": "acme-llm", "kind": "fine_tuned", "base_ref": "llama-3.1-8b", "provider_ref": "local-vllm",
	}, tenantHdr(tenant))
	if om.code != http.StatusCreated {
		t.Fatalf("create owned model = %d %s", om.code, om.raw)
	}
	omID, _ := om.body["id"].(string)
	if omID == "" {
		t.Fatal("owned model has no id")
	}

	// A bad kind is rejected (the inventory stays queryable by a closed enum).
	if bad := h.do("POST", mbase+"/owned-models", editor, map[string]any{"name": "x", "kind": "nonsense"}, tenantHdr(tenant)); bad.code != http.StatusBadRequest {
		t.Errorf("bad kind = %d, want 400", bad.code)
	}

	// Two versions with parent lineage.
	v1 := h.do("POST", mbase+"/model-versions", editor, map[string]any{
		"owned_ref": omID, "version": "1.0.0", "artifact_ref": "registry://acme-llm:1.0.0", "status": "active",
	}, tenantHdr(tenant))
	if v1.code != http.StatusCreated {
		t.Fatalf("create v1 = %d %s", v1.code, v1.raw)
	}
	v1ID, _ := v1.body["id"].(string)
	v2 := h.do("POST", mbase+"/model-versions", editor, map[string]any{
		"owned_ref": omID, "version": "1.1.0", "parent_ref": v1ID, "status": "draft",
	}, tenantHdr(tenant))
	if v2.code != http.StatusCreated || v2.body["parent_ref"] != v1ID {
		t.Fatalf("create v2 = %d parent=%v, want 201 with lineage to %s", v2.code, v2.body["parent_ref"], v1ID)
	}
	v2ID, _ := v2.body["id"].(string)

	// A version under a non-existent owned model is rejected.
	if bad := h.do("POST", mbase+"/model-versions", editor, map[string]any{"owned_ref": "00000000-0000-0000-0000-000000000000", "version": "9"}, tenantHdr(tenant)); bad.code == http.StatusCreated {
		t.Error("version under missing owned model should fail")
	}

	// A governable local inference deployment.
	dep := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{
		"name": "acme-llm-prod", "runtime": "vllm", "endpoint_ref": "https://vllm.internal:8000",
		"owned_ref": omID, "version_ref": v1ID, "governed": true,
	}, tenantHdr(tenant))
	if dep.code != http.StatusCreated || dep.body["governed"] != true {
		t.Fatalf("create deployment = %d governed=%v", dep.code, dep.body["governed"])
	}
	if bad := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{"name": "y", "runtime": "nope"}, tenantHdr(tenant)); bad.code != http.StatusBadRequest {
		t.Errorf("bad runtime = %d, want 400", bad.code)
	}
	// A deployment whose owned_ref does not resolve is rejected (referential integrity).
	if bad := h.do("POST", mbase+"/inference-deployments", editor, map[string]any{"name": "d-orphan", "runtime": "vllm", "owned_ref": "00000000-0000-0000-0000-000000000000"}, tenantHdr(tenant)); bad.code == http.StatusCreated {
		t.Errorf("deployment with non-existent owned_ref = %d, want failure", bad.code)
	}

	// A fine-tune job RECORD: running → succeeded, linking the version it produced.
	job := h.do("POST", mbase+"/finetune-jobs", editor, map[string]any{
		"name": "ft-acme-1", "base_ref": "llama-3.1-8b", "dataset_ref": "s3://acme/ds-v3", "runtime": "vllm", "status": "running",
	}, tenantHdr(tenant))
	if job.code != http.StatusCreated {
		t.Fatalf("create job = %d %s", job.code, job.raw)
	}
	jobID, _ := job.body["id"].(string)
	upd := h.do("PUT", mbase+"/finetune-jobs/"+jobID, editor, map[string]any{
		"name": "ft-acme-1", "status": "succeeded", "result_version_ref": v2ID,
	}, tenantHdr(tenant))
	if upd.code != http.StatusOK || upd.body["status"] != "succeeded" || upd.body["result_version_ref"] != v2ID {
		t.Fatalf("job update = %d status=%v result=%v", upd.code, upd.body["status"], upd.body["result_version_ref"])
	}
	// A job linking a non-existent result version is rejected (lineage integrity).
	if bad := h.do("PUT", mbase+"/finetune-jobs/"+jobID, editor, map[string]any{"name": "ft-acme-1", "status": "succeeded", "result_version_ref": "00000000-0000-0000-0000-000000000000"}, tenantHdr(tenant)); bad.code == http.StatusOK {
		t.Errorf("job with non-existent result_version_ref = %d, want failure", bad.code)
	}
	// A job's optional runtime is enum-validated when set.
	if bad := h.do("POST", mbase+"/finetune-jobs", editor, map[string]any{"name": "ft-bad", "runtime": "nope"}, tenantHdr(tenant)); bad.code != http.StatusBadRequest {
		t.Errorf("job bad runtime = %d, want 400", bad.code)
	}

	// Reads: filter owned models by kind; list versions by owned_ref.
	if l := h.do("GET", mbase+"/owned-models?kind=fine_tuned", viewer, nil, tenantHdr(tenant)); l.code != http.StatusOK || len(items(l)) != 1 {
		t.Errorf("list owned (kind=fine_tuned) = %d n=%d", l.code, len(items(l)))
	}
	if l := h.do("GET", mbase+"/model-versions?owned_ref="+omID, viewer, nil, tenantHdr(tenant)); l.code != http.StatusOK || len(items(l)) != 2 {
		t.Errorf("list versions = %d n=%d, want 2", l.code, len(items(l)))
	}
	if l := h.do("GET", mbase+"/finetune-jobs?status=succeeded", viewer, nil, tenantHdr(tenant)); l.code != http.StatusOK || len(items(l)) != 1 {
		t.Errorf("list jobs (succeeded) = %d n=%d", l.code, len(items(l)))
	}

	// A viewer cannot write (registry writes are editor-tier, audited).
	if w := h.do("POST", mbase+"/owned-models", viewer, map[string]any{"name": "z", "kind": "hosted"}, tenantHdr(tenant)); w.code != http.StatusForbidden {
		t.Errorf("viewer write = %d, want 403", w.code)
	}
}

// TestS70GPAIPosture exercises FIN-13: an operator attests a brokered provider's
// GPAI compliance posture (claim vs verified), and a second attestation upserts
// the same provider rather than duplicating it.
func TestS70GPAIPosture(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	p := h.do("PUT", mbase+"/gpai-posture", editor, map[string]any{
		"provider_ref": "anthropic", "cop_signatory": true, "technical_docs": true,
		"training_data_summary": true, "verified": true, "verification_method": "reviewed published model docs",
	}, tenantHdr(tenant))
	if p.code != http.StatusCreated || p.body["verified"] != true {
		t.Fatalf("attest = %d verified=%v %s", p.code, p.body["verified"], p.raw)
	}

	// provider_ref is required.
	if bad := h.do("PUT", mbase+"/gpai-posture", editor, map[string]any{"technical_docs": true}, tenantHdr(tenant)); bad.code != http.StatusBadRequest {
		t.Errorf("missing provider_ref = %d, want 400", bad.code)
	}

	// Re-attest the same provider: upsert (no duplicate), now an unverified claim.
	if up := h.do("PUT", mbase+"/gpai-posture", editor, map[string]any{
		"provider_ref": "anthropic", "copyright_policy": true, "verified": false,
	}, tenantHdr(tenant)); up.code != http.StatusCreated {
		t.Fatalf("re-attest = %d %s", up.code, up.raw)
	}
	l := h.do("GET", mbase+"/gpai-posture?provider_ref=anthropic", viewer, nil, tenantHdr(tenant))
	if l.code != http.StatusOK || len(items(l)) != 1 {
		t.Fatalf("list posture = %d n=%d, want 1 (upsert)", l.code, len(items(l)))
	}
	if got := items(l)[0].(map[string]any)["verified"]; got != false {
		t.Errorf("verified after re-attest = %v, want false (claim)", got)
	}
}

// items extracts the items slice from a list response body.
func items(r resp) []any {
	out, _ := r.body["items"].([]any)
	return out
}
