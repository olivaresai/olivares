// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

func TestTracingCLIChangesOnlyNamedChoices(t *testing.T) {
	choice := obstrace.DefaultSettings()
	choice.Endpoint, choice.SampleRatio = "https://collector.example:4318", 0.25
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/system/tracing" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("tracing did not use the authenticated endpoint")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&choice); err != nil {
				t.Error(err)
				return
			}
			writes++
		}
		_ = json.NewEncoder(w).Encode(api.TracingStatus{Settings: choice, Effective: choice, Overrides: []string{}})
	}))
	defer server.Close()
	out, _, err := execRoot(t, withConnect(server.URL, "config", "tracing", "set", "--enabled", "--collector-insecure", "-o", "json")...)
	var state api.TracingStatus
	if err != nil || json.Unmarshal([]byte(out), &state) != nil || !state.Settings.Enabled || !state.Settings.Insecure || state.Settings.SampleRatio != 0.25 || state.Settings.Endpoint != "https://collector.example:4318" || writes != 1 {
		t.Fatalf("set must preserve unnamed choices and return JSON: %v %s writes=%d", err, out, writes)
	}
	if _, _, err := execRoot(t, withConnect(server.URL, "config", "tracing", "set", "--endpoint", "https://user:secret@collector.example")...); err == nil || writes != 1 {
		t.Fatal("invalid endpoint must refuse before PUT")
	}
}
