// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// hookclient.go is the HOOK COMMAND half: the process Codex actually launches.
//
// Codex hands the command the hook JSON on stdin and reads the decision from stdout. This
// is that command's reusable core: it forwards the payload to the control plane over
// loopback HTTP and relays the returned decision verbatim.
//
// It is DENY-CLOSED end to end. Endpoint unset, unreachable, non-2xx, unreadable body —
// every one of them emits a deny that this connector renders itself, in the shape THAT
// EVENT honors. A control plane that is down must block the agent, not become invisible.
//
// Two properties are measured facts about Codex, not conventions:
//
//   - The verdict travels in STDOUT, not in the exit code, for every event whose shape we
//     have verified. The exit code stays 0 there.
//   - For an event we do NOT know, the stdout shape is a guess, so the exit code carries
//     the veto too: exit 2 with the reason on stderr, which Codex documents as blocking
//     and which it explicitly complains about when the reason is missing.

// ClientConfig is the hook command's configuration, supplied by the environment the
// managed hooks.json sets. Token is the agent's PEP credential; it travels only in the
// loopback Authorization header and is never written to stdout or stderr.
type ClientConfig struct {
	Endpoint string
	Token    string
	Tenant   string
	Agent    string
	Org      string
	Account  string
	Timeout  time.Duration
	Client   *http.Client
}

// ClientResult is what the calling process should do: write Stdout, write Stderr, exit
// with ExitCode. It is returned rather than performed so the whole path is testable
// without a process boundary.
type ClientResult struct {
	Stdout   []byte
	Stderr   string
	ExitCode int
}

// RunClient reads a Codex hook payload from in, forwards it to the governed PEP and
// returns what to emit. It never returns an error: there is no failure mode in which the
// right answer is to write nothing, because Codex reads an empty stdout as "no objection".
func RunClient(ctx context.Context, in io.Reader, cfg ClientConfig) ClientResult {
	body, _ := io.ReadAll(io.LimitReader(in, maxHookBody))
	event := EventNameOf(body)

	denyClosed := func(reason string) ClientResult {
		out := DenyClosed(event, reason)
		res := ClientResult{Stdout: out}
		if code := ExitCodeFor(event, Decision{Verdict: VerdictDeny, Reason: reason}); code != 0 {
			res.ExitCode = code
			// Codex warns when a hook exits 2 without a reason on stderr, so the reason
			// always accompanies the code.
			res.Stderr = reason
		}
		return res
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return denyClosed("governed endpoint not configured (deny-closed)")
	}

	client := cfg.Client
	if client == nil {
		to := cfg.Timeout
		if to <= 0 {
			to = 5 * time.Second
		}
		client = &http.Client{Timeout: to}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return denyClosed("could not build the governed request (deny-closed)")
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	setIf(req, hdrTenant, cfg.Tenant)
	setIf(req, hdrAgent, cfg.Agent)
	setIf(req, hdrOrg, cfg.Org)
	setIf(req, hdrAccount, cfg.Account)

	resp, err := client.Do(req)
	if err != nil {
		return denyClosed("governed endpoint unreachable (deny-closed)")
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, rerr := io.ReadAll(io.LimitReader(resp.Body, maxHookBody))
	if rerr != nil {
		return denyClosed("could not read the governed response (deny-closed)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return denyClosed("governed endpoint returned an error (deny-closed)")
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		// An empty body is the one response that would be read as consent. It is treated
		// as a fault, not as an allow.
		return denyClosed("governed endpoint returned an empty decision (deny-closed)")
	}
	res := ClientResult{Stdout: respBody}
	// The governed answer for an UNKNOWN event travels on both channels, not just stdout.
	//
	// The server denies an unknown event (it never reaches the decider), but the client
	// used to relay that body and exit 0 — so the second channel was only ever applied to
	// the client's OWN deny-closed, never to the server's. For an event with no verified
	// output schema the stdout shape is a guess, which is precisely when the exit code is
	// worth having. The client knows the event name without trusting the response, so it
	// applies the rule itself.
	if !IsKnownEvent(event) {
		res.ExitCode = 2
		res.Stderr = "unknown hook event: governance denied it and this build has no verified output shape for it"
	}
	return res
}

func setIf(r *http.Request, key, val string) {
	if v := strings.TrimSpace(val); v != "" {
		r.Header.Set(key, v)
	}
}
