// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The first-run families (C08-01) are tested in BOTH directions, because only one
// of them is usually written down and it is the wrong one to trust alone:
//
//   - DENY  — with no credential, or with a plane that refuses, the command fails,
//     carries the right exit code, and does not leak (it does not even
//     open a connection when it can refuse from the arguments alone).
//   - PERMIT — with a credential and a plane that accepts, the command actually
//     WORKS: the right method, the right path, the right body, the right
//     headers, and output a script can consume.
//
// Every deny case therefore carries a request COUNTER, and every permit case
// asserts the wire, not just the exit status.

func prepareBootstrapCLITest(t *testing.T) {
	t.Helper()
	// A stray config file or environment variable would supply the very credential
	// the deny half is trying to withhold, and the test would pass by measuring
	// nothing. Neutralize all three sources.
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
}

// execRootStdin is execRoot with something on stdin — the form every secret in
// these families is meant to arrive by (`--password-file -`).
func execRootStdin(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

// countingServer records how many HTTP requests reached it, so a "the command
// refused" claim can be separated from "the command asked the server first".
type countingServer struct {
	*httptest.Server
	hits atomic.Int64
}

func newCountingServer(t *testing.T, h http.HandlerFunc) *countingServer {
	t.Helper()
	cs := &countingServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(cs.Close)
	return cs
}

// --- PERMIT ------------------------------------------------------------------

func TestTokensIssueBoundTokenReachesTheEngineAndShowsTheSecretOnce(t *testing.T) {
	prepareBootstrapCLITest(t)
	var body map[string]any
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != tokensPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, tokensPath)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer olvk_caller" {
			t.Errorf("Authorization = %q, want the caller's bearer", got)
		}
		if got := r.Header.Get("X-Olivares-Tenant"); got != "tenant-a" {
			t.Errorf("X-Olivares-Tenant = %q, want tenant-a", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "olvk_brandnewsecret", "id": "tok-1", "name": "ci",
		})
	})

	out, stderr, err := execRoot(t, "tokens", "issue", "--server", srv.URL,
		"--token", "olvk_caller", "--tenant", "tenant-a", "--name", "ci", "--role", "admin")
	if err != nil {
		t.Fatalf("tokens issue: %v (stderr %q)", err, stderr)
	}
	// The tenant travels in the BODY as well as the header: the engine reads the
	// bound tenant from the body (handlers_core.go:349).
	if body["tenant"] != "tenant-a" || body["role"] != "admin" || body["name"] != "ci" {
		t.Fatalf("issue body = %#v, want name/tenant/role carried", body)
	}
	if _, ok := body["superadmin"]; ok {
		t.Fatalf("issue body carries superadmin without --superadmin: %#v", body)
	}
	if !strings.Contains(out, "olvk_brandnewsecret") {
		t.Fatalf("the show-once secret is not on stdout:\n%s", out)
	}
	// The warning must NOT be on stdout: a script captures stdout into a variable.
	if strings.Contains(out, "shown ONCE") {
		t.Fatalf("the show-once warning belongs on stderr, not in the captured value:\n%s", out)
	}
	if !strings.Contains(stderr, "shown ONCE") {
		t.Fatalf("stderr does not warn that the secret is unrecoverable:\n%s", stderr)
	}
}

