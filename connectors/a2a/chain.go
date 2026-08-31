// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

// chain.go governs MULTI-AGENT delegation: the lineage of agents a single
// multi-agent task has been delegated through. Governs each delegation in
// isolation (verify card → capability → allowlist → plan → gate). But a multi-agent
// task is a CHAIN — A delegates to B, B (fronted by this control plane) delegates to C —
// and a chain has two failure modes a per-hop PEP cannot see:
//
//   - UNBOUNDED DEPTH: a runaway fan-out where agents keep delegating onward with no
//     ceiling (the cascading-delegation / autonomous-loop risk; OWASP ASI Tool Misuse).
//   - CYCLES: A → B → A, an agent delegating (transitively) back to one already in the
//     lineage — an infinite-work loop and a privilege-laundering vector.
//
// This file makes the lineage a first-class, governed value: an inbound DelegationChain
// (set by the composition root from the inbound request) is enforced against a
// ChainPolicy (max depth + no cycles, DENY-CLOSED) BEFORE a delegation is authorized,
// and the outbound chain (inbound + this hop) is propagated to the remote agent out-of-
// band so a re-entrant Olivares plane downstream keeps the same bounded lineage.
//
// HONESTY (docs/SECURITY-HARDENING.md anti-evasion): this enforces the lineage THIS plane constructs and
// propagates. A third-party remote agent that re-delegates OUTSIDE an Olivares plane is
// not visible to us — we cannot bound what we cannot see. What we DO control — every
// delegation that leaves THIS Delegator, and every chain a cooperating Olivares plane
// forwards — is bounded and cycle-free. MINIMAL DATA (docs/SECURITY-HARDENING.md): a chain carries agent
// NAME references and a correlation root only — never a payload, prompt or credential.

// delegationPathHeader is the out-of-band HTTP header the Delegator propagates the
// (updated) delegation chain on, alongside W3C `traceparent`. It is an Olivares
// extension (namespaced), NOT an A2A spec field — A2A v1.0 deliberately defines NO
// in-protocol principal/delegation propagation ("Identity information is handled at
// the protocol layer, not within A2A semantics", §7) — so a non-Olivares peer
// ignores it (no harm) and a cooperating Olivares plane decodes it to keep the
// lineage bounded. The value is base64url(JSON) of an RFC 8693-shaped claims object
// (see DelegationChain.encode) — never a credential, never a token.
//
// STANDARD CLAIMS SHAPE. The JSON carried here uses the RFC 8693 §4.1
// delegation vocabulary so the lineage is portable to real token-exchange flows:
//
//	{
//	  "sub": "<original principal>",            // stays the ORIGINAL principal across hops
//	  "act": {"sub": "<current actor>",         // outermost act = most recent actor
//	          "act": {"sub": "<prior actor>"}}, // nesting = older actors (least recent deepest)
//	  "olivares.root": "<correlation id>"       // private claim (RFC 7519 §4.3 collision-resistant)
//	}
//
// SECURITY SEMANTICS (unchanged from now with the RFC's own rule attached):
// this header is UNSIGNED transport metadata, so it is consumed DENY-ONLY — depth
// and cycle limits can only REFUSE a delegation, never authorize one — and per RFC
// 8693 §4.1 nested prior actors are "informational only and are not to be considered
// in access control decisions": the PEP's authorization stays the verified card +
// allowlist + ApprovalGate; the chain feeds lineage governance and audit. A forged
// header can only shrink what a downstream plane permits. Per-hop authorization is
// re-established at every hop (verify → capability → allowlist → plan → gate),
// matching draft-ietf-wimse-arch §3.3.9's "each hop in the chain MUST explicitly
// scope and re-bind the security context".
const delegationPathHeader = "X-Olivares-A2A-Delegation-Path"

// chainRootClaim is the private claim carrying the correlation root (a namespaced,
// collision-resistant name per RFC 7519 §4.2/§4.3 — RFC 8693 defines no
// correlation claim).
const chainRootClaim = "olivares.root"

// defaultMaxDepth bounds a delegation lineage when the operator sets no explicit depth.
// It is a SAFE default, never "unbounded": a ChainPolicy with MaxDepth<=0 resolves to
// this, so a Delegator can never be constructed with no depth ceiling.
const defaultMaxDepth = 8

// maxChainHops caps how many hops a DECODED inbound chain may carry, so a hostile or
// buggy caller cannot inject an enormous lineage to exhaust memory or evade the depth
// check by overflow. A decoded chain longer than this is rejected (treated as absent).
const maxChainHops = 64

// maxChainHeaderLen caps the encoded header length accepted on decode (defense against a
// runaway header). It comfortably exceeds maxChainHops agent references.
const maxChainHeaderLen = 8 << 10 // 8 KiB

