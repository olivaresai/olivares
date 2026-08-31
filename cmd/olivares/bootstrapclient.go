// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// maxBootstrapCLIResponseSize bounds any first-run API response the CLI reads, so
// a hostile or broken plane cannot make the client allocate without limit.
const maxBootstrapCLIResponseSize = 1 << 20

// bootstrapClient is the shared authenticated JSON client of the FIRST-RUN command
// families — `tokens`, `users`, `members` and `tenants`, plus the unauthenticated
// `auth bootstrap` leg. It is the mcpPinsClient shape (cmd_mcp.go:209), copied
// deliberately rather than reinvented: same resolution order (flag > env > client
// context, via resolveCLIConfig), same hardened transport and pinning
// (cliTransport), same transport-failure classification (cliDo → exit 6), same
// redaction of the caller's own bearer in every error it returns.
//
// IT DECIDES NOTHING ABOUT AUTHORIZATION. Every one of these routes is gated
// server-side — superadmin for /v1/users and /v1/system/orgs, token:write in the
// bound tenant for /v1/tokens, membership:write for /v1/memberships — and the
// client's only job is to carry the caller's credential and the tenant header the
// engine's own resolveTenant reads (core/api/middleware.go:320). There is no
// client-side role check to drift from the server's, and no path that adds a right
// the caller does not already hold.
type bootstrapClient struct {
	flags *authClientFlags
	// surface names the API family in read/decode failures, so "decode users
	// response" and "decode tokens response" are different sentences.
	surface string
	// anonymous marks the legs that must work with no credential at all: POST
	// /v1/setup, which is setup-gate-exempt (core/api/middleware.go:197) and whose
	// gate is the one-time setup token in its body, and POST /v1/auth/login, whose
	// gate is the password in its body. Everywhere else a missing token is refused
	// HERE, before any request is built — the engine would answer 401 anyway, and
	// asking it first would tell an unauthenticated caller that the host exists and
	// is an Olivares plane.
	//
	// IT MEANS "SEND NO CREDENTIAL", NOT "DO NOT REQUIRE ONE". Until the sol-max
	// contrast of 2026-08-16 it only skipped the local missing-token refusal and
	// then resolved credentials normally, so both legs shipped the ambient bearer:
	//
	//   - a token from a previous context traveled to whatever --server the
	//     operator pointed at, including an install that has never seen it;
	//   - and — measured, not hypothesized — a STALE OLIVARES_TOKEN made the
	//     middleware reject the request 401 before the public handler ever ran, so
	//     a valid email and password could not recover a session. A credential the
	//     caller did not ask to use denied a caller who could legitimately get in.
	//
	// SkipCredentials (cliconfig.go:52) is the mechanism that was already there for
	// exactly this, unused by these commands; do() now applies it.
	anonymous bool
	// carriesSecret says the BODY carries a secret even when no bearer does: the
	// setup token, the first password, a login password. The transport needs it to
	// refuse cleartext and cross-origin redirects for these legs — judging by the
	// Authorization header alone would classify the two most secret requests this
	// CLI makes as the two that need no protection.
	carriesSecret bool
}

