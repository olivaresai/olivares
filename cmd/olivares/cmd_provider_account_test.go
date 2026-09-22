// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Provider-account CLI acceptance. The verbs are thin HTTP clients, so what is
// pinned here is the request each one sends and the exit code a script branches
// on: 0 on success, 3 when the caller may not, 4 when nothing has that
// reference, 5 when the name is taken or the profile is already an account, and 1
// for a name of the wrong shape (the server decides the shape; the CLI does not
// keep a second copy of the rule).

const providerAccountJSON = `{"account_ref":"ppf_fixture","name":"claude-b","driver":"claude",` +
	`"environment_ref":"env-a","state":"active","home_mode":"adopted","home_generation":0,` +
	`"home_relative":"","isolation_level":"shared","auth_source":"","identity":"","identity_source":"none"}`

type accountProbeCall struct {
	method, path, query, body string
}

type accountProbeServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []accountProbeCall
}

func newAccountProbeServer(t *testing.T, status int, payload string) *accountProbeServer {
	t.Helper()
	p := &accountProbeServer{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.calls = append(p.calls, accountProbeCall{
			method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, body: string(raw),
		})
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *accountProbeServer) recorded() []accountProbeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]accountProbeCall(nil), p.calls...)
}

func TestProviderAccountCommands_LsGetAdoptExitCodes(t *testing.T) {
	const base = "/v1/m/sessions/provider-accounts"
	refused := func(msg string) string { return `{"error":{"message":"` + msg + `"}}` }
	cases := []struct {
		name      string
		args      []string
		status    int
		payload   string
		wantCode  int
		wantCalls int
		method    string
		path      string
		query     string
		// body is the JSON object the verb must send; nil means no body is checked.
		body map[string]any
	}{
		{"ls answers", []string{"ls"}, http.StatusOK, `{"items":[` + providerAccountJSON + `]}`,
			exitcode.OK, 1, http.MethodGet, base, "", nil},
		{"ls narrows by environment, driver and state",
			[]string{"ls", "--environment", "env-a", "--driver", "codex", "--state", "active"},
			http.StatusOK, `{"items":[]}`, exitcode.OK, 1, http.MethodGet, base,
			"driver=codex&environment=env-a&state=active", nil},
		{"ls forbidden is 3", []string{"ls"}, http.StatusForbidden, refused("forbidden"),
			exitcode.Auth, 1, http.MethodGet, base, "", nil},
		{"get answers", []string{"get", "ppf_fixture"}, http.StatusOK, providerAccountJSON,
			exitcode.OK, 1, http.MethodGet, base + "/ppf_fixture", "", nil},
		{"get unknown is 4", []string{"get", "ppf_missing"}, http.StatusNotFound, refused("provider account not found"),
			exitcode.NotFound, 1, http.MethodGet, base + "/ppf_missing", "", nil},
		{"get forbidden is 3", []string{"get", "ppf_fixture"}, http.StatusForbidden, refused("forbidden"),
			exitcode.Auth, 1, http.MethodGet, base + "/ppf_fixture", "", nil},
		{"get of a blank reference never reaches the server", []string{"get", ""}, http.StatusOK, providerAccountJSON,
			exitcode.Usage, 0, "", "", "", nil},
		{"adopt with a name", []string{"adopt", "ppf_fixture", "--name", "claude-b"}, http.StatusOK, providerAccountJSON,
			exitcode.OK, 1, http.MethodPost, base + "/ppf_fixture/adopt", "", map[string]any{"name": "claude-b"}},
		{"adopt without a name asks the server to generate one", []string{"adopt", "ppf_fixture"}, http.StatusOK, providerAccountJSON,
			exitcode.OK, 1, http.MethodPost, base + "/ppf_fixture/adopt", "", map[string]any{}},
		{"adopt of an unknown profile is 4", []string{"adopt", "ppf_missing"}, http.StatusNotFound, refused("provider profile not found"),
			exitcode.NotFound, 1, http.MethodPost, base + "/ppf_missing/adopt", "", map[string]any{}},
		{"adopt of a taken name is 5", []string{"adopt", "ppf_fixture", "--name", "claude-b"}, http.StatusConflict,
			refused(`account name \"claude-b\" is already taken in this environment`),
			exitcode.Conflict, 1, http.MethodPost, base + "/ppf_fixture/adopt", "", map[string]any{"name": "claude-b"}},
		{"adopt by a caller without write is 3", []string{"adopt", "ppf_fixture"}, http.StatusForbidden, refused("forbidden"),
			exitcode.Auth, 1, http.MethodPost, base + "/ppf_fixture/adopt", "", map[string]any{}},
		{"adopt of a badly shaped name is 1", []string{"adopt", "ppf_fixture", "--name", "Claude_B"}, http.StatusUnprocessableEntity,
			refused("an account name must be lowercase ASCII"),
			exitcode.Err, 1, http.MethodPost, base + "/ppf_fixture/adopt", "", map[string]any{"name": "Claude_B"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newAccountProbeServer(t, tc.status, tc.payload)
			args := append([]string{"provider", "account"}, tc.args...)
			args = append(args, "--server", p.URL, "--token", "t", "--tenant", "tenant-a")
			out, errb, err := execRootStdin(t, "", args...)
			code := exitcode.OK
			if err != nil {
				code = exitcode.From(err)
			}
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v stderr=%s)", code, tc.wantCode, err, errb)
			}
			calls := p.recorded()
			if len(calls) != tc.wantCalls {
				t.Fatalf("server calls = %d (%+v), want %d", len(calls), calls, tc.wantCalls)
			}
			if tc.wantCalls == 0 {
				return
			}
			got := calls[0]
			if got.method != tc.method || got.path != tc.path || got.query != tc.query {
				t.Fatalf("request = %s %s?%s, want %s %s?%s", got.method, got.path, got.query, tc.method, tc.path, tc.query)
			}
			if tc.body != nil {
				var sent map[string]any
				if uerr := json.Unmarshal([]byte(got.body), &sent); uerr != nil {
					t.Fatalf("request body %q is not a JSON object: %v", got.body, uerr)
				}
				if !reflect.DeepEqual(sent, tc.body) {
					t.Fatalf("request body = %v, want %v", sent, tc.body)
				}
			}
			if code == exitcode.OK && tc.status == http.StatusOK && strings.Contains(tc.payload, "claude-b") &&
				!strings.Contains(out, "claude-b") {
				t.Fatalf("a successful verb did not show the account name: %q", out)
			}
		})
	}
}
