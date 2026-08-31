// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"fmt"
	"sort"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/managedsettings"
	"github.com/olivaresai/olivares/sdk/model"
)

// governance.go is the Cowork governance authoring/verification surface — pure
// functions the AGPL governance module calls (the legal arrow module→
// connector). It governs Cowork by the SANCTIONED path: observe and
// govern, never proxy the subscription.
//
// HONESTY about mechanism (verified against the Claude Enterprise admin docs and
// the Cowork GA announcement, AsOf 2026-06-10): Cowork's governance controls split
// across distinct delivery mechanisms, and conflating them would over-promise:
//   - MANAGED-SETTINGS FILE (authorable here, rendered by reusing the
//     managedsettings connector): marketplace lockdown (strictKnownMarketplaces,
//     strictPluginOnlyCustomization) and the managed MCP-connector allowlist
//     (allowedMcpServers/deniedMcpServers/allowManagedMcpServersOnly) — which MCP
//     servers may be configured AT ALL. This is a SEPARATE mechanism from the GA
//     per-tool connector controls below; an earlier revision of this file conflated
//     the two.
//   - ADMIN CONSOLE only — modeled and verifiable here, but with NO file key and NO
//     public Admin API (the control plane cannot author them as a file):
//       - per-plugin install state (Installed by default / Available for install /
//         Not available / Required) and private-GitHub marketplace sources.
//       - the GA per-tool connector controls (role editor, "Connectors" tab,
//         Enterprise plans): "Always allow" / "Needs approval" / "Blocked", per
//         connector or per individual tool once a connector is set to "Custom";
//         they do not govern Cowork deployed on a third-party platform. Modeled in
//         controls.go as the org-effective ConnectorControlPolicy that yields
//         PERMITTED edges + the live drift finding.
//       - group spend limits (Organization settings > Usage, "By group" tab): a
//         per-user monthly cap inherited by group members, precedence individual
//         override > most-restrictive (lowest) group > org default; the PLATFORM
//         BLOCKS Claude, Cowork and Claude Code at the cap until the next billing
//         period. The GA docs document the console path only; no public Admin API
//         for group spend was found AsOf 2026-06-10 (an earlier revision claimed
//         read:spend_limits/write:spend_limits — corrected). Modeled here with the
//         precedence resolver (ResolveSpendLimit) + the BreachFinding drift
//         primitive.
//
// GovernanceControls() returns this split as a machine-readable inventory so a
// consumer never mistakes a console-only control for a file-authorable one.

// PluginState is a per-plugin install preference an org marketplace assigns (the
// four verbatim states from the Cowork plugin admin docs). It is ADMIN-CONSOLE
// governed — modeled here for verification, not authorable as a managed-settings key.
type PluginState string

const (
	// PluginInstalledByDefault is auto-installed for all members; a member may uninstall.
	PluginInstalledByDefault PluginState = "installed_by_default"
	// PluginAvailable is listed in the catalog for self-service install.
	PluginAvailable PluginState = "available"
	// PluginNotAvailable is hidden from the catalog entirely.
	PluginNotAvailable PluginState = "not_available"
	// PluginRequired is auto-installed without the option to remove.
	PluginRequired PluginState = "required"
)

// Valid reports whether s is one of the four documented plugin states.
func (s PluginState) Valid() bool {
	switch s {
	case PluginInstalledByDefault, PluginAvailable, PluginNotAvailable, PluginRequired:
		return true
	default:
		return false
	}
}

// PluginGovernance is the Cowork plugin/connector governance intent the control
// plane authors. The marketplace-lockdown and managed-connector fields are
// file-authorable (RenderManagedSettings projects them onto a managed-settings.json
// via); PluginStates is admin-console-only (carried for verification, never
// rendered into the file).
type PluginGovernance struct {
	// KnownMarketplacesOnly locks Cowork to known/approved plugin marketplaces
	// (managed-settings strictKnownMarketplaces). File-authorable.
	KnownMarketplacesOnly bool
	// PluginOnlyCustomization restricts org customization to plugins
	// (managed-settings strictPluginOnlyCustomization). File-authorable.
	PluginOnlyCustomization bool
	// AllowedConnectors / DeniedConnectors are the managed MCP-connector allow/deny
	// lists (managed-settings allowedMcpServers/deniedMcpServers): the FILE lockdown
	// of which MCP servers may be configured at all. They are NOT the GA per-tool
	// connector controls (Always allow / Needs approval / Blocked) — those live in
	// the admin console role editor with no file key and are modeled separately as
	// the org-effective ConnectorControlPolicy (controls.go). File-authorable.
	AllowedConnectors []string
	DeniedConnectors  []string
	// ManagedConnectorsOnly admits only allow-listed MCP connectors
	// (managed-settings allowManagedMcpServersOnly). File-authorable.
	ManagedConnectorsOnly bool
	// PluginStates is the per-plugin install state per the org marketplace. ADMIN
	// CONSOLE only — modeled for verification, NOT projected into the managed-settings
	// file (RenderManagedSettings ignores it; it would be a fabricated key).
	PluginStates map[string]PluginState
}

