// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/managedsettings"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestRenderManagedSettingsRoundTrip proves the file-authorable Cowork plugin/
// connector governance renders via the managedsettings path and round-trips:
// the rendered bytes parse back to a Policy preserving the governed keys.
func TestRenderManagedSettingsRoundTrip(t *testing.T) {
	g := PluginGovernance{
		KnownMarketplacesOnly:   true,
		PluginOnlyCustomization: true,
		AllowedConnectors:       []string{"github", "jira"},
		DeniedConnectors:        []string{"shell-exec"},
		ManagedConnectorsOnly:   true,
		// PluginStates is admin-console-only; it must NOT appear in the rendered file.
		PluginStates: map[string]PluginState{"acme-plugin": PluginRequired},
	}
	wire, err := RenderManagedSettings(g)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pol, err := managedsettings.ParsePolicyFromWire(wire)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// strictKnownMarketplaces is the empty-array lockdown posture (array model), not
	// a bool: KnownMarketplacesOnly=true projects to a present-but-empty allowlist.
	if pol.StrictKnownMarketplaces == nil || len(*pol.StrictKnownMarketplaces) != 0 {
		t.Errorf("strictKnownMarketplaces lockdown not preserved as empty array: %+v", pol.StrictKnownMarketplaces)
	}
	if !pol.StrictPluginOnlyCustomization || !pol.AllowManagedMCPServersOnly {
		t.Errorf("lockdown flags not preserved: %+v", pol)
	}
	// either lockdown toggle also projects disableSideloadFlags — the documented
	// per-run bypass closer that explicitly covers Cowork local sessions (v2.1.193+).
	if !pol.DisableSideloadFlags {
		t.Errorf("disableSideloadFlags not projected with the lockdown toggles: %+v", pol)
	}
	// AllowedMCPServers carries named-server predicates since (three-state
	// pointer; cowork projects names).
	if pol.AllowedMCPServers == nil || !containsAllRules(*pol.AllowedMCPServers, "github", "jira") {
		t.Errorf("allowed connectors = %v", pol.AllowedMCPServers)
	}
	if !containsAllRules(pol.DeniedMCPServers, "shell-exec") {
		t.Errorf("denied connectors = %v", pol.DeniedMCPServers)
	}
}

// TestVerifyManagedSettingsDrift proves the connector reuses the drift engine: a
// host matching the authored policy shows no drift; a bare host drifts.
func TestVerifyManagedSettingsDrift(t *testing.T) {
	g := PluginGovernance{KnownMarketplacesOnly: true, ManagedConnectorsOnly: true, AllowedConnectors: []string{"github"}}
	authored, err := RenderManagedSettings(g)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	noDrift, err := VerifyManagedSettingsDrift(authored, authored, testTime)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(noDrift) != 0 {
		t.Errorf("a host matching the policy must not drift, got %d findings", len(noDrift))
	}
	drift, err := VerifyManagedSettingsDrift(authored, []byte(`{}`), testTime)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(drift) == 0 {
		t.Error("a bare host must drift from the authored Cowork policy")
	}
}

// TestResolveSpendLimit proves the documented precedence: individual override >
// most-restrictive group > org default > none.
func TestResolveSpendLimit(t *testing.T) {
	p := GroupSpendPolicy{
		OrgDefaultMicroUSD: USDToMicro(1000),
		GroupMicroUSD:      map[string]int64{"eng": USDToMicro(500), "research": USDToMicro(300)},
		IndividualMicroUSD: map[string]int64{"user_01VIP": USDToMicro(2000)},
	}
	if lim, src, ok := p.ResolveSpendLimit("user_01VIP", []string{"eng"}); !ok || src != SpendLimitIndividual || lim != USDToMicro(2000) {
		t.Errorf("individual override = (%d,%s,%v)", lim, src, ok)
	}
	if lim, src, ok := p.ResolveSpendLimit("user_01A", []string{"eng", "research"}); !ok || src != SpendLimitGroup || lim != USDToMicro(300) {
		t.Errorf("most-restrictive group = (%d,%s,%v), want 300/group", lim, src, ok)
	}
	if lim, src, ok := p.ResolveSpendLimit("user_01B", []string{"sales"}); !ok || src != SpendLimitOrgDefault || lim != USDToMicro(1000) {
		t.Errorf("org default = (%d,%s,%v)", lim, src, ok)
	}
	empty := GroupSpendPolicy{}
	if lim, src, ok := empty.ResolveSpendLimit("user_01C", nil); ok || src != SpendLimitNone || lim != 0 {
		t.Errorf("no policy = (%d,%s,%v), want 0/none/false", lim, src, ok)
	}
}

func TestGovernanceControlsInventory(t *testing.T) {
	controls := GovernanceControls()
	if len(controls) != 5 {
		t.Fatalf("expected 5 governance controls, got %d", len(controls))
	}
	byName := map[string]GovernanceControl{}
	for _, c := range controls {
		byName[c.Name] = c
	}
	if !byName["plugin marketplace lockdown"].FileAuthorable || byName["plugin marketplace lockdown"].Mechanism != MechManagedSettingsFile {
		t.Error("marketplace lockdown should be file-authorable")
	}
	if !byName["managed MCP connector allow/deny"].FileAuthorable || byName["managed MCP connector allow/deny"].Mechanism != MechManagedSettingsFile {
		t.Error("managed MCP connector allow/deny must stay a file-authorable managed-settings control")
	}
	// The GA per-tool connector controls (role editor) are console-only — a SEPARATE
	// mechanism from the managed-settings allowlist above (the over-promise the
	// 2026-06-10 verification corrected).
	if c, ok := byName["per-tool connector controls"]; !ok || c.FileAuthorable || c.Mechanism != MechAdminConsole {
		t.Errorf("per-tool connector controls must be admin-console (NOT file-authorable), got %+v", c)
	}
	// Group spend limits: the GA docs document the console path only (no public Admin
	// API found AsOf 2026-06-10), so the honest mechanism is admin-console.
	if byName["group spend limits"].FileAuthorable || byName["group spend limits"].Mechanism != MechAdminConsole {
		t.Error("group spend limits must be admin-console (NOT file-authorable) — honest mechanism")
	}
	if byName["per-plugin install state"].FileAuthorable || byName["per-plugin install state"].Mechanism != MechAdminConsole {
		t.Error("per-plugin install state must be admin-console (NOT file-authorable)")
	}
}

