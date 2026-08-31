// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// govern.go turns the resolved OpenHands configuration into observations: PERMITTED
// capability edges (configured MCP servers + action plugins), posture findings for
// enforcement gaps, an effective-config inventory, and a coverage finding documenting
// whether live OTEL gen_ai.* activity reaches the control-plane collector.
package openhands

import (
	"sort"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	subjectPosture  = "openhands.posture"
	subjectConfig   = "openhands.config"
	subjectCoverage = "openhands.coverage"

	resourceMCPServer = "mcp.server"
	resourceAction    = "openhands.action"

	costType = "openhands"
)

func (s *Source) postureFindings(c config) []model.FindingReport {
	var out []model.FindingReport
	at := s.clock().UTC()

	// Sandbox type: unrestricted code execution is the headline risk.
	sandboxType := c.effectiveSandboxType()
	switch sandboxType {
	case "docker", "e2b", "remote":
		// Governed sandbox — no finding.
	default:
		out = append(out, finding(subjectPosture, "sandbox.type", model.SeverityHigh,
			"OpenHands: sandbox type not hardened",
			"sandbox.sandbox_type="+strconv.Quote(sandboxType)+"; code execution runs without a hardened sandbox (docker/e2b/remote recommended)", at))
	}

	// LLM model pinning.
	if c.effectiveModel() == "" {
		out = append(out, finding(subjectPosture, "llm.model", model.SeverityMedium,
			"OpenHands: LLM model not pinned",
			"llm.model is unset; no model governance — the agent may use any available model", at))
	}

	// Credential exposure: API key in the TOML config file.
	if c.hasAPIKeyInConfig() {
		out = append(out, finding(subjectPosture, "llm.api_key", model.SeverityHigh,
			"OpenHands: API key in config.toml (credential exposure)",
			"llm.api_key is set in config.toml; credentials should be in environment variables or a secret store, not in a config file", at))
	}

	// Telemetry: OTEL exporter not configured.
	if !c.otelEnabled() {
		out = append(out, finding(subjectPosture, "otel.exporter", model.SeverityMedium,
			"OpenHands: no OTEL exporter configured — no central observability",
			"neither core.otel_exporter_otlp_endpoint nor OTEL_EXPORTER_OTLP_ENDPOINT is set; gen_ai.* spans will not reach any collector", at))
	}

	// Sandbox permissions: file browsing unrestricted.
	if browsable, set := c.getBool("sandbox.browsergym_eval"); set && browsable {
		out = append(out, finding(subjectPosture, "sandbox.browsergym", model.SeverityMedium,
			"OpenHands: browser sandbox evaluation mode enabled",
			"sandbox.browsergym_eval=true; the sandbox has browser access which expands the attack surface", at))
	}

	// Max iterations: unset or very high → runaway agent risk.
	if maxIter, ok := c.effectiveMaxIterations(); ok && maxIter > 200 {
		out = append(out, finding(subjectPosture, "core.max_iterations", model.SeverityLow,
			"OpenHands: max_iterations is very high ("+strconv.FormatInt(maxIter, 10)+")",
			"core.max_iterations="+strconv.FormatInt(maxIter, 10)+"; a very high limit increases the risk of runaway agent cost and behavior", at))
	} else if !ok && !c.present {
		out = append(out, finding(subjectPosture, "core.max_iterations", model.SeverityLow,
			"OpenHands: max_iterations not configured",
			"core.max_iterations is unset; the agent may run with the default limit which may be too high for production", at))
	}

	return out
}

func (s *Source) permittedEdges(c config) []model.EdgeObservation {
	at := s.clock().UTC()
	var out []model.EdgeObservation

	servers := c.mcpServers()
	for _, name := range sortedKeys(servers) {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceMCPServer, ResourceRef: servers[name],
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: name, ObservedAt: at,
		})
	}

	for _, plugin := range c.actionPlugins() {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceAction, ResourceRef: plugin,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		})
	}

	return out
}

func (s *Source) inventoryFinding(c config) model.FindingReport {
	mdPresent := agentsMDPresent(expandHome(s.configPath))
	servers := c.mcpServers()
	plugins := c.actionPlugins()

	detail := join(" ",
		"model="+firstNonEmpty(c.effectiveModel(), "unset"),
		"sandbox="+firstNonEmpty(c.effectiveSandboxType(), "unset"),
		"otel="+boolStr(c.otelEnabled()),
		"mcp_servers="+strconv.Itoa(len(servers)),
		"action_plugins="+strconv.Itoa(len(plugins)),
		"api_key_in_config="+boolStr(c.hasAPIKeyInConfig()),
		"agents_md="+boolStr(mdPresent),
	)
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectConfig,
		SubjectRef:  s.agentRef,
		Title:       "OpenHands effective config (" + detail + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) coverageFinding(c config) model.FindingReport {
	at := s.clock().UTC()
	var sev model.Severity
	var title, detail string

	ep := c.otelEndpoint()
	switch {
	case !c.otelEnabled():
		sev = model.SeverityMedium
		title = "OpenHands: no live OTEL observability (exporter not configured)"
		detail = "no OTEL exporter endpoint configured; set OTEL_EXPORTER_OTLP_ENDPOINT or core.otel_exporter_otlp_endpoint pointed at the control-plane collector to ingest gen_ai.* live activity"
	case ep != "":
		sev = model.SeverityInfo
		title = "OpenHands: OTEL exporter configured — verify endpoint reaches the control plane"
		detail = "OTEL exporter endpoint set to " + redact.Clean(ep) + "; verify this points at the control-plane collector for gen_ai.* ingest. OpenHands emits the vendor-neutral gen_ai.* semconv profile"
	default:
		sev = model.SeverityLow
		title = "OpenHands: OTEL exporter partially configured"
		detail = "OTEL exporter appears configured but the endpoint is not resolvable; verify the OTLP target"
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
		"OpenHands: config.toml present but invalid TOML",
		"the config.toml at "+s.configPath+" failed to parse; its controls cannot be verified", s.clock().UTC())
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

func sortedKeys(m map[string]string) []string {
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

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func join(sep string, parts ...string) string {
	var sb []string
	for _, p := range parts {
		if p != "" {
			sb = append(sb, p)
		}
	}
	result := ""
	for i, p := range sb {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}
