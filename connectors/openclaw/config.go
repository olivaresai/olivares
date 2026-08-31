// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openclaw

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/json5"
)

const (
	maxConfigBytes  = 1 << 20
	maxIncludeDepth = 10
	maxSkillEntries = 200
)

type clawConfig struct {
	Present       bool
	Invalid       bool
	InvalidReason string

	AgentRef   string
	StateDir   string
	ConfigPath string
	Profile    string
	Legacy     bool

	Gateway     gatewayConfig     `json:"gateway"`
	Discovery   discoveryConfig   `json:"discovery"`
	Channels    channelsConfig    `json:"channels"`
	Tools       toolsConfig       `json:"tools"`
	Agents      agentsConfig      `json:"agents"`
	Skills      skillsConfig      `json:"skills"`
	Plugins     pluginsConfig     `json:"plugins"`
	Security    securityConfig    `json:"security"`
	Logging     loggingConfig     `json:"logging"`
	Models      modelsConfig      `json:"models"`
	MCP         mcpConfig         `json:"mcp"`
	Session     sessionConfig     `json:"session"`
	Diagnostics diagnosticsConfig `json:"diagnostics"`
	Update      updateConfig      `json:"update"`
	Wizard      wizardConfig      `json:"wizard"`

	envKeys                  map[string]struct{}
	credentialedProviders    map[string]struct{}
	literalCredentialCount   int
	literalCredentialSources []string
	gatewayTokenPresent      bool
	gatewayPasswordPresent   bool
	skillSources             []skillSource
	agentsMD                 bool
	legacyEra                bool
}

type gatewayConfig struct {
	Bind      string            `json:"bind"`
	Auth      gatewayAuthConfig `json:"auth"`
	TLS       tlsConfig         `json:"tls"`
	Tailscale tailscaleConfig   `json:"tailscale"`
	ControlUI controlUIConfig   `json:"controlUi"`
}

type gatewayAuthConfig struct {
	Mode string `json:"mode"`
}

type tlsConfig struct {
	Enabled *bool `json:"enabled"`
}

type tailscaleConfig struct {
	Mode string `json:"mode"`
}

type controlUIConfig struct {
	Enabled                      *bool `json:"enabled"`
	AllowInsecureAuth            *bool `json:"allowInsecureAuth"`
	DangerouslyDisableDeviceAuth *bool `json:"dangerouslyDisableDeviceAuth"`
}

type discoveryConfig struct {
	MDNS     mdnsConfig     `json:"mdns"`
	WideArea wideAreaConfig `json:"wideArea"`
}

type mdnsConfig struct {
	Mode string `json:"mode"`
}

type wideAreaConfig struct {
	Enabled *bool `json:"enabled"`
}

type channelsConfig struct {
	Defaults  channelDefaults
	Providers map[string]channelConfig
}

type channelDefaults struct {
	GroupPolicy       string `json:"groupPolicy"`
	ContextVisibility string `json:"contextVisibility"`
}

type channelConfig struct {
	Enabled                      *bool          `json:"enabled"`
	DMPolicy                     string         `json:"dmPolicy"`
	AllowFrom                    any            `json:"allowFrom"`
	GroupPolicy                  string         `json:"groupPolicy"`
	GroupAllowFrom               any            `json:"groupAllowFrom"`
	ConfigWrites                 *bool          `json:"configWrites"`
	Network                      channelNetwork `json:"network"`
	DangerouslyAllowNameMatching *bool          `json:"dangerouslyAllowNameMatching"`
	AllowBots                    *bool          `json:"allowBots"`
}

type channelNetwork struct {
	DangerouslyAllowPrivateNetwork *bool `json:"dangerouslyAllowPrivateNetwork"`
}

func (c *channelsConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Providers = map[string]channelConfig{}
	for k, v := range raw {
		if k == "defaults" {
			if err := json.Unmarshal(v, &c.Defaults); err != nil {
				return err
			}
			continue
		}
		var ch channelConfig
		if err := json.Unmarshal(v, &ch); err != nil {
			return err
		}
		c.Providers[strings.ToLower(k)] = ch
	}
	return nil
}

