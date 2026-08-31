// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The witnesses for the sol-max contrast of 2026-08-16
// (an internal design note (not shipped)). Each one is written in BOTH
// directions on purpose: the audit's own finding was that the existing tests proved
// the deny half of things nobody was attacking while the permit half — a valid
// operator getting in — was the half that was broken.
//
//	DENY   — the credential does not travel where it must not.
//	PERMIT — the operator who should get in still gets in, on the same code path.
//
// Every deny case that claims "it never asked the plane" carries a request COUNTER,
// because "the command failed" and "the command failed without leaking" are
// different claims and only one of them is the point.

// --- §3 ALTA · a credential does not travel in cleartext -----------------------

// TestCredentialsRefusePlainHTTPToARemoteHostButNotLoopbackOrTLS is the transport
// half, measured where the decision is made. Four permit cases guard it, because a
// refusal that also refuses the first-run walkthrough (http://127.0.0.1) or every
// public GET would be a worse defect than the one it fixes.
func TestCredentialsRefusePlainHTTPToARemoteHostButNotLoopbackOrTLS(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(tlsSrv.Close)

	cases := []struct {
		name    string
		opts    cliTransportOptions
		env     string
		wantErr bool
	}{
		{
			name: "DENY bearer over plain http to a remote host",
			opts: cliTransportOptions{Resolved: cliResolvedConfig{
				Server: "http://plane.example.com", Token: "olvk_caller"}},
			wantErr: true,
		},
		{
			name: "DENY a secret body over plain http even with no bearer",
			opts: cliTransportOptions{
				Resolved:      cliResolvedConfig{Server: "http://plane.example.com"},
				CarriesSecret: true,
			},
			wantErr: true,
		},
		{
			// The C-21 walkthrough and every `serve --insecure` install.
			name: "PERMIT bearer over plain http to loopback",
			opts: cliTransportOptions{Resolved: cliResolvedConfig{
				Server: "http://127.0.0.1:8443", Token: "olvk_caller"}},
		},
		{
			name: "PERMIT bearer over plain http to localhost by name",
			opts: cliTransportOptions{Resolved: cliResolvedConfig{
				Server: "http://localhost:8443", Token: "olvk_caller"}},
		},
		{
			// GET /status resolves with SkipCredentials, so it carries nothing and
			// must keep working against any plain-HTTP plane.
			name: "PERMIT no credential at all over plain http",
			opts: cliTransportOptions{Resolved: cliResolvedConfig{Server: "http://plane.example.com"}},
		},
		{
			name: "PERMIT bearer over https",
			opts: cliTransportOptions{Resolved: cliResolvedConfig{
				Server: tlsSrv.URL, Token: "olvk_caller"}},
		},
		{
			name: "PERMIT the explicit dangerous opt-in flag",
			opts: cliTransportOptions{
				Resolved:       cliResolvedConfig{Server: "http://plane.example.com", Token: "olvk_caller"},
				AllowCleartext: true,
			},
		},
		{
			name: "PERMIT the explicit dangerous opt-in environment variable",
			opts: cliTransportOptions{Resolved: cliResolvedConfig{
				Server: "http://plane.example.com", Token: "olvk_caller"}},
			env: "1",
		},
		{
			// An env var that is SET but not affirmative must not disable a
			// credential protection: "0" is how a script says no.
			name: "DENY a non-affirmative opt-in value",
			opts: cliTransportOptions{Resolved: cliResolvedConfig{
				Server: "http://plane.example.com", Token: "olvk_caller"}},
			env:     "0",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(cliCleartextOptInEnv, tc.env)
			_, _, err := cliTransport(tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatal("a credential was allowed onto plain HTTP to a remote host")
				}
				if got := exitcode.From(err); got != exitcode.Usage {
					t.Fatalf("exit code = %d, want %d (the invocation is unsafe, not the plane down)", got, exitcode.Usage)
				}
				for _, want := range []string{"plain HTTP", "--allow-cleartext", cliCleartextOptInEnv} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("the refusal does not name %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("a legitimate transport was refused: %v", err)
			}
		})
	}
}