func TestTokensListRendersATableAndPreservesRawJSON(t *testing.T) {
	prepareBootstrapCLITest(t)
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != tokensPath {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("include_revoked") != "true" {
			t.Errorf("query = %q, want include_revoked=true", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"id": "tok-1", "name": "ci", "bound_tenant_id": "tenant-a", "role": "admin",
				"revoked": false, "created_at": "2026-08-16T10:00:00Z",
			}},
			"has_more": true, "cursor": "next-page", "request_id": "req-kept",
		})
	})

	out, _, err := execRoot(t, "tokens", "ls", "--server", srv.URL, "--token", "olvk_caller", "--include-revoked")
	if err != nil {
		t.Fatalf("tokens ls: %v", err)
	}
	for _, want := range []string{"ID", "NAME", "TENANT", "ROLE", "tok-1", "ci", "tenant-a", "admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	// A truncated listing that looks complete is how an operator concludes a token
	// does not exist. It must say so, and name the cursor.
	if !strings.Contains(out, "--cursor next-page") {
		t.Errorf("a truncated listing did not name the continuation cursor:\n%s", out)
	}

	jsonOut, _, err := execRoot(t, "tokens", "ls", "--server", srv.URL, "--token", "olvk_caller",
		"--include-revoked", "-o", "json")
	if err != nil {
		t.Fatalf("tokens ls -o json: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &raw); err != nil {
		t.Fatalf("JSON output is not JSON: %v\n%s", err, jsonOut)
	}
	if raw["request_id"] != "req-kept" {
		t.Fatalf("raw API fields were dropped from -o json: %#v", raw)
	}
}

func TestUsersCreateReadsThePasswordFromStdinAndNamesTheNextStep(t *testing.T) {
	prepareBootstrapCLITest(t)
	var body map[string]any
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != usersPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "usr-1", "email": "ops@example.com", "status": "active",
			"is_superadmin": false, "created_at": "2026-08-16T10:00:00Z",
		})
	})

	out, _, err := execRootStdin(t, "correct-horse-battery-staple\n",
		"users", "create", "--server", srv.URL, "--token", "olvk_caller",
		"--email", "ops@example.com", "--display-name", "Ops", "--password-file", "-")
	if err != nil {
		t.Fatalf("users create: %v", err)
	}
	if body["password"] != "correct-horse-battery-staple" {
		t.Fatalf("password was not read from stdin: %#v", body["password"])
	}
	if body["email"] != "ops@example.com" || body["display_name"] != "Ops" {
		t.Fatalf("create body = %#v", body)
	}
	if !strings.Contains(out, "usr-1") || !strings.Contains(out, "members grant") {
		t.Fatalf("output does not name the account or the next step:\n%s", out)
	}
	// The password must never be echoed back at the operator.
	if strings.Contains(out, "correct-horse-battery-staple") {
		t.Fatalf("stdout echoed the password:\n%s", out)
	}
}

func TestTenantsCreateAndListReachTheSystemOrgRoutes(t *testing.T) {
	prepareBootstrapCLITest(t)
	var created map[string]any
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == orgsPath:
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "org-1", "tenant_id": "t_acme", "name": "Acme GmbH", "slug": "acme",
				"status": "active", "created_at": "2026-08-16T10:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == orgsPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"id": "org-1", "tenant_id": "t_acme", "name": "Acme GmbH", "slug": "acme",
				"status": "active", "created_at": "2026-08-16T10:00:00Z",
			}}})
		default:
			http.NotFound(w, r)
		}
	})

	out, _, err := execRoot(t, "tenants", "create", "--server", srv.URL, "--token", "olvk_su",
		"--name", "Acme GmbH", "--slug", "acme")
	if err != nil {
		t.Fatalf("tenants create: %v", err)
	}
	if created["name"] != "Acme GmbH" || created["slug"] != "acme" {
		t.Fatalf("create body = %#v", created)
	}
	if !strings.Contains(out, "t_acme") {
		t.Fatalf("the new tenant id is not in the output — a script cannot continue:\n%s", out)
	}

	list, _, err := execRoot(t, "tenants", "ls", "--server", srv.URL, "--token", "olvk_su")
	if err != nil {
		t.Fatalf("tenants ls: %v", err)
	}
	if !strings.Contains(list, "t_acme") || !strings.Contains(list, "TENANT ID") {
		t.Fatalf("tenants ls did not render the roster:\n%s", list)
	}
}

func TestMembersGrantCarriesTheTenantInBodyAndHeader(t *testing.T) {
	prepareBootstrapCLITest(t)
	var body map[string]any
	var header string
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != membershipsPath {
			http.NotFound(w, r)
			return
		}
		header = r.Header.Get("X-Olivares-Tenant")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "mem-1", "user_id": "usr-1", "tenant": "t_acme", "role": "editor",
		})
	})

	out, _, err := execRoot(t, "members", "grant", "--server", srv.URL, "--token", "olvk_su",
		"--tenant", "t_acme", "--user", "usr-1", "--role", "editor")
	if err != nil {
		t.Fatalf("members grant: %v", err)
	}
	if body["tenant"] != "t_acme" || body["user_id"] != "usr-1" || body["role"] != "editor" {
		t.Fatalf("grant body = %#v", body)
	}
	if header != "t_acme" {
		t.Fatalf("X-Olivares-Tenant = %q, want t_acme", header)
	}
	if !strings.Contains(out, "editor") || !strings.Contains(out, "t_acme") {
		t.Fatalf("grant output does not state what was granted:\n%s", out)
	}
}

