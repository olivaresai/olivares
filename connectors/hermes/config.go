// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hermes

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxConfigBytes = 1 << 20
)

type hermesConfig struct {
	Present       bool
	Invalid       bool
	InvalidReason string

	AgentRef             string
	StateDir             string
	ConfigPath           string
	ManagedDir           string
	Profile              string
	UserConfigPresent    bool
	ManagedConfigPresent bool

	Model             modelConfig               `yaml:"model" json:"model"`
	FallbackProviders stringList                `yaml:"fallback_providers" json:"fallback_providers"`
	CustomProviders   map[string]customProvider `yaml:"custom_providers" json:"custom_providers"`
	ProviderRouting   providerRoutingConfig     `yaml:"provider_routing" json:"provider_routing"`
	Terminal          terminalConfig            `yaml:"terminal" json:"terminal"`
	Security          securityConfig            `yaml:"security" json:"security"`
	Approvals         approvalsConfig           `yaml:"approvals" json:"approvals"`
	CommandAllowlist  stringList                `yaml:"command_allowlist" json:"command_allowlist"`
	Skills            skillsConfig              `yaml:"skills" json:"skills"`
	Memory            memoryConfig              `yaml:"memory" json:"memory"`
	Platforms         map[string]platformConfig `yaml:"platforms" json:"platforms"`
	MCPServers        map[string]mcpServer      `yaml:"mcp_servers" json:"mcp_servers"`
	Dashboard         dashboardConfig           `yaml:"dashboard" json:"dashboard"`
	CodeExecution     codeExecutionConfig       `yaml:"code_execution" json:"code_execution"`

	envKeys    map[string]struct{}
	envValues  map[string]string
	stateFacts hermesStateFacts

	literalCredentialCount   int
	literalCredentialSources []string
}

type modelConfig struct {
	Default       string `yaml:"default" json:"default"`
	Model         string `yaml:"model" json:"model"`
	Provider      string `yaml:"provider" json:"provider"`
	BaseURL       string `yaml:"base_url" json:"base_url"`
	APIKey        string `yaml:"api_key" json:"api_key"`
	AuthMode      string `yaml:"auth_mode" json:"auth_mode"`
	ContextLength int    `yaml:"context_length" json:"context_length"`
	MaxTokens     int    `yaml:"max_tokens" json:"max_tokens"`
}

type customProvider struct {
	BaseURL string     `yaml:"base_url" json:"base_url"`
	KeyEnv  string     `yaml:"key_env" json:"key_env"`
	APIKey  string     `yaml:"api_key" json:"api_key"`
	Model   string     `yaml:"model" json:"model"`
	Models  stringList `yaml:"models" json:"models"`
	Default string     `yaml:"default" json:"default"`
}

type providerRoutingConfig struct {
	Only   stringList `yaml:"only" json:"only"`
	Ignore stringList `yaml:"ignore" json:"ignore"`
	Order  stringList `yaml:"order" json:"order"`
}

type terminalConfig struct {
	Backend                   string     `yaml:"backend" json:"backend"`
	DockerRunAsHostUser       *bool      `yaml:"docker_run_as_host_user" json:"docker_run_as_host_user"`
	DockerMountCWDToWorkspace *bool      `yaml:"docker_mount_cwd_to_workspace" json:"docker_mount_cwd_to_workspace"`
	DockerForwardEnv          stringList `yaml:"docker_forward_env" json:"docker_forward_env"`
	DockerVolumes             stringList `yaml:"docker_volumes" json:"docker_volumes"`
	DockerExtraArgs           stringList `yaml:"docker_extra_args" json:"docker_extra_args"`
	SudoPassword              string     `yaml:"sudo_password" json:"sudo_password"`
}

type securityConfig struct {
	RedactSecrets     *bool `yaml:"redact_secrets" json:"redact_secrets"`
	AllowPrivateURLs  *bool `yaml:"allow_private_urls" json:"allow_private_urls"`
	AllowLazyInstalls *bool `yaml:"allow_lazy_installs" json:"allow_lazy_installs"`
}

type approvalsConfig struct {
	Mode             string     `yaml:"mode" json:"mode"`
	CronMode         string     `yaml:"cron_mode" json:"cron_mode"`
	CommandAllowlist stringList `yaml:"command_allowlist" json:"command_allowlist"`
}

type skillsConfig struct {
	WriteApproval     *bool                     `yaml:"write_approval" json:"write_approval"`
	GuardAgentCreated *bool                     `yaml:"guard_agent_created" json:"guard_agent_created"`
	ExternalDirs      stringList                `yaml:"external_dirs" json:"external_dirs"`
	Config            map[string]map[string]any `yaml:"config" json:"config"`
}

