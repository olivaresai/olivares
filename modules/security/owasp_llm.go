// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

// OWASP Top 10 for LLM Applications 2025 — the control-plane CROSSWALK.
// Four of the ten risks are tagged directly by a guardrail detector (LLM01 prompt
// injection, LLM02 sensitive-information disclosure, LLM05 improper output handling,
// LLM07 system-prompt leakage); the rest are evidenced by OTHER modules — the moat is
// the access-map drift that IS Excessive Agency (LLM06), FinOps that bounds
// consumption (LLM10), and the knowledge/RAG perimeter that owns vector/embedding
// risk (LLM08). This file states each mapping with primary-source backing so the same
// risk an auditor asks about is traceable to the exact product control that addresses
// it, with an HONEST coverage grade — not an inflated "we cover all ten".
//
// SOURCE (verified verbatim, /tmp/s27-taxonomies.md + genai.owasp.org, jun-2026):
//
//	OWASP — "OWASP Top 10 for LLM Applications 2025".
//	https://genai.owasp.org/llm-top-10/
//	  LLM01:2025 Prompt Injection
//	  LLM02:2025 Sensitive Information Disclosure
//	  LLM03:2025 Supply Chain
//	  LLM04:2025 Data and Model Poisoning
//	  LLM05:2025 Improper Output Handling
//	  LLM06:2025 Excessive Agency
//	  LLM07:2025 System Prompt Leakage
//	  LLM08:2025 Vector and Embedding Weaknesses
//	  LLM09:2025 Misinformation
//	  LLM10:2025 Unbounded Consumption
//
// REFUTATION (deliberate): LLM09 Misinformation is intentionally NOT a
// guardrail detector tag — the content filter flags a conservative set of harmful-
// content REQUESTS, which is a different concern from a model emitting false
// information, and there is no canonical detector for "misinformation" that would not
// over-claim (contentfilter.go owasp:''). LLM09 is recorded here as `Referenced`
// (relevant controls exist) and is NEVER asserted as a detector-backed claim.

// llmCoverage reuses the honest coverage grades (addressed / partial / referenced)
// shared with the OWASP MCP Top 10 mapping (owasp_mcp.go) — the same grading line so
// neither crosswalk inflates coverage.
type llmCoverage = mcpCoverage

// LLMControl is one OWASP LLM Top 10 2025 entry mapped to the product's controls.
type LLMControl struct {
	// ID is the OWASP id with its year designation (e.g. "LLM06:2025").
	ID string
	// Title is the verbatim published 2025 title.
	Title string
	// DetectorTagged is true when a security guardrail detector emits this id directly
	// on a finding (LLM01/02/05/07); false when the risk is evidenced by another module
	// (the crosswalk) or is deliberately untagged (LLM09).
	DetectorTagged bool
	// ProductControl is the capability/module that addresses the risk.
	ProductControl string
	// Evidence lists the detector rules, finding kinds and/or module references.
	Evidence []string
	// Coverage is the honest completeness grade.
	Coverage llmCoverage
}

