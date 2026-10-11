// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestSidebarCursorPagination(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sidebar-pages")
	viewer := h.viewerToken(admin, tenant, "sidebar@x.io")
	ctx := context.Background()
	for i := 0; i < 35; i++ {
		if err := m.onEdge(ctx, tenant.String(), sessEdge(fmt.Sprintf("sess-%d", i), "file", "/a", sdkmodel.ModeRead, "Read", baseTime)); err != nil {
			t.Fatal(err)
		}
	}
	// Real records through the same repository the runtime uses; no process launch is needed for a list read.
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for i := 0; i < 35; i++ {
			_, err = repo.Create(ctx, model.Record{colRunRef: fmt.Sprintf("run-%d", i), colState: stateRunning, colTransport: string(TransportStreamJSON), colPermissionMode: "default", colIsolation: string(IsolationNative), colLastEventSeq: int64(0)})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"live", "runs"} {
		t.Run(source, func(t *testing.T) {
			seen := map[string]bool{}
			cursor := ""
			for page := 0; page < 3; page++ {
				r := h.do("GET", "/v1/m/sessions/"+source+"?pagination=cursor&limit=30&cursor="+url.QueryEscape(cursor), viewer, tenantHdr(tenant))
				if r.code != http.StatusOK {
					t.Fatalf("page = %d %s", r.code, r.raw)
				}
				for _, item := range r.body["items"].([]any) {
					key := "session_ref"
					if source == "runs" {
						key = "run_ref"
					}
					ref := item.(map[string]any)[key].(string)
					if seen[ref] {
						t.Fatalf("repeated row %s", ref)
					}
					seen[ref] = true
				}
				if r.body["has_more"] == false {
					break
				}
				next, _ := r.body["cursor"].(string)
				if next == "" || next == cursor {
					t.Fatal("nonadvancing cursor")
				}
				cursor = next
			}
			if len(seen) != 35 {
				t.Fatalf("read %d rows, want 35", len(seen))
			}
		})
	}
}