type memoryConfig struct {
	MemoryEnabled *bool `yaml:"memory_enabled" json:"memory_enabled"`
	WriteApproval *bool `yaml:"write_approval" json:"write_approval"`
}

type platformConfig struct {
	Enabled                *bool  `yaml:"enabled" json:"enabled"`
	UnauthorizedDMBehavior string `yaml:"unauthorized_dm_behavior" json:"unauthorized_dm_behavior"`
	DMPolicy               string `yaml:"dm_policy" json:"dm_policy"`
}

type mcpServer struct {
	Command string         `yaml:"command" json:"command"`
	Args    stringList     `yaml:"args" json:"args"`
	Env     map[string]any `yaml:"env" json:"env"`
	URL     string         `yaml:"url" json:"url"`
	Headers map[string]any `yaml:"headers" json:"headers"`
}

type dashboardConfig struct {
	BasicAuth basicAuthConfig `yaml:"basic_auth" json:"basic_auth"`
	PublicURL string          `yaml:"public_url" json:"public_url"`
}

type basicAuthConfig struct {
	Username     string `yaml:"username" json:"username"`
	PasswordHash string `yaml:"password_hash" json:"password_hash"`
	Password     string `yaml:"password" json:"password"`
}

type codeExecutionConfig struct {
	Mode         string `yaml:"mode" json:"mode"`
	Timeout      int    `yaml:"timeout" json:"timeout"`
	MaxToolCalls int    `yaml:"max_tool_calls" json:"max_tool_calls"`
}

type stringList []string

func (l *stringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		out := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			if strings.TrimSpace(item.Value) != "" {
				out = append(out, strings.TrimSpace(item.Value))
			}
		}
		*l = out
		return nil
	case yaml.ScalarNode:
		if strings.TrimSpace(value.Value) == "" {
			*l = nil
		} else {
			*l = []string{strings.TrimSpace(value.Value)}
		}
		return nil
	case yaml.MappingNode:
		out := make([]string, 0, len(value.Content)/2)
		for i := 0; i+1 < len(value.Content); i += 2 {
			key := strings.TrimSpace(value.Content[i].Value)
			if key == "" {
				continue
			}
			val := strings.ToLower(strings.TrimSpace(value.Content[i+1].Value))
			if val == "false" || val == "0" || val == "off" {
				continue
			}
			out = append(out, key)
		}
		sort.Strings(out)
		*l = out
		return nil
	case 0:
		return nil
	default:
		return fmt.Errorf("expected string list")
	}
}

func (s *Source) readInstall(inst hermesInstall) hermesConfig {
	c := hermesConfig{
		Present:    true,
		AgentRef:   inst.agentRef,
		StateDir:   inst.stateDir,
		ConfigPath: inst.configPath,
		ManagedDir: inst.managedDir,
		Profile:    inst.profile,
		Platforms:  map[string]platformConfig{},
		MCPServers: map[string]mcpServer{},
		envKeys:    map[string]struct{}{},
		envValues:  map[string]string{},
	}

	userRaw, userPresent, userErr := readYAMLMap(inst.configPath)
	if userErr != nil && !errors.Is(userErr, os.ErrNotExist) {
		c.Invalid = true
		c.InvalidReason = userErr.Error()
	}
	c.UserConfigPresent = userPresent
	if userRaw == nil {
		userRaw = map[string]any{}
	}

	managedConfigPath := filepath.Join(inst.managedDir, configFileName)
	managedRaw, managedPresent, managedErr := readYAMLMap(managedConfigPath)
	if managedErr != nil && !errors.Is(managedErr, os.ErrNotExist) {
		c.Invalid = true
		c.InvalidReason = managedErr.Error()
	}
	c.ManagedConfigPresent = managedPresent
	if managedRaw == nil {
		managedRaw = map[string]any{}
	}

	merged := map[string]any{}
	deepMerge(merged, userRaw)
	deepMerge(merged, managedRaw)

	c.literalCredentialCount, c.literalCredentialSources = countLiteralCredentials(merged)
	if data, err := yaml.Marshal(merged); err == nil {
		if err := yaml.Unmarshal(data, &c); err != nil {
			c.Invalid = true
			c.InvalidReason = err.Error()
		}
	} else {
		c.Invalid = true
		c.InvalidReason = err.Error()
	}

	c.AgentRef = inst.agentRef
	c.StateDir = inst.stateDir
	c.ConfigPath = inst.configPath
	c.ManagedDir = inst.managedDir
	c.Profile = inst.profile
	c.Present = true
	c.UserConfigPresent = userPresent
	c.ManagedConfigPresent = managedPresent
	c.envKeys, c.envValues = readEnvEvidence(inst.stateDir, inst.managedDir)
	c.stateFacts = scanState(inst.stateDir, inst.configPath)
	return c
}

