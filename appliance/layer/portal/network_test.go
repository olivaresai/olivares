// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package portal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNetworkPage_RefusesChangesUntilComposition(t *testing.T) {
	h := NewHandler(Status{})
	r := httptest.NewRequest(http.MethodGet, "/network", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Network changes are unavailable") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/network", strings.NewReader(`{}`)))
	if w.Code < 400 {
		t.Fatal("network page accepted unauthenticated change")
	}
}
