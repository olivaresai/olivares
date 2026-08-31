// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"sort"
	"strings"
)

// capability.go binds a delegation to the remote agent's CRYPTOGRAPHICALLY-
// DECLARED capability surface. Verifies a card's SIGNATURE (identity) and the
// operator allowlist authorizes WHICH (agent, skill, scope) tuples a caller may exercise.
// But a verified signature + an allowlist entry is not enough: a signed card is also the
// agent's signed CLAIM of what it can do, and a control plane must not delegate a
// capability the agent does not claim. So between identity verification and the operator
// allowlist, the PEP now also checks the SIGNED card:
//
//   - the requested skill MUST be declared in the card's signed skills[] (by id or name);
//   - a STREAMING delegation MUST land on a card whose signed capabilities advertise
//     streaming (you cannot stream to an agent that does not claim it).
//
// Both are DENY-CLOSED: a skill absent from the signed card, or a streaming delegation to
// a non-streaming card, is refused before the allowlist is even consulted — the signed
// capability surface is the floor, the operator allowlist the ceiling, and a delegation
// needs BOTH. This closes a capability-confusion gap: an operator could allowlist
// (agent, skill=deploy) against a verified agent whose card never declares `deploy`, and
// Would have emitted to a capability the agent does not cryptographically claim.
//
// The check reasons ONLY over the already trust-verified card (verifyCard / verifiedCard,
// emit_task.go) and the request — never over a self-asserted or unverified card. It holds
// no network call and no credential (minimal data, docs/SECURITY-HARDENING.md).

// CapabilityError is the typed refusal returned when a delegation does not match the
// remote agent's SIGNED capability surface — a requested skill not declared in the
// verified card, or a streaming delegation to a card that does not advertise streaming.
// It is distinct from a DenyError (an operator-allowlist / gate policy refusal) so a
// caller, an auditor and a test can tell a "the agent does not claim this capability"
// refusal apart from a "policy does not permit this" refusal. Like DenyError it is a
// POLICY refusal, never a transport error: nothing is emitted.
type CapabilityError struct {
	Reason string
	Agent  string
	Skill  string
}

func (e *CapabilityError) Error() string {
	a := strings.TrimSpace(e.Agent)
	s := strings.TrimSpace(e.Skill)
	switch {
	case a != "" && s != "":
		return "a2a: capability denied for agent " + a + " skill " + s + ": " + e.Reason
	case a != "":
		return "a2a: capability denied for agent " + a + ": " + e.Reason
	default:
		return "a2a: capability denied: " + e.Reason
	}
}

// requireDeclaredSkill enforces that a requested skill is declared in the agent's SIGNED
// card. A blank skill is a skill-less delegation (the agent's general endpoint) and is
// NOT capability-checked here — the allowlist still governs whether a skill-less
// delegation is permitted (scopeAllowed). A non-blank skill that the verified card does
// not declare is refused (deny-closed): the agent does not cryptographically claim it.
func requireDeclaredSkill(card AgentCard, agent, skill string) error {
	if strings.TrimSpace(skill) == "" {
		return nil // skill-less delegation; the allowlist governs it, not the capability surface
	}
	if card.declaresSkill(skill) {
		return nil
	}
	return &CapabilityError{
		Agent:  agent,
		Skill:  skill,
		Reason: "skill is not declared in the agent's signed card (capability not claimed)",
	}
}

// requireStreaming enforces that the agent's SIGNED card advertises the streaming
// capability before a streaming delegation (SendStreamingMessage / SubscribeToTask)
// opens. A card with no capabilities object, or capabilities.streaming=false, is refused
// (deny-closed): you cannot stream to an agent that does not claim streaming, and silently
// downgrading to a unary call would be acting outside the agent's declared surface.
func requireStreaming(card AgentCard, agent string) error {
	if card.Capabilities != nil && card.Capabilities.Streaming {
		return nil
	}
	return &CapabilityError{
		Agent:  agent,
		Reason: "agent's signed card does not advertise the streaming capability",
	}
}

// requirePushNotifications enforces that the agent's SIGNED card advertises the
// pushNotifications capability before any push-config operation (Create/Get/List/
// Delete, spec §3.3.4: a server without it MUST refuse with
// PushNotificationNotSupportedError — the governed client refuses FIRST, deny-closed,
// rather than probing outside the declared surface).
func requirePushNotifications(card AgentCard, agent string) error {
	if card.Capabilities != nil && card.Capabilities.PushNotifications {
		return nil
	}
	return &CapabilityError{
		Agent:  agent,
		Reason: "agent's signed card does not advertise the pushNotifications capability",
	}
}