func TestAuthBootstrapRedeemsTheSetupTokenWithNoBearerAtAll(t *testing.T) {
	prepareBootstrapCLITest(t)
	var body map[string]any
	var sawAuthHeader bool
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != authSetupPath {
			http.NotFound(w, r)
			return
		}
		sawAuthHeader = r.Header.Get("Authorization") != ""
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "usr-root", "email": "admin@example.com", "is_superadmin": true,
			"organization": map[string]any{
				"id": "org-1", "tenant_id": "t_first", "name": "Acme GmbH",
				"slug": "acme", "status": "active", "created_at": "2026-08-16T10:00:00Z",
			},
		})
	})
	tokenFile := filepath.Join(t.TempDir(), "setup.token")
	if err := os.WriteFile(tokenFile, []byte("one-time-setup-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, err := execRootStdin(t, "first-admin-password\n",
		"auth", "bootstrap", "--server", srv.URL,
		"--setup-token-file", tokenFile, "--email", "admin@example.com",
		"--password-file", "-", "--organization", "Acme GmbH")
	if err != nil {
		t.Fatalf("auth bootstrap: %v", err)
	}
	// This is the ONE leg that must work with no credential: it runs before any
	// account exists. A bearer header here would mean the CLI required something
	// a fresh install cannot have.
	if sawAuthHeader {
		t.Fatal("auth bootstrap sent an Authorization header; the setup route has no bearer to send")
	}
	if body["token"] != "one-time-setup-token" {
		t.Fatalf("the setup token was not sent (or not trimmed): %#v", body["token"])
	}
	if body["password"] != "first-admin-password" || body["organization"] != "Acme GmbH" {
		t.Fatalf("setup body = %#v", body)
	}
	// The tenant id is what every later command needs; if it is not printed the
	// operator has nothing to pass to --tenant.
	if !strings.Contains(out, "t_first") {
		t.Fatalf("the first tenant id is not in the output:\n%s", out)
	}
	if strings.Contains(out, "one-time-setup-token") || strings.Contains(out, "first-admin-password") {
		t.Fatalf("stdout echoed a secret:\n%s", out)
	}
}

func TestAuthLoginWithPasswordSavesTheSessionItWasGiven(t *testing.T) {
	prepareBootstrapCLITest(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(cliConfigOverrideEnv, configPath)
	var loginBody map[string]any
	var whoamiBearer string
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case authLoginPath:
			_ = json.NewDecoder(r.Body).Decode(&loginBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "olvs_session", "session_id": "sess-1", "expires_at": "2026-08-17T10:00:00Z",
			})
		case "/v1/auth/whoami":
			whoamiBearer = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"kind": "user", "actor": "admin@example.com", "superadmin": true,
				"grants": []map[string]any{{"tenant": "t_first", "role": "owner"}},
			})
		default:
			http.NotFound(w, r)
		}
	})

	_, stderr, err := execRootStdin(t, "first-admin-password\n",
		"auth", "login", "--server", srv.URL, "--email", "admin@example.com", "--password-file", "-")
	if err != nil {
		t.Fatalf("auth login --email: %v (stderr %q)", err, stderr)
	}
	if loginBody["email"] != "admin@example.com" || loginBody["password"] != "first-admin-password" {
		t.Fatalf("login body = %#v", loginBody)
	}
	// The whoami validation must run against the SESSION the password produced,
	// not against nothing: a context saved without validation is the defect the
	// original `auth login` exists to avoid.
	if whoamiBearer != "Bearer olvs_session" {
		t.Fatalf("whoami Authorization = %q, want the freshly minted session", whoamiBearer)
	}
	saved, rerr := os.ReadFile(configPath)
	if rerr != nil {
		t.Fatalf("no client context was written: %v", rerr)
	}
	if !strings.Contains(string(saved), "olvs_session") {
		t.Fatalf("the saved context does not carry the session token:\n%s", saved)
	}
	if strings.Contains(string(saved), "first-admin-password") {
		t.Fatalf("the saved context carries the PASSWORD:\n%s", saved)
	}
}

