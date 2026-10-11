// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvalidBodyUsesDocumentedErrorEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/checks", strings.NewReader("{"))
	var value struct{ Name string }
	if decodeJSON(w, r, &value) || w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body accepted: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "module_error" || body.Error.Message != "invalid JSON body" {
		t.Fatalf("error = %+v", body.Error)
	}
}
