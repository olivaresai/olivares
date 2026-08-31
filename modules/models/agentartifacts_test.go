// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/models"
)

// seedArtifacts registers one artifact per CUR-7 class and returns the editor
// token + tenant. The skill carries a full posture verdict + content hash; the
// AGENTS.md is registered unscanned (the honest posture_scanned=false path).
func seedArtifacts(t *testing.T, h *harness) (editor string, tenant model.TenantID) {
	t.Helper()
	admin := h.adminLogin()
	tenant = h.createOrg(admin, "acme")
	editor = h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	const skillHash = "2222222222222222222222222222222222222222222222222222222222222222"
	for _, a := range []map[string]any{
		{"artifact_class": "skill", "name": "deploy-helper", "version": "1.2.0",
			"provenance": "marketplace:team-market", "source_ref": "plugin:myplugin",
			"content_hash": skillHash, "posture_grade": "C", "posture_issues": 3, "verified": true},
		{"artifact_class": "mcpb_extension", "name": "notes", "version": "2.0.0",
			"provenance": "org-allowlist", "posture_grade": "A", "posture_issues": 0},
		{"artifact_class": "mcp_app_template", "name": "ui://srv/dashboard",
			"provenance": "pre-declared", "posture_grade": "B", "posture_issues": 1},
		{"artifact_class": "agents_md", "name": "AGENTS.md", "provenance": "repo:olivaresai/olivares",
			"content_hash": "3333333333333333333333333333333333333333333333333333333333333333"},
	} {
		if r := h.do("POST", "/v1/m/models/agent-artifacts", editor, a, tenantHdr(tenant)); r.code != http.StatusCreated {
			t.Fatalf("create artifact %v = %d %s", a["name"], r.code, r.raw)
		}
	}
	return editor, tenant
}

// TestAgentArtifactRegistryAndValidation: the four classes register; a bogus
// class, a fabricated verdict (scanned with no grade) and a duplicate are
// refused.
func TestAgentArtifactRegistryAndValidation(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	editor, tenant := seedArtifacts(t, h)

	if r := h.do("GET", "/v1/m/models/agent-artifacts", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	} else if items, _ := r.body["items"].([]any); len(items) != 4 {
		t.Fatalf("want the 4 registered artifacts, got %d", len(items))
	}
	// Class filter.
	if r := h.do("GET", "/v1/m/models/agent-artifacts?artifact_class=skill", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("filtered list = %d", r.code)
	} else if items, _ := r.body["items"].([]any); len(items) != 1 {
		t.Fatalf("class filter must yield 1 skill, got %d", len(items))
	}

	// Rejections: unknown class / fabricated verdict / posture count without a grade.
	bad := []map[string]any{
		{"artifact_class": "plugin", "name": "x"},
		{"artifact_class": "skill", "name": "x", "posture_scanned": true},
		{"artifact_class": "skill", "name": "x", "posture_issues": 2},
		{"artifact_class": "skill", "name": "x", "posture_grade": "E"},
	}
	for _, b := range bad {
		if r := h.do("POST", "/v1/m/models/agent-artifacts", editor, b, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Errorf("payload %v must be rejected 400, got %d %s", b, r.code, r.raw)
		}
	}
	// Duplicate (class, name) conflicts.
	dup := map[string]any{"artifact_class": "skill", "name": "deploy-helper"}
	if r := h.do("POST", "/v1/m/models/agent-artifacts", editor, dup, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("duplicate (class,name) must 409, got %d %s", r.code, r.raw)
	}
	// Registration self-audits.
	if !contains(h.auditActions(tenant), "models.agent_artifact.create") {
		t.Error("artifact registration must self-audit")
	}
}

