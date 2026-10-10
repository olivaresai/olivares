// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	codexPrompt  = `1. Open this link in your browser\n   https://auth.openai.com/codex/device\n2. Enter this one-time code\n   ABCD-12345\n`
	grokPrompt   = `Open this URL in your browser to approve:\n  https://accounts.x.ai/device\nCode: K7M2QX9P\nWaiting for approval…\n`
	claudePrompt = `Opening browser to sign in...\nIf the browser did not open, visit: https://claude.com/cai/oauth/authorize?code=true&state=stub\nPaste code here if prompted > `
)

// #470 (clean-host audit, O1): Codex could not reach its sign-in service, printed
// why and exited 1, and the page said only "The sign-in did not complete". The
// failed sign-in now carries the tool's own last line, its exit status and one
// next step, and never a secret, a URL path or query, or the one-time code.
func TestAFailedSignInSaysTheToolsOwnReason(t *testing.T) {
	const multibyteReasonRune = "é" // language-data: UTF-8 reason truncation input
	for _, tc := range []struct {
		name, driver, prompt, last string
		want                       string
		absent                     []string
	}{{
		name: "vendor refused", driver: "codex",
		last: `Error logging in with device code: device code request failed with status 403 Forbidden`,
		want: "Codex stopped (exit status 1): Error logging in with device code: device code request failed with status 403 Forbidden. Fix that, then start the sign-in again.",
	}, {
		name: "vendor refused after the prompt", driver: "codex", prompt: codexPrompt,
		last: `Error logging in with device code: device code request failed with status 403 Forbidden`,
		want: "Codex stopped (exit status 1): Error logging in with device code: device code request failed with status 403 Forbidden. Fix that, then start the sign-in again.",
	}, {
		// An unreachable service (#470's host): Codex prints this one line before
		// any link or code; its URL is not a link that ends the reason.
		name: "unreachable before any link", driver: "codex",
		last: `Error logging in with device code: error sending request for url (https://auth.openai.com/api/accounts/deviceauth/usercode)`,
		want: "Codex stopped (exit status 1): Error logging in with device code: error sending request for url (https://auth.openai.com). Fix that, then start the sign-in again.",
	}, {
		name: "secrets in the line", driver: "codex", prompt: codexPrompt,
		last:   `Error: error sending request for url (https://user:hunter2@auth.openai.com/api/accounts/deviceauth/usercode?code=ABCD-12345&state=s3cr3t) access_token=sk-abcdefghijklmnopqrstuvwxyz0123 for ABCD-12345`,
		want:   "Codex stopped (exit status 1): Error: error sending request for url (https://auth.openai.com) access_token=[REDACTED] for [code]. Fix that, then start the sign-in again.",
		absent: []string{"hunter2", "deviceauth", "s3cr3t", "ABCD-12345", "sk-abcdefghij"},
	}, {
		name: "other URL and token shapes", driver: "codex",
		last:   "HTTPS://Auth.OpenAI.com/x?code=1 auth.openai.com/oauth?code=X&state=Y ftp://h/p?x=1 sk-​abcdefghijklmnopqrstuvwxyz0123 9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddcc",
		want:   "Codex stopped (exit status 1): HTTPS://Auth.OpenAI.com auth.openai.com/oauth ftp://h [REDACTED:openai-key] [REDACTED]. Fix that, then start the sign-in again.",
		absent: []string{"code=", "state=", "/x", "/p", "abcdefghij", "9f8e7d6c"},
	}, {
		// Grok's code has no dash: only the code the tool printed identifies it.
		name: "the printed code", driver: "grok", prompt: grokPrompt,
		last:   `error: device authorization for K7M2QX9P was denied`,
		want:   "Grok Build stopped (exit status 1): error: device authorization for [code] was denied. Fix that, then start the sign-in again.",
		absent: []string{"K7M2QX9P"},
	}, {
		// Claude reads the code on its prompt line: its answer follows the prompt there.
		name: "after the paste prompt", driver: "claude", prompt: claudePrompt,
		last: "Paste code here if prompted > Invalid code",
		want: "Claude Code stopped (exit status 1): Invalid code. Fix that, then start the sign-in again.",
	}, {
		// Only a line the prompt opens loses it: the tool's error keeps its words.
		name: "the prompt inside an error", driver: "grok", prompt: grokPrompt,
		last: "error: denied while Waiting for approval",
		want: "Grok Build stopped (exit status 1): error: denied while Waiting for approval. Fix that, then start the sign-in again.",
	}, {
		// Grok's token-paste screen ends the prompt with ASCII dots; an error
		// that follows it on the line is the reason.
		name: "an error after the ASCII prompt", driver: "grok", prompt: grokPrompt,
		last: "Waiting for approval... error: access_denied",
		want: "Grok Build stopped (exit status 1): error: access_denied. Fix that, then start the sign-in again.",
	}, {
		name: "a long line", driver: "codex",
		last: strings.Repeat(multibyteReasonRune, 300),
		want: "Codex stopped (exit status 1): " + strings.Repeat(multibyteReasonRune, 120) + "…. Fix that, then start the sign-in again.",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			stub := "#!/bin/sh\n" +
				"printf '" + tc.prompt + "'\n" +
				"sleep 1\n" +
				// A blank line, then the reason with no newline: it is read only at EOF.
				"printf '\\n%s' '" + tc.last + "' >&2\n" +
				"exit 1\n"
			call, _ := newSignInServer(t, func(_ *Module, bin string) {
				if err := os.WriteFile(filepath.Join(bin, tc.driver), []byte(stub), 0o755); err != nil {
					t.Fatal(err)
				}
			})
			code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": tc.driver})
			if code != 202 {
				t.Fatalf("start = %d %v", code, s)
			}
			got := pollSignIn(t, call, s["id"].(string), "failed")["message"]
			if got != tc.want {
				t.Fatalf("message = %q\nwant      %q", got, tc.want)
			}
			for _, secret := range tc.absent {
				if strings.Contains(got.(string), secret) {
					t.Fatalf("message %q leaks %q", got, secret)
				}
			}
		})
	}
}

