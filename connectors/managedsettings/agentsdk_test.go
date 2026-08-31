// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"
)

func TestAgentSDKGuardrailToPolicy(t *testing.T) {
	g := AgentSDKGuardrail{
		MaxMode:                ModePlan,
		ForbidBypass:           true,
		ForbidAuto:             true,
		Deny:                   []string{"Bash(rm *)", "  "}, // blank dropped
		Ask:                    []string{"WebFetch"},
		ForceManagedHooksOnly:  true,
		PEPHookCommand:         "/usr/local/bin/olivares-pep",
		ParentSettingsBehavior: ParentMerge,
	}
	p, err := g.ToPolicy()
	if err != nil {
		t.Fatalf("ToPolicy: %v", err)
	}
	if p.Permissions.DefaultMode != ModePlan {
		t.Errorf("defaultMode = %q", p.Permissions.DefaultMode)
	}
	if !p.Permissions.DisableBypassPermissionsMode || !p.Permissions.DisableAutoMode {
		t.Error("ForbidBypass/ForbidAuto must set the disable* locks")
	}
	if len(p.Permissions.Deny) != 1 || p.Permissions.Deny[0] != "Bash(rm *)" {
		t.Errorf("deny rules = %v (blank must be dropped)", p.Permissions.Deny)
	}
	if !p.AllowManagedHooksOnly || p.ParentSettingsBehavior != ParentMerge {
		t.Errorf("hook lockdown / parent behavior not mapped: %+v", p)
	}
	if !hasPreToolUseHook(p.Hooks) {
		t.Error("PEPHookCommand must distribute a managed PreToolUse PEP hook")
	}
}

func TestRenderAgentSDKGuardrailIsValid(t *testing.T) {
	g := AgentSDKGuardrail{MaxMode: ModePlan, ForbidBypass: true, ForbidAuto: true, Deny: []string{"Bash(rm *)"}}
	b, err := RenderAgentSDKGuardrail(g)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// The rendered managed object must be a precedence-winning managed-settings.json that
	// the connector's own verified validator accepts.
	if issues := ValidateJSON(b); len(issues) != 0 {
		t.Errorf("rendered guardrail must be valid managed-settings, issues: %v", issues)
	}
	if !HasAnyKeys(b) {
		t.Error("a non-trivial guardrail must deliver keys")
	}
	// The disable lock must render in Claude Code's non-boolean wire form.
	if !strings.Contains(string(b), `"disableBypassPermissionsMode": "disable"`) {
		t.Errorf("ForbidBypass must render the disable marker:\n%s", b)
	}
}

func TestValidateAgentSDKGuardrailHonestyCheck(t *testing.T) {
	// A safe-looking MaxMode WITHOUT the disable lock does not actually forbid bypass.
	issues, err := ValidateAgentSDKGuardrail(AgentSDKGuardrail{MaxMode: ModeAcceptEdits})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !containsSubstr(issues, "ForbidBypass") {
		t.Errorf("a non-bypass MaxMode without ForbidBypass must be flagged: %v", issues)
	}
	// With the locks set, no SDK-specific issue (and the document validates).
	issues, err = ValidateAgentSDKGuardrail(AgentSDKGuardrail{MaxMode: ModePlan, ForbidBypass: true, ForbidAuto: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if containsSubstr(issues, "ForbidBypass") || containsSubstr(issues, "ForbidAuto") {
		t.Errorf("a locked guardrail must raise no disable-lock issues: %v", issues)
	}
}

func TestAgentSDKPrecedenceContract(t *testing.T) {
	facts := AgentSDKPrecedence()
	if len(facts) == 0 {
		t.Fatal("precedence contract must enumerate facts")
	}
	for _, f := range facts {
		if f.ManagedKey == "" || f.OverridesSDKOption == "" || f.Guarantee == "" {
			t.Errorf("incomplete precedence fact: %+v", f)
		}
	}
	// The deny-rule fact must state it binds under bypassPermissions (the headline guarantee).
	var found bool
	for _, f := range facts {
		if strings.Contains(f.ManagedKey, "permissions.deny") && strings.Contains(f.Guarantee, "bypassPermissions") {
			found = true
		}
	}
	if !found {
		t.Error("permissions.deny precedence fact must state it binds in bypassPermissions")
	}
}

func TestAgentSDKPrecedencePreviewHonesty(t *testing.T) {
	lines := AgentSDKPrecedencePreview(AgentSDKGuardrail{MaxMode: ModePlan, ForbidBypass: true})
	var sawCaveat, sawMapping, sawDelivery bool
	for _, l := range lines {
		if strings.Contains(l.Note, "ANTHROPIC_BASE_URL") && strings.Contains(l.Note, "NOT impossible to bypass") {
			sawCaveat = true
		}
		if l.Scope == "overrides-sdk-option" {
			sawMapping = true
		}
		if l.Scope == "managed-tier" { // reused from DeliveryPreview
			sawDelivery = true
		}
	}
	if !sawCaveat {
		t.Error("preview MUST carry the verified-deployed / not-impossible-to-bypass ANTHROPIC_BASE_URL caveat")
	}
	if !sawMapping {
		t.Error("preview must map managed keys to the SDK options they override")
	}
	if !sawDelivery {
		t.Error("preview must reuse the generic DeliveryPreview precedence facts")
	}
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
