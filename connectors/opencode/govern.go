// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	subjectPosture  = "opencode.posture"
	subjectConfig   = "opencode.config"
	subjectCoverage = "opencode.coverage"

	resourceMCPServer = "mcp.server"
	resourceTool      = "opencode.tool"
	resourceAgent     = "opencode.agent"
)

type managedConfigStatus struct {
	present bool
	path    string
}

func (s *Source) postureFindings(c config) []model.FindingReport {
	var out []model.FindingReport
	at := s.clock().UTC()

	managed := detectManagedConfig()
	if managed.present {
		out = append(out, finding(subjectPosture, "admin_override.present", model.SeverityInfo,
			"opencode: managed admin config present (not immutable; OPENCODE_PERMISSION may override)",
			"managed config detected at "+redact.Clean(managed.path)+"; opencode merges managed config last, but OPENCODE_PERMISSION can override permission, OPENCODE_TEST_MANAGED_CONFIG_DIR can redirect the directory, per-key merge is not an immutable lock, and remote organization config is outside local-file coverage", at))
	} else {
		out = append(out, finding(subjectPosture, "admin_override.absent", model.SeverityHigh,
			"opencode: no local managed admin config detected (remote org config not visible locally)",
			"no opencode.json/opencode.jsonc managed config was found in the OS managed directory; user and project config remain self-governed unless a remote organization layer exists outside this reader", at))
	}

	if c.hasPermissiveDefault() {
		out = append(out, finding(subjectPosture, "permission.default", model.SeverityHigh,
			"opencode: no permission block — permissive default",
			"no effective top-level or primary-agent permission block is configured; opencode defaults most tools to allow and the built-in build agent is permissive by default", at))
	}

	for _, subject := range c.blanketAllowSubjects() {
		out = append(out, finding(subjectPosture, "permission.allow."+subject, model.SeverityHigh,
			"opencode: blanket permission allow configured for "+subject,
			"permission is set to allow for "+subject+"; this bypasses per-tool ask/deny governance", at))
	}

	perm := c.effectivePrimaryPermission()
	if !permissionActionGated(perm, "bash") {
		out = append(out, finding(subjectPosture, "permission.bash", model.SeverityHigh,
			"opencode: bash is not gated by ask/deny",
			"the effective primary-agent permission does not gate bash with ask or deny; shell execution may proceed without approval", at))
	}
	if !permissionActionGated(perm, "edit") {
		out = append(out, finding(subjectPosture, "permission.edit", model.SeverityHigh,
			"opencode: edit/write/patch is not gated by ask/deny",
			"the effective primary-agent permission does not gate edit with ask or deny; edit also controls write, edit, and patch tools", at))
	}

	servers := c.enabledMCPServers()
	if len(servers) > 0 {
		out = append(out, finding(subjectPosture, "mcp.allowlist", model.SeverityMedium,
			"opencode: "+strconv.Itoa(len(servers))+" MCP server(s) configured without a separate allowlist mechanism",
			"opencode permits configured MCP servers from config; there is no distinct admin allowlist surface beyond replacing or constraining the mcp map in managed config", at))
	}

	if c.credentialInConfig() {
		out = append(out, finding(subjectPosture, "provider.apiKey", model.SeverityHigh,
			"opencode: literal provider apiKey in config",
			"provider.options.apiKey contains a literal value; use {env:VAR}, {file:path}, or an external secret mechanism instead of storing credentials in opencode config", at))
	}

	if c.shareMode() == "auto" {
		out = append(out, finding(subjectPosture, "share.auto", model.SeverityHigh,
			"opencode: share auto enabled (automatic session-data egress)",
			"share=auto automatically publishes session data to opencode's share server; set share=disabled for governed environments", at))
	}

	if c.autoupdateEnabled() {
		out = append(out, finding(subjectPosture, "autoupdate.true", model.SeverityMedium,
			"opencode: autoupdate enabled",
			"autoupdate=true allows the local agent binary to update itself automatically; governed environments should pin updates or use notify mode", at))
	}

	if c.continueLoopOnDeny() {
		out = append(out, finding(subjectPosture, "experimental.continue_loop_on_deny", model.SeverityHigh,
			"opencode: continue-loop-on-deny enabled",
			"experimental.continue_loop_on_deny=true keeps the agent loop running after a denied tool call, increasing autonomy after policy denial", at))
	}

	out = append(out, finding(subjectPosture, "permission.runtime_bypass", model.SeverityInfo,
		"opencode: OPENCODE_PERMISSION runtime override is outside local-file coverage",
		"OPENCODE_PERMISSION can override permission at runtime, including managed permission; this local config reader reports the caveat but cannot prove the target process environment", at))

	return out
}