type toolsConfig struct {
	Profile  string         `json:"profile"`
	Allow    stringList     `json:"allow"`
	Deny     stringList     `json:"deny"`
	Exec     execConfig     `json:"exec"`
	FS       fsConfig       `json:"fs"`
	Elevated elevatedConfig `json:"elevated"`
}

type execConfig struct {
	Security   string           `json:"security"`
	Ask        *bool            `json:"ask"`
	ApplyPatch applyPatchConfig `json:"applyPatch"`
}

type applyPatchConfig struct {
	WorkspaceOnly *bool `json:"workspaceOnly"`
}

type fsConfig struct {
	WorkspaceOnly *bool `json:"workspaceOnly"`
}

type elevatedConfig struct {
	Enabled   *bool `json:"enabled"`
	AllowFrom any   `json:"allowFrom"`
}

type sandboxConfig struct {
	Mode            string `json:"mode"`
	Scope           string `json:"scope"`
	WorkspaceAccess string `json:"workspaceAccess"`
}

type agentsConfig struct {
	Defaults agentConfig   `json:"defaults"`
	List     []agentConfig `json:"list"`
}

type agentConfig struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Workspace  string         `json:"workspace"`
	AgentDir   string         `json:"agentDir"`
	Model      modelConfig    `json:"model"`
	Models     map[string]any `json:"models"`
	Skills     stringList     `json:"skills"`
	Tools      toolsConfig    `json:"tools"`
	Sandbox    sandboxConfig  `json:"sandbox"`
	MCPServers stringList     `json:"mcpServers"`
	Default    *bool          `json:"default"`
}

// mcpConfig is OpenClaw's top-level MCP surface (openclaw.json "mcp"): a named
// set of MCP servers any agent can be routed to. Per-agent routing narrows the
// set via agents[].mcpServers (a name allowlist); no override inherits the full
// global set. This mirrors Hermes' mcp_servers map.
type mcpConfig struct {
	Servers map[string]mcpServer `json:"servers"`
}

// mcpServer is one configured MCP server. Command/Args (+ Transport "stdio")
// describe a spawned child process — an npx/uvx command resolves code from a
// remote registry at start time (the same supply-chain surface a skill has);
// URL describes an HTTP/SSE server. Env/Headers may carry credentials.
type mcpServer struct {
	Command   string         `json:"command"`
	Args      stringList     `json:"args"`
	Transport string         `json:"transport"`
	URL       string         `json:"url"`
	Env       map[string]any `json:"env"`
	Headers   map[string]any `json:"headers"`
}

type modelConfig struct {
	Primary   string     `json:"primary"`
	Fallbacks stringList `json:"fallbacks"`
}

func (m *modelConfig) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Primary = s
		return nil
	}
	type alias modelConfig
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*m = modelConfig(a)
	return nil
}

type skillsConfig struct {
	Entries  map[string]skillEntry `json:"entries"`
	Load     skillsLoadConfig      `json:"load"`
	Install  skillsInstallConfig   `json:"install"`
	Workshop skillsWorkshopConfig  `json:"workshop"`
}

type skillEntry struct {
	Enabled *bool `json:"enabled"`
}

type skillsLoadConfig struct {
	ExtraDirs           stringList `json:"extraDirs"`
	AllowSymlinkTargets *bool      `json:"allowSymlinkTargets"`
	Watch               *bool      `json:"watch"`
}

type skillsInstallConfig struct {
	AllowUploadedArchives *bool `json:"allowUploadedArchives"`
}

type skillsWorkshopConfig struct {
	AllowSymlinkTargetWrites *bool `json:"allowSymlinkTargetWrites"`
}

type pluginsConfig struct {
	Enabled *bool                  `json:"enabled"`
	Entries map[string]pluginEntry `json:"entries"`
}

type pluginEntry struct {
	Enabled *bool       `json:"enabled"`
	Hooks   pluginHooks `json:"hooks"`
}

type pluginHooks struct {
	AllowPromptInjection    *bool `json:"allowPromptInjection"`
	AllowConversationAccess *bool `json:"allowConversationAccess"`
}

type securityConfig struct {
	InstallPolicy string `json:"installPolicy"`
}

type loggingConfig struct {
	Level           string `json:"level"`
	File            string `json:"file"`
	RedactSensitive string `json:"redactSensitive"`
}

