// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func (h *harness) mustDataProduct(token string, tenant model.TenantID, body map[string]any) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/knowledge/data-products", token, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create data product = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

func (h *harness) mustPublishDataProduct(token string, tenant model.TenantID, id string) {
	h.t.Helper()
	r := h.do("POST", "/v1/m/knowledge/data-products/"+id+"/publish", token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("publish data product = %d %s", r.code, r.raw)
	}
}

func (h *harness) mustDataContract(token string, tenant model.TenantID, productID string, body map[string]any) map[string]any {
	h.t.Helper()
	r := h.do("POST", "/v1/m/knowledge/data-products/"+productID+"/contracts", token, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create data contract = %d %s", r.code, r.raw)
	}
	return r.body
}

func (h *harness) dataProductRecord(tenant model.TenantID, id string) model.Record {
	h.t.Helper()
	var out model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		out, err = repo.Get(context.Background(), model.ID(id))
		return err
	}); err != nil {
		h.t.Fatalf("dataProductRecord: %v", err)
	}
	return out
}

func (h *harness) updateDataProductRaw(tenant model.TenantID, id string, mutate func(model.Record)) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(dataProductKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(id))
		if err != nil {
			return err
		}
		mutate(rec)
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		h.t.Fatalf("updateDataProductRaw: %v", err)
	}
}

func dataProductTestMC(h *harness, tenant model.TenantID) api.ModuleContext {
	return api.ModuleContext{
		Principal: h.scopedPrincipal(tenant),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
}

func TestDataProductCRUDLifecycleAndContracts(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dpcrud")
	editor := h.roleToken(admin, tenant, "editor@dpcrud.io", auth.RoleEditor)
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "catalog-kb"})

	dpID := h.mustDataProduct(editor, tenant, map[string]any{
		"name":                  "customer-360",
		"description":           "Customer support knowledge",
		"owner_ref":             "team:data",
		"kb_ref":                kbID,
		"tags":                  map[string]any{"domain": "support"},
		"freshness_sla_seconds": float64(86400),
		"availability_target":   "99.9%",
		"enforcement_mode":      "enforce",
	})

	if dup := h.do("POST", "/v1/m/knowledge/data-products", editor, map[string]any{
		"name": "customer-360", "owner_ref": "team:data",
	}, tenantHdr(tenant)); dup.code != http.StatusConflict {
		t.Fatalf("duplicate data product = %d %s, want 409", dup.code, dup.raw)
	}

	list := h.do("GET", "/v1/m/knowledge/data-products?status=draft", editor, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("list data products = %d %s", list.code, list.raw)
	}
	if items := list.body["items"].([]any); len(items) != 1 {
		t.Fatalf("expected one draft data product, got %d", len(items))
	}

	update := h.do("PUT", "/v1/m/knowledge/data-products/"+dpID, editor, map[string]any{
		"name":             "customer-360-prod",
		"enforcement_mode": "warn",
		"quality_score":    float64(82),
	}, tenantHdr(tenant))
	if update.code != http.StatusOK {
		t.Fatalf("update data product = %d %s", update.code, update.raw)
	}
	if update.body["status"] != dpStatusDraft || update.body["enforcement_mode"] != dpModeWarn {
		t.Fatalf("unexpected update body: %v", update.body)
	}

	h.mustPublishDataProduct(editor, tenant, dpID)
	if again := h.do("POST", "/v1/m/knowledge/data-products/"+dpID+"/publish", editor, nil, tenantHdr(tenant)); again.code != http.StatusBadRequest {
		t.Fatalf("republish = %d %s, want 400", again.code, again.raw)
	}

	v1 := h.mustDataContract(editor, tenant, dpID, map[string]any{
		"schema_definition": map[string]any{
			"type":     "object",
			"required": []any{"title"},
			"properties": map[string]any{
				"title": map[string]any{"type": "string"},
			},
		},
		"validation_mode": "strict",
		"note":            "initial",
	})
	if v1["version"].(float64) != 1 || v1["status"] != contractStatusActive {
		t.Fatalf("v1 contract = %v", v1)
	}
	v2 := h.mustDataContract(editor, tenant, dpID, map[string]any{
		"validation_mode":        "none",
		"completeness_threshold": float64(70),
		"note":                   "quality gate",
	})
	if v2["version"].(float64) != 2 || v2["status"] != contractStatusActive {
		t.Fatalf("v2 contract = %v", v2)
	}

	active := h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/contracts/active", editor, nil, tenantHdr(tenant))
	if active.code != http.StatusOK || active.body["version"].(float64) != 2 {
		t.Fatalf("active contract = %d %s", active.code, active.raw)
	}
	old := h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/contracts/1", editor, nil, tenantHdr(tenant))
	if old.code != http.StatusOK || old.body["status"] != contractStatusSuperseded {
		t.Fatalf("old contract = %d %s", old.code, old.raw)
	}

	dep := h.do("POST", "/v1/m/knowledge/data-products/"+dpID+"/deprecate", editor, nil, tenantHdr(tenant))
	if dep.code != http.StatusOK || dep.body["status"] != dpStatusDeprecated {
		t.Fatalf("deprecate = %d %s", dep.code, dep.raw)
	}
	arch := h.do("POST", "/v1/m/knowledge/data-products/"+dpID+"/archive", admin, nil, tenantHdr(tenant))
	if arch.code != http.StatusOK || arch.body["status"] != dpStatusArchived {
		t.Fatalf("archive = %d %s", arch.code, arch.raw)
	}
	events := h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/events", editor, nil, tenantHdr(tenant))
	if events.code != http.StatusOK {
		t.Fatalf("events = %d %s", events.code, events.raw)
	}
	if got := len(events.body["items"].([]any)); got < 3 {
		t.Fatalf("expected lifecycle events, got %d", got)
	}
	del := h.do("DELETE", "/v1/m/knowledge/data-products/"+dpID, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusOK {
		t.Fatalf("delete data product = %d %s", del.code, del.raw)
	}
	get := h.do("GET", "/v1/m/knowledge/data-products/"+dpID, editor, nil, tenantHdr(tenant))
	if get.code != http.StatusNotFound {
		t.Fatalf("get deleted = %d %s, want 404", get.code, get.raw)
	}
}

