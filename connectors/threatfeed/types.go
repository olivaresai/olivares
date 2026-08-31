// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package threatfeed

// The curated feed channels. A feed artifact carries zero or more of these; each
// is independently versioned content the engine overlays on its compiled-in base
// catalog. The names are stable wire identifiers (do not rename without a feed
// schema bump).
const (
	// ChannelAgenticSignatures carries curated agentic-attack signatures (injection
	// / jailbreak / exfiltration / tool-poisoning), mapped to OWASP Agentic
	// (ASI01-ASI10) and MITRE ATLAS.
	ChannelAgenticSignatures = "agentic-signatures"
	// ChannelMCPReputation carries curated MCP-server reputation/posture entries.
	ChannelMCPReputation = "mcp-reputation"
	// ChannelModelCalendar carries the curated model deprecation/retirement calendar.
	ChannelModelCalendar = "model-calendar"
	// ChannelControlDeltas carries incremental control-mapping deltas layered over
	// the open modules/compliance base catalog.
	ChannelControlDeltas = "control-deltas"
)

// Channels is the canonical ordered list of feed channels (for status rendering
// and config validation).
func Channels() []string {
	return []string{ChannelAgenticSignatures, ChannelMCPReputation, ChannelModelCalendar, ChannelControlDeltas}
}

