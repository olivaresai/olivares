// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	jose "github.com/go-jose/go-jose/v4"
)

// A2A v1.0 defines three equivalent transport bindings over one canonical
// Protocol-Buffers model (spec §5.1 "all supported protocols MUST ... return
// semantically equivalent results"): JSON-RPC 2.0 (§9), gRPC (§10), HTTP+JSON/REST
// (§11). For READ-ONLY observation this connector needs only the lowest-risk,
// highest-leverage surface — fetching the (cacheable, public) Agent Card over
// HTTP+JSON. The richer surfaces are RECOGNIZED but staged, not claimed as built:
//
//   - gRPC / JSON-RPC bindings: recognized as equivalent transports; a future stage
//     can add them behind this same fetch seam without changing the discovery /
//     verification / edge logic (it operates on the canonical card, transport-agnostic).
//   - SSE streaming + push notifications: A2A push notifications let an agent POST
//     task updates to a webhook. A passive push-notification RECEIVER is the honest
//     evolution of Task-lifecycle observation (it observes TASK_STATE_* transitions
//     without sitting in any request data path); today such updates are supplied as
//     observed `interactions` (config). This is the streaming seam.
//
// The connector ACTIVELY uses only HTTP+JSON (card fetch); gRPC/JSON-RPC and the
// push-notification receiver are documented, not built (see doc.go / the contract).

// maxCardBody caps an Agent Card response so a hostile/runaway endpoint cannot
// exhaust memory.
const maxCardBody = 4 << 20 // 4 MiB

// acceptCardMedia is the Accept header for Agent Card document fetches: the v1.0.1
// A2A media type preferred, plain JSON tolerated (the card document's content type
// is unspecified in v1.0.1 — see fetchCardHTTP).
const acceptCardMedia = "application/a2a+json, application/json"

// fetchCardHTTP fetches an Agent Card over the HTTP+JSON binding (read-only GET).
// It applies the spec's optional headers and bounds the body. It is the default
// fetcher; tests inject a fake via Source.fetch.
func (s *Source) fetchCardHTTP(ctx context.Context, spec agentSpec) ([]byte, error) {
	u := cardURL(spec, s.cfg.wellKnownPath)
	if u == "" {
		return nil, fmt.Errorf("a2a: agent %q has no url to discover a card", spec.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// The card document's content type is not normatively fixed in v1.0.1 (the
	// registered application/a2a+json media type is scoped to the HTTP+JSON binding,
	// §14.1.1, and the extended-card example uses it) — accept both, preferring the
	// A2A media type.
	req.Header.Set("Accept", acceptCardMedia)
	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: fetch card: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCardBody))
		return nil, fmt.Errorf("a2a: card http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxCardBody))
}

// fetchJKU resolves a JWK Set from a card-supplied jku URL (the self-asserted,
// lower-trust verification path, enabled only when allow_jku_fetch is set). The URL
// is CARD-SUPPLIED (untrusted input), so the fetch is hardened: HTTPS is mandatory
// (RFC 7515 §4.1.2 — the JWKS retrieval MUST use TLS with server identity validated;
// A2A v1.0 §8.4.3 only adds SHOULD-level "secure channels") and the dial goes through
// the SSRF-guarded client (no reserved/private ranges, re-checked at dial time) so a
// hostile card cannot point the verifier at internal metadata endpoints. It is a
// read-only GET bounded by the same body cap; the caller still labels the outcome
// "self-asserted" because the key is vouched for by the card itself, not the operator.
func (s *Source) fetchJKU(ctx context.Context, jkuURL string) (*jose.JSONWebKeySet, error) {
	if err := requireHTTPS(jkuURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jkuURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.jkuHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: fetch jku: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCardBody))
		return nil, fmt.Errorf("a2a: jku http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBody))
	if err != nil {
		return nil, err
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("a2a: parse jku jwks: %w", err)
	}
	return &set, nil
}