// toPolicy projects the file-authorable fields onto an managedsettings.Policy.
// PluginStates and group spend are deliberately NOT projected (they have no
// managed-settings.json key; see the file header).
func (g PluginGovernance) toPolicy() managedsettings.Policy {
	p := managedsettings.Policy{
		DeniedMCPServers:              managedsettings.MCPServersByName(g.DeniedConnectors...),
		AllowManagedMCPServersOnly:    g.ManagedConnectorsOnly,
		StrictPluginOnlyCustomization: g.PluginOnlyCustomization,
	}
	// AllowedMCPServers is three-state since (nil = no restriction; [] =
	// lockdown; list = allowlist). Cowork's connector list carries names only, so
	// a non-empty list projects as named-server predicates and an empty list keeps
	// the key ABSENT (the pre semantics: no restriction, never an accidental
	// lockdown).
	if len(g.AllowedConnectors) > 0 {
		rules := managedsettings.MCPServersByName(g.AllowedConnectors...)
		p.AllowedMCPServers = &rules
	}
	// strictKnownMarketplaces is an ARRAY of marketplace sources (corrected the
	// Bool; managedsettings.Policy.StrictKnownMarketplaces is now *[]Marketplace).
	// The file-authorable Cowork toggle carries no per-source allowlist, so the faithful
	// projection of "known marketplaces only" is the empty-array LOCKDOWN posture (strict
	// mode, no custom marketplaces — renders `"strictKnownMarketplaces": []`); the toggle
	// off leaves the key absent (not enforced).
	if g.KnownMarketplacesOnly {
		p.StrictKnownMarketplaces = &[]managedsettings.Marketplace{}
	}
	// A marketplace/MCP lockdown is bypassable per-run via --plugin-dir/--plugin-url/
	// --agents/--mcp-config unless disableSideloadFlags is also authored — and the key
	// explicitly covers Cowork: it "also rejects these flags from any surface that
	// spawns the CLI with them internally, currently Cowork local sessions in the
	// desktop app" (code.claude.com/docs/en/settings, v2.1.193+, VERIFIED 2026-07-03).
	// So the faithful projection of either lockdown toggle closes its documented
	// bypass instead of authoring a lockdown that drifts HIGH on its own hosts.
	if g.KnownMarketplacesOnly || g.ManagedConnectorsOnly {
		p.DisableSideloadFlags = true
	}
	return p
}

// RenderManagedSettings renders the FILE-AUTHORABLE subset of Cowork plugin/
// connector governance into the canonical managed-settings.json bytes the control
// plane distributes, by reusing the managedsettings renderer (so Cowork and
// Claude Code share one authoring/verification path, never a forked one). Per-plugin
// install state and group spend are NOT included (console/Admin-API only). The
// returned bytes round-trip through managedsettings.ParsePolicyFromWire and verify
// via VerifyManagedSettingsDrift.
func RenderManagedSettings(g PluginGovernance) ([]byte, error) {
	return managedsettings.Render(g.toPolicy())
}

// VerifyManagedSettingsDrift reports PERMITTED(authored)-vs-OBSERVED(live) drift for
// a distributed Cowork managed-settings document, delegating to the shared
// drift engine under a cowork scope so a tampered or stale endpoint is flagged with
// the same per-key severities Claude Code uses.
func VerifyManagedSettingsDrift(authoredJSON, observedJSON []byte, at time.Time) ([]model.FindingReport, error) {
	return managedsettings.VerifyDriftJSON("cowork", authoredJSON, observedJSON, at)
}

// --- Group spend limits (admin-console governed; modeled + resolved here) -------

