// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package goose

// settings.go holds the Goose profiles.yaml wire shape (the subset this connector
// governs) and the profile selector.
//
// VERIFIED (primary source, jun-2026, github.com/block/goose crates/goose-cli/src/
// config.rs): profiles.yaml is a map of profile names to profile configs. Each profile
// has a provider, model, extensions (MCP servers), and tool settings. The active profile
// is selected by GOOSE_PROFILE env var or defaults to "default".

// profilesFile is the top-level profiles.yaml: a map of profile name → profileConfig.
type profilesFile map[string]profileConfig

// profileConfig is one Goose profile's governance-relevant configuration.
type profileConfig struct {
	Provider   string               `yaml:"provider"`
	Model      string               `yaml:"model"`
	Extensions map[string]extension `yaml:"extensions"`
	Toolshim   *toolshimConfig      `yaml:"toolshim"`

	// Metadata (not from YAML).
	present     bool   `yaml:"-"`
	invalid     bool   `yaml:"-"`
	profileName string `yaml:"-"`
}

// extension is one configured extension (MCP server) in a Goose profile.
type extension struct {
	Type    string            `yaml:"type"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	URL     string            `yaml:"url"`
	Env     map[string]string `yaml:"env"`
	Enabled *bool             `yaml:"enabled"`
}

// toolshimConfig governs tool approval behavior.
type toolshimConfig struct {
	RequireApproval *bool    `yaml:"require_approval"`
	AllowedTools    []string `yaml:"allowed_tools"`
}

// effectiveModel returns the configured model, or empty.
func (p profileConfig) effectiveModel() string {
	return p.Model
}

// effectiveProvider returns the configured provider, or empty.
func (p profileConfig) effectiveProvider() string {
	return p.Provider
}

// enabledExtensions returns the extensions that are not explicitly disabled.
func (p profileConfig) enabledExtensions() map[string]extension {
	out := make(map[string]extension, len(p.Extensions))
	for name, ext := range p.Extensions {
		if ext.Enabled != nil && !*ext.Enabled {
			continue
		}
		out[name] = ext
	}
	return out
}

// extensionRef returns the best non-sensitive reference for an extension (URL or command).
func extensionRef(name string, ext extension) string {
	if ext.URL != "" {
		return ext.URL
	}
	if ext.Command != "" {
		return ext.Command
	}
	return name
}

// requiresApproval reports whether tool execution requires human approval.
func (p profileConfig) requiresApproval() (required, configured bool) {
	if p.Toolshim == nil || p.Toolshim.RequireApproval == nil {
		return false, false
	}
	return *p.Toolshim.RequireApproval, true
}

// hasToolAllowlist reports whether an explicit tool allowlist is configured.
func (p profileConfig) hasToolAllowlist() bool {
	return p.Toolshim != nil && len(p.Toolshim.AllowedTools) > 0
}

// allowedTools returns the configured tool allowlist.
func (p profileConfig) allowedTools() []string {
	if p.Toolshim == nil {
		return nil
	}
	return p.Toolshim.AllowedTools
}