// DelegationChain is the lineage of a multi-agent task: the ordered agent-name hops it
// has been delegated through, plus a correlation Root (the originating task/request id)
// and the ORIGINAL Principal the task is being performed on behalf of (RFC 8693
// delegation semantics: the top-level subject stays the original principal across
// every hop; the actors change). Hops[0] is the origin agent; Hops[len-1] is the
// immediate caller about to delegate onward. It is minimal data — references only,
// never a credential or payload.
type DelegationChain struct {
	// Root correlates every hop of one multi-agent task (an opaque id; never a payload).
	Root string
	// Principal is the original on-behalf-of principal reference (the RFC 8693 `sub`
	// position). Seeded from the root delegation's RequestedBy; "" when unknown — a
	// missing principal is reported as unknown, never fabricated from a mid-chain hop.
	Principal string
	// Hops is the ordered lineage of agent name references already in the chain
	// (oldest first — the RFC 8693 act nesting in reverse).
	Hops []string
}

// Depth is the number of hops already in the lineage.
func (c DelegationChain) Depth() int { return len(c.Hops) }

// Contains reports whether agent already appears in the lineage (the cycle test). The
// comparison is trim-normalized; a blank agent never matches.
func (c DelegationChain) Contains(agent string) bool {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return false
	}
	for _, h := range c.Hops {
		if strings.TrimSpace(h) == agent {
			return true
		}
	}
	return false
}

// appendHop returns a new chain with agent appended (the inbound caller's view of the
// outbound lineage). It copies the hops so the inbound chain is never mutated. A blank
// Root is left blank (the composition root may seed it); the connector never fabricates
// one (a fabricated root would falsely correlate unrelated tasks). The Principal is
// carried through unchanged — delegation never rewrites who the work is FOR.
func (c DelegationChain) appendHop(agent string) DelegationChain {
	hops := make([]string, len(c.Hops), len(c.Hops)+1)
	copy(hops, c.Hops)
	if a := strings.TrimSpace(agent); a != "" {
		hops = append(hops, a)
	}
	return DelegationChain{Root: c.Root, Principal: c.Principal, Hops: hops}
}

// empty reports whether the chain carries nothing to propagate.
func (c DelegationChain) empty() bool {
	return c.Root == "" && c.Principal == "" && len(c.Hops) == 0
}

