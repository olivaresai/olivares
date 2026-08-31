// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// This file guards ONE shape, not five call sites.
//
// The house redactor used to be `redactCoded(err error, token string) error`
// and it rebuilt a plain errors.New, so every caller that returned its result
// destroyed the exit code the error carried. Nineteen of its twenty callers were
// written that way; the one that was not (bootstrapclient.go) repaired it BY HAND.
// Four of those losses were reachable on this tree — cliTransport refuses an
// unsafe invocation with Usage(2), including the plain-HTTP credential refusal,
// and four families relabelled it Server(6): "the plane is down" instead of "your
// invocation is unsafe", which is the reading a script must not act on.
//
// The repair is a TYPE, not a habit: redactCLISecrets returns a string, so it
// cannot be returned where an error is expected, and the only two redactors that
// yield an error — redactCoded and redactCodedServer — read exitcode.From before
// they rebuild anything. The tests below pin both halves at once (the code
// survives AND the secret does not) and both directions (an unclassified failure
// is still promoted to Server, a classified one is never flattened).

// TestRedactCodedKeepsTheCodeAndScrubsEverySecret pins the contract of the one
// redactor a caller is allowed to return.
func TestRedactCodedKeepsTheCodeAndScrubsEverySecret(t *testing.T) {
	const bearer = "olvk_bearer_value"
	const password = "correct-horse-battery-staple"

	if got := redactCoded(nil, bearer); got != nil {
		t.Fatalf("redactCoded(nil) = %v, want nil", got)
	}

	for _, tc := range []struct {
		name     string
		in       error
		secrets  []string
		wantCode int
		wantMsg  string
	}{
		{
			name:     "auth classification survives redaction",
			in:       exitcode.New(exitcode.Auth, errors.New("invalid credentials for "+password)),
			secrets:  []string{password},
			wantCode: exitcode.Auth,
			wantMsg:  "invalid credentials for <redacted>",
		},
		{
			name:     "every secret is scrubbed, not just the first",
			in:       exitcode.New(exitcode.Conflict, errors.New(bearer+" and "+password)),
			secrets:  []string{bearer, password},
			wantCode: exitcode.Conflict,
			wantMsg:  "<redacted> and <redacted>",
		},
		{
			name:     "an unclassified error stays unclassified here",
			in:       errors.New("plain " + bearer),
			secrets:  []string{bearer},
			wantCode: exitcode.Err,
			wantMsg:  "plain <redacted>",
		},
		{
			// ⛔ NO MINIMUM LENGTH, AND THAT IS DELIBERATE — pinned here because the obvious
			// "improvement" is a credential leak. A short secret makes ReplaceAll scrub
			// aggressively and the message reads badly, which invites a guard like
			// `if len(secret) < 8 { continue }`. That guard would print short passwords
			// VERBATIM into an error the operator pipes into a log, and passwords are
			// passed here: cmd_auth.go:184,408,458,480 hand `password` to redactCoded.
			// A mangled message is recoverable; a leaked credential is not, so
			// over-redaction is the correct direction to fail in. If legibility matters,
			// reject absurd passwords at input — do not stop redacting them.
			// The secret is "7" and not a letter for a reason worth knowing: this
			// harness also asserts the secret does not appear in the output, and every
			// letter of "<redacted>" (r, e, d, a, c, t) would be found INSIDE the
			// replacement itself, so such a case can never pass however correct the
			// redaction is. That is another face of the same pathology — a
			// single-character secret is indistinguishable from ordinary text — and one
			// more reason to refuse absurd passwords at input rather than here.
			// ⛔ CAMBIADO AL RESTAURAR #824 (PR #1112), y el motivo importa más que el string.
			// Este caso fijaba el comportamiento POST-REVERT: mientras #824 estuvo fuera,
			// redactCLISecrets no tenía suelo y un secreto de un carácter salía sustituido
			// —y de paso destrozaba el mensaje: el "7" de "attempt 7" también caía—.
			// #824 no vuelve al `continue` que ese pin prohíbe (eso SÍ imprimiría el secreto):
			// RETIENE el mensaje entero, así que el secreto no se emite nunca y su longitud
			// tampoco se filtra por el número de `<redacted>`. El suelo de 12 está atado a lo
			// que el plano ACUÑA —84 caracteres, core/auth/credential.go:22-41,58-74—, así que
			// ninguna credencial real cae por esta rama.
			name:     "a one-character secret withholds the message instead of mangling it",
			in:       exitcode.New(exitcode.Auth, errors.New("bad password: 7 (attempt 7)")),
			secrets:  []string{"7"},
			wantCode: exitcode.Auth,
			wantMsg: "message withheld: it contains the 1-character value supplied as the " +
				"credential, which is too short to remove without destroying the text; re-run " +
				"with the real credential to read it",
		},
		{
			// A bearer resolved from a client context is "" as far as the flags
			// know. Treating that as a match would replace every empty string in
			// the message — i.e. redact the message itself.
			name:     "an empty secret redacts nothing",
			in:       exitcode.New(exitcode.NotFound, errors.New("no such token")),
			secrets:  []string{""},
			wantCode: exitcode.NotFound,
			wantMsg:  "no such token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactCoded(tc.in, tc.secrets...)
			if code := exitcode.From(got); code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (%v)", code, tc.wantCode, got)
			}
			if got.Error() != tc.wantMsg {
				t.Fatalf("message = %q, want %q", got.Error(), tc.wantMsg)
			}
			for _, secret := range tc.secrets {
				if secret != "" && strings.Contains(got.Error(), secret) {
					t.Fatalf("secret %q survived: %q", secret, got.Error())
				}
			}
		})
	}
}

