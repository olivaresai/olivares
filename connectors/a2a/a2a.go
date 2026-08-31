// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Source is the A2A observation SourceConnector. It is a batch source: each Gather
// discovers + verifies every configured agent's card once and turns observed
// interactions into edges, then returns; the engine owns re-scheduling.
type Source struct {
	cfg        config
	httpClient *http.Client
	// jkuHTTP is the SSRF-guarded client jku fetches go through (the jku URL is
	// card-supplied, i.e. untrusted input — see fetchJKU). Built in Open when
	// allow_jku_fetch is enabled; tests may inject their own.
	jkuHTTP httpGetter
	// fetch retrieves an Agent Card for a spec. Defaults to the HTTP+JSON binding;
	// tests inject a fake. resolveJKU is the self-asserted jku fallback, non-nil only
	// when allow_jku_fetch is enabled.
	fetch      func(ctx context.Context, spec agentSpec) ([]byte, error)
	resolveJKU jkuResolver
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an A2A connector; agents/interactions are supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves configuration and builds the HTTP client. A configuration error
// surfaces here, before Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.httpClient = &http.Client{Timeout: c.timeout}
	if s.fetch == nil {
		s.fetch = s.fetchCardHTTP
	}
	if c.allowJKU {
		if s.jkuHTTP == nil {
			s.jkuHTTP = pushSSRFClient()
		}
		s.resolveJKU = s.fetchJKU
	}
	return nil
}

// Gather runs the connector once: (1) discover + verify each agent's card, emitting
// trust + security-scheme findings; (2) turn observed interactions into agent↔agent
// edges (with confidence reflecting the peer's verified trust) and Task-lifecycle
// findings. It returns nil on completion and ctx.Err() if canceled mid-pass.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()
	trust := make(map[string]trustLevel, len(s.cfg.agents))

	// Stage 1: Agent Card discovery + JWS/JCS signature verification (read-only).
	for _, spec := range s.cfg.agents {
		if err := ctx.Err(); err != nil {
			return err
		}
		lvl, detail, card, err := s.discoverAndVerify(ctx, spec)
		if err != nil {
			if emitErr := sink.Emit(ctx, discoveryFailedFinding(spec.Name, err, at)); emitErr != nil {
				return emitErr
			}
			continue
		}
		trust[spec.Name] = lvl
		for _, f := range agentFindings(spec.Name, card, lvl, detail, at) {
			if emitErr := sink.Emit(ctx, f); emitErr != nil {
				return emitErr
			}
		}
	}

	// Stage 2: observed Task/message interactions → agent↔agent edges + lifecycle.
	for _, it := range s.cfg.interactions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if it.From == "" || it.To == "" {
			continue // nothing nameable to connect — honest skip
		}
		if emitErr := sink.Emit(ctx, interactionEdge(it, trust[it.To], at)); emitErr != nil {
			return emitErr
		}
		if f, ok := taskStateFinding(it, at); ok {
			if emitErr := sink.Emit(ctx, f); emitErr != nil {
				return emitErr
			}
		}
	}
	return nil
}

// Close releases resources; the connector holds none between runs.
func (s *Source) Close(context.Context) error { return nil }

// discoverAndVerify fetches and verifies one agent's card under the per-card
// timeout, returning the trust level, a non-sensitive detail, and the parsed card.
func (s *Source) discoverAndVerify(ctx context.Context, spec agentSpec) (trustLevel, string, AgentCard, error) {
	fctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()

	data, err := s.fetch(fctx, spec)
	if err != nil {
		return "", "", AgentCard{}, err
	}
	rc, err := parseCard(data)
	if err != nil {
		return "", "", AgentCard{}, err
	}
	anchor, err := parseJWKS(spec.TrustJWKS)
	if err != nil {
		return "", "", AgentCard{}, err
	}
	lvl, detail := verifyCard(ctx, rc, anchor, s.resolveJKU)
	return lvl, detail, rc.card, nil
}
