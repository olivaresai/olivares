// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package model holds the connector-facing wire vocabulary of Olivares AI: the
// normalized observation DTOs a SourceConnector emits and the small set of
// enums that cross the connector boundary. It is licensed Apache-2.0 and
// depends only on the standard library, so third parties can build connectors
// without copyleft friction and without importing the AGPL engine.
//
// Connectors never import the engine's persistence model (core/model); they
// speak only this vocabulary. The engine maps these observations onto its
// persisted entities (see core/model.AccessEdge) inside AGPL code. Seeds
// only what access-edge ingest needs; S02 expands this package.
package model

// AccessMode is the read/write classification of an access, the spine of the
// R/RW map (ARCHITECTURE.md, §6). It crosses the connector boundary, so it lives in
// the SDK and is reused verbatim by the engine's persistence model.
type AccessMode string

// The access modes. Unknown is explicit (never guessed) so the product can show
// honest confidence levels rather than fabricate a classification (ARCHITECTURE.md).
const (
	// ModeUnknown means the read/write nature could not be determined.
	ModeUnknown AccessMode = "unknown"
	// ModeRead is a read-only access (e.g. pgAudit READ, S3 readOnly=true).
	ModeRead AccessMode = "read"
	// ModeWrite is a write-only access.
	ModeWrite AccessMode = "write"
	// ModeReadWrite is an access that both reads and writes.
	ModeReadWrite AccessMode = "readwrite"
)

// Valid reports whether m is a known access mode.
func (m AccessMode) Valid() bool {
	switch m {
	case ModeUnknown, ModeRead, ModeWrite, ModeReadWrite:
		return true
	default:
		return false
	}
}

// SignalSource identifies which collector produced an observation. The product
// shows the provenance of every edge rather than collapsing sources, because a
// pgAudit READ and an MCP annotation carry very different trust (ARCHITECTURE.md).
type SignalSource string