// TestTokensLsRefusesAPlainHTTPRemotePlaneBeforeDialling wires the same rule
// through a real command, and proves the refusal is decided BEFORE any packet: the
// address is TEST-NET-2 (RFC 5737, never routed), so a client that got as far as
// dialing would burn its timeout and exit 6 instead.
func TestTokensLsRefusesAPlainHTTPRemotePlaneBeforeDialling(t *testing.T) {
	prepareBootstrapCLITest(t)
	_, _, err := execRoot(t, "tokens", "ls",
		"--server", "http://198.51.100.7:8080", "--token", "olvk_caller", "--timeout", "800ms")
	if err == nil {
		t.Fatal("a bearer to a remote plain-HTTP plane must be refused")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want %d; %v", got, exitcode.Usage, err)
	}
	if !strings.Contains(err.Error(), "plain HTTP") {
		t.Fatalf("the refusal does not say why: %v", err)
	}

	// PERMIT: the same command against the same kind of plane over loopback works.
	srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	})
	if _, _, perr := execRoot(t, "tokens", "ls", "--server", srv.URL, "--token", "olvk_caller"); perr != nil {
		t.Fatalf("the loopback first-run path must keep working: %v", perr)
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("server hits = %d, want 1", n)
	}
}

// --- §3 ALTA · a redirect does not carry the secret to a stranger --------------