func TestDataProductContractEnforcementOnIngest(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dpingest")
	editor := h.roleToken(admin, tenant, "editor@dpingest.io", auth.RoleEditor)
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "contract-kb"})
	dpID := h.mustDataProduct(editor, tenant, map[string]any{
		"name": "contracted-product", "owner_ref": "team:data", "kb_ref": kbID,
	})
	h.mustPublishDataProduct(editor, tenant, dpID)
	h.mustDataContract(editor, tenant, dpID, map[string]any{
		"schema_definition": map[string]any{
			"type":     "object",
			"required": []any{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
		"validation_mode": "strict",
	})

	bad := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "bad", "body": `{"age": 3}`}},
	}, tenantHdr(tenant))
	if bad.code != http.StatusUnprocessableEntity {
		t.Fatalf("strict invalid ingest = %d %s, want 422", bad.code, bad.raw)
	}
	if chunks := h.allChunks(tenant, kbID); len(chunks) != 0 {
		t.Fatalf("strict contract violation must not persist chunks, got %d", len(chunks))
	}
	events := h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/events?event_type=contract_violation", editor, nil, tenantHdr(tenant))
	if events.code != http.StatusOK || len(events.body["items"].([]any)) != 1 {
		t.Fatalf("contract violation events = %d %s", events.code, events.raw)
	}

	good := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "good", "body": `{"name": "alpha"}`}},
	}, tenantHdr(tenant))
	if good.code != http.StatusOK {
		t.Fatalf("strict valid ingest = %d %s", good.code, good.raw)
	}
	if rec := h.dataProductRecord(tenant, dpID); rec.String(colLastIngestAt) == "" {
		t.Fatal("successful ingest should set data_product.last_ingest_at")
	}

	h.mustDataContract(editor, tenant, dpID, map[string]any{
		"schema_definition": map[string]any{
			"type":     "object",
			"required": []any{"code"},
			"properties": map[string]any{
				"code": map[string]any{"type": "string"},
			},
		},
		"validation_mode": "lenient",
	})
	warn := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "warn", "body": `{}`}},
	}, tenantHdr(tenant))
	if warn.code != http.StatusOK {
		t.Fatalf("lenient invalid ingest = %d %s", warn.code, warn.raw)
	}
	events = h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/events?event_type=contract_violation", editor, nil, tenantHdr(tenant))
	if events.code != http.StatusOK || len(events.body["items"].([]any)) < 2 {
		t.Fatalf("lenient violation event missing = %d %s", events.code, events.raw)
	}
}

