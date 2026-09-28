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

func TestProviderAccountMetadataLabelKeepsStableIdentityVisible(t *testing.T) {
	payload := strings.TrimSuffix(providerAccountJSON, "}") + `,"display_name":"Research account"}`
	for _, verb := range []string{"get", "ls"} {
		t.Run(verb, func(t *testing.T) {
			body := payload
			args := []string{"provider", "account", verb}
			if verb == "get" {
				args = append(args, "ppf_fixture")
			} else {
				body = `{"items":[` + payload + `]}`
			}
			p := newAccountProbeServer(t, http.StatusOK, body)
			args = append(args, "--server", p.URL, "--token", "t", "--tenant", "tenant-a")
			out, stderr, err := execRootStdin(t, "", args...)
			if err != nil {
				t.Fatalf("%s: %v %s", verb, err, stderr)
			}
			for _, want := range []string{"Research account", "claude-b", "ppf_fixture"} {
				if !strings.Contains(out, want) {
					t.Fatalf("%s hides %q: %s", verb, want, out)
				}
			}
		})
	}
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
		{"edit sends exact label", []string{"edit", "ppf_fixture", "--display-name", "  Research account  "}, http.StatusOK, providerAccountJSON,
			exitcode.OK, 1, http.MethodPatch, base + "/ppf_fixture", "", map[string]any{"display_name": "  Research account  "}},
		{"edit explicitly clears", []string{"edit", "ppf_fixture", "--display-name", ""}, http.StatusOK, providerAccountJSON,
			exitcode.OK, 1, http.MethodPatch, base + "/ppf_fixture", "", map[string]any{"display_name": ""}},
		{"edit sends only accent", []string{"edit", "ppf_fixture", "--accent", "blue"}, http.StatusOK, providerAccountJSON,
			exitcode.OK, 1, http.MethodPatch, base + "/ppf_fixture", "", map[string]any{"accent": "blue"}},
		{"edit clears both fields", []string{"edit", "ppf_fixture", "--accent", "", "--display-name", ""}, http.StatusOK, providerAccountJSON,
			exitcode.OK, 1, http.MethodPatch, base + "/ppf_fixture", "", map[string]any{"accent": "", "display_name": ""}},
		{"edit requires explicit flag before HTTP", []string{"edit", "ppf_fixture"}, http.StatusOK, providerAccountJSON,
			exitcode.Usage, 0, "", "", "", nil},
		{"edit retired is 5", []string{"edit", "ppf_fixture", "--display-name", "Research"}, http.StatusConflict, refused("profile is retired"),
			exitcode.Conflict, 1, http.MethodPatch, base + "/ppf_fixture", "", map[string]any{"display_name": "Research"}},
		{"edit forbidden is 3", []string{"edit", "ppf_fixture", "--display-name", "Research"}, http.StatusForbidden, refused("forbidden"),
			exitcode.Auth, 1, http.MethodPatch, base + "/ppf_fixture", "", map[string]any{"display_name": "Research"}},
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

// managedAccountJSON is what the server answers when it has BUILT a home: the
// mode says the product made it, and the home is named relative to the accounts
// root — the absolute path is in the ledger and never in an answer.
const managedAccountJSON = `{"account_ref":"ppf_fixture","name":"claude-b","driver":"claude",` +
	`"environment_ref":"env-a","state":"active","home_mode":"managed","home_generation":0,` +
	`"home_relative":"tnt-a/env-a/ppf_fixture","isolation_level":"shared","auth_source":"",` +
	`"identity":"","identity_source":"none"}`

// `add` is the verb that CREATES an account, home and all. It is a thin client
// like the rest, so what is pinned is the request it sends and the exit a script
// branches on: 0 on success — the server answers 201, because a resource was
// created — 3 when the caller may not, 5 when the name is taken or the generated
// ones ran out, and 1 for a name the server refuses the shape of.
func TestProviderAccountCommands_AddExitCodes(t *testing.T) {
	const base = "/v1/m/sessions/provider-accounts"
	refused := func(msg string) string { return `{"error":{"message":"` + msg + `"}}` }
	cases := []struct {
		name     string
		args     []string
		status   int
		payload  string
		wantCode int
		body     map[string]any
	}{
		{
			"add builds a home for a driver", []string{"add", "--driver", "claude"},
			http.StatusCreated, managedAccountJSON, exitcode.OK,
			map[string]any{"driver": "claude"},
		},
		{
			"add takes the name the operator chose", []string{"add", "--driver", "claude", "--name", "claude-b"},
			http.StatusCreated, managedAccountJSON, exitcode.OK,
			map[string]any{"driver": "claude", "name": "claude-b"},
		},
		{
			"add by a caller without write is 3", []string{"add", "--driver", "claude"},
			http.StatusForbidden, refused("forbidden"), exitcode.Auth,
			map[string]any{"driver": "claude"},
		},
		{
			"add of a taken name is 5", []string{"add", "--driver", "claude", "--name", "claude-b"},
			http.StatusConflict, refused(`account name \"claude-b\" is already taken in this environment`),
			exitcode.Conflict, map[string]any{"driver": "claude", "name": "claude-b"},
		},
		{
			"add whose generated names ran out is 5", []string{"add", "--driver", "claude"},
			http.StatusConflict, refused("generated account names for driver \\\"claude\\\" were exhausted"),
			exitcode.Conflict, map[string]any{"driver": "claude"},
		},
		{
			"add of a badly shaped name is 1", []string{"add", "--driver", "claude", "--name", "Claude_B"},
			http.StatusUnprocessableEntity, refused("an account name must be lowercase ASCII"),
			exitcode.Err, map[string]any{"driver": "claude", "name": "Claude_B"},
		},
		{
			// A node whose composition root never named an accounts root cannot
			// build a home, and says so rather than inventing a directory. The exit
			// is the tree's own mapping of a 5xx (cmd_agent.go:873-886, design §3.9
			// "≥500 → 6"), which is what every other verb of this binary answers.
			"add on a node with no accounts root is 6", []string{"add", "--driver", "claude"},
			http.StatusServiceUnavailable,
			refused("this node has no accounts root; provider account homes cannot be created here"),
			exitcode.Server, map[string]any{"driver": "claude"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newAccountProbeServer(t, tc.status, tc.payload)
			args := append([]string{"provider", "account"}, tc.args...)
			args = append(args, "--server", p.URL, "--token", "t", "--tenant", "tenant-a", "--idempotency-key", "cli-retry-key")
			out, errb, err := execRootStdin(t, "", args...)
			code := exitcode.OK
			if err != nil {
				code = exitcode.From(err)
			}
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v stderr=%s)", code, tc.wantCode, err, errb)
			}
			calls := p.recorded()
			if len(calls) != 1 {
				t.Fatalf("server calls = %d (%+v), want 1", len(calls), calls)
			}
			got := calls[0]
			if got.method != http.MethodPost || got.path != base || got.query != "" {
				t.Fatalf("request = %s %s?%s, want POST %s with no query", got.method, got.path, got.query, base)
			}
			var sent map[string]any
			if uerr := json.Unmarshal([]byte(got.body), &sent); uerr != nil {
				t.Fatalf("request body %q is not a JSON object: %v", got.body, uerr)
			}
			if sent["idempotency_key"] != "cli-retry-key" {
				t.Fatalf("retry key not retained: %v", sent)
			}
			delete(sent, "idempotency_key")
			if !strings.Contains(errb, "cli-retry-key") {
				t.Fatal("retry key not reported before request")
			}
			if !reflect.DeepEqual(sent, tc.body) {
				t.Fatalf("request body = %v, want %v", sent, tc.body)
			}
			if code == exitcode.OK {
				if !strings.Contains(out, "claude-b") {
					t.Fatalf("a created account was not shown: %q", out)
				}
				// The operator is told which directory the product made, and it is
				// named relative to the accounts root: this binary never prints a
				// home path the server did not put in the answer.
				if !strings.Contains(out, "tnt-a/env-a/ppf_fixture") {
					t.Fatalf("a created account did not show its home: %q", out)
				}
			}
		})
	}

	// A driver is not optional: without one there is nothing to build a home for,
	// and the refusal happens here rather than as a round trip. (`main` reports
	// this as a usage error, exit 2, by re-asking cobra's own validators at
	// main.go:93-97; this harness calls Execute directly and sees the raw error.)
	t.Run("add without a driver never reaches the server", func(t *testing.T) {
		p := newAccountProbeServer(t, http.StatusCreated, managedAccountJSON)
		_, _, err := execRootStdin(t, "", "provider", "account", "add",
			"--server", p.URL, "--token", "t", "--tenant", "tenant-a")
		if err == nil {
			t.Fatal("add with no --driver succeeded, want a refusal")
		}
		if calls := p.recorded(); len(calls) != 0 {
			t.Fatalf("add with no --driver called the server %d times: %+v", len(calls), calls)
		}
	})
}