// --- DENY --------------------------------------------------------------------

// TestFirstRunFamiliesRefuseWithoutACredentialBeforeOpeningAConnection is the deny
// half with its positive control attached: the SAME invocation, with a token,
// must reach the server. Without the control the assertion "0 requests" would be
// satisfied by a command that is simply broken.
func TestFirstRunFamiliesRefuseWithoutACredentialBeforeOpeningAConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"tokens", []string{"tokens", "ls"}},
		{"users", []string{"users", "ls"}},
		{"members", []string{"members", "ls", "--tenant", "t_acme"}},
		{"tenants", []string{"tenants", "ls"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareBootstrapCLITest(t)
			srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			})

			deny := append(append([]string{}, tc.args...), "--server", srv.URL)
			_, stderr, err := execRoot(t, deny...)
			if err == nil {
				t.Fatal("a command with no credential must fail")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit code = %d, want %d (usage: the invocation is missing a credential)", got, exitcode.Usage)
			}
			if n := srv.hits.Load(); n != 0 {
				t.Fatalf("the CLI contacted the plane %d time(s) before refusing; an unauthenticated "+
					"caller must not learn that this host answers", n)
			}
			if !strings.Contains(err.Error(), "token") {
				t.Fatalf("the refusal does not say what is missing: %v (stderr %q)", err, stderr)
			}

			// POSITIVE CONTROL: the same command, with a credential, reaches the plane.
			permit := append(append([]string{}, tc.args...), "--server", srv.URL, "--token", "olvk_caller")
			if _, _, perr := execRoot(t, permit...); perr != nil {
				t.Fatalf("with a credential the same command must work: %v", perr)
			}
			if n := srv.hits.Load(); n != 1 {
				t.Fatalf("server hits = %d, want exactly 1 (the permitted call)", n)
			}
		})
	}
}

func TestFirstRunFamiliesMapAServerRefusalToTheAuthExitCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		status int
		want   int
	}{
		{"tokens forbidden", []string{"tokens", "ls"}, http.StatusForbidden, exitcode.Auth},
		{"users unauthorized", []string{"users", "ls"}, http.StatusUnauthorized, exitcode.Auth},
		{"tenants forbidden", []string{"tenants", "ls"}, http.StatusForbidden, exitcode.Auth},
		{"members forbidden", []string{"members", "ls", "--tenant", "t_a"}, http.StatusForbidden, exitcode.Auth},
		{"tenants not found", []string{"tenants", "ls"}, http.StatusNotFound, exitcode.NotFound},
		{"tokens server error", []string{"tokens", "ls"}, http.StatusInternalServerError, exitcode.Server},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareBootstrapCLITest(t)
			srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"refused"}`)
			})
			args := append(append([]string{}, tc.args...), "--server", srv.URL, "--token", "olvk_caller")
			out, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatalf("HTTP %d must fail the command", tc.status)
			}
			if got := exitcode.From(err); got != tc.want {
				t.Fatalf("exit code = %d, want %d for HTTP %d", got, tc.want, tc.status)
			}
			if strings.TrimSpace(out) != "" {
				t.Fatalf("a refused command wrote to stdout: %q", out)
			}
		})
	}
}

// TestStepUpRefusalNamesTheCeremonyInsteadOfABare403 covers the ONE refusal on
// these routes an operator cannot act on from the generic message. Three
// first-run-adjacent routes are AAL3-gated and unreachable from any CLI
// credential; the reply must say so rather than read as "you lack a role".
func TestStepUpRefusalNamesTheCeremonyInsteadOfABare403(t *testing.T) {
	prepareBootstrapCLITest(t)
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"auth: step-up required: this action requires a verified hardware authenticator (AAL3)"}`)
	})
	_, _, err := execRoot(t, "tenants", "set-status", "t_acme", "--status", "suspended", "--yes",
		"--server", srv.URL, "--token", "olvk_su")
	if err == nil {
		t.Fatal("a step-up refusal must fail the command")
	}
	if got := exitcode.From(err); got != exitcode.Auth {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Auth)
	}
	for _, want := range []string{"AAL3", "console"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the step-up refusal does not mention %q: %v", want, err)
		}
	}
}

