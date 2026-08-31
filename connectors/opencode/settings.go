// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	scopeGlobal  = "global"
	scopeProject = "project"
)

type configLayer struct {
	scope   string
	path    string
	cfg     config
	present bool
	invalid bool
}

// config is the governance-relevant subset of opencode's JSONC config schema.
// Unknown keys are intentionally ignored.
type config struct {
	Permission        *permissionConfig         `json:"permission"`
	MCP               map[string]mcpConfig      `json:"mcp"`
	Provider          map[string]providerConfig `json:"provider"`
	Model             *string                   `json:"model"`
	SmallModel        *string                   `json:"small_model"`
	Agent             map[string]agentConfig    `json:"agent"`
	Mode              map[string]agentConfig    `json:"mode"`
	Tools             map[string]bool           `json:"tools"`
	Share             *string                   `json:"share"`
	Experimental      *experimentalConfig       `json:"experimental"`
	Autoupdate        *autoUpdateConfig         `json:"autoupdate"`
	DisabledProviders []string                  `json:"disabled_providers"`
	EnabledProviders  []string                  `json:"enabled_providers"`
	Instructions      []string                  `json:"instructions"`
	DefaultAgent      *string                   `json:"default_agent"`
	LogLevel          *string                   `json:"logLevel"`

	present bool
}

type permissionConfig struct {
	Set        bool
	Scalar     string
	ObjectMode bool
	Object     map[string]permissionRule
}

type permissionRule struct {
	Set            bool
	Scalar         string
	PatternActions map[string]string
}

type mcpConfig struct {
	Type    *string      `json:"type"`
	Command commandValue `json:"command"`
	URL     *string      `json:"url"`
	Enabled *bool        `json:"enabled"`
}

type commandValue struct {
	Set   bool
	Parts []string
}

type providerConfig struct {
	Options *providerOptions           `json:"options"`
	Models  map[string]json.RawMessage `json:"models"`
}

type providerOptions struct {
	APIKey  *string `json:"apiKey"`
	BaseURL *string `json:"baseURL"`
}

type agentConfig struct {
	Model      *string           `json:"model"`
	Prompt     *string           `json:"prompt"`
	Tools      map[string]bool   `json:"tools"`
	Permission *permissionConfig `json:"permission"`
	Mode       *string           `json:"mode"`
	Disable    *bool             `json:"disable"`
	Hidden     *bool             `json:"hidden"`
	Steps      *int              `json:"steps"`
}

type experimentalConfig struct {
	OpenTelemetry      *bool `json:"openTelemetry"`
	ContinueLoopOnDeny *bool `json:"continue_loop_on_deny"`
}

type autoUpdateConfig struct {
	Set   bool
	Bool  *bool
	Value string
}

