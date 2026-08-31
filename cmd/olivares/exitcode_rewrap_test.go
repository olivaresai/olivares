// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// A credential shaped exactly like the ones core/auth mints
// (core/auth/credential.go:26-41,60-73): "<prefix>_<selector>_<secret>", a
// 4-character prefix plus base32 of 16 and 32 random bytes — 84 characters.
// Tests use a real-shaped one because the redaction rules below turn on LENGTH.
const rewrapToken = "olvk_AAAAAAAAAAAAAAAAAAAAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

const rewrapTenant = "00000000-0000-0000-0000-000000000000"

// runCLIExit executes one invocation through the real root command and returns
// the exit code the process would use, plus both streams. (cmd_sources_plan_test
// already owns the name runCLI, with a different signature.)
func runCLIExit(t *testing.T, argv ...string) (code int, stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("OLIVARES_CLI_CONFIG", t.TempDir()+"/config.yaml")
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(argv)
	_, err = root.ExecuteC()
	if err == nil {
		return exitcode.OK, out.String(), errb.String(), nil
	}
	return exitcode.From(err), out.String(), errb.String(), err
}

// theFourRewrapSites is the census the backlog row C08-03 names: the four
// commands whose HTTP client re-wrapped an ALREADY-CLASSIFIED error as
// exit 6. Each entry reaches cliTransport through a different one of them.
//
//	cmd_mcp.go:221 · cmd_findings.go:73 · cmd_compliance.go:160 · cmd_auth.go:358
var theFourRewrapSites = map[string][]string{
	"mcp pins ls (cmd_mcp.go)": {
		"mcp", "pins", "ls", "--tenant", rewrapTenant,
	},
	"findings export (cmd_findings.go)": {
		"findings", "export", "--format", "sarif", "--tenant", rewrapTenant,
	},
	"compliance holds ls (cmd_compliance.go)": {
		"compliance", "holds", "ls", "--tenant", rewrapTenant,
	},
	"auth status (cmd_auth.go)": {
		"auth", "status", "--tenant", rewrapTenant,
	},
}

// TestArgumentErrorsKeepTheirUsageExitCodeThroughEveryClient is the DENY half of
// C08-03, and the defect the row exists to close.
//
// cliTransport classifies every refusal about the CALLER'S ARGUMENTS as exit 2
// ("the invocation itself is wrong" — the contract `olivares --help` publishes),
// and TestTransportArgumentErrorsAreUsageErrors in tlspin_test.go proves it at
// the unit level. Four command clients then threw that classification away:
// they wrapped the returned error in exitcode.New(exitcode.Server, …), so a
// mistyped --pin-sha256 came out as 6 — "the control plane failed or was
// unreachable" — from `mcp`, `findings`, `compliance` and `auth status`, while
// the SAME mistake exits 2 from `agent session ls`. A script cannot retry a 6
// that was really a typo, and must not retry a typo at all.
//
// cmd_compliance.go:135-143 already recorded the disagreement in a comment
// ("Two is right … `findings export` exits 6 by falling into cliTransport's
// generic error"); this test is that sentence made executable.
func TestArgumentErrorsKeepTheirUsageExitCodeThroughEveryClient(t *testing.T) {
	// Two argument mistakes cliTransport refuses, reachable through all four
	// clients. Both need a resolvable server and token so the command gets past
	// its own preconditions and REACHES the transport.
	mistakes := map[string][]string{
		"malformed --pin-sha256": {
			"--server", "https://plane.invalid", "--token", rewrapToken,
			"--pin-sha256", "not-a-digest",
		},
		"--pin-sha256 on an http server": {
			"--server", "http://plane.invalid", "--token", rewrapToken,
			"--pin-sha256", "not-a-digest",
		},
	}
	for site, base := range theFourRewrapSites {
		for mistake, extra := range mistakes {
			t.Run(site+" / "+mistake, func(t *testing.T) {
				code, stdout, _, err := runCLIExit(t, append(append([]string(nil), base...), extra...)...)
				if err == nil {
					t.Fatalf("%s accepted %s", site, mistake)
				}
				if code != exitcode.Usage {
					t.Fatalf("exit = %d, want %d (usage) — %s re-wrapped an argument error as a "+
						"transport failure: %v", code, exitcode.Usage, site, err)
				}
				if strings.TrimSpace(stdout) != "" {
					t.Errorf("a refused invocation wrote to stdout: %q", stdout)
				}
			})
		}
	}
}

// TestTransportFailuresStillExitServerThroughEveryClient is the OTHER direction,
// and the one a fix like this breaks silently. Exit 6 is not wrong at these call
// sites — it is the right answer for an error that carries NO classification of
// its own, and for a control plane that is genuinely unreachable. Preserving an
// inner code must not stop the fallback from applying.
func TestTransportFailuresStillExitServerThroughEveryClient(t *testing.T) {
	// A CA bundle that does not exist: loadCLIRootCAs returns a PLAIN error with
	// no exit code attached, so the call site's own fallback is what must answer.
	for site, base := range theFourRewrapSites {
		t.Run(site+" / unreadable --ca-cert", func(t *testing.T) {
			argv := append(append([]string(nil), base...),
				"--server", "https://plane.invalid", "--token", rewrapToken,
				"--ca-cert", t.TempDir()+"/no-such-bundle.pem")
			code, _, _, err := runCLIExit(t, argv...)
			if err == nil {
				t.Fatalf("%s accepted a missing CA bundle", site)
			}
			if code != exitcode.Server {
				t.Fatalf("exit = %d, want %d (server): an unclassified transport failure lost its "+
					"fallback code in %s: %v", code, exitcode.Server, site, err)
			}
		})
	}
}

