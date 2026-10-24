// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A pgcontent table that declares no ACL columns is denied to an identity with no
// groups until the operator declares the source public (#3030). The real
// connector reads a real export, the document sources are wired exactly as boot
// wires them (knowledgeContentSources, then openAll), and the engine's own
// routes ingest, query and scan.
func TestPgcontentTableWithoutACLIsDeniedUntilTheSourceIsPublic(t *testing.T) {
	h := newHarness(t)
	export := filepath.Join(t.TempDir(), "orders.json")
	rows := `{"schema":"public","table":"orders","rows":[` +
		`{"id":"1","title":"Order one","body":"turbine calibration alpha"},` +
		`{"id":"2","title":"Order two","body":"turbine calibration beta"}]}`
	if err := os.WriteFile(export, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	source := func(name, public string) map[string]any {
		cfg := map[string]string{
			"mode": "export", "export_path": export, "table": "orders",
			"key_columns": "id", "body_columns": "body", "title_column": "title",
		}
		if public != "" {
			cfg["public"] = public
		}
		return map[string]any{"name": name, "kind": "pgcontent", "config": cfg}
	}
	// The operator's file, read by the same loader boot uses.
	file, err := json.Marshal(map[string]any{"documents": []map[string]any{
		source("orders", ""), source("orders-public", "true"), source("orders-typo", "yes"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(configPath, file, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_SOURCES_CONFIG", configPath)
	cfg, err := loadSourcesConfig(quietLog())
	if err != nil {
		t.Fatalf("load sources config: %v", err)
	}
	pending := knowledgeContentSources(cfg, quietLog())
	(&deferredSecretWiring{content: pending, knowledge: h.set.knowledge}).openAll(context.Background(), nil, quietLog())

	visible := func(name string) float64 {
		t.Helper()
		var kb struct {
			ID string `json:"id"`
		}
		if code := h.reqInto("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA, map[string]any{
			"name": "kb-" + name, "classification": "public",
		}, &kb); code != http.StatusCreated || kb.ID == "" {
			t.Fatalf("create KB for %s = %d", name, code)
		}
		if code, raw := h.req("POST", "/v1/m/knowledge/kbs/"+kb.ID+"/ingest", h.adminToken, h.tenantA, map[string]any{"source": name}); code != http.StatusOK {
			t.Fatalf("ingest %s = %d %s", name, code, raw)
		}
		// No agent_ref: the identity has no groups, so only an explicit "anyone" shows.
		var q struct {
			Count float64 `json:"count"`
		}
		if code := h.reqInto("POST", "/v1/m/knowledge/kbs/"+kb.ID+"/query", h.adminToken, h.tenantA, map[string]any{
			"query": "turbine calibration", "top_k": 5,
		}, &q); code != http.StatusOK {
			t.Fatalf("query %s = %d", name, code)
		}
		return q.Count
	}
	if n := visible("orders"); n != 0 {
		t.Errorf("a table without ACL columns returned %v rows to an identity with no groups, want 0", n)
	}
	if n := visible("orders-typo"); n != 0 {
		t.Errorf(`public "yes" is not "true": %v rows visible, want 0`, n)
	}
	if n := visible("orders-public"); n != 2 {
		t.Errorf(`a source declared "public": "true" returned %v rows, want 2`, n)
	}

	var scan struct {
		DocumentsWithoutACL float64 `json:"documents_without_acl"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/sources/orders/scan", h.adminToken, h.tenantA, nil, &scan); code != http.StatusOK {
		t.Fatalf("source scan = %d", code)
	}
	if scan.DocumentsWithoutACL != 2 {
		t.Errorf("documents_without_acl = %v, want 2", scan.DocumentsWithoutACL)
	}

	tenant, err := model.ParseTenantID(h.tenantA)
	if err != nil {
		t.Fatal(err)
	}
	publicEntries := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == "knowledge.source.public" {
				publicEntries++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if publicEntries != 1 {
		t.Errorf("knowledge.source.public ledger entries = %d, want 1 (only the public source's ingest)", publicEntries)
	}
}
