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
)

func TestSessionIdleCLIUsesTheInstanceSetting(t *testing.T) {
	state := api.SessionIdleSettings{}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/system/session-idle" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("idle setting did not use the authenticated system endpoint")
			w.WriteHeader(400)
			return
		}
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
				t.Error(err)
				return
			}
			writes++
		}
		_ = json.NewEncoder(w).Encode(state)
	}))
	defer server.Close()
	for _, minutes := range []string{"6", "0"} {
		out, _, err := execRoot(t, withConnect(server.URL, "config", "session-idle", "set", "--minutes", minutes, "-o", "json")...)
		var got api.SessionIdleSettings
		if err != nil || json.Unmarshal([]byte(out), &got) != nil || got != state {
			t.Fatalf("set: %v %s", err, out)
		}
	}
	if writes != 2 || state.IdleStopMinutes != 0 {
		t.Fatal("enable or disable did not reach the instance setting")
	}
	if _, _, err := execRoot(t, withConnect(server.URL, "config", "session-idle", "set", "--minutes", "5")...); err == nil || writes != 2 {
		t.Fatal("invalid idle window reached PUT")
	}
}
