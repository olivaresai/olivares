// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Endpoint      string         // governed PEP URL (e.g. http://127.0.0.1:8447/)
	Token         string         // bearer credential (the agent's PEP token)
	Tenant        string         // X-Olivares-Hook-Tenant
	Agent         string         // X-Olivares-Hook-Agent
	Org           string         // X-Olivares-Hook-Org
	Account       string         // X-Olivares-Hook-Account
	Timeout       time.Duration  // whole helper deadline, including stdin and paths (default 5s)
	StartedAt     time.Time      // optional invocation start; zero starts at RunHookClient entry
	ExpectedEvent string         // invocation event pinned by protected settings; rendering only
	Client        *http.Client   // optional override (tests inject)
	pathCalls     *hookPathCalls // private filesystem seam; nil uses the agent host
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
	to := cfg.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	started := cfg.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	ctx, cancel := context.WithDeadline(ctx, started.Add(to))
	defer cancel()
	event := hookPreToolUse
	if _, known := hookSpecs[cfg.ExpectedEvent]; known {
		event = cfg.ExpectedEvent
	}
	timedOut := func() error {
		_, err := out.Write(denyClosedDecision(event, "governed PEP decision timed out; tool-call denied"))
		return err
	}
	if ctx.Err() != nil {
		return timedOut()
	}
	// Stdin and host filesystem syscalls do not support cancellation. They stay
	// off the sole output path: a stalled worker cannot hold or later overwrite
	// the explicit deadline verdict. The standalone helper exits after this call.
	events := make(chan string, 1)
	type result struct {
		body []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var buffered bytes.Buffer
		err := runHookClientRequest(ctx, in, &buffered, cfg, events)
		done <- result{body: bytes.Clone(buffered.Bytes()), err: err}
	}()
	readEvent := func() {
		select {
		case event = <-events:
		default:
		}
	}
	select {
	case reply := <-done:
		readEvent()
		// Deadline wins even if completion and cancellation become ready together.
		if ctx.Err() != nil {
			return timedOut()
		}
		if reply.err != nil {
			return reply.err
		}
		_, err := out.Write(reply.body)
		return err
	case <-ctx.Done():
		readEvent()
		return timedOut()
	}
}

func runHookClientRequest(ctx context.Context, in io.Reader, out io.Writer, cfg HookClientConfig, events chan<- string) error {
	body, readErr := io.ReadAll(io.LimitReader(in, maxClientBody+1))
	incompleteInput := readErr != nil || len(body) > maxClientBody
	event := eventOf(body)
	refusalEvent := event
	expected, known := hookSpecs[cfg.ExpectedEvent]
	if known && expected.enforceable {
		// A parsed neutral event cannot neutralize the registered gate's refusal.
		refusalEvent = cfg.ExpectedEvent
	} else if cfg.ExpectedEvent != "" && !known && !hookSpecFor(event).enforceable {
		// A malformed rendering hint never grants a neutral timeout response.
		refusalEvent = hookPreToolUse
	}
	events <- refusalEvent

	denyClosed := func(reason string) error {
		_, err := out.Write(denyClosedDecision(refusalEvent, reason))
		return err
	}
	if cfg.ExpectedEvent != "" && (!known || cfg.ExpectedEvent != event) {
		return denyClosed("hook input did not match its configured event (deny-closed)")
	}
	if incompleteInput {
		return denyClosed("could not read complete hook input (deny-closed)")
	}
	if ctx.Err() != nil {
		return denyClosed("governed PEP decision timed out; tool-call denied")
	}
	// Canonicalize on the agent host, keeping the original best-effort behavior.
	calls := hostHookPathCalls
	if cfg.pathCalls != nil {
		calls = *cfg.pathCalls
	}
	body = canonicalizeHookPayloadPathsWith(ctx, body, calls)
	// A recovered filesystem must not dispatch a request after the helper denied.
	if ctx.Err() != nil {
		return denyClosed("governed PEP decision timed out; tool-call denied")
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
		if errors.Is(err, context.DeadlineExceeded) {
			return denyClosed("governed PEP decision timed out; tool-call denied")
		}
		return denyClosed("governed PEP unreachable (deny-closed)")
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxClientBody+1))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		diag(cfg, "governed PEP returned HTTP %d", resp.StatusCode)
		return denyClosed("governed PEP returned an error (deny-closed)")
	}
	if readErr != nil || len(respBody) > maxClientBody || !completeHookDecision(event, respBody) {
		return denyClosed("governed PEP returned an incomplete decision (deny-closed)")
	}
	if ctx.Err() != nil {
		return denyClosed("governed PEP decision timed out; tool-call denied")
	}
	_, werr := out.Write(respBody)
	return werr
}

// A successful transport is insufficient: an empty/truncated response to a
// permission gate is ignored by Claude Code. Require the event's explicit
// permission schema before relaying it. Other event mechanisms allow neutral {}.
func completeHookDecision(event string, body []byte) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return false
	}
	mech := hookMechFor(event)
	if mech != mechPermissionDecision && mech != mechPermissionBehavior {
		return true
	}
	var output struct {
		Event      string `json:"hookEventName"`
		Permission string `json:"permissionDecision"`
		Decision   struct {
			Behavior string `json:"behavior"`
		} `json:"decision"`
	}
	if json.Unmarshal(object["hookSpecificOutput"], &output) != nil || output.Event != event {
		return false
	}
	if mech == mechPermissionBehavior {
		return output.Decision.Behavior == permAllow || output.Decision.Behavior == permDeny
	}
	return permissionValueValid(event, output.Permission)
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