type modelsConfig struct {
	Providers map[string]modelProviderConfig `json:"providers"`
	Pricing   pricingConfig                  `json:"pricing"`
}

type modelProviderConfig struct {
	BaseURL string     `json:"baseUrl"`
	Models  stringList `json:"models"`
}

type pricingConfig struct {
	Enabled *bool `json:"enabled"`
}

type sessionConfig struct {
	DMScope string `json:"dmScope"`
}

type diagnosticsConfig struct {
	Enabled *bool      `json:"enabled"`
	OTEL    otelConfig `json:"otel"`
}

type otelConfig struct {
	Enabled *bool `json:"enabled"`
}

type updateConfig struct {
	Channel string `json:"channel"`
	Auto    struct {
		Enabled *bool `json:"enabled"`
	} `json:"auto"`
	CheckOnStart *bool `json:"checkOnStart"`
}

type wizardConfig struct {
	LastRunVersion string `json:"lastRunVersion"`
	LastRunCommit  string `json:"lastRunCommit"`
}

type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*l = nil
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*l = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			*l = nil
		} else {
			*l = []string{s}
		}
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err == nil {
		out := make([]string, 0, len(m))
		for k, v := range m {
			if b, ok := v.(bool); ok && !b {
				continue
			}
			out = append(out, k)
		}
		sort.Strings(out)
		*l = out
		return nil
	}
	return fmt.Errorf("expected string list")
}

type effectiveAgent struct {
	ID         string
	Subject    string
	Default    bool
	Workspace  string
	AgentDir   string
	Model      modelConfig
	Models     map[string]any
	Skills     []string
	Tools      toolsConfig
	Sandbox    sandboxConfig
	MCPServers []string
}

type skillSource struct {
	Source string
	Dir    string
	Count  int
	Names  []string
}

func (s *Source) readInstall(inst openclawInstall) clawConfig {
	c := clawConfig{
		Present:    true,
		AgentRef:   inst.agentRef,
		StateDir:   inst.stateDir,
		ConfigPath: inst.configPath,
		Profile:    inst.profile,
		Legacy:     inst.legacy,
	}

	raw, err := readJSON5Config(inst.configPath, 0, map[string]struct{}{})
	if errors.Is(err, os.ErrNotExist) {
		raw = map[string]any{}
	} else if err != nil {
		c.Invalid = true
		c.InvalidReason = err.Error()
		raw = map[string]any{}
	}

	c.literalCredentialCount, c.literalCredentialSources = countLiteralCredentials(raw)
	c.gatewayTokenPresent = credentialPresentAt(raw, "gateway", "auth", "token")
	c.gatewayPasswordPresent = credentialPresentAt(raw, "gateway", "auth", "password")
	c.envKeys = readStateEnvKeySet(inst.stateDir)
	c.credentialedProviders = credentialedProviders(raw, c.envKeys)

	substituted, substErr := substituteEnv(raw)
	if substErr != nil {
		c.Invalid = true
		c.InvalidReason = substErr.Error()
		substituted = raw
	}
	if data, err := json.Marshal(substituted); err == nil {
		if err := json.Unmarshal(data, &c); err != nil {
			c.Invalid = true
			c.InvalidReason = err.Error()
		}
	}

	c.AgentRef = inst.agentRef
	c.StateDir = inst.stateDir
	c.ConfigPath = inst.configPath
	c.Profile = inst.profile
	c.Legacy = inst.legacy
	c.Present = true
	c.agentsMD = c.detectAgentsMD()
	c.legacyEra = c.detectLegacyEra()
	c.skillSources = c.scanSkillSources()
	return c
}

func readJSON5Config(path string, depth int, stack map[string]struct{}) (map[string]any, error) {
	if depth > maxIncludeDepth {
		return nil, fmt.Errorf("include depth exceeds %d", maxIncludeDepth)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return nil, err
	}
	if _, ok := stack[abs]; ok {
		return nil, fmt.Errorf("include cycle at %s", filepath.Base(path))
	}
	stack[abs] = struct{}{}
	defer delete(stack, abs)

	f, err := os.Open(abs) //nolint:gosec // operator/local agent config path.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes))
	if err != nil {
		return nil, err
	}
	var current map[string]any
	if err := json5.Unmarshal(data, &current); err != nil {
		return nil, err
	}

	dir := filepath.Dir(abs)
	base := map[string]any{}
	for _, inc := range includeList(current["$include"]) {
		inc, err = substituteEnvString(inc)
		if err != nil {
			return nil, err
		}
		incPath, err := resolveIncludePath(dir, inc)
		if err != nil {
			return nil, err
		}
		m, err := readJSON5Config(incPath, depth+1, stack)
		if err != nil {
			return nil, err
		}
		deepMerge(base, m)
	}
	delete(current, "$include")
	deepMerge(base, current)
	return base, nil
}

