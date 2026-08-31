// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/olivaresai/olivares/sdk/model"
)

// authoring.go is the AUTHORING entry-point of the connector: the exported, pure
// functions the AGPL governance module calls to validate, canonicalize, drift-verify and
// preview the precedence of Codex managed-config documents authored in the console —
// WITHOUT this Apache connector importing /core or /modules (the legal arrow only runs
// module->connector). It reuses the SAME verified render/drift logic the read-only Source
// uses, so the authoring and verification halves can never disagree.

// ValidateRequirementsTOML validates a requirements.toml document SERVER-SIDE (defense in
// depth — the UI is never the security boundary). It returns issue strings (empty =
// valid). It is forward-compatible: unknown top-level keys are NOT rejected (Codex adds
// keys frequently), but a known key with the wrong shape, an unknown enum value, or a
// malformed MCP identity is reported. A document that is not valid TOML is the first,
// fatal issue.
func ValidateRequirementsTOML(content []byte) []string {
	if strings.TrimSpace(string(content)) == "" {
		return []string{"requirements document is empty"}
	}
	w, md, err := parseRequirements(content)
	if err != nil {
		return []string{"requirements.toml is not valid TOML (a known key may have the wrong type): " + err.Error()}
	}
	var issues []string
	issues = append(issues, validateEnumList("allowed_approval_policies", w.AllowedApprovalPolicies, knownApprovalPolicy, "untrusted|on-request|never")...)
	issues = append(issues, validateEnumList("allowed_sandbox_modes", w.AllowedSandboxModes, knownSandboxMode, "read-only|workspace-write|danger-full-access")...)
	issues = append(issues, validateEnumList("allowed_web_search_modes", w.AllowedWebSearchModes, knownWebSearchMode, "disabled|cached|live")...)
	issues = append(issues, validateEnumList("allowed_approvals_reviewers", w.AllowedApprovalsReviewers, knownReviewer, "auto_review|user")...)
	issues = append(issues, validateRequirementsPolicy(w.toRequirements(md))...)
	for _, name := range sortedMCPNames(w.MCPServers) {
		s := w.MCPServers[name]
		c, u := s.command(), s.url()
		hasCommand, hasURL := c != "" || matcherValue(s.Identity.Command), u != "" || matcherValue(s.Identity.URL)
		switch {
		case !hasCommand && !hasURL:
			issues = append(issues, "mcp_servers."+name+" must carry an identity (identity = { command = ... } or identity = { url = ... }) — a server with no identity is never enabled")
		case hasCommand && hasURL:
			issues = append(issues, "mcp_servers."+name+" must carry exactly ONE identity selector (command OR url), not both")
		}
	}
	return issues
}

// ValidateManagedConfigTOML validates a managed_config.toml document SERVER-SIDE.
func ValidateManagedConfigTOML(content []byte) []string {
	if strings.TrimSpace(string(content)) == "" {
		return []string{"managed_config document is empty"}
	}
	w, _, err := parseManagedConfig(content)
	if err != nil {
		return []string{"managed_config.toml is not valid TOML (a known key may have the wrong type): " + err.Error()}
	}
	var issues []string
	if s, granular := approvalPolicyScalar(w.ApprovalPolicy); !granular && s != "" && !knownApprovalPolicy(s) {
		issues = append(issues, "approval_policy "+strconv.Quote(s)+" is not one of untrusted|on-request|never (or the granular inline-table form)")
	}
	if s := strings.TrimSpace(w.SandboxMode); s != "" && !knownSandboxMode(s) {
		issues = append(issues, "sandbox_mode "+strconv.Quote(s)+" is not one of read-only|workspace-write|danger-full-access")
	}
	if s, table := webSearchScalar(w.WebSearch); !table && s != "" && !knownWebSearchMode(s) {
		issues = append(issues, "web_search "+strconv.Quote(s)+" is not one of disabled|cached|live (or the table form)")
	}
	if name := otelExporterName(w.OTEL); name != "" && !knownExporter(name) {
		issues = append(issues, "otel.exporter "+strconv.Quote(name)+" is not one of none|otlp-http|otlp-grpc")
	}
	if name := otelTraceExporterName(w.OTEL); name != "" && !knownExporter(name) {
		issues = append(issues, "otel.trace_exporter "+strconv.Quote(name)+" is not one of none|otlp-http|otlp-grpc")
	}
	if me := stringFromMap(w.OTEL, "metrics_exporter"); me != "" && !knownExporter(me) && me != OTELExporterStatsig {
		issues = append(issues, "otel.metrics_exporter "+strconv.Quote(me)+" is not one of none|statsig|otlp-http|otlp-grpc")
	}
	// ⛔ THE NAME BEING KNOWN IS NOT THE SAME AS THE VALUE BEING RENDERABLE, and this is the
	// half that was missing. otlp-http and otlp-grpc are STRUCT variants: codex-cli 0.147.0
	// refuses to load a config.toml that carries them without an endpoint ("invalid type:
	// unit variant, expected struct variant"), and refuses otlp-http without a protocol
	// ("missing field `protocol`"). A refused config.toml does not degrade telemetry — it
	// stops the agent, so an unrenderable pin has to die at authoring time, in the console,
	// where somebody can read the reason.
	for _, slot := range []struct{ key, name string }{
		{"exporter", otelExporterName(w.OTEL)},
		{"trace_exporter", otelTraceExporterName(w.OTEL)},
		{"metrics_exporter", exporterName(w.OTEL["metrics_exporter"])},
	} {
		if !structVariantExporter(slot.name) {
			continue
		}
		if otelSlotEndpoint(w.OTEL, slot.key) == "" {
			issues = append(issues, "otel."+slot.key+" "+strconv.Quote(slot.name)+
				" needs an endpoint: codex refuses to load a config where it appears without one")
		}
		if p := otelSlotProtocol(w.OTEL, slot.key); p != "" && !knownOTELProtocol(p) {
			issues = append(issues, "otel."+slot.key+".protocol "+strconv.Quote(p)+" is not binary|json")
		}
	}
	return issues
}

