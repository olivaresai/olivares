// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestRequirementsDriftBareHostHighs(t *testing.T) {
	expected := sampleStrictPolicy().Requirements
	// A live requirements that enforces NOTHING (absent everywhere).
	d := requirementsDrift("h", expected, requirementsWire{}, toml.MetaData{}, testNow())
	if len(d) < 7 {
		t.Fatalf("expected drift on many keys, got %d: %+v", len(d), d)
	}
	var sawApprovalHigh, sawSandboxHigh, sawRemoteHigh bool
	for _, f := range d {
		switch {
		case strings.Contains(f.Title, "approval-policy constraint") && f.Severity == model.SeverityHigh:
			sawApprovalHigh = true
		case strings.Contains(f.Title, "sandbox-mode constraint") && f.Severity == model.SeverityHigh:
			sawSandboxHigh = true
		case strings.Contains(f.Title, "remote control is NOT disabled") && f.Severity == model.SeverityHigh:
			sawRemoteHigh = true
		}
	}
	if !sawApprovalHigh || !sawSandboxHigh || !sawRemoteHigh {
		t.Errorf("expected HIGH drift for approval/sandbox/remote-control; got approval=%v sandbox=%v remote=%v", sawApprovalHigh, sawSandboxHigh, sawRemoteHigh)
	}
}

