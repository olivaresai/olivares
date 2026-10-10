// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
)

func TestHookRuleMatchPathGlobDenyEtcSecrets(t *testing.T) {
	r := PolicyRule{
		Tool:         "Read",
		ResourceKind: ResourceKindFile,
		Mode:         "read",
		Paths:        []string{"/etc/secrets/**"},
		Decision:     "deny",
	}
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: ResourceKindFile,
		ResourceRef:  "/etc/secrets/prod.key",
		Mode:         "read",
	}
	if !hookRuleMatches(r, in) {
		t.Fatal("/etc/secrets/** path-scoped deny rule must match a file under that tree")
	}
	in.ResourceRef = "/etc/public/prod.key"
	if hookRuleMatches(r, in) {
		t.Fatal("/etc/secrets/** path-scoped deny rule must not match a sibling path")
	}
	in.ResourceKind = "shell"
	in.ResourceRef = "/etc/secrets/prod.key"
	if hookRuleMatches(r, in) {
		t.Fatal("path-scoped rules must not match non-file resources")
	}
}

func TestHookRuleMatchSubtreeSegmentBoundary(t *testing.T) {
	r := PolicyRule{
		ResourceKind: ResourceKindFile,
		Subtree:      "/a/b",
		Decision:     "deny",
	}
	in := claude.HookDecisionInput{Event: "PreToolUse", Tool: "Read", ResourceKind: ResourceKindFile, ResourceRef: "/a/b/c", Mode: "read"}
	if !hookRuleMatches(r, in) {
		t.Fatal("subtree rule must match a descendant")
	}
	in.ResourceRef = "/a/b"
	if !hookRuleMatches(r, in) {
		t.Fatal("subtree rule must match the subtree root itself")
	}
	in.ResourceRef = "/a/bc"
	if hookRuleMatches(r, in) {
		t.Fatal("subtree rule must not match a path that only shares a string prefix")
	}
}

func TestHookPolicyDenyOverridesPathRule(t *testing.T) {
	pol := PolicyDoc{
		Default:        "allow",
		PathPrecedence: "deny-overrides",
		Rules: []PolicyRule{
			{Tool: "Read", ResourceKind: ResourceKindFile, Paths: []string{"/etc/**"}, Decision: "allow", Reason: "broad allow"},
			{Tool: "Read", ResourceKind: ResourceKindFile, Paths: []string{"/etc/secrets/**"}, Decision: "deny", Reason: "secret subtree"},
		},
	}
	disp, matched := evalHookPolicy(pol, claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: ResourceKindFile,
		ResourceRef:  "/etc/secrets/key",
		Mode:         "read",
	})
	if !matched || disp.decision != claude.DecisionDeny || disp.reason != "secret subtree" {
		t.Fatalf("deny-overrides must let a later path deny beat an earlier allow, got matched=%v disp=%+v", matched, disp)
	}
}

func TestHookPolicyFirstMatchPathRuleDefault(t *testing.T) {
	pol := PolicyDoc{
		Default: "deny",
		Rules: []PolicyRule{
			{Tool: "Read", ResourceKind: ResourceKindFile, Paths: []string{"/etc/**"}, Decision: "allow", Reason: "broad allow"},
			{Tool: "Read", ResourceKind: ResourceKindFile, Paths: []string{"/etc/secrets/**"}, Decision: "deny", Reason: "secret subtree"},
		},
	}
	disp, matched := evalHookPolicy(pol, claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: ResourceKindFile,
		ResourceRef:  "/etc/secrets/key",
		Mode:         "read",
	})
	if !matched || disp.decision != claude.DecisionAllow || disp.reason != "broad allow" {
		t.Fatalf("first-match default must keep the earlier allow, got matched=%v disp=%+v", matched, disp)
	}
}

func TestHookPolicyOnUnresolvedPathAskAndDeny(t *testing.T) {
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: ResourceKindFile,
		ResourceRef:  "relative/secret.txt",
		Mode:         "read",
	}
	pol := PolicyDoc{
		Default: "allow",
		Rules: []PolicyRule{
			{Tool: "Read", ResourceKind: ResourceKindFile, Paths: []string{"/repo/**"}, Decision: "allow"},
		},
	}
	disp, matched := evalHookPolicy(pol, in)
	if !matched || disp.decision != claude.DecisionAsk {
		t.Fatalf("unresolved file path under a path-scoped policy must ask by default, got matched=%v disp=%+v", matched, disp)
	}
	if strings.Contains(disp.reason, in.ResourceRef) {
		t.Fatalf("unresolved-path reason must not echo the raw path, got %q", disp.reason)
	}

	pol.OnUnresolvedPath = "deny"
	disp, matched = evalHookPolicy(pol, in)
	if !matched || disp.decision != claude.DecisionDeny {
		t.Fatalf("on_unresolved_path=deny must deny, got matched=%v disp=%+v", matched, disp)
	}
}