// ParseRequirementsTOML parses a requirements.toml document into the connector's
// governance-authored Requirements form (the inverse of RenderRequirements). It is used to
// import an existing host file into the console and to drift-verify. Unknown keys are
// ignored (forward-compatible); malformed TOML is an error. Three-state keys
// (allowed_web_search_modes, the [mcp_servers] lockdown) preserve their present-but-empty
// form via the TOML presence metadata.
func ParseRequirementsTOML(content []byte) (Requirements, error) {
	w, md, err := parseRequirements(content)
	if err != nil {
		return Requirements{}, err
	}
	r := w.toRequirements(md)
	if isDefined(md, "allowed_web_search_modes") {
		v := append([]string(nil), w.AllowedWebSearchModes...)
		r.AllowedWebSearchModes = &v
	}
	if isDefined(md, "mcp_servers") {
		servers := liveMCPServers(w.MCPServers)
		r.AllowedMCPServers = &servers
	}
	return r, nil
}

// ParseManagedConfigTOML parses a managed_config.toml document into the authored
// ManagedConfig form (the inverse of RenderManagedConfig). A granular approval_policy or a
// web_search table — shapes the scalar authored form cannot represent — is dropped from
// the canonical intent (drift still observes the host's live value).
func ParseManagedConfigTOML(content []byte) (ManagedConfig, error) {
	w, _, err := parseManagedConfig(content)
	if err != nil {
		return ManagedConfig{}, err
	}
	c := ManagedConfig{SandboxMode: strings.TrimSpace(w.SandboxMode)}
	if s, granular := approvalPolicyScalar(w.ApprovalPolicy); !granular {
		c.ApprovalPolicy = s
	}
	if s, table := webSearchScalar(w.WebSearch); !table {
		c.WebSearch = s
	}
	if w.SandboxWorkspaceWrite != nil {
		c.NetworkAccess = w.SandboxWorkspaceWrite.NetworkAccess
		c.WritableRoots = w.SandboxWorkspaceWrite.WritableRoots
	}
	if w.ExperimentalNetwork != nil {
		n := networkFromWire(w.ExperimentalNetwork)
		if n.hasAny() {
			c.Network = &n
		}
	}
	if len(w.OTEL) > 0 {
		o := OTELConfig{
			Environment:     stringFromMap(w.OTEL, "environment"),
			Exporter:        otelExporterName(w.OTEL),
			TraceExporter:   otelTraceExporterName(w.OTEL),
			MetricsExporter: stringFromMap(w.OTEL, "metrics_exporter"),
			Endpoint:        otelEndpoint(w.OTEL),
		}
		if v, present := otelLogUserPrompt(w.OTEL); present {
			o.LogUserPrompt = &v
		}
		if o.hasAny() {
			c.OTEL = &o
		}
	}
	return c, nil
}

