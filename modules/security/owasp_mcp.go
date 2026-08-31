// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

// OWASP MCP Top 10 control mapping (AIP-07). The product already implements most
// of what the OWASP MCP Top 10 demands (shadow-server discovery, access-map drift,
// tamper-evident audit, MCP-as-untrusted, OAuth-RS auth); this file states that
// with primary-source backing — converting implemented behavior into an auditable,
// sales-grade claim, mirroring the ASI taxonomy in owasp_agentic.go.
//
// SOURCE (verified verbatim against the canonical project index.md, jun-2026):
//
//	OWASP Foundation — "OWASP MCP Top 10" project.
//	https://owasp.org/www-project-mcp-top-10/
//	Status: BETA (Phase 3 of 5: "Beta Release and Pilot Testing"), version v0.1,
//	classified Incubator Project, a LIVING DOCUMENT — titles may change before the
//	Phase-4 Final Release. License CC BY-NC-SA 4.0. Lead: Vandana Verma Sehgal.
//	Entries carry the :2025 designation.
//
// Titles are quoted verbatim from index.md. NOTE on MCP06: the project's README and
// index.md disagree on the same branch — README says "Prompt Injection via
// Contextual Payloads" while index.md (and the only existing content file) says
// "Intent Flow Subversion". We cite index.md ("Intent Flow Subversion") as the
// authoritative live title and record the discrepancy rather than pretend it away.
//
// This is a CONTROL MAPPING (positioning), not a runtime regex detector: each entry
// ties an MCP Top 10 risk to the product capability that addresses it, the evidence
// (finding kinds / file refs), and the AIP it traces to. It feeds compliance
// and positioning, and lets findings be tagged with the right MCP id.

// mcpCoverage grades how completely the product addresses a risk (honest, not
// inflated): addressed (a built control covers it), partial (covered but with a
// known gap/phase), or referenced (relevant control exists elsewhere/cross-cutting).
type mcpCoverage string

const (
	mcpAddressed  mcpCoverage = "addressed"
	mcpPartial    mcpCoverage = "partial"
	mcpReferenced mcpCoverage = "referenced"
)

// MCPControl is one OWASP MCP Top 10 entry mapped to the product's controls.
type MCPControl struct {
	// ID is the OWASP id with its year designation (e.g. "MCP09:2025").
	ID string
	// Title is the verbatim published title (index.md).
	Title string
	// ProductControl is the capability that addresses the risk.
	ProductControl string
	// Evidence lists the finding kinds and/or code references that evidence it.
	Evidence []string
	// AIPRefs are the gap ids that track the relevant work.
	AIPRefs []string
	// Coverage is the honest completeness grade.
	Coverage mcpCoverage
}