// requireExtendedCard enforces that the agent's SIGNED card advertises the
// extendedAgentCard capability (the v1.0 rename of the v0.x top-level
// supportsAuthenticatedExtendedCard) before GetExtendedAgentCard is called
// (§3.3.4 — deny-closed, same posture as the other capability gates).
func requireExtendedCard(card AgentCard, agent string) error {
	if card.Capabilities != nil && card.Capabilities.ExtendedAgentCard {
		return nil
	}
	return &CapabilityError{
		Agent:  agent,
		Reason: "agent's signed card does not advertise the extendedAgentCard capability",
	}
}

// requireSecurityScheme enforces two deny-closed gates on a delegation's security
// posture against the agent's SIGNED card (A2A v1.0):
//
//  1. Floor: the card MUST declare at least one recognized SecurityScheme (an agent
//     that exposes no auth surface is not safe to delegate to — deny-closed).
//  2. Binding: when a credential kind is known (from the out-of-band headers used
//     for the delegation), at least one declared scheme kind must be compatible
//     with that credential (e.g. a bearer token matches oauth2/openIdConnect/http;
//     an API key matches apiKey; mTLS matches mutualTLS). A mismatch is a
//     capability-confusion gap and is refused.
//
// An empty credentialKind skips the binding check (floor only). openIdConnect is
// treated as compatible with "oauth2" credentials because it is OAuth2-based.
func requireSecurityScheme(card AgentCard, agent, credentialKind string) error {
	if len(card.SecuritySchemes) == 0 {
		return &CapabilityError{
			Agent:  agent,
			Reason: "agent's signed card declares no security scheme (authentication surface not claimed)",
		}
	}
	// Floor: at least one recognized scheme kind.
	kinds := make(map[string]struct{})
	for _, sc := range card.SecuritySchemes {
		if k := sc.kindLabel(); k != "" {
			kinds[k] = struct{}{}
		}
	}
	if len(kinds) == 0 {
		return &CapabilityError{
			Agent:  agent,
			Reason: "agent's signed card declares no recognized security scheme kind",
		}
	}
	// Binding: credential kind must match at least one declared scheme.
	credentialKind = strings.TrimSpace(credentialKind)
	if credentialKind == "" {
		return nil // no credential to bind — floor passed
	}
	if schemeKindCompatible(kinds, credentialKind) {
		return nil
	}
	declared := make([]string, 0, len(kinds))
	for k := range kinds {
		declared = append(declared, k)
	}
	sort.Strings(declared)
	return &CapabilityError{
		Agent:  agent,
		Reason: "credential kind " + credentialKind + " incompatible with declared security schemes [" + strings.Join(declared, ", ") + "]",
	}
}

// schemeKindCompatible returns true when at least one declared scheme kind is
// compatible with the credential kind used for the delegation. The compatibility
// matrix (A2A v1.0 SecurityScheme oneof → credential type):
//
//	oauth2        ← bearer token (OAuth 2.x)          — exact match or openIdConnect
//	openIdConnect ← bearer token (OIDC is OAuth2-based) — also matches oauth2 credential
//	http          ← basic auth (HTTP Authorization)   — exact match only
//	apiKey        ← API key in header                 — exact match only
//	mutualTLS     ← client certificate                — exact match only
//
// openIdConnect is the only non-trivial cross-match: OIDC is OAuth2-based, so an
// oauth2 (bearer) credential is valid for an openIdConnect scheme. `http` and
// `oauth2` are NOT cross-compatible: http means basic/digest auth, oauth2 means
// bearer — they are distinct credential kinds even if both travel in Authorization.
func schemeKindCompatible(declared map[string]struct{}, credKind string) bool {
	if _, ok := declared[credKind]; ok {
		return true // exact match (apiKey/http/oauth2/openIdConnect/mutualTLS)
	}
	// openIdConnect is OAuth2-based: a bearer (oauth2) credential matches it.
	if credKind == "oauth2" {
		if _, ok := declared["openIdConnect"]; ok {
			return true
		}
	}
	return false
}

// credentialSchemeKind infers the A2A SecurityScheme kind from the out-of-band
// HTTP headers used for a delegation. It returns "" when no auth header is
// present (the floor check still applies; the binding check is skipped).
func credentialSchemeKind(headers map[string]string) string {
	for k, v := range headers {
		kl := strings.ToLower(k)
		if kl == "authorization" {
			vl := strings.ToLower(strings.TrimSpace(v))
			switch {
			case strings.HasPrefix(vl, "bearer "):
				return "oauth2"
			case strings.HasPrefix(vl, "basic "):
				return "http"
			default:
				return "http" // other Authorization schemes (e.g. Digest) map to http
			}
		}
		if kl == "x-api-key" || kl == "api-key" || kl == "apikey" {
			return "apiKey"
		}
	}
	return ""
}
