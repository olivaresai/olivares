// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

// CoSAI/OASIS "Model Context Protocol (MCP) Security" — the control-plane mapping,
// sitting ALONGSIDE the OWASP MCP Top 10 mapping (owasp_mcp.go) as a second,
// independent primary source. Same discipline: verbatim titles, real product
// controls as evidence, honest coverage grades — not an inflated "we cover all
// twelve".
//
// SOURCE (verified verbatim against the PDF itself, 2026-06-10):
//
//	CoSAI — Coalition for Secure AI (an OASIS Open Project), Workstream 4
//	"Secure Design Patterns for Agentic Systems" — "Model Context Protocol (MCP)
//	Security". Approved by the CoSAI Project Governing Board on 2026-01-08;
//	announced publicly 2026-01-27 by OASIS and CoSAI. Status: DRAFT — a LIVING
//	DOCUMENT (titles may change; re-verify before quoting in external material).
//	PDF (27 pp):
//	https://www.coalitionforsecureai.org/wp-content/uploads/2026/03/model-context-protocol-security-1.pdf
//	Announcement:
//	https://www.oasis-open.org/2026/01/27/coalition-for-secure-ai-releases-extensive-taxonomy-for-model-context-protocol-security/
//
// Structure of the paper: twelve core threat categories (MCP-T1..MCP-T12, mapped
// below) covering almost forty threats across three specificity tiers — Tier 1
// MCP-Specific (7 threats), Tier 2 MCP-Contextualized (8), Tier 3 Conventional
// (19) — plus eleven control/mitigation categories (§3.2: Agent Identity; Secure
// Delegation and Access Control; Input and Data Sanitization and Filtering;
// Cryptographic Integrity and Remote Attestation; Sandboxing and Isolation;
// Cryptographic Verification of Resources; Transport Layer Security; Secure Tool
// and UX Design; Human-in-the-loop; Logging; Lifecycle and Governance) and four
// deployment patterns. The paper analyzes MCP spec revision 2025-06-18 and later.
//
// Category ids/titles below are quoted verbatim from the paper.
//
// HONESTY NOTE on the OWASP cross-references (OWASPRefs): the CoSAI paper contains
// ZERO references to OWASP (verified by grep over the extracted PDF text), and no
// upstream mapping between CoSAI MCP-T* and the OWASP MCP Top 10 exists in either
// direction. Every OWASPRefs entry in this file is therefore OLIVARES' OWN
// ANALYSIS — a conservative correspondence we assert and stand behind, never an
// upstream claim — recorded explicitly, exactly the way owasp_mcp.go records the
// README-vs-index.md MCP06 title discrepancy rather than pretending it away.
//
// This is a CONTROL MAPPING (positioning), not a runtime detector: each entry ties
// a CoSAI threat category to the product capability that addresses it, the
// evidence (finding kinds / file refs), our OWASP MCP cross-map, and an honest
// coverage grade (the shared mcpCoverage scale from owasp_mcp.go).

// CoSAIMCPControl is one CoSAI MCP Security threat category mapped to the
// product's controls.
type CoSAIMCPControl struct {
	// ID is the CoSAI category id (e.g. "MCP-T11").
	ID string
	// Title is the verbatim published category title.
	Title string
	// ProductControl is the capability that addresses the threat category.
	ProductControl string
	// Evidence lists the finding kinds and/or code references that evidence it.
	Evidence []string
	// OWASPRefs is OLIVARES' OWN conservative cross-map to OWASP MCP Top 10 ids
	// (e.g. "MCP04:2025"). NO upstream CoSAI↔OWASP mapping exists (see file
	// header); these are our analysis, kept to the unambiguous correspondences.
	OWASPRefs []string
	// Coverage is the honest completeness grade (shared scale, owasp_mcp.go).
	Coverage mcpCoverage
}