func (p *permissionConfig) UnmarshalJSON(data []byte) error {
	p.Set = true
	var scalar string
	if err := json.Unmarshal(data, &scalar); err == nil {
		p.Scalar = normalizeAction(scalar)
		p.ObjectMode = false
		p.Object = nil
		return nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.ObjectMode = true
	p.Object = make(map[string]permissionRule, len(raw))
	for key, val := range raw {
		var rule permissionRule
		if err := json.Unmarshal(val, &rule); err == nil && rule.Set {
			p.Object[key] = rule
		}
	}
	return nil
}

func (r *permissionRule) UnmarshalJSON(data []byte) error {
	r.Set = true
	var scalar string
	if err := json.Unmarshal(data, &scalar); err == nil {
		r.Scalar = normalizeAction(scalar)
		return nil
	}
	var patterns map[string]string
	if err := json.Unmarshal(data, &patterns); err != nil {
		return err
	}
	r.PatternActions = make(map[string]string, len(patterns))
	for pattern, action := range patterns {
		r.PatternActions[pattern] = normalizeAction(action)
	}
	return nil
}

func (c *commandValue) UnmarshalJSON(data []byte) error {
	c.Set = true
	var parts []string
	if err := json.Unmarshal(data, &parts); err == nil {
		c.Parts = compactStrings(parts)
		return nil
	}
	var scalar string
	if err := json.Unmarshal(data, &scalar); err != nil {
		return err
	}
	scalar = strings.TrimSpace(scalar)
	if scalar != "" {
		c.Parts = []string{scalar}
	}
	return nil
}

func (a *autoUpdateConfig) UnmarshalJSON(data []byte) error {
	a.Set = true
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		a.Bool = &b
		if b {
			a.Value = "true"
		} else {
			a.Value = "false"
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	a.Value = strings.TrimSpace(s)
	return nil
}

func (c config) merge(overlay config) config {
	out := c
	if overlay.Permission != nil {
		out.Permission = mergePermission(out.Permission, overlay.Permission)
	}
	out.MCP = mergeStringMap(out.MCP, overlay.MCP, func(base, over mcpConfig) mcpConfig { return base.merge(over) })
	out.Provider = mergeStringMap(out.Provider, overlay.Provider, func(base, over providerConfig) providerConfig { return base.merge(over) })
	out.Agent = mergeStringMap(out.Agent, overlay.Agent, func(base, over agentConfig) agentConfig { return base.merge(over) })
	out.Tools = mergeBoolMap(out.Tools, overlay.Tools)

	if overlay.Model != nil {
		out.Model = overlay.Model
	}
	if overlay.SmallModel != nil {
		out.SmallModel = overlay.SmallModel
	}
	if overlay.Share != nil {
		out.Share = overlay.Share
	}
	if overlay.Experimental != nil {
		out.Experimental = mergeExperimental(out.Experimental, overlay.Experimental)
	}
	if overlay.Autoupdate != nil {
		out.Autoupdate = overlay.Autoupdate
	}
	if overlay.DisabledProviders != nil {
		out.DisabledProviders = overlay.DisabledProviders
	}
	if overlay.EnabledProviders != nil {
		out.EnabledProviders = overlay.EnabledProviders
	}
	if overlay.Instructions != nil {
		out.Instructions = overlay.Instructions
	}
	if overlay.DefaultAgent != nil {
		out.DefaultAgent = overlay.DefaultAgent
	}
	if overlay.LogLevel != nil {
		out.LogLevel = overlay.LogLevel
	}
	out.present = out.present || overlay.present
	return out
}

func (c *config) foldDeprecatedMode() {
	if len(c.Mode) == 0 {
		return
	}
	if c.Agent == nil {
		c.Agent = map[string]agentConfig{}
	}
	for name, modeAgent := range c.Mode {
		c.Agent[name] = c.Agent[name].merge(modeAgent)
	}
}

func mergePermission(base, overlay *permissionConfig) *permissionConfig {
	if overlay == nil {
		return base
	}
	if base == nil || !base.ObjectMode || !overlay.ObjectMode {
		return overlay.clone()
	}
	out := base.clone()
	if out.Object == nil {
		out.Object = map[string]permissionRule{}
	}
	for key, rule := range overlay.Object {
		out.Object[key] = rule
	}
	return out
}

func (p *permissionConfig) clone() *permissionConfig {
	if p == nil {
		return nil
	}
	out := *p
	if p.Object != nil {
		out.Object = make(map[string]permissionRule, len(p.Object))
		for key, rule := range p.Object {
			out.Object[key] = rule
		}
	}
	return &out
}

func (m mcpConfig) merge(overlay mcpConfig) mcpConfig {
	out := m
	if overlay.Type != nil {
		out.Type = overlay.Type
	}
	if overlay.Command.Set {
		out.Command = overlay.Command
	}
	if overlay.URL != nil {
		out.URL = overlay.URL
	}
	if overlay.Enabled != nil {
		out.Enabled = overlay.Enabled
	}
	return out
}

func (p providerConfig) merge(overlay providerConfig) providerConfig {
	out := p
	if overlay.Options != nil {
		if out.Options == nil {
			out.Options = overlay.Options
		} else {
			merged := *out.Options
			if overlay.Options.APIKey != nil {
				merged.APIKey = overlay.Options.APIKey
			}
			if overlay.Options.BaseURL != nil {
				merged.BaseURL = overlay.Options.BaseURL
			}
			out.Options = &merged
		}
	}
	if len(overlay.Models) > 0 {
		if out.Models == nil {
			out.Models = map[string]json.RawMessage{}
		}
		for key, val := range overlay.Models {
			out.Models[key] = val
		}
	}
	return out
}

func (a agentConfig) merge(overlay agentConfig) agentConfig {
	out := a
	if overlay.Model != nil {
		out.Model = overlay.Model
	}
	if overlay.Prompt != nil {
		out.Prompt = overlay.Prompt
	}
	if overlay.Permission != nil {
		out.Permission = mergePermission(out.Permission, overlay.Permission)
	}
	if overlay.Tools != nil {
		out.Tools = mergeBoolMap(out.Tools, overlay.Tools)
	}
	if overlay.Mode != nil {
		out.Mode = overlay.Mode
	}
	if overlay.Disable != nil {
		out.Disable = overlay.Disable
	}
	if overlay.Hidden != nil {
		out.Hidden = overlay.Hidden
	}
	if overlay.Steps != nil {
		out.Steps = overlay.Steps
	}
	return out
}

func mergeExperimental(base, overlay *experimentalConfig) *experimentalConfig {
	if overlay == nil {
		return base
	}
	if base == nil {
		out := *overlay
		return &out
	}
	out := *base
	if overlay.OpenTelemetry != nil {
		out.OpenTelemetry = overlay.OpenTelemetry
	}
	if overlay.ContinueLoopOnDeny != nil {
		out.ContinueLoopOnDeny = overlay.ContinueLoopOnDeny
	}
	return &out
}

func mergeStringMap[T any](base, overlay map[string]T, merge func(T, T) T) map[string]T {
	if overlay == nil {
		return base
	}
	out := make(map[string]T, len(base)+len(overlay))
	for key, val := range base {
		out[key] = val
	}
	for key, val := range overlay {
		if old, ok := out[key]; ok {
			out[key] = merge(old, val)
		} else {
			out[key] = val
		}
	}
	return out
}

func mergeBoolMap(base, overlay map[string]bool) map[string]bool {
	if overlay == nil {
		return base
	}
	out := make(map[string]bool, len(base)+len(overlay))
	for key, val := range base {
		out[key] = val
	}
	for key, val := range overlay {
		out[key] = val
	}
	return out
}

func (c config) primaryAgentName() string {
	if c.DefaultAgent != nil && strings.TrimSpace(*c.DefaultAgent) != "" {
		return strings.TrimSpace(*c.DefaultAgent)
	}
	for _, name := range sortedAgentKeys(c.Agent) {
		agent := c.Agent[name]
		if agent.disabled() {
			continue
		}
		if agent.Mode != nil {
			mode := strings.TrimSpace(*agent.Mode)
			if mode == "primary" || mode == "all" {
				return name
			}
		}
	}
	return "build"
}

func (c config) effectivePrimaryPermission() *permissionConfig {
	primary := c.primaryAgentName()
	if agent, ok := c.Agent[primary]; ok && agent.Permission != nil {
		return agent.Permission
	}
	if c.Permission != nil {
		return c.Permission
	}
	if primary == "plan" {
		return planDefaultPermission()
	}
	return nil
}

func planDefaultPermission() *permissionConfig {
	return &permissionConfig{
		Set:        true,
		ObjectMode: true,
		Object: map[string]permissionRule{
			"edit": {Set: true, Scalar: "ask"},
			"bash": {Set: true, Scalar: "ask"},
		},
	}
}

func (c config) permissionMode() string {
	p := c.effectivePrimaryPermission()
	if p == nil || !p.Set {
		return "permissive-default"
	}
	if p.Scalar != "" {
		return p.Scalar
	}
	return "object"
}

func (c config) hasPermissiveDefault() bool {
	return c.effectivePrimaryPermission() == nil
}

func (c config) blanketAllowSubjects() []string {
	var out []string
	if c.Permission != nil && c.Permission.blanketAllow() {
		out = append(out, "top-level")
	}
	for _, name := range sortedAgentKeys(c.Agent) {
		agent := c.Agent[name]
		if agent.disabled() || agent.Permission == nil {
			continue
		}
		if agent.Permission.blanketAllow() {
			out = append(out, "agent."+name)
		}
	}
	return out
}

func (p *permissionConfig) blanketAllow() bool {
	return p != nil && p.Set && p.Scalar == "allow"
}

func (p *permissionConfig) actionGated(tool string) bool {
	if p == nil || !p.Set {
		return false
	}
	if p.Scalar != "" {
		return p.Scalar == "ask" || p.Scalar == "deny"
	}
	rule, ok := p.Object[tool]
	if !ok {
		return false
	}
	return rule.gated()
}

func (r permissionRule) gated() bool {
	if r.Scalar != "" {
		return r.Scalar == "ask" || r.Scalar == "deny"
	}
	for _, action := range r.PatternActions {
		if action == "ask" || action == "deny" {
			return true
		}
	}
	return false
}

func (c config) enabledMCPServers() map[string]string {
	out := map[string]string{}
	for _, name := range sortedMCPKeys(c.MCP) {
		srv := c.MCP[name]
		if srv.Enabled != nil && !*srv.Enabled {
			continue
		}
		ref := strings.TrimSpace(name)
		if srv.URL != nil && strings.TrimSpace(*srv.URL) != "" {
			ref = strings.TrimSpace(*srv.URL)
		} else if len(srv.Command.Parts) > 0 {
			ref = srv.Command.Parts[0]
		}
		if ref != "" {
			out[name] = ref
		}
	}
	return out
}

func (c config) enabledTools() []string {
	seen := map[string]bool{}
	for name, enabled := range c.Tools {
		if enabled && strings.TrimSpace(name) != "" {
			seen[strings.TrimSpace(name)] = true
		}
	}
	for _, agentName := range sortedAgentKeys(c.Agent) {
		agent := c.Agent[agentName]
		if agent.disabled() {
			continue
		}
		for tool, enabled := range agent.Tools {
			if enabled && strings.TrimSpace(tool) != "" {
				seen[strings.TrimSpace(tool)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for tool := range seen {
		out = append(out, tool)
	}
	sort.Strings(out)
	return out
}

func (c config) customAgents() []string {
	var out []string
	for _, name := range sortedAgentKeys(c.Agent) {
		agent := c.Agent[name]
		if agent.disabled() || isBuiltinAgent(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

func (c config) credentialInConfig() bool {
	for _, name := range sortedProviderKeys(c.Provider) {
		provider := c.Provider[name]
		if provider.Options == nil || provider.Options.APIKey == nil {
			continue
		}
		apiKey := strings.TrimSpace(*provider.Options.APIKey)
		if apiKey != "" && !isUnresolvedToken(apiKey) {
			return true
		}
	}
	return false
}

func (c config) shareMode() string {
	if c.Share == nil || strings.TrimSpace(*c.Share) == "" {
		return "manual"
	}
	return strings.TrimSpace(*c.Share)
}

func (c config) otelEnabled() bool {
	return c.Experimental != nil && c.Experimental.OpenTelemetry != nil && *c.Experimental.OpenTelemetry
}

func (c config) continueLoopOnDeny() bool {
	return c.Experimental != nil && c.Experimental.ContinueLoopOnDeny != nil && *c.Experimental.ContinueLoopOnDeny
}

func (c config) autoupdateEnabled() bool {
	return c.Autoupdate != nil && c.Autoupdate.Bool != nil && *c.Autoupdate.Bool
}

func (c config) autoupdateLabel() string {
	if c.Autoupdate == nil || !c.Autoupdate.Set {
		return "unset"
	}
	return firstNonEmpty(c.Autoupdate.Value, "unset")
}

func (a agentConfig) disabled() bool {
	return a.Disable != nil && *a.Disable
}

func normalizeAction(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func isUnresolvedToken(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{env:") || strings.HasPrefix(s, "{file:")
}

func isBuiltinAgent(name string) bool {
	switch name {
	case "plan", "build", "general", "explore", "title", "summary", "compaction":
		return true
	default:
		return false
	}
}

func sortedAgentKeys(m map[string]agentConfig) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedMCPKeys(m map[string]mcpConfig) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedProviderKeys(m map[string]providerConfig) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func compactStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func stripJSONC(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false
	lineComment := false
	blockComment := false

	for i := 0; i < len(data); i++ {
		ch := data[i]
		next := byte(0)
		if i+1 < len(data) {
			next = data[i+1]
		}

		switch {
		case lineComment:
			if ch == '\n' || ch == '\r' {
				lineComment = false
				out = append(out, ch)
			}
			continue
		case blockComment:
			if ch == '\n' || ch == '\r' {
				out = append(out, ch)
			}
			if ch == '*' && next == '/' {
				blockComment = false
				i++
			}
			continue
		case inString:
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
		default:
			if ch == '"' {
				inString = true
				out = append(out, ch)
				continue
			}
			if ch == '/' && next == '/' {
				lineComment = true
				i++
				continue
			}
			if ch == '/' && next == '*' {
				blockComment = true
				i++
				continue
			}
			out = append(out, ch)
		}
	}
	return removeTrailingCommas(out)
}

func removeTrailingCommas(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out = append(out, ch)
	}
	return out
}
