// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// pathWorld is a small estate: agent A holds one write on the shared resource, agent
// B holds writes on the shared resource and on `elsewhere` more resources, and the
// shared resource is sensitive.
type pathWorld struct {
	a, b   model.Agent
	shared model.Resource
}

func seedPathWorld(t *testing.T, h *harness, tenant model.TenantID, elsewhere int) pathWorld {
	t.Helper()
	ctx := context.Background()
	var w pathWorld
	err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if w.a, err = sc.Agents().Create(ctx, model.Agent{Name: "agent-a", Kind: "claude-code", ExternalID: "ext-a", Status: model.StatusActive}); err != nil {
			return err
		}
		if w.b, err = sc.Agents().Create(ctx, model.Agent{Name: "agent-b", Kind: "claude-code", ExternalID: "ext-b", Status: model.StatusActive}); err != nil {
			return err
		}
		if w.shared, err = sc.Resources().Create(ctx, model.Resource{Name: "shared-db", Kind: "postgres.table", URI: "pg://shared", Sensitivity: "high"}); err != nil {
			return err
		}
		write := func(agent, resource model.ID) error {
			_, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
				OriginKind: "agent", OriginID: agent, ResourceID: resource,
				Mode: sdkmodel.ModeReadWrite, SignalSource: sdkmodel.SignalPGAudit,
				Confidence: sdkmodel.ConfidenceAttributed, Observed: true,
			})
			return err
		}
		if err := write(w.a.ID, w.shared.ID); err != nil {
			return err
		}
		if err := write(w.b.ID, w.shared.ID); err != nil {
			return err
		}
		for i := 0; i < elsewhere; i++ {
			r, err := sc.Resources().Create(ctx, model.Resource{Name: fmt.Sprintf("sink-%d", i), Kind: "s3.bucket", URI: fmt.Sprintf("s3://sink-%d", i)})
			if err != nil {
				return err
			}
			if err := write(w.b.ID, r.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed estate: %v", err)
	}
	return w
}

func truncated(r resp) bool {
	b, _ := r.body["truncated"].(bool)
	return b
}

// escalation_paths and exfil_routes were never assigned, so the summary printed 0 as
// if it had measured them.
func TestAPI_AttackPathSummaryCountsEscalationAndExfil(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedPathWorld(t, h, tenant, 2)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	r := h.do("GET", "/v1/m/accessmap/attack-paths/summary", editor, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("summary = %d %s", r.code, r.raw)
	}
	// A shares the shared resource with B, who holds three writes to A's one.
	if got := jsonNum(r.body["escalation_paths"]); got != 1 {
		t.Errorf("escalation_paths = %d, want 1 (A -> shared -> B) (%s)", got, r.raw)
	}
	// Out of the one sensitive resource, B (a reader of it) can write the two sinks.
	if got := jsonNum(r.body["exfil_routes"]); got != 2 {
		t.Errorf("exfil_routes = %d, want 2 (shared -> B -> sink-0/sink-1) (%s)", got, r.raw)
	}
	if got := jsonNum(r.body["total_agents"]); got != 2 {
		t.Errorf("total_agents = %d, want 2", got)
	}
	for field, want := range map[string]int{"reachable_paths": 4, "critical_agents": 0, "sensitive_targets": 1} {
		if got := jsonNum(r.body[field]); got != want {
			t.Errorf("%s = %d, want %d (%s)", field, got, want, r.raw)
		}
	}
	if truncated(r) {
		t.Errorf("a small estate must not be marked truncated: %s", r.raw)
	}
}