// cosaiMCPSecurity is the verified MCP-T1..MCP-T12 catalog mapped to product
// controls. Grades are honest: a category that is largely a deployment/operator
// concern (network binding, per-server rate limiting) is `referenced`/`partial`,
// not `addressed`, even where the plane contributes a real control.
var cosaiMCPSecurity = []CoSAIMCPControl{
	{
		ID: "MCP-T1", Title: "Improper Authentication and Identity Management",
		ProductControl: "Inline OAuth 2.1 Resource-Server PEP: RFC 9728 Protected Resource Metadata, fail-closed bearer validation with MANDATORY audience binding (RFC 8707 / RFC 9068 'at+jwt'; RFC 7662 opaque) — a token not minted for this server is rejected. Client side does PRM→AS discovery + PKCE S256 + resource indicators. Enterprise identity: ID-JAG (SEP-990) identity-assertion grants are validated fail-closed and can only delegate to operator-approved servers (internal registry as policy input).",
		Evidence:       []string{"connectors/mcp/rs.go", "connectors/mcp/tokenvalidate.go (audience-bind fail-closed)", "connectors/mcp/oauth.go (client side)", "connectors/mcp/idjag.go (ID-JAG, approved servers only)", "finding:mcp_auth"},
		OWASPRefs:      []string{"MCP07:2025"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP-T2", Title: "Missing or Improper Access Control",
		ProductControl: "Deny-by-default tools/call PEP over a SERVER-OWNED tool→scope map: the required scope is enforced per call, insufficient scope answers a 403 step-up scope challenge (SEP-835). Detective: access-map permitted-vs-observed R/RW drift (module III) plus the posture scanner flagging over-broad requested OAuth scopes.",
		Evidence:       []string{"connectors/mcp/rs.go (deny-by-default scope gate, step-up)", "connectors/mcp/toolset.go", "modules/access-map (drift)", "finding:mcp_posture [MCP02]"},
		OWASPRefs:      []string{"MCP02:2025", "MCP07:2025"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP-T3", Title: "Input Validation/Sanitization Failures",
		ProductControl: "Guardrail detectors for dangerous tool arguments and code-execution surfaces (OWASP-Agentic ASI02/ASI05), homoglyph/zero-width/bidi scanning of tool and server names, and the posture scanner flagging executional tool surfaces at introspection. HONEST GAP: the plane screens and detects; per-tool inputSchema enforcement remains the MCP server's own responsibility.",
		Evidence:       []string{"modules/security/owasp_agentic.go (ASI02/ASI05)", "connectors/mcp/textscan.go", "connectors/mcp/posture.go (executional tool surface)", "finding:mcp_posture [MCP05]"},
		OWASPRefs:      []string{"MCP05:2025"}, Coverage: mcpPartial,
	},
	{
		ID: "MCP-T4", Title: "Input/Instruction Boundary Distinction Failure",
		ProductControl: "MCP content treated UNTRUSTED by design (SignalMCPAnnotation + ConfidenceApproximate): the posture scanner statically screens tool/prompt DESCRIPTIONS and the server `instructions` for instruction-injection markers and CONCEALED (invisible/bidi) instructions at introspection time, while the runtime indirect-injection detector screens tool/RAG output surfaces; advertised elicitation/sampling surfaces (model/user input vectors) are flagged as findings.",
		Evidence:       []string{"connectors/mcp/posture.go (description injection)", "connectors/mcp/textscan.go (injection markers + invisible chars)", "modules/security/injection.go (indirect/RAG surface)", "finding:mcp_surface"},
		OWASPRefs:      []string{"MCP06:2025", "MCP10:2025"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP-T5", Title: "Inadequate Data Protection and Confidentiality Controls",
		ProductControl: "Minimal-data capture by construction (connectors never persist tokens/secrets; refs scrubbed), secret/PII detectors on input and output, deny-closed DLP over classified content, and BYOK/HYOK/CMEK envelope encryption at rest. The posture scanner additionally flags a credential/secret SHAPE leaked in catalog metadata.",
		Evidence:       []string{"connectors/internal/redact", "modules/security/pii.go", "modules/knowledge/dlp.go (deny-closed DLP)", "core/secure (BYOK/CMEK envelope)", "connectors/mcp/posture.go (secret-in-metadata)"},
		OWASPRefs:      []string{"MCP01:2025", "MCP10:2025"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP-T6", Title: "Missing Integrity/Verification Controls",
		ProductControl: "Signed catalog entries (Ed25519 over a deterministic canonical content hash — any later field change breaks the hash), the deny-closed signed-admission flow, and signed admission of FEDERATED catalog entries before they become instantiable. HONEST GAP: no remote attestation and no per-resource cryptographic verification (CoSAI §3.2 control categories the plane does not implement).",
		Evidence:       []string{"modules/catalog/sign.go (Ed25519 canonical entry hash)", "modules/catalog/mcpadmission.go (signed federated-entry admission)", "connectors/mcp/federation.go (federated catalogs)", "finding:mcp_provenance"},
		OWASPRefs:      []string{"MCP04:2025"}, Coverage: mcpPartial,
	},
	{
		ID: "MCP-T7", Title: "Session and Transport Security Failures",
		ProductControl: "The RS PEP enforces the Streamable-HTTP Origin allowlist (invalid Origin → 403, the DNS-rebinding defense) and never mints an Mcp-Session-Id (the next-revision stateless core removes sessions entirely; the legacy client echoes a server session id but never treats it as a credential — every call is bearer-authenticated, audience-bound). HONEST GAP: TLS termination is a deployment convention in front of the RS, not enforced in-process.",
		Evidence:       []string{"connectors/mcp/rs.go (Origin 403)", "connectors/mcp/stateless.go (sessionless RC core)", "connectors/mcp/http.go (session id never auth)", "connectors/mcp/tokenvalidate.go"},
		OWASPRefs:      []string{"MCP07:2025"}, Coverage: mcpPartial,
	},
	{
		ID: "MCP-T8", Title: "Network Binding/Isolation Failures",
		ProductControl: "Largely a DEPLOYMENT/OPERATOR concern (bind addresses, segmentation) outside the control plane. The plane contributes the DNS-rebinding Origin defense at the RS and an isolated-by-construction sandbox runner for scenario testing (no store/network/secret handle; mock-miss never reaches a real resource).",
		Evidence:       []string{"connectors/mcp/rs.go (Origin/DNS-rebinding)", "modules/sandbox (isolated mock runner)"},
		OWASPRefs:      nil, Coverage: mcpReferenced,
	},
	{
		ID: "MCP-T9", Title: "Trust Boundary and Privilege Design Failures",
		ProductControl: "MCP servers are UNTRUSTED by design: tool annotations never lower a gate (required scope + destructive classification resolve from the SERVER-OWNED toolset map, not the tool's self-description), and token passthrough is structurally impossible — the upstream request carries no inbound token (a separate OAuth client mints its own audience-bound token), the confused-deputy defense. ID-JAG delegation reaches only operator-approved servers.",
		Evidence:       []string{"connectors/mcp/toolset.go (server-owned policy)", "connectors/mcp/rs.go (no-passthrough upstream)", "connectors/mcp/idjag.go (delegation to approved servers only)", "connectors/mcp (annotations UNTRUSTED)"},
		OWASPRefs:      []string{"MCP01:2025", "MCP02:2025"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP-T10", Title: "Resource Management/Rate Limiting Absence",
		ProductControl: "Largely a DEPLOYMENT/SERVER-SIDE concern: the inline MCP PEP does not meter tools/call rates. Relevant controls exist elsewhere — the plane's own API has inbound rate-limit middleware (RateLimit-* headers) and FinOps bounds agent consumption detectively (the LLM10 Unbounded Consumption counterpart).",
		Evidence:       []string{"core/api (inbound rate-limit middleware)", "modules/finops (consumption bounds)"},
		OWASPRefs:      nil, Coverage: mcpReferenced,
	},
	{
		ID: "MCP-T11", Title: "Supply Chain and Lifecycle Security Failures",
		ProductControl: "The core target: registry SYNC over the org's owned namespaces flags YANKED/deprecated and UNMANAGED publications; the internal registry pins approved versions and flags RUNNING-vs-PINNED drift (rug-pull); passive discovery flags shadow servers absent from any verified namespace; and adds deprecation-aware posture, a private sub-registry, and federated catalogs whose entries pass signature/provenance/SBOM verification through the signed-admission flow before adoption.",
		Evidence:       []string{"connectors/mcp/registrysync.go (yank/unmanaged)", "connectors/mcp/internalregistry.go (version drift)", "connectors/mcp/deprecation.go (deprecation-aware posture)", "connectors/mcp/subregistry.go (private sub-registry)", "connectors/mcp/federation.go (federated catalogs)", "modules/catalog/mcpadmission.go (signed federated-entry admission)", "finding:mcp_shadow", "finding:mcp_provenance"},
		OWASPRefs:      []string{"MCP04:2025", "MCP09:2025"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP-T12", Title: "Insufficient Logging, Monitoring, and Auditability",
		ProductControl: "Tamper-evident hash-chained, Ed25519-signed audit ledger with offline re-verification plus standards SIEM/OTel export (OCSF/CEF/LEEF/syslog/OTLP); every inline tools/call gate decision (allow/deny) is recorded, and each destructive call opens a governed approval written to that ledger.",
		Evidence:       []string{"core/audit (hash-chain ledger)", "sdk/siemwire (OBS-02/08)", "connectors/mcp/gate.go (GateAuditor)", "cmd/olivares/mcpgateway.go"},
		OWASPRefs:      []string{"MCP08:2025"}, Coverage: mcpAddressed,
	},
}

// CoSAIMCPSecurity returns a copy of the CoSAI MCP Security control mapping
// (positioning / compliance read model —).
func CoSAIMCPSecurity() []CoSAIMCPControl {
	out := make([]CoSAIMCPControl, len(cosaiMCPSecurity))
	copy(out, cosaiMCPSecurity)
	return out
}

// cosaiByFindingKind maps a connector/security finding KIND to the CoSAI MCP
// Security category it most directly evidences. Intentionally conservative — only
// the unambiguous mappings — so a finding is tagged with a category it truly
// exemplifies, not a stretch. Kinds not listed return "".
//
// NOTE the deliberate absence of "mcp_posture", mirroring mcpTop10ByFindingKind: a
// posture finding's category depends on the SPECIFIC issue (a homoglyph name is
// boundary/poisoning territory, an over-broad scope is MCP-T2, an injected
// description MCP-T4, …), so a single kind→id mapping would be wrong — the precise
// id rides in the finding's TITLE. "mcp_revision" is likewise excluded (protocol-
// revision hygiene is not unambiguously one category).
var cosaiByFindingKind = map[string]string{
	"mcp_shadow":     "MCP-T11", // shadow servers = lifecycle/supply chain
	"mcp_provenance": "MCP-T11", // registry provenance = supply chain
	"mcp_auth":       "MCP-T1",  // Improper Authentication and Identity Management
	"mcp_surface":    "MCP-T4",  // context-injection vectors = instruction boundary
}

// CoSAIMCPForFindingKind returns the CoSAI MCP Security category id a finding kind
// evidences, or "" when there is no unambiguous mapping (the connector's
// FindingReport wire type carries no tag field, so the canonical mapping lives
// here, alongside MCPTop10ForFindingKind).
func CoSAIMCPForFindingKind(kind string) string {
	return cosaiByFindingKind[kind]
}