func TestMCPLockdownDriftHigh(t *testing.T) {
	// The org authored the COMPLETE MCP lockdown (empty allowlist); a host that does not
	// enforce it drifts HIGH (any MCP server may be enabled).
	expected := Requirements{AllowedMCPServers: &[]MCPServer{}}
	d := requirementsDrift("h", expected, requirementsWire{}, toml.MetaData{}, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityHigh || !strings.Contains(d[0].Title, "LOCKDOWN") {
		t.Errorf("expected one HIGH MCP-lockdown drift, got %+v", d)
	}
}

func TestPermissionProfilesDriftSeverities(t *testing.T) {
	expected := Requirements{AllowedPermissionProfiles: &map[string]bool{":workspace": true}}
	d := requirementsDrift("h", expected, requirementsWire{}, toml.MetaData{}, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityMedium || !strings.Contains(d[0].Title, "NOT enforced") {
		t.Fatalf("missing allowed_permission_profiles should drift MEDIUM, got %+v", d)
	}
	w, md := mustDecodeReqW(t, "[allowed_permission_profiles]\n\":workspace\" = true\n\":danger-full-access\" = true\n")
	d = requirementsDrift("h", expected, w, md, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityHigh || !strings.Contains(d[0].Title, ":danger-full-access") {
		t.Fatalf("danger-full-access profile broadening should drift HIGH, got %+v", d)
	}
	// `w` allows TWO profiles and this lockdown allows none, so BOTH are broadenings.
	// This assertion used to read `len(d) != 1`, which is what the verifier did rather
	// than what it owed: it returned after the first extra. The count is 2 now, and the
	// escape hatch keeps its HIGH regardless of where it sorts.
	lockdown := Requirements{AllowedPermissionProfiles: &map[string]bool{}}
	d = requirementsDrift("h", lockdown, w, md, testNow())
	if len(d) != 2 {
		t.Fatalf("present-empty profiles lockdown with two live true profiles should drift twice, got %+v", d)
	}
	if d[0].Severity != model.SeverityHigh || !strings.Contains(d[0].Title, ":danger-full-access") {
		t.Fatalf("the danger-full-access broadening must be reported HIGH, got %+v", d[0])
	}
	if d[1].Severity != model.SeverityMedium || !strings.Contains(d[1].Title, ":workspace") {
		t.Fatalf("the workspace broadening must be reported MEDIUM, got %+v", d[1])
	}
}

func TestMCPMatcherIdentityTolerated(t *testing.T) {
	expected := Requirements{AllowedMCPServers: &[]MCPServer{{Name: "docs", Command: "codex-mcp"}}}
	body := `
[mcp_servers.docs.identity]
command = { executable = "codex-mcp", args = [{ match = "exact", value = "serve" }] }
`
	if issues := ValidateRequirementsTOML([]byte(body)); len(issues) != 0 {
		t.Fatalf("matcher-form MCP identity should validate tolerantly, got %v", issues)
	}
	if _, err := ParseRequirementsTOML([]byte(body)); err != nil {
		t.Fatalf("matcher-form MCP identity should parse: %v", err)
	}
	w, md := mustDecodeReqW(t, body)
	d := requirementsDrift("h", expected, w, md, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityInfo || !strings.Contains(d[0].Title, "matcher form") {
		t.Fatalf("matcher-form MCP identity should emit exactly one Info finding, got %+v", d)
	}
}

func TestMCPAllowlistNotEnforcedIsMedium(t *testing.T) {
	// A non-empty allowlist not enforced is MEDIUM (a user may add unapproved servers).
	expected := Requirements{AllowedMCPServers: &[]MCPServer{{Name: "docs", Command: "codex-mcp"}}}
	d := requirementsDrift("h", expected, requirementsWire{}, toml.MetaData{}, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityMedium {
		t.Errorf("expected one MEDIUM MCP-allowlist drift, got %+v", d)
	}
}

func TestFeaturePinDrift(t *testing.T) {
	expected := Requirements{Features: map[string]bool{"computer_use": false}}
	// Host does not pin the feature -> drift.
	if d := requirementsDrift("h", expected, requirementsWire{}, mustDecodeReq(t, ""), testNow()); len(d) != 1 {
		t.Fatalf("unpinned feature should drift once, got %+v", d)
	}
	// Host pins the same value -> no drift.
	w, md := mustDecodeReqW(t, "[features]\ncomputer_use = false\n")
	if d := requirementsDrift("h", expected, w, md, testNow()); len(d) != 0 {
		t.Errorf("a matching feature pin must not drift, got %+v", d)
	}
	// Host pins the OPPOSITE value -> drift.
	w2, md2 := mustDecodeReqW(t, "[features]\ncomputer_use = true\n")
	if d := requirementsDrift("h", expected, w2, md2, testNow()); len(d) != 1 {
		t.Errorf("a conflicting feature pin must drift, got %+v", d)
	}
}

func TestManagedConfigNetworkAccessOnlyTrueDrifts(t *testing.T) {
	expected := ManagedConfig{NetworkAccess: boolPtr(false)}
	// Absent network_access defaults to no-network -> already meets intent -> no drift.
	if d := managedConfigDrift("h", expected, managedConfigWire{}, toml.MetaData{}, testNow()); len(d) != 0 {
		t.Errorf("absent network_access must not drift against a pinned-false intent, got %+v", d)
	}
	// A live TRUE diverges -> MEDIUM.
	w, md := mustDecodeMC(t, "[sandbox_workspace_write]\nnetwork_access = true\n")
	d := managedConfigDrift("h", expected, w, md, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityMedium {
		t.Errorf("a live network_access=true must drift MEDIUM, got %+v", d)
	}
}

func TestExperimentalNetworkEnabledDrift(t *testing.T) {
	expectedOff := ManagedConfig{Network: &NetworkConfig{Enabled: boolPtr(false)}}
	w, md := mustDecodeMC(t, "[experimental_network]\nenabled = true\n")
	d := managedConfigDrift("h", expectedOff, w, md, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityHigh || !strings.Contains(d[0].Title, "enabled=true") {
		t.Fatalf("managed experimental_network enabled=true against pinned false should drift HIGH, got %+v", d)
	}
	expectedOn := ManagedConfig{Network: &NetworkConfig{Enabled: boolPtr(true)}}
	w, md = mustDecodeMC(t, "[experimental_network]\nenabled = false\n")
	d = managedConfigDrift("h", expectedOn, w, md, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityLow {
		t.Fatalf("managed experimental_network disabled against pinned true should drift LOW, got %+v", d)
	}
	reqExpected := Requirements{Network: &NetworkConfig{Enabled: boolPtr(false)}}
	rw, rmd := mustDecodeReqW(t, "[experimental_network]\nenabled = true\n")
	rd := requirementsDrift("h", reqExpected, rw, rmd, testNow())
	if len(rd) != 1 || rd[0].Severity != model.SeverityHigh {
		t.Fatalf("requirements experimental_network enabled=true against pinned false should drift HIGH, got %+v", rd)
	}
}

func TestNewRequirementDriftSurfaces(t *testing.T) {
	expected := Requirements{
		EnforceResidency:                     "us",
		WindowsAllowedSandboxImplementations: []string{"unelevated"},
		RemoteSandboxConfigs: []RemoteSandboxConfig{
			{HostnamePatterns: []string{"*.corp.example"}, AllowedSandboxModes: []string{SandboxReadOnly}},
		},
		AllowLockedComputerUse: boolPtr(false),
		Marketplaces: &MarketplacesRequirement{
			RestrictToAllowedSources: true,
			AllowedSources:           map[string]MarketplaceSource{"approved": {Source: "git", URL: "https://github.com/example/tools"}},
		},
		PrefixRules: []PrefixRule{{Pattern: []PatternToken{{Token: "git"}, {Token: "push"}}, Decision: "forbidden"}},
	}
	body := `
enforce_residency = "eu"

[windows]
allowed_sandbox_implementations = ["elevated", "unelevated"]

[[remote_sandbox_config]]
hostname_patterns = ["*.corp.example"]
allowed_sandbox_modes = ["read-only", "danger-full-access"]

[computer_use]
allow_locked_computer_use = true

[marketplaces]
restrict_to_allowed_sources = false

[marketplaces.allowed_sources.approved]
source = "git"
url = "https://github.com/example/tools"

[rules]
`
	w, md := mustDecodeReqW(t, body)
	d := requirementsDrift("h", expected, w, md, testNow())
	var residency, windows, remoteHigh, locked, marketplaceHigh, prefixHigh bool
	for _, f := range d {
		if strings.Contains(f.Title, "residency") && f.Severity == model.SeverityMedium {
			residency = true
		}
		if strings.Contains(f.Title, "Windows sandbox-implementation") && f.Severity == model.SeverityMedium {
			windows = true
		}
		if strings.Contains(f.Title, "remote_sandbox_config") && f.Severity == model.SeverityHigh {
			remoteHigh = true
		}
		if strings.Contains(f.Title, "locked computer use") && f.Severity == model.SeverityMedium {
			locked = true
		}
		if strings.Contains(f.Title, "supply-chain gate is off") && f.Severity == model.SeverityHigh {
			marketplaceHigh = true
		}
		if strings.Contains(f.Title, "prefix rule") && f.Severity == model.SeverityHigh {
			prefixHigh = true
		}
	}
	if !residency || !windows || !remoteHigh || !locked || !marketplaceHigh || !prefixHigh {
		t.Fatalf("missing expected new-surface drifts residency=%v windows=%v remote=%v locked=%v marketplace=%v prefix=%v; got %+v",
			residency, windows, remoteHigh, locked, marketplaceHigh, prefixHigh, d)
	}
}

func TestOTELLogUserPromptHighOnlyWhenTrue(t *testing.T) {
	expected := ManagedConfig{OTEL: &OTELConfig{LogUserPrompt: boolPtr(false)}}
	// Absent -> default false -> no drift.
	if d := managedConfigDrift("h", expected, managedConfigWire{}, toml.MetaData{}, testNow()); len(d) != 0 {
		t.Errorf("absent log_user_prompt must not drift against pinned-false, got %+v", d)
	}
	// Live true -> HIGH (raw prompts exported).
	w, md := mustDecodeMC(t, "[otel]\nlog_user_prompt = true\n")
	d := managedConfigDrift("h", expected, w, md, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityHigh || !strings.Contains(d[0].Title, "RAW USER PROMPTS") {
		t.Errorf("a live log_user_prompt=true must drift HIGH, got %+v", d)
	}
}

func TestManagedDefaultScalarsAreLow(t *testing.T) {
	expected := ManagedConfig{ApprovalPolicy: ApprovalOnRequest, SandboxMode: SandboxReadOnly, WebSearch: WebSearchDisabled}
	d := managedConfigDrift("h", expected, managedConfigWire{}, toml.MetaData{}, testNow())
	if len(d) != 3 {
		t.Fatalf("expected 3 default drifts, got %+v", d)
	}
	for _, f := range d {
		if f.Severity != model.SeverityLow {
			t.Errorf("managed-default scalar drift should be LOW (user can change a default), got %+v", f)
		}
	}
}

func TestWebSearchDisabledFloorEquivalence(t *testing.T) {
	// "disabled" is ALWAYS implicitly allowed, so an authored [] lockdown and a host
	// listing ["disabled"] are the SAME posture — they must NOT drift (review finding).
	expected := Requirements{AllowedWebSearchModes: &[]string{}}
	w, md := mustDecodeReqW(t, `allowed_web_search_modes = ["disabled"]`)
	if d := requirementsDrift("h", expected, w, md, testNow()); len(d) != 0 {
		t.Errorf("[] vs [\"disabled\"] are identical posture; want 0 drift, got %+v", d)
	}
	// inverse: authored ["disabled"] vs a host that locks down with [] — also identical.
	expected2 := Requirements{AllowedWebSearchModes: &[]string{WebSearchDisabled}}
	w2, md2 := mustDecodeReqW(t, `allowed_web_search_modes = []`)
	if d := requirementsDrift("h", expected2, w2, md2, testNow()); len(d) != 0 {
		t.Errorf("[\"disabled\"] vs [] are identical posture; want 0 drift, got %+v", d)
	}
	// a genuine broadening (host adds "live") still drifts MEDIUM, naming ONLY "live".
	w3, md3 := mustDecodeReqW(t, `allowed_web_search_modes = ["disabled", "live"]`)
	d := requirementsDrift("h", expected, w3, md3, testNow())
	if len(d) != 1 || d[0].Severity != model.SeverityMedium ||
		!strings.Contains(d[0].Title, "live") || strings.Contains(d[0].Title, "disabled") {
		t.Errorf("a host adding 'live' must drift MEDIUM naming only 'live', got %+v", d)
	}
}

func TestOTELEndpointOnlyRendersAndRoundTrips(t *testing.T) {
	// An endpoint authored with NO exporter id must still render (defaulted to otlp-http),
	// not be silently dropped — and must drift-clean against its own rendered file (the
	// authoring and verification halves agree; review finding).
	p := Policy{Defaults: ManagedConfig{OTEL: &OTELConfig{Endpoint: "https://collector.example/v1/logs"}}}
	out, err := RenderManagedConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "https://collector.example/v1/logs") {
		t.Fatalf("endpoint-only OTEL must render the endpoint (not drop it):\n%s", out)
	}
	if !strings.Contains(string(out), "otlp-http") {
		t.Errorf("endpoint-only OTEL should default to an otlp-http exporter:\n%s", out)
	}
	w, md, err := parseManagedConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if d := managedConfigDrift("h", p.Defaults, w, md, testNow()); len(d) != 0 {
		t.Errorf("endpoint-only OTEL must drift-clean against its own rendered file, got %+v", d)
	}
	// A host with NO telemetry drifts EXACTLY once (telemetry-off, not double-reported with endpoint).
	if d := managedConfigDrift("h", p.Defaults, managedConfigWire{}, toml.MetaData{}, testNow()); len(d) != 1 {
		t.Errorf("a host with no telemetry should drift exactly once (no double-report), got %+v", d)
	}
}

func TestPermittedEdges(t *testing.T) {
	mcp := []MCPServer{{Name: "docs", Command: "codex-mcp"}, {Name: "", Command: "skip"}}
	domains := []string{"api.openai.com", "  "}
	edges := permittedEdges("host-a", mcp, domains, testNow())
	if len(edges) != 2 { // the nameless server and the blank domain are skipped
		t.Fatalf("want 2 edges, got %d: %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.OriginKind != originManagedConfig || e.OriginRef != "host-a" || e.Source != model.SignalPolicy {
			t.Errorf("edge provenance = %+v", e)
		}
		if e.Mode != model.ModeUnknown {
			t.Errorf("edge mode should be unknown (no R/W guess), got %s", e.Mode)
		}
		if e.ResourceKind != resMCPServer && e.ResourceKind != resNetworkDomain {
			t.Errorf("edge resource kind = %q", e.ResourceKind)
		}
	}
}

func TestRequirementsAbsenceSeverityHonest(t *testing.T) {
	authored := requirementsAbsence("h", "is absent", true, testNow())
	if authored.Severity != model.SeverityHigh {
		t.Errorf("absent + authored should be HIGH, got %s", authored.Severity)
	}
	observe := requirementsAbsence("h", "is absent", false, testNow())
	if observe.Severity != model.SeverityMedium {
		t.Errorf("absent observe-only should be MEDIUM (cloud/MDM caveat), got %s", observe.Severity)
	}
	if !strings.Contains(observe.Title, "cloud-managed/MDM") {
		t.Errorf("absence title must be honest about the higher tiers, got %q", observe.Title)
	}
}

// --- decode helpers ---------------------------------------------------------------

func mustDecodeReq(t *testing.T, body string) toml.MetaData {
	t.Helper()
	_, md := mustDecodeReqW(t, body)
	return md
}

func mustDecodeReqW(t *testing.T, body string) (requirementsWire, toml.MetaData) {
	t.Helper()
	w, md, err := parseRequirements([]byte(body))
	if err != nil {
		t.Fatalf("decode requirements: %v", err)
	}
	return w, md
}

func mustDecodeMC(t *testing.T, body string) (managedConfigWire, toml.MetaData) {
	t.Helper()
	w, md, err := parseManagedConfig([]byte(body))
	if err != nil {
		t.Fatalf("decode managed_config: %v", err)
	}
	return w, md
}

// TestManagedHooksOnlyDriftsHigh pins the HOOK supply-chain gate at HIGH.
//
// It was MEDIUM, and that contradicted three things at once: this package's own drift
// doctrine (doc.go — an authored CONSTRAINT a host does not enforce drifts HIGH), its
// sibling constraints on the same layer (allow_remote_control and the marketplace
// supply-chain gate are both HIGH), and — the one that decided it — a measurement of
// codex-cli 0.145.0: hooks ARE the Codex PEP, and a host that has not locked the hook
// supply chain can shadow or replace the enforcing hook. Below HIGH the finding is not
// even persisted (modules/security/anomaly.go drops sub-HIGH), so the warning that the
// PEP can be bypassed never reached a record.
func TestManagedHooksOnlyDriftsHigh(t *testing.T) {
	expected := Requirements{AllowManagedHooksOnly: true}
	d := requirementsDrift("h", expected, requirementsWire{}, toml.MetaData{}, testNow())
	if len(d) != 1 {
		t.Fatalf("expected exactly one drift finding, got %d: %+v", len(d), d)
	}
	if d[0].Severity != model.SeverityHigh {
		t.Errorf("hook supply-chain lockdown unenforced must drift HIGH (it is the Codex PEP's own supply chain), got %v: %q", d[0].Severity, d[0].Title)
	}
	if !strings.Contains(d[0].Title, "hook supply-chain") {
		t.Errorf("finding must name the hook supply chain, got %q", d[0].Title)
	}
}

// TestManagedHooksOnlySiblingsAgree is the consistency guard: the three supply-chain /
// escape-hatch constraints on the requirements layer must not be graded differently from
// each other. It is what stops the next edit from quietly demoting one of them again.
func TestManagedHooksOnlySiblingsAgree(t *testing.T) {
	cases := map[string]Requirements{
		"hooks":       {AllowManagedHooksOnly: true},
		"remote":      {AllowRemoteControl: boolPtr(false)},
		"marketplace": {Marketplaces: &MarketplacesRequirement{RestrictToAllowedSources: true}},
	}
	for name, expected := range cases {
		d := requirementsDrift("h", expected, requirementsWire{}, toml.MetaData{}, testNow())
		if len(d) == 0 {
			t.Fatalf("%s: expected a drift finding on a bare host, got none", name)
		}
		if d[0].Severity != model.SeverityHigh {
			t.Errorf("%s: an unenforced requirements CONSTRAINT must drift HIGH, got %v", name, d[0].Severity)
		}
	}
}

// TestPermissionProfilesDriftReportsEveryExtraProfile pins the whole of a
// permission-profile broadening, not its alphabetically-first member. The verifier
// used to `return` inside the loop over the extra profiles, so a host that allows
// two profiles the org excluded produced ONE finding — and because the extras are
// walked in sorted order, a profile sorting before ":danger-full-access" took the
// report's only slot and the HIGH severity vanished with it. An operator reading
// that report fixes one profile and never learns the escape hatch is open.
func TestPermissionProfilesDriftReportsEveryExtraProfile(t *testing.T) {
	// The org authored the lockdown: no profile is allowed.
	lockdown := Requirements{AllowedPermissionProfiles: &map[string]bool{}}
	// The host allows two. ":agent" sorts before ":danger-full-access".
	w, md := mustDecodeReqW(t, "[allowed_permission_profiles]\n\":agent\" = true\n\":danger-full-access\" = true\n")

	d := requirementsDrift("h", lockdown, w, md, testNow())
	if len(d) != 2 {
		t.Fatalf("both excluded profiles must be reported, got %d: %+v", len(d), d)
	}
	var sawAgent, sawDanger bool
	for _, f := range d {
		switch {
		case strings.Contains(f.Title, ":agent"):
			sawAgent = true
			if f.Severity != model.SeverityMedium {
				t.Errorf(":agent broadening should be MEDIUM, got %s", f.Severity)
			}
		case strings.Contains(f.Title, ":danger-full-access"):
			sawDanger = true
			if f.Severity != model.SeverityHigh {
				t.Errorf(":danger-full-access is the escape hatch: it must stay HIGH, got %s", f.Severity)
			}
		}
	}
	if !sawAgent || !sawDanger {
		t.Fatalf("expected a finding for each excluded profile, saw agent=%v danger=%v: %+v", sawAgent, sawDanger, d)
	}
}