func readYAMLMap(path string) (map[string]any, bool, error) {
	f, err := os.Open(path) //nolint:gosec // local Hermes config path.
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, os.ErrNotExist
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes))
	if err != nil {
		return nil, true, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, true, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, true, nil
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		sm, sok := asStringMap(v)
		dm, dok := asStringMap(dst[k])
		if sok && dok {
			deepMerge(dm, sm)
			dst[k] = dm
			continue
		}
		dst[k] = v
	}
}

func asStringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			ks, ok := k.(string)
			if !ok {
				return nil, false
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}

func readEnvEvidence(stateDir, managedDir string) (map[string]struct{}, map[string]string) {
	keys := map[string]struct{}{}
	values := map[string]string{}
	if stateDir != "" {
		addDotenvEvidence(keys, values, filepath.Join(stateDir, ".env"))
	}
	if managedDir != "" {
		addDotenvEvidence(keys, values, filepath.Join(managedDir, ".env"))
	}
	return keys, values
}

func addDotenvEvidence(keys map[string]struct{}, values map[string]string, path string) {
	data, err := os.ReadFile(path) //nolint:gosec // local dotenv key-name scan only.
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if key != "" {
			keys[key] = struct{}{}
			if dotenvValueAllowed(key) {
				values[key] = normalizeDotenvValue(line[idx+1:])
			}
		}
	}
}

func dotenvValueAllowed(key string) bool {
	switch key {
	case "API_SERVER_ENABLED",
		"API_SERVER_HOST",
		"GATEWAY_ALLOW_ALL_USERS",
		"WEIXIN_DM_POLICY",
		"HERMES_YOLO_MODE",
		"HERMES_ALLOW_PRIVATE_URLS",
		"HERMES_REDACT_SECRETS",
		"HERMES_DISABLE_LAZY_INSTALLS",
		"HERMES_ENABLE_PROJECT_PLUGINS",
		"HERMES_MODEL",
		"HERMES_INFERENCE_MODEL":
		return true
	default:
		return strings.HasSuffix(key, "_ALLOW_ALL_USERS")
	}
}

func normalizeDotenvValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
	}
	return strings.TrimSpace(v)
}

func countLiteralCredentials(root map[string]any) (int, []string) {
	var sources []string
	if literalSecretAt(root, "model", "api_key") {
		sources = append(sources, "model.api_key")
	}
	if cps, ok := asStringMap(root["custom_providers"]); ok {
		for name, raw := range cps {
			if cp, ok := asStringMap(raw); ok && literalSecretValue(cp["api_key"]) {
				sources = append(sources, "custom_providers."+safeSuffix(name)+".api_key")
			}
		}
	}
	sort.Strings(sources)
	return len(sources), sources
}

func literalSecretAt(root map[string]any, path ...string) bool {
	var cur any = root
	for _, part := range path {
		m, ok := asStringMap(cur)
		if !ok {
			return false
		}
		cur = m[part]
	}
	return literalSecretValue(cur)
}

func literalSecretValue(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	return !(strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}"))
}

func (c hermesConfig) modelName() string {
	return firstNonEmpty(c.hermesEnvValue("HERMES_MODEL"), c.hermesEnvValue("HERMES_INFERENCE_MODEL"), c.Model.Default, c.Model.Model)
}

func (c hermesConfig) providerName() string {
	return firstNonEmpty(c.Model.Provider, "anthropic")
}

func (c hermesConfig) commandAllowlistCount() int {
	seen := map[string]struct{}{}
	for _, item := range c.CommandAllowlist {
		if strings.TrimSpace(item) != "" {
			seen[item] = struct{}{}
		}
	}
	for _, item := range c.Approvals.CommandAllowlist {
		if strings.TrimSpace(item) != "" {
			seen[item] = struct{}{}
		}
	}
	return len(seen)
}