// microPerUSD converts whole US dollars to integer micro-USD, the unit FinOps uses
// (CostSample.CostMicroUSD) so a resolved cap compares directly against spend.
const microPerUSD = 1_000_000

// USDToMicro converts a whole-dollar monthly limit to micro-USD.
func USDToMicro(usd int64) int64 { return usd * microPerUSD }

// GroupSpendPolicy is the Cowork group spend-limit governance intent: a monthly cap
// (micro-USD) at the org-default, per-group, and per-user-override tiers. It mirrors
// the Anthropic admin model (Organization settings > Usage, "By group" tab; verified
// AsOf 2026-06-10 against support.claude.com article 13799932); the resolver below
// implements the documented precedence. The WRITE is ADMIN-CONSOLE only — the GA
// docs document no public Admin API for group spend (an earlier revision claimed
// read/write:spend_limits scopes; corrected) — so this models the policy so FinOps
// can reason about a user's effective cap and flag overspend (BreachFinding). A
// zero cap means "no limit at that tier".
type GroupSpendPolicy struct {
	OrgDefaultMicroUSD int64            // org-wide default monthly cap (0 = none)
	GroupMicroUSD      map[string]int64 // per-group monthly cap
	IndividualMicroUSD map[string]int64 // per-user override (highest precedence)
}

// SpendLimitSource names which tier supplied a resolved spend limit.
type SpendLimitSource string

const (
	SpendLimitIndividual SpendLimitSource = "individual"
	SpendLimitGroup      SpendLimitSource = "group"
	SpendLimitOrgDefault SpendLimitSource = "org_default"
	SpendLimitNone       SpendLimitSource = "none"
)

// ResolveSpendLimit resolves a user's effective monthly Cowork spend cap using the
// documented precedence: an individual override wins; else the MOST RESTRICTIVE
// (lowest) cap among the user's groups that set one; else the org default; else no
// limit. It returns the cap in micro-USD, the tier it came from, and whether any
// limit applies (false ⇒ the cap value is meaningless). This is the governance
// primitive FinOps/governance call to decide whether observed spend breached policy.
func (p GroupSpendPolicy) ResolveSpendLimit(userID string, userGroups []string) (limitMicroUSD int64, source SpendLimitSource, hasLimit bool) {
	if v, ok := p.IndividualMicroUSD[userID]; ok && v > 0 {
		return v, SpendLimitIndividual, true
	}
	best := int64(0)
	found := false
	for _, g := range userGroups {
		if v, ok := p.GroupMicroUSD[g]; ok && v > 0 {
			if !found || v < best {
				best, found = v, true
			}
		}
	}
	if found {
		return best, SpendLimitGroup, true
	}
	if p.OrgDefaultMicroUSD > 0 {
		return p.OrgDefaultMicroUSD, SpendLimitOrgDefault, true
	}
	return 0, SpendLimitNone, false
}

// findingKindSpendDrift marks observed Cowork spend exceeding the user's governed
// monthly cap — the PERMITTED-vs-OBSERVED drift signal for the spend dimension.
const findingKindSpendDrift = "spend_limit_drift"

// BreachFinding reports observed monthly spend that exceeds the user's resolved
// governed cap. The PLATFORM itself blocks Claude, Cowork and Claude Code when the
// cap is hit (until the next billing period), so observed spend BEYOND the modeled
// cap means the authored policy and the console reality have drifted (a cap was
// raised/removed in the console but not in the model, or enforcement lagged the
// aggregate) — the drift signal FinOps/governance consume. The caller (the AGPL
// module) supplies the observed month aggregate in micro-USD and the period (e.g.
// "2026-06"); the resolver supplies the cap via the documented precedence
// (ResolveSpendLimit). ok=false when no limit applies, when spend is at/under the
// cap, or when there is no user to attribute. The title carries whole dollars only
// (observed rounded UP, cap rounded DOWN, so the displayed inequality always holds);
// the exact amounts ride the DetailHash (docs/SECURITY-HARDENING.md).
func (p GroupSpendPolicy) BreachFinding(userRef string, groups []string, observedMicroUSD int64, period string, at time.Time) (model.FindingReport, bool) {
	if userRef == "" {
		return model.FindingReport{}, false
	}
	limit, source, hasLimit := p.ResolveSpendLimit(userRef, groups)
	if !hasLimit || observedMicroUSD <= limit {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindSpendDrift,
		Severity:    model.SeverityMedium,
		SubjectKind: resIdentityAccount,
		SubjectRef:  userRef,
		Title: fmt.Sprintf("Cowork spend over governed cap (%s tier): $%d observed > $%d cap, %s",
			source, ceilUSD(observedMicroUSD), floorUSD(limit), period),
		DetailHash: redact.Hash(fmt.Sprintf("%s|%s|%d|%s|%d", userRef, period, limit, source, observedMicroUSD)),
		OccurredAt: at,
	}, true
}

