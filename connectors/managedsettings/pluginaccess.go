// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"fmt"
	"sort"
	"strings"
)

// pluginaccess.go MODELS the claude.ai / Claude Cowork ORG-ADMIN plugin-access surface
// (B1) — the per-GROUP plugin governance an org administrator authors in the
// claude.ai admin console. It is NOT a managed-settings.json key: like delivery.go
// models the server-vs-endpoint tiers it cannot deliver, this models a surface the
// control plane AUTHORS and EXPLAINS but does not enforce (claude.ai enforces it). It is
// the contract the Cowork plugin-governance path consumes; this file provides the
// model + resolver.
//
// VERIFIED 2026-06-09 (support.claude.com/.../13837433-manage-claude-cowork-plugins-for-
// your-organization):
//
//   - FOUR per-plugin access levels (the brief's "auto-install / available / hidden"
//     was a 3-level approximation; there are FOUR — the verbatim labels below).
//   - A group-level OVERRIDE (Enterprise) REPLACES the org-wide preference for that
//     group's members.
//   - When a member belongs to ≥2 groups with different settings, "the most permissive
//     setting applies."

// PluginAccessLevel is one of the four verified per-plugin access levels. The constant
// VALUES are stable connector identifiers; Label() returns the verbatim console label.
type PluginAccessLevel string

const (
	// PluginInstalledByDefault: "Automatically installed for all org members… Members can
	// uninstall if they choose."
	PluginInstalledByDefault PluginAccessLevel = "installed_by_default"
	// PluginAvailableForInstall: "Listed in the plugin catalog. Members see it… and can
	// install it themselves."
	PluginAvailableForInstall PluginAccessLevel = "available_for_install"
	// PluginNotAvailable: "Hidden from the catalog entirely. Members can't see or install
	// the plugin." (the brief's "hidden").
	PluginNotAvailable PluginAccessLevel = "not_available"
	// PluginRequired: "Automatically installed for all org members without the option to
	// remove it… cannot be disabled or uninstalled."
	PluginRequired PluginAccessLevel = "required"
)

// knownPluginAccessLevel reports whether l is one of the four verified levels.
func knownPluginAccessLevel(l PluginAccessLevel) bool {
	switch l {
	case PluginInstalledByDefault, PluginAvailableForInstall, PluginNotAvailable, PluginRequired:
		return true
	default:
		return false
	}
}

// Label returns the verbatim claude.ai console label for the level.
func (l PluginAccessLevel) Label() string {
	switch l {
	case PluginInstalledByDefault:
		return "Installed by default"
	case PluginAvailableForInstall:
		return "Available for install"
	case PluginNotAvailable:
		return "Not available"
	case PluginRequired:
		return "Required"
	default:
		return string(l)
	}
}

// Installed reports whether the level results in the plugin being PRESENT for the member
// without action (Required or Installed-by-default). Removable reports whether the member
// may then uninstall it (true only for Installed-by-default). Visible reports whether the
// plugin appears in the member's catalog (everything except Not-available).
func (l PluginAccessLevel) Installed() bool {
	return l == PluginRequired || l == PluginInstalledByDefault
}
func (l PluginAccessLevel) Removable() bool { return l == PluginInstalledByDefault }
func (l PluginAccessLevel) Visible() bool   { return l != PluginNotAvailable }

// permissiveness orders the levels for the verified "most permissive setting applies"
// multi-group conflict rule. The ordering is by ACCESS GRANTED to the member (the
// plugin present/usable is more permissive than absent): Required > Installed-by-default
// > Available-for-install > Not-available.
//
// CONFIDENCE: the four LABELS are high-confidence (verbatim across two reads); this
// ORDERING is MEDIUM confidence (the ranking appeared in only one read). It is isolated
// here so a future correction is a one-line change. See the verification doc's
// `to-confirm`.
func permissiveness(l PluginAccessLevel) int {
	switch l {
	case PluginRequired:
		return 3
	case PluginInstalledByDefault:
		return 2
	case PluginAvailableForInstall:
		return 1
	case PluginNotAvailable:
		return 0
	default:
		return -1
	}
}

// PluginAccessPolicy is the authored access posture for ONE plugin across an org: the
// org-wide default level plus optional per-group overrides (Enterprise). Plugin is the
// plugin reference (e.g. "name@marketplace").
type PluginAccessPolicy struct {
	Plugin string `json:"plugin"`
	// OrgWide is the organization-wide installation preference (the default for any member
	// not covered by a group override).
	OrgWide PluginAccessLevel `json:"org_wide"`
	// GroupOverrides maps a group name to the level that REPLACES OrgWide for that group's
	// members. Empty = no overrides (every member gets OrgWide). Enterprise-only.
	GroupOverrides map[string]PluginAccessLevel `json:"group_overrides,omitempty"`
}