// The summary must agree with the per-agent and per-resource answers it totals.
func TestAPI_AttackPathSummaryAgreesWithTheDetailRoutes(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	w := seedPathWorld(t, h, tenant, 2)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	esc := h.do("GET", "/v1/m/accessmap/attack-paths/escalation?agent_id="+w.a.ID.String(), editor, tenantHdr(tenant))
	exf := h.do("GET", "/v1/m/accessmap/attack-paths/exfil?resource_id="+w.shared.ID.String(), editor, tenantHdr(tenant))
	sum := h.do("GET", "/v1/m/accessmap/attack-paths/summary", editor, tenantHdr(tenant))
	escPaths, _ := esc.body["paths"].([]any)
	exfPaths, _ := exf.body["paths"].([]any)
	if len(escPaths) != 1 || len(exfPaths) != 2 {
		t.Fatalf("detail routes = %d escalation, %d exfil paths, want 1 and 2 (%s / %s)", len(escPaths), len(exfPaths), esc.raw, exf.raw)
	}
	if jsonNum(sum.body["escalation_paths"]) != len(escPaths) || jsonNum(sum.body["exfil_routes"]) != len(exfPaths) {
		t.Errorf("summary %s disagrees with the detail routes (%d escalation, %d exfil)", sum.raw, len(escPaths), len(exfPaths))
	}
}

// An unknown agent_id is a 404 on escalation, as it already is on reachability, not a
// 200 with no paths that the CLI prints as "no escalation paths found".
func TestAPI_EscalationOfAnUnknownAgentIsNotFound(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedPathWorld(t, h, tenant, 0)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	for _, route := range []string{"reachability", "escalation"} {
		r := h.do("GET", "/v1/m/accessmap/attack-paths/"+route+"?agent_id="+model.NewID().String(), editor, tenantHdr(tenant))
		if r.code != http.StatusNotFound {
			t.Errorf("%s for an unknown agent = %d %s, want 404", route, r.code, r.raw)
		}
	}
}

// A path list cut at the path bound, or a read cut at the store ceiling, says so.
func TestAPI_AttackPathAnswersReportTruncation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	w := seedPathWorld(t, h, tenant, 101) // 101 sinks: one more route than the 100-path bound
	editor := h.editorToken(admin, tenant, "e@acme.com")

	exf := h.do("GET", "/v1/m/accessmap/attack-paths/exfil?resource_id="+w.shared.ID.String(), editor, tenantHdr(tenant))
	if paths, _ := exf.body["paths"].([]any); exf.code != http.StatusOK || len(paths) != 100 || !truncated(exf) {
		t.Errorf("exfil = %d, %d paths, truncated=%v; want 100 paths and truncated=true (%.200s)", exf.code, len(paths), truncated(exf), exf.raw)
	}

	// Below the bound the answer is whole and unmarked.
	small := h.createOrg(admin, "globex")
	w2 := seedPathWorld(t, h, small, 3)
	editor2 := h.editorToken(admin, small, "e2@globex.com")
	ok := h.do("GET", "/v1/m/accessmap/attack-paths/exfil?resource_id="+w2.shared.ID.String(), editor2, tenantHdr(small))
	if paths, _ := ok.body["paths"].([]any); len(paths) != 3 || truncated(ok) {
		t.Errorf("small exfil = %d paths truncated=%v, want 3 and false (%.200s)", len(paths), truncated(ok), ok.raw)
	}
}

// Reachability reads at the store's 1000-row ceiling: an agent past it is truncated.
func TestAPI_ReachabilityAtStoreCapReportsTruncation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	var agent model.Agent
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		agent, err = sc.Agents().Create(context.Background(), model.Agent{Name: "busy", Kind: "claude-code", ExternalID: "ext-busy", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	seedEdgesFrom(t, h, tenant, "agent", agent.ID, 1001)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	r := h.do("GET", "/v1/m/accessmap/attack-paths/reachability?agent_id="+agent.ID.String(), editor, tenantHdr(tenant))
	if paths, _ := r.body["paths"].([]any); r.code != http.StatusOK || len(paths) != 1000 || !truncated(r) {
		t.Errorf("reachability = %d, %d paths, truncated=%v; want 1000 paths and truncated=true", r.code, len(paths), truncated(r))
	}
}