// mcpTop10 is the verified MCP01–MCP10 catalog mapped to product controls.
var mcpTop10 = []MCPControl{
	{
		ID: "MCP01:2025", Title: "Token Mismanagement & Secret Exposure",
		ProductControl: "Minimal-data capture (connectors never persist tokens/secrets; refs scrubbed, connectors/internal/redact) AND token-passthrough forbidden by design ENFORCE-side: the inline MCP Resource-Server PEP (AIP-02) never relays an inbound token — the upstream request (mcpc.UpstreamRequest) carries no token, so the inbound credential is structurally unreachable from any upstream call (a separate OAuth client mints its own audience-bound token). DETECTIVE (AIP-10): the posture scanner flags a credential/secret SHAPE embedded in catalog metadata (a server leaking a token in a tool/prompt description).",
		Evidence:       []string{"connectors/internal/redact", "connectors/mcp/rs.go (no-passthrough upstream)", "connectors/mcp/gate.go", "connectors/mcp/posture.go (secret-in-metadata)", "finding:mcp_auth (token-binding-verified)", "finding:mcp_posture [MCP01]"},
		AIPRefs:        []string{"AIP-02", "AIP-10"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP02:2025", Title: "Privilege Escalation via Scope Creep",
		ProductControl: "Detective (access-map permitted-vs-observed R/RW drift, module III) AND preventive ENFORCE-side: the inline tools/call PEP (AIP-02) is deny-by-default over a SERVER-OWNED tool→scope map, enforces the required scope per call, and answers an insufficient scope with a 403 step-up scope challenge (SEP-835) — scope creep is blocked at call time, not only surfaced. DETECTIVE (AIP-10): the posture scanner flags an OVER-BROAD requested OAuth scope (wildcard/admin/'*'); the RS now advertises scopes_supported derived from the ENFORCED toolset so advertised==enforced.",
		Evidence:       []string{"modules/access-map (drift)", "connectors/mcp/rs.go (deny-by-default scope gate, step-up)", "connectors/mcp/toolset.go", "connectors/mcp/posture.go (over-broad scope)", "finding:mcp_shadow", "finding:mcp_posture [MCP02]"},
		AIPRefs:        []string{"AIP-02", "AIP-10"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP03:2025", Title: "Tool Poisoning",
		ProductControl: "Annotations treated UNTRUSTED (SignalMCPAnnotation + ConfidenceApproximate) AND ENFORCE-side the tools/call gate resolves a tool's required scope + destructive classification from the SERVER-OWNED toolset map, NEVER the tool's own annotation — a poisoned readOnlyHint/destructiveHint cannot lower its gate (AIP-02). DETECTIVE (AIP-10): an ACTIVE posture scanner flags homoglyph/mixed-script and zero-width/bidi-control characters in tool/server NAMES (spoofing) and a POISONED readOnlyHint (a tool claiming read-only whose name/description implies mutation) at introspection time.",
		Evidence:       []string{"connectors/mcp (annotations UNTRUSTED)", "connectors/mcp/toolset.go (server-owned policy)", "connectors/mcp/textscan.go (homoglyph/zero-width)", "connectors/mcp/posture.go (poisoned readOnly hint)", "finding:mcp_surface", "finding:mcp_posture [MCP03]"},
		AIPRefs:        []string{"AIP-04", "AIP-02", "AIP-10"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP04:2025", Title: "Software Supply Chain Attacks & Dependency Tampering",
		ProductControl: "MCP Registry provenance (reverse-DNS namespace + ownership-verification, labeled PREVIEW/self-verify) and per-server protocol-revision hygiene, PLUS (AIP-10): registry SYNC enumerates the org's OWNED namespaces in the public registry (API version PINNED to the frozen /v0.1) and flags YANKED/deprecated and UNMANAGED publications, and an INTERNAL registry pins approved versions and flags RUNNING-vs-PINNED version drift (rug-pull). Internal/owned servers are no longer mislabeled shadows. Adds the DEPRECATION dimension (a server/client still depending on Roots/Sampling/Logging, HTTP+SSE, DCR-only registration or includeContext is an EOL'd protocol dependency — scored into the posture grade, with the official deprecated-features registry ingested as a drift detector) and CATALOG FEDERATION: federated /v0.1 registries (GitHub BYO org/enterprise allowlists) are reconciled like the official one, the Docker MCP Catalog's sha256 pins are checked against the running fleet (pin drift = rug-pull shape; community-built entries without Docker's signature/attestations degrade the score), and federated MCP catalog entries gate their catalog approval on a VERIFIED provenance/SBOM attestation via the signed-admission flow (core/secure/modelsign VerifyAttestation, deny-closed per tenant policy). The plane also SERVES the approved set as a private /v0.1 sub-registry (the official registry rejects private servers).",
		Evidence:       []string{"finding:mcp_provenance", "finding:mcp_revision", "connectors/mcp/registrysync.go (yank/unmanaged)", "connectors/mcp/internalregistry.go (owned namespace + version drift)", "connectors/mcp/deprecation.go (deprecation-aware posture)", "connectors/mcp/federation.go (federated catalogs + Docker pin checks)", "connectors/mcp/subregistry.go (private sub-registry)", "modules/catalog/mcpadmission.go (signed federated-entry admission)", "core/secure/modelsign/attestation.go (SLSA/SBOM attestation verification)", "finding:mcp_provenance [MCP04]", "finding:mcp_posture [MCP04]"},
		AIPRefs:        []string{"AIP-03", "AIP-01", "AIP-10"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP05:2025", Title: "Command Injection & Execution",
		ProductControl: "Guardrail detectors for dangerous tool args / code execution (OWASP-Agentic ASI02 Tool Misuse, ASI05 Unexpected Code Execution). DETECTIVE (AIP-10): the MCP posture scanner flags a tool whose name/description exposes an arbitrary-code/command-execution surface (exec/shell/eval/run-command) at introspection, so the governance layer can hold it to a higher bar.",
		Evidence:       []string{"modules/security/owasp_agentic.go (ASI02/ASI05)", "connectors/mcp/posture.go (executional tool surface)", "finding:mcp_posture [MCP05]"},
		AIPRefs:        []string{"AIP-10"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP06:2025", Title: "Intent Flow Subversion",
		ProductControl: "Prompt-injection / goal-hijack detectors (OWASP-Agentic ASI01) over the tool/output surfaces. (Live index.md title; README differs — see file header.) DETECTIVE (AIP-10): the posture scanner is the STATIC, introspection-time MCP-surface counterpart — it scans tool/prompt DESCRIPTIONS and the server `instructions` for instruction-injection markers and CONCEALED (invisible/bidi) instructions the runtime guardrails never see (the connector emits a hashed finding, not the description).",
		Evidence:       []string{"modules/security/injection.go", "modules/security/owasp_agentic.go (ASI01)", "connectors/mcp/posture.go (description injection)", "connectors/mcp/textscan.go (injection markers + invisible chars)", "finding:mcp_posture [MCP06]"},
		AIPRefs:        []string{"AIP-10"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP07:2025", Title: "Insufficient Authentication & Authorization",
		ProductControl: "INLINE OAuth 2.1 Resource Server PEP (AIP-02): serves RFC 9728 Protected Resource Metadata, emits WWW-Authenticate on 401, and validates every inbound bearer FAIL-CLOSED with MANDATORY audience-binding (RFC 8707 / RFC 9068 'at+jwt' via go-jose; opaque via RFC 7662) — a token not minted for this server is rejected (the confused-deputy defense), cross-audience → 401, insufficient scope → 403. The client side (PRM→AS→PKCE S256 + resource indicators) complements it. Streamable HTTP returns 403 for an invalid Origin. AIP-10: the RS now serves a COMPLETE RFC 9728 PRM (bearer_methods_supported, resource_name/_documentation, scopes_supported as the UNION of operator-declared and enforced-toolset scopes so advertised>=enforced) with OPT-IN RFC 9068 strict at+jwt typing; DETECTIVE-side, an OAuth-protected server advertising NO RFC 9728 resource_metadata is flagged as auth-discovery non-conformant.",
		Evidence:       []string{"connectors/mcp/rs.go", "connectors/mcp/tokenvalidate.go (audience-bind fail-closed + at+jwt)", "connectors/mcp/oauth.go (client side)", "finding:mcp_auth [MCP07]"},
		AIPRefs:        []string{"AIP-02", "AIP-10"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP08:2025", Title: "Lack of Audit and Telemetry",
		ProductControl: "Tamper-evident hash-chained, Ed25519-signed audit ledger with offline re-verification + standards SIEM/OTel export (OCSF/CEF/LEEF/syslog/OTLP); ENFORCE-side every inline tools/call gate decision (allow/deny) is recorded and each destructive call opens a governed approval written to that ledger (AIP-02).",
		Evidence:       []string{"core/audit (hash-chain ledger)", "sdk/siemwire (OBS-02/08)", "connectors/mcp/gate.go (GateAuditor)", "cmd/olivares/mcpgateway.go"},
		AIPRefs:        nil, Coverage: mcpAddressed,
	},
	{
		ID: "MCP09:2025", Title: "Shadow MCP Servers",
		ProductControl: "Passive MCP discovery/inventory cross-referenced against the registry: a server absent from any verified namespace is flagged as a shadow candidate, feeding module III drift. Adds the ALLOWLIST shape: with a federated allowlist registry configured (e.g. the org's GitHub BYO registry), an introspected server absent from it is flagged out-of-allowlist — raised only from a successfully fetched snapshot, never from an error.",
		Evidence:       []string{"finding:mcp_shadow", "modules/inventory (discovery)", "connectors/mcp/federation.go (allowlist membership)"},
		AIPRefs:        []string{"AIP-03"}, Coverage: mcpAddressed,
	},
	{
		ID: "MCP10:2025", Title: "Context Injection & Over-Sharing",
		ProductControl: "Feature-surface findings for advertised elicitation/sampling (user/model input & over-sharing vectors) plus PII/secret content filters on observed surfaces. Adds the RUNTIME observation seam surface.go reserved: a server that actively initiates sampling/createMessage (especially with the deprecated includeContext=thisServer/allServers over-sharing values) or elicitation/create against the zero-capability introspection client is a scored posture issue, not just an advertised suspicion.",
		Evidence:       []string{"finding:mcp_surface [MCP10]", "modules/security/pii.go", "modules/security/contentfilter.go", "connectors/mcp/observer.go + deprecation.go (observed sampling/includeContext/elicitation)", "finding:mcp_posture [MCP10]"},
		AIPRefs:        []string{"AIP-04"}, Coverage: mcpAddressed,
	},
}

// MCPTop10 returns a copy of the OWASP MCP Top 10 control mapping (positioning /
// compliance read model —).
func MCPTop10() []MCPControl {
	out := make([]MCPControl, len(mcpTop10))
	copy(out, mcpTop10)
	return out
}

// mcpTop10ByFindingKind maps a connector/security finding KIND to the OWASP MCP
// Top 10 id it most directly evidences. It is intentionally conservative — only the
// unambiguous mappings — so a finding is tagged with an id it truly exemplifies, not
// a stretch. Kinds not listed return "".
//
// NOTE the deliberate absence of "mcp_posture" (AIP-10): a posture finding's
// MCP id depends on the SPECIFIC issue (a homoglyph name is MCP03, an over-broad scope
// MCP02, an injected description MCP06, …), so a single kind→id mapping would be
// wrong. The precise id therefore rides in the posture finding's TITLE (e.g.
// "[MCP03] …"), exactly as the connector's other multi-vector findings carry it (the
// FindingReport wire type has no MCP tag field).
var mcpTop10ByFindingKind = map[string]string{
	"mcp_shadow":     "MCP09:2025", // Shadow MCP Servers
	"mcp_provenance": "MCP04:2025", // Software Supply Chain
	"mcp_auth":       "MCP07:2025", // Insufficient Authentication & Authorization
	"mcp_surface":    "MCP10:2025", // Context Injection & Over-Sharing (elicitation/sampling)
}

// MCPTop10ForFindingKind returns the OWASP MCP Top 10 id a finding kind evidences,
// or "" when there is no unambiguous mapping. It lets compliance/notify tag MCP
// findings with the framework id (the connector's FindingReport wire type carries
// no tag field, so the canonical mapping lives here).
func MCPTop10ForFindingKind(kind string) string {
	return mcpTop10ByFindingKind[kind]
}
