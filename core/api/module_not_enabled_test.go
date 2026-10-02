// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A route of a module this node does not run answers 404 module_not_enabled and
// names the module in a structured field, so the console offers the action that
// enables that module instead of parsing the message.
func TestModuleNotEnabledErrorNamesTheModule(t *testing.T) {
	rec := httptest.NewRecorder()
	moduleNotEnabledHandler("reporting").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/m/reporting/reports", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Module  string `json:"module"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	if body.Error.Code != "module_not_enabled" || body.Error.Module != "reporting" || body.Error.Message == "" {
		t.Fatalf("error = %+v, want code module_not_enabled, module reporting and a message", body.Error)
	}
}