// TestRedactCodedServerPromotesOnlyWhatNobodyClassified is the contrafactual in
// BOTH directions. Promotion is what the four defective call sites were trying to
// express; flattening is what they actually did.
func TestRedactCodedServerPromotesOnlyWhatNobodyClassified(t *testing.T) {
	const bearer = "olvk_bearer_value"

	if got := redactCodedServer(nil, bearer); got != nil {
		t.Fatalf("redactCodedServer(nil) = %v, want nil", got)
	}

	for _, tc := range []struct {
		name     string
		in       error
		wantCode int
	}{
		{
			// http.NewRequestWithContext, a marshal failure: nobody said what it
			// was, and for a transport path "the plane failed" is the honest read.
			name:     "PROMOTE an error carrying no code at all",
			in:       errors.New("unsupported protocol scheme"),
			wantCode: exitcode.Server,
		},
		{
			// THE MEASURED LOSS: cliTransport's plain-HTTP credential refusal.
			name:     "KEEP the usage refusal cliTransport classified",
			in:       exitcode.New(exitcode.Usage, errors.New("refusing to send a credential over plain HTTP: "+bearer)),
			wantCode: exitcode.Usage,
		},
		{
			name:     "KEEP an auth classification",
			in:       exitcode.New(exitcode.Auth, errors.New("rejected")),
			wantCode: exitcode.Auth,
		},
		{
			// cliDo already says Server; promotion must be idempotent, not double.
			name:     "KEEP the server classification cliDo attached",
			in:       exitcode.New(exitcode.Server, errors.New("dial tcp: connection refused")),
			wantCode: exitcode.Server,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactCodedServer(tc.in, bearer)
			if code := exitcode.From(got); code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (%v)", code, tc.wantCode, got)
			}
			if strings.Contains(got.Error(), bearer) {
				t.Fatalf("the bearer survived: %q", got.Error())
			}
		})
	}
}

// TestUnsafeInvocationExitsUsageInEveryClientFamily is the end-to-end half of the
// same claim, through the four call sites that lost a REAL code.
//
// The address is TEST-NET-3 (RFC 5737, never routed) and the refusal is decided
// before any packet: a client that got as far as dialing would burn its timeout
// and exit 6, which is exactly the answer this test exists to distinguish from.
func TestUnsafeInvocationExitsUsageInEveryClientFamily(t *testing.T) {
	const bearer = "olvk_caller_secret"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "findings export (cmd_findings.go)", args: []string{"findings", "export"}},
		{name: "compliance holds ls (cmd_compliance.go)", args: []string{"compliance", "holds", "ls"}},
		{name: "mcp pins ls (cmd_mcp.go)", args: []string{"mcp", "pins", "ls"}},
		{name: "auth status (cmd_auth.go, fetchAuthWhoami)", args: []string{"auth", "status"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareRedactionCLITest(t)
			args := append(append([]string(nil), tc.args...),
				"--server", "http://198.51.100.7:8080",
				"--token", bearer, "--tenant", "tenant-a", "--timeout", "800ms")
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatal("a bearer to a remote plain-HTTP plane must be refused")
			}
			// Name the guard that fired. Every family also refuses a MISSING value
			// with Usage(2), so a test that only read the code could pass through
			// the wrong door.
			if !strings.Contains(err.Error(), "plain HTTP") {
				t.Fatalf("a different guard answered: %v", err)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit code = %d, want %d — an unsafe invocation is not a dead plane: %v",
					got, exitcode.Usage, err)
			}
			if strings.Contains(err.Error(), bearer) {
				t.Fatalf("the bearer is in the refusal: %v", err)
			}
		})
	}
}

