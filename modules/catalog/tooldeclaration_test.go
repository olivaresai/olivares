// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package catalog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/secure/modelsign"
)

func TestToolDeclarationAdmission(t *testing.T) {
	h := newHarness(t, true)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tool-fixture")
	entry := map[string]any{"kind": "mcp", "name": "Fixture MCP", "slug": "fixture-mcp", "version": "1.0.0", "spec": map[string]any{"tool_install": map[string]any{"source_url": "https://tools.example/fixture.tar.gz", "version": "1.0.0", "sha256": strings.Repeat("a", 64), "program": "fixture-mcp", "config_home": "fixture-mcp", "kind": "mcp"}}}
	r := h.do("POST", "/v1/m/catalog/entries", admin, entry, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	mc := api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(h.st, tenant)}
	if _, err := h.cat.ResolveToolDeclaration(context.Background(), mc, "https://tools.example/fixture.tar.gz", "1.0.0"); err == nil {
		t.Fatal("draft was installable")
	}
	r = h.do("POST", "/v1/m/catalog/entries/"+id+"/admit", admin, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("admit: %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["verified"] != true {
		t.Fatalf("verify: %d %s", r.code, r.raw)
	}
	d, err := h.cat.ResolveToolDeclaration(context.Background(), mc, "https://tools.example/fixture.tar.gz", "1.0.0")
	if err != nil || d.Program != "fixture-mcp" {
		t.Fatalf("resolve: %+v %v", d, err)
	}
	other := h.createOrg(admin, "other-tool-tenant")
	if _, err := h.cat.ResolveToolDeclaration(context.Background(), api.ModuleContext{Tenant: other, Data: api.NewScopedData(h.st, other)}, d.SourceURL, d.Version); err == nil {
		t.Fatal("declaration crossed tenants")
	}
	r = h.do("POST", "/v1/m/catalog/entries/"+id+"/deprecate", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("deprecate: %d %s", r.code, r.raw)
	}
	if _, err := h.cat.ResolveToolDeclaration(context.Background(), mc, d.SourceURL, d.Version); err == nil {
		t.Fatal("deprecated declaration was installable")
	}
}

func TestToolDeclarationRejectsUnsafeSourceAndPaths(t *testing.T) {
	for _, change := range []map[string]any{{"source_url": "http://tools.example/release.tar.gz"}, {"source_url": "https://user:password@tools.example/release.tar.gz"}, {"program": "../escape"}, {"config_home": "/etc"}, {"sha256": "bad"}, {"kind": "shell"}} {
		h := newHarness(t, false)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "invalid-tool")
		d := map[string]any{"source_url": "https://tools.example/fixture.tar.gz", "version": "1.0.0", "sha256": strings.Repeat("a", 64), "program": "fixture-mcp", "config_home": "fixture-mcp", "kind": "mcp"}
		for k, v := range change {
			d[k] = v
		}
		entry := map[string]any{"kind": "mcp", "name": "Fixture MCP", "slug": "fixture-mcp", "version": "1.0.0", "spec": map[string]any{"tool_install": d}}
		r := h.do("POST", "/v1/m/catalog/entries", admin, entry, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Fatalf("unsafe declaration %v: %d %s", change, r.code, r.raw)
		}
	}
}

func TestToolDeclarationKeepsMCPAdmissionPolicy(t *testing.T) {
	h := newHarness(t, true)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "signed-tool")
	att := newAttestation(t, modelsign.PredicateTypeSLSAProvenanceV1, fixtureDigest)
	if r := h.do("PUT", "/v1/m/catalog/mcp-admission/policy", admin, map[string]any{"require_signed": true, "require_subject_digest": true, "trusted_keys": []string{att.pubPEM}}, tenantHdr(tenant)); r.code != 200 {
		t.Fatal(r.raw)
	}
	d := map[string]any{"source_url": "https://tools.example/signed.tar.gz", "version": "1.0.0", "sha256": fixtureDigest, "program": "signed-mcp", "config_home": "signed-mcp", "kind": "mcp"}
	r := h.do("POST", "/v1/m/catalog/entries", admin, map[string]any{"kind": "mcp", "name": "Signed MCP", "slug": "signed-mcp", "version": "1.0.0", "spec": map[string]any{"tool_install": d}}, tenantHdr(tenant))
	if r.code != 201 {
		t.Fatal(r.raw)
	}
	path := "/v1/m/catalog/entries/" + r.body["id"].(string) + "/admit"
	if r := h.do("POST", path, admin, map[string]any{}, tenantHdr(tenant)); r.code != 409 {
		t.Fatalf("policy bypass: %d %s", r.code, r.raw)
	}
	if r := h.do("POST", path, admin, map[string]any{"bundle": json.RawMessage(att.bundle), "expected_digest": strings.Repeat("a", 64)}, tenantHdr(tenant)); r.code != 400 {
		t.Fatalf("digest override: %d %s", r.code, r.raw)
	}
	if r := h.do("POST", path, admin, map[string]any{"bundle": json.RawMessage(att.bundle)}, tenantHdr(tenant)); r.code != 200 || r.body["admitted"] != true {
		t.Fatalf("pinned attestation: %d %s", r.code, r.raw)
	}
	if r := h.do("POST", path, admin, map[string]any{}, tenantHdr(tenant)); r.code != 200 || r.body["status"] != "approved" {
		t.Fatalf("approved declaration: %d %s", r.code, r.raw)
	}
}