func includeList(v any) []string {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) != "" {
			return []string{x}
		}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func resolveIncludePath(configDir, include string) (string, error) {
	if include == "" {
		return "", fmt.Errorf("empty include path")
	}
	path := include
	if !filepath.IsAbs(path) {
		path = filepath.Join(configDir, path)
	}
	clean := filepath.Clean(path)
	realConfigDir, err := filepath.EvalSymlinks(configDir)
	if err != nil {
		return "", err
	}
	realInclude, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realConfigDir, realInclude)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("include escapes config directory")
	}
	return realInclude, nil
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		sm, sok := v.(map[string]any)
		dm, dok := dst[k].(map[string]any)
		if sok && dok {
			deepMerge(dm, sm)
			continue
		}
		dst[k] = v
	}
}

var envSubstPattern = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

func substituteEnv(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			sub, err := substituteEnv(val)
			if err != nil {
				return nil, err
			}
			out[k] = sub
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			sub, err := substituteEnv(val)
			if err != nil {
				return nil, err
			}
			out[i] = sub
		}
		return out, nil
	case string:
		return substituteEnvString(x)
	default:
		return v, nil
	}
}

func substituteEnvString(s string) (string, error) {
	var missing string
	out := envSubstPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := envSubstPattern.FindStringSubmatch(match)[1]
		val, ok := os.LookupEnv(name)
		if !ok {
			missing = name
			return match
		}
		return val
	})
	if missing != "" {
		return "", fmt.Errorf("missing environment variable %s", missing)
	}
	return out, nil
}

