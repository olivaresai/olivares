// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// govern.go turns the resolved Hermes config/state tree into observations:
// posture findings from the catalog, config-declared channel/skill/model/MCP
// edges, one install inventory finding, and one Langfuse coverage finding.
package hermes

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	subjectPosture  = "hermes.posture"
	subjectConfig   = "hermes.config"
	subjectCoverage = "hermes.coverage"

	resourceChannel   = "hermes.channel"
	resourceSkill     = "hermes.skill"
	resourceModel     = "hermes.model"
	resourceMCPServer = "hermes.mcp_server"

	CostType = "hermes"
)

func (s *Source) findings(c hermesConfig) []model.FindingReport {
	out := s.postureFindings(c)
	out = append(out, s.inventoryFinding(c), s.coverageFinding(c))
	return out
}

func (s *Source) postureFindings(c hermesConfig) []model.FindingReport {
	var out []model.FindingReport
	at := s.clock().UTC()
	add := func(ref string, sev model.Severity, title, detail string) {
		out = append(out, finding(ref, c.AgentRef, sev, title, detail, at))
	}

	if c.Invalid {
		add("config.invalid", model.SeverityMedium,
			"Hermes config is invalid or not fully resolvable",
			"config_path="+filepath.Base(c.ConfigPath)+" invalid=true reason_hash_input="+c.InvalidReason)
	}

	channels := c.enabledMessagingChannels()
	if len(channels) > 0 && terminalUnsandboxed(c.Terminal) {
		add("terminal.unsandboxed", model.SeverityHigh,
			"Hermes terminal backend is unsandboxed for enabled messaging channels",
			"backend="+firstNonEmpty(c.Terminal.Backend, "local")+" messaging_channels="+strconv.Itoa(len(channels))+" offenders="+strings.Join(channels, ","))
	}
	if strings.EqualFold(c.Approvals.Mode, "off") || c.hermesEnvTruthy("HERMES_YOLO_MODE") {
		add("approvals.off", model.SeverityHigh,
			"Hermes approvals are disabled",
			"approvals_mode="+firstNonEmpty(c.Approvals.Mode, "manual")+" yolo_env="+boolStr(c.hermesEnvTruthy("HERMES_YOLO_MODE")))
	}
	writeApproval := boolValue(c.Skills.WriteApproval)
	if !writeApproval {
		add("skills.self_write_ungoverned", model.SeverityHigh,
			"Hermes agent can edit executable skills without review",
			"skills_write_approval="+boolPtrState(c.Skills.WriteApproval)+" guard_agent_created="+boolPtrState(c.Skills.GuardAgentCreated))
	}
	if !writeApproval && !boolValue(c.Skills.GuardAgentCreated) {
		add("skills.guard_off", model.SeverityMedium,
			"Hermes agent-created skill guard is off",
			"skills_write_approval="+boolPtrState(c.Skills.WriteApproval)+" guard_agent_created="+boolPtrState(c.Skills.GuardAgentCreated))
	}
	if offenders := allowAllOffenders(c, c.enabledChannels()); len(offenders) > 0 {
		add("channels.allow_all", model.SeverityHigh,
			"Hermes channel authorization allows all users",
			"allow_all_offenders="+strings.Join(offenders, ",")+" count="+strconv.Itoa(len(offenders)))
	}
	if offenders := openDMOffenders(c); len(offenders) > 0 {
		add("channels.dm_open", model.SeverityMedium,
			"Hermes channel has documented open direct-message behavior",
			"dm_open_channels="+strings.Join(offenders, ",")+" count="+strconv.Itoa(len(offenders)))
	}
	if boolValue(c.Security.AllowPrivateURLs) || c.hermesEnvTruthy("HERMES_ALLOW_PRIVATE_URLS") {
		add("security.ssrf_opt_out", model.SeverityMedium,
			"Hermes private URL protection is disabled",
			"allow_private_urls="+boolPtrState(c.Security.AllowPrivateURLs)+" env_override="+boolStr(c.hermesEnvTruthy("HERMES_ALLOW_PRIVATE_URLS")))
	}
	if lazyInstallsEnabled(c) {
		add("security.lazy_installs", model.SeverityLow,
			"Hermes lazy installs are allowed",
			"allow_lazy_installs="+boolPtrState(c.Security.AllowLazyInstalls)+" disable_lazy_installs_env="+boolStr(c.hermesEnvTruthy("HERMES_DISABLE_LAZY_INSTALLS")))
	}
	if ptrBoolFalse(c.Security.RedactSecrets) || c.hermesEnvFalse("HERMES_REDACT_SECRETS") {
		add("security.redaction_off", model.SeverityMedium,
			"Hermes secret redaction is disabled",
			"redact_secrets="+boolPtrState(c.Security.RedactSecrets)+" redact_env_false="+boolStr(c.hermesEnvFalse("HERMES_REDACT_SECRETS")))
	}
	if dockerWeakened(c.Terminal) {
		add("sandbox.weakened", model.SeverityMedium,
			"Hermes Docker terminal backend is weakened",
			"run_as_host_user="+boolPtrState(c.Terminal.DockerRunAsHostUser)+" mount_cwd="+boolPtrState(c.Terminal.DockerMountCWDToWorkspace)+" forward_env_count="+strconv.Itoa(len(c.Terminal.DockerForwardEnv))+" volume_count="+strconv.Itoa(len(c.Terminal.DockerVolumes)))
	}
	if strings.TrimSpace(c.Terminal.SudoPassword) != "" {
		add("credentials.sudo_password", model.SeverityHigh,
			"Hermes config contains a plaintext sudo password field",
			"terminal_sudo_password_present=true")
	}
	if c.literalCredentialCount > 0 {
		add("credentials.api_key_literal", model.SeverityHigh,
			"Hermes config embeds literal model API keys",
			"literal_credential_fields="+strconv.Itoa(c.literalCredentialCount)+" sources_hash_input="+strings.Join(c.literalCredentialSources, ","))
	}
	if count := c.commandAllowlistCount(); count > 0 {
		add("command_allowlist.present", model.SeverityMedium,
			"Hermes command allowlist contains permanent approvals",
			"command_allowlist_count="+strconv.Itoa(count))
	}
	if apiServerExposed(c) {
		add("exposure.api_server", model.SeverityHigh,
			"Hermes API server is enabled without loopback-only keyed exposure",
			"api_server_enabled=true host="+firstNonEmpty(c.envValue("API_SERVER_HOST"), "127.0.0.1")+" key_present="+boolStr(apiServerKeyPresent(c)))
	}
	if strings.TrimSpace(c.Dashboard.BasicAuth.Password) != "" {
		add("exposure.dashboard_basic_password", model.SeverityMedium,
			"Hermes dashboard basic auth contains a plaintext password field",
			"dashboard_basic_password_present=true")
	}
	if c.hermesEnvTruthy("HERMES_ENABLE_PROJECT_PLUGINS") {
		add("plugins.project_enabled", model.SeverityMedium,
			"Hermes project plugins are enabled from environment",
			"project_plugins_enabled=true")
	}
	if !c.ManagedConfigPresent {
		add("managed_scope.absent", model.SeverityMedium,
			"Hermes managed scope is absent",
			"managed_config_present=false managed_dir="+filepath.Base(c.ManagedDir))
	}
	if c.stateFacts.PendingSkillCount > 0 {
		add("skills.pending_writes", model.SeverityInfo,
			"Hermes has pending skill writes staged for approval",
			"pending_skill_writes="+strconv.Itoa(c.stateFacts.PendingSkillCount))
	}
	if c.stateFacts.CommunityTapCount > 0 {
		add("skills.community_taps", model.SeverityMedium,
			"Hermes skills hub contains community or trusted taps",
			"community_tap_count="+strconv.Itoa(c.stateFacts.CommunityTapCount)+" tap_names="+strings.Join(c.stateFacts.CommunityTapNames, ","))
	}
	if !boolValue(c.Memory.WriteApproval) {
		add("memory.write_ungoverned", model.SeverityLow,
			"Hermes memory writes are unreviewed",
			"memory_write_approval="+boolPtrState(c.Memory.WriteApproval))
	}
	if c.stateFacts.MigrationOpenClaw {
		add("migration.openclaw_absorbed", model.SeverityInfo,
			"Hermes state includes an OpenClaw migration archive",
			"migration_openclaw_present=true")
	}
	return out
}