// The reason line reaches the console, the CLI and the engine log, so no secret
// shape may pass: a one-time code with or without a label, a name=value or cookie
// value, a short token, a credential after an auth scheme, userinfo, an encoded
// query. Over-redaction is allowed; the words that say why the sign-in failed are
// not lost.
func TestASignInReasonCarriesNoSecret(t *testing.T) {
	for _, tc := range []struct {
		line, code, want string
		absent           []string
	}{
		// The tool's own words, hosts, addresses, versions and times survive whole.
		{line: "Error logging in with device code: device code request failed with status 403 Forbidden", want: "Error logging in with device code: device code request failed with status 403 Forbidden"},
		{line: "dial tcp 104.18.33.45:443: connect: connection refused", want: "dial tcp 104.18.33.45:443: connect: connection refused"},
		{line: "x509: certificate has expired or is not yet valid: current time 2026-10-06T12:00:00Z is after 2026-01-01T00:00:00Z", want: "x509: certificate has expired or is not yet valid: current time 2026-10-06T12:00:00Z is after 2026-01-01T00:00:00Z"},
		{line: "unexpected status 429 Too Many Requests over HTTP/1.1 from grok-4.7", want: "unexpected status 429 Too Many Requests over HTTP/1.1 from grok-4.7"},
		{line: "connect ECONNREFUSED 127.0.0.1:443", want: "connect ECONNREFUSED 127.0.0.1:443"},
		{line: "Request failed with status code 403", want: "Request failed with status code 403"},
		{line: "Basic authentication failed: token rejected, state mismatch", want: "Basic authentication failed: token rejected, state mismatch"},
		{line: "status=403 error=access_denied", want: "status=403 error=access_denied"},
		{line: `{"error":"authorization_pending","error_description":"The user has not yet approved"}`, want: `{"error":"authorization_pending","error_description":"The user has not yet approved"}`},
		{line: "Paste code here if prompted > Invalid code", want: "Paste code here if prompted > Invalid code"},
		{line: "authorization: failed because the token expired", want: "authorization: failed because the token expired"},
		// Shapes the #546 review measured leaking.
		{line: "Code: K7M2QX9P expired", want: "Code: [REDACTED] expired"},
		{line: "error: code Z9Y8X7W6 expired", code: "K7M2QX9P", want: "error: code [REDACTED] expired"},
		{line: "invalid grant code=SplxlOBeZQQYbYS6WxSbIA", want: "invalid grant code=[REDACTED]"},
		{line: "&code=SplxlOBeZQQ", want: "&code=[REDACTED]"},
		{line: "Cookie: sessionid=Zx9aB3kQ7p", want: "Cookie: sessionid=[REDACTED]"},
		{line: "token rt-AbC123xyz-QwE9 rejected", want: "token [REDACTED] rejected"},
		{line: "Authorization: Basic dXNlcjpwYXNz", want: "Authorization: Basic [REDACTED]"},
		{line: "code abcd-1234 expired", want: "code [REDACTED] expired"},
		{line: "redirect to auth.openai.com/cb%3Fcode%3DZx9aB3kQ7p failed", want: "redirect to auth.openai.[REDACTED] failed"},
		// Their siblings: letters-only and numeric codes, other labels, schemes,
		// quotes and encodings.
		{line: "Code: KMQXPRST expired", want: "Code: [REDACTED] expired"},
		{line: "Code:\u00a0KMQXPRST expired, token\u3000QwErTyUiOpAsDf", want: "Code: [REDACTED] expired, token [REDACTED]"},
		{line: `{"user_code":"WDJBMPHX","interval":5}`, want: `{"user_code":[REDACTED],"interval":[REDACTED]}`},
		{line: "Cookie: sessionid=QwErTyUiOp; csrftoken=AsDfGhJkLz", want: "Cookie: sessionid=[REDACTED]; csrftoken=[REDACTED]"},
		{line: "authorization: basic dXNlcjpwYXNz", want: "authorization: basic [REDACTED]"},
		{line: "callback state=af0ifjsldkj mismatch", want: "callback state=[REDACTED] mismatch"},
		{line: `Authorization: Digest username="bob", response="6629fae49393a05397450978507c4ef1"`, absent: []string{"bob", "6629fae4"}},
		{line: `password="my secret pass" failed`, want: "password=[REDACTED] failed"},
		{line: `{"refresh_token": "abc def ghi"}`, want: `{"refresh_token": [REDACTED]}`},
		{line: "token QwErTyUiOpAsDf rejected", want: "token [REDACTED] rejected"},
		{line: "Your code is WDJBMPHX", want: "Your code is [REDACTED]"},
		{line: "Code - WDJBMPHX, user-code WDJBMPHX", want: "Code - [REDACTED], user-code [REDACTED]"},
		{line: "otp: 482913", want: "otp: [REDACTED]"},
		{line: "code is 123456, pin 1234", want: "code is [REDACTED], pin [REDACTED]"},
		{line: "Authorization: Negotiate YIIabcdefghij", want: "Authorization: Negotiate [REDACTED]"},
		{line: "Authorization: Token abcdefghijkl", want: "Authorization: Token [REDACTED]"},
		{line: "Proxy-Authorization: NTLM abcdefghijklmnop", want: "Proxy-Authorization: NTLM [REDACTED]"},
		{line: "Authorization: ApiKey abcdefghijklmnop", want: "Authorization: ApiKey [REDACTED]"},
		{line: "code: wdjbmphx", want: "code: [REDACTED]"},
		{line: "nonce abcdefghijklmnopqr", want: "nonce [REDACTED]"},
		{line: "code ABCD 1234 expired", want: "code [REDACTED] expired"},
		{line: "user code: ABCD EFGH", want: "user code: [REDACTED]"},
		{line: "user:hunter2@auth.openai.com failed", want: "[REDACTED]@auth.openai.com failed"},
		{line: "auth.openai.com/cb&#63;code&#61;abcdefghIJ", want: "auth.openai.com/cb"},
		{line: "code ＡＢＣＤ１２３４ bad", want: "code [REDACTED] bad"},
		// The cut comes after the redaction: no part of a token survives it.
		{line: strings.Repeat("word ", 47) + "Zx9aB3kQ7pLm", absent: []string{"Zx9aB"}},
		// Shapes the review measured clean: they stay clean.
		{line: "Enter ABCD-1234 to continue", absent: []string{"ABCD-1234"}},
		{line: "code ABCD-12345 expired", absent: []string{"ABCD-12345"}},
		{line: "token abcd-efgh-ijkl-mnop-qrst-uvwx-yz12-3456 rejected", absent: []string{"abcd-efgh", "3456"}},
		{line: "bad credential QWxhZGRpbjpvcGVuIHNlc2FtZQ==QWxhZGRpbjpvcGVuIHNlc2FtZQ", absent: []string{"QWxhZGRp"}},
		{line: "bad credential dXNlcjpwYXNzdXNlcjpwYXNz==dXNlcjpwYXNz", absent: []string{"dXNlcjpwYXNz"}},
		{line: "id_token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U invalid", absent: []string{"eyJhbGci", "dozjgNry"}},
		{line: "auth.openai.com/oauth?code=SplxlOBeZQQYbYS6WxSbIA&state=af0ifjsldkj", absent: []string{"SplxlOBeZQQ", "af0ifjsldkj"}},
		{line: "Set-Cookie: __Secure-next-auth.session-token=abcdefghij; Path=/", absent: []string{"abcdefghij"}},
		{line: "Authorization: Bearer abcdefghijklmnop", absent: []string{"abcdefghijklmnop"}},
		{line: "access_token=abcdefgh refresh_token: ijklmnop", absent: []string{"abcdefgh", "ijklmnop"}},
		{line: `{"device_code":"GmRhmhcxhwAzkoEqiMEg_DnyEysNkuNhszIySk9eS","user_code":"WDJB-MJHT"}`, absent: []string{"GmRhmhcx", "WDJB-MJHT"}},
		{line: "open https://auth.openai.com/device?user_code=WDJB-MJHT", absent: []string{"WDJB-MJHT"}},
		{line: "https://auth.openai.com/cb#access_token=abcdefghij", absent: []string{"abcdefghij"}},
		{line: "https://user:hunter2@auth.openai.com/", absent: []string{"hunter2"}},
		{line: "key sk-ant-api03-abcdefghijklmnopqrstuvwxyz rejected", absent: []string{"abcdefghij"}},
		{line: "olvs_abcdefghijk", absent: []string{"abcdefghijk"}},
		{line: "pasted code abcDEF123ghiJKL456mnoPQR789stu#xyzSTATEabc123def456 rejected", absent: []string{"abcDEF123", "xyzSTATE"}},
		{line: "tok" + string(rune(0x200b)) + "en=abcdefgh", absent: []string{"abcdefgh"}},
		{line: "\x1b]8;;https://auth.openai.com/cb?code=Zx9aB3kQ7pLm\x1b\\sign in\x1b]8;;\x1b\\", absent: []string{"Zx9aB3kQ7pLm"}},
	} {
		got := signInReason(tc.line, tc.code)
		if tc.want != "" && got != tc.want {
			t.Errorf("signInReason(%q) = %q\nwant %q", tc.line, got, tc.want)
		}
		for _, secret := range tc.absent {
			if strings.Contains(got, secret) {
				t.Errorf("signInReason(%q) = %q leaks %q", tc.line, got, secret)
			}
		}
		if len(got) > maxReason+len("…") {
			t.Errorf("signInReason(%q) is %d bytes", tc.line, len(got))
		}
	}
}