// TestSecretsAreNotReplayedToAnotherOriginByARedirect is the audit's own
// experiment, inverted into a guard. The audit built a 307 to a second port, saw
// the password AND the bearer arrive there, and the test passed.
func TestSecretsAreNotReplayedToAnotherOriginByARedirect(t *testing.T) {
	prepareBootstrapCLITest(t)
	const password = "correct-horse-battery-staple"

	t.Run("DENY cross-origin 307 with a bearer and a password", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		var sawSecret bool
		attacker := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("Authorization") != "" || strings.Contains(string(body), password) {
				sawSecret = true
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"usr-1","email":"ops@example.com","status":"active","created_at":"x"}`)
		})
		plane := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, attacker.URL+usersPath, http.StatusTemporaryRedirect)
		})

		_, _, err := execRootStdin(t, password+"\n", "users", "create", "--server", plane.URL,
			"--token", "olvk_caller", "--email", "ops@example.com", "--password-file", "-")
		if err == nil {
			t.Fatal("the CLI followed a redirect that carried its secrets to another origin")
		}
		if n := attacker.hits.Load(); n != 0 {
			t.Fatalf("the redirect target was contacted %d time(s)", n)
		}
		if sawSecret {
			t.Fatal("the redirect target received the bearer or the password")
		}
		if !strings.Contains(err.Error(), "refusing to follow a redirect") {
			t.Fatalf("the refusal does not name what happened: %v", err)
		}
	})

	t.Run("PERMIT same-origin redirect keeps working, credential intact", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		var finalAuth string
		var finalBody map[string]any
		plane := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == usersPath {
				// A path-canonicalising redirect on the SAME origin: the ordinary,
				// harmless kind, which must not become collateral damage.
				http.Redirect(w, r, usersPath+"/", http.StatusTemporaryRedirect)
				return
			}
			finalAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&finalBody)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "usr-1", "email": "ops@example.com", "status": "active",
				"is_superadmin": false, "created_at": "2026-08-16T10:00:00Z",
			})
		})
		if _, _, err := execRootStdin(t, password+"\n", "users", "create", "--server", plane.URL,
			"--token", "olvk_caller", "--email", "ops@example.com", "--password-file", "-"); err != nil {
			t.Fatalf("a same-origin redirect must still be followed: %v", err)
		}
		if finalAuth != "Bearer olvk_caller" {
			t.Fatalf("the credential was dropped on a same-origin redirect: %q", finalAuth)
		}
		if finalBody["password"] != password {
			t.Fatalf("the body was not replayed on the 307: %#v", finalBody)
		}
	})

	t.Run("PERMIT a request carrying nothing still follows a cross-origin redirect", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		// `status` resolves with SkipCredentials: nothing to leak, so the ordinary
		// redirect behavior is kept rather than tightened for its own sake.
		final := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "components": []any{}, "timestamp": "2026-08-16T10:00:00Z",
			})
		})
		first := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, final.URL+"/status", http.StatusTemporaryRedirect)
		})
		_, _, err := execRoot(t, "status", "--server", first.URL)
		if err != nil && strings.Contains(err.Error(), "refusing to follow a redirect") {
			t.Fatalf("a credential-free request was refused a redirect: %v", err)
		}
		if n := final.hits.Load(); n != 1 {
			t.Fatalf("the redirect target was reached %d time(s), want 1", n)
		}
	})
}

// TestTheRedirectPolicyJudgesTheWholeOriginAndStillTerminates measures the policy
// where it decides, because two of its cases cannot be staged end to end.
//
// The DOWNGRADE case is the one the audit named and the standard library gets
// wrong: net/http drops Authorization only when the HOST changes, so an
// https://plane → http://plane redirect — same host, same port, no TLS — keeps the
// bearer and puts it on the wire in clear. Same origin means same SCHEME too.
//
// The CAP case is not a leak but a hang: installing a CheckRedirect REPLACES the
// standard client's ten-redirect limit, so a policy that returns nil for every
// same-origin hop — which is exactly what the permit half above requires — would
// follow a self-referential redirect forever.
func TestTheRedirectPolicyJudgesTheWholeOriginAndStillTerminates(t *testing.T) {
	mustURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}
	for _, tc := range []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{
			name: "DENY a scheme downgrade to the same host and port",
			from: "https://plane.example.com/v1/users", to: "http://plane.example.com/v1/users",
			wantErr: true,
		},
		{
			name: "DENY an implicit-port downgrade (https:443 to http:80)",
			from: "https://plane.example.com/v1/users", to: "http://plane.example.com:80/v1/users",
			wantErr: true,
		},
		{
			// THE CASE THAT ACTUALLY TESTS THE SCHEME. Both cases above are
			// refused by the PORT comparison alone — https defaults to 443 and
			// http to 80 — so deleting the scheme check from sameCLIOrigin left
			// them both green (mutant M3, 2026-08-16). A plane on an explicit
			// non-default port redirecting to plain HTTP on the SAME port is the
			// one shape where scheme is the only thing standing between the
			// bearer and the wire.
			name: "DENY a scheme downgrade on the same explicit port",
			from: "https://plane.example.com:8443/v1/users", to: "http://plane.example.com:8443/v1/users",
			wantErr: true,
		},
		{
			name: "DENY another port on the same host",
			from: "https://plane.example.com/v1/users", to: "https://plane.example.com:8443/v1/users",
			wantErr: true,
		},
		{
			name: "DENY a sibling host",
			from: "https://plane.example.com/v1/users", to: "https://evil.example.com/v1/users",
			wantErr: true,
		},
		{
			name: "PERMIT the same origin spelled with its default port",
			from: "https://plane.example.com/v1/users", to: "https://plane.example.com:443/v1/users/",
		},
		{
			name: "PERMIT the same origin in different case",
			from: "https://Plane.Example.com/v1/users", to: "https://plane.example.com/v1/users/",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			via := []*http.Request{{URL: mustURL(tc.from)}}
			err := cliRedirectPolicy(true)(&http.Request{URL: mustURL(tc.to)}, via)
			if tc.wantErr {
				if err == nil {
					t.Fatal("the secret was allowed to follow the redirect")
				}
				if !strings.Contains(err.Error(), "refusing to follow a redirect") {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("an ordinary same-origin redirect was refused: %v", err)
			}
		})
	}

	t.Run("DENY an endless same-origin redirect chain", func(t *testing.T) {
		policy := cliRedirectPolicy(true)
		u := mustURL("https://plane.example.com/v1/users")
		via := []*http.Request{{URL: u}}
		for len(via) < maxCLIRedirects {
			if err := policy(&http.Request{URL: u}, via); err != nil {
				t.Fatalf("hop %d was refused before the cap: %v", len(via), err)
			}
			via = append(via, &http.Request{URL: u})
		}
		err := policy(&http.Request{URL: u}, via)
		if err == nil {
			t.Fatalf("the chain was still being followed after %d hops", maxCLIRedirects)
		}
		if !strings.Contains(err.Error(), "stopped after") {
			t.Fatalf("the cap did not fire; refused for another reason: %v", err)
		}
	})

	t.Run("PERMIT the cap still bounds a request carrying nothing", func(t *testing.T) {
		policy := cliRedirectPolicy(false)
		u := mustURL("http://plane.example.com/status")
		via := make([]*http.Request, maxCLIRedirects)
		for i := range via {
			via[i] = &http.Request{URL: u}
		}
		if err := policy(&http.Request{URL: u}, via); err == nil {
			t.Fatal("a credential-free request may loop forever")
		}
	})
}

// --- §1 ALTA · the anonymous legs send no ambient credential -------------------

// TestAnonymousLegsSendNoAmbientBearer is the deny half: a token from the
// environment or a saved context must not ride along to /v1/setup or
// /v1/auth/login, whatever --server points at.
func TestAnonymousLegsSendNoAmbientBearer(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		args []string
	}{
		{"auth bootstrap", authSetupPath, nil},
		{"auth login", authLoginPath, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareBootstrapCLITest(t)
			t.Setenv("OLIVARES_TOKEN", "olvk_ambient_from_another_install")
			t.Setenv("OLIVARES_TENANT", "t_from_another_install")
			var sawAuth, sawTenant string
			srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.path {
					sawAuth = r.Header.Get("Authorization")
					sawTenant = r.Header.Get("X-Olivares-Tenant")
				}
				switch r.URL.Path {
				case authSetupPath:
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"id":"usr-root","email":"a@e.test","organization":{"tenant_id":"t_first"}}`)
				case authLoginPath:
					_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvs_session", "expires_at": "x"})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "user", "actor": "a@e.test"})
				}
			})

			var args []string
			if tc.path == authSetupPath {
				tokenFile := filepath.Join(t.TempDir(), "setup.token")
				if err := os.WriteFile(tokenFile, []byte("one-time"), 0o600); err != nil {
					t.Fatal(err)
				}
				args = []string{"auth", "bootstrap", "--server", srv.URL, "--setup-token-file", tokenFile,
					"--email", "a@e.test", "--password-file", "-"}
			} else {
				args = []string{"auth", "login", "--server", srv.URL, "--email", "a@e.test", "--password-file", "-"}
			}
			if _, stderr, err := execRootStdin(t, "pw\n", args...); err != nil {
				t.Fatalf("%s: %v (stderr %q)", tc.name, err, stderr)
			}
			if sawAuth != "" {
				t.Fatalf("%s sent an inherited bearer to a public route: %q", tc.name, sawAuth)
			}
			if sawTenant != "" {
				t.Fatalf("%s sent an inherited tenant header to a public route: %q", tc.name, sawTenant)
			}
		})
	}
}