func (c hermesConfig) modelRefs() []string {
	seen := map[string]struct{}{}
	add := func(provider, modelRef string) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		modelRef = strings.TrimSpace(modelRef)
		if modelRef == "" {
			return
		}
		if p, m, ok := strings.Cut(modelRef, "/"); ok {
			provider = strings.ToLower(strings.TrimSpace(p))
			modelRef = strings.TrimSpace(m)
		}
		if provider == "" {
			provider = c.providerName()
		}
		if provider != "" && modelRef != "" {
			seen[provider+"/"+modelRef] = struct{}{}
		}
	}
	baseModel := c.modelName()
	add(c.Model.Provider, baseModel)
	for _, provider := range c.FallbackProviders {
		if strings.TrimSpace(provider) != "" {
			add(provider, baseModel)
		}
	}
	for name, cp := range c.CustomProviders {
		if strings.TrimSpace(cp.Model) != "" {
			add(name, cp.Model)
		}
		for _, modelRef := range cp.Models {
			add(name, modelRef)
		}
		if strings.TrimSpace(cp.Default) != "" {
			add(name, cp.Default)
		}
		if strings.TrimSpace(cp.Model) == "" && len(cp.Models) == 0 && strings.TrimSpace(cp.Default) == "" {
			add(name, baseModel)
		}
	}
	out := make([]string, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

func (c hermesConfig) skillNames() []string {
	seen := map[string]struct{}{}
	for _, name := range c.stateFacts.SkillNames {
		if strings.TrimSpace(name) != "" {
			seen[name] = struct{}{}
		}
	}
	for name := range c.Skills.Config {
		if strings.TrimSpace(name) != "" {
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

func (c hermesConfig) mcpServerNames() []string {
	seen := map[string]struct{}{}
	for name, srv := range c.MCPServers {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if strings.TrimSpace(srv.Command) == "" && strings.TrimSpace(srv.URL) == "" && len(srv.Args) == 0 && len(srv.Env) == 0 && len(srv.Headers) == 0 {
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

func (c hermesConfig) enabledChannels() []string {
	seen := map[string]struct{}{}
	for name, cfg := range c.Platforms {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if cfg.Enabled != nil && !*cfg.Enabled {
			continue
		}
		seen[name] = struct{}{}
	}
	for _, ch := range documentedChannelKeys(c.envKeys) {
		seen[ch] = struct{}{}
	}
	if c.envTruthy("API_SERVER_ENABLED") {
		seen["api_server"] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for ch := range seen {
		out = append(out, ch)
	}
	sort.Strings(out)
	return out
}

func (c hermesConfig) enabledMessagingChannels() []string {
	var out []string
	for _, ch := range c.enabledChannels() {
		switch ch {
		case "api_server", "open_webui", "webhooks", "cli", "tui":
			continue
		default:
			out = append(out, ch)
		}
	}
	return out
}

func documentedChannelKeys(keys map[string]struct{}) []string {
	seen := map[string]struct{}{}
	keyPresent := func(key string) bool {
		_, ok := keys[key]
		return ok
	}
	prefixPresent := func(prefix string) bool {
		for key := range keys {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
		return false
	}
	if keyPresent("TELEGRAM_BOT_TOKEN") {
		seen["telegram"] = struct{}{}
	}
	if keyPresent("DISCORD_BOT_TOKEN") {
		seen["discord"] = struct{}{}
	}
	if keyPresent("SLACK_BOT_TOKEN") {
		seen["slack"] = struct{}{}
	}
	if prefixPresent("WHATSAPP_") {
		seen["whatsapp"] = struct{}{}
	}
	if prefixPresent("SIGNAL_") {
		seen["signal"] = struct{}{}
	}
	if keyPresent("EMAIL_ADDRESS") {
		seen["email"] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for ch := range seen {
		out = append(out, ch)
	}
	sort.Strings(out)
	return out
}

func (c hermesConfig) hasEnvKey(key string) bool {
	if _, ok := c.envKeys[key]; ok {
		return true
	}
	if !strings.HasPrefix(key, "HERMES_") {
		return false
	}
	_, ok := os.LookupEnv(key)
	return ok
}

func (c hermesConfig) hermesEnvValue(key string) string {
	if v := strings.TrimSpace(c.envValues[key]); v != "" {
		return v
	}
	if strings.HasPrefix(key, "HERMES_") {
		return strings.TrimSpace(os.Getenv(key))
	}
	return ""
}

func (c hermesConfig) envValue(key string) string {
	return strings.TrimSpace(c.envValues[key])
}

func (c hermesConfig) envTruthy(key string) bool {
	v, ok := c.envValues[key]
	return ok && truthy(v)
}

func (c hermesConfig) envFalse(key string) bool {
	v, ok := c.envValues[key]
	return ok && explicitFalse(v)
}

func (c hermesConfig) hermesEnvTruthy(key string) bool {
	if c.envTruthy(key) {
		return true
	}
	v, ok := os.LookupEnv(key)
	return strings.HasPrefix(key, "HERMES_") && ok && truthy(v)
}

func (c hermesConfig) hermesEnvFalse(key string) bool {
	if c.envFalse(key) {
		return true
	}
	v, ok := os.LookupEnv(key)
	return strings.HasPrefix(key, "HERMES_") && ok && explicitFalse(v)
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "t", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func explicitFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "f", "no", "n", "off", "disabled":
		return true
	default:
		return false
	}
}

func boolValue(v *bool) bool {
	return v != nil && *v
}

func ptrBoolFalse(v *bool) bool {
	return v != nil && !*v
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

func boolStr(b bool) string {
	if b {
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

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
