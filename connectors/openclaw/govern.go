// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// govern.go turns the resolved OpenClaw JSON5 configuration into observations:
// posture findings from the catalog, config-declared channel/skill/model
// edges, one install inventory finding, and one diagnostics coverage finding.
package openclaw

import (
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	subjectPosture  = "openclaw.posture"
	subjectConfig   = "openclaw.config"
	subjectCoverage = "openclaw.coverage"

	resourceChannel = "openclaw.channel"
	resourceSkill   = "openclaw.skill"
	resourceModel   = "openclaw.model"
	resourceMCP     = "openclaw.mcp_server"

	CostType = "openclaw"
)

func (s *Source) findings(c clawConfig) []model.FindingReport {
	out := s.postureFindings(c)
	out = append(out, s.inventoryFinding(c), s.coverageFinding(c))
	out = append(out, s.skillSupplyChainFindings(c)...)
	return out
}

func (s *Source) postureFindings(c clawConfig) []model.FindingReport {
	var out []model.FindingReport
	at := s.clock().UTC()
	add := func(ref string, sev model.Severity, title, detail string) {
		out = append(out, finding(ref, c.AgentRef, sev, title, detail, at))
	}
	addAgent := func(agent effectiveAgent, ref string, sev model.Severity, title, detail string) {
		out = append(out, finding(ref, agent.Subject, sev, title, detail, at))
	}

	if c.Invalid {
		add("config.invalid", model.SeverityMedium,
			"OpenClaw config is invalid or not fully resolvable",
			"config_path="+filepath.Base(c.ConfigPath)+" invalid=true reason_hash_input="+c.InvalidReason)
	}

	channels := c.enabledChannels()
	agents := c.effectiveAgents()
	for _, agent := range agents {
		if len(channels) > 0 && sandboxOff(agent.Sandbox) {
			addAgent(agent, "sandbox.off", model.SeverityHigh,
				"OpenClaw agent sandbox is off",
				"agent="+agent.ID+" sandbox_mode="+firstNonEmpty(agent.Sandbox.Mode, "absent")+" reachable_channels="+strconv.Itoa(len(channels)))
		}
		if execUnrestricted(agent.Tools) {
			addAgent(agent, "exec.unrestricted", model.SeverityHigh,
				"OpenClaw exec tools are unrestricted",
				"agent="+agent.ID+" exec_security="+firstNonEmpty(agent.Tools.Exec.Security, "absent")+" deny_count="+strconv.Itoa(len(agent.Tools.Deny)))
		}
		if ptrBoolFalse(agent.Tools.Exec.ApplyPatch.WorkspaceOnly) || ptrBoolFalse(agent.Tools.FS.WorkspaceOnly) {
			addAgent(agent, "exec.patch_escape", model.SeverityHigh,
				"OpenClaw file or patch tools can escape the workspace",
				"agent="+agent.ID+" apply_patch_workspace_only="+boolPtrState(agent.Tools.Exec.ApplyPatch.WorkspaceOnly)+" fs_workspace_only="+boolPtrState(agent.Tools.FS.WorkspaceOnly))
		}
	}

	if gatewayExposed(c) {
		add("gateway.exposed", model.SeverityHigh,
			"OpenClaw gateway is exposed without password authentication",
			"bind="+normalizedBind(c.Gateway.Bind)+" auth_mode="+firstNonEmpty(c.Gateway.Auth.Mode, "absent")+" token_present="+boolStr(c.gatewayTokenPresent))
	}
	if strings.EqualFold(c.Gateway.Tailscale.Mode, "funnel") {
		sev := model.SeverityMedium
		if !strings.EqualFold(c.Gateway.Auth.Mode, "password") {
			sev = model.SeverityHigh
		}
		add("gateway.funnel", sev,
			"OpenClaw Tailscale funnel is enabled",
			"tailscale_mode=funnel auth_mode="+firstNonEmpty(c.Gateway.Auth.Mode, "absent")+" password_present="+boolStr(c.gatewayPasswordPresent))
	}
	if boolValue(c.Gateway.ControlUI.AllowInsecureAuth) || boolValue(c.Gateway.ControlUI.DangerouslyDisableDeviceAuth) {
		add("gateway.control_ui_insecure", model.SeverityHigh,
			"OpenClaw control UI weakens authentication",
			"allow_insecure_auth="+boolPtrState(c.Gateway.ControlUI.AllowInsecureAuth)+" disable_device_auth="+boolPtrState(c.Gateway.ControlUI.DangerouslyDisableDeviceAuth))
	}
	if nonLoopbackBind(c.Gateway.Bind) && !boolValue(c.Gateway.TLS.Enabled) {
		add("gateway.tls_off_lan", model.SeverityMedium,
			"OpenClaw gateway is reachable beyond loopback without TLS",
			"bind="+normalizedBind(c.Gateway.Bind)+" tls_enabled="+boolPtrState(c.Gateway.TLS.Enabled))
	}

	dmOpen := dmOpenChannels(c)
	if len(dmOpen) > 0 {
		add("channels.dm_open", model.SeverityHigh,
			"OpenClaw channel permits open direct messages",
			"channels="+strings.Join(dmOpen, ",")+" count="+strconv.Itoa(len(dmOpen)))
	}
	groupOpen := groupOpenChannels(c)
	if len(groupOpen) > 0 {
		add("channels.group_open", model.SeverityMedium,
			"OpenClaw channel permits open group context",
			"channels="+strings.Join(groupOpen, ",")+" count="+strconv.Itoa(len(groupOpen)))
	}
	if channelDangerousFlags(c) > 0 {
		add("channels.dangerous_flags", model.SeverityMedium,
			"OpenClaw channel dangerous flags are enabled",
			"dangerous_channel_flags="+strconv.Itoa(channelDangerousFlags(c)))
	}
	if channelConfigWrites(c) > 0 {
		add("channels.config_writes", model.SeverityHigh,
			"OpenClaw channel can rewrite config",
			"channels_with_config_writes="+strconv.Itoa(channelConfigWrites(c)))
	}

	if boolValue(c.Tools.Elevated.Enabled) {
		add("elevated.enabled", model.SeverityMedium,
			"OpenClaw elevated tools are enabled",
			"allow_from_count="+strconv.Itoa(allowListCount(c.Tools.Elevated.AllowFrom))+" allow_star="+boolStr(includesStar(c.Tools.Elevated.AllowFrom)))
	}
	if skillSourceCount(c.skillSources) > 0 {
		add("skills.unpinned_sources", model.SeverityMedium,
			"OpenClaw loads skills from unpinned source directories",
			"source_dirs="+strconv.Itoa(len(c.skillSources))+" skill_count="+strconv.Itoa(skillSourceCount(c.skillSources))+" sources="+skillSourceKinds(c.skillSources))
	}
	if boolValue(c.Skills.Load.AllowSymlinkTargets) || boolValue(c.Skills.Workshop.AllowSymlinkTargetWrites) {
		add("skills.symlink_targets", model.SeverityMedium,
			"OpenClaw skills allow symlink target access",
			"load_allow_symlink_targets="+boolPtrState(c.Skills.Load.AllowSymlinkTargets)+" workshop_allow_symlink_target_writes="+boolPtrState(c.Skills.Workshop.AllowSymlinkTargetWrites))
	}
	if boolValue(c.Skills.Install.AllowUploadedArchives) {
		add("skills.uploaded_archives", model.SeverityMedium,
			"OpenClaw allows uploaded skill archives",
			"allow_uploaded_archives=true")
	}
	for _, name := range mcpServerSortedNames(c) {
		srv := c.MCP.Servers[name]
		ref := "mcp." + safeSuffix(name)
		if mcpRemoteRunner(srv.Command) {
			add(ref+".remote_runner", model.SeverityMedium,
				"OpenClaw MCP server runs code resolved from a remote registry at start time",
				"server="+safeSuffix(name)+" command="+redact.Clean(baseCommand(srv.Command))+" transport="+firstNonEmpty(srv.Transport, "stdio"))
		}
		if host, ok := mcpNonLoopbackHost(srv.URL); ok {
			add(ref+".remote_url", model.SeverityMedium,
				"OpenClaw MCP server is reached over a non-loopback network URL",
				"server="+safeSuffix(name)+" host="+redact.Clean(host)+" transport="+firstNonEmpty(srv.Transport, "http"))
		}
		if mcpEnvLiteralCredential(srv) {
			add(ref+".env_credential", model.SeverityHigh,
				"OpenClaw MCP server embeds a literal credential in its environment or headers",
				"server="+safeSuffix(name)+" transport="+firstNonEmpty(srv.Transport, "stdio"))
		}
	}
	if pluginHookGrantCount(c) > 0 {
		add("plugins.hook_grants", model.SeverityHigh,
			"OpenClaw plugins have prompt or conversation hook grants",
			"plugins_with_hook_grants="+strconv.Itoa(pluginHookGrantCount(c)))
	}
	if c.Security.InstallPolicy != "" && !strings.EqualFold(c.Security.InstallPolicy, "operator-command") {
		add("plugins.install_policy", model.SeverityMedium,
			"OpenClaw plugin install policy is weakened",
			"install_policy="+redact.Clean(c.Security.InstallPolicy))
	}
	if strings.EqualFold(c.Discovery.MDNS.Mode, "full") || boolValue(c.Discovery.WideArea.Enabled) {
		add("discovery.broadcast", model.SeverityMedium,
			"OpenClaw discovery broadcasts expanded network metadata",
			"mdns_mode="+firstNonEmpty(c.Discovery.MDNS.Mode, "minimal")+" wide_area_enabled="+boolPtrState(c.Discovery.WideArea.Enabled))
	}
	if redactionWeakened(c.Logging.RedactSensitive) {
		add("logging.redaction_weakened", model.SeverityMedium,
			"OpenClaw logging redaction is weakened",
			"redact_sensitive="+redact.Clean(c.Logging.RedactSensitive))
	}
	if tmpLogFile(c.Logging.File) {
		add("logging.tmp_logfile", model.SeverityLow,
			"OpenClaw logs default to /tmp",
			"log_file="+firstNonEmpty(filepath.Dir(firstNonEmpty(c.Logging.File, "/tmp/openclaw/openclaw-YYYY-MM-DD.log")), "/tmp"))
	}
	if len(c.Agents.Defaults.Models) == 0 && len(c.credentialedProviders) > 1 {
		add("models.no_allowlist", model.SeverityMedium,
			"OpenClaw model surface has no defaults allowlist",
			"credentialed_providers="+strconv.Itoa(len(c.credentialedProviders))+" defaults_models=absent")
	}
	if c.literalCredentialCount > 0 {
		add("credentials.literal", model.SeverityHigh,
			"OpenClaw config embeds literal credentials",
			"literal_credential_fields="+strconv.Itoa(c.literalCredentialCount)+" sources_hash_input="+strings.Join(c.literalCredentialSources, ","))
	}
	if strings.EqualFold(firstNonEmpty(c.Session.DMScope, "main"), "main") && len(channels) > 1 {
		add("session.dm_shared", model.SeverityLow,
			"OpenClaw shares direct-message context across channels",
			"dm_scope="+firstNonEmpty(c.Session.DMScope, "main")+" enabled_channels="+strconv.Itoa(len(channels)))
	}
	if c.legacyEra {
		add("state.legacy_era", model.SeverityInfo,
			"OpenClaw legacy Clawdbot-era state is present",
			"legacy_state_present=true")
	}
	if strings.EqualFold(c.Update.Channel, "dev") {
		add("update.dev_channel", model.SeverityLow,
			"OpenClaw update channel tracks dev builds",
			"update_channel=dev")
	}

	return out
}