// TestDestructiveFirstRunVerbsRefuseUnattendedConsent proves the consent gate runs
// BEFORE the network call: a cron job that forgot --yes must not have revoked
// anything by the time it is told off.
func TestDestructiveFirstRunVerbsRefuseUnattendedConsent(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"tokens revoke", []string{"tokens", "revoke", "tok-1"}},
		{"tenants rm", []string{"tenants", "rm", "t_acme"}},
		{"invites revoke", []string{"members", "invites", "revoke", "inv-1", "--tenant", "t_a"}},
		{"tenants suspend", []string{"tenants", "set-status", "t_acme", "--status", "suspended"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareBootstrapCLITest(t)
			srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
				// set-status is the one non-DELETE destructive verb: it answers 200
				// with the updated org, so the stub has to speak both shapes or the
				// positive control below would "fail" for the wrong reason.
				if r.Method == http.MethodPut {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id": "org-1", "tenant_id": "t_acme", "name": "Acme", "slug": "acme",
						"status": "suspended", "created_at": "2026-08-16T10:00:00Z",
					})
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			args := append(append([]string{}, tc.args...), "--server", srv.URL, "--token", "olvk_su")
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatal("a destructive verb must refuse an unattended session without --yes")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit code = %d, want %d", got, exitcode.Usage)
			}
			if n := srv.hits.Load(); n != 0 {
				t.Fatalf("the destructive call reached the plane %d time(s) despite no consent", n)
			}

			// POSITIVE CONTROL: with --yes the very same call goes through.
			permit := append(append([]string{}, args...), "--yes")
			if _, _, perr := execRoot(t, permit...); perr != nil {
				t.Fatalf("with --yes the same command must work: %v", perr)
			}
			if n := srv.hits.Load(); n != 1 {
				t.Fatalf("server hits = %d, want exactly 1 (the consented call)", n)
			}
		})
	}
}

// TestTokensIssueRefusesAContradictoryScopeFromTheArgumentsAlone: --superadmin
// mints a CROSS-TENANT credential and the two shapes are exclusive in the engine.
// The refusal is decided locally, so a contradictory request never becomes a
// privileged one by accident of ordering.
func TestTokensIssueRefusesAContradictoryScopeFromTheArgumentsAlone(t *testing.T) {
	for _, args := range [][]string{
		{"tokens", "issue", "--name", "x", "--superadmin", "--tenant", "t_a"},
		{"tokens", "issue", "--name", "x", "--superadmin", "--role", "owner"},
		{"tokens", "issue", "--name", "x", "--tenant", "t_a", "--role", "wizard"},
		{"tokens", "issue", "--tenant", "t_a", "--role", "admin"},
	} {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			prepareBootstrapCLITest(t)
			srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"token":"olvk_should_never_be_minted","id":"x","name":"x"}`)
			})
			full := append(append([]string{}, args...), "--server", srv.URL, "--token", "olvk_caller")
			out, _, err := execRoot(t, full...)
			if err == nil {
				t.Fatalf("a contradictory or incomplete issue must fail; stdout was %q", out)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit code = %d, want %d", got, exitcode.Usage)
			}
			if n := srv.hits.Load(); n != 0 {
				t.Fatalf("a token was requested from the plane %d time(s) despite the contradiction", n)
			}
		})
	}
}

// TestFirstRunVerbsCannotBeRetargetedByAPositionalID: an id is caller-supplied
// and lands in the URL path. A value carrying separators must not be able to aim
// the request at a different route of the same plane.
func TestFirstRunVerbsCannotBeRetargetedByAPositionalID(t *testing.T) {
	prepareBootstrapCLITest(t)
	var seenPath string
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})
	if _, _, err := execRoot(t, "tokens", "revoke", "../../v1/system/orgs/t_victim", "--yes",
		"--server", srv.URL, "--token", "olvk_caller"); err != nil {
		t.Fatalf("tokens revoke: %v", err)
	}
	if !strings.HasPrefix(seenPath, tokensPath+"/") {
		t.Fatalf("the request escaped its route: path = %q", seenPath)
	}
	if strings.Contains(seenPath, "system/orgs") {
		t.Fatalf("a positional id re-aimed the request at another route: %q", seenPath)
	}
}

// TestUsersCreateRedactsThePasswordOutOfAServerError closes the leak the deny
// direction usually forgets: the refusal itself. The password traveled in the
// request body, so an error page that echoes it would print it in the operator's
// terminal and in any log that captures stderr.
func TestUsersCreateRedactsThePasswordOutOfAServerError(t *testing.T) {
	prepareBootstrapCLITest(t)
	const secret = "correct-horse-battery-staple"
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"rejected value `+secret+`"}`)
	})
	_, _, err := execRootStdin(t, secret+"\n", "users", "create", "--server", srv.URL,
		"--token", "olvk_su", "--email", "ops@example.com", "--password-file", "-")
	if err == nil {
		t.Fatal("a 400 must fail the command")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the password leaked through the error message: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("the error was not redacted at all: %v", err)
	}
}