// ceilUSD/floorUSD convert micro-USD to whole dollars for the breach title, rounding
// the observed value UP and the cap DOWN so the displayed inequality always holds
// (round-to-nearest could render a self-contradictory "$N observed > $N cap" for a
// sub-50-cent overage — the realistic case, since enforcement lag produces small
// overshoots). Exact amounts ride the DetailHash.
func ceilUSD(micro int64) int64  { return (micro + microPerUSD - 1) / microPerUSD }
func floorUSD(micro int64) int64 { return micro / microPerUSD }

// --- Governance control inventory (honest mechanism map) ------------------------

// GovernanceMechanism is how a Cowork governance control is delivered.
type GovernanceMechanism string

const (
	// MechManagedSettingsFile: authorable as a managed-settings.json key (rendered
	// here by the managed-settings renderer); the control plane can distribute
	// and drift-verify it.
	MechManagedSettingsFile GovernanceMechanism = "managed_settings_file"
	// MechAdminConsole: admin-console only; modeled/verifiable but not file-authorable.
	MechAdminConsole GovernanceMechanism = "admin_console"
	// MechAdminAPI: Admin API only; modeled here, written by the admin plane, not as a file. NOTE: after the 2026-06-10 verification no Cowork control
	// in the inventory maps to it anymore (group spend limits turned out to be
	// console-documented, with no public Admin API found); the value stays part of
	// the vocabulary for controls that genuinely gain an API.
	MechAdminAPI GovernanceMechanism = "admin_api"
)

// GovernanceControl is one Cowork governance control and its honest delivery
// mechanism, so a consumer never mistakes a console/Admin-API control for a
// file-authorable one (the over-promise warns against).
type GovernanceControl struct {
	Name           string              `json:"name"`
	Mechanism      GovernanceMechanism `json:"mechanism"`
	FileAuthorable bool                `json:"file_authorable"`
	Note           string              `json:"note"`
}

// governanceControls is the verified control inventory (AsOf 2026-06-10).
var governanceControls = []GovernanceControl{
	{"plugin marketplace lockdown", MechManagedSettingsFile, true, "strictKnownMarketplaces / strictPluginOnlyCustomization — authored + drift-verified via."},
	{"managed MCP connector allow/deny", MechManagedSettingsFile, true, "allowedMcpServers / deniedMcpServers / allowManagedMcpServersOnly — the managed-settings FILE lockdown of which MCP servers may be configured at all; denylist merges across scopes. A SEPARATE mechanism from the GA per-tool connector controls (role editor), which have no file key."},
	{"per-tool connector controls", MechAdminConsole, false, "role editor, Connectors tab (Enterprise plans): Always allow / Needs approval / Blocked, per connector or per individual tool once a connector is set to Custom (Blocked hides the connector or tool). Console-only — no Admin API or managed-settings key AsOf 2026-06-10; does not govern Cowork deployed on a third-party platform. Modeled here as the org-effective ConnectorControlPolicy (controls.go) for PERMITTED edges + live drift findings."},
	{"per-plugin install state", MechAdminConsole, false, "Installed by default / Available / Not available / Required, per org marketplace (incl. private-GitHub sources). Modeled + verifiable; admin-console only, not a managed-settings key."},
	{"group spend limits", MechAdminConsole, false, "monthly per-user cap (org default / group / individual override) at Organization settings > Usage, By group tab; precedence individual > most-restrictive group > org default; the platform BLOCKS Claude/Cowork/Claude Code at the cap until the next billing period. The GA docs document the console path; no public Admin API for group spend was found AsOf 2026-06-10. Modeled here with ResolveSpendLimit + the BreachFinding drift primitive."},
}

// GovernanceControls returns the Cowork governance control inventory in stable order
// (a copy, so a caller cannot mutate package state). It is the honest companion to
// the authoring surface: which controls the control plane can deliver as a file vs
// which require the admin console / Admin API.
func GovernanceControls() []GovernanceControl {
	out := append([]GovernanceControl(nil), governanceControls...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