func (s *Source) permittedEdges(c hermesConfig) []model.EdgeObservation {
	at := s.clock().UTC()
	var out []model.EdgeObservation
	for _, ch := range c.enabledChannels() {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: c.AgentRef,
			ResourceKind: resourceChannel, ResourceRef: ch,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: ch, ObservedAt: at,
		})
	}
	for _, skill := range c.skillNames() {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: c.AgentRef,
			ResourceKind: resourceSkill, ResourceRef: skill,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: "skill", ObservedAt: at,
		})
	}
	for _, modelRef := range c.modelRefs() {
		provider, _, _ := strings.Cut(modelRef, "/")
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: c.AgentRef,
			ResourceKind: resourceModel, ResourceRef: modelRef,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: provider, ObservedAt: at,
		})
	}
	for _, server := range c.mcpServerNames() {
		out = append(out, model.EdgeObservation{
			OriginKind: "agent", OriginRef: c.AgentRef,
			ResourceKind: resourceMCPServer, ResourceRef: server,
			Mode: model.ModeUnknown, Source: model.SignalConfig, Confidence: model.ConfidenceAttributed,
			ToolRef: "mcp", ObservedAt: at,
		})
	}
	return out
}

func (s *Source) inventoryFinding(c hermesConfig) model.FindingReport {
	detail := strings.Join([]string{
		"version=" + firstNonEmpty(c.stateFacts.Version, "unknown"),
		"backend=" + firstNonEmpty(c.Terminal.Backend, "local"),
		"approvals=" + firstNonEmpty(c.Approvals.Mode, "manual"),
		"channels=" + strconv.Itoa(len(c.enabledChannels())),
		"skills=" + strconv.Itoa(c.stateFacts.SkillCount),
		"mcp=" + strconv.Itoa(len(c.mcpServerNames())),
		"pending=" + strconv.Itoa(c.stateFacts.PendingSkillCount),
		"managed_scope=" + boolStr(c.ManagedConfigPresent),
		"agents_md=" + boolStr(c.stateFacts.AgentsMD),
		"migration_openclaw=" + boolStr(c.stateFacts.MigrationOpenClaw),
		"memories=" + boolStr(c.stateFacts.MemoriesPresent),
		"pairing_files=" + strconv.Itoa(c.stateFacts.PairingFileCount),
		"pairing_approved=" + strconv.Itoa(c.stateFacts.PairingApprovedCount),
	}, " ")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectConfig,
		SubjectRef:  c.AgentRef,
		Title:       "Hermes install inventory (" + detail + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) coverageFinding(c hermesConfig) model.FindingReport {
	at := s.clock().UTC()
	enabled := c.hasEnvKey("HERMES_LANGFUSE_PUBLIC_KEY")
	sev := model.SeverityMedium
	title := "Hermes Langfuse plugin key is not present"
	detail := "langfuse_public_key_present=false local_trajectory_logs=true coverage=config_only native_otel=false"
	if enabled {
		sev = model.SeverityLow
		title = "Hermes Langfuse plugin key is present"
		detail = "langfuse_public_key_present=true local_trajectory_logs=true coverage=config_only native_otel=false"
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

func terminalUnsandboxed(t terminalConfig) bool {
	backend := strings.ToLower(strings.TrimSpace(t.Backend))
	return backend == "" || backend == "local"
}

func dockerWeakened(t terminalConfig) bool {
	if !strings.EqualFold(strings.TrimSpace(t.Backend), "docker") {
		return false
	}
	return boolValue(t.DockerRunAsHostUser) ||
		boolValue(t.DockerMountCWDToWorkspace) ||
		len(t.DockerForwardEnv) > 0 ||
		len(t.DockerVolumes) > 0 ||
		len(t.DockerExtraArgs) > 0
}

func lazyInstallsEnabled(c hermesConfig) bool {
	if c.hermesEnvTruthy("HERMES_DISABLE_LAZY_INSTALLS") {
		return false
	}
	return c.Security.AllowLazyInstalls == nil || boolValue(c.Security.AllowLazyInstalls)
}

func allowAllOffenders(c hermesConfig, channels []string) []string {
	seen := map[string]struct{}{}
	if c.envTruthy("GATEWAY_ALLOW_ALL_USERS") {
		seen["gateway"] = struct{}{}
	}
	for _, ch := range channels {
		key := strings.ToUpper(strings.ReplaceAll(ch, "-", "_")) + "_ALLOW_ALL_USERS"
		if c.envTruthy(key) {
			seen[ch] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func openDMOffenders(c hermesConfig) []string {
	seen := map[string]struct{}{}
	if _, ok := channelSet(c.enabledChannels())["weixin"]; ok {
		policy := strings.ToLower(firstNonEmpty(c.envValue("WEIXIN_DM_POLICY"), c.Platforms["weixin"].DMPolicy, c.Platforms["weixin"].UnauthorizedDMBehavior, "open"))
		if policy == "open" {
			seen["weixin"] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func channelSet(channels []string) map[string]struct{} {
	out := make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		out[ch] = struct{}{}
	}
	return out
}

func apiServerExposed(c hermesConfig) bool {
	if !c.envTruthy("API_SERVER_ENABLED") {
		return false
	}
	host := firstNonEmpty(c.envValue("API_SERVER_HOST"), "127.0.0.1")
	return nonLoopbackHost(host) || !apiServerKeyPresent(c)
}

func apiServerKeyPresent(c hermesConfig) bool {
	_, ok := c.envKeys["API_SERVER_KEY"]
	return ok
}

func nonLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return false
	default:
		return true
	}
}
