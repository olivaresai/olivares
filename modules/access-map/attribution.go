// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Attribution tiers (G8, ARCHITECTURE.md). The attribution tier is the
// HONEST, per-edge firmness with which module III can tie an access to a CONCRETE
// agent/NHI — the deepest, most defendable, and most fragile piece of the R/RW
// differential. It is deliberately STRICTER than Confidence: an edge can be
// Confidence=attributed (the signal itself is trustworthy) yet only `approximate`
// attribution when the credential is shared, and `unknown` when the store cannot be
// audited per identity at all. We never fabricate firmness the signal does not
// support (docs/SECURITY-HARDENING.md — "no finge lo que no sabe" — "no afirmes
// firmeza sin SVID/credencial dedicada").
const (
	// attribFirm is a concrete agent (or a dedicated per-agent NHI) backed by a real
	// per-agent identity signal: a SPIFFE workload SVID or an Anthropic WIF service
	// account, a governance-minted dedicated NHI or a credential bound to
	// exactly one agent, or a cooperative agent/session that named itself.
	attribFirm = "firm"
	// attribApproximate is an access where an identity is known but a single agent
	// cannot be pinned: a shared/pooled service account, an ambiguous credential, a
	// human/unknown principal, or a per-agent credential whose agent link is pending.
	attribApproximate = "approximate"
	// attribUnknown is the deny-closed floor: no per-identity attribution is
	// possible — a store with no per-identity audit (Redis/SQLite/D1, the opaque
	// tier) seen by a non-cooperative backstop, or an origin we cannot tie to any
	// identity. Never an invented agent.
	attribUnknown = "unknown"
)

// attributionTier classifies how firmly the resolved origin ties this edge to a
// concrete agent/NHI, consuming the per-agent identity signals expose (the
// roster NHI classification, a workload SVID / WIF source, a dedicated credential
// binding). It runs inside the ingest Mutate transaction, so the identity lookup is
// consistent with the bridge's own resolution.
//
// It is deny-closed: without a firm per-agent signal it returns approximate, and on
// an OPAQUE store (no passive per-identity audit) it floors any non-firm attribution
// to unknown rather than overstating it as a shared-account guess. It NEVER raises an
// edge to firm without an SVID/WIF/dedicated-credential signal (G8).
func attributionTier(ctx context.Context, sc store.Scope, attr attribution, coverage string) (tier, reason string, err error) {
	tier, reason, err = classifyAttribution(ctx, sc, attr)
	if err != nil {
		return "", "", err
	}
	// Opaque-store floor: a store with no passive per-identity audit (Redis/SQLite/
	// D1) cannot attribute an access per identity. Only a cooperative signal that
	// firmly named the agent survives; anything weaker is honestly unknown, never a
	// shared-account guess dressed up as approximate (ARCHITECTURE.md — tiered coverage,
	// "imposible passive").
	if coverage == tierOpaque && tier != attribFirm {
		return attribUnknown, "opaque store — no per-identity audit; attribution not possible", nil
	}
	return tier, reason, nil
}

// classifyAttribution derives the agent/NHI firmness from the bridge's resolution
// alone (before the opaque-store floor). The bridge has already refused to attribute
// to one of several candidates, so an `agent` origin at attributed confidence is, by
// construction, an UNAMBIGUOUS single-agent resolution.
func classifyAttribution(ctx context.Context, sc store.Scope, attr attribution) (tier, reason string, err error) {
	if attr.OriginID.IsZero() {
		return attribUnknown, "no per-identity signal", nil
	}
	// A shared/pooled/ambiguous signal (the bridge already degraded confidence to
	// approximate) can name an identity but never one agent.
	if attr.Confidence != sdkmodel.ConfidenceAttributed {
		return attribApproximate, "shared or ambiguous credential — per-agent attribution not firm", nil
	}
	switch attr.OriginKind {
	case originAgent:
		// The bridge resolves to an agent ONLY when it ties UNAMBIGUOUSLY to one
		// agent (a per-agent token/SVID, a sole-agent credential, or a cooperative
		// session/agent that named itself) — a firm per-agent attribution.
		return attribFirm, "resolved to a single agent", nil
	case originIdentity:
		// A credential identity with no single agent is firm ONLY if it is itself a
		// dedicated per-agent NHI (a workload SVID, a WIF service account, or a
		// minted NHI): the per-NHI attribution is firm even before an agent links.
		strong, e := strongNHIIdentity(ctx, sc, attr.OriginID)
		if e != nil {
			return "", "", e
		}
		if strong {
			return attribFirm, "dedicated NHI credential (SVID/WIF/minted)", nil
		}
		return attribApproximate, "credential identity; per-agent link pending", nil
	case originSession:
		// A cooperative session is a non-shared run, but without an agent link it is
		// not yet a firm per-agent attribution (honest: the agent is pending).
		return attribApproximate, "session resolved; agent link pending", nil
	default:
		return attribApproximate, "origin resolved without a firm per-agent signal", nil
	}
}