// The signal sources seeded in v1. The list grows as connectors are added; it
// is a plain string so a third-party connector can introduce its own.
const (
	// SignalOTEL is OpenTelemetry tool telemetry from a cooperative agent.
	SignalOTEL SignalSource = "otel"
	// SignalMCPAnnotation is an MCP readOnlyHint/destructiveHint — UNTRUSTED per
	// the MCP spec, so it must be corroborated, never trusted alone (ARCHITECTURE.md).
	SignalMCPAnnotation SignalSource = "mcp_annotation"
	// SignalPGAudit is a Postgres pgAudit READ/WRITE classification.
	SignalPGAudit SignalSource = "pg_audit"
	// SignalCloudTrail is an AWS CloudTrail record (e.g. S3 readOnly).
	SignalCloudTrail SignalSource = "cloudtrail"
	// SignalEBPF is a kernel-level Tetragon backstop observation.
	SignalEBPF SignalSource = "ebpf"
	// SignalPolicy is a declared/permitted grant (not an observation) used to
	// populate the permitted side of the permitted-vs-observed diff (ARCHITECTURE.md).
	SignalPolicy SignalSource = "policy"
	// SignalA2A is an Agent2Agent (A2A) observation: an agent↔agent communication
	// edge the a2a connector observed, carrying the agent-to-agent trust signal
	// (a signed Agent Card the connector verified). Like mcp_annotation it is a
	// vendor-neutral interop signal whose confidence reflects the verification: an
	// edge tied to a verified signed card is attributed; one tied to an unsigned or
	// unverifiable card is approximate (never silently trusted; ARCHITECTURE.md).
	SignalA2A SignalSource = "a2a"
	// SignalConfig is a capability DECLARED in static configuration (not observed at
	// runtime): a subagent/Skill/plugin/output-style the operator wrote into the
	// agent's config tree, discovered by READING that config (CLA-14). It is what lets
	// a consumer distinguish a DECLARED capability (signal_source=config) from one seen
	// EXECUTING on the bus (otel/mcp_annotation/...): the former is "this exists and is
	// wired", the latter "this actually ran". The feeder reads metadata only (names,
	// refs) — never a prompt body, skill content or secret (docs/SECURITY-HARDENING.md).
	SignalConfig SignalSource = "config"
	// SignalCMA is a Claude Managed Agents (CMA) control-plane observation: a vault/
	// credential, memory-store/version, permission-policy, outcome, work-queue or skill
	// fact the claude-managed-agents connector read from the CMA API or terminated from a
	// signed CMA webhook (C1-C5). It is an Anthropic-first governance signal,
	// orthogonal to SignalA2A (inter-agent interop) and the MCP signals: its confidence is
	// attributed (the connector authenticated to the API / verified the webhook HMAC). The
	// connector reads references and state only — never credential material, memory content
	// or a webhook payload body (docs/SECURITY-HARDENING.md).
	SignalCMA SignalSource = "cma"
	// SignalScopedGrant is a FASE X CONFIGURED scope grant projected onto
	// the PERMITTED side of the access map: a source→scope binding (or scoped grant)
	// the source-scoping plane published so the permitted-vs-observed drift reflects
	// what the control plane's OWN scoping permits, not only IdP/credential-derived
	// grants (SignalPolicy). Like SignalPolicy it is declared, never observed — it maps
	// to (observed=false, permitted=true). It carries no payload, only the
	// agent→source edge it authorizes (ARCHITECTURE.md).
	SignalScopedGrant SignalSource = "scoped_grant"
	// SignalGitHub is an OBSERVED access the github source connector derived from a
	// GitHub webhook event (push, pull_request, code_scanning) or from the GitHub
	// REST/GraphQL API during a reconciliation poll. It maps to (observed=true,
	// permitted=false) in the access-map — the PERMITTED counterpart for repo ACLs
	// rides the existing SignalPolicy path.
	SignalGitHub SignalSource = "github"
	// SignalGitLab is the GitLab counterpart of SignalGitHub: an OBSERVED access the
	// gitlab source connector derived from a GitLab webhook event or REST API poll.
	// Same mapping as SignalGitHub — (observed=true, permitted=false); ACL-derived
	// permitted edges use SignalPolicy.
	SignalGitLab SignalSource = "gitlab"
	// SignalAgentCore is a DECLARED capability the agentcore connector derived from
	// an AWS Bedrock AgentCore Registry record — an MCP/A2A/CUSTOM/AGENT_SKILLS
	// agent registered in the account's agent directory. It maps to
	// (observed=false, permitted=true) in the access-map: a registry record is a
	// DECLARED agent, not an observed access.
	SignalAgentCore SignalSource = "agentcore"
	// SignalCoT is an OBSERVED Cursor-on-Target event the tak connector received on
	// a CoT listener. It maps to (observed=true, permitted=false). Like
	// SignalMCPAnnotation it is a self-asserted signal: base CoT rides plain UDP/TCP
	// with no authentication of the emitting uid, so an edge derived from it is
	// ConfidenceApproximate unless a deployment terminates mTLS in front of the
	// listener. Never trust a CoT uid as an authenticated identity.
	SignalCoT SignalSource = "cot"
)

// Confidence is the qualitative trust in an observation. It is shown to the
// operator (attributed vs approximate) so the product never fakes certainty
// (ARCHITECTURE.md).
type Confidence string

// The confidence levels.
const (
	// ConfidenceAttributed means the access is firmly attributed to the origin
	// (e.g. per-agent identity in the audit trail).
	ConfidenceAttributed Confidence = "attributed"
	// ConfidenceApproximate means the attribution is inferred and may be lossy
	// (e.g. a shared service account, or a lossy store such as Mongo/Redis).
	ConfidenceApproximate Confidence = "approximate"
)

// Valid reports whether c is a known confidence level.
func (c Confidence) Valid() bool {
	switch c {
	case ConfidenceAttributed, ConfidenceApproximate:
		return true
	default:
		return false
	}
}

// Severity is the qualitative seriousness of a finding or notification. It is a
// single shared vocabulary so a security connector's FindingReport and an output
// connector's Notification grade danger on the same scale (ARCHITECTURE.md). It is an
// ordered set, so callers can threshold ("warn at high or above").
type Severity string

// The severity levels, in increasing order of seriousness.
const (
	// SeverityInfo is informational, no action implied.
	SeverityInfo Severity = "info"
	// SeverityLow is a low-priority issue.
	SeverityLow Severity = "low"
	// SeverityMedium is a moderate issue worth attention.
	SeverityMedium Severity = "medium"
	// SeverityHigh is a serious issue that should be acted on.
	SeverityHigh Severity = "high"
	// SeverityCritical is an urgent issue demanding immediate action.
	SeverityCritical Severity = "critical"
)

// CostProvenance records whether a CostSample's monetary amount is the provider's
// authoritative billed figure or a figure the engine derived from list pricing. It
// crosses the connector boundary so module XI can label every spend figure honestly
// (billed vs estimated) and never present an estimate as the invoice (ARCHITECTURE.md).
// It is provider-neutral: any cost connector tags its samples with it.
type CostProvenance string

