// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// observeTestFarFuture is a fixed, well-past-any-run expiry so an observe fixture holds a LIVE
// grant (E3 requires observe_until in the future); the expiry-specific tests set their own.
var observeTestFarFuture = time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)

// setPEPObserve grants the fixture's tenant a LIVE observe window through the operator
// config path (E3: observe needs an unexpired grant, else it enforces).
func setPEPObserve(t *testing.T, f *hookPEPFixture) {
	t.Helper()
	f.dec.Tenants[f.tenant] = hookTenantConfig(t, f.tenant, hookpep.TenantConfig{
		RequireFirm: f.requireFirm, Policy: f.pol,
		Enforcement: hookpep.EnforcementModeObserve, ObserveUntil: observeTestFarFuture.Format(time.RFC3339),
	})
}

// THE second escape test (Codex-found): a POLICY base-deny that would be shadowed must still
// deny when the Bash command carries an INVARIANT ambiguity the path scan could not resolve —
// otherwise a shadowed base-deny hides an un-inspectable command.
func TestObserveBashAmbiguityIsNotShadowable(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-bash@e2e.test")
	// default deny = a POLICY base-deny; a file path rule makes the policy path-scoped so the
	// Bash scanner runs.
	policy := hookpep.PolicyDoc{
		Version: "obs-bash-guard/v1",
		Default: claude.DecisionDeny,
		Rules:   []hookpep.PolicyRule{{ResourceKind: hookpep.ResourceKindFile, Paths: []string{"/etc/**"}, Decision: claude.DecisionDeny}},
	}

	fe := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	if got := decisionOf(fe.call(t, "Bash", map[string]any{"command": "cat /tmp/ok"}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("enforce base-deny must DENY, got %q", got)
	}

	fo := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	setPEPObserve(t, fo)
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": "cat /tmp/ok"}, tok, h.tenantA)); got != claude.DecisionAllow {
		t.Fatalf("observe should SHADOW a policy base-deny on a CLEAN command → allow, got %q", got)
	}
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": `cat "unterminated`}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("observe must NOT shadow an AMBIGUOUS Bash command (escape) → deny, got %q", got)
	}
}

// The firewall is an invariant that must ALWAYS run in observe — including when the local
// policy deny is shadowable (the enforce-mode skip must not silently exempt the call from DLP).
func TestObserveFirewallStillEnforces(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-fw@e2e.test")
	fo := newHookPEPFixture(t, h, hookpep.PolicyDoc{Default: claude.DecisionDeny}, false, fixedEval{allow: true}, false)
	setPEPObserve(t, fo)
	fo.dec.Inspector = &fakeHookInspector{forward: false}
	out := fo.call(t, "Write", map[string]any{"file_path": "/app/c.env", "content": "AKIAIOSFODNN7EXAMPLE"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("firewall (invariant) must run + deny in observe even when the local policy deny is shadowable; got %q (%v)", got, out)
	}
}

// A CLEAN authored Bash path-deny RULE (recognized decision) is business policy → shadowable in
// observe. Regression pin for the raw-deny-pattern over-tag that made every bash-rule deny invariant.
func TestObserveShadowsCleanBashRuleDeny(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-bashrule@e2e.test")
	policy := hookpep.PolicyDoc{
		Version: "obs-bashrule/v1",
		Default: claude.DecisionAllow, // base allows; the Bash RULE is the authored deny
		Rules:   []hookpep.PolicyRule{{ResourceKind: hookpep.ResourceKindShell, Paths: []string{"/etc/**"}, Decision: claude.DecisionDeny, Reason: "no /etc access"}},
	}
	fe := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	if got := decisionOf(fe.call(t, "Bash", map[string]any{"command": "cat /etc/hosts"}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("enforce clean bash-rule deny must DENY, got %q", got)
	}
	fo := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	setPEPObserve(t, fo)
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": "cat /etc/hosts"}, tok, h.tenantA)); got != claude.DecisionAllow {
		t.Fatalf("observe must SHADOW a clean authored bash-RULE deny → allow, got %q", got)
	}
}

// A typo'd Bash rule decision (config error → deny-closed) is ClassInvariant and must DOMINATE a
// shadowable policy base-deny. THE escape: without the fix, the base policy-deny is shadowed and the
// unknown-decision rule is ignored → the command runs.
func TestObserveBashTypoDecisionIsNotShadowable(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-bashtypo@e2e.test")
	policy := hookpep.PolicyDoc{
		Version: "obs-bashtypo/v1",
		Default: claude.DecisionDeny, // a POLICY base-deny that observe WOULD shadow
		Rules:   []hookpep.PolicyRule{{ResourceKind: hookpep.ResourceKindShell, Paths: []string{"/etc/**"}, Decision: "typo-not-a-decision"}},
	}
	fo := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	setPEPObserve(t, fo)
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": "cat /etc/passwd"}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("a typo'd bash deny rule must force invariant deny in observe (escape), not be shadowed; got %q", got)
	}
}