// strongNHIIdentity reports whether the resolved credential identity is a DEDICATED
// per-agent non-human identity strong enough to justify firm attribution: a SPIFFE
// workload SVID, an Anthropic WIF service account, or a governance-minted dedicated
// NHI. A missing identity, a human principal, an unenriched bare credential or a
// generic shared role is NOT firm. The lookup tolerates ErrNotFound (a credential
// the bridge has not persisted yet is simply not-firm, never an error).
func strongNHIIdentity(ctx context.Context, sc store.Scope, id model.ID) (bool, error) {
	ident, err := sc.Identities().Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return isStrongNHI(ident), nil
}

// isStrongNHI is the pure predicate over a resolved Identity's governance metadata —
// the allow-listed, non-PII signals reconciles from the roster and derives
// from a verified SVID/WIF. A human principal is never firm; an SVID (source=spiffe),
// a WIF service account (source=anthropic) or a minted dedicated NHI is.
func isStrongNHI(ident model.Identity) bool {
	if principalType(ident) == string(identitysource.PrincipalHuman) {
		return false // a human identity is never a firm per-agent NHI attribution
	}
	switch identitySource(ident) {
	case string(identitysource.SourceSPIFFE), string(identitysource.SourceAnthropic):
		return true // workload SVID / Anthropic WIF — a dedicated per-NHI credential
	case string(identitysource.SourceEntraAgent), string(identitysource.SourceAgentCore),
		string(identitysource.SourceGoogleAgent):
		// the hyperscaler agent registries (Entra Agent ID, AgentCore Identity,
		// Google Agent Identity) are dedicated per-agent identity sources — but only
		// their PER-AGENT rows. The same connectors also roster blueprint principals
		// (the credential shared by ALL of a blueprint's agents), credential
		// providers and service-account-backed agents; those are governed NHIs yet
		// never a firm per-agent signal, so firmness is gated on the registry-native
		// kind the roster stamped, not on the source alone.
		return dedicatedAgentKind(ident)
	}
	if minted, _ := ident.Metadata["minted"].(bool); minted {
		return true // governance-minted dedicated per-agent NHI
	}
	return false
}

// dedicatedAgentKind reports whether a federated-registry identity is one of the
// DEDICATED per-agent kinds (identitysource.KindAgentIdentity — an Entra
// agentIdentity service principal or a Google SPIFFE-based agent identity — or
// identitysource.KindWorkloadIdentity, the AgentCore directory's per-agent
// primitive). It prefers the reconciled Kind column (stamps the connector's
// roster kind there) and falls back to the roster_kind metadata, mirroring how
// identitySource prefers Provider over metadata.
func dedicatedAgentKind(ident model.Identity) bool {
	kind := strings.TrimSpace(ident.Kind)
	if kind == "" && ident.Metadata != nil {
		kind, _ = ident.Metadata["roster_kind"].(string)
	}
	switch kind {
	case identitysource.KindAgentIdentity, identitysource.KindWorkloadIdentity:
		return true
	}
	return false
}

// principalType reads the human/NHI/unknown classification stamps on a
// reconciled identity (empty when the directory never revealed it).
func principalType(ident model.Identity) string {
	if ident.Metadata == nil {
		return ""
	}
	s, _ := ident.Metadata["principal_type"].(string)
	return s
}

// identitySource returns the identity's provenance, preferring the canonical
// Provider field and falling back to the roster's metadata "source" key, lowercased
// so it compares against the identitysource.SourceKind vocabulary.
func identitySource(ident model.Identity) string {
	if p := strings.ToLower(strings.TrimSpace(ident.Provider)); p != "" {
		return p
	}
	if ident.Metadata != nil {
		if s, ok := ident.Metadata["source"].(string); ok {
			return strings.ToLower(strings.TrimSpace(s))
		}
	}
	return ""
}

// tierRank orders attribution tiers so fusion keeps the stronger one (firm >
// approximate > unknown). An empty/unrecognized tier is absent here and ranks below
// every real tier (tierRankOf returns -1), so a deliberate "unknown" still beats it.
var tierRank = map[string]int{attribUnknown: 0, attribApproximate: 1, attribFirm: 2}

// tierRankOf returns the rank of a tier, or -1 for an empty/unrecognized value.
func tierRankOf(s string) int {
	if r, ok := tierRank[s]; ok {
		return r
	}
	return -1
}

// strongerTier returns the higher-ranked of two attribution tiers, so a later,
// weaker signal never downgrades a firmly-attributed edge (mirroring the confidence
// rule in fuse; ARCHITECTURE.md).
func strongerTier(a, b string) string {
	if tierRankOf(a) >= tierRankOf(b) {
		return a
	}
	return b
}