// encode serializes the chain as the base64url(JSON) header value, in the RFC 8693
// claims shape (see delegationPathHeader): top-level `sub` = original principal,
// nested `act` objects = actors with the MOST RECENT outermost (RFC 8693 §4.1: "The
// outermost `act` claim represents the current actor while nested `act` claims
// represent prior actors. The least recent actor is the most deeply nested."), plus
// the private `olivares.root` correlation claim. An empty chain encodes to "" so no
// header is set.
func (c DelegationChain) encode() string {
	if c.empty() {
		return ""
	}
	claims := map[string]any{}
	if c.Principal != "" {
		claims["sub"] = c.Principal
	}
	if c.Root != "" {
		claims[chainRootClaim] = c.Root
	}
	// Build the act nesting: Hops is oldest-first, the act chain is newest-outermost.
	var act map[string]any
	for _, hop := range c.Hops {
		inner := act
		act = map[string]any{"sub": hop}
		if inner != nil {
			act["act"] = inner
		}
	}
	if act != nil {
		claims["act"] = act
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "" // never propagate a malformed chain
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeChain parses an inbound delegation-path header value into a DelegationChain.
// It accepts the RFC 8693 claims shape (sub / nested act / olivares.root) and, for a
// not-yet-upgraded cooperating plane, the legacy {root, hops} shape. It is
// DEFENSIVE: an over-long header, undecodable base64/JSON, an act nesting deeper
// than maxChainHops, or a legacy chain with too many hops yields an empty chain (the
// inbound lineage is treated as absent — a fresh root), never an error that blocks
// the request. Blank hops are dropped.
func decodeChain(headerValue string) DelegationChain {
	v := strings.TrimSpace(headerValue)
	if v == "" || len(v) > maxChainHeaderLen {
		return DelegationChain{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return DelegationChain{}
	}
	var wire struct {
		// RFC 8693 claims shape.
		Sub  string          `json:"sub"`
		Act  json.RawMessage `json:"act"`
		Corr string          `json:"olivares.root"`
		// Legacy shape.
		Root string   `json:"root"`
		Hops []string `json:"hops"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return DelegationChain{}
	}
	// Claims shape wins when any of its members is present.
	if wire.Sub != "" || len(wire.Act) > 0 || wire.Corr != "" {
		hops, ok := decodeActChain(wire.Act)
		if !ok {
			return DelegationChain{}
		}
		return DelegationChain{
			Root:      strings.TrimSpace(wire.Corr),
			Principal: strings.TrimSpace(wire.Sub),
			Hops:      hops,
		}
	}
	if len(wire.Hops) > maxChainHops {
		return DelegationChain{}
	}
	clean := make([]string, 0, len(wire.Hops))
	for _, h := range wire.Hops {
		if h = strings.TrimSpace(h); h != "" {
			clean = append(clean, h)
		}
	}
	return DelegationChain{Root: strings.TrimSpace(wire.Root), Hops: clean}
}

// decodeActChain unwinds a nested RFC 8693 act object into oldest-first hops. The
// nesting is bounded by maxChainHops (a deeper one is rejected — ok=false — so a
// hostile header cannot exhaust the decoder or smuggle an unbounded lineage); blank
// subs are dropped; a malformed act member rejects the whole chain (deny-safe: the
// caller treats it as absent, and an absent lineage can only make the depth check
// STRICTER for this plane's own hops, never authorize anything).
func decodeActChain(raw json.RawMessage) (hops []string, ok bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var newestFirst []string
	for depth := 0; len(raw) > 0; depth++ {
		if depth >= maxChainHops {
			return nil, false
		}
		var act struct {
			Sub string          `json:"sub"`
			Act json.RawMessage `json:"act"`
		}
		if err := json.Unmarshal(raw, &act); err != nil {
			return nil, false
		}
		if s := strings.TrimSpace(act.Sub); s != "" {
			newestFirst = append(newestFirst, s)
		}
		raw = act.Act
	}
	// Reverse: the outermost act is the most recent actor; Hops is oldest-first.
	hops = make([]string, 0, len(newestFirst))
	for i := len(newestFirst) - 1; i >= 0; i-- {
		hops = append(hops, newestFirst[i])
	}
	return hops, true
}

// ChainPolicy bounds a multi-agent delegation lineage. MaxDepth is the maximum number of
// hops a chain may already carry before a further delegation is refused; MaxDepth<=0
// resolves to defaultMaxDepth (never unbounded). Cycle rejection is always on (it is not
// an opt-in — a cycle is never legitimate).
type ChainPolicy struct {
	MaxDepth int
}

// maxDepth resolves the effective ceiling (the safe default when unset).
func (p ChainPolicy) maxDepth() int {
	if p.MaxDepth <= 0 {
		return defaultMaxDepth
	}
	return p.MaxDepth
}

// ChainError is the typed refusal returned when a delegation would violate the multi-
// agent ChainPolicy — exceeding the depth ceiling, or closing a cycle back onto an agent
// already in the lineage. Like DenyError / CapabilityError it is a POLICY refusal (deny-
// closed), never a transport error: nothing is emitted.
type ChainError struct {
	Reason string
	Agent  string
	Depth  int
}

func (e *ChainError) Error() string {
	if a := strings.TrimSpace(e.Agent); a != "" {
		return "a2a: delegation chain denied for agent " + a + " (depth " + strconv.Itoa(e.Depth) + "): " + e.Reason
	}
	return "a2a: delegation chain denied (depth " + strconv.Itoa(e.Depth) + "): " + e.Reason
}

// enforceChain validates a prospective delegation to target against the inbound lineage
// and the policy, returning the OUTBOUND chain (inbound + target) to propagate when it
// passes. It is DENY-CLOSED on both invariants:
//
//   - CYCLE: target already in the inbound lineage → refuse (A → B → A is never valid).
//   - DEPTH: the inbound lineage already at/over the ceiling → refuse (a further hop
//     would exceed it).
//
// The cycle check runs first so a 1-hop cycle is reported as a cycle, not a depth breach.
func enforceChain(policy ChainPolicy, inbound DelegationChain, target string) (DelegationChain, error) {
	target = strings.TrimSpace(target)
	if inbound.Contains(target) {
		return DelegationChain{}, &ChainError{
			Agent:  target,
			Depth:  inbound.Depth(),
			Reason: "target agent is already in the delegation lineage (cycle)",
		}
	}
	if inbound.Depth() >= policy.maxDepth() {
		return DelegationChain{}, &ChainError{
			Agent:  target,
			Depth:  inbound.Depth(),
			Reason: "delegation lineage has reached the maximum depth",
		}
	}
	return inbound.appendHop(target), nil
}

// --- chain context propagation --------------------------------------------------

type chainKey struct{}

// withChain stashes the (outbound) delegation chain on the context so doRPC / openStream
// propagate it to the remote agent as the delegation-path header. An empty chain is a
// no-op (no header set).
func withChain(ctx context.Context, chain DelegationChain) context.Context {
	if chain.empty() {
		return ctx
	}
	return context.WithValue(ctx, chainKey{}, chain)
}

// chainFrom returns the propagated outbound delegation chain, or the zero chain if none.
func chainFrom(ctx context.Context) DelegationChain {
	if v, ok := ctx.Value(chainKey{}).(DelegationChain); ok {
		return v
	}
	return DelegationChain{}
}