// PluginAccessResolution is the effective access decision for one member, with the honest
// reason it resolved that way (the console renders it; Olivares does not enforce it).
type PluginAccessResolution struct {
	Plugin string `json:"plugin"`
	// Effective is the level that applies to the member.
	Effective PluginAccessLevel `json:"effective"`
	// FromGroups lists the member's groups whose override participated (empty when the
	// org-wide default applied because no override matched).
	FromGroups []string `json:"from_groups,omitempty"`
	// OrgWideApplied reports that no group override matched, so OrgWide was used.
	OrgWideApplied bool `json:"org_wide_applied"`
	// Installed / Removable / Visible are the resolved access semantics, precomputed for
	// the console so it need not re-derive them from Effective.
	Installed bool   `json:"installed"`
	Removable bool   `json:"removable"`
	Visible   bool   `json:"visible"`
	Reason    string `json:"reason"`
}

// Resolve computes the effective plugin access for a member given their group
// memberships, applying the VERIFIED rules: a group override REPLACES the org-wide
// preference for that group's members; among MULTIPLE applicable overrides the MOST
// PERMISSIVE wins; a member in no overridden group gets OrgWide. memberGroups order does
// not matter (the result is deterministic in the group names for a stable FromGroups).
func (p PluginAccessPolicy) Resolve(memberGroups []string) PluginAccessResolution {
	// Collect the overrides that apply to this member (their group ∈ GroupOverrides).
	type applied struct {
		group string
		level PluginAccessLevel
	}
	var apply []applied
	seen := map[string]struct{}{}
	for _, g := range memberGroups {
		if _, dup := seen[g]; dup {
			continue // dedupe: an IdP group list may repeat a group; count it once
		}
		if lvl, ok := p.GroupOverrides[g]; ok {
			seen[g] = struct{}{}
			apply = append(apply, applied{g, lvl})
		}
	}

	res := PluginAccessResolution{Plugin: p.Plugin}
	if len(apply) == 0 {
		// No override matched → the org-wide preference governs.
		res.Effective = p.OrgWide
		res.OrgWideApplied = true
		res.Reason = "no group override applies; the organization-wide preference (" + p.OrgWide.Label() + ") governs"
	} else {
		// One or more overrides apply; the most permissive wins (verified conflict rule).
		// Sort by group name first for a deterministic tie order, then pick max
		// permissiveness (ties resolve to the lexicographically-first group, deterministic).
		sort.Slice(apply, func(i, j int) bool { return apply[i].group < apply[j].group })
		best := apply[0]
		for _, a := range apply[1:] {
			if permissiveness(a.level) > permissiveness(best.level) {
				best = a
			}
		}
		res.Effective = best.level
		// FromGroups lists every group whose override equals the winning level (the set of
		// groups that justify the decision), in stable order.
		for _, a := range apply {
			if a.level == best.level {
				res.FromGroups = append(res.FromGroups, a.group)
			}
		}
		if len(apply) == 1 {
			res.Reason = "group " + best.group + " overrides the org-wide preference with " + best.level.Label()
		} else {
			res.Reason = "member is in multiple overridden groups; the most permissive override (" + best.level.Label() + ", from " + strings.Join(res.FromGroups, ", ") + ") applies"
		}
	}
	res.Installed = res.Effective.Installed()
	res.Removable = res.Effective.Removable()
	res.Visible = res.Effective.Visible()
	return res
}

// ValidatePluginAccessPolicy validates an authored per-plugin access policy SERVER-SIDE:
// a non-empty plugin ref and a KNOWN level for the org-wide default and every group
// override (an unknown level would be silently ignored by the console — a governance
// hole). It returns issue strings (empty = valid), in deterministic order.
func ValidatePluginAccessPolicy(p PluginAccessPolicy) []string {
	var issues []string
	if strings.TrimSpace(p.Plugin) == "" {
		issues = append(issues, "plugin reference is required")
	}
	if !knownPluginAccessLevel(p.OrgWide) {
		issues = append(issues, fmt.Sprintf("org_wide level %q is not one of installed_by_default|available_for_install|not_available|required", p.OrgWide))
	}
	for _, g := range sortedAccessGroups(p.GroupOverrides) {
		if lvl := p.GroupOverrides[g]; !knownPluginAccessLevel(lvl) {
			issues = append(issues, fmt.Sprintf("group_overrides[%q] level %q is not one of installed_by_default|available_for_install|not_available|required", g, lvl))
		}
	}
	return issues
}

// sortedAccessGroups returns the override group names in stable order (deterministic
// validation output).
func sortedAccessGroups(m map[string]PluginAccessLevel) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
