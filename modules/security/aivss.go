// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"fmt"
	"math"
)

// EXPERIMENTAL: implements the AIVSS v0.8 scoring formula and carries
// the OWASP document's OFFICIAL worked-example reference scores. A reference
// score describes the OWASP document's illustrative scenario for that risk
// class, NOT an assessment of this tenant's deployment; per-deployment scoring
// needs an assessor-supplied CVSS_Base and factor profile. Source:
// https://aivss.owasp.org, AIVSS Scoring System For OWASP Agentic AI Core
// Security Risks v0.8, §3.3-3.5 (formula), Table 5 (reference scores),
// Appendix B (ASI crosswalk). Watch-hook: on AIVSS v1.0, re-verify every
// constant here.

const (
	aivssThreatMultiplierAttacked       = 1.00
	aivssThreatMultiplierProofOfConcept = 0.97 // doc default
	aivssThreatMultiplierUnreported     = 0.50

	aivssMitigationNoWeak  = 1.00
	aivssMitigationPartial = 0.83
	aivssMitigationStrong  = 0.67

	aivssMetadataKey = "aivss_ref"
)

// aivssFactors holds the 10 risk-amplification factors in the document's §2.2
// order. Each factor is scored 0.0, 0.5 or 1.0.
type aivssFactors struct {
	executionAutonomy          float64
	externalToolControlSurface float64
	naturalLanguageInterface   float64
	contextualAwareness        float64
	behavioralNonDeterminism   float64
	opacityReflexivity         float64
	persistentStateRetention   float64
	dynamicIdentity            float64
	multiAgentInteractions     float64
	selfModification           float64
}

func (f aivssFactors) validate() error {
	for i, v := range f.values() {
		if v != 0 && v != 0.5 && v != 1.0 {
			return fmt.Errorf("aivss factor %d = %v, want 0, 0.5 or 1", i+1, v)
		}
	}
	return nil
}

func (f aivssFactors) sum() float64 {
	var out float64
	for _, v := range f.values() {
		out += v
	}
	return out
}

func (f aivssFactors) values() [10]float64 {
	return [10]float64{
		f.executionAutonomy,
		f.externalToolControlSurface,
		f.naturalLanguageInterface,
		f.contextualAwareness,
		f.behavioralNonDeterminism,
		f.opacityReflexivity,
		f.persistentStateRetention,
		f.dynamicIdentity,
		f.multiAgentInteractions,
		f.selfModification,
	}
}

func aivssAARS(cvssBase, factorSum, thm float64) float64 {
	riskGap := 10 - cvssBase
	return riskGap * (factorSum / 10) * thm
}

func aivssScore(cvssBase, factorSum, thm, mitigation float64) (raw float64, rounded float64) {
	raw = (cvssBase + aivssAARS(cvssBase, factorSum, thm)) * mitigation
	return raw, aivssRoundHalfUp1(raw)
}

func aivssRoundHalfUp1(x float64) float64 {
	return math.Floor(x*10+0.5+1e-9) / 10
}

func aivssSeverity(score float64) string {
	switch {
	case score >= 9.0 && score <= 10.0:
		return "Critical"
	case score >= 7.0 && score <= 8.9:
		return "High"
	case score >= 4.0 && score <= 6.9:
		return "Medium"
	case score >= 0.1 && score <= 3.9:
		return "Low"
	default:
		return "none"
	}
}

type aivssReferenceScore struct {
	slug      string
	name      string
	cvssBase  float64
	factorSum float64
	aars      float64
	aivss     float64
	severity  string
}

// Values verbatim from Table 5/§3.6.
var aivssReferenceScores = []aivssReferenceScore{
	{slug: "tool_misuse", name: "Agentic AI Tool Misuse", cvssBase: 9.4, factorSum: 9.0, aars: 0.5238, aivss: 9.9, severity: "Critical"},
	{slug: "access_control_violation", name: "Agent Access Control Violation", cvssBase: 8.7, factorSum: 8.0, aars: 1.0088, aivss: 9.7, severity: "Critical"},
	{slug: "cascading_failures", name: "Agent Cascading Failures", cvssBase: 7.1, factorSum: 8.0, aars: 2.2504, aivss: 9.4, severity: "Critical"},
	{slug: "orchestration_exploitation", name: "Agent Orchestration and Multi-Agent Exploitation", cvssBase: 9.4, factorSum: 9.5, aars: 0.5529, aivss: 10.0, severity: "Critical"},
	{slug: "identity_impersonation", name: "Agent Identity Impersonation", cvssBase: 7.4, factorSum: 7.5, aars: 1.8915, aivss: 9.3, severity: "Critical"},
	{slug: "memory_manipulation", name: "Agent Memory and Context Manipulation", cvssBase: 5.8, factorSum: 7.5, aars: 3.0555, aivss: 8.9, severity: "High"},
	{slug: "critical_systems_interaction", name: "Insecure Agent Critical Systems Interaction", cvssBase: 6.9, factorSum: 7.5, aars: 2.25525, aivss: 9.2, severity: "Critical"},
	{slug: "supply_chain_dependency", name: "Agent Supply Chain and Dependency Risk", cvssBase: 9.3, factorSum: 6.5, aars: 0.44135, aivss: 9.7, severity: "Critical"},
	{slug: "untraceability", name: "Agent Untraceability", cvssBase: 5.3, factorSum: 6.5, aars: 2.96335, aivss: 8.3, severity: "High"},
	{slug: "goal_instruction_manipulation", name: "Agent Goal and Instruction Manipulation", cvssBase: 2.1, factorSum: 6.5, aars: 4.98095, aivss: 7.1, severity: "High"},
}

var asiToAIVSSCoreRisk = map[string]string{
	"ASI01": "goal_instruction_manipulation",
	"ASI02": "tool_misuse",
	"ASI03": "access_control_violation",
	"ASI04": "supply_chain_dependency",
	"ASI05": "tool_misuse",
	"ASI06": "memory_manipulation",
	"ASI07": "orchestration_exploitation",
	"ASI08": "cascading_failures",
	"ASI09": "identity_impersonation",
	"ASI10": "access_control_violation",
}

func aivssMeta(d Detection, base map[string]any) map[string]any {
	if len(d.OWASPASI) == 0 {
		return base
	}

	refs := aivssReferencesBySlug()
	scores := make([]map[string]any, 0, len(d.OWASPASI))
	for _, id := range d.OWASPASI {
		slug, ok := asiToAIVSSCoreRisk[id]
		if !ok {
			continue
		}
		ref, ok := refs[slug]
		if !ok {
			continue
		}
		scores = append(scores, map[string]any{
			"asi":       id,
			"core_risk": slug,
			"aivss":     ref.aivss,
			"severity":  ref.severity,
		})
	}

	// An ASI id with no v0.8 reference mapping (a future ASI revision) yields no
	// entry; if nothing mapped, add no key at all rather than an empty score set.
	if len(scores) == 0 {
		return base
	}
	base[aivssMetadataKey] = map[string]any{
		"version":      "0.8",
		"experimental": true,
		"basis":        "owasp_aivss_v0.8_reference_scenarios",
		"scores":       scores,
	}
	return base
}

func aivssReferencesBySlug() map[string]aivssReferenceScore {
	out := make(map[string]aivssReferenceScore, len(aivssReferenceScores))
	for _, ref := range aivssReferenceScores {
		out[ref.slug] = ref
	}
	return out
}
