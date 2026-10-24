// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The console's last-hour swimlane reads each live session's events from an hour ago
// on: `since` keeps a long session's earlier events out of the page, on both the
// by-id and the legacy timeline, and a since that is no time is refused.
func TestTimelineSince(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.viewerToken(admin, tenant, "v@acme.com")
	ctx := context.Background()
	for i, tool := range []string{"Read", "Grep", "Edit"} {
		at := baseTime.Add(time.Duration(i) * 30 * time.Minute)
		if err := m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "file", "/a", sdkmodel.ModeRead, tool, at)); err != nil {
			t.Fatal(err)
		}
	}
	r := h.do("GET", "/v1/m/sessions/live/sess-1", viewer, tenantHdr(tenant))
	liveRef, _ := r.body["live_ref"].(string)
	if r.code != http.StatusOK || liveRef == "" {
		t.Fatalf("get live = %d %s", r.code, r.raw)
	}
	since := url.QueryEscape(baseTime.Add(30 * time.Minute).Format(time.RFC3339))
	for _, path := range []string{
		"/v1/m/sessions/live/sess-1/timeline",
		"/v1/m/sessions/live/by-id/" + liveRef + "/timeline",
	} {
		r := h.do("GET", path+"?since="+since, viewer, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("%s since = %d %s", path, r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		var tools []string
		for _, it := range items {
			tools = append(tools, it.(map[string]any)["tool_ref"].(string))
		}
		if len(tools) != 2 || tools[0] != "Grep" || tools[1] != "Edit" {
			t.Errorf("%s since 12:30 = %v, want [Grep Edit]", path, tools)
		}
		if r := h.do("GET", path, viewer, tenantHdr(tenant)); len(r.body["items"].([]any)) != 3 {
			t.Errorf("%s without since = %s, want all three events", path, r.raw)
		}
		for _, bad := range []string{"yesterday", ""} {
			if r := h.do("GET", path+"?since="+bad, viewer, tenantHdr(tenant)); r.code != http.StatusBadRequest {
				t.Errorf("%s since=%q = %d %s, want 400", path, bad, r.code, r.raw)
			}
		}
		// The same moment in another zone reads the same events.
		offset := url.QueryEscape(baseTime.Add(30 * time.Minute).In(time.FixedZone("CEST", 2*3600)).Format(time.RFC3339))
		if r := h.do("GET", path+"?since="+offset, viewer, tenantHdr(tenant)); len(r.body["items"].([]any)) != 2 {
			t.Errorf("%s since=%s = %s, want the two events from 12:30 UTC", path, offset, r.raw)
		}
	}
}