func (s *Source) permittedEdges(c clawConfig) []model.EdgeObservation {
	at := s.clock().UTC()
	var out []model.EdgeObservation
	channels := c.enabledChannels()
	for _, ch := range channels {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: c.AgentRef,
			ResourceKind: resourceChannel, ResourceRef: ch,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: ch, ObservedAt: at,
		})
	}
	for _, agent := range c.effectiveAgents() {
		for _, skill := range c.skillNamesForAgent(agent) {
			out = append(out, model.EdgeObservation{
				OriginKind: "agent", OriginRef: agent.Subject,
				ResourceKind: resourceSkill, ResourceRef: skill,
				Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
				ToolRef: "skill", ObservedAt: at,
			})
		}
		for _, modelRef := range c.modelRefsForAgent(agent) {
			provider, _, _ := strings.Cut(modelRef, "/")
			out = append(out, model.EdgeObservation{
				OriginKind: "agent", OriginRef: agent.Subject,
				ResourceKind: resourceModel, ResourceRef: modelRef,
				Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
				ToolRef: provider, ObservedAt: at,
			})
		}
		for _, server := range agent.MCPServers {
			out = append(out, model.EdgeObservation{
				OriginKind: "agent", OriginRef: agent.Subject,
				ResourceKind: resourceMCP, ResourceRef: server,
				Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
				ToolRef: "mcp", ObservedAt: at,
			})
		}
	}
	return out
}

