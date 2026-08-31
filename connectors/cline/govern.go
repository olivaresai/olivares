// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// govern.go turns the resolved Cline/Kilo Code VSCode settings into observations:
// PERMITTED capability edges (configured MCP servers + allowed tools), posture findings
// for enforcement gaps, an effective-config inventory, and a coverage finding documenting
// that Cline has no native OTEL (honest blind spot).
package cline

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	subjectPosture  = "cline.posture"
	subjectConfig   = "cline.config"
	subjectCoverage = "cline.coverage"

	resourceMCPServer = "mcp.server"
	resourceTool      = "cline.tool"

	costType = "cline"
)

func (s *Source) postureFindings(ls layers) []model.FindingReport {
	var out []model.FindingReport
	at := s.clock().UTC()
	label := s.variantLabel()

	// Auto-approve: the headline risk — agent runs without confirmation.
	if ls.hasAutoApprove() {
		out = append(out, finding(subjectPosture, "autoApprove", model.SeverityHigh,
			label+": auto-approve enabled — agent runs without confirmation",
			"auto-approve is enabled; the agent may execute tool calls, file writes, or commands without human confirmation", at))
	}

	// MCP server allowlist: any configured MCP server is permitted.
	servers := ls.mcpServers()
	if len(servers) > 0 {
		out = append(out, finding(subjectPosture, "mcp.allowlist", model.SeverityMedium,
			label+": "+strconv.Itoa(len(servers))+" MCP server(s) configured with no admin allowlist",
			label+" has no admin-controlled MCP server allowlist; any user-configured MCP server is permitted in VSCode settings", at))
	}

	// Credential exposure: API key in VSCode settings.
	if ls.hasAPIKeyInSettings() {
		out = append(out, finding(subjectPosture, "apiKey", model.SeverityHigh,
			label+": API key in VSCode settings (credential exposure)",
			"an API key is set in VSCode settings.json; credentials should be in environment variables or a secret store, not in a settings file accessible to all extensions", at))
	}

	// Model not pinned.
	if ls.effProvider() == "" && ls.effModel() == "" {
		out = append(out, finding(subjectPosture, "provider.model", model.SeverityMedium,
			label+": provider and model not pinned",
			"neither apiProvider nor apiModelId is set; no model governance — the extension may use any available model", at))
	}

	// Custom instructions: potential injection vector.
	if ls.hasCustomInstructions() {
		out = append(out, finding(subjectPosture, "customInstructions", model.SeverityLow,
			label+": custom instructions set (review for injection risk)",
			"customInstructions is set in settings; review content for instruction-injection risks — the agentsmd connector scans instruction files separately", at))
	}

	// Telemetry: Cline has no native OTEL.
	out = append(out, finding(subjectPosture, "telemetry", model.SeverityMedium,
		label+": no native OTEL instrumentation",
		label+" has no native OTEL gen_ai.* instrumentation; observability requires a proxy/wrapper or the MCP gateway to generate gen_ai.* spans", at))

	return out
}

func (s *Source) permittedEdges(ls layers) []model.EdgeObservation {
	at := s.clock().UTC()
	var out []model.EdgeObservation

	servers := ls.mcpServers()
	names := sortedKeysStr(servers)
	for _, name := range names {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceMCPServer, ResourceRef: servers[name],
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: name, ObservedAt: at,
		})
	}

	tools := ls.allowedTools()
	sort.Strings(tools)
	for _, tool := range tools {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceTool, ResourceRef: tool,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		})
	}

	return out
}

func (s *Source) inventoryFinding(ls layers) model.FindingReport {
	mdPresent := agentsMDPresent(s.workspacePath)
	servers := ls.mcpServers()
	tools := ls.allowedTools()

	detail := strings.Join([]string{
		"variant=" + s.variant,
		"provider=" + firstNonEmpty(ls.effProvider(), "unset"),
		"model=" + firstNonEmpty(ls.effModel(), "unset"),
		"auto_approve=" + strconv.FormatBool(ls.hasAutoApprove()),
		"mcp_servers=" + strconv.Itoa(len(servers)),
		"allowed_tools=" + strconv.Itoa(len(tools)),
		"custom_instructions=" + strconv.FormatBool(ls.hasCustomInstructions()),
		"api_key_in_settings=" + strconv.FormatBool(ls.hasAPIKeyInSettings()),
		"agents_md=" + strconv.FormatBool(mdPresent),
	}, " ")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectConfig,
		SubjectRef:  s.agentRef,
		Title:       s.variantLabel() + " effective config (" + detail + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) coverageFinding() model.FindingReport {
	at := s.clock().UTC()
	label := s.variantLabel()
	return model.FindingReport{
		Kind:        "coverage",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectCoverage,
		SubjectRef:  s.agentRef,
		Title:       label + ": no native OTEL observability",
		DetailHash: redact.Hash(label + " has no native OTEL gen_ai.* instrumentation; " +
			"observability requires a proxy/wrapper or the MCP gateway to generate gen_ai.* spans for control-plane ingest"),
		OccurredAt: at,
	}
}

func (s *Source) invalidLayerFinding(scope string) model.FindingReport {
	return finding(subjectPosture, "settings.invalid."+scope, model.SeverityMedium,
		s.variantLabel()+": "+scope+" settings present but invalid JSON",
		"the "+scope+" settings.json failed to parse; its controls cannot be verified", s.clock().UTC())
}

func (s *Source) variantLabel() string {
	if s.variant == variantKiloCode {
		return "Kilo Code"
	}
	return "Cline"
}

func agentsMDPresent(workspacePath string) bool {
	if workspacePath == "" {
		return false
	}
	dir := dirOf(workspacePath)
	if dir == "" {
		return false
	}
	// workspace settings are in .vscode/settings.json — go up one level.
	parent := dirOf(dir)
	if parent == "" {
		return false
	}
	info, err := os.Stat(parent + "/AGENTS.md")
	return err == nil && !info.IsDir()
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

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

func sortedKeysStr(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