// FeedStatus is the minimal-data summary of the threat-intel feed's current
// state, returned by verify/apply/pull/status and rendered by the CLI/console. It
// NEVER carries a signature, a private key, or any feed content value.
type FeedStatus struct {
	// Enabled is true when the add-on is active (enterprise build + a parsed config).
	Enabled bool `json:"enabled"`
	// TrustedKeys is the number of operator-pinned feed verification keys. Zero
	// means feed UPDATES are disabled (deny-closed): the engine runs on its
	// compiled-in base catalog only, and verify/apply/pull refuse to trust any feed.
	TrustedKeys int `json:"trusted_keys"`
	// KeyFingerprints are the 8-hex SHA-256 prefixes of the trusted keys (an
	// eyeball anchor so an operator can confirm WHICH publisher key they pinned).
	KeyFingerprints []string `json:"key_fingerprints,omitempty"`
	// FeedURL is the configured pull endpoint (empty when the operator applies
	// feeds out-of-band). Host+path only; never embeds a credential.
	FeedURL string `json:"feed_url,omitempty"`

	// FeedLoaded is true when a signed feed artifact has been verified and applied.
	FeedLoaded bool `json:"feed_loaded"`
	// FeedVersion is the monotonic version of the active feed (0 when none). A feed
	// whose version is not strictly greater than the active one is rejected on
	// apply (anti-rollback).
	FeedVersion uint64 `json:"feed_version"`
	// SchemaVersion is the active feed's schema version.
	SchemaVersion int `json:"schema_version,omitempty"`
	// IssuedAt / ExpiresAt are the active feed's RFC3339 timestamps.
	IssuedAt  string `json:"issued_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	// Expired is true when the active feed is past ExpiresAt: the engine then
	// IGNORES the feed overlay and runs base-only (a stale feed is never served).
	Expired bool `json:"expired"`

	// Channels reports the per-channel state (base + feed-overlay counts).
	Channels []ChannelStatus `json:"channels"`

	// Base reports the compiled-in base-catalog entry counts (what the add-on
	// detects with no feed applied — the as-shipped snapshot the live feed refreshes).
	Base BaseCounts `json:"base"`

	// CrosswalkRows / CrosswalkGaps summarize the Claude/Anthropic governance
	// crosswalk: total obligation rows and how many are honest empty gaps (no
	// backing plane capability).
	CrosswalkRows int `json:"crosswalk_rows"`
	CrosswalkGaps int `json:"crosswalk_gaps"`
}

// ChannelStatus is the per-channel slice of FeedStatus.
type ChannelStatus struct {
	Name string `json:"name"`
	// Enabled reports whether the operator left this channel on (default on).
	Enabled bool `json:"enabled"`
	// Entries is the count of ACTIVE entries (base catalog plus the verified feed
	// overlay, deduped). FeedEntries is the subset contributed by the signed feed.
	Entries     int `json:"entries"`
	FeedEntries int `json:"feed_entries"`
}

// BaseCounts is the compiled-in base-catalog entry counts.
type BaseCounts struct {
	Signatures    int `json:"signatures"`
	MCPReputation int `json:"mcp_reputation"`
	ModelCalendar int `json:"model_calendar"`
	ControlDeltas int `json:"control_deltas"`
}

// --- Claude/Anthropic governance crosswalk shapes ----------------------------
//
// These mirror the rigor of modules/compliance (FrameworkPin/SourceRef) and ADD a
// dedicated verbatim Quote field (which that package deliberately lacks). The
// DATA — the actual obligation rows and pins — lives in the closed
// enterprise/threatintel/claudecrosswalk; these are only the shapes the open CLI
// renders.

// Crosswalk maps Anthropic governance obligations to the plane capabilities that
// honestly evidence them.
type Crosswalk struct {
	Title string `json:"title"`
	// Disclaimer states the honest boundary: this is a TECHNICAL MAPPING showing
	// which plane controls bear on an obligation, NOT a certification of
	// compliance and NOT a claim the obligation is fully met.
	Disclaimer string         `json:"disclaimer"`
	Rows       []CrosswalkRow `json:"rows"`
}

// Policy identifiers for a crosswalk row (which Anthropic policy the obligation
// is drawn from).
const (
	PolicyUsage            = "usage_policy"
	PolicyModelDeprecation = "model_deprecation"
	PolicyHighRiskUseCase  = "high_risk_use_case"
)

// CrosswalkRow is one Anthropic obligation, its verbatim source pin, and the
// plane capabilities (if any) that honestly evidence it.
type CrosswalkRow struct {
	// ID is a stable, non-sensitive identifier.
	ID string `json:"id"`
	// Policy is one of the Policy* constants.
	Policy string `json:"policy"`
	// Obligation is a short human-readable statement of what the policy requires.
	Obligation string `json:"obligation"`
	// Pin is the verbatim primary-source citation backing the obligation.
	Pin SourcePin `json:"pin"`
	// Capabilities lists the plane controls that bear on the obligation. An EMPTY
	// list is an HONEST GAP (the plane does not evidence this obligation), never a
	// fabricated mapping — GapNote then explains why.
	Capabilities []CapabilityEvidence `json:"capabilities,omitempty"`
	// GapNote, when Capabilities is empty, states honestly why the plane cannot
	// evidence this obligation (e.g. "no harmful-content safety classifier in the
	// base engine").
	GapNote string `json:"gap_note,omitempty"`
}

// Mapped reports whether the row has at least one backing capability (i.e. it is
// not an honest empty gap).
func (r CrosswalkRow) Mapped() bool { return len(r.Capabilities) > 0 }

// SourcePin is a verbatim primary-source citation: the exact policy text, where
// it was published, and when this repo last verified it. It is the same rigor as
// modules/compliance FrameworkPin, plus an explicit verbatim Quote.
type SourcePin struct {
	// Document is the canonical document title, verbatim.
	Document string `json:"document"`
	// Quote is the VERBATIM obligation text from the source (no paraphrase).
	Quote string `json:"quote"`
	// SourceURL is the canonical primary-source URL.
	SourceURL string `json:"source_url"`
	// Effective is the effective/published date shown on the source (optional).
	Effective string `json:"effective,omitempty"`
	// VerifiedOn is the ISO date this repo last verified the quote against the
	// source. REQUIRED — a pin without it does not ship.
	VerifiedOn string `json:"verified_on"`
}

// CapabilityEvidence names one plane capability and how it honestly bears on an
// obligation, with a file:line entrypoint an auditor can re-verify.
type CapabilityEvidence struct {
	// Capability is the human-readable control name (e.g. "Claude Code hooks PEP").
	Capability string `json:"capability"`
	// Entrypoint is a file:line reference into the open source tree.
	Entrypoint string `json:"entrypoint"`
	// How states precisely what the capability evidences — and, where relevant,
	// its honest limit (e.g. "verified-deployed at the proxy, not unbypassable").
	How string `json:"how"`
}
