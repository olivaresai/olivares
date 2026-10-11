// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	egressproxy "github.com/olivaresai/olivares/connectors/egress-proxy"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inventory"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestConfiguredProxyAIDiscoveryAndOutage(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			cfg := store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
			if backend == "postgres" {
				if !enginetest.PostgresAvailable(t) {
					t.Skip("PostgreSQL fixture unavailable; named PG16 leg runs separately")
				}
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg = store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, Debug: true, MaxConns: 8}
			}
			configuredProxyAIDiscoveryAndOutage(t, cfg)
		})
	}
}

func configuredProxyAIDiscoveryAndOutage(t *testing.T, cfg store.Config) {
	t.Helper()
	m := inventory.New(inventory.WithCollectionCoverage())
	h := newHarnessConfig(t, m, cfg)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "proxy-ai")
	other := h.createOrg(admin, "proxy-other")
	viewer := h.viewerToken(admin, tenant, "viewer@proxy.test")
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Resources().Create(ctx, model.Resource{
			Name: "existing.ai", Kind: "http.api", URI: "existing.ai",
			Metadata: map[string]any{"private": "c34-label-canary"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "proxy.jsonl")
	log := `{"ts":"2026-10-02T12:00:00Z","fqdn":"api.anthropic.com","identity":"worker","decision":"allow","method":"POST","body":"c34-payload-canary","authorization":"c34-key-canary"}
{"ts":"2026-10-02T12:00:01Z","fqdn":"existing.ai","identity":"worker","decision":"allow","method":"POST"}
{"fqdn":"untimed.ai","identity":"worker","decision":"allow","method":"POST"}
{"ts":"2026-10-02T12:00:02Z","fqdn":"denied.ai","identity":"worker","decision":"deny","method":"POST"}
`
	if err := os.WriteFile(path, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stop)
	})
	reg := event.SourceRegistration{SourceID: "c34-proxy", SourceRevision: 1, EnvironmentRef: "c34-env"}
	sourceConfig := sdk.Config{Settings: map[string]string{"path": path, "ai_hosts": "api.anthropic.com,existing.ai,untimed.ai,denied.ai"}}
	if err := rt.AddPreparedSourceRegistered(ctx, "c34-proxy", rt.PrepareInProcSource(egressproxy.New()), sourceConfig, tenant.String(), 30*time.Millisecond, reg); err != nil {
		t.Fatal(err)
	}
	h.waitCatalog(tenant, 5)
	coverageWait(t, h, viewer, tenant, reg, func(item map[string]any) bool {
		current := item["current"].(map[string]any)
		return current["run_order"].(float64) >= 2 && current["coverage"] == "unknown" && current["reason"] == "scope_unproven"
	})
	list := h.do("GET", "/v1/m/inventory/entities?kind=resource", viewer, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("list: %d %s", list.code, list.raw)
	}
	items := list.body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("duplicate polling or denied access materialized extra endpoints: %d", len(items))
	}
	for _, item := range items {
		entry := item.(map[string]any)
		uri, id := entry["ref"].(string), entry["entity_id"].(string)
		p := "/v1/m/inventory/entities/resource/" + id
		detail := h.do("GET", p, viewer, nil, tenantHdr(tenant))
		if detail.code != http.StatusOK {
			t.Fatalf("detail: %d %s", detail.code, detail.raw)
		}
		want := "unregistered_at_discovery"
		if uri == "existing.ai" {
			want = "unknown"
		}
		if detail.body["detail"].(map[string]any)["ai_discovery"] != want {
			t.Fatalf("%s discovery state = %v, want %s", uri, detail.body["detail"], want)
		}
		if uri == "untimed.ai" && detail.body["entry"].(map[string]any)["occurred_at"] != nil {
			t.Fatal("missing log time became a source-declared occurrence")
		}
		history := h.do("GET", p+"/observations", viewer, nil, tenantHdr(tenant))
		if history.code != http.StatusOK || !strings.Contains(history.raw, reg.SourceID) {
			t.Fatalf("source provenance missing: %d %s", history.code, history.raw)
		}
		signals := detail.body["entry"].(map[string]any)["signal_sources"].([]any)
		if len(signals) != 1 || signals[0] != "egress_proxy_ai" {
			t.Fatalf("catalog signal changed: %v", signals)
		}
		if uri == "api.anthropic.com" {
			receipt := history.body["items"].([]any)[0].(map[string]any)
			occurred, err := time.Parse(time.RFC3339Nano, receipt["source_occurred_at"].(string))
			if err != nil || !occurred.Equal(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)) {
				t.Fatal("declared occurrence missing from receipt")
			}
		}
		for _, canary := range []string{"c34-payload-canary", "c34-key-canary", "c34-label-canary"} {
			if strings.Contains(detail.raw+history.raw, canary) {
				t.Fatal("payload, credential or arbitrary core label exposed")
			}
		}
		if cross := h.do("GET", p, admin, nil, tenantHdr(other)); cross.code != http.StatusNotFound {
			t.Fatalf("other-tenant point read = %d, want404", cross.code)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	coverageWait(t, h, viewer, tenant, reg, func(item map[string]any) bool {
		current := item["current"].(map[string]any)
		return current["coverage"] == "unavailable" && current["reason"] == "gather_error"
	})
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	coverageWait(t, h, viewer, tenant, reg, func(item map[string]any) bool {
		current := item["current"].(map[string]any)
		return current["coverage"] == "unknown" && current["reason"] == "scope_unproven" && item["last_qualified_success"] == nil
	})
	retained := h.do("GET", "/v1/m/inventory/entities?kind=resource", viewer, nil, tenantHdr(tenant))
	if retained.code != 200 || len(retained.body["items"].([]any)) != 3 {
		t.Fatal("outage or empty log erased historical endpoints")
	}
}