func TestDataProductQualityGateFreshnessAndUsage(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dpfresh")
	editor := h.roleToken(admin, tenant, "editor@dpfresh.io", auth.RoleEditor)
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "fresh-kb"})
	dpID := h.mustDataProduct(editor, tenant, map[string]any{
		"name": "fresh-product", "owner_ref": "team:data", "kb_ref": kbID,
		"freshness_sla_seconds": float64(3600), "enforcement_mode": "enforce",
	})
	h.mustPublishDataProduct(editor, tenant, dpID)

	ingest := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "fresh", "body": "governed freshness content"}},
	}, tenantHdr(tenant))
	if ingest.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingest.code, ingest.raw)
	}

	mod := h.module()
	mc := dataProductTestMC(h, tenant)
	if _, err := mod.Query(context.Background(), mc, QueryRequest{
		KBID: kbID, Query: "freshness",
	}); err != nil {
		t.Fatalf("fresh query = %v", err)
	}
	if got := h.dataProductRecord(tenant, dpID).Int(colUsageCount); got != 1 {
		t.Fatalf("usage_count after successful query = %d, want 1", got)
	}

	old := model.NewTimestamp(time.Now().Add(-2 * time.Hour)).String()
	h.updateDataProductRaw(tenant, dpID, func(rec model.Record) {
		rec[colFreshnessSLASeconds] = int64(1)
		rec[colLastIngestAt] = old
		rec[colEnforcementMode] = dpModeEnforce
	})
	_, err := mod.Query(context.Background(), mc, QueryRequest{
		KBID: kbID, Query: "freshness",
	})
	qe, ok := IsQueryError(err)
	if !ok || qe.Kind != QueryErrConflict {
		t.Fatalf("stale enforce query error = %v, want conflict", err)
	}
	events := h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/events?event_type=freshness_breach", editor, nil, tenantHdr(tenant))
	if events.code != http.StatusOK || len(events.body["items"].([]any)) == 0 {
		t.Fatalf("freshness breach event missing = %d %s", events.code, events.raw)
	}

	h.updateDataProductRaw(tenant, dpID, func(rec model.Record) {
		rec[colEnforcementMode] = dpModeWarn
	})
	if _, err := mod.Query(context.Background(), mc, QueryRequest{
		KBID: kbID, Query: "freshness",
	}); err != nil {
		t.Fatalf("stale warn query should proceed: %v", err)
	}
	if got := h.dataProductRecord(tenant, dpID).Int(colUsageCount); got != 2 {
		t.Fatalf("usage_count after warn query = %d, want 2", got)
	}
	warnEvents := h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/events?event_type=quality_gate_warn", editor, nil, tenantHdr(tenant))
	if warnEvents.code != http.StatusOK || len(warnEvents.body["items"].([]any)) == 0 {
		t.Fatalf("quality gate warning event missing = %d %s", warnEvents.code, warnEvents.raw)
	}
}

func TestDataProductCompletenessHealthAndMCPExport(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dphealth")
	editor := h.roleToken(admin, tenant, "editor@dphealth.io", auth.RoleEditor)
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "health-kb"})
	dpID := h.mustDataProduct(editor, tenant, map[string]any{
		"name": "health-product", "owner_ref": "team:data", "kb_ref": kbID,
		"quality_score": float64(10), "enforcement_mode": "enforce",
	})
	h.mustPublishDataProduct(editor, tenant, dpID)
	h.mustDataContract(editor, tenant, dpID, map[string]any{
		"validation_mode":        "none",
		"completeness_threshold": float64(70),
	})

	mod := h.module()
	mc := dataProductTestMC(h, tenant)
	_, err := mod.Query(context.Background(), mc, QueryRequest{
		KBID: kbID, Query: "quality",
	})
	qe, ok := IsQueryError(err)
	if !ok || qe.Kind != QueryErrConflict {
		t.Fatalf("completeness gate query error = %v, want conflict", err)
	}

	health := h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/health", editor, nil, tenantHdr(tenant))
	if health.code != http.StatusOK {
		t.Fatalf("health = %d %s", health.code, health.raw)
	}
	quality := health.body["quality"].(map[string]any)
	if quality["status"] != "failing" || health.body["overall_health"] != "unhealthy" {
		t.Fatalf("empty KB health = %v", health.body)
	}

	src := newFakeSource([]contentsource.Document{{DocID: "h1", Title: "Health", Body: "quality gate now has indexed content"}})
	h.addSource("health-src", src)
	ingest := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": "health-src"}, tenantHdr(tenant))
	if ingest.code != http.StatusOK {
		t.Fatalf("health ingest = %d %s", ingest.code, ingest.raw)
	}
	health = h.do("GET", "/v1/m/knowledge/data-products/"+dpID+"/health", editor, nil, tenantHdr(tenant))
	if health.code != http.StatusOK {
		t.Fatalf("health after ingest = %d %s", health.code, health.raw)
	}
	if health.body["overall_health"] != "healthy" {
		t.Fatalf("health after ingest = %v, want healthy", health.body)
	}
	if _, err := mod.Query(context.Background(), mc, QueryRequest{
		KBID: kbID, Query: "indexed",
	}); err != nil {
		t.Fatalf("query after health should pass: %v", err)
	}

	draftID := h.mustDataProduct(editor, tenant, map[string]any{
		"name": "draft-product", "owner_ref": "team:data",
	})
	result, err := mod.ListDataProducts(context.Background(), mc)
	if err != nil {
		t.Fatalf("ListDataProducts = %v", err)
	}
	seenPublished, seenDraft := false, false
	for _, dp := range result.DataProducts {
		if dp.ID == dpID {
			seenPublished = true
			if dp.KBID != kbID || dp.QualityScore != 100 {
				t.Fatalf("published summary = %+v", dp)
			}
		}
		if dp.ID == draftID {
			seenDraft = true
		}
	}
	if !seenPublished || seenDraft {
		t.Fatalf("MCP data products published=%v draft=%v summaries=%+v", seenPublished, seenDraft, result.DataProducts)
	}
}
