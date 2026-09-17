// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"testing"
)

// TestEvidenceDigestIsStableAcrossRuleOrder proves the digest is a property of the RULE
// SET, not of a map walk. Go randomizes map iteration per range statement, so a digest
// that consumed p.dlp.rules directly would differ between two calls on the SAME value —
// this repeats the call enough times that such an implementation cannot pass by luck.
func TestEvidenceDigestIsStableAcrossRuleOrder(t *testing.T) {
	pol := PolicyWithDLPRules(defaultProxyPolicy(), map[string]string{
		"secret.credential": dlpDeny, "pii.email": dlpAllow, "pii.card": dlpDeny,
		"unscanned": dlpAllow, "*": dlpAllow,
	})
	want := pol.EvidenceDigest()
	for i := 0; i < 64; i++ {
		if got := pol.EvidenceDigest(); got != want {
			t.Fatalf("digest is not stable across calls (iteration %d): %x != %x", i, got, want)
		}
	}
	// The same rule content authored in a different literal order is the same rule set.
	other := PolicyWithDLPRules(defaultProxyPolicy(), map[string]string{
		"*": dlpAllow, "unscanned": dlpAllow, "pii.card": dlpDeny,
		"pii.email": dlpAllow, "secret.credential": dlpDeny,
	})
	if other.EvidenceDigest() != want {
		t.Fatalf("digest depends on rule presentation order")
	}
}

// TestEvidenceDigestChangesOnEveryGoverningDimension varies exactly one dimension at a
// time and requires the digest to move. A field this table forgets is a field the
// evidence would silently stop binding, so the table IS the coverage claim.
func TestEvidenceDigestChangesOnEveryGoverningDimension(t *testing.T) {
	base := func() ProxyPolicy { return PolicyWithDLPRules(defaultProxyPolicy(), nil) }
	baseline := base().EvidenceDigest()

	tests := []struct {
		name   string
		mutate func(*ProxyPolicy)
	}{
		{"configured", func(p *ProxyPolicy) { p.Configured = true }},
		{"fail_open", func(p *ProxyPolicy) { p.FailOpen = true }},
		{"response_dlp_mode", func(p *ProxyPolicy) { p.ResponseDLPMode = ResponseDLPFlag }},
		{"record_mandatory", func(p *ProxyPolicy) { p.RecordMandatory = false }},
		{"record_mandatory_chosen", func(p *ProxyPolicy) { p.RecordMandatoryChosen = true }},
		{"gate_model_access", func(p *ProxyPolicy) { p.GateModelAccess = false }},
		{"gate_budget", func(p *ProxyPolicy) { p.GateBudget = false }},
		{"gate_residency", func(p *ProxyPolicy) { p.GateResidency = false }},
		{"gate_context_window", func(p *ProxyPolicy) { p.GateContextWindow = false }},
		{"gate_dlp_request", func(p *ProxyPolicy) { p.GateDLPRequest = false }},
		{"gate_dlp_response", func(p *ProxyPolicy) { p.GateDLPResponse = false }},
		{"ceilings_enforce", func(p *ProxyPolicy) { p.Ceilings.Enforce = true }},
		{"ceilings_max_tokens", func(p *ProxyPolicy) { p.Ceilings.MaxTokens = 1 }},
		{"ceilings_max_tool_uses", func(p *ProxyPolicy) { p.Ceilings.MaxToolUses = 1 }},
		{"ceilings_task_budget_tokens", func(p *ProxyPolicy) { p.Ceilings.TaskBudgetTokens = 1 }},
		{"dlp_rule_added", func(p *ProxyPolicy) { *p = PolicyWithDLPRules(*p, map[string]string{"pii.email": dlpDeny}) }},
		{"dlp_action_changed", func(p *ProxyPolicy) {
			*p = PolicyWithDLPRules(*p, map[string]string{dlpClassSecret: dlpAllow, dlpClassUnscanned: dlpDeny, dlpClassAny: dlpAllow})
		}},
		{"dlp_class_changed", func(p *ProxyPolicy) {
			*p = PolicyWithDLPRules(*p, map[string]string{dlpClassSecret: dlpDeny, dlpClassUnscanned: dlpDeny, dlpClassAny: dlpDeny})
		}},
	}

	seen := map[[32]byte]string{baseline: "baseline"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base()
			tt.mutate(&p)
			got := p.EvidenceDigest()
			if got == baseline {
				t.Fatalf("%s does not change the digest; the evidence would not bind it", tt.name)
			}
			if prior, clash := seen[got]; clash {
				t.Fatalf("%s collides with %s", tt.name, prior)
			}
			seen[got] = tt.name
		})
	}
}

// TestEvidenceDigestFramingIsUnambiguous shows the length prefixes do real work: two
// policies whose field VALUES concatenate to the same byte string must still differ.
func TestEvidenceDigestFramingIsUnambiguous(t *testing.T) {
	a := PolicyWithDLPRules(defaultProxyPolicy(), map[string]string{"ab": "c" + dlpAllow[1:]})
	b := PolicyWithDLPRules(defaultProxyPolicy(), map[string]string{"a": "bc" + dlpAllow[1:]})
	if a.EvidenceDigest() == b.EvidenceDigest() {
		t.Fatal("class/action boundary is not framed: (ab,c…) and (a,bc…) collide")
	}
}

// TestEvidenceDigestIgnoresNothingOnTheZeroValue guards the one shape a caller can
// construct outside this package: the zero ProxyPolicy has no rules at all, and it must
// not hash the same as the stock posture, which denies secrets and unscanned content.
func TestEvidenceDigestZeroPolicyDiffersFromStock(t *testing.T) {
	if (ProxyPolicy{}).EvidenceDigest() == defaultProxyPolicy().EvidenceDigest() {
		t.Fatal("the zero policy (every gate off, no rules) hashes like the safe default")
	}
}