// CanonicalRequirementsTOML re-renders a requirements.toml document through the verified
// authored form, producing the canonical, minimal bytes an operator distributes (the
// round-trip the console shows on publish). A granular/object shape the authored form
// cannot represent is normalized away.
func CanonicalRequirementsTOML(content []byte) ([]byte, error) {
	r, err := ParseRequirementsTOML(content)
	if err != nil {
		return nil, err
	}
	return RenderRequirements(Policy{Requirements: r})
}

// CanonicalManagedConfigTOML re-renders a managed_config.toml document canonically.
func CanonicalManagedConfigTOML(content []byte) ([]byte, error) {
	c, err := ParseManagedConfigTOML(content)
	if err != nil {
		return nil, err
	}
	return RenderManagedConfig(Policy{Defaults: c})
}

// VerifyDriftTOML runs the PERMITTED-policy-vs-OBSERVED-config drift check at publish time:
// expected is the just-published authored Policy (PERMITTED); observedRequirements and
// observedManagedConfig are the host's live files (OBSERVED). It reuses the connector's
// verified drift logic so authoring and verification share one source of truth. An absent
// (empty) observed file is itself a finding (honest about the cloud/MDM caveat for
// requirements). A malformed observed file becomes a "present but invalid" finding, never a
// silent pass.
func VerifyDriftTOML(scope string, expected Policy, observedRequirements, observedManagedConfig []byte, at time.Time) []model.FindingReport {
	var out []model.FindingReport

	if expected.Requirements.hasAny() {
		if len(strings.TrimSpace(string(observedRequirements))) == 0 {
			out = append(out, requirementsAbsence(scope, "is absent", true, at))
		} else if w, md, err := parseRequirements(observedRequirements); err != nil {
			out = append(out, requirementsAbsence(scope, "is present but invalid TOML", true, at))
		} else {
			out = append(out, requirementsDrift(scope, expected.Requirements, w, md, at)...)
		}
	}
	if expected.Defaults.hasAny() {
		if len(strings.TrimSpace(string(observedManagedConfig))) == 0 {
			out = append(out, managedConfigAbsence(scope, "is absent", at))
		} else if w, md, err := parseManagedConfig(observedManagedConfig); err != nil {
			out = append(out, managedConfigAbsence(scope, "is present but invalid TOML", at))
		} else {
			out = append(out, managedConfigDrift(scope, expected.Defaults, w, md, at)...)
		}
	}
	return out
}

// Preview returns the human-readable precedence/effect lines for the authoring console's
// dry-run (the precedence chains + the system-tier-only verification caveat).
func Preview() []DryRunLine {
	return append(PrecedencePreview(), permissionProfilesVersionLine())
}

// PreviewPolicy returns Preview plus policy-dependent honesty lines. Preview itself has
// no Policy parameter, so it cannot know whether profiles were authored; callers with
// the Policy should use this helper to add the minimum-version note only when relevant.
func PreviewPolicy(p Policy) []DryRunLine {
	lines := PrecedencePreview()
	if p.Requirements.AllowedPermissionProfiles != nil || strings.TrimSpace(p.Requirements.DefaultPermissions) != "" {
		lines = append(lines, permissionProfilesVersionLine())
	}
	return lines
}

// --- helpers ---------------------------------------------------------------------

// validateEnumList reports issues for each member of a list that is not a known enum value.
func validateEnumList(key string, vals []string, known func(string) bool, allowed string) []string {
	var issues []string
	for _, v := range vals {
		if !known(strings.TrimSpace(v)) {
			issues = append(issues, key+" contains unknown value "+strconv.Quote(v)+" (allowed: "+allowed+")")
		}
	}
	return issues
}