// TestAStaleAmbientCredentialDoesNotBlockAValidPasswordLogin is the PERMIT half,
// and it is the one that mattered: the audit MEASURED, against a real engine, that
// an expired OLIVARES_TOKEN made `auth login --email --password-file` fail 401
// before the public handler ever ran. Somebody who could legitimately sign in
// could not, because of a credential they had not asked to use.
//
// The stub here is the engine's own order of operations: authenticate first, and
// reject a bad bearer before any handler sees the request.
func TestAStaleAmbientCredentialDoesNotBlockAValidPasswordLogin(t *testing.T) {
	prepareBootstrapCLITest(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(cliConfigOverrideEnv, configPath)
	t.Setenv("OLIVARES_TOKEN", "olvk_expired_yesterday")

	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if bearer := r.Header.Get("Authorization"); bearer == "Bearer olvk_expired_yesterday" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"auth: unauthenticated"}`)
			return
		}
		switch r.URL.Path {
		case authLoginPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "olvs_fresh_session", "session_id": "s1", "expires_at": "2026-08-17T10:00:00Z"})
		case "/v1/auth/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"kind": "user", "actor": "admin@e.test", "superadmin": true,
				"grants": []map[string]any{{"tenant": "t_first", "role": "owner"}}})
		default:
			http.NotFound(w, r)
		}
	})

	if _, stderr, err := execRootStdin(t, "valid-password\n", "auth", "login",
		"--server", srv.URL, "--email", "admin@e.test", "--password-file", "-"); err != nil {
		t.Fatalf("a stale environment credential blocked a valid password login: %v (stderr %q)", err, stderr)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("no context was saved: %v", err)
	}
	if !strings.Contains(string(saved), "olvs_fresh_session") {
		t.Fatalf("the recovered session was not saved:\n%s", saved)
	}

	// PERMIT, second spelling: `--token ""` is how an operator says "ignore the
	// environment", and the resolver documents that meaning. It used to be refused
	// as if it were a second credential.
	if _, _, err := execRootStdin(t, "valid-password\n", "auth", "login", "--server", srv.URL,
		"--token", "", "--email", "admin@e.test", "--password-file", "-"); err != nil {
		t.Fatalf(`--token "" with --email must be accepted: %v`, err)
	}

	// DENY is unchanged: a REAL --token next to --email is still two credentials.
	_, _, err = execRootStdin(t, "valid-password\n", "auth", "login", "--server", srv.URL,
		"--token", "olvk_other", "--email", "admin@e.test", "--password-file", "-")
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("--token <value> with --email must still be refused: %v", err)
	}
}

// TestPasswordLoginDoesNotInheritThePreviousIdentitysTenant covers the second
// PERMIT break of §1: after changing identity the context kept the ambient tenant,
// so the new session was saved against a tenant it may hold nothing in and every
// later command answered 403 with nothing on screen linking it to the login.
func TestPasswordLoginDoesNotInheritThePreviousIdentitysTenant(t *testing.T) {
	newServer := func(t *testing.T, superadmin bool, grants ...string) *countingServer {
		t.Helper()
		rows := make([]map[string]any, 0, len(grants))
		for _, g := range grants {
			rows = append(rows, map[string]any{"tenant": g, "role": "admin"})
		}
		return newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case authLoginPath:
				_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvs_session", "expires_at": "x"})
			case "/v1/auth/whoami":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"kind": "user", "actor": "new@e.test", "superadmin": superadmin, "grants": rows})
			default:
				http.NotFound(w, r)
			}
		})
	}

	t.Run("PERMIT the tenant is re-derived from the new identity", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		t.Setenv(cliConfigOverrideEnv, configPath)
		t.Setenv("OLIVARES_TENANT", "t_previous_operator")
		srv := newServer(t, false, "t_mine")
		if _, _, err := execRootStdin(t, "pw\n", "auth", "login", "--server", srv.URL,
			"--email", "new@e.test", "--password-file", "-"); err != nil {
			t.Fatalf("auth login: %v", err)
		}
		saved, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(saved), "t_previous_operator") {
			t.Fatalf("the new session was bound to the PREVIOUS identity's tenant:\n%s", saved)
		}
		if !strings.Contains(string(saved), "t_mine") {
			t.Fatalf("the tenant was not re-derived from whoami:\n%s", saved)
		}
	})

	t.Run("PERMIT an explicit --tenant this identity holds is kept", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		t.Setenv(cliConfigOverrideEnv, configPath)
		srv := newServer(t, false, "t_a", "t_b")
		if _, _, err := execRootStdin(t, "pw\n", "auth", "login", "--server", srv.URL,
			"--tenant", "t_b", "--email", "new@e.test", "--password-file", "-"); err != nil {
			t.Fatalf("an explicitly requested, held tenant must be accepted: %v", err)
		}
		saved, _ := os.ReadFile(configPath)
		if !strings.Contains(string(saved), "t_b") {
			t.Fatalf("the explicit tenant was not saved:\n%s", saved)
		}
	})

	t.Run("PERMIT a superadmin may name any tenant", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "config.yaml"))
		srv := newServer(t, true)
		if _, _, err := execRootStdin(t, "pw\n", "auth", "login", "--server", srv.URL,
			"--tenant", "t_anything", "--email", "root@e.test", "--password-file", "-"); err != nil {
			t.Fatalf("a superadmin is cross-tenant and must not be second-guessed: %v", err)
		}
	})

	t.Run("DENY an explicit --tenant this identity holds nothing in", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		t.Setenv(cliConfigOverrideEnv, configPath)
		srv := newServer(t, false, "t_a")
		_, _, err := execRootStdin(t, "pw\n", "auth", "login", "--server", srv.URL,
			"--tenant", "t_not_mine", "--email", "new@e.test", "--password-file", "-")
		if err == nil {
			t.Fatal("a context that cannot work must not be written silently")
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Fatalf("exit code = %d, want %d", got, exitcode.Usage)
		}
		if _, serr := os.Stat(configPath); serr == nil {
			t.Fatal("the unusable context was written anyway")
		}
	})
}

// --- §4 ALTA · rotate does not present as atomic what is not -------------------

// TestRotateFailureReportsWhetherTheOldSecretIsStillAlive. The endpoint issues and
// revokes in two transactions, so a failure in between leaves the old token live —
// a defect of the endpoint, elevated to its owner. What the CLI owes the operator
// is the truth about the state it left behind, and a non-zero exit.
func TestRotateFailureReportsWhetherTheOldSecretIsStillAlive(t *testing.T) {
	t.Run("DENY silence when the previous token survived", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/rotate") {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"audit append failed"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"id": "tok-1", "name": "ci", "revoked": false, "created_at": "x",
			}}})
		})
		_, stderr, err := execRoot(t, "tokens", "rotate", "tok-1", "--server", srv.URL, "--token", "olvk_su")
		if err == nil {
			t.Fatal("a failed rotation must fail the command")
		}
		if got := exitcode.From(err); got != exitcode.Server {
			t.Fatalf("exit code = %d, want %d", got, exitcode.Server)
		}
		if !strings.Contains(stderr, "STILL ACTIVE") {
			t.Fatalf("the operator is not told the old secret still authenticates:\n%s", stderr)
		}
	})

	t.Run("PERMIT a successful rotation says both halves happened, and probes nothing", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "olvk_replacement", "id": "tok-2", "name": "ci", "revoked_id": "tok-1"})
		})
		out, stderr, err := execRoot(t, "tokens", "rotate", "tok-1", "--server", srv.URL, "--token", "olvk_su")
		if err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if !strings.Contains(out, "olvk_replacement") {
			t.Fatalf("the show-once secret is not on stdout:\n%s", out)
		}
		if !strings.Contains(stderr, "tok-1") || !strings.Contains(stderr, "revoked") {
			t.Fatalf("the fate of the previous token is not stated:\n%s", stderr)
		}
		if n := srv.hits.Load(); n != 1 {
			t.Fatalf("server hits = %d, want exactly 1 — a successful rotate must not probe", n)
		}
	})

	t.Run("DENY probing when nothing was ever sent", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
		})
		_, _, err := execRoot(t, "tokens", "rotate", "tok-1", "--server", srv.URL)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("a rotate with no credential must exit %d: %v", exitcode.Usage, err)
		}
		if n := srv.hits.Load(); n != 0 {
			t.Fatalf("the plane was contacted %d time(s) by a command that refused locally", n)
		}
	})
}

// --- §5 MEDIA · the AAL3 verbs a legitimate caller was denied ------------------

// TestAAL3GatedVerbsExistAndCarryTheCallersCredential. The premise for withholding
// them — "no CLI credential can carry AAL3" — is false for a session elevated by a
// step-up ceremony, which `auth login --token` exists to carry. The gate itself is
// the engine's and is untouched: the deny case proves the refusal is passed through
// with the reason an operator can act on.
func TestAAL3GatedVerbsExistAndCarryTheCallersCredential(t *testing.T) {
	t.Run("PERMIT users disable reaches the engine with the caller's session", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		var method, path, auth string
		srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			method, path, auth = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "usr-2", "email": "ops@e.test", "status": "disabled",
				"is_superadmin": true, "created_at": "x"})
		})
		out, _, err := execRoot(t, "users", "disable", "usr-2", "--yes",
			"--server", srv.URL, "--token", "olvs_elevated_session")
		if err != nil {
			t.Fatalf("users disable: %v", err)
		}
		if method != http.MethodPost || path != usersPath+"/usr-2/disable" {
			t.Fatalf("request = %s %s", method, path)
		}
		if auth != "Bearer olvs_elevated_session" {
			t.Fatalf("Authorization = %q", auth)
		}
		if !strings.Contains(out, "disabled") {
			t.Fatalf("the new state is not reported:\n%s", out)
		}
	})

	t.Run("PERMIT tenants set-region carries the region", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		var body map[string]any
		srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "org-1", "tenant_id": "t_acme", "name": "Acme", "slug": "acme",
				"status": "active", "data_region": "eu", "created_at": "x"})
		})
		if _, _, err := execRoot(t, "tenants", "set-region", "t_acme", "--region", "eu", "--yes",
			"--server", srv.URL, "--token", "olvs_elevated_session"); err != nil {
			t.Fatalf("tenants set-region: %v", err)
		}
		if body["data_region"] != "eu" {
			t.Fatalf("region body = %#v", body)
		}
	})

	t.Run("DENY set-region without a decision, and without asking the plane", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "t_acme"})
		})
		for _, args := range [][]string{
			{"tenants", "set-region", "t_acme", "--yes"},
			{"tenants", "set-region", "t_acme", "--region", "eu", "--clear", "--yes"},
			{"tenants", "set-region", "t_acme", "--region", "eu"},
			{"users", "disable", "usr-2"},
		} {
			full := append(append([]string{}, args...), "--server", srv.URL, "--token", "olvs_x")
			_, _, err := execRoot(t, full...)
			if err == nil || exitcode.From(err) != exitcode.Usage {
				t.Fatalf("%v: want exit %d, got %v", args, exitcode.Usage, err)
			}
		}
		if n := srv.hits.Load(); n != 0 {
			t.Fatalf("the plane was contacted %d time(s) by refused invocations", n)
		}
	})

	t.Run("DENY a caller below AAL3, with the reason it can act on", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"auth: step-up required: this action requires a verified hardware authenticator (AAL3)"}`)
		})
		_, _, err := execRoot(t, "users", "disable", "usr-2", "--yes",
			"--server", srv.URL, "--token", "olvk_api_token")
		if err == nil || exitcode.From(err) != exitcode.Auth {
			t.Fatalf("a step-up refusal must exit %d: %v", exitcode.Auth, err)
		}
		for _, want := range []string{"AAL3", "console", "auth login --token-file"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal does not name %q: %v", want, err)
			}
		}
	})
}