// llmTop10 is the verified LLM01–LLM10 catalog mapped to product controls. Titles are
// quoted verbatim from the 2025 list; coverage is graded honestly (a crosswalk to
// another module is `referenced`/`partial`, not `addressed`, unless a built control
// directly covers the risk).
var llmTop10 = []LLMControl{
	{
		ID: "LLM01:2025", Title: "Prompt Injection", DetectorTagged: true,
		ProductControl: "Deterministic prompt-injection guardrail (direct + indirect/RAG, role-override, payload-splitting, obfuscation) over every surface; indirect injection cross-tagged ASI01 + AML.T0051.001.",
		Evidence:       []string{"modules/security/injection.go", "modules/security/screen.go", "modules/security/jailbreak.go (many-shot)"},
		Coverage:       mcpAddressed,
	},
	{
		ID: "LLM02:2025", Title: "Sensitive Information Disclosure", DetectorTagged: true,
		ProductControl: "Secret/PII detectors (Presidio-aligned entities + key=value + Luhn-checked cards) on input and output, minimal-data by construction (values never echoed); markdown/image exfil heuristics.",
		Evidence:       []string{"modules/security/pii.go", "modules/security/outputvalidation.go (output-leaks-secret)", "AML.T0024/T0057"},
		Coverage:       mcpAddressed,
	},
	{
		ID: "LLM03:2025", Title: "Supply Chain", DetectorTagged: false,
		ProductControl: "MCP registry provenance + per-server protocol hygiene (OWASP MCP04, owasp_mcp.go), the MCP/connector catalog, and signed releases + SBOM + pinned deps (supply_chain capability). Cross-walks to ASI04.",
		Evidence:       []string{"modules/security/owasp_mcp.go (MCP04)", "finding:mcp_provenance", "docs/08 §7 (signed releases/SBOM)"},
		Coverage:       mcpPartial,
	},
	{
		ID: "LLM04:2025", Title: "Data and Model Poisoning", DetectorTagged: false,
		ProductControl: "Red-team tool/data-poisoning battery (module XVIII) and eval regression findings (module XII) evidence poisoning resilience; RAG-poisoning maps to AML.T0070. The control plane does NOT inspect training datasets.",
		Evidence:       []string{"modules/redteam (tool_poisoning suite)", "modules/evals (eval_regression)", "AML.T0070/T0099"},
		Coverage:       mcpReferenced,
	},
	{
		ID: "LLM05:2025", Title: "Improper Output Handling", DetectorTagged: true,
		ProductControl: "Output-validation guardrail: the model's output is treated as untrusted (system-prompt leakage, markdown/image render exfil, secret/PII leak in the response).",
		Evidence:       []string{"modules/security/outputvalidation.go"},
		Coverage:       mcpAddressed,
	},
	{
		ID: "LLM06:2025", Title: "Excessive Agency", DetectorTagged: false,
		ProductControl: "The MOAT: access-map permitted-vs-observed R/RW least-privilege drift (module III) IS Excessive Agency made measurable — an agent acting beyond its granted scope is a drift edge. Reinforced by guardrail ASI03 (privilege abuse) / ASI10 (rogue) text signals.",
		Evidence:       []string{"modules/access-map (drift)", "capability:least_privilege_drift", "modules/security/owasp_agentic.go (ASI03/ASI10)"},
		Coverage:       mcpAddressed,
	},
	{
		ID: "LLM07:2025", Title: "System Prompt Leakage", DetectorTagged: true,
		ProductControl: "Output-validation rules that flag a response reciting its system prompt / hidden instructions (LLM07 + MITRE ATLAS AML.T0056 Extract LLM System Prompt).",
		Evidence:       []string{"modules/security/outputvalidation.go (leaks-system-prompt)", "AML.T0056"},
		Coverage:       mcpAddressed,
	},
	{
		ID: "LLM08:2025", Title: "Vector and Embedding Weaknesses", DetectorTagged: false,
		ProductControl: "Knowledge/RAG perimeter: data-lineage proves retrieved content stays within the perimeter (module VIII), and the indirect-injection detector screens retrieved/tool (RAG) content for smuggled instructions. RAG poisoning maps to AML.T0070.",
		Evidence:       []string{"modules/knowledge (data_lineage)", "modules/security/injection.go (indirect/RAG surface)", "AML.T0070"},
		Coverage:       mcpPartial,
	},
	{
		ID: "LLM09:2025", Title: "Misinformation", DetectorTagged: false,
		ProductControl: "DELIBERATELY untagged by any guardrail detector: the conservative content filter flags harmful-content REQUESTS, a different concern from a model emitting false information; no canonical detector is asserted rather than over-claim (contentfilter.go owasp:'').",
		Evidence:       []string{"modules/security/contentfilter.go (owasp:'' — intentional)"},
		Coverage:       mcpReferenced,
	},
	{
		ID: "LLM10:2025", Title: "Unbounded Consumption", DetectorTagged: false,
		ProductControl: "FinOps token/compute/cost accounting + budgets bound consumption (module: finops.cost_sample, budgets), and the guardrail ASI08 (Cascading Failures) flags unbounded retry/recursion/fan-out instructions that drive runaway consumption.",
		Evidence:       []string{"modules/finops (cost_sample, budgets)", "modules/security/owasp_agentic.go (ASI08)"},
		Coverage:       mcpPartial,
	},
}

// LLMTop10 returns a copy of the OWASP LLM Top 10 2025 control crosswalk (positioning /
// compliance read model). It is the single source for "which control evidences which
// LLM risk", consumed by compliance reporting and product positioning.
func LLMTop10() []LLMControl {
	out := make([]LLMControl, len(llmTop10))
	copy(out, llmTop10)
	return out
}