func (c clawConfig) effectiveAgents() []effectiveAgent {
	defaultAgent := c.Agents.Defaults
	base := effectiveAgent{
		ID:         firstNonEmpty(defaultAgent.ID, "default"),
		Subject:    c.AgentRef,
		Default:    true,
		Workspace:  resolveConfigPath(c.ConfigPath, c.StateDir, defaultAgent.Workspace),
		AgentDir:   resolveConfigPath(c.ConfigPath, c.StateDir, defaultAgent.AgentDir),
		Model:      defaultAgent.Model,
		Models:     defaultAgent.Models,
		Skills:     append([]string(nil), defaultAgent.Skills...),
		Tools:      overlayTools(c.Tools, defaultAgent.Tools),
		Sandbox:    defaultAgent.Sandbox,
		MCPServers: c.resolveAgentMCP(defaultAgent.MCPServers),
	}
	if len(c.Agents.List) == 0 {
		return []effectiveAgent{base}
	}
	out := make([]effectiveAgent, 0, len(c.Agents.List))
	for i, agent := range c.Agents.List {
		eff := base
		if strings.TrimSpace(agent.ID) != "" {
			eff.ID = strings.TrimSpace(agent.ID)
		} else if strings.TrimSpace(agent.Name) != "" {
			eff.ID = strings.TrimSpace(agent.Name)
		} else {
			eff.ID = "agent-" + strconv.Itoa(i+1)
		}
		eff.Default = boolValue(agent.Default) || eff.ID == "default"
		if !eff.Default {
			eff.Subject = c.AgentRef + "/" + safeSuffix(eff.ID)
		} else {
			eff.Subject = c.AgentRef
		}
		if strings.TrimSpace(agent.Workspace) != "" {
			eff.Workspace = resolveConfigPath(c.ConfigPath, c.StateDir, agent.Workspace)
		}
		if strings.TrimSpace(agent.AgentDir) != "" {
			eff.AgentDir = resolveConfigPath(c.ConfigPath, c.StateDir, agent.AgentDir)
		}
		eff.Model = overlayModel(eff.Model, agent.Model)
		if len(agent.Models) > 0 {
			eff.Models = agent.Models
		}
		if agent.Skills != nil {
			eff.Skills = append([]string(nil), agent.Skills...)
		}
		eff.Tools = overlayTools(eff.Tools, agent.Tools)
		eff.Sandbox = overlaySandbox(eff.Sandbox, agent.Sandbox)
		if agent.MCPServers != nil {
			eff.MCPServers = c.resolveAgentMCP(agent.MCPServers)
		}
		out = append(out, eff)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

func overlayTools(base, over toolsConfig) toolsConfig {
	out := base
	if over.Profile != "" {
		out.Profile = over.Profile
	}
	if over.Allow != nil {
		out.Allow = append([]string(nil), over.Allow...)
	}
	if over.Deny != nil {
		out.Deny = append([]string(nil), over.Deny...)
	}
	if over.Exec.Security != "" {
		out.Exec.Security = over.Exec.Security
	}
	if over.Exec.Ask != nil {
		out.Exec.Ask = over.Exec.Ask
	}
	if over.Exec.ApplyPatch.WorkspaceOnly != nil {
		out.Exec.ApplyPatch.WorkspaceOnly = over.Exec.ApplyPatch.WorkspaceOnly
	}
	if over.FS.WorkspaceOnly != nil {
		out.FS.WorkspaceOnly = over.FS.WorkspaceOnly
	}
	if over.Elevated.Enabled != nil {
		out.Elevated.Enabled = over.Elevated.Enabled
	}
	if over.Elevated.AllowFrom != nil {
		out.Elevated.AllowFrom = over.Elevated.AllowFrom
	}
	return out
}

func overlaySandbox(base, over sandboxConfig) sandboxConfig {
	out := base
	if over.Mode != "" {
		out.Mode = over.Mode
	}
	if over.Scope != "" {
		out.Scope = over.Scope
	}
	if over.WorkspaceAccess != "" {
		out.WorkspaceAccess = over.WorkspaceAccess
	}
	return out
}

func overlayModel(base, over modelConfig) modelConfig {
	out := base
	if over.Primary != "" {
		out.Primary = over.Primary
	}
	if over.Fallbacks != nil {
		out.Fallbacks = append([]string(nil), over.Fallbacks...)
	}
	return out
}

func (c clawConfig) enabledChannels() []string {
	seen := map[string]struct{}{}
	for name, ch := range c.Channels.Providers {
		if ch.Enabled != nil && !*ch.Enabled {
			continue
		}
		seen[name] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (c clawConfig) skillNamesForAgent(agent effectiveAgent) []string {
	seen := map[string]struct{}{}
	if len(agent.Skills) > 0 {
		for _, name := range agent.Skills {
			if strings.TrimSpace(name) != "" {
				seen[strings.TrimSpace(name)] = struct{}{}
			}
		}
	} else {
		for _, src := range c.skillSources {
			for _, name := range src.Names {
				if strings.TrimSpace(name) != "" {
					seen[name] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (c clawConfig) modelRefsForAgent(agent effectiveAgent) []string {
	seen := map[string]struct{}{}
	add := func(modelID string) {
		modelID = strings.TrimSpace(strings.ToLower(modelID))
		if modelID == "" {
			return
		}
		if !strings.Contains(modelID, "/") {
			modelID = "anthropic/" + modelID
		}
		seen[modelID] = struct{}{}
	}
	add(agent.Model.Primary)
	for _, fallback := range agent.Model.Fallbacks {
		add(fallback)
	}
	out := make([]string, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// configured reports whether an MCP server entry carries any transport detail
// (an empty stanza is a placeholder, not a reachable server).
func (m mcpServer) configured() bool {
	return strings.TrimSpace(m.Command) != "" || strings.TrimSpace(m.URL) != "" ||
		len(m.Args) > 0 || len(m.Env) > 0 || len(m.Headers) > 0
}

// mcpServerNames returns the sorted, sanitized names of every configured
// top-level MCP server (the global set an agent inherits absent an override).
func (c clawConfig) mcpServerNames() []string {
	seen := map[string]struct{}{}
	for name, srv := range c.MCP.Servers {
		if strings.TrimSpace(name) == "" || !srv.configured() {
			continue
		}
		seen[safeSuffix(name)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// resolveAgentMCP resolves the MCP servers an agent can reach. A nil override
// inherits the full configured global set; a non-nil list is a per-agent
// allowlist by raw server name — entries that name no configured global server
// are dropped (an agent cannot reach a server that is not defined).
func (c clawConfig) resolveAgentMCP(override []string) []string {
	if override == nil {
		return c.mcpServerNames()
	}
	seen := map[string]struct{}{}
	for _, name := range override {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if srv, ok := c.MCP.Servers[name]; ok && srv.configured() {
			seen[safeSuffix(name)] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (c clawConfig) scanSkillSources() []skillSource {
	var dirs []skillSource
	add := func(source, dir string) {
		dir = resolveConfigPath(c.ConfigPath, c.StateDir, dir)
		if dir == "" {
			return
		}
		count, names := countSkillDir(dir)
		if count == 0 {
			return
		}
		dirs = append(dirs, skillSource{Source: source, Dir: dir, Count: count, Names: names})
	}
	for _, agent := range c.effectiveAgents() {
		if agent.Workspace != "" {
			add("workspace", filepath.Join(agent.Workspace, "skills"))
			add("workspace", filepath.Join(agent.Workspace, ".agents", "skills"))
		}
		if agent.AgentDir != "" {
			add("workspace", filepath.Join(agent.AgentDir, "skills"))
		}
		if agent.ID != "" {
			add("workspace", filepath.Join(c.StateDir, "workspace-"+safeSuffix(agent.ID), "skills"))
		}
	}
	if c.StateDir != "" {
		add("workspace", filepath.Join(c.StateDir, "workspace", "skills"))
	}
	if home := homeDir(); home != "" {
		add("home-agents", filepath.Join(home, ".agents", "skills"))
	}
	for _, dir := range c.Skills.Load.ExtraDirs {
		add("extraDirs", dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].Source == dirs[j].Source {
			return dirs[i].Dir < dirs[j].Dir
		}
		return dirs[i].Source < dirs[j].Source
	})
	return dirs
}

func countSkillDir(dir string) (int, []string) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return 0, nil
	}
	if fileExists(filepath.Join(dir, "SKILL.md")) {
		return 1, []string{filepath.Base(dir)}
	}
	count := 0
	var names []string
	entries := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > maxSkillEntries {
			return filepath.SkipAll
		}
		if path == dir {
			return nil
		}
		if d.IsDir() {
			if filepath.Dir(path) != dir {
				return filepath.SkipDir
			}
			if fileExists(filepath.Join(path, "SKILL.md")) {
				count++
				names = append(names, d.Name())
			}
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(names)
	return count, names
}

func (c clawConfig) detectAgentsMD() bool {
	check := func(path string) bool { return fileExists(filepath.Join(path, "AGENTS.md")) }
	if c.StateDir != "" && check(filepath.Join(c.StateDir, "workspace")) {
		return true
	}
	if c.ConfigPath != "" && check(filepath.Dir(c.ConfigPath)) {
		return true
	}
	for _, agent := range c.effectiveAgents() {
		if agent.Workspace != "" && check(agent.Workspace) {
			return true
		}
		if agent.AgentDir != "" && check(agent.AgentDir) {
			return true
		}
	}
	return false
}

func (c clawConfig) detectLegacyEra() bool {
	if c.Legacy {
		return true
	}
	if c.StateDir != "" && fileExists(filepath.Join(c.StateDir, legacyConfigFileName)) {
		return true
	}
	if home := homeDir(); home != "" && dirExists(filepath.Join(home, legacyStateDirName)) {
		return true
	}
	return false
}

func resolveConfigPath(configPath, stateDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = expandHome(path)
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if configPath != "" {
		return filepath.Clean(filepath.Join(filepath.Dir(configPath), path))
	}
	if stateDir != "" {
		return filepath.Clean(filepath.Join(stateDir, path))
	}
	return filepath.Clean(path)
}

func readStateEnvKeySet(stateDir string) map[string]struct{} {
	keys := map[string]struct{}{}
	if stateDir != "" {
		addDotenvKeys(keys, filepath.Join(stateDir, ".env"))
	}
	return keys
}

func addDotenvKeys(keys map[string]struct{}, path string) {
	data, err := os.ReadFile(path) //nolint:gosec // local dotenv key-name scan only.
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if key != "" {
			keys[key] = struct{}{}
		}
	}
}

func credentialedProviders(root map[string]any, keys map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	if providers, ok := getMap(root, "models", "providers"); ok {
		for provider, raw := range providers {
			if m, ok := raw.(map[string]any); ok {
				if v, ok := m["apiKey"]; ok && credentialPresent(v) {
					out[strings.ToLower(provider)] = struct{}{}
				}
			}
		}
	}
	for key := range keys {
		if provider := providerForEnvKey(key); provider != "" {
			out[provider] = struct{}{}
		}
	}
	return out
}

func providerForEnvKey(key string) string {
	switch key {
	case "ANTHROPIC_API_KEY":
		return "anthropic"
	case "OPENAI_API_KEY":
		return "openai"
	case "OPENROUTER_API_KEY":
		return "openrouter"
	case "GOOGLE_API_KEY", "GEMINI_API_KEY":
		return "google"
	case "MISTRAL_API_KEY":
		return "mistral"
	case "XAI_API_KEY":
		return "xai"
	case "DEEPSEEK_API_KEY":
		return "deepseek"
	case "GROQ_API_KEY":
		return "groq"
	default:
		return ""
	}
}

func countLiteralCredentials(root map[string]any) (int, []string) {
	var sources []string
	add := func(ref string, v any) {
		if credentialLiteral(v) {
			sources = append(sources, ref)
		}
	}
	if auth, ok := getMap(root, "gateway", "auth"); ok {
		add("gateway.auth.token", auth["token"])
		add("gateway.auth.password", auth["password"])
	}
	if channels, ok := getMap(root, "channels"); ok {
		for name, raw := range channels {
			if name == "defaults" {
				continue
			}
			if m, ok := raw.(map[string]any); ok {
				for _, field := range []string{"botToken", "token", "appToken", "accessToken", "password"} {
					add("channels."+name+"."+field, m[field])
				}
			}
		}
	}
	if providers, ok := getMap(root, "models", "providers"); ok {
		for name, raw := range providers {
			if m, ok := raw.(map[string]any); ok {
				add("models.providers."+name+".apiKey", m["apiKey"])
			}
		}
	}
	if entries, ok := getMap(root, "skills", "entries"); ok {
		for name, raw := range entries {
			if m, ok := raw.(map[string]any); ok {
				add("skills.entries."+name+".apiKey", m["apiKey"])
			}
		}
	}
	if entries, ok := getMap(root, "plugins", "entries"); ok {
		for name, raw := range entries {
			if m, ok := raw.(map[string]any); ok {
				add("plugins.entries."+name+".apiKey", m["apiKey"])
			}
		}
	}
	sort.Strings(sources)
	return len(sources), sources
}

func credentialLiteral(v any) bool {
	s, ok := v.(string)
	if ok {
		s = strings.TrimSpace(s)
		return s != "" && !strings.Contains(s, "${")
	}
	return false
}

func credentialPresent(v any) bool {
	if credentialLiteral(v) {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	if m, ok := v.(map[string]any); ok {
		source, _ := m["source"].(string)
		switch strings.ToLower(source) {
		case "env", "file", "exec":
			return true
		}
	}
	return false
}

func credentialPresentAt(root map[string]any, path ...string) bool {
	if len(path) == 0 {
		return false
	}
	cur := any(root)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur = m[key]
	}
	return credentialPresent(cur)
}

func getMap(root map[string]any, path ...string) (map[string]any, bool) {
	var cur any = root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = m[key]
	}
	m, ok := cur.(map[string]any)
	return m, ok
}

func boolValue(p *bool) bool {
	return p != nil && *p
}

func ptrBoolFalse(p *bool) bool {
	return p != nil && !*p
}

func allowListCount(v any) int {
	switch x := v.(type) {
	case []any:
		return len(x)
	case []string:
		return len(x)
	case string:
		if strings.TrimSpace(x) == "" {
			return 0
		}
		return 1
	default:
		return 0
	}
}

func includesStar(v any) bool {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) == "*" {
				return true
			}
		}
	case []string:
		for _, s := range x {
			if strings.TrimSpace(s) == "*" {
				return true
			}
		}
	case string:
		return strings.TrimSpace(x) == "*"
	}
	return false
}