// Without a tool error to show (no output, or a login that ended with exit 0 but
// is not signed in, or a kill, or an end right after the prompt, including a
// prompt line that waits for the person), the plain sentence stays.
func TestAFailedSignInWithoutAToolErrorKeepsThePlainSentence(t *testing.T) {
	for name, tc := range map[string]struct{ driver, stub string }{
		"no output":           {"codex", "#!/bin/sh\nexit 1\n"},
		"exit 0, not signed":  {"codex", "#!/bin/sh\n[ \"$2\" = status ] && { echo 'Not logged in'; exit 1; }\necho 'Successfully logged in'\nexit 0\n"},
		"killed":              {"codex", "#!/bin/sh\necho 'Waiting for the device code'\nkill -9 $$\n"},
		"exit 1 after prompt": {"codex", "#!/bin/sh\nprintf '" + codexPrompt + "'\nsleep 1\nexit 1\n"},
		// Grok's prompt ends with its waiting line, Claude's with its paste prompt
		// (no newline, read at EOF): neither is a reason (#546 review).
		"grok: exit 1 after prompt":          {"grok", "#!/bin/sh\nprintf '" + grokPrompt + "'\nsleep 1\nexit 1\n"},
		"claude: exit 1 after prompt":        {"claude", "#!/bin/sh\nprintf '" + claudePrompt + "'\nsleep 1\nexit 1\n"},
		"grok: an error, then waiting again": {"grok", "#!/bin/sh\nprintf '" + grokPrompt + "error: slow down\\nWaiting for approval…\\n'\nsleep 1\nexit 1\n"},
	} {
		t.Run(name, func(t *testing.T) {
			call, _ := newSignInServer(t, func(_ *Module, bin string) {
				if err := os.WriteFile(filepath.Join(bin, tc.driver), []byte(tc.stub), 0o755); err != nil {
					t.Fatal(err)
				}
			})
			_, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": tc.driver})
			if got := pollSignIn(t, call, s["id"].(string), "failed")["message"]; got != "The sign-in did not complete. Start it again." {
				t.Fatalf("message = %q", got)
			}
		})
	}
}