// TestRedactionKeepsTheExitCodeAndTheChain pins the root cause the backlog row
// names at cmd_auth.go:417-422: redactCLIError returned errors.New(...), which
// DESTROYS the classification. Every one of the four sites calls it on the way
// out, so the exit code a script sees depended on whether the caller happened to
// pass --token: with a token the code was rebuilt away, without one the error
// passed through intact. Same mistake, two different exit codes.
func TestRedactionKeepsTheExitCodeAndTheChain(t *testing.T) {
	for _, code := range []int{exitcode.Usage, exitcode.Auth, exitcode.NotFound,
		exitcode.Conflict, exitcode.Server, exitcode.Degraded, exitcode.Indeterminate} {
		coded := exitcode.New(code, errors.New("refused: "+rewrapToken+" is not usable"))
		got := redactCoded(coded, rewrapToken)
		if exitcode.From(got) != code {
			t.Errorf("redaction turned exit %d into %d", code, exitcode.From(got))
		}
		if strings.Contains(got.Error(), rewrapToken) {
			t.Errorf("the credential survived redaction: %s", got.Error())
		}
		if !strings.Contains(got.Error(), "<redacted>") {
			t.Errorf("the redacted message does not say a value was removed: %s", got.Error())
		}
	}
}

// TestRedactionDoesNotLeakThroughUnwrapping. Preserving the exit code must not
// preserve a path back to the raw text. errors.Unwrap on the result — at any
// depth — must never hand anybody the credential back.
func TestRedactionDoesNotLeakThroughUnwrapping(t *testing.T) {
	raw := errors.New("refused: " + rewrapToken)
	got := redactCoded(exitcode.New(exitcode.Auth, raw), rewrapToken)
	for depth := 0; got != nil && depth < 16; depth++ {
		if strings.Contains(got.Error(), rewrapToken) {
			t.Fatalf("unwrap depth %d hands back the credential: %s", depth, got.Error())
		}
		got = errors.Unwrap(got)
	}
}

// TestAShortCredentialIsWithheldRatherThanShredded is the second half of the row:
// "redacta por subcadena sin longitud mínima: con --token t el error sale
// ilegible". Substring replacement with a one-character token rewrites every
// occurrence of that letter, so "no server: set --server, OLIVARES_SERVER_URL,
// or an active client context" comes back as a string with no words left in it.
//
// The fix is NOT to stop redacting — that would print the value the caller
// passed as a credential. It is to withhold the message and say why.
func TestAShortCredentialIsWithheldRatherThanShredded(t *testing.T) {
	const msg = "no server: set --server, OLIVARES_SERVER_URL, or an active client context"
	got := redactCoded(exitcode.New(exitcode.Usage, errors.New(msg)), "t")

	if exitcode.From(got) != exitcode.Usage {
		t.Errorf("withholding the message also lost the exit code: %d", exitcode.From(got))
	}
	if strings.Count(got.Error(), "<redacted>") > 1 {
		t.Errorf("the message was shredded rather than withheld: %s", got.Error())
	}
	// It must still be a sentence an operator can act on, and it must name the
	// reason: the value passed as a credential is too short.
	for _, want := range []string{"credential", "short"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("the withheld message does not mention %q, so it does not explain itself: %s",
				want, got.Error())
		}
	}
}

// TestAMessageWithoutTheCredentialIsLeftAlone. The withholding path above must
// not fire on every short token — only when the message actually contains it.
func TestAMessageWithoutTheCredentialIsLeftAlone(t *testing.T) {
	original := exitcode.New(exitcode.Conflict, errors.New("hold already released"))
	got := redactCoded(original, "zq")
	if got.Error() != "hold already released" {
		t.Errorf("a message that never mentioned the credential was rewritten: %s", got.Error())
	}
	if exitcode.From(got) != exitcode.Conflict {
		t.Errorf("exit = %d, want %d", exitcode.From(got), exitcode.Conflict)
	}
}

// okComplianceServer answers, with a 200 and a well-formed body, every route the
// four clients call. It is the control that makes the DENY assertions above mean
// something: without it, "the command failed" would be satisfied just as well by
// a command that is simply broken.
func okComplianceServer(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(mcpToolPinsPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	mux.HandleFunc(findingsExportPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2.1.0","runs":[]}`))
	})
	mux.HandleFunc(complianceBase+"/holds", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	mux.HandleFunc("/v1/auth/whoami", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"kind":"token","actor":"svc","grants":[{"tenant":"` +
			rewrapTenant + `","role":"admin"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestSuccessStillExitsZeroThroughEveryClient is the PERMIT half, and the half
// that gets forgotten: a change to how errors are classified must not make the
// legitimate caller's success path return anything but 0. Asserted on the real
// commands, against a real server, not on the error helpers.
func TestSuccessStillExitsZeroThroughEveryClient(t *testing.T) {
	srv := okComplianceServer(t)
	for site, base := range theFourRewrapSites {
		t.Run(site, func(t *testing.T) {
			argv := append(append([]string(nil), base...),
				"--server", srv, "--token", rewrapToken)
			code, _, _, err := runCLIExit(t, argv...)
			if err != nil {
				t.Fatalf("%s failed on the success path: exit %d: %v", site, code, err)
			}
			if code != exitcode.OK {
				t.Fatalf("exit = %d, want 0", code)
			}
		})
	}
}
