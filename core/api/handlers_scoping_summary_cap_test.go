// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The workspace summary reports counts derived from a List page, and the store
// clamps a page to maxLimit (1000). Before 2026-08-24 the handler discarded the
// model.Page that says the page was truncated, so a workspace with 1001 agents
// and one with 50000 both answered "1000" and nothing said otherwise.
//
// This test pins BOTH directions, because only one of them is the interesting
// one and a flag that is always true would pass a test that checks only the cap:
//
//	over the cap   -> count is the clamp AND the capped flag is true
//	under the cap  -> exact count AND the capped flag is FALSE
//
// The endpoint had no test at all before this one.
func TestWorkspaceSummaryCountsReportTheirOwnTruncation(t *testing.T) {
	const storeMaxLimit = 1000 // core/internal/store/sqlstore/generic.go maxLimit

	h := newHarness(t)
	tok := h.adminLogin()
	tenant := h.createOrg(tok, "wscap")
	ctx := context.Background()

	var overID, underID model.ID
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		over, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Over", Slug: "over", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		under, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Under", Slug: "under", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		overID, underID = over.ID, under.ID
		// One past the clamp: the smallest input that can tell a total from a page.
		for i := 0; i < storeMaxLimit+1; i++ {
			if _, err := sc.Agents().Create(ctx, model.Agent{
				Name: fmt.Sprintf("over-%04d", i), Kind: "service",
				Status: model.StatusActive, WorkspaceID: over.ID,
			}); err != nil {
				return err
			}
		}
		for i := 0; i < 3; i++ {
			if _, err := sc.Agents().Create(ctx, model.Agent{
				Name: fmt.Sprintf("under-%d", i), Kind: "service",
				Status: model.StatusActive, WorkspaceID: under.ID,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	summary := func(id model.ID) map[string]any {
		t.Helper()
		r := h.do("GET", "/v1/workspaces/"+id.String()+"/summary", tok, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("summary %s = %d %s", id, r.code, r.raw)
		}
		return r.body
	}

	over := summary(overID)
	if got := int(over["agent_count"].(float64)); got != storeMaxLimit {
		t.Fatalf("over-cap agent_count = %d, want the clamp %d", got, storeMaxLimit)
	}
	if capped, _ := over["agent_count_capped"].(bool); !capped {
		t.Fatalf("over-cap agent_count_capped = false with %d agents seeded: the count is a "+
			"floor and the response says it is a total", storeMaxLimit+1)
	}

	// The non-firing direction. A flag hardwired to true would satisfy the check
	// above and still tell the console nothing.
	under := summary(underID)
	if got := int(under["agent_count"].(float64)); got != 3 {
		t.Fatalf("under-cap agent_count = %d, want 3", got)
	}
	if capped, _ := under["agent_count_capped"].(bool); capped {
		t.Fatalf("under-cap agent_count_capped = true with 3 agents: the flag fires on a " +
			"count that is exact")
	}
	// The three siblings share the handler's shape; an empty workspace must not
	// claim truncation on any of them.
	for _, k := range []string{"session_count_capped", "resource_count_capped", "group_count_capped"} {
		if capped, _ := under[k].(bool); capped {
			t.Fatalf("under-cap %s = true with nothing seeded", k)
		}
	}
}
