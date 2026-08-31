// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// govern.go turns the resolved Goose profile configuration into observations: PERMITTED
// capability edges (configured extensions/MCP servers + tools), posture findings for
// enforcement gaps, an effective-config inventory, and a coverage finding documenting
// Goose's limited native OTEL support (honest blind spot).
package goose

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
	subjectPosture  = "goose.posture"
	subjectConfig   = "goose.config"
	subjectCoverage = "goose.coverage"

	resourceMCPServer = "mcp.server"
	resourceTool      = "goose.tool"

	costType = "goose"
)

func (s *Source) postureFindings(c profileConfig) []model.FindingReport {
	var out []model.FindingReport
	at := s.clock().UTC()

	// No admin/system override layer — Goose has no admin settings mechanism.
	// The profiles.yaml is user-controlled; there is no system-tier enforcement.
	out = append(out, finding(subjectPosture, "admin_override", model.SeverityHigh,
		"Goose: no admin/system settings override — users fully self-govern",
		"Goose has no admin/system settings tier; the profiles.yaml is user-controlled with no enforced override mechanism", at))

	// Provider not pinned.
	if c.effectiveProvider() == "" && c.effectiveModel() == "" {
		out = append(out, finding(subjectPosture, "provider.model", model.SeverityMedium,
			"Goose: provider and model not pinned",
			"neither provider nor model is set in the active profile "+strconv.Quote(c.profileName)+"; no model governance", at))
	}

	// Extensions unrestricted: no extension allowlist mechanism exists.
	exts := c.enabledExtensions()
	if len(exts) > 0 {
		// Extensions are present — flag that there is no allowlist mechanism to constrain them.
		out = append(out, finding(subjectPosture, "extensions.allowlist", model.SeverityMedium,
			"Goose: "+strconv.Itoa(len(exts))+" extension(s) configured with no admin allowlist",
			"Goose has no admin-controlled extension allowlist; any user-configured extension (MCP server) is permitted", at))
	}

	// Telemetry: Goose has limited native OTEL.
	if !gooseOtelConfigured() {
		out = append(out, finding(subjectPosture, "telemetry", model.SeverityMedium,
			"Goose: no OTEL telemetry configured",
			"Goose has limited native OTEL support; no OTEL_EXPORTER_OTLP_ENDPOINT is set for gen_ai.* span export", at))
	}

	// Tool approval: auto-approve or no confirmation.
	if required, configured := c.requiresApproval(); configured && !required {
		out = append(out, finding(subjectPosture, "toolshim.require_approval", model.SeverityMedium,
			"Goose: tool execution does not require approval",
			"toolshim.require_approval=false in profile "+strconv.Quote(c.profileName)+"; tools run without human confirmation", at))
	}

	// Code isolation: Goose does not sandbox code execution by default.
	out = append(out, finding(subjectPosture, "code_isolation", model.SeverityLow,
		"Goose: no code execution sandbox — code runs in user context",
		"Goose executes code directly in the user's environment with no sandbox isolation; this is by design (Goose does not provide a sandboxed runtime)", at))

	return out
}

func (s *Source) permittedEdges(c profileConfig) []model.EdgeObservation {
	at := s.clock().UTC()
	var out []model.EdgeObservation

	exts := c.enabledExtensions()
	names := sortedKeys(exts)
	for _, name := range names {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceMCPServer, ResourceRef: extensionRef(name, exts[name]),
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: name, ObservedAt: at,
		})
	}

	for _, tool := range c.allowedTools() {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceTool, ResourceRef: tool,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		})
	}

	return out
}

func (s *Source) inventoryFinding(c profileConfig) model.FindingReport {
	mdPresent := agentsMDPresent(expandHome(s.configPath))
	exts := c.enabledExtensions()

	detail := strings.Join([]string{
		"profile=" + firstNonEmpty(c.profileName, "default"),
		"provider=" + firstNonEmpty(c.effectiveProvider(), "unset"),
		"model=" + firstNonEmpty(c.effectiveModel(), "unset"),
		"extensions=" + strconv.Itoa(len(exts)),
		"tool_allowlist=" + strconv.FormatBool(c.hasToolAllowlist()),
		"agents_md=" + strconv.FormatBool(mdPresent),
	}, " ")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectConfig,
		SubjectRef:  s.agentRef,
		Title:       "Goose effective config (" + detail + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) coverageFinding(c profileConfig) model.FindingReport {
	at := s.clock().UTC()
	// Goose has limited native OTEL — documented as honest blind spot.
	var sev model.Severity
	var title, detail string
	if gooseOtelConfigured() {
		sev = model.SeverityLow
		title = "Goose: OTEL endpoint set — limited native gen_ai.* support"
		detail = "OTEL_EXPORTER_OTLP_ENDPOINT is set but Goose has limited native gen_ai.* semconv instrumentation; verify the harness emits gen_ai.* spans to the control-plane collector"
	} else {
		sev = model.SeverityMedium
		title = "Goose: no OTEL observability — limited native support"
		detail = "Goose has limited native OTEL gen_ai.* instrumentation and no exporter is configured; consider a proxy/wrapper for gen_ai.* span generation to enable live activity observability"
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

func (s *Source) invalidConfigFinding() model.FindingReport {
	return finding(subjectPosture, "config.invalid", model.SeverityMedium,
		"Goose: profiles.yaml present but invalid YAML",
		"the profiles.yaml at "+s.configPath+" failed to parse; its controls cannot be verified", s.clock().UTC())
}

func gooseOtelConfigured() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

func agentsMDPresent(configPath string) bool {
	dir := dirOf(configPath)
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir + "/AGENTS.md")
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

func sortedKeys[V any](m map[string]V) []string {
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
