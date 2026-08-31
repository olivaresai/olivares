// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"
)

// TestScopeOutranks pins the verified hierarchy: managed cannot be overridden (it
// outranks EVERY other scope, including CLI args), and the chain is strictly ordered.
func TestScopeOutranks(t *testing.T) {
	order := []SettingsScope{ScopeManaged, ScopeCLIArgs, ScopeLocal, ScopeProject, ScopeUser}
	// Managed outranks all others.
	for _, s := range order[1:] {
		if !ScopeOutranks(ScopeManaged, s) {
			t.Errorf("managed must outrank %s", s)
		}
		if ScopeOutranks(s, ScopeManaged) {
			t.Errorf("%s must NOT outrank managed (managed cannot be overridden)", s)
		}
	}
	// Each scope outranks the one below it.
	for i := 0; i < len(order)-1; i++ {
		if !ScopeOutranks(order[i], order[i+1]) {
			t.Errorf("%s must outrank %s", order[i], order[i+1])
		}
	}
	// CLI args outrank local/project/user but not managed (the subtle correct point).
	if !ScopeOutranks(ScopeCLIArgs, ScopeUser) || ScopeOutranks(ScopeCLIArgs, ScopeManaged) {
		t.Error("CLI args must sit below managed and above user")
	}
}

// TestEffectiveScope: an override setting resolves to the highest-precedence present
// scope, regardless of input order; nothing present → ok=false.
func TestEffectiveScope(t *testing.T) {
	if s, ok := EffectiveScope([]SettingsScope{ScopeUser, ScopeProject, ScopeManaged}); !ok || s != ScopeManaged {
		t.Errorf("managed present must win, got %s ok=%v", s, ok)
	}
	if s, ok := EffectiveScope([]SettingsScope{ScopeProject, ScopeUser}); !ok || s != ScopeProject {
		t.Errorf("project must beat user, got %s", s)
	}
	if _, ok := EffectiveScope(nil); ok {
		t.Error("no scopes present → ok must be false")
	}
}

// TestAllowlistAndDenylistMerge: a managed-only lockdown restricts the ALLOWLIST to
// managed, but the DENYLIST always merges from all scopes.
func TestAllowlistAndDenylistMerge(t *testing.T) {
	if got := AllowlistSources(true); len(got) != 1 || got[0] != ScopeManaged {
		t.Errorf("locked allowlist must be managed-only, got %v", got)
	}
	if got := AllowlistSources(false); len(got) != len(settingsPrecedence) {
		t.Errorf("unlocked allowlist must merge all scopes, got %v", got)
	}
	if !DenylistAlwaysMerges || !RulesMerge {
		t.Error("denylist and permission rules must always merge (verified invariants)")
	}
}

// TestEnforceBeforeExec: the marketplace allowlist is enforced BEFORE any download (the
// enforce-before-network/fs guarantee), and the ordered points are non-empty.
func TestEnforceBeforeExec(t *testing.T) {
	pts := EnforceBeforeExec()
	if len(pts) == 0 {
		t.Fatal("expected enforce-before-exec points")
	}
	foundMarket := false
	for _, p := range pts {
		if strings.Contains(p.Gate, "marketplace") {
			foundMarket = true
			if !strings.Contains(p.BeforeOp, "before downloading") && !strings.Contains(p.BeforeOp, "filesystem") {
				t.Errorf("marketplace enforcement must be before download/fs, got %q", p.BeforeOp)
			}
		}
	}
	if !foundMarket {
		t.Error("expected a marketplace enforce-before-exec point")
	}
}

// TestPrecedencePreviewAndDeliveryPreview: the preview surfaces the hierarchy, the
// merge rule, and the enforce-before-exec lines; and DeliveryPreview now includes them.
func TestPrecedencePreviewAndDeliveryPreview(t *testing.T) {
	pre := PrecedencePreview()
	joined := ""
	for _, l := range pre {
		joined += l.Scope + "|" + l.Note + "\n"
	}
	for _, want := range []string{"CANNOT be overridden", "MERGE across all scopes", "enforce-before-exec"} {
		if !strings.Contains(joined, want) {
			t.Errorf("precedence preview missing %q\n%s", want, joined)
		}
	}
	// DeliveryPreview must now carry the precedence chain too (the console's resolved view).
	dj := ""
	for _, l := range DeliveryPreview(true) {
		dj += l.Scope + "\n"
	}
	if !strings.Contains(dj, "enforce-before-exec") || !strings.Contains(dj, "hierarchy") {
		t.Errorf("DeliveryPreview should include precedence lines, got scopes:\n%s", dj)
	}
}
