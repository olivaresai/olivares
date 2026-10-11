// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestModulesCLIListsAndChangesTheCompleteSelection(t *testing.T) {
	selected := []string{"finops"}
	var writes [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/console/modules" || r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("X-Olivares-Tenant") != "tenant-a" {
			t.Errorf("request did not use the existing authenticated modules endpoint")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		restarting := false
		if r.Method == http.MethodPut {
			var in struct {
				Selected []string `json:"selected"`
			}
			if r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&in) != nil || in.Selected == nil {
				t.Error("PUT must send a JSON selected list, including [] when empty")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			selected = in.Selected
			writes = append(writes, append([]string{}, selected...))
			restarting = true
		} else if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		on := func(name string) bool {
			for _, n := range selected {
				if n == name {
					return true
				}
			}
			return false
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"modules": []any{
			map[string]any{"name": "adoption", "selected": on("adoption"), "running": on("adoption")},
			map[string]any{"name": "finops", "selected": on("finops"), "running": on("finops")},
			map[string]any{"name": "sessions", "selected": false, "running": true, "always_on": true},
		}, "restarting": restarting})
	}))
	defer server.Close()
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"ls"}, "adoption\toff\toff"},
		{[]string{"on", "adoption"}, "restarting"},
		{[]string{"ls"}, "adoption\ton\ton"},
		{[]string{"off", "finops"}, "restarting"},
		{[]string{"off", "adoption"}, "restarting"},
	} {
		out, _, err := execRoot(t, withConnect(server.URL, append([]string{"modules"}, step.args...)...)...)
		if err != nil || !strings.Contains(out, step.want) {
			t.Fatalf("modules %v: err=%v output=%q, want %q", step.args, err, out, step.want)
		}
	}
	if want := [][]string{{"adoption", "finops"}, {"adoption"}, {}}; !reflect.DeepEqual(writes, want) {
		t.Fatalf("complete selections = %v, want %v", writes, want)
	}
	if _, _, err := execRoot(t, withConnect(server.URL, "modules", "on", "unknown")...); exitcode.From(err) != exitcode.Usage || len(writes) != 3 {
		t.Fatalf("unknown module must refuse without PUT: %v, writes=%v", err, writes)
	}
	out, _, err := execRoot(t, withConnect(server.URL, "modules", "ls", "-o", "json")...)
	var state struct {
		Modules []struct {
			Name     string `json:"name"`
			Selected bool   `json:"selected"`
			Running  bool   `json:"running"`
		} `json:"modules"`
	}
	if err != nil || json.Unmarshal([]byte(out), &state) != nil || len(state.Modules) != 3 || !state.Modules[2].Running || state.Modules[2].Selected {
		t.Fatalf("JSON must preserve selected versus always-running state: %v %s", err, out)
	}
}

// The restart that applies a module change stops every running session: the help
// says so, and the reply names how many stopped and what to do next (#507).
func TestModulesCLISaysTheRestartStopsRunningSessions(t *testing.T) {
	for _, tc := range []struct {
		sessions int
		want     string
	}{
		{2, " 2 running sessions stop; resume each one to continue."},
		{1, " 1 running session stops; resume it to continue."},
		{0, "olivares modules ls.\n"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"modules": []any{
				map[string]any{"name": "adoption", "selected": r.Method == http.MethodPut, "running": false},
			}, "restarting": r.Method == http.MethodPut, "running_sessions": tc.sessions})
		}))
		out, _, err := execRoot(t, withConnect(server.URL, "modules", "on", "adoption")...)
		server.Close()
		if err != nil || !strings.HasSuffix(out, tc.want) && !strings.HasSuffix(out, tc.want+"\n") {
			t.Fatalf("modules on with %d sessions: err=%v output=%q, want it to end %q", tc.sessions, err, out, tc.want)
		}
	}
	help, _, err := execRoot(t, "modules", "on", "--help")
	if want := "stops the running sessions"; err != nil || !strings.Contains(help, want) {
		t.Fatalf("modules on --help: err=%v output=%q, want %q", err, help, want)
	}
}

func TestModulesCLIPropagatesRefusalsWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		status, code int
		message      string
	}{
		{501, exitcode.Server, "module selection is not available on this engine"},
		{503, exitcode.Server, "the engine cannot restart itself to apply the module selection"},
		{403, exitcode.Auth, "auth: step-up required: this action requires a verified hardware authenticator (AAL3)"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			for _, refusedMethod := range []string{http.MethodGet, http.MethodPut} {
				calls, writes := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Method == http.MethodPut {
						writes++
					}
					if r.Method == refusedMethod {
						w.WriteHeader(tc.status)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "step_up_required", "message": tc.message + " secret-token"}})
						return
					}
					_, _ = io.WriteString(w, `{"modules":[{"name":"adoption","selected":false,"running":false}]}`)
				}))
				_, _, err := execRoot(t, withConnect(server.URL, "modules", "on", "adoption")...)
				server.Close()
				wantCalls, wantWrites := 1, 0
				if refusedMethod == http.MethodPut {
					wantCalls, wantWrites = 2, 1
				}
				if err == nil || exitcode.From(err) != tc.code || !strings.Contains(err.Error(), tc.message) || strings.Contains(err.Error(), "secret-token") || calls != wantCalls || writes != wantWrites {
					t.Fatalf("%s refusal: code=%d err=%v calls=%d writes=%d", refusedMethod, exitcode.From(err), err, calls, writes)
				}
			}
		})
	}
}