// --- §3 MEDIA · the bearer leaves argv, and stops leaking through error bodies --

// TestTheBearerHasAFileFormAndNeverSurvivesAnErrorBody covers both remaining exits
// the audit found: argv (no file form existed) and the SERVER'S error body, which
// httpErr embeds verbatim.
func TestTheBearerHasAFileFormAndNeverSurvivesAnErrorBody(t *testing.T) {
	const bearer = "olvk_secret_bearer_value"

	t.Run("PERMIT --token-file authenticates exactly like --token", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		var seen string
		srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		})
		tokenFile := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte(bearer+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := execRoot(t, "tokens", "ls", "--server", srv.URL, "--token-file", tokenFile); err != nil {
			t.Fatalf("tokens ls --token-file: %v", err)
		}
		if seen != "Bearer "+bearer {
			t.Fatalf("Authorization = %q, want the token read from the file", seen)
		}
	})

	t.Run("PERMIT --token-file - reads stdin once and still resolves the tenant", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		var body map[string]any
		var seen string
		srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvk_new", "id": "t1", "name": "ci"})
		})
		// `tokens issue` resolves TWICE (once for the tenant, once in the client):
		// a second read of a consumed stdin would look like "no credential".
		if _, _, err := execRootStdin(t, bearer+"\n", "tokens", "issue", "--server", srv.URL,
			"--token-file", "-", "--tenant", "t_a", "--name", "ci", "--role", "admin"); err != nil {
			t.Fatalf("tokens issue --token-file -: %v", err)
		}
		if seen != "Bearer "+bearer {
			t.Fatalf("Authorization = %q after two resolutions", seen)
		}
		if body["tenant"] != "t_a" {
			t.Fatalf("issue body = %#v", body)
		}
	})

	t.Run("DENY two spellings of the credential, and an empty file", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		})
		empty := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"tokens", "ls", "--token", bearer, "--token-file", empty},
			{"tokens", "ls", "--token-file", empty},
			{"tokens", "ls", "--token-file", filepath.Join(t.TempDir(), "nope")},
		} {
			full := append(append([]string{}, args...), "--server", srv.URL)
			_, _, err := execRoot(t, full...)
			if err == nil || exitcode.From(err) != exitcode.Usage {
				t.Fatalf("%v: want exit %d, got %v", args, exitcode.Usage, err)
			}
		}
		if n := srv.hits.Load(); n != 0 {
			t.Fatalf("the plane was contacted %d time(s) by refused invocations", n)
		}
	})

	// The file form only helps an operator who learns it exists. --password and
	// --setup-token have warned about argv all along; the bearer — the
	// longest-lived of the three, and the one most often pasted — did not.
	t.Run("DENY silence when the bearer is passed in argv", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		var body map[string]any
		srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvk_new", "id": "t1", "name": "ci"})
		})
		// `tokens issue` resolves TWICE, so this also pins that the warning is one
		// line and not two: a repeated warning reads like two separate events.
		_, stderr, err := execRoot(t, "tokens", "issue", "--server", srv.URL,
			"--token", bearer, "--tenant", "t_a", "--name", "ci", "--role", "admin")
		if err != nil {
			t.Fatalf("--token must keep working, warning or not: %v", err)
		}
		if n := strings.Count(stderr, cliTokenArgvWarning); n != 1 {
			t.Fatalf("argv warning appeared %d time(s), want exactly 1:\n%s", n, stderr)
		}
		if !strings.Contains(stderr, "--token-file") {
			t.Fatalf("the warning does not name the way out:\n%s", stderr)
		}
		if strings.Contains(stderr, bearer) {
			t.Fatalf("the warning printed the secret it is warning about:\n%s", stderr)
		}
	})

	// PERMIT: the two spellings that are NOT argv stay silent. Warning on a bearer
	// that was never in argv would train the operator to ignore the line.
	t.Run("PERMIT no argv warning for the file form or the environment", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		})
		tokenFile := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte(bearer+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			env  string
			args []string
		}{
			{name: "file form", args: []string{"--token-file", tokenFile}},
			{name: "environment", env: bearer},
		} {
			t.Setenv("OLIVARES_TOKEN", tc.env)
			args := append([]string{"tokens", "ls", "--server", srv.URL}, tc.args...)
			_, stderr, err := execRoot(t, args...)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if strings.Contains(stderr, cliTokenArgvWarning) {
				t.Fatalf("%s: warned about argv for a bearer that was never in argv:\n%s", tc.name, stderr)
			}
		}
	})

	t.Run("DENY a bearer echoed back inside the plane's error body", func(t *testing.T) {
		prepareBootstrapCLITest(t)
		srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
			// A proxy or a badly written error page that reflects the request
			// headers. httpErr embeds this body verbatim.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"upstream rejected `+r.Header.Get("Authorization")+`"}`)
		})
		_, _, err := execRoot(t, "tokens", "ls", "--server", srv.URL, "--token", bearer)
		if err == nil {
			t.Fatal("a 500 must fail the command")
		}
		if strings.Contains(err.Error(), bearer) {
			t.Fatalf("the bearer came back through the error body: %v", err)
		}
		if got := exitcode.From(err); got != exitcode.Server {
			t.Fatalf("exit code = %d, want %d — redaction must not cost the classification", got, exitcode.Server)
		}
	})
}

