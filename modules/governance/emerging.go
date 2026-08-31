// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

// IDN-12 — emerging agent-identity standards, DESIGN-TOWARD.
//
// The 2026 agent-identity standards (workload identity, transaction tokens,
// cross-app access, identity chaining, signed agent cards, verifiable agent
// credentials) are the direction the target audience tracks, but they are NOT
// stable and must NOT be hard-coded against. So this is a TRACKED registry, not
// an implementation: each entry maps a spec to the EVENTUAL seam in this codebase
// where it would land, with the revision verified at documentation time and an
// explicit caveat that the revision moves.
//
// This is data only — naming a seam, never wiring a draft wire-format. The
// revisions below were verified against IETF/LF/W3C primary sources on 2026-06;
// several CORRECT the numbers used earlier
// (identity-chaining is -12 not -08; A2A is v1.0 GA not RC; transaction tokens
// are an OAuth-WG draft, not a WIMSE-WG one). Refresh them at adoption time —
// do NOT treat the numbers here as final.

// EmergingStatus is how stable a tracked standard is today.
type EmergingStatus string

const (
	// StatusDraft is an in-progress I-D / working draft (do not hard-code).
	StatusDraft EmergingStatus = "draft"
	// StatusStable is a published/ratified spec (a seam MAY target it concretely).
	StatusStable EmergingStatus = "stable"
)

// EmergingStandard is one tracked agent-identity standard and the seam it maps to.
type EmergingStandard struct {
	// Key is a stable local identifier for the standard.
	Key string `json:"key"`
	// Name is the human-readable standard name.
	Name string `json:"name"`
	// Body is the standards organization / working group.
	Body string `json:"body"`
	// Spec is the document identifier (I-D name or spec title).
	Spec string `json:"spec"`
	// Revision is the revision verified at documentation time (moves — refresh it).
	Revision string `json:"revision"`
	// Status is how stable the standard is today.
	Status EmergingStatus `json:"status"`
	// Seam names the eventual integration point in THIS codebase.
	Seam string `json:"seam"`
	// Caveat is the honesty note (why it is tracked, not implemented).
	Caveat string `json:"caveat"`
	// VerifiedAt is when the revision/status was last checked against the source.
	VerifiedAt string `json:"verified_at"`
	// Authority is the primary-source URL.
	Authority string `json:"authority"`
}

// emergingVerifiedAt is the month the registry's revisions were verified against
// primary sources. It is intentionally coarse — the point is that revisions move.
const emergingVerifiedAt = "2026-06"

