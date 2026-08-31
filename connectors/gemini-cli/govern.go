// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// govern.go turns the resolved settings layers into observations: PERMITTED capability
// edges (configured MCP servers + allowed tools), posture findings for enforcement gaps,
// an effective-config inventory, an observe-coverage finding (does live activity reach the
// OTel collector?), and a Policy-Engine presence finding. Every finding cites the
// exact control key and the scope that won it; PII never applies here (config is not
// payload), but the detail still rides a one-way hash for a stable, greppable ref.
package geminicli

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subject kinds.
const (
	subjectPosture  = "gemini-cli.posture"
	subjectConfig   = "gemini-cli.config"
	subjectCoverage = "gemini-cli.coverage"
	subjectPolicy   = "gemini-cli.policy"
)

// Resource kinds for the PERMITTED capability edges.
const (
	resourceMCPServer = "mcp.server"
	resourceTool      = "gemini.tool"
)

// getters for the resolver (one per governable key).
func getDisableYolo(s settings) *bool {
	if s.Security != nil {
		return s.Security.DisableYoloMode
	}
	return nil
}
func getTelemetryEnabled(s settings) *bool {
	if s.Telemetry != nil {
		return s.Telemetry.Enabled
	}
	return nil
}
func getLogPrompts(s settings) *bool {
	if s.Telemetry != nil {
		return s.Telemetry.LogPrompts
	}
	return nil
}
func getUsageStats(s settings) *bool {
	if s.Privacy != nil {
		return s.Privacy.UsageStatisticsEnabled
	}
	return nil
}
func getTelemetryTarget(s settings) string {
	if s.Telemetry != nil {
		return s.Telemetry.Target
	}
	return ""
}
func getEnforcedAuth(s settings) string {
	if s.Security != nil && s.Security.Auth != nil {
		return s.Security.Auth.EnforcedType
	}
	return ""
}
func getSelectedAuth(s settings) string {
	if s.Security != nil && s.Security.Auth != nil {
		return s.Security.Auth.SelectedType
	}
	return ""
}
func getApprovalMode(s settings) string {
	if s.General != nil {
		return s.General.DefaultApprovalMode
	}
	return ""
}
func getToolsCore(s settings) []string {
	if s.Tools != nil {
		return s.Tools.Core
	}
	return nil
}
func getToolsAllowed(s settings) []string {
	if s.Tools != nil {
		return s.Tools.Allowed
	}
	return nil
}
func getMCPAllowed(s settings) []string {
	if s.MCP != nil {
		return s.MCP.Allowed
	}
	return nil
}

