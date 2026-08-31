// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"testing"
)

// TestPluginAccessLevelSemantics pins the access semantics of each of the four verified
// levels (the console renders these without re-deriving them).
func TestPluginAccessLevelSemantics(t *testing.T) {
	cases := []struct {
		level                         PluginAccessLevel
		label                         string
		installed, removable, visible bool
	}{
		{PluginRequired, "Required", true, false, true},
		{PluginInstalledByDefault, "Installed by default", true, true, true},
		{PluginAvailableForInstall, "Available for install", false, false, true},
		{PluginNotAvailable, "Not available", false, false, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.level), func(t *testing.T) {
			if tc.level.Label() != tc.label {
				t.Errorf("label = %q, want %q", tc.level.Label(), tc.label)
			}
			if tc.level.Installed() != tc.installed {
				t.Errorf("Installed() = %v, want %v", tc.level.Installed(), tc.installed)
			}
			if tc.level.Removable() != tc.removable {
				t.Errorf("Removable() = %v, want %v", tc.level.Removable(), tc.removable)
			}
			if tc.level.Visible() != tc.visible {
				t.Errorf("Visible() = %v, want %v", tc.level.Visible(), tc.visible)
			}
		})
	}
}

// TestResolveOrgWide: with no matching override, the org-wide preference governs.
func TestResolveOrgWide(t *testing.T) {
	p := PluginAccessPolicy{Plugin: "fmt@acme", OrgWide: PluginAvailableForInstall}
	r := p.Resolve([]string{"engineering"})
	if !r.OrgWideApplied || r.Effective != PluginAvailableForInstall {
		t.Fatalf("expected org-wide available, got %+v", r)
	}
	if len(r.FromGroups) != 0 {
		t.Errorf("org-wide resolution should name no groups, got %v", r.FromGroups)
	}
}

// TestResolveGroupOverrideReplacesOrgWide: a single matching override REPLACES org-wide.
func TestResolveGroupOverrideReplacesOrgWide(t *testing.T) {
	p := PluginAccessPolicy{
		Plugin:         "deploy@acme",
		OrgWide:        PluginNotAvailable,
		GroupOverrides: map[string]PluginAccessLevel{"sre": PluginRequired},
	}
	r := p.Resolve([]string{"sre", "engineering"})
	if r.OrgWideApplied {
		t.Error("a matching override must replace org-wide, not fall back to it")
	}
	if r.Effective != PluginRequired {
		t.Fatalf("effective = %q, want required", r.Effective)
	}
	if !r.Installed || r.Removable {
		t.Errorf("required → installed & not removable, got installed=%v removable=%v", r.Installed, r.Removable)
	}
	if len(r.FromGroups) != 1 || r.FromGroups[0] != "sre" {
		t.Errorf("FromGroups = %v, want [sre]", r.FromGroups)
	}
}

// TestResolveMostPermissiveWins: multiple conflicting overrides → most permissive wins.
func TestResolveMostPermissiveWins(t *testing.T) {
	p := PluginAccessPolicy{
		Plugin:  "scanner@acme",
		OrgWide: PluginAvailableForInstall,
		GroupOverrides: map[string]PluginAccessLevel{
			"contractors": PluginNotAvailable,
			"security":    PluginRequired,
			"interns":     PluginAvailableForInstall,
		},
	}
	// Member in contractors+security: security's Required is most permissive.
	r := p.Resolve([]string{"contractors", "security"})
	if r.Effective != PluginRequired {
		t.Fatalf("most-permissive should be required, got %q", r.Effective)
	}
	if len(r.FromGroups) != 1 || r.FromGroups[0] != "security" {
		t.Errorf("FromGroups = %v, want [security]", r.FromGroups)
	}
	if !strings.Contains(r.Reason, "most permissive") {
		t.Errorf("reason should mention most permissive, got %q", r.Reason)
	}
	// A member only in contractors gets the denial (single override).
	if r := p.Resolve([]string{"contractors"}); r.Effective != PluginNotAvailable {
		t.Errorf("single contractors override should deny, got %q", r.Effective)
	}
}

// TestResolveDeterministicTie: two groups with the SAME winning level resolve
// deterministically (both listed, lexicographic order) regardless of input order.
func TestResolveDeterministicTie(t *testing.T) {
	p := PluginAccessPolicy{
		Plugin: "x@acme", OrgWide: PluginNotAvailable,
		GroupOverrides: map[string]PluginAccessLevel{"zeta": PluginRequired, "alpha": PluginRequired},
	}
	r1 := p.Resolve([]string{"zeta", "alpha"})
	r2 := p.Resolve([]string{"alpha", "zeta"})
	if strings.Join(r1.FromGroups, ",") != "alpha,zeta" {
		t.Errorf("FromGroups not stable/sorted: %v", r1.FromGroups)
	}
	if strings.Join(r1.FromGroups, ",") != strings.Join(r2.FromGroups, ",") {
		t.Errorf("resolution not order-independent: %v vs %v", r1.FromGroups, r2.FromGroups)
	}
}

// TestResolveDedupesGroups: a duplicated group name in the member's list (routine in
// IdP-sourced group lists) must be counted ONCE — FromGroups carries it once and the
// single-group reason fires, never the false "multiple groups" branch.
func TestResolveDedupesGroups(t *testing.T) {
	p := PluginAccessPolicy{
		Plugin: "x@acme", OrgWide: PluginNotAvailable,
		GroupOverrides: map[string]PluginAccessLevel{"sre": PluginRequired},
	}
	r := p.Resolve([]string{"sre", "sre", "sre"})
	if r.Effective != PluginRequired {
		t.Fatalf("effective = %q, want required", r.Effective)
	}
	if len(r.FromGroups) != 1 || r.FromGroups[0] != "sre" {
		t.Errorf("duplicate group must appear once in FromGroups, got %v", r.FromGroups)
	}
	if strings.Contains(r.Reason, "multiple") {
		t.Errorf("a single distinct group must not report a multi-group conflict: %q", r.Reason)
	}
}

// TestValidatePluginAccessPolicy covers the required-ref and known-level checks.
func TestValidatePluginAccessPolicy(t *testing.T) {
	ok := PluginAccessPolicy{Plugin: "p@m", OrgWide: PluginRequired, GroupOverrides: map[string]PluginAccessLevel{"g": PluginNotAvailable}}
	if issues := ValidatePluginAccessPolicy(ok); len(issues) != 0 {
		t.Fatalf("valid policy reported issues: %v", issues)
	}
	bad := PluginAccessPolicy{Plugin: "  ", OrgWide: "bogus", GroupOverrides: map[string]PluginAccessLevel{"g": "nope"}}
	issues := ValidatePluginAccessPolicy(bad)
	if !containsSub(issues, "plugin reference is required") {
		t.Errorf("expected plugin-ref issue, got %v", issues)
	}
	if !containsSub(issues, `org_wide level "bogus"`) {
		t.Errorf("expected org_wide level issue, got %v", issues)
	}
	if !containsSub(issues, `group_overrides["g"] level "nope"`) {
		t.Errorf("expected group override level issue, got %v", issues)
	}
}
