// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"crypto/ed25519"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/models"
)

// the SPDX 3.0.1 AI Profile export and the generated model card, both rendered
// from the same governed inventory as the CycloneDX AIBOM.

// seedExportModel creates an owned model + version + pii dataset + verified admission
// and returns (ownedID, versionID).
func seedExportModel(t *testing.T, h *harness, tenant model.TenantID, adminTok, editor string) (string, string) {
	t.Helper()
	om := h.do("POST", "/v1/m/models/owned-models", editor, map[string]any{
		"name": "exp-model", "kind": "fine_tuned", "base_ref": "llama-3", "provider_ref": "acme-ai",
		"note": "internal support assistant",
	}, tenantHdr(tenant))
	if om.code != http.StatusCreated {
		t.Fatalf("owned = %d %s", om.code, om.raw)
	}
	ownedID := om.body["id"].(string)
	mv := h.do("POST", "/v1/m/models/model-versions", editor, map[string]any{
		"owned_ref": ownedID, "version": "1.2.0", "artifact_ref": "oci://registry.acme/exp-model:1.2.0",
	}, tenantHdr(tenant))
	if mv.code != http.StatusCreated {
		t.Fatalf("version = %d %s", mv.code, mv.raw)
	}
	versionID := mv.body["id"].(string)

	const dsHash = "2222222222222222222222222222222222222222222222222222222222222222"
	if r := h.do("POST", "/v1/m/models/datasets", editor, map[string]any{
		"owned_ref": ownedID, "name": "support-tickets", "classification": "pii",
		"content_hash": dsHash, "source_ref": "s3://acme/tickets",
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("dataset = %d %s", r.code, r.raw)
	}

	_, priv, _ := ed25519.GenerateKey(nil)
	bundle, pubPEM, _ := omsBareKeyBundle(t, "weights.bin", "w", priv)
	if r := h.do("PUT", "/v1/m/models/admission-policy", adminTok, map[string]any{"require_signed": true, "trusted_keys": []string{pubPEM}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("policy = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/models/model-versions/"+versionID+"/admit", editor, map[string]any{"bundle": bundle}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("admit = %d %s", r.code, r.raw)
	}
	return ownedID, versionID
}

// TestSPDXAIProfileExport: ?format=spdx renders a faithful SPDX 3.0.1 JSON-LD
// document — context, shared CreationInfo, exactly one SpdxDocument with the four
// profile conformances, an ai_AIPackage meeting the AI-profile required fields, a
// dataset_DatasetPackage with the honest no-assertion modality, the pii→sensitive
// mapping, and a trainedOn relationship.
func TestSPDXAIProfileExport(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)
	ownedID, _ := seedExportModel(t, h, tenant, adminTok, editor)

	r := h.do("GET", "/v1/m/models/owned-models/"+ownedID+"/aibom?format=spdx", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("spdx = %d %s", r.code, r.raw)
	}
	if r.body["@context"] != "https://spdx.org/rdf/3.0.1/spdx-context.jsonld" {
		t.Fatalf("@context = %v, want the SPDX 3.0.1 context", r.body["@context"])
	}
	graph, _ := r.body["@graph"].([]any)
	if len(graph) == 0 {
		t.Fatal("empty @graph")
	}

	var creationInfo, doc, aiPkg, dsPkg, rel map[string]any
	docs := 0
	for _, el := range graph {
		em := el.(map[string]any)
		switch em["type"] {
		case "CreationInfo":
			creationInfo = em
		case "SpdxDocument":
			docs++
			doc = em
		case "ai_AIPackage":
			aiPkg = em
		case "dataset_DatasetPackage":
			dsPkg = em
		case "Relationship":
			rel = em
		}
	}
	if creationInfo == nil || creationInfo["specVersion"] != "3.0.1" || creationInfo["@id"] != "_:creationinfo" {
		t.Errorf("CreationInfo must be the _:creationinfo blank node with specVersion 3.0.1; got %v", creationInfo)
	}
	if docs != 1 {
		t.Errorf("a serialization must contain exactly one SpdxDocument, got %d", docs)
	}
	pc, _ := doc["profileConformance"].([]any)
	if len(pc) != 4 {
		t.Errorf("profileConformance must declare core/software/ai/dataset; got %v", pc)
	}

	if aiPkg == nil {
		t.Fatal("no ai_AIPackage element")
	}
	// The AI-profile required fields (external restrictions, minCount 1).
	for _, field := range []string{"software_downloadLocation", "software_packageVersion", "software_primaryPurpose", "releaseTime", "suppliedBy", "creationInfo", "name"} {
		if aiPkg[field] == nil || aiPkg[field] == "" {
			t.Errorf("ai_AIPackage missing required %s", field)
		}
	}
	if aiPkg["software_primaryPurpose"] != "model" {
		t.Errorf("primaryPurpose = %v, want model", aiPkg["software_primaryPurpose"])
	}
	if aiPkg["software_downloadLocation"] != "oci://registry.acme/exp-model:1.2.0" {
		t.Errorf("downloadLocation must carry the recorded artifact ref; got %v", aiPkg["software_downloadLocation"])
	}
	if !strings.Contains(aiPkg["comment"].(string), "signature_verified=true") {
		t.Errorf("the admission verdict must ride in the package comment; got %v", aiPkg["comment"])
	}

	if dsPkg == nil {
		t.Fatal("no dataset_DatasetPackage element")
	}
	dt, _ := dsPkg["dataset_datasetType"].([]any)
	if len(dt) != 1 || dt[0] != "noAssertion" {
		t.Errorf("unrecorded modality must serialize the vocabulary's noAssertion; got %v", dsPkg["dataset_datasetType"])
	}
	if dsPkg["dataset_hasSensitivePersonalInformation"] != "yes" || dsPkg["dataset_confidentialityLevel"] != "amber" {
		t.Errorf("pii classification must map to hasSensitivePersonalInformation=yes + confidentialityLevel=amber; got %v / %v",
			dsPkg["dataset_hasSensitivePersonalInformation"], dsPkg["dataset_confidentialityLevel"])
	}
	vu, _ := dsPkg["verifiedUsing"].([]any)
	if len(vu) != 1 || vu[0].(map[string]any)["hashValue"] != "2222222222222222222222222222222222222222222222222222222222222222" {
		t.Errorf("dataset must carry its sha256 as verifiedUsing Hash; got %v", dsPkg["verifiedUsing"])
	}
	// originatedBy: ARRAY form (the official JSON Schema rejects a bare string) with
	// exactly one honest NOASSERTION agent — the registry never records a dataset
	// originator, and the model's provider must NOT be fabricated into the slot.
	ob, _ := dsPkg["originatedBy"].([]any)
	if len(ob) != 1 {
		t.Fatalf("dataset originatedBy must be a 1-element array; got %v", dsPkg["originatedBy"])
	}
	var noassertSeen bool
	for _, el := range graph {
		em := el.(map[string]any)
		if em["spdxId"] == ob[0] {
			if em["name"] != "NOASSERTION" {
				t.Errorf("dataset originatedBy must point at the NOASSERTION agent (originator unrecorded), got agent %v", em["name"])
			}
			noassertSeen = true
		}
	}
	if !noassertSeen {
		t.Errorf("originatedBy ref %v does not resolve to a graph element", ob[0])
	}

	// The registry records no train-vs-evaluate role, so the relationship must be the
	// role-neutral hasDataFile — never a fabricated trainedOn.
	if rel == nil || rel["relationshipType"] != "hasDataFile" || rel["from"] != aiPkg["spdxId"] {
		t.Errorf("hasDataFile relationship from the model to its datasets missing; got %v", rel)
	}
	to, _ := rel["to"].([]any)
	if len(to) != 1 || to[0] != dsPkg["spdxId"] {
		t.Errorf("relationship.to must list the dataset; got %v", to)
	}

	// A version-less model must omit rootElement/element (0..*), never emit null.
	om := h.do("POST", "/v1/m/models/owned-models", editor, map[string]any{"name": "bare-spdx", "kind": "imported"}, tenantHdr(tenant))
	bare := h.do("GET", "/v1/m/models/owned-models/"+om.body["id"].(string)+"/aibom?format=spdx", editor, nil, tenantHdr(tenant))
	for _, el := range bare.body["@graph"].([]any) {
		em := el.(map[string]any)
		if em["type"] != "SpdxDocument" {
			continue
		}
		for _, k := range []string{"rootElement", "element"} {
			if v, present := em[k]; present && v == nil {
				t.Errorf("version-less SpdxDocument must omit %s, not serialize null", k)
			}
		}
	}
}

// TestModelCardExport: the generated card carries only recorded evidence, marks the
// rest not_recorded, and renders Markdown on demand.
func TestModelCardExport(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", auth.RoleAdmin)
	editor := h.roleToken(admin, tenant, "e@x.io", auth.RoleEditor)
	ownedID, _ := seedExportModel(t, h, tenant, adminTok, editor)

	r := h.do("GET", "/v1/m/models/owned-models/"+ownedID+"/model-card", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("model-card = %d %s", r.code, r.raw)
	}
	if r.body["schema"] != "olivares:model-card:v1" {
		t.Errorf("schema = %v", r.body["schema"])
	}
	// Honesty: what the plane does not record is not_recorded, never invented.
	if r.body["evaluation"] != "not_recorded" || r.body["ethical_considerations"] != "not_recorded" {
		t.Errorf("evaluation/ethical must be not_recorded; got %v / %v", r.body["evaluation"], r.body["ethical_considerations"])
	}
	if r.body["intended_use"] != "internal support assistant" {
		t.Errorf("intended_use must come from the recorded note; got %v", r.body["intended_use"])
	}
	td, ok := r.body["training_data"].([]any)
	if !ok || len(td) != 1 || td[0].(map[string]any)["name"] != "support-tickets" {
		t.Errorf("training_data must list the governed dataset; got %v", r.body["training_data"])
	}
	prov, _ := r.body["provenance_and_admission"].(map[string]any)
	if prov["signed_admissions_verified"] != float64(1) {
		t.Errorf("provenance must count the verified admission; got %v", prov)
	}
	if d, _ := r.body["disclaimer"].(string); !strings.Contains(d, "NOT a certification") {
		t.Error("model card must carry the never-certify disclaimer")
	}

	md := h.do("GET", "/v1/m/models/owned-models/"+ownedID+"/model-card?format=md", editor, nil, tenantHdr(tenant))
	if md.code != http.StatusOK || !strings.Contains(md.raw, "# Model Card — exp-model") {
		t.Fatalf("markdown card = %d %.120s", md.code, md.raw)
	}
	if !strings.Contains(md.raw, "not_recorded") {
		t.Error("markdown card must keep the not_recorded honesty markers")
	}

	// A model with no note/datasets degrades honestly.
	om := h.do("POST", "/v1/m/models/owned-models", editor, map[string]any{"name": "bare-model", "kind": "imported"}, tenantHdr(tenant))
	bare := h.do("GET", "/v1/m/models/owned-models/"+om.body["id"].(string)+"/model-card", editor, nil, tenantHdr(tenant))
	if bare.body["intended_use"] != "not_recorded" || bare.body["training_data"] != "not_recorded" {
		t.Errorf("bare model must mark intended_use/training_data not_recorded; got %v / %v", bare.body["intended_use"], bare.body["training_data"])
	}
}