// TestAgentArtifactBOMShape: the agent-supply-chain BOM is CycloneDX 1.6 with
// one component per artifact, the class-correct component types, schema-valid
// hashes, and provenance + posture verdict as HONEST properties (the unscanned
// AGENTS.md carries posture_scanned=false and NO grade property).
func TestAgentArtifactBOMShape(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	editor, tenant := seedArtifacts(t, h)

	r := h.do("GET", "/v1/m/models/agent-artifacts/aibom", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("aibom = %d %s", r.code, r.raw)
	}
	if r.body["bomFormat"] != "CycloneDX" || r.body["specVersion"] != "1.6" {
		t.Fatalf("bom header = %v/%v, want CycloneDX/1.6", r.body["bomFormat"], r.body["specVersion"])
	}
	meta, _ := r.body["metadata"].(map[string]any)
	mc, _ := meta["component"].(map[string]any)
	if mc["name"] != "agent-artifacts" || mc["type"] != "application" {
		t.Errorf("metadata.component = %v, want application/agent-artifacts", mc)
	}

	comps, _ := r.body["components"].([]any)
	if len(comps) != 4 {
		t.Fatalf("want 4 components (one per artifact class), got %d", len(comps))
	}
	wantType := map[string]string{
		"deploy-helper": "library", "notes": "application",
		"ui://srv/dashboard": "file", "AGENTS.md": "file",
	}
	seen := map[string]bool{}
	for _, c := range comps {
		cm := c.(map[string]any)
		name := cm["name"].(string)
		seen[name] = true
		if cm["type"] != wantType[name] {
			t.Errorf("component %q type = %v, want %v", name, cm["type"], wantType[name])
		}
		props, _ := cm["properties"].([]any)
		switch name {
		case "deploy-helper":
			if !hasProp(props, "olivares:artifact:class", "skill") ||
				!hasProp(props, "olivares:artifact:provenance", "marketplace:team-market") ||
				!hasProp(props, "olivares:artifact:posture_grade", "C") ||
				!hasProp(props, "olivares:artifact:posture_issues", "3") ||
				!hasProp(props, "olivares:artifact:posture_scanned", "true") ||
				!hasProp(props, "olivares:artifact:provenance_verified", "true") {
				t.Errorf("skill component missing honest properties: %v", props)
			}
			hashes, _ := cm["hashes"].([]any)
			if len(hashes) == 0 || hashes[0].(map[string]any)["alg"] != "SHA-256" {
				t.Errorf("skill component must carry its schema-valid SHA-256 hash; got %v", cm["hashes"])
			}
		case "AGENTS.md":
			if !hasProp(props, "olivares:artifact:posture_scanned", "false") {
				t.Errorf("unscanned artifact must say posture_scanned=false: %v", props)
			}
			for _, p := range props {
				if p.(map[string]any)["name"] == "olivares:artifact:posture_grade" {
					t.Errorf("unscanned artifact must NOT fabricate a grade: %v", props)
				}
			}
		}
	}
	for name := range wantType {
		if !seen[name] {
			t.Errorf("BOM missing component %q", name)
		}
	}
}

// TestAgentArtifactBOMSeal: the seal is deterministic for an unchanged
// inventory, changes when the inventory changes, anchors an advancing ledger
// head under the "agent-artifacts" subject, and self-audits — the
// tamper-evidence pattern over the agent supply chain.
func TestAgentArtifactBOMSeal(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	editor, tenant := seedArtifacts(t, h)

	s1 := h.do("POST", "/v1/m/models/agent-artifacts/aibom", editor, nil, tenantHdr(tenant))
	if s1.code != http.StatusCreated {
		t.Fatalf("seal = %d %s", s1.code, s1.raw)
	}
	seal1, _ := s1.body["seal"].(map[string]any)
	if seal1["owned_ref"] != "agent-artifacts" {
		t.Errorf("seal subject = %v, want agent-artifacts", seal1["owned_ref"])
	}
	if intOf(seal1["component_count"]) != 4 || intOf(seal1["ledger_seq"]) <= 0 {
		t.Errorf("seal must count 4 components and anchor a ledger head: %v", seal1)
	}

	// Unchanged inventory → identical content hash; the ledger advanced.
	s2 := h.do("POST", "/v1/m/models/agent-artifacts/aibom", editor, nil, tenantHdr(tenant))
	seal2, _ := s2.body["seal"].(map[string]any)
	if seal1["content_hash"] != seal2["content_hash"] {
		t.Errorf("content hash must be deterministic for an unchanged inventory: %v != %v", seal1["content_hash"], seal2["content_hash"])
	}
	if intOf(seal2["ledger_seq"]) <= intOf(seal1["ledger_seq"]) {
		t.Errorf("second seal must anchor a later head")
	}

	// Inventory-SENSITIVITY: registering a fifth artifact changes the hash.
	h.do("POST", "/v1/m/models/agent-artifacts", editor, map[string]any{
		"artifact_class": "skill", "name": "release-notes", "posture_grade": "A",
	}, tenantHdr(tenant))
	s3 := h.do("POST", "/v1/m/models/agent-artifacts/aibom", editor, nil, tenantHdr(tenant))
	seal3, _ := s3.body["seal"].(map[string]any)
	if seal3["content_hash"] == seal1["content_hash"] {
		t.Errorf("content hash must change when the inventory changes")
	}

	// The seals list under their OWN kind — and never leak into the model-AIBOM
	// seal list (modules/compliance counts models.aibom rows as MODEL-lineage
	// evidence; an agent-artifact seal there would inflate it).
	r := h.do("GET", "/v1/m/models/agent-artifacts/aiboms", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list seals = %d", r.code)
	}
	if items, _ := r.body["items"].([]any); len(items) != 3 {
		t.Errorf("want the 3 agent-artifact seals, got %d", len(items))
	}
	if r := h.do("GET", "/v1/m/models/aiboms", editor, nil, tenantHdr(tenant)); r.code == http.StatusOK {
		if items, _ := r.body["items"].([]any); len(items) != 0 {
			t.Errorf("agent-artifact seals must NOT appear among model-AIBOM seals, got %d", len(items))
		}
	}
	if !contains(h.auditActions(tenant), "models.agent_aibom.seal") {
		t.Error("agent-artifact BOM seal must self-audit under its own kind")
	}
}