// TestSpendBreachFinding proves the spend drift primitive: observed monthly spend
// beyond the resolved cap (per the documented precedence) is a medium finding; an
// at/under-cap aggregate, a user with no limit, or a missing userRef is not.
func TestSpendBreachFinding(t *testing.T) {
	p := GroupSpendPolicy{
		OrgDefaultMicroUSD: USDToMicro(1000),
		GroupMicroUSD:      map[string]int64{"eng": USDToMicro(500), "research": USDToMicro(300)},
		IndividualMicroUSD: map[string]int64{"user_01VIP": USDToMicro(2000)},
	}

	// Individual override tier breached: full field check.
	f, ok := p.BreachFinding("user_01VIP", []string{"eng"}, USDToMicro(2500), "2026-06", testTime)
	if !ok {
		t.Fatal("spend over the individual cap must be a finding")
	}
	if f.Kind != findingKindSpendDrift || f.Severity != model.SeverityMedium {
		t.Errorf("kind/severity = %s/%s, want spend_limit_drift/medium", f.Kind, f.Severity)
	}
	if f.SubjectKind != resIdentityAccount || f.SubjectRef != "user_01VIP" {
		t.Errorf("subject = %s/%s, want identity.account/user_01VIP", f.SubjectKind, f.SubjectRef)
	}
	if want := "Cowork spend over governed cap (individual tier): $2500 observed > $2000 cap, 2026-06"; f.Title != want {
		t.Errorf("title = %q, want %q", f.Title, want)
	}
	if want := redact.Hash(fmt.Sprintf("user_01VIP|2026-06|%d|individual|%d", USDToMicro(2000), USDToMicro(2500))); f.DetailHash != want {
		t.Errorf("detailHash = %q, want hash of user|period|limit|source|observed", f.DetailHash)
	}
	if !f.OccurredAt.Equal(testTime) {
		t.Errorf("occurredAt = %v", f.OccurredAt)
	}

	// Most-restrictive group tier: the 300 cap governs, and the title says "group".
	f, ok = p.BreachFinding("user_01A", []string{"eng", "research"}, USDToMicro(400), "2026-06", testTime)
	if !ok || f.Title != "Cowork spend over governed cap (group tier): $400 observed > $300 cap, 2026-06" {
		t.Errorf("group-tier breach = %+v ok=%v", f, ok)
	}

	// Org-default tier for a user in no capped group.
	f, ok = p.BreachFinding("user_01B", []string{"sales"}, USDToMicro(1500), "2026-06", testTime)
	if !ok || f.Title != "Cowork spend over governed cap (org_default tier): $1500 observed > $1000 cap, 2026-06" {
		t.Errorf("org-default breach = %+v ok=%v", f, ok)
	}

	// A sub-dollar overage (the realistic enforcement-lag case) must not render a
	// self-contradictory "$N observed > $N cap": observed ceils, the cap floors.
	f, ok = p.BreachFinding("user_01VIP", []string{"eng"}, USDToMicro(2000)+300_000, "2026-06", testTime)
	if !ok || f.Title != "Cowork spend over governed cap (individual tier): $2001 observed > $2000 cap, 2026-06" {
		t.Errorf("sub-dollar overage title = %+v ok=%v", f, ok)
	}

	// No limit resolved → never a finding (there is no cap to drift from).
	if _, ok := (GroupSpendPolicy{}).BreachFinding("user_01C", nil, USDToMicro(9999), "2026-06", testTime); ok {
		t.Error("a user with no resolved limit must not breach")
	}

	// At/under the cap → the platform enforced (or spend is simply within policy).
	if _, ok := p.BreachFinding("user_01A", []string{"research"}, USDToMicro(300), "2026-06", testTime); ok {
		t.Error("spend exactly AT the cap must not be a finding (observed <= limit)")
	}
	if _, ok := p.BreachFinding("user_01A", []string{"research"}, USDToMicro(120), "2026-06", testTime); ok {
		t.Error("spend under the cap must not be a finding")
	}

	// No user to attribute → nothing.
	if _, ok := p.BreachFinding("", []string{"eng"}, USDToMicro(9999), "2026-06", testTime); ok {
		t.Error("a breach without a userRef must not be built")
	}
}

func TestPluginStateValid(t *testing.T) {
	for _, s := range []PluginState{PluginInstalledByDefault, PluginAvailable, PluginNotAvailable, PluginRequired} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if PluginState("bogus").Valid() {
		t.Error("an unknown plugin state must be invalid")
	}
}

// containsAllRules reports whether every needle appears as a named-server
// predicate in the rule list (the cowork projection emits names only).
func containsAllRules(rules []managedsettings.MCPServerRule, needles ...string) bool {
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		names = append(names, r.Name)
	}
	return containsAll(names, needles...)
}

func containsAll(haystack []string, needles ...string) bool {
	set := map[string]struct{}{}
	for _, h := range haystack {
		set[h] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}