func (s *Source) permittedEdges(c config) []model.EdgeObservation {
	at := s.clock().UTC()
	var out []model.EdgeObservation

	servers := c.enabledMCPServers()
	for _, name := range sortedStringKeys(servers) {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceMCPServer, ResourceRef: safeResourceRef(servers[name]),
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: name, ObservedAt: at,
		})
	}

	for _, tool := range c.enabledTools() {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceTool, ResourceRef: tool,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		})
	}

	for _, agent := range c.customAgents() {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: s.agentRef,
			ResourceKind: resourceAgent, ResourceRef: agent,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		})
	}

	return out
}

func (s *Source) inventoryFinding(c config) model.FindingReport {
	servers := c.enabledMCPServers()
	tools := c.enabledTools()
	agents := c.customAgents()
	detail := strings.Join([]string{
		"model=" + firstNonEmpty(pointerString(c.Model), "unset"),
		"small_model=" + firstNonEmpty(pointerString(c.SmallModel), "unset"),
		"default_agent=" + c.primaryAgentName(),
		"permission=" + c.permissionMode(),
		"bash_gated=" + boolStr(permissionActionGated(c.effectivePrimaryPermission(), "bash")),
		"edit_gated=" + boolStr(permissionActionGated(c.effectivePrimaryPermission(), "edit")),
		"mcp_servers=" + strconv.Itoa(len(servers)),
		"enabled_tools=" + strconv.Itoa(len(tools)),
		"custom_agents=" + strconv.Itoa(len(agents)),
		"providers=" + strconv.Itoa(len(c.Provider)),
		"enabled_providers=" + strconv.Itoa(len(c.EnabledProviders)),
		"disabled_providers=" + strconv.Itoa(len(c.DisabledProviders)),
		"share=" + c.shareMode(),
		"otel=" + boolStr(c.otelEnabled()),
		"autoupdate=" + c.autoupdateLabel(),
		"continue_loop_on_deny=" + boolStr(c.continueLoopOnDeny()),
		"api_key_literal=" + boolStr(c.credentialInConfig()),
		"instructions=" + strconv.Itoa(len(c.Instructions)),
		"agents_md=" + boolStr(instructionFilePresent(s.projectPath)),
	}, " ")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectConfig,
		SubjectRef:  s.agentRef,
		Title:       "opencode effective config (" + detail + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) coverageFinding(c config) model.FindingReport {
	at := s.clock().UTC()
	var sev model.Severity
	var title, detail string
	if c.otelEnabled() {
		sev = model.SeverityInfo
		title = "opencode: native OTEL enabled — verify OTEL_* exporter reaches the control plane"
		detail = "experimental.openTelemetry=true enables native AI-SDK-governed gen_ai.* telemetry; live usage can ride the OTLP ingest if out-of-band OTEL_* exporter settings point at the control-plane collector. Exact gen_ai.* attribute names are AI-SDK-governed and not asserted here"
	} else {
		sev = model.SeverityMedium
		title = "opencode: no live OTEL observability (experimental.openTelemetry off)"
		detail = "experimental.openTelemetry is off by default and opencode has no OTLP endpoint field in config; configure experimental.openTelemetry=true and out-of-band OTEL_* exporter settings for gen_ai.* live activity ingest"
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

func (s *Source) invalidConfigFinding(scope string) model.FindingReport {
	return finding(subjectPosture, "config.invalid."+scope, model.SeverityMedium,
		"opencode: "+scope+" config present but invalid JSONC",
		"the "+scope+" opencode config failed to parse; that layer's controls cannot be verified", s.clock().UTC())
}

func detectManagedConfig() managedConfigStatus {
	for _, dir := range managedConfigDirs() {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err == nil && !info.IsDir() {
				return managedConfigStatus{present: true, path: path}
			}
		}
	}
	return managedConfigStatus{}
}

func managedConfigDirs() []string {
	if override := strings.TrimSpace(os.Getenv("OPENCODE_TEST_MANAGED_CONFIG_DIR")); override != "" {
		return []string{override}
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/opencode"}
	case "windows":
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			return []string{filepath.Join(programData, "opencode")}
		}
		return []string{`C:\ProgramData\opencode`}
	default:
		return []string{"/etc/opencode"}
	}
}

func instructionFilePresent(projectPath string) bool {
	dir := dirOf(projectPath)
	if dir == "" {
		return false
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func dirOf(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}

func permissionActionGated(p *permissionConfig, tool string) bool {
	return p != nil && p.actionGated(tool)
}

func safeResourceRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "://") || strings.Contains(ref, "@") {
		return redact.SanitizeURL(ref)
	}
	return redact.Clean(ref)
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

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func pointerString(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