// postureFindings is the governance-gap list. Each gap is a distinct finding keyed on the
// control name (SubjectRef), so the estate can query "which gemini-cli installs disable
// YOLO". Severities follow the verified risk model (research).
func (s *Source) postureFindings(ls layers) []model.FindingReport {
	var out []model.FindingReport
	at := s.clock().UTC()

	// The headline: no admin/system settings tier at all → users fully self-govern.
	if !ls.hasSystemLayer() {
		out = append(out, finding(subjectPosture, "system_settings", model.SeverityHigh,
			"Gemini CLI: no admin/system settings — users fully self-govern",
			"no system settings.json present at "+s.systemPath+"; the enforced override tier is absent, so user/workspace settings are unconstrained", at))
	}

	// YOLO bypass must be disabled at an enforcing scope.
	if val, set, scope := ls.effBool(getDisableYolo); !(set && val) {
		out = append(out, finding(subjectPosture, "security.disableYoloMode", model.SeverityHigh,
			"Gemini CLI: YOLO auto-approve bypass not disabled",
			"security.disableYoloMode is "+boolState(set, val, scope)+"; the agent may run tools without confirmation", at))
	}

	// Telemetry must be on for central audit/observability.
	if val, set, scope := ls.effBool(getTelemetryEnabled); !(set && val) {
		out = append(out, finding(subjectPosture, "telemetry.enabled", model.SeverityHigh,
			"Gemini CLI: telemetry disabled — no central audit/observability",
			"telemetry.enabled is "+boolState(set, val, scope)+"; no logs/metrics/traces flow to a collector (gen_ai.* ingest sees nothing)", at))
	}

	// Prompt logging: runtime default is TRUE, so it is ON unless EXPLICITLY false at a
	// winning scope, whenever telemetry is enabled.
	if telVal, telSet, _ := ls.effBool(getTelemetryEnabled); telSet && telVal {
		if lpVal, lpSet, scope := ls.effBool(getLogPrompts); !(lpSet && !lpVal) {
			out = append(out, finding(subjectPosture, "telemetry.logPrompts", model.SeverityMedium,
				"Gemini CLI: prompt content logged to telemetry",
				"telemetry.logPrompts is "+logPromptsState(lpSet, lpVal, scope)+" (runtime default true) while telemetry is enabled; prompts are written to the telemetry sink", at))
		}
	}

	// Approval mode: auto_edit silently accepts edits.
	if mode, scope := ls.effStr(getApprovalMode); mode == "auto_edit" {
		out = append(out, finding(subjectPosture, "general.defaultApprovalMode", model.SeverityMedium,
			"Gemini CLI: auto-approve edits (defaultApprovalMode=auto_edit)",
			"general.defaultApprovalMode=auto_edit at scope "+scope+"; file edits are applied without confirmation", at))
	}

	// Tool allowlist: with neither tools.core nor tools.allowed set, the tool surface is wide.
	_, coreScope := ls.effStrs(getToolsCore)
	_, allowedScope := ls.effStrs(getToolsAllowed)
	if coreScope == "" && allowedScope == "" {
		out = append(out, finding(subjectPosture, "tools.allowlist", model.SeverityMedium,
			"Gemini CLI: no tool allowlist — wide tool surface",
			"neither tools.core nor tools.allowed is set at any scope; the agent's tool surface is unbounded", at))
	}

	// MCP allowlist: without mcp.allowed, any configured MCP server is permitted.
	if _, scope := ls.effStrs(getMCPAllowed); scope == "" {
		out = append(out, finding(subjectPosture, "mcp.allowed", model.SeverityMedium,
			"Gemini CLI: no MCP server allowlist — any configured MCP server permitted",
			"mcp.allowed is unset at any scope; user-defined MCP servers are not constrained to an allowlist", at))
	}

	// Auth type pinning.
	if pinned, _ := ls.effStr(getEnforcedAuth); pinned == "" {
		out = append(out, finding(subjectPosture, "security.auth.enforcedType", model.SeverityLow,
			"Gemini CLI: auth type not pinned",
			"security.auth.enforcedType is unset; users may select any auth mode (oauth-personal/gemini-api-key/vertex-ai/...)", at))
	}

	// Anonymized usage statistics to Google (default true).
	if val, set, scope := ls.effBool(getUsageStats); !(set && !val) {
		out = append(out, finding(subjectPosture, "privacy.usageStatisticsEnabled", model.SeverityLow,
			"Gemini CLI: anonymized usage statistics sent to Google",
			"privacy.usageStatisticsEnabled is "+boolState(set, val, scope)+" (default true); anonymized usage telemetry goes to Google", at))
	}

	return out
}

// permittedEdges emits the agent's declared, wired capabilities as PERMITTED config edges
// (Source=config, attributed — read directly from the operator's config): each configured
// MCP server and each allowed tool. Mode is unknown (the config grants reach, not a
// read/write classification), which the connector states honestly rather than guessing.
func (s *Source) permittedEdges(ls layers) []model.EdgeObservation {
	at := s.clock().UTC()
	var out []model.EdgeObservation

	servers := ls.mcpServers()
	names := sortedKeys(servers)
	for _, name := range names {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceMCPServer, ResourceRef: servers[name],
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: name, ObservedAt: at,
		})
	}

	for _, tool := range s.allowedTools(ls) {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceTool, ResourceRef: tool,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		})
	}
	return out
}

