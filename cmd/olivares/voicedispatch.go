// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	voiceconn "github.com/olivaresai/olivares/connectors/voice"
	voicemod "github.com/olivaresai/olivares/modules/voice"
)

// voicedispatch.go is the XVI↔actuation seam adapter: it implements the voice
// module's Dispatcher port (modules/voice/ports.go) over the provider-agnostic
// connectors/voice backend, so an APPROVED voice open stops being "declared, not
// opened" and actually mints a governed realtime session. Like deployexec.go /
// orchdispatch.go it lives in the composition root (cmd, AGPL) bridging the AGPL
// module port to the Apache voice connector.
//
// The module only reaches Open AFTER its policy match AND its two-phase HITL gate
// approved the open and the plan still matches (policies.go: policy allowed +
// decision.Allowed() && PlanHash==planHash). This adapter MATERIALIZES the session:
//   - it selects the provider adapter by the (already policy-validated) ProviderRef;
//   - it fixes model/instructions/voice/tools/turn-detection FROM the operator policy
//     (the client cannot escalate — the OpenRequest carries only references);
//   - it mints, server-side, an EPHEMERAL session credential and returns ONLY that
//     credential + connect coordinates in OpenResult.Ref. The provider master key and
//     (for Claude-as-think) the BYO Anthropic key NEVER leave the server.
//   - Claude-as-think by default (Deepgram): the reasoning turn routes to Claude's
//     Messages API; Claude is never treated as a realtime audio peer (Anthropic
//     exposes no native realtime/voice API).
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): no audio, no transcript, no master secret crosses this
// seam; the returned Ref is the ephemeral connection bundle only.

// voiceDispatcher mints governed realtime sessions for approved opens. Safe for
// concurrent use after construction.
type voiceDispatcher struct {
	// providers maps a provider_ref (the value that travels in OpenRequest.ProviderRef
	// after policy validation) to its connector adapter.
	providers map[string]voiceconn.Provider
	// think holds the server-side Claude-as-think defaults (endpoint + BYO headers) for
	// providers that carry a brain (Deepgram). The per-session think model comes from
	// the policy; the endpoint/headers (the Anthropic key) come from here.
	think map[string]thinkDefaults
	// policies maps an agent_ref to its governed session settings; fallback applies
	// when an agent has no specific policy (nil => no fallback, unknown agents denied).
	policies map[string]voiceSessionPolicy
	fallback *voiceSessionPolicy
	log      *slog.Logger
}

// thinkDefaults is the operator-provisioned, server-only Claude brain wiring.
type thinkDefaults struct {
	endpointURL string
	headers     map[string]string // x-api-key, anthropic-version — server-held, never returned
}

// voiceSessionPolicy is the resolved, governed configuration for one agent's sessions.
type voiceSessionPolicy struct {
	providerRef  string
	model        string
	thinkModel   string
	voice        string
	instructions string
	language     string
	maxDuration  time.Duration
	turn         voiceconn.TurnDetection
	tools        []voiceconn.Tool
}

var _ voicemod.Dispatcher = (*voiceDispatcher)(nil)

// sessionRefPayload is the ephemeral connection bundle returned to the caller in
// OpenResult.Ref. It carries ONLY what a client needs to connect — never the provider
// master key or the BYO Anthropic key.
type sessionRefPayload struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	SessionID      string `json:"session_id"`
	Transport      string `json:"transport"`
	Connect        string `json:"connect"`
	Credential     string `json:"credential"`
	ExpiresAt      string `json:"expires_at"`
	ServerMediated bool   `json:"server_mediated"`
}

