// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// hookclient.go is the managed HOOK COMMAND half of the governed PEP. Claude Code
// runs a configured command on PreToolUse/PostToolUse, handing it the hook JSON on stdin
// and reading the decision from stdout. This is that command's reusable core: it forwards
// the payload to the control plane's governed PEP endpoint over loopback HTTP and relays
// the returned decision.
//
// It is DENY-CLOSED end to end (docs/SECURITY-HARDENING.md): if the endpoint is unset, unreachable, or
// answers a non-2xx, the client emits a DENY decision itself (a PostToolUse failure
// blocks post-processing), so a down or misconfigured control plane BLOCKS the agent's
// tool-call rather than silently letting it proceed. It writes the decision to stdout and
// returns nil even on a governed deny — the verdict travels in the body, not the exit
// code, which is how Claude Code consumes a hook decision.

// maxClientBody bounds both the inbound hook payload and the endpoint's response.
const maxClientBody = 1 << 20

// HookClientConfig is the managed hook command's configuration, supplied by the
// environment the managed-settings hook block sets. Token is the agent's PEP credential
// (the bearer the endpoint resolves to a firm principal); it is sent only over the
// loopback Authorization header and never written to stdout/stderr.
type HookClientConfig struct {
	Endpoint string        // governed PEP URL (e.g. http://127.0.0.1:8447/)
	Token    string        // bearer credential (the agent's PEP token)
	Tenant   string        // X-Olivares-Hook-Tenant
	Agent    string        // X-Olivares-Hook-Agent
	Org      string        // X-Olivares-Hook-Org
	Account  string        // X-Olivares-Hook-Account
	Timeout  time.Duration // request timeout (default 5s)
	Client   *http.Client  // optional override (tests inject)
	// Diag receives the CAUSE of a deny-closed, for the operator. It never receives the
	// decision itself: that goes to `out` as the hookSpecificOutput the agent enforces, and
	// its reason string is a CONTRACT with Claude Code — widening it to carry transport
	// detail would leak engine internals to the agent, which this design exists to prevent
	// («the agent sees only a localhost decision endpoint»). Nil is fine and silent.
	//
	// ⛔ POR QUÉ EXISTE. Medido el 2026-08-19: el error de `client.Do` se DESCARTABA entero, así
	// que apuntar el hook a un endpoint https con certificado privado devolvía «governed PEP
	// unreachable (deny-closed)» y NADA más — ni en stdout, ni en stderr, ni en ningún log. El
	// deny-closed es correcto; perder la causa no. Un operador veía «inalcanzable» sin poder
	// distinguir un puerto cerrado de un certificado que no verifica, y el remedio de cada uno
	// es distinto.
	Diag io.Writer
}

// diag writes the CAUSE of a deny-closed where the operator can see it, and never where the
// agent can. A nil sink is silent, which keeps every existing caller and test unchanged.
func diag(cfg HookClientConfig, format string, args ...any) {
	if cfg.Diag == nil {
		return
	}
	_, _ = fmt.Fprintf(cfg.Diag, "olivares hook: "+format+"\n", args...)
}

// RunHookClient reads a Claude Code hook payload from in, forwards it to the governed PEP,
// and writes the returned decision to out. It is deny-closed on every failure path. It
// returns an error only if writing the decision to out fails.
func RunHookClient(ctx context.Context, in io.Reader, out io.Writer, cfg HookClientConfig) error {
	body, _ := io.ReadAll(io.LimitReader(in, maxClientBody))
	// canonicalize symlinked file paths on the AGENT host (the only place that sees the
	// real filesystem) so the governed decision is made against the real target, closing a
	// symlink-escape of a path/subtree deny. Best-effort: unresolved paths are forwarded as-is.
	body = canonicalizeHookPayloadPaths(body)
	event := eventOf(body)

	denyClosed := func(reason string) error {
		_, err := out.Write(denyClosedDecision(event, reason))
		return err
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return denyClosed("governed PEP endpoint not configured (deny-closed)")
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
		diag(cfg, "could not build the governed PEP request for %q: %v", endpoint, err)
		return denyClosed("could not build governed PEP request (deny-closed)")
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	setHeaderIf(req, hdrHookTenant, cfg.Tenant)
	setHeaderIf(req, hdrHookAgent, cfg.Agent)
	setHeaderIf(req, hdrHookOrg, cfg.Org)
	setHeaderIf(req, hdrHookAccount, cfg.Account)

	resp, err := client.Do(req)
	if err != nil {
		diag(cfg, "governed PEP unreachable: %v", err)
		return denyClosed("governed PEP unreachable (deny-closed)")
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxClientBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		diag(cfg, "governed PEP returned HTTP %d", resp.StatusCode)
		return denyClosed("governed PEP returned an error (deny-closed)")
	}
	_, werr := out.Write(respBody)
	return werr
}

// denyClosedDecision renders the deny verdict the client emits when it cannot obtain a
// governed decision. It routes through renderHookDecision (the SAME taxonomy the PEP's
// render uses), so the fail-closed deny is in the shape EACH event honors — a top-level
// block, continue:false, a permissionDecision/decision.behavior deny, or a NEUTRAL answer
// for the non-gating + inverted Stop/SubagentStop events (a synthetic block there would
// keep the agent running, the opposite of fail-safe). An unknown event falls to the
// permission-deny schema (eventOf defaults to PreToolUse): the neutral set never widens.
func denyClosedDecision(event, reason string) []byte {
	return renderHookDecision(event, HookDecisionResult{Permission: permDeny, Reason: reason})
}

// eventOf reads the hook_event_name from a payload, defaulting to PreToolUse so a
// deny-closed answer to an unparsable payload still uses the permission-decision schema.
func eventOf(body []byte) string {
	var p struct {
		HookEventName string `json:"hook_event_name"`
	}
	if json.Unmarshal(body, &p) == nil && p.HookEventName != "" {
		return p.HookEventName
	}
	return hookPreToolUse
}

func setHeaderIf(r *http.Request, key, val string) {
	if v := strings.TrimSpace(val); v != "" {
		r.Header.Set(key, v)
	}
}