// do performs one JSON request and returns the raw body and status. It never
// interprets the status: each verb classifies its own, because "404" means
// different things to `tokens revoke` (no such token, or one outside your
// authority) and to `tenants rm`.
func (c bootstrapClient) do(cmd *cobra.Command, method, path string, body any) ([]byte, int, string, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return nil, 0, "", redactCoded(err, c.flags.effectiveToken())
	}
	// The anonymous legs send NO credential: not the flag's, not the environment's,
	// not the active context's. See the field comment above for what shipping one
	// cost in both directions.
	opts.SkipCredentials = c.anonymous
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return nil, 0, "", redactCoded(err, c.flags.effectiveToken())
	}
	if resolved.Server == "" {
		return nil, 0, "", missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	}
	if !c.anonymous && resolved.Token == "" {
		return nil, 0, "", missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved:       resolved,
		Insecure:       c.flags.insecure,
		Timeout:        c.flags.timeout,
		Stderr:         cmd.ErrOrStderr(),
		CarriesSecret:  c.carriesSecret,
		AllowCleartext: c.flags.allowCleartext,
	})
	if err != nil {
		// cliTransport classifies its OWN refusals: an argument it will not accept
		// is exit 2 (clitransport.go:59), including the plain-HTTP refusal. Blanket
		// exit 6 would relabel "your invocation is unsafe" as "the plane is down",
		// which is the one reading a script must not act on. Anything it does not
		// classify stays 6, as before.
		//
		// This is where redactCodedServer comes from: the rule was written out here
		// by hand and copied nowhere, so the four sibling clients kept the blanket
		// promotion. It is one call now, and the same call everywhere.
		return nil, 0, resolved.Token, redactCodedServer(err, resolved.Token)
	}
	var requestBody io.Reader
	if body != nil {
		encoded, merr := json.Marshal(body)
		if merr != nil {
			return nil, 0, resolved.Token, merr
		}
		requestBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, resolved.Server+path, requestBody)
	if err != nil {
		return nil, 0, resolved.Token, redactCodedServer(err, resolved.Token)
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return nil, 0, resolved.Token, redactCodedServer(err, resolved.Token)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBootstrapCLIResponseSize+1))
	if err != nil {
		return nil, resp.StatusCode, resolved.Token, exitcode.New(exitcode.Server,
			fmt.Errorf("read %s response: %w", c.surface, err))
	}
	if len(raw) > maxBootstrapCLIResponseSize {
		return nil, resp.StatusCode, resolved.Token, exitcode.New(exitcode.Server,
			fmt.Errorf("%s response exceeds %d bytes", c.surface, maxBootstrapCLIResponseSize))
	}
	return raw, resp.StatusCode, resolved.Token, nil
}

// expect is the one-liner every verb uses: perform the call and hand back the body
// only when the status is the one this operation contracts for. Anything else goes
// through httpErr (cmd_agent.go:589), which is what makes 401/403 exit 3, 404 exit
// 4, 409 exit 5 and 5xx exit 6 for these families without each verb restating it.
func (c bootstrapClient) expect(cmd *cobra.Command, method, path string, body any, want int) ([]byte, error) {
	raw, status, bearer, err := c.do(cmd, method, path, body)
	if err != nil {
		return nil, err
	}
	if status != want {
		// THE ERROR BODY IS THE PLANE'S, AND IT MAY CONTAIN OUR BEARER. httpErr
		// embeds the response body verbatim (cmd_agent.go:589), and a proxy, a WAF
		// or a badly written error page that echoes the request headers puts the
		// token straight into the operator's terminal and into whatever captures
		// stderr. do() already redacts it from errors WE build; this closes the one
		// the SERVER builds. redactCoded keeps the exit code httpErr attached, so a
		// script can still tell 401 from 500.
		return nil, redactCoded(bootstrapHTTPError(status, raw), bearer)
	}
	return raw, nil
}

// bootstrapHTTPError names the ONE refusal an operator meets on these routes that
// httpErr's generic wording cannot explain: the AAL3 step-up.
//
// Three first-run-adjacent routes are gated on a verified hardware ceremony
// (core/api/middleware.go:298): superadmin enable/disable, the tenant residency
// pin, and console onboarding.
//
// WHAT THIS MESSAGE USED TO SAY, AND WHY IT WAS WRONG. It said no CLI credential
// can carry AAL3, full stop, and three verbs were withheld on that premise. Half
// of it is true — an API token's principal never sets an assurance level
// (core/auth/authenticator.go:220) and a session is minted at AAL1 — but a
// step-up ELEVATES THE SESSION ROW, not the credential (core/auth/assurance.go:57):
// the WebAuthn ceremony raises the very session the operator already holds, for 15
// minutes (assurance.go:31). `auth login --token` accepts "a session you already
// hold", so a superadmin who stepped up in the console CAN carry AAL3 here. The
// false premise denied a legitimate caller a whole surface; the verbs now exist
// and this message tells the operator how to satisfy the gate instead of telling
// them to give up on the CLI.
func bootstrapHTTPError(status int, raw []byte) error {
	if status == http.StatusForbidden && bytes.Contains(raw, []byte("step-up")) {
		return exitcode.New(exitcode.Auth, fmt.Errorf(
			"the control plane requires an AAL3 step-up for this operation and the credential you "+
				"sent does not carry one. An API token never can (it has no assurance level) and a "+
				"password session starts at AAL1; a USER SESSION that completed the WebAuthn/PIV "+
				"ceremony does, for 15 minutes. Run the ceremony in the console, then pass that "+
				"session here with `olivares auth login --token-file <file>`: HTTP %d: %s",
			status, trimAPIErrorBody(raw)))
	}
	return httpErr(status, raw)
}