// Open materializes an approved voice open into a minted provider session. It returns
// an explicit error on any failure (the module records op_status=failed/502 and does
// NOT mark the session governed); a real mint returns the ephemeral connection bundle
// as OpenResult.Ref (op_status=dispatched).
func (d *voiceDispatcher) Open(ctx context.Context, req voicemod.OpenRequest) (voicemod.OpenResult, error) {
	// Defense in depth: the module never reaches Open without a bound plan.
	if strings.TrimSpace(req.PlanHash) == "" {
		return voicemod.OpenResult{}, fmt.Errorf("voice dispatch: empty plan_hash; refusing to open (approval binding missing)")
	}

	prov, ok := d.providers[req.ProviderRef]
	if !ok {
		return voicemod.OpenResult{}, fmt.Errorf("voice dispatch: no provider configured for provider_ref %q", req.ProviderRef)
	}
	pol, ok := d.resolvePolicy(req.AgentRef)
	if !ok {
		return voicemod.OpenResult{}, fmt.Errorf("voice dispatch: no session policy for agent %q (deny-closed)", req.AgentRef)
	}

	// Build the governed SessionPolicy ENTIRELY from operator provisioning — the client
	// cannot escalate the model/instructions/voice/tools/turn-detection. The requested
	// model is honored only as a fallback when the operator policy leaves it open.
	sp := voiceconn.SessionPolicy{
		Model:         firstNonEmpty(pol.model, req.ModelRef),
		Voice:         pol.voice,
		Instructions:  pol.instructions,
		Language:      pol.language,
		Tools:         pol.tools,
		TurnDetection: pol.turn,
		MaxDuration:   pol.maxDuration,
	}
	// Claude-as-think: when the provider carries a brain (Deepgram), wire the reasoning
	// turn to Claude's Messages API. The endpoint + BYO Anthropic auth are server-side
	// defaults; the model is governed by the policy (default to the requested/opus id).
	if td, ok := d.think[req.ProviderRef]; ok {
		sp.Think = &voiceconn.ThinkConfig{
			ProviderType: "anthropic",
			Model:        firstNonEmpty(pol.thinkModel, pol.model, req.ModelRef),
			EndpointURL:  td.endpointURL,
			Headers:      td.headers,
		}
	}

	sess, err := prov.MintSession(ctx, sp, principalID(req))
	if err != nil {
		return voicemod.OpenResult{}, fmt.Errorf("voice dispatch: mint %s session failed: %w", req.ProviderRef, err)
	}

	ref, err := encodeSessionRef(sess)
	if err != nil {
		return voicemod.OpenResult{}, fmt.Errorf("voice dispatch: encode session ref: %w", err)
	}
	if d.log != nil {
		d.log.Info("voice: session minted", "provider", sess.Provider, "model", sess.Model, "transport", sess.Transport, "server_mediated", sess.ServerMediated, "expires_at", sess.ExpiresAt.Format(time.RFC3339))
	}
	return voicemod.OpenResult{Ref: ref}, nil
}

// resolvePolicy returns the agent's governed policy, falling back to the default.
func (d *voiceDispatcher) resolvePolicy(agentRef string) (voiceSessionPolicy, bool) {
	if p, ok := d.policies[agentRef]; ok {
		return p, true
	}
	if d.fallback != nil {
		return *d.fallback, true
	}
	return voiceSessionPolicy{}, false
}

// encodeSessionRef serializes the ephemeral connection bundle. It deliberately copies
// only the non-secret session fields — the provider master key and Claude BYO key are
// not on Session and so cannot leak here.
func encodeSessionRef(s voiceconn.Session) (string, error) {
	b, err := json.Marshal(sessionRefPayload{
		Provider:       s.Provider,
		Model:          s.Model,
		SessionID:      s.SessionID,
		Transport:      s.Transport,
		Connect:        s.ConnectCoords,
		Credential:     s.EphemeralCredential,
		ExpiresAt:      s.ExpiresAt.UTC().Format(time.RFC3339),
		ServerMediated: s.ServerMediated,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// principalID derives a pseudonymous, stable per-(tenant,agent,session) identifier for
// the provider's audit/safety identifier (e.g. OpenAI-Safety-Identifier). It is a hash
// — never PII, never the real human principal (which the module audits in its ledger).
func principalID(req voicemod.OpenRequest) string {
	sum := sha256.Sum256([]byte(req.Tenant.String() + "|" + req.AgentRef + "|" + req.SessionRef))
	return hex.EncodeToString(sum[:16])
}

// firstNonEmpty returns the first non-blank string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
