// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"net/http/httptest"
	"testing"
)

func TestJSONResponseLegacyPolicy(t *testing.T) {
	w := httptest.NewRecorder()
	writeResponse(w, 200, nil)
	if w.Code != 200 || w.Body.String() != "null\n" {
		t.Fatalf("nil response = %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", w.Header())
	}
}
