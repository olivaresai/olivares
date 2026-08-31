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

// TestAIBOMGenerateShape verifies the generated AIBOM is a faithful CycloneDX 1.6
// machine-learning BOM: bomFormat/specVersion, a metadata machine-learning-model
// component, a `data` dataset component referenced by the model's modelCard, and the
// signed-model-admission verdict surfaced as an honest property.
func TestAIBOMGenerateShape(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	// Owned model + version.
	om := h.do("POST", "/v1/m/models/owned-models", editor, map[string]any{"name": "ix-model", "kind": "fine_tuned", "base_ref": "llama-3", "provider_ref": "acme"}, tenantHdr(tenant))
	if om.code != http.StatusCreated {
		t.Fatalf("owned = %d %s", om.code, om.raw)
	}
	ownedID := om.body["id"].(string)
	mv := h.do("POST", "/v1/m/models/model-versions", editor, map[string]any{"owned_ref": ownedID, "version": "2.1.0", "source_ref": "git://acme/train@abc"}, tenantHdr(tenant))
	versionID := mv.body["id"].(string)

	// A dataset feeding the model (lineage component). A real 64-hex SHA-256 digest so
	// the AIBOM emits a schema-valid hashes[] entry.
	const dsHash = "1111111111111111111111111111111111111111111111111111111111111111"
	ds := h.do("POST", "/v1/m/models/datasets", editor, map[string]any{
		"owned_ref": ownedID, "name": "train-set", "classification": "internal",
		"governance": "curated by data team", "content_hash": dsHash, "source_ref": "s3://acme/train",
	}, tenantHdr(tenant))
	if ds.code != http.StatusCreated {
		t.Fatalf("dataset = %d %s", ds.code, ds.raw)
	}

	// Admit the version (trusted bare key) so the AIBOM carries a verified verdict.
	_, priv, _ := ed25519.GenerateKey(nil)
	bundle, pubPEM, _ := omsBareKeyBundle(t, "weights.bin", "w", priv)
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true, "trusted_keys": []string{pubPEM}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("policy = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/models/model-versions/"+versionID+"/admit", editor, map[string]any{"bundle": bundle}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["admitted"] != true {
		t.Fatalf("admit = %d %s", r.code, r.raw)
	}

	// Generate the AIBOM.
	r := h.do("GET", "/v1/m/models/owned-models/"+ownedID+"/aibom", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("aibom = %d %s", r.code, r.raw)
	}
	if r.body["bomFormat"] != "CycloneDX" || r.body["specVersion"] != "1.6" {
		t.Fatalf("bom header = %v/%v, want CycloneDX/1.6", r.body["bomFormat"], r.body["specVersion"])
	}
	meta, _ := r.body["metadata"].(map[string]any)
	mc, _ := meta["component"].(map[string]any)
	if mc["type"] != "machine-learning-model" {
		t.Errorf("metadata.component.type = %v, want machine-learning-model", mc["type"])
	}

	comps, _ := r.body["components"].([]any)
	var dataRef string
	var sawModel bool
	for _, c := range comps {
		cm := c.(map[string]any)
		switch cm["type"] {
		case "data":
			dataRef = cm["bom-ref"].(string)
			data, _ := cm["data"].([]any)
			if len(data) == 0 || data[0].(map[string]any)["type"] != "dataset" {
				t.Errorf("data component missing data[].type=dataset; got %v", cm["data"])
			}
			// The valid 64-hex content hash must emit a schema-valid CycloneDX hash.
			hashes, _ := cm["hashes"].([]any)
			if len(hashes) == 0 || hashes[0].(map[string]any)["alg"] != "SHA-256" || hashes[0].(map[string]any)["content"] != dsHash {
				t.Errorf("data component must carry the SHA-256 hash %q; got %v", dsHash, cm["hashes"])
			}
		case "machine-learning-model":
			sawModel = true
			card, _ := cm["modelCard"].(map[string]any)
			if card == nil {
				t.Fatalf("model component missing modelCard")
			}
			// The admission verdict must be an honest property on the model card.
			props, _ := card["properties"].([]any)
			if !hasProp(props, "olivares:admission:signature_verified", "true") {
				t.Errorf("modelCard must carry olivares:admission:signature_verified=true; got %v", props)
			}
		}
	}
	if !sawModel {
		t.Fatal("AIBOM has no machine-learning-model component")
	}
	if dataRef == "" {
		t.Fatal("AIBOM has no data (dataset) component")
	}
	// The model's modelCard must reference the dataset by its bom-ref (lineage link).
	for _, c := range comps {
		cm := c.(map[string]any)
		if cm["type"] != "machine-learning-model" {
			continue
		}
		card := cm["modelCard"].(map[string]any)
		mp, _ := card["modelParameters"].(map[string]any)
		dsRefs, _ := mp["datasets"].([]any)
		found := false
		for _, d := range dsRefs {
			if d.(map[string]any)["ref"] == dataRef {
				found = true
			}
		}
		if !found {
			t.Errorf("model modelParameters.datasets must reference the dataset %q; got %v", dataRef, dsRefs)
		}
	}
}

// TestAIBOMSealAnchorsLedger seals an AIBOM, checks the content hash is deterministic
// for unchanged lineage, and that the seal anchors an advancing ledger head (the same
// tamper-evidence pattern as compliance's evidence package).
func TestAIBOMSealAnchorsLedger(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)

	om := h.do("POST", "/v1/m/models/owned-models", editor, map[string]any{"name": "sealed-model", "kind": "imported"}, tenantHdr(tenant))
	ownedID := om.body["id"].(string)
	h.do("POST", "/v1/m/models/model-versions", editor, map[string]any{"owned_ref": ownedID, "version": "1.0.0"}, tenantHdr(tenant))

	s1 := h.do("POST", "/v1/m/models/owned-models/"+ownedID+"/aibom", editor, nil, tenantHdr(tenant))
	if s1.code != http.StatusCreated {
		t.Fatalf("seal = %d %s", s1.code, s1.raw)
	}
	seal1, _ := s1.body["seal"].(map[string]any)
	if seal1["content_hash"] == "" || seal1["content_hash"] == nil {
		t.Fatal("seal must carry a content hash")
	}
	if seal1["serial_number"] == "" || seal1["spec_version"] != "1.6" {
		t.Errorf("seal must record serial_number + spec_version 1.6; got %v", seal1)
	}
	if intOf(seal1["ledger_seq"]) <= 0 {
		t.Errorf("seal must anchor a ledger head seq > 0; got %v", seal1["ledger_seq"])
	}

	// Seal again, unchanged lineage: content hash identical (deterministic), but the
	// ledger advanced (the first seal self-audited).
	s2 := h.do("POST", "/v1/m/models/owned-models/"+ownedID+"/aibom", editor, nil, tenantHdr(tenant))
	seal2, _ := s2.body["seal"].(map[string]any)
	if seal1["content_hash"] != seal2["content_hash"] {
		t.Errorf("content hash must be deterministic for unchanged lineage: %v != %v", seal1["content_hash"], seal2["content_hash"])
	}
	if intOf(seal2["ledger_seq"]) <= intOf(seal1["ledger_seq"]) {
		t.Errorf("second seal must anchor a later head: %v !> %v", seal2["ledger_seq"], seal1["ledger_seq"])
	}

	// Lineage-SENSITIVITY: changing the lineage (a new version) MUST change the content
	// hash — proving the hash binds the substantive BOM, not a constant.
	h.do("POST", "/v1/m/models/model-versions", editor, map[string]any{"owned_ref": ownedID, "version": "2.0.0"}, tenantHdr(tenant))
	s3 := h.do("POST", "/v1/m/models/owned-models/"+ownedID+"/aibom", editor, nil, tenantHdr(tenant))
	seal3, _ := s3.body["seal"].(map[string]any)
	if seal3["content_hash"] == seal1["content_hash"] {
		t.Errorf("content hash must change when the lineage changes (a new version was added); got identical %v", seal3["content_hash"])
	}

	// The sealed AIBOM is listed and the seal self-audited.
	if r := h.do("GET", "/v1/m/models/aiboms", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("list aiboms = %d %s", r.code, r.raw)
	}
	if !contains(h.auditActions(tenant), "models.aibom.seal") {
		t.Errorf("aibom seal must self-audit")
	}
}

func hasProp(props []any, name, value string) bool {
	for _, p := range props {
		pm, _ := p.(map[string]any)
		if pm["name"] == name && pm["value"] == value {
			return true
		}
	}
	return false
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