// TestRejectedSecretsKeepTheirExitCodeAndStayRedacted is the pair that a MEASURED
// defect produced, not a hypothesis. The first run of the C-21 walkthrough against
// a real engine exited 1 on a rejected password: the house redaction helper
// rebuilt a plain error, so the classification httpErr had attached was thrown
// away and a script could not tell "wrong password" from "the CLI broke". That
// helper now returns a string (redactCLISecrets, cmd_auth.go) and cannot be
// returned as an error at all, but this pair stays: it is what proves the
// replacement keeps BOTH halves at once — the exit code survives AND the secret
// does not.
func TestRejectedSecretsKeepTheirExitCodeAndStayRedacted(t *testing.T) {
	t.Run("password rejected by login", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		const secret = "wrong-password-value"
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"auth: invalid credentials for `+secret+`"}`)
		})
		_, _, err := execRootStdin(t, secret+"\n", "auth", "login", "--server", srv.URL,
			"--email", "admin@example.com", "--password-file", "-")
		if err == nil {
			t.Fatal("a rejected password must fail the command")
		}
		if got := exitcode.From(err); got != exitcode.Auth {
			t.Fatalf("exit code = %d, want %d — a rejected credential is an AUTH failure, not a generic one", got, exitcode.Auth)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the password leaked through the refusal: %v", err)
		}
	})

	t.Run("setup token rejected by bootstrap", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		const secret = "wrong-setup-token"
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"forbidden: `+secret+`"}`)
		})
		tokenFile := filepath.Join(t.TempDir(), "bad.token")
		if err := os.WriteFile(tokenFile, []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := execRootStdin(t, "some-password\n", "auth", "bootstrap", "--server", srv.URL,
			"--setup-token-file", tokenFile, "--email", "a@example.com", "--password-file", "-")
		if err == nil {
			t.Fatal("a rejected setup token must fail the command")
		}
		if got := exitcode.From(err); got != exitcode.Auth {
			t.Fatalf("exit code = %d, want %d", got, exitcode.Auth)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the one-time setup token leaked through the refusal: %v", err)
		}
	})
}

// TestAuthLoginRefusesTwoCredentialsAtOnce: --token and --email are different
// credentials with different lifetimes. Silently preferring one would save a
// context the operator did not ask for.
func TestAuthLoginRefusesTwoCredentialsAtOnce(t *testing.T) {
	prepareBootstrapCLITest(t)
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvs_session"})
	})
	_, _, err := execRootStdin(t, "pw\n", "auth", "login", "--server", srv.URL,
		"--token", "olvk_existing", "--email", "admin@example.com", "--password-file", "-")
	if err == nil {
		t.Fatal("passing both --token and --email must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Usage)
	}
	if n := srv.hits.Load(); n != 0 {
		t.Fatalf("the CLI logged in %d time(s) despite the contradiction", n)
	}
}

// TestAuthBootstrapRefusesTwoSecretsOnOneStdin — one stream cannot carry two
// values without a framing convention this CLI does not have, and guessing would
// send the password as the setup token.
func TestAuthBootstrapRefusesTwoSecretsOnOneStdin(t *testing.T) {
	prepareBootstrapCLITest(t)
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"u","organization":{"tenant_id":"t"}}`)
	})
	_, _, err := execRootStdin(t, "whatever\n", "auth", "bootstrap", "--server", srv.URL,
		"--setup-token-file", "-", "--email", "a@example.com", "--password-file", "-")
	if err == nil {
		t.Fatal("two secrets on one stdin must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Usage)
	}
	if n := srv.hits.Load(); n != 0 {
		t.Fatalf("setup was attempted %d time(s) with an ambiguous secret", n)
	}
}