// emergingStandards is the tracked design-toward registry. Adding a wire format
// for any of these is a deliberate future step gated on the spec stabilizing;
// until then a seam is named, not wired.
var emergingStandards = []EmergingStandard{
	{
		Key:  "oauth_transaction_tokens",
		Name: "OAuth Transaction Tokens (Txn-Tokens)",
		Body: "IETF OAuth WG",
		Spec: "draft-ietf-oauth-transaction-tokens",
		// NOTE: Framed these as "WIMSE transaction tokens"; they are an
		// OAuth-WG draft. WIMSE WG is the separate workload-identity effort.
		Revision:   "08",
		Status:     StatusDraft,
		Seam:       "core/auth token exchange (tokenexchange.go, RFC 8693 — IDN-08): a call-chain authorization-context token would extend the delegated-token primitive already built.",
		Caveat:     "Draft; the call-chain claim set is not final. Track, do not hard-code. Maps to the existing delegated-token seam, not a new wire format.",
		Authority:  "https://datatracker.ietf.org/doc/draft-ietf-oauth-transaction-tokens/",
		VerifiedAt: emergingVerifiedAt,
	},
	{
		Key:        "oauth_id_jag_xaa",
		Name:       "Identity Assertion JWT Authorization Grant (ID-JAG) / Cross-App Access",
		Body:       "IETF OAuth WG",
		Spec:       "draft-ietf-oauth-identity-assertion-authz-grant",
		Revision:   "04",
		Status:     StatusDraft,
		Seam:       "MCP authorization broker (ANT-3): IdP-brokered agent-to-app access would consume an ID-JAG as the cross-domain grant.",
		Caveat:     "Draft; the grant/claims profile is evolving. Track, do not hard-code.",
		Authority:  "https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/",
		VerifiedAt: emergingVerifiedAt,
	},
	{
		Key:  "oauth_identity_chaining",
		Name: "OAuth Identity and Authorization Chaining Across Domains",
		Body: "IETF OAuth WG",
		Spec: "draft-ietf-oauth-identity-chaining",
		// Said -08; verified current is -12.
		Revision:   "12",
		Status:     StatusDraft,
		Seam:       "cross-tenant / A2A delegation boundary (Module XX/IV): chaining identity across trust domains layers on the token-exchange seam.",
		Caveat:     "Draft (corrects -08 to the verified -12). Track, do not hard-code.",
		Authority:  "https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-chaining/",
		VerifiedAt: emergingVerifiedAt,
	},
	{
		Key:      "a2a_signed_agent_cards",
		Name:     "A2A Signed Agent Cards (+ mTLS)",
		Body:     "Linux Foundation (A2A Project)",
		Spec:     "A2A Protocol",
		Revision: "v1.0",
		// A2A reached v1.0 GA (~2026-04) said "v1.0 RC".
		Status:     StatusStable,
		Seam:       "an A2A connector (ABSENT today): when built, it would verify signed Agent Cards for cross-org agent trust and feed the NHI roster.",
		Caveat:     "v1.0 is GA (corrects 'RC'), but the A2A connector that would consume signed cards does not exist yet — so this remains a seam, not an integration.",
		Authority:  "https://a2a-protocol.org/latest/specification/",
		VerifiedAt: emergingVerifiedAt,
	},
	{
		Key:        "w3c_vc_did_agent",
		Name:       "W3C Verifiable Credentials / DIDs for agent identity",
		Body:       "W3C",
		Spec:       "Verifiable Credentials Data Model / Decentralized Identifiers",
		Revision:   "VC 2.0 / DID 1.0",
		Status:     StatusStable,
		Seam:       "cross-org agent-credential interop: a VC/DID verifier would bind a presented agent credential to a roster identity (alongside the SPIFFE SVID path, IDN-07).",
		Caveat:     "The base VC/DID specs are stable, but the agent-identity PROFILE (which credential schema an agent presents) is still forming in the W3C agent-identity community work. Track the profile; do not hard-code a schema.",
		Authority:  "https://www.w3.org/TR/vc-data-model-2.0/",
		VerifiedAt: emergingVerifiedAt,
	},
	{
		Key:        "scim_device_model",
		Name:       "SCIM Device / EndpointApp schema (NHI-native provisioning)",
		Body:       "IETF SCIM WG",
		Spec:       "draft-ietf-scim-device-model",
		Revision:   "draft",
		Status:     StatusDraft,
		Seam:       "the SCIM resource layer (core/api/scim, IDN-11) is already list-driven so a Device/EndpointApp resource type is a registration, not a refactor — added when the draft is final.",
		Caveat:     "Still a draft; the schema URN/attributes are intentionally NOT declared in core/api/scim until it reaches RFC. Track, do not hard-code.",
		Authority:  "https://datatracker.ietf.org/doc/draft-ietf-scim-device-model/",
		VerifiedAt: emergingVerifiedAt,
	},
}

// EmergingStandards returns the tracked design-toward registry (a copy, so a
// caller cannot mutate the package state).
func EmergingStandards() []EmergingStandard {
	out := make([]EmergingStandard, len(emergingStandards))
	copy(out, emergingStandards)
	return out
}

// handleEmergingStandards surfaces the design-toward registry read-only, with an
// explicit disclaimer that it is tracked, not implemented (IDN-12). The web's
// intelligence surface can show "standards we track" without implying support.
func (m *Module) handleEmergingStandards(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusOK, map[string]any{
		"standards":   EmergingStandards(),
		"verified_at": emergingVerifiedAt,
		"disclaimer":  "Design-toward only (IDN-12): these emerging agent-identity standards are TRACKED, not implemented. Revisions move — re-verify the current revision before adopting. Nothing here is hard-coded against a draft.",
	})
}
