// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/finops"
)

// admitsThrough is the admission door a handler outside the module router gets in the
// composition, over a real authorizer: the same Authorize the seam asks.
func admitsThrough(az *auth.Authorizer) func(context.Context, auth.Request) bool {
	return func(ctx context.Context, req auth.Request) bool { return az.Authorize(ctx, req).Allow }
}

// Writing a spend limit asks the admission seam for finops:budget:admin, not the rank of
// the caller's role. A viewer holding a tenant-scope grant may write one; an editor by
// role may not; and a handler with no admission door refuses every write.
func TestAppsGatewaySpendLimitWriteFollowsTheAdmissionSeam(t *testing.T) {
	h := newAppsGatewayHarness(t)
	body := `{"scope":{"type":"organization"},"amount":"1","period":"daily"}`
	send := func(method, path string, p auth.Principal, admits func(context.Context, auth.Request) bool) *httptest.ResponseRecorder {
		t.Helper()
		handler := newAppsGatewayHandler(inferenceProxyConfig{}, h.tenant, fakeProxyAuthr{p: p}, h.ipx, h.fin, nil, time.Now, "test", admits)
		mux := http.NewServeMux()
		mountAppsGatewayHandlers(mux, handler)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		mux.ServeHTTP(rec, req)
		return rec
	}
	post := func(p auth.Principal, admits func(context.Context, auth.Request) bool) *httptest.ResponseRecorder {
		t.Helper()
		return send(http.MethodPost, appsGatewaySpendLimitPath, p, admits)
	}
	viewer := auth.ScopedPrincipal(model.ID("viewer"), "viewer", h.tenant, auth.RoleViewer)
	editor := auth.ScopedPrincipal(model.ID("editor"), "editor", h.tenant, auth.RoleEditor)
	admin := auth.ScopedPrincipal(model.ID("admin"), "admin", h.tenant, auth.RoleAdmin)
	rbacOnly := admitsThrough(auth.NewAuthorizer(nil))
	withGrant := admitsThrough(auth.NewAuthorizer(nil, auth.WithScopedGrants(scopedGrantsOf{finops.PermBudgetAdmin: true})))

	wantWrite := http.StatusOK
	if thisEdition.name == "community" {
		wantWrite = http.StatusNotImplemented
	}
	if rec := post(viewer, withGrant); rec.Code != wantWrite {
		t.Errorf("viewer with a tenant-scope grant: status %d, want the edition-specific write status: %s", rec.Code, rec.Body.String())
	}
	if rec := post(admin, rbacOnly); rec.Code != wantWrite {
		t.Errorf("admin by role: status %d, want the edition-specific write status: %s", rec.Code, rec.Body.String())
	}
	if rec := post(editor, rbacOnly); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"permission_error"`) {
		t.Errorf("editor by role: status %d, want 403 permission_error: %s", rec.Code, rec.Body.String())
	}
	superadmin := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), Superadmin: true}
	if rec := post(superadmin, rbacOnly); rec.Code != wantWrite {
		t.Errorf("superadmin with no membership: status %d, want the edition-specific write status: %s", rec.Code, rec.Body.String())
	}
	// The same gate guards a delete: it is refused before the row is looked up.
	remove := appsGatewaySpendLimitPath + "/spl_none"
	if rec := send(http.MethodDelete, remove, editor, rbacOnly); rec.Code != http.StatusForbidden {
		t.Errorf("editor delete: status %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if rec := send(http.MethodDelete, remove, viewer, withGrant); rec.Code == http.StatusForbidden {
		t.Errorf("viewer with a tenant-scope grant delete was refused by the gate: %s", rec.Body.String())
	}
	if rec := post(admin, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"permission_error"`) {
		t.Errorf("admin without an admission door: status %d, want 403 permission_error: %s", rec.Code, rec.Body.String())
	}
}