func validateRequirementsPolicy(r Requirements) []string {
	var issues []string
	issues = append(issues, validateEnumList("allowed_approval_policies", r.AllowedApprovalPolicies, knownApprovalPolicy, "untrusted|on-request|never")...)
	issues = append(issues, validateEnumList("allowed_sandbox_modes", r.AllowedSandboxModes, knownSandboxMode, "read-only|workspace-write|danger-full-access")...)
	if r.AllowedWebSearchModes != nil {
		issues = append(issues, validateEnumList("allowed_web_search_modes", *r.AllowedWebSearchModes, knownWebSearchMode, "disabled|cached|live")...)
	}
	issues = append(issues, validateEnumList("allowed_approvals_reviewers", r.AllowedApprovalsReviewers, knownReviewer, "auto_review|user")...)
	if s := strings.TrimSpace(r.EnforceResidency); s != "" && s != "us" {
		issues = append(issues, "enforce_residency "+strconv.Quote(s)+" is not one of us")
	}
	issues = append(issues, validateEnumList("windows.allowed_sandbox_implementations", r.WindowsAllowedSandboxImplementations, knownWindowsSandboxImplementation, "elevated|unelevated")...)
	for i, cfg := range r.RemoteSandboxConfigs {
		issues = append(issues, validateEnumList("remote_sandbox_config["+strconv.Itoa(i)+"].allowed_sandbox_modes", cfg.AllowedSandboxModes, knownSandboxMode, "read-only|workspace-write|danger-full-access")...)
	}
	if dp := strings.TrimSpace(r.DefaultPermissions); dp != "" && r.AllowedPermissionProfiles != nil {
		if !(*r.AllowedPermissionProfiles)[dp] {
			issues = append(issues, "default_permissions "+strconv.Quote(dp)+" is not allowed by allowed_permission_profiles (The profile must be allowed by allowed_permission_profiles)")
		}
	}
	if r.Marketplaces != nil {
		for _, name := range sortedMapKeys(r.Marketplaces.AllowedSources) {
			src := r.Marketplaces.AllowedSources[name]
			switch s := strings.TrimSpace(src.Source); {
			case !knownMarketplaceSource(s):
				issues = append(issues, "marketplaces.allowed_sources."+name+".source "+strconv.Quote(s)+" is not one of git|host_pattern|local")
			case s == "git" && strings.TrimSpace(src.URL) == "":
				issues = append(issues, "marketplaces.allowed_sources."+name+" source git requires url")
			case s == "host_pattern" && strings.TrimSpace(src.HostPattern) == "":
				issues = append(issues, "marketplaces.allowed_sources."+name+" source host_pattern requires host_pattern")
			case s == "local" && strings.TrimSpace(src.Path) == "":
				issues = append(issues, "marketplaces.allowed_sources."+name+" source local requires path")
			}
		}
	}
	for i, rule := range r.PrefixRules {
		if !knownPrefixDecision(strings.TrimSpace(rule.Decision)) {
			issues = append(issues, "rules.prefix_rules["+strconv.Itoa(i)+"].decision must be prompt or forbidden (Requirements rules can only prompt or forbid (not allow))")
		}
		for j, tok := range rule.Pattern {
			hasToken := strings.TrimSpace(tok.Token) != ""
			hasAnyOf := len(tok.AnyOf) > 0
			if hasToken == hasAnyOf {
				issues = append(issues, "rules.prefix_rules["+strconv.Itoa(i)+"].pattern["+strconv.Itoa(j)+"] must set exactly one of token or any_of")
			}
		}
	}
	return issues
}

func (w requirementsWire) toRequirements(md toml.MetaData) Requirements {
	r := Requirements{
		AllowedApprovalPolicies:              w.AllowedApprovalPolicies,
		AllowedSandboxModes:                  w.AllowedSandboxModes,
		AllowedApprovalsReviewers:            w.AllowedApprovalsReviewers,
		EnforceResidency:                     strings.TrimSpace(w.EnforceResidency),
		WindowsAllowedSandboxImplementations: w.windowsSandboxImplementations(),
		RemoteSandboxConfigs:                 remoteSandboxConfigsFromWire(w.RemoteSandboxConfigs),
		AllowRemoteControl:                   w.AllowRemoteControl,
		AllowAppshots:                        w.AllowAppshots,
		AllowManagedHooksOnly:                w.AllowManagedHooksOnly,
		DenyRead:                             w.denyRead(),
		Features:                             w.Features,
		DefaultPermissions:                   strings.TrimSpace(w.DefaultPermissions),
		GuardianPolicyConfig:                 w.GuardianPolicyConfig,
		Marketplaces:                         marketplacesFromWire(w.Marketplaces),
		PrefixRules:                          prefixRulesFromWire(w.Rules),
	}
	if w.ComputerUse != nil {
		r.AllowLockedComputerUse = w.ComputerUse.AllowLockedComputerUse
	}
	if isDefined(md, "allowed_permission_profiles") {
		profiles := make(map[string]bool, len(w.AllowedPermissionProfiles))
		for k, v := range w.AllowedPermissionProfiles {
			profiles[k] = v
		}
		r.AllowedPermissionProfiles = &profiles
	}
	if w.ExperimentalNetwork != nil {
		n := networkFromWire(w.ExperimentalNetwork)
		if n.hasAny() {
			r.Network = &n
		}
	}
	return r
}

func (w requirementsWire) windowsSandboxImplementations() []string {
	if w.Windows == nil {
		return nil
	}
	return w.Windows.AllowedSandboxImplementations
}