// The cost provenances.
const (
	// ProvenanceEstimated is a figure DERIVED from declared list pricing applied to
	// token counts (e.g. Anthropic usage_report × pricing). It is the default when a
	// provider returns usage but no money, and the only available figure for billing
	// models a cost API omits (e.g. Anthropic Priority Tier, absent from cost_report).
	ProvenanceEstimated CostProvenance = "estimated"
	// ProvenanceBilled is a monetary amount the provider's own cost API reported
	// (e.g. Anthropic cost_report). It is the authoritative, reconcilable figure.
	ProvenanceBilled CostProvenance = "billed"
)

// Valid reports whether p is a known cost provenance. An empty/unknown provenance
// is treated as estimated by consumers (the conservative default), but Valid lets a
// host reject a garbage value rather than silently mislabel a billed figure.
func (p CostProvenance) Valid() bool {
	switch p {
	case ProvenanceEstimated, ProvenanceBilled:
		return true
	default:
		return false
	}
}

// Gateway is the deployment surface a model call was served through. The same model
// (e.g. Claude) is reachable direct from the vendor API or via a cloud gateway
// (Bedrock, Vertex, Foundry), each with a different id space, billing, residency and
// observability surface — so cost and access must record WHICH one served a call
// rather than collapse them. It is an open string: a third-party
// connector may introduce its own surface without an SDK release.
type Gateway string

// The deployment surfaces seeded in v1. bedrock-mantle is the current AWS Bedrock
// surface and the build target; bedrock-legacy (InvokeModel/Converse) is
// observe-only/deprecated, never a new build target (ANT2-01).
const (
	// GatewayDirect is the vendor's first-party API (e.g. api.anthropic.com).
	GatewayDirect Gateway = "direct"
	// GatewayBedrockMantle is the current Amazon Bedrock surface (Bedrock _Mantle_).
	GatewayBedrockMantle Gateway = "bedrock-mantle"
	// GatewayBedrockLegacy is the deprecated Bedrock InvokeModel/Converse surface,
	// observe-only — not a build target.
	GatewayBedrockLegacy Gateway = "bedrock-legacy"
	// GatewayVertex is Google Vertex AI.
	GatewayVertex Gateway = "vertex"
	// GatewayFoundry is Microsoft Foundry.
	GatewayFoundry Gateway = "foundry"
	// GatewayClaudePlatformAWS is Claude Platform on AWS (Anthropic-operated on AWS,
	// distinct from partner-operated Bedrock).
	GatewayClaudePlatformAWS Gateway = "claude-platform-aws"
)

// seededGateways is the set of surfaces shipped in v1. Gateway is an OPEN string (a
// connector may introduce its own), so this is a recognition aid for config
// validation and display — not a closed allowlist a consumer rejects on.
var seededGateways = map[Gateway]struct{}{
	GatewayDirect: {}, GatewayBedrockMantle: {}, GatewayBedrockLegacy: {},
	GatewayVertex: {}, GatewayFoundry: {}, GatewayClaudePlatformAWS: {},
}

// Valid reports whether g is one of the seeded v1 surfaces. It is a convenience for
// validating operator-supplied gateway config (catching a typo); because Gateway is
// open, a consumer must still accept an unseeded value rather than treat !Valid as an
// error.
func (g Gateway) Valid() bool {
	_, ok := seededGateways[g]
	return ok
}

// severityRank orders the levels 0..4 so Severity.AtLeast can threshold. An
// unknown severity is absent from the map, so the comma-ok lookups in Valid and
// AtLeast reject it — there is no sentinel rank; the missing key is the guard.
var severityRank = map[Severity]int{
	SeverityInfo: 0, SeverityLow: 1, SeverityMedium: 2, SeverityHigh: 3, SeverityCritical: 4,
}

// Valid reports whether s is a known severity level.
func (s Severity) Valid() bool {
	_, ok := severityRank[s]
	return ok
}

// AtLeast reports whether s is at least as severe as floor. It fails closed on
// either side: an unknown s never clears a gate, and an unknown floor matches
// nothing (a garbage threshold must not silently behave like "info").
func (s Severity) AtLeast(floor Severity) bool {
	sr, ok := severityRank[s]
	if !ok {
		return false
	}
	fr, ok := severityRank[floor]
	if !ok {
		return false
	}
	return sr >= fr
}