// trimAPIErrorBody keeps an error body short enough to read on one screen. The
// engine's error envelope is small; a proxy's HTML page is not.
func trimAPIErrorBody(raw []byte) string {
	const maxShown = 400
	s := string(bytes.TrimSpace(raw))
	if len(s) > maxShown {
		return s[:maxShown] + "…"
	}
	return s
}

// redactCoded scrubs one or more secrets out of an error WITHOUT throwing away
// the exit code it carries. With redactCodedServer below it is the ONLY way to
// obtain a redacted error in this package — redactCLISecrets (cmd_auth.go)
// returns a string precisely so that the code-destroying shortcut cannot be
// written any more.
//
// Rebuilding an error is what loses the classification: a plain errors.New drops
// the exitcode.coded httpErr attached, and a rejected password exits 1 instead
// of 3 — measured, in the first run of the C-21 walkthrough, on
// `auth login --email` against a real engine. So the code is read FIRST and put
// back on the outside. The secrets these families send — a password, a one-time
// setup token, the caller's own bearer — must never survive into a terminal or a
// log, and the script that called them must still be able to branch on the
// reason.
func redactCoded(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	code := exitcode.From(err)
	redacted := err
	// Rebuild ONLY when a secret was actually found. The message is identical
	// either way; what an untouched error keeps is its wrapped chain, which the
	// old helper discarded for every non-empty secret whether or not it appeared
	// in the message.
	msg := err.Error()
	if scrubbed := redactCLISecrets(msg, secrets...); scrubbed != msg {
		redacted = errors.New(scrubbed)
	}
	return exitcode.New(code, redacted)
}

// redactCodedServer is redactCoded for the transport paths that must PROMOTE an
// unclassified failure to Server(6) — a request that could not be built, a plane
// that could not be reached — without flattening a failure that already said
// what it was.
//
// It is the rule this file wrote by hand and the other four clients did not: a
// blanket exitcode.New(exitcode.Server, …) around cliTransport's refusals
// relabels "your invocation is unsafe" (exit 2, clitransport.go:170, including
// the plain-HTTP credential refusal) as "the plane is down" (exit 6), which is
// the one reading a script must not act on. Anything cliTransport does not
// classify still ends at 6, exactly as before.
//
// Err is the right test because it is what exitcode.From returns for an error
// carrying no code at all; promoting only then is the difference between
// "nobody classified this" and "somebody classified this as generic".
func redactCodedServer(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	if exitcode.From(err) == exitcode.Err {
		err = exitcode.New(exitcode.Server, err)
	}
	return redactCoded(err, secrets...)
}

// decodeBootstrapJSON unmarshals an API response, classifying a body the CLI
// cannot read as a SERVER failure (exit 6) rather than the generic 1: a plane
// answering 200 with something that is not the declared shape is broken, and a
// script must be able to tell that from a rejected request.
func decodeBootstrapJSON(surface string, raw []byte, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return exitcode.New(exitcode.Server, fmt.Errorf("decode %s response: %w", surface, err))
	}
	return nil
}

// bootstrapPathID escapes one caller-supplied path segment.
//
// It is not decoration. These verbs take an id as a positional argument, and a
// value carrying `/` or `?` would otherwise re-target the request at a different
// route of the same plane — `tokens revoke '../../system/orgs/t_x'` is a DELETE
// somewhere the operator never named. url.PathEscape leaves the id itself
// untouched (ids are hex/uuid) and neutralizes everything else.
func bootstrapPathID(id string) string { return url.PathEscape(id) }