func (s *Source) inventoryFinding(c clawConfig) model.FindingReport {
	agents := c.effectiveAgents()
	detail := strings.Join([]string{
		"version=" + firstNonEmpty(c.Wizard.LastRunVersion, c.Wizard.LastRunCommit, "unknown"),
		"sandbox_modes=" + sandboxModeSummary(agents),
		"channels=" + strconv.Itoa(len(c.enabledChannels())),
		"skills=" + strconv.Itoa(skillSourceCount(c.skillSources)),
		"mcp=" + strconv.Itoa(len(c.mcpServerNames())),
		"agents=" + strconv.Itoa(len(agents)),
		"agents_md=" + boolStr(c.agentsMD),
		"legacy_era=" + boolStr(c.legacyEra),
		"systemd_units=" + strconv.Itoa(len(s.discoverSystemdUnits())),
	}, " ")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectConfig,
		SubjectRef:  c.AgentRef,
		Title:       "OpenClaw install inventory (" + detail + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) coverageFinding(c clawConfig) model.FindingReport {
	at := s.clock().UTC()
	enabled := boolValue(c.Diagnostics.OTEL.Enabled)
	sev := model.SeverityMedium
	title := "OpenClaw diagnostics OTEL plugin is not enabled"
	detail := "diagnostics_otel_enabled=false endpoint_probe=false coverage=config_only"
	if enabled {
		sev = model.SeverityLow
		title = "OpenClaw diagnostics OTEL plugin is configured"
		detail = "diagnostics_otel_enabled=true endpoint_probe=false coverage=config_only"
	}
	return model.FindingReport{
		Kind:        "coverage",
		Severity:    sev,
		SubjectKind: subjectCoverage,
		SubjectRef:  c.AgentRef,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func finding(ref, subjectRef string, sev model.Severity, title, detail string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectPosture,
		SubjectRef:  subjectRef + "/" + ref,
		Title:       title,
		DetailHash:  redact.Hash(ref + ": " + detail),
		OccurredAt:  at,
	}
}

func findingSortKey(f model.FindingReport) string {
	ref := findingRef(f)
	if ref == "" {
		ref = f.Kind
	}
	return ref + "\x00" + f.SubjectRef
}

func findingRef(f model.FindingReport) string {
	if f.SubjectKind == subjectPosture {
		if idx := strings.LastIndexByte(f.SubjectRef, '/'); idx >= 0 && idx+1 < len(f.SubjectRef) {
			return f.SubjectRef[idx+1:]
		}
		return f.SubjectRef
	}
	switch f.Kind {
	case "coverage":
		return "coverage"
	case "inventory":
		return "inventory"
	default:
		return ""
	}
}

func dmOpenChannels(c clawConfig) []string {
	var out []string
	for name, ch := range c.Channels.Providers {
		if ch.Enabled != nil && !*ch.Enabled {
			continue
		}
		if strings.EqualFold(ch.DMPolicy, "open") || includesStar(ch.AllowFrom) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func groupOpenChannels(c clawConfig) []string {
	var out []string
	for name, ch := range c.Channels.Providers {
		if ch.Enabled != nil && !*ch.Enabled {
			continue
		}
		groupPolicy := firstNonEmpty(ch.GroupPolicy, c.Channels.Defaults.GroupPolicy, "allowlist")
		if strings.EqualFold(groupPolicy, "open") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func sandboxOff(s sandboxConfig) bool {
	mode := strings.ToLower(strings.TrimSpace(s.Mode))
	return mode == "" || mode == "off"
}

func execUnrestricted(t toolsConfig) bool {
	security := strings.ToLower(strings.TrimSpace(t.Exec.Security))
	if security != "" && security != "full" {
		return false
	}
	return !denyCoversExec(t.Deny)
}

func denyCoversExec(deny []string) bool {
	for _, item := range deny {
		item = strings.ToLower(strings.TrimSpace(item))
		switch item {
		case "*", "exec", "tools.exec", "group:exec", "group:runtime", "runtime", "shell":
			return true
		}
	}
	return false
}

func gatewayExposed(c clawConfig) bool {
	if !nonLoopbackBind(c.Gateway.Bind) {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(c.Gateway.Auth.Mode))
	return mode == "" || (mode == "token" && !c.gatewayTokenPresent)
}

func nonLoopbackBind(bind string) bool {
	switch normalizedBind(bind) {
	case "", "loopback", "localhost", "127.0.0.1", "::1":
		return false
	default:
		return true
	}
}

func normalizedBind(bind string) string {
	return strings.ToLower(strings.TrimSpace(bind))
}

func channelDangerousFlags(c clawConfig) int {
	count := 0
	for name, ch := range c.Channels.Providers {
		if ch.Enabled != nil && !*ch.Enabled {
			continue
		}
		switch name {
		case "matrix":
			if boolValue(ch.Network.DangerouslyAllowPrivateNetwork) {
				count++
			}
		case "googlechat":
			if boolValue(ch.DangerouslyAllowNameMatching) {
				count++
			}
		case "discord":
			if boolValue(ch.AllowBots) {
				count++
			}
		}
	}
	return count
}

func channelConfigWrites(c clawConfig) int {
	count := 0
	for _, ch := range c.Channels.Providers {
		if ch.Enabled != nil && !*ch.Enabled {
			continue
		}
		if boolValue(ch.ConfigWrites) {
			count++
		}
	}
	return count
}

func skillSourceCount(sources []skillSource) int {
	total := 0
	for _, src := range sources {
		total += src.Count
	}
	return total
}

func skillSourceKinds(sources []skillSource) string {
	seen := map[string]struct{}{}
	for _, src := range sources {
		seen[src.Source] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for src := range seen {
		out = append(out, src)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func pluginHookGrantCount(c clawConfig) int {
	count := 0
	for _, entry := range c.Plugins.Entries {
		if entry.Enabled != nil && !*entry.Enabled {
			continue
		}
		if boolValue(entry.Hooks.AllowPromptInjection) || boolValue(entry.Hooks.AllowConversationAccess) {
			count++
		}
	}
	return count
}

func redactionWeakened(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "tools", "all", "true", "on":
		return false
	case "off", "none", "false", "0", "disabled":
		return true
	default:
		return false
	}
}

func tmpLogFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		path = "/tmp/openclaw/openclaw-YYYY-MM-DD.log"
	}
	path = filepath.Clean(expandHome(path))
	return path == "/tmp" || strings.HasPrefix(path, "/tmp/")
}

func sandboxModeSummary(agents []effectiveAgent) string {
	seen := map[string]struct{}{}
	for _, agent := range agents {
		seen[firstNonEmpty(agent.Sandbox.Mode, "off")] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for mode := range seen {
		out = append(out, mode)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func boolPtrState(v *bool) string {
	if v == nil {
		return "absent"
	}
	if *v {
		return "true"
	}
	return "false"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
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

// mcpServerSortedNames returns the raw (map-key) names of every configured MCP
// server, sorted for deterministic emission. The raw name is needed to look the
// server up; safeSuffix is applied at the display/ref boundary.
func mcpServerSortedNames(c clawConfig) []string {
	out := make([]string, 0, len(c.MCP.Servers))
	for name, srv := range c.MCP.Servers {
		if strings.TrimSpace(name) == "" || !srv.configured() {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// baseCommand returns the trailing path element of a command (npx, uvx, node…),
// lowercased, for both posix and windows separators.
func baseCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if idx := strings.LastIndexAny(cmd, `/\`); idx >= 0 {
		cmd = cmd[idx+1:]
	}
	return strings.ToLower(cmd)
}

// mcpRemoteRunner reports whether an MCP server is launched via an ephemeral
// package runner (npx/uvx) — the server's code is resolved from a remote
// registry at start time, the same supply-chain surface a marketplace skill has.
func mcpRemoteRunner(cmd string) bool {
	switch baseCommand(cmd) {
	case "npx", "npx.cmd", "uvx", "uvx.cmd", "pnpm", "pnpx", "bunx":
		return true
	default:
		return false
	}
}

// mcpNonLoopbackHost reports the host of an MCP server URL when it resolves to a
// non-loopback address (a network-reachable MCP endpoint), and false for an
// empty/loopback/unparseable URL.
func mcpNonLoopbackHost(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "", "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return "", false
	default:
		if strings.HasPrefix(host, "127.") {
			return "", false
		}
		return host, true
	}
}

// mcpEnvLiteralCredential reports whether an MCP server's env OR headers bind a
// credential-shaped key (TOKEN/KEY/SECRET/PASSWORD/AUTH…) to a literal (non-${VAR})
// value — an inline secret in the config rather than an env/secret reference.
// Headers are the common auth surface for an HTTP/SSE MCP server (Authorization:
// Bearer …), so they must be inspected alongside the stdio env.
func mcpEnvLiteralCredential(srv mcpServer) bool {
	for _, m := range []map[string]any{srv.Env, srv.Headers} {
		for k, v := range m {
			if credentialShapedKey(k) && credentialLiteral(v) {
				return true
			}
		}
	}
	return false
}

// credentialShapedKey reports whether an env-var name looks like it holds a
// secret (case-insensitive substring match on the usual markers).
func credentialShapedKey(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "APIKEY", "API_KEY", "ACCESS_KEY", "PRIVATE_KEY", "CREDENTIAL", "AUTH"} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}
