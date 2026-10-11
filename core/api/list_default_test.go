// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestStableListDefault(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "list-default")
	ctx := t.Context()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := range 101 {
			name := fmt.Sprintf("item-%03d", i)
			if _, err := sc.Agents().Create(ctx, model.Agent{Name: name, Kind: "claude-code", Status: model.StatusActive}); err != nil {
				return err
			}
			if _, err := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: name, Slug: name, Status: model.StatusActive}); err != nil {
				return err
			}
			if _, err := sc.Workspaces().Create(ctx, model.Workspace{Name: name, Slug: name, Status: model.StatusActive}); err != nil {
				return err
			}
			now := model.NewTimestamp(time.Now())
			if _, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
				OriginKind: "agent", OriginID: model.NewID(), ResourceID: model.NewID(),
				Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalOTEL,
				Confidence: sdkmodel.ConfidenceApproximate, Observed: true, FirstSeen: now, LastSeen: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		for i := range 101 {
			if _, err := as.Users().Create(ctx, model.User{Email: fmt.Sprintf("user-%03d@example.test", i), Status: model.StatusActive}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	drafts := make([]model.AuditDraft, 101)
	for i := range drafts {
		drafts[i] = model.AuditDraft{Actor: "pagination", ActorKind: model.ActorAgent, Action: "pagination.seed", TargetKind: "core.agent"}
	}
	appendAuditDrafts(t, h, tenant, drafts...)
	appendAuditDrafts(t, h, model.SystemTenantID, drafts...)
	page := h.do("GET", "/v1/agent-groups", admin, nil, tenantHdr(tenant))
	if page.code != http.StatusOK || len(page.body["items"].([]any)) != 100 || page.body["has_more"] != true {
		t.Fatal("agent-group default must remain 100 with another page")
	}
	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	for _, path := range []string{"/v1/access-edges", "/v1/agents", "/v1/audit", "/v1/audit/system", "/v1/users", "/v1/workspaces"} {
		t.Run(path, func(t *testing.T) {
			t.Run("OpenAPI", func(t *testing.T) {
				op := doc["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
				for _, raw := range op["parameters"].([]any) {
					param := raw.(map[string]any)
					if param["name"] == "limit" && param["in"] == "query" {
						if got := param["schema"].(map[string]any)["default"]; got != float64(50) {
							t.Fatalf("limit default = %v, want 50", got)
						}
						return
					}
				}
				t.Fatal("missing limit parameter")
			})
			for _, tc := range []struct {
				query string
				want  int
			}{{"", 50}, {"?limit=51", 51}} {
				t.Run("HTTP"+tc.query, func(t *testing.T) {
					page := h.do("GET", path+tc.query, admin, nil, tenantHdr(tenant))
					if page.code != http.StatusOK {
						t.Fatalf("status = %d: %s", page.code, page.raw)
					}
					items := page.body["items"].([]any)
					if len(items) != tc.want || page.body["has_more"] != true {
						t.Fatalf("rows=%d has_more=%v, want %d and true", len(items), page.body["has_more"], tc.want)
					}
				})
			}
		})
	}
	for _, path := range []string{"/v1/audit", "/v1/audit/system"} {
		t.Run(path+"/filtered", func(t *testing.T) {
			page := h.do("GET", path+"?action=pagination.seed", admin, nil, tenantHdr(tenant))
			if page.code != http.StatusOK {
				t.Fatalf("filtered status=%d", page.code)
			}
			if len(page.body["items"].([]any)) != 50 || page.body["has_more"] != true {
				t.Fatalf("filtered default: status=%d rows=%d has_more=%v", page.code, len(page.body["items"].([]any)), page.body["has_more"])
			}
		})
	}
}
