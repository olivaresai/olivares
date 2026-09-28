// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// The three routes whose module method ends in `return ..., storeError(err)`
// with err == nil: list intents, list observations, delete target.
func TestAdoptedReadAndDeleteRoutesSucceed(t *testing.T) {
	h := newHarness(t)
	r, err := h.push(h.user(), "op-rd", "refs/heads/olivares/rd", "")
	if err != nil || r.Intent.State != StateApplied {
		t.Fatalf("setup push = %+v %v", r.Intent, err)
	}
	list := handlerFor(t, h.m, http.MethodGet, "/intents")
	req := httptest.NewRequest(http.MethodGet, "/x?target_id="+h.target.ID.String(), nil)
	rec := httptest.NewRecorder()
	list(rec, req, api.ModuleContext{Principal: h.user().Principal, Tenant: h.tenant})
	obs := call(handlerFor(t, h.m, http.MethodGet, "/intents/{id}/observations"), h.user(), http.MethodGet, r.Intent.ID.String(), nil)
	del := call(handlerFor(t, h.m, http.MethodDelete, "/targets/{id}"), h.admin(), http.MethodDelete, h.target.ID.String(), nil)
	var gone bool
	_ = h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		_, e := loadTarget(context.Background(), sc, h.target.ID)
		gone = e != nil
		return nil
	})
	t.Logf("GET /intents = %d %s", rec.Code, rec.Body.String())
	t.Logf("GET /intents/{id}/observations = %d %s", obs.Code, obs.Body.String())
	t.Logf("DELETE /targets/{id} = %d %s (row deleted: %v)", del.Code, del.Body.String(), gone)
	if rec.Code != http.StatusOK || obs.Code != http.StatusOK || del.Code != http.StatusNoContent {
		t.Fatalf("list intents %d, observations %d, delete %d: want 200, 200, 204", rec.Code, obs.Code, del.Code)
	}
}