// --- §5 BAJA · what was validated is what travels ------------------------------

// TestAccidentalWhitespaceIsNormalizedOnceAndTravelsTrimmed: the local check
// trimmed and the payload did not, so a value with an accidental space passed here
// and was refused by an engine that compares exactly — a legitimate invocation
// denied, with an error from the far end that never mentions whitespace.
func TestAccidentalWhitespaceIsNormalizedOnceAndTravelsTrimmed(t *testing.T) {
	prepareBootstrapCLITest(t)
	var body map[string]any
	var header string
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("X-Olivares-Tenant")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvk_x", "id": "t1", "name": "ci"})
	})
	if _, _, err := execRoot(t, "tokens", "issue", "--server", srv.URL, "--token", "olvk_su",
		"--tenant", " t_acme ", "--role", " admin ", "--name", "ci"); err != nil {
		t.Fatalf("tokens issue with padded values: %v", err)
	}
	if body["role"] != "admin" {
		t.Fatalf("role traveled as %#v, not the value that was validated", body["role"])
	}
	if body["tenant"] != "t_acme" || header != "t_acme" {
		t.Fatalf("tenant body = %#v, header = %q", body["tenant"], header)
	}

	// The same normalization on `members grant`, which repeated the pattern.
	prepareBootstrapCLITest(t)
	var grant map[string]any
	grantSrv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&grant)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "mem-1", "user_id": "usr-1", "tenant": "t_acme", "role": "editor"})
	})
	if _, _, err := execRoot(t, "members", "grant", "--server", grantSrv.URL, "--token", "olvk_su",
		"--tenant", " t_acme ", "--user", "usr-1", "--role", " editor "); err != nil {
		t.Fatalf("members grant with padded values: %v", err)
	}
	if grant["role"] != "editor" || grant["tenant"] != "t_acme" {
		t.Fatalf("grant body = %#v", grant)
	}
}