// allowedTools is the de-duplicated union of the effective tools.core + tools.allowed
// allowlist (the agent's permitted built-in/configured tools).
func (s *Source) allowedTools(ls layers) []string {
	seen := map[string]bool{}
	var out []string
	core, _ := ls.effStrs(getToolsCore)
	allowed, _ := ls.effStrs(getToolsAllowed)
	for _, t := range append(append([]string{}, core...), allowed...) {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// inventoryFinding summarizes the effective config so the estate sees how this install is
// configured at a glance.
func (s *Source) inventoryFinding(ls layers) model.FindingReport {
	auth, _ := ls.effStr(getSelectedAuth)
	enforced, _ := ls.effStr(getEnforcedAuth)
	mode, _ := ls.effStr(getApprovalMode)
	telOn, _, _ := ls.effBool(getTelemetryEnabled)
	target, _ := ls.effStr(getTelemetryTarget)
	servers := ls.mcpServers()
	tools := s.allowedTools(ls)

	detail := strings.Join([]string{
		"auth=" + firstNonEmpty(auth, "unset"),
		"enforced=" + firstNonEmpty(enforced, "none"),
		"approval=" + firstNonEmpty(mode, "default"),
		"telemetry=" + telState(telOn, target),
		"mcp_servers=" + strconv.Itoa(len(servers)),
		"allowed_tools=" + strconv.Itoa(len(tools)),
		"system_layer=" + strconv.FormatBool(ls.hasSystemLayer()),
	}, " ")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectConfig,
		SubjectRef:  s.agentRef,
		Title:       "Gemini CLI effective config (" + detail + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// coverageFinding states whether live activity reaches the control-plane OTel collector
// (the gen_ai.* ingest) given the telemetry settings — the honest OBSERVE posture.
func (s *Source) coverageFinding(ls layers) model.FindingReport {
	at := s.clock().UTC()
	telOn, telSet, _ := ls.effBool(getTelemetryEnabled)
	target, _ := ls.effStr(getTelemetryTarget)

	var sev model.Severity
	var title, detail string
	switch {
	case !(telSet && telOn):
		sev = model.SeverityMedium
		title = "Gemini CLI: no live OTel observability (telemetry off)"
		detail = "telemetry is disabled; set telemetry.enabled=true with target=local and an OTLP endpoint pointed at the control-plane collector to ingest gen_ai.* live activity"
	case target == "gcp":
		sev = model.SeverityLow
		title = "Gemini CLI: telemetry exports to Google Cloud, not the control plane"
		detail = "telemetry.target=gcp sends logs/metrics/traces to Google Cloud Trace/Monitoring; for control-plane gen_ai.* ingest set target=local with an OTLP endpoint at our collector"
	default: // local (or default-local)
		sev = model.SeverityInfo
		title = "Gemini CLI: telemetry local — wire the OTLP collector to the control plane"
		detail = "telemetry.target is local; point its OTLP endpoint at the control-plane collector to ingest gen_ai.* live activity. The gemini-cli also emits the gemini_cli.* namespace alongside gen_ai.*"
	}
	return model.FindingReport{
		Kind:        "coverage",
		Severity:    sev,
		SubjectKind: subjectCoverage,
		SubjectRef:  s.agentRef,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

// policyPresenceFinding reports whether the admin Policy Engine directory holds rule files.
// The policy engine uses a 5-tier priority model (DEFAULT=1, EXTENSION=2, WORKSPACE=3,
// USER=4, ADMIN=5) with numeric priority 0-999 within each tier; admin rules at tier 5
// override all other tiers. Presence/absence is a real governance signal; parsing the
// per-rule TOML is a declared follow-up (no TOML dependency is pulled in for a secondary
// surface). ok is false when no policy dir is configured/exists (an optional surface —
// not flagged as absent).
func (s *Source) policyPresenceFinding() (model.FindingReport, bool) {
	dir := s.adminPolicy
	if dir == "" {
		return model.FindingReport{}, false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return model.FindingReport{}, false // dir absent → an optional surface, not flagged
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	at := s.clock().UTC()
	if len(matches) == 0 {
		return finding(subjectPolicy, "policy_engine", model.SeverityLow,
			"Gemini CLI: admin Policy Engine directory present but empty",
			"the admin policy dir "+dir+" exists with no *.toml rule files; admin-tier (priority 5) per-tool allow/deny/ask_user decisions are ungoverned by the Policy Engine", at), true
	}
	return finding(subjectPolicy, "policy_engine", model.SeverityInfo,
		"Gemini CLI: admin Policy Engine active ("+strconv.Itoa(len(matches))+" rule file(s))",
		"the admin policy dir "+dir+" holds "+strconv.Itoa(len(matches))+" admin-tier (priority 5) *.toml rule file(s); per-rule content parsing is a declared follow-up", at), true
}

// invalidLayerFinding flags a present-but-malformed settings layer (it cannot be governed —
// the CLI may also reject it, leaving the host on a different effective config).
func (s *Source) invalidLayerFinding(scope string) model.FindingReport {
	return finding(subjectPosture, "settings.invalid."+scope, model.SeverityMedium,
		"Gemini CLI: "+scope+" settings present but invalid JSON",
		"the "+scope+" settings.json failed to parse; its controls cannot be verified and the CLI may apply a different effective config", s.clock().UTC())
}

// finding builds a posture finding with a hashed, greppable detail ref. SubjectRef is the
// control key so each gap is a distinct, queryable finding.
func finding(subjectKind, ref string, sev model.Severity, title, detail string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  redact.Hash(ref + ": " + detail),
		OccurredAt:  at,
	}
}

// boolState renders an effective *bool key for a finding detail: "unset (no layer)" or the
// value plus the scope that won it.
func boolState(set, val bool, scope string) string {
	if !set {
		return "unset (no layer sets it)"
	}
	return strconv.FormatBool(val) + " at scope " + scope
}

// logPromptsState is boolState specialized to the logPrompts runtime default (true).
func logPromptsState(set, val bool, scope string) string {
	if !set {
		return "unset (runtime default true)"
	}
	return strconv.FormatBool(val) + " at scope " + scope
}

// telState renders the telemetry state for the inventory summary.
func telState(on bool, target string) string {
	if !on {
		return "off"
	}
	return "on/" + firstNonEmpty(target, "local")
}

// sortedKeys returns a map's keys in deterministic order (stable edge emission for tests).
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