// TestPlaneErrorBodyNeverCarriesTheBearerBack is the other half of the census:
// httpErr embeds the response body VERBATIM, so a proxy, a WAF or a badly written
// error page that reflects the request headers hands the operator's own bearer
// back to them — in the terminal and in whatever captures stderr.
//
// For `mcp pins` this was STRUCTURAL, not an omission: mcpPinsClient.do did not
// return the bearer, so its three callers had no secret to redact with even if
// they had wanted to. The fix is the signature, which is why the witness goes
// through the command and not through mcpPinsHTTPError.
func TestPlaneErrorBodyNeverCarriesTheBearerBack(t *testing.T) {
	const bearer = "olvk_reflected_bearer"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "mcp pins ls", args: []string{"mcp", "pins", "ls"}},
		{name: "mcp pins approve", args: []string{"mcp", "pins", "approve", "github.search", "--fingerprint", "fp"}},
		{name: "mcp pins rm", args: []string{"mcp", "pins", "rm", "github.search"}},
		{name: "findings export", args: []string{"findings", "export"}},
		{name: "compliance holds ls", args: []string{"compliance", "holds", "ls"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareRedactionCLITest(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"upstream rejected `+r.Header.Get("Authorization")+`"}`)
			}))
			defer srv.Close()
			args := append(append([]string(nil), tc.args...),
				"--server", srv.URL, "--token", bearer, "--tenant", "tenant-a")
			_, _, err := execRoot(t, args...)
			if err == nil {
				t.Fatal("a 500 must fail the command")
			}
			if strings.Contains(err.Error(), bearer) {
				t.Fatalf("the bearer came back through the error body: %v", err)
			}
			if got := exitcode.From(err); got != exitcode.Server {
				t.Fatalf("exit code = %d, want %d — redaction must not cost the classification: %v",
					got, exitcode.Server, err)
			}
		})
	}
}

// TestMCPPinsSuccessPathIsUnchangedByTheBearerPlumbing is the OTHER contrafactual:
// mcpPinsClient.do grew a return value, and the happy path must still exit 0 and
// print exactly what it printed before.
func TestMCPPinsSuccessPathIsUnchangedByTheBearerPlumbing(t *testing.T) {
	prepareRedactionCLITest(t)
	var seen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		if got := r.Header.Get("Authorization"); got != "Bearer olvk_live" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"items":[{"tool":"github.search","fingerprint":"short-fp","pinned_at":"2026-07-20T09:00:00Z","pin_count":2}]}`)
	}))
	defer srv.Close()

	out, _, err := execRoot(t, "mcp", "pins", "ls",
		"--server", srv.URL, "--token", "olvk_live", "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("the success path must still exit 0: %v", err)
	}
	if seen != 1 {
		t.Fatalf("server hits = %d, want 1", seen)
	}
	for _, want := range []string{"TOOL", "github.search", "short-fp"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the table lost %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<redacted>") {
		t.Fatalf("a legitimate row was redacted:\n%s", out)
	}
}

// TestMCPAddOnBoundaryStatesItsExitCode: the 501 branch used to return a bare
// fmt.Errorf, so exitcode.From read the generic 1 by omission rather than by
// decision — and the same status through httpErr would have been Server(6), "the
// plane failed", which an add-on boundary is not. complianceHTTPError states
// Err(1) for the identical case; this one now does too.
func TestMCPAddOnBoundaryStatesItsExitCode(t *testing.T) {
	prepareRedactionCLITest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(w, `{"error":"no verifier wired"}`)
	}))
	defer srv.Close()

	_, _, err := execRoot(t, "mcp", "pins", "ls",
		"--server", srv.URL, "--token", "olvk_live", "--tenant", "tenant-a")
	if err == nil {
		t.Fatal("the community 501 must fail")
	}
	if got := exitcode.From(err); got != exitcode.Err {
		t.Fatalf("exit code = %d, want %d — an add-on boundary is not a server failure: %v",
			got, exitcode.Err, err)
	}
	if !strings.Contains(err.Error(), "enterprise add-on") {
		t.Fatalf("the 501 is not actionable: %v", err)
	}
}

func prepareRedactionCLITest(t *testing.T) {
	t.Helper()
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	t.Setenv(cliCleartextOptInEnv, "")
}