// --- §1 · --save-context validates before it writes ----------------------------

// TestSaveContextValidatesTheSessionAndUsesTheTenantSetupCreated. The tail of
// `auth bootstrap --save-context` claimed to go through the `auth login` path and
// did not: it wrote the config itself, skipping the whoami check that is the whole
// reason `auth login` exists, and preferred an ambient tenant over the one setup
// had just created.
func TestSaveContextValidatesTheSessionAndUsesTheTenantSetupCreated(t *testing.T) {
	prepareBootstrapCLITest(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(cliConfigOverrideEnv, configPath)
	t.Setenv("OLIVARES_TENANT", "t_from_a_previous_install")

	var whoamiCalls int
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case authSetupPath:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"usr-root","email":"admin@e.test","is_superadmin":true,`+
				`"organization":{"id":"org-1","tenant_id":"t_first","name":"E2E","slug":"e2e","status":"active","created_at":"x"}}`)
		case authLoginPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "olvs_session", "expires_at": "x"})
		case "/v1/auth/whoami":
			whoamiCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"kind": "user", "actor": "admin@e.test", "superadmin": true})
		default:
			http.NotFound(w, r)
		}
	})
	tokenFile := filepath.Join(t.TempDir(), "setup.token")
	if err := os.WriteFile(tokenFile, []byte("one-time"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := execRootStdin(t, "pw\n", "auth", "bootstrap", "--server", srv.URL,
		"--setup-token-file", tokenFile, "--email", "admin@e.test", "--password-file", "-",
		"--save-context"); err != nil {
		t.Fatalf("auth bootstrap --save-context: %v (stderr %q)", err, stderr)
	}
	if whoamiCalls != 1 {
		t.Fatalf("whoami calls = %d, want 1 — the session must be validated before it is stored", whoamiCalls)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "t_from_a_previous_install") {
		t.Fatalf("the new superadmin was bound to an ambient tenant:\n%s", saved)
	}
	if !strings.Contains(string(saved), "t_first") {
		t.Fatalf("the context does not carry the tenant setup created:\n%s", saved)
	}
}
