// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package threatfeed

// A rule-pack is the OPEN, self-hostable security-rule channel: a signed,
// versioned bundle of deny-list indicators (IOCs), blocked MCP servers and
// agentic-attack patterns that an operator applies WITHOUT a subscription and
// WITHOUT restarting the engine. It is the OSS counterpart to the enterprise curated
// feed: same idea, but the operator (or any publisher they pin) signs a local
// pack. Signing is Ed25519 with a rule-pack DOMAIN TAG so a pack can never be
// replayed as some other Ed25519-signed artifact (update manifest / license /
// advisory feed all carry different tags). This file has NO engine dependency
// (stdlib only) — a connector must not import core (scripts/check-boundary.sh).

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RulePackSchema is the versioned schema tag. A reader rejects an unknown schema.
const RulePackSchema = 1

// Indicator types (deny-list IOCs).
const (
	IndicatorDomain = "domain"
	IndicatorURL    = "url"
	IndicatorIP     = "ip"
	IndicatorSHA256 = "sha256"
)

// RulePack is a signed, versioned set of security rules applied at runtime.
type RulePack struct {
	// Schema is RulePackSchema.
	Schema int `json:"schema"`
	// Version is a monotonic counter. Apply refuses a pack whose Version is not
	// strictly greater than the active one (anti-rollback).
	Version uint64 `json:"version"`
	// IssuedAt / ExpiresAt are RFC3339 (UTC). An expired pack is refused on Apply and
	// is reported expired by the manager (a stale pack is never served).
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at,omitempty"`
	// Indicators are deny-list IOCs the guardrails block on match.
	Indicators []Indicator `json:"indicators,omitempty"`
	// BlockedMCP is the MCP-server deny-list (by name or URL, case-insensitive).
	BlockedMCP []string `json:"blocked_mcp,omitempty"`
	// Patterns are agentic-attack signatures (substring or, when Regex, RE2).
	Patterns []Pattern `json:"patterns,omitempty"`
	// Note is free-form operator/publisher context (no secrets).
	Note string `json:"note,omitempty"`
}

// Indicator is one deny-list IOC.
type Indicator struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Severity string `json:"severity,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Pattern is one agentic-attack signature.
type Pattern struct {
	ID       string `json:"id"`
	Match    string `json:"match"`
	Regex    bool   `json:"regex,omitempty"`
	Severity string `json:"severity,omitempty"`
	Note     string `json:"note,omitempty"`
}

// rulePackDomainTag DOMAIN-SEPARATES a rule-pack signature from every other
// Ed25519-signed Olivares artifact. A pack payload is JSON ('{'), never this tag, so
// the message spaces are provably disjoint.
var rulePackDomainTag = []byte("olivares.threatfeed.rulepack.v1\n")

// RulePackSigningInput returns the exact bytes a rule-pack signature covers.
func RulePackSigningInput(packJSON []byte) []byte {
	out := make([]byte, 0, len(rulePackDomainTag)+len(packJSON))
	out = append(out, rulePackDomainTag...)
	out = append(out, packJSON...)
	return out
}

// SignRulePack returns a detached Ed25519 signature over the domain-separated pack.
func SignRulePack(packJSON []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, RulePackSigningInput(packJSON))
}

// MarshalRulePack serializes a pack to stable indented JSON (the bytes to sign).
func MarshalRulePack(p RulePack) ([]byte, error) {
	p.Schema = RulePackSchema
	return json.MarshalIndent(p, "", "  ")
}

// VerifyRulePack authenticates packJSON against sig with ANY of pubs (multiple
// trusted publisher keys), then parses and validates it. Deny-closed: zero trusted
// keys, a bad signature or a malformed pack all fail with an error — the caller must
// treat that as "do not apply", never as an empty (permissive) pack.
func VerifyRulePack(packJSON, sig []byte, pubs []ed25519.PublicKey) (RulePack, error) {
	if len(pubs) == 0 {
		return RulePack{}, fmt.Errorf("threatfeed: no trusted keys pinned — rule-pack updates are disabled (deny-closed)")
	}
	input := RulePackSigningInput(packJSON)
	ok := false
	for _, pub := range pubs {
		if len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, input, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return RulePack{}, fmt.Errorf("threatfeed: rule-pack signature does not verify against any trusted key")
	}
	var p RulePack
	if err := json.Unmarshal(packJSON, &p); err != nil {
		return RulePack{}, fmt.Errorf("threatfeed: parse rule-pack: %w", err)
	}
	if err := p.Validate(); err != nil {
		return RulePack{}, err
	}
	return p, nil
}

// Validate checks a pack is well-formed: known schema, non-zero version, parseable
// timestamps, every indicator typed+valued, every pattern id+match.
func (p RulePack) Validate() error {
	if p.Schema != RulePackSchema {
		return fmt.Errorf("threatfeed: unknown rule-pack schema %d (want %d)", p.Schema, RulePackSchema)
	}
	if p.Version == 0 {
		return fmt.Errorf("threatfeed: rule-pack version must be > 0")
	}
	if _, err := time.Parse(time.RFC3339, p.IssuedAt); err != nil {
		return fmt.Errorf("threatfeed: rule-pack issued_at is not RFC3339: %w", err)
	}
	if p.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil {
			return fmt.Errorf("threatfeed: rule-pack expires_at is not RFC3339: %w", err)
		}
	}
	for i, ind := range p.Indicators {
		if strings.TrimSpace(ind.Type) == "" || strings.TrimSpace(ind.Value) == "" {
			return fmt.Errorf("threatfeed: indicator %d needs a type and a value", i)
		}
	}
	for i, pat := range p.Patterns {
		if strings.TrimSpace(pat.ID) == "" || strings.TrimSpace(pat.Match) == "" {
			return fmt.Errorf("threatfeed: pattern %d needs an id and a match", i)
		}
	}
	return nil
}

// ExpiredAt reports whether the pack is past its ExpiresAt as of now (false when no
// expiry is set).
func (p RulePack) ExpiredAt(now time.Time) bool {
	if p.ExpiresAt == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, p.ExpiresAt)
	if err != nil {
		return true // an unparseable expiry is treated as expired (fail-safe)
	}
	return now.After(exp)
}
