// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// seedEdges upserts n permitted identity→resource edges that share one origin.
func seedEdges(t *testing.T, h *harness, tenant model.TenantID, origin model.ID, n int) {
	t.Helper()
	seedEdgesFrom(t, h, tenant, "identity", origin, n)
}

// seedEdgesFrom upserts n permitted edges from one origin of the given kind, each to a
// fresh resource id.
func seedEdgesFrom(t *testing.T, h *harness, tenant model.TenantID, originKind string, origin model.ID, n int) {
	t.Helper()
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
				OriginKind: originKind, OriginID: origin, ResourceID: model.NewID(),
				Mode: sdkmodel.ModeRead, SignalSource: sdkmodel.SignalPolicy,
				Confidence: sdkmodel.ConfidenceAttributed, Permitted: true,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d edges: %v", n, err)
	}
}

func hasMore(r resp) bool {
	b, _ := r.body["has_more"].(bool)
	return b
}

// A graph read narrowed by ?limit must say there is more, and hand back the
// cursor to the rest, instead of presenting the first page as the whole graph.
func TestAPI_GraphLimitedReadReportsHasMoreAndCursor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedEdges(t, h, tenant, model.NewID(), 3)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	first := h.do("GET", "/v1/m/accessmap/graph?limit=2", editor, tenantHdr(tenant))
	if first.code != http.StatusOK {
		t.Fatalf("graph = %d %s", first.code, first.raw)
	}
	if edges, _ := first.body["edges"].([]any); len(edges) != 2 {
		t.Fatalf("first page edges = %d, want 2 (%s)", len(edges), first.raw)
	}
	cursor, _ := first.body["cursor"].(string)
	if !hasMore(first) || cursor == "" {
		t.Fatalf("first page has_more=%v cursor=%q, want has_more=true and a cursor (%s)", hasMore(first), cursor, first.raw)
	}

	rest := h.do("GET", "/v1/m/accessmap/graph?limit=2&cursor="+cursor, editor, tenantHdr(tenant))
	if edges, _ := rest.body["edges"].([]any); rest.code != http.StatusOK || len(edges) != 1 || hasMore(rest) {
		t.Fatalf("second page = %d has_more=%v %s, want the 1 remaining edge and has_more=false", rest.code, hasMore(rest), rest.raw)
	}
}

// The store returns at most 1000 neighbors per direction and no page token.
// A node at that ceiling must be reported as possibly truncated, never complete.
func TestAPI_NeighborsAtStoreCapReportsHasMore(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	origin := model.NewID()
	seedEdges(t, h, tenant, origin, 1001)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	r := h.do("GET", "/v1/m/accessmap/neighbors?direction=outgoing&id="+origin.String(), editor, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("neighbors = %d %.200s", r.code, r.raw)
	}
	if edges, _ := r.body["edges"].([]any); len(edges) != 1000 {
		t.Fatalf("neighbors edges = %d, want the store cap 1000", len(edges))
	}
	if !hasMore(r) {
		t.Fatalf("neighbors at the store cap answered has_more=false: a partial graph is shown as complete")
	}

	// Below the cap the answer really is complete.
	small := model.NewID()
	seedEdges(t, h, tenant, small, 3)
	r = h.do("GET", "/v1/m/accessmap/neighbors?direction=outgoing&id="+small.String(), editor, tenantHdr(tenant))
	if edges, _ := r.body["edges"].([]any); len(edges) != 3 || hasMore(r) {
		t.Fatalf("small neighbors = %d edges has_more=%v, want 3 edges and has_more=false", len(edges), hasMore(r))
	}
}

// Invalid limits keep the published default-page response and add a warning.
// Valid limits and modes continue to work.
func TestAPI_GraphAndDriftInvalidLimitsWarnAndRetainDefaultPage(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedEdges(t, h, tenant, model.NewID(), 2)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	for _, view := range []string{"graph", "drift"} {
		defaultPage := h.do("GET", "/v1/m/accessmap/"+view, editor, tenantHdr(tenant))
		for _, bad := range []string{"limit=abc", "limit=-1", "limit=1.5"} {
			if r := h.do("GET", "/v1/m/accessmap/"+view+"?"+bad, editor, tenantHdr(tenant)); r.code != http.StatusOK || !strings.Contains(strings.Join(r.header.Values("Warning"), ", "), "Invalid limit") || !reflect.DeepEqual(r.body, defaultPage.body) {
				t.Errorf("%s?%s = %d %.100s, want 200 with a deprecation warning", view, bad, r.code, r.raw)
			}
		}
		for _, good := range []string{"mode=readwrite", "mode=read", "mode=unknown", "limit=0", "limit=5"} {
			if r := h.do("GET", "/v1/m/accessmap/"+view+"?"+good, editor, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Errorf("%s?%s = %d %.100s, want 200", view, good, r.code, r.raw)
			}
		}
	}
}

// Exactly 1000 neighbors is the store ceiling too: flagged, with the documented ponytail
// false positive, and a both-direction read reaching it through its merged sides is flagged.
func TestAPI_NeighborsBothDirectionsReportsTheCeiling(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	origin := model.NewID()
	seedEdges(t, h, tenant, origin, 1000)
	editor := h.editorToken(admin, tenant, "e@acme.com")

	r := h.do("GET", "/v1/m/accessmap/neighbors?direction=both&id="+origin.String(), editor, tenantHdr(tenant))
	if edges, _ := r.body["edges"].([]any); r.code != http.StatusOK || len(edges) != 1000 || !hasMore(r) {
		t.Errorf("both-direction neighbors at the ceiling = %d, %d edges, has_more=%v", r.code, len(edges), hasMore(r))
	}
}

// A drift reconciliation is over the full window; legacy paging remains accepted
// and warns rather than implying that the cursor selected a page.
func TestAPI_DriftPagingWarnsAndRetainsTheWindow(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedEdges(t, h, tenant, model.NewID(), 3)
	editor := h.editorToken(admin, tenant, "e@acme.com")
	whole := h.do("GET", "/v1/m/accessmap/drift", editor, tenantHdr(tenant))
	paged := h.do("GET", "/v1/m/accessmap/drift?limit=1&cursor=legacy", editor, tenantHdr(tenant))
	if paged.code != http.StatusOK || !reflect.DeepEqual(paged.body, whole.body) || !strings.Contains(paged.header.Get("Warning"), "Drift paging") {
		t.Fatalf("drift legacy paging = %d %s warning=%q", paged.code, paged.raw, paged.header.Get("Warning"))
	}
	if whole.header.Get("Warning") != "" {
		t.Fatal("unpaged drift must not be deprecated")
	}
}