func remoteSandboxConfigsFromWire(in []remoteSandboxConfigWire) []RemoteSandboxConfig {
	out := make([]RemoteSandboxConfig, 0, len(in))
	for _, c := range in {
		out = append(out, RemoteSandboxConfig{
			HostnamePatterns:    c.HostnamePatterns,
			AllowedSandboxModes: c.AllowedSandboxModes,
		})
	}
	return out
}

func marketplacesFromWire(w *marketplacesWire) *MarketplacesRequirement {
	if w == nil {
		return nil
	}
	req := &MarketplacesRequirement{
		RestrictToAllowedSources: w.RestrictToAllowedSources,
		AllowedSources:           make(map[string]MarketplaceSource, len(w.AllowedSources)),
	}
	for name, src := range w.AllowedSources {
		req.AllowedSources[name] = MarketplaceSource{
			Source:      strings.TrimSpace(src.Source),
			URL:         strings.TrimSpace(src.URL),
			Ref:         strings.TrimSpace(src.Ref),
			HostPattern: strings.TrimSpace(src.HostPattern),
			Path:        strings.TrimSpace(src.Path),
		}
	}
	return req
}

func prefixRulesFromWire(w *rulesWire) []PrefixRule {
	if w == nil {
		return nil
	}
	out := make([]PrefixRule, 0, len(w.PrefixRules))
	for _, r := range w.PrefixRules {
		pattern := make([]PatternToken, 0, len(r.Pattern))
		for _, tok := range r.Pattern {
			pattern = append(pattern, PatternToken{Token: strings.TrimSpace(tok.Token), AnyOf: tok.AnyOf})
		}
		out = append(out, PrefixRule{
			Pattern:       pattern,
			Decision:      strings.TrimSpace(r.Decision),
			Justification: strings.TrimSpace(r.Justification),
		})
	}
	return out
}

func networkFromWire(w *expNetworkWire) NetworkConfig {
	if w == nil {
		return NetworkConfig{}
	}
	n := NetworkConfig{
		Enabled:           w.Enabled,
		AllowedDomains:    w.AllowedDomains,
		DeniedDomains:     w.DeniedDomains,
		HTTPPort:          w.HTTPPort,
		SocksPort:         w.SocksPort,
		UnixSockets:       w.UnixSockets,
		AllowLocalBinding: w.AllowLocalBinding,
	}
	if w.ManagedAllowedDomainsOnly != nil {
		n.ManagedAllowedDomainsOnly = *w.ManagedAllowedDomainsOnly
	}
	return n
}

func permissionProfilesVersionLine() DryRunLine {
	return DryRunLine{
		Scope: "permission-profiles-version",
		Note:  "allowed_permission_profiles and managed default_permissions require Codex >= " + MinPermissionProfilesCodexVersion + "; Codex 0.137.0 and earlier ignore allowed_permission_profiles and managed default_permissions, and this connector cannot observe the host Codex version from TOML",
	}
}

// sortedMCPNames returns the [mcp_servers.<name>] keys in deterministic order.
func sortedMCPNames(m map[string]mcpServerWire) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// stringFromMap returns m[key] when it is a string (trimmed), else "".
func stringFromMap(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// otelEndpoint returns the first "endpoint" string found anywhere in the [otel] subtree
// (it nests under exporter.<id>.endpoint / trace_exporter.<id>.endpoint). "" when none.
func otelEndpoint(otel map[string]any) string {
	return findEndpoint(otel)
}

func findEndpoint(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if ep, ok := m["endpoint"].(string); ok {
		if t := strings.TrimSpace(ep); t != "" {
			return t
		}
	}
	// Deterministic walk over sub-tables.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ep := findEndpoint(m[k]); ep != "" {
			return ep
		}
	}
	return ""
}

// otelSlotEndpoint reads exporter.<id>.endpoint out of the authored map form, whichever
// struct variant is in the slot. Empty when the slot is a bare string or has no endpoint.
func otelSlotEndpoint(otel map[string]any, key string) string {
	return otelSlotField(otel, key, "endpoint")
}

// otelSlotProtocol reads exporter.<id>.protocol out of the authored map form.
func otelSlotProtocol(otel map[string]any, key string) string {
	return otelSlotField(otel, key, "protocol")
}

func otelSlotField(otel map[string]any, key, field string) string {
	t, ok := otel[key].(map[string]any)
	if !ok {
		return ""
	}
	for _, v := range t {
		inner, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := inner[field].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
