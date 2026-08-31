// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.codex-managed-config"

const version = "0.1.0"

// Configuration keys.
const (
	cfgRequirementsPath  = "requirements_path"
	cfgManagedConfigPath = "managed_config_path"
	cfgScope             = "scope"
	cfgExpectedPolicy    = "expected_policy"
)

// Default system-tier file locations (VERIFIED 2026-06-20). The operator names the exact
// path for the host being verified; these are the Unix defaults (Windows lives under
// %ProgramData%\OpenAI\Codex\).
//
//	requirements.toml    Unix /etc/codex/requirements.toml      · Windows %ProgramData%\OpenAI\Codex\requirements.toml
//	managed_config.toml  Unix /etc/codex/managed_config.toml    · Windows %ProgramData%\OpenAI\Codex\managed_config.toml (Windows user fallback ~/.codex/managed_config.toml)
const (
	defaultRequirementsPath  = "/etc/codex/requirements.toml"
	defaultManagedConfigPath = "/etc/codex/managed_config.toml"
)

// config is the resolved connector configuration.
type config struct {
	requirementsPath  string
	managedConfigPath string
	scope             string
	// expected is the governance-authored intent. When nil, the connector is
	// observe-only: it inventories the live policy and flags absence, but does not
	// compute drift (there is nothing to diff against).
	expected *Policy
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "OpenAI Codex managed config (govern + verify)",
		Description: "Reads the live system-tier Codex requirements.toml + managed_config.toml on a host, emits the allowed MCP servers / egress domains as PERMITTED policy edges, and reports drift against the governance-authored Codex policy (constraints vs managed defaults). Read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgRequirementsPath, Type: sdk.FieldString, Default: defaultRequirementsPath, Description: "absolute path to the host's live requirements.toml (Unix: /etc/codex/requirements.toml; Windows: %ProgramData%\\OpenAI\\Codex\\requirements.toml). This is the SYSTEM tier; cloud-managed/MDM requirements (higher precedence) are not observable from here"},
			{Key: cfgManagedConfigPath, Type: sdk.FieldString, Default: defaultManagedConfigPath, Description: "absolute path to the host's live managed_config.toml (Unix: /etc/codex/managed_config.toml). The managed-defaults tier"},
			{Key: cfgScope, Type: sdk.FieldString, Default: "", Description: "attribution scope ref for the managed policy (a host id / org-distribution name); defaults to the OS hostname"},
			{Key: cfgExpectedPolicy, Type: sdk.FieldString, Default: "", Description: "OPTIONAL governance-authored intent as a Policy JSON object {requirements, managed_config}; when set, the connector reports drift (PERMITTED-policy vs OBSERVED-config). When empty it is observe-only (inventory + absence)"},
		},
	}
}

// loadConfig resolves and validates the settings. Both paths must be absolute (a relative
// path is unpredictable in a service context). A malformed expected_policy fails LOUD
// here — never silently downgrading to observe-only, which would hide drift the operator
// asked to detect (the managedsettings posture).
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		requirementsPath:  strings.TrimSpace(firstNonEmpty(cfg.Get(cfgRequirementsPath), defaultRequirementsPath)),
		managedConfigPath: strings.TrimSpace(firstNonEmpty(cfg.Get(cfgManagedConfigPath), defaultManagedConfigPath)),
		scope:             strings.TrimSpace(cfg.Get(cfgScope)),
	}
	if !isAbsPath(c.requirementsPath) {
		return config{}, fmt.Errorf("requirements_path must be absolute, got %q", c.requirementsPath)
	}
	if !isAbsPath(c.managedConfigPath) {
		return config{}, fmt.Errorf("managed_config_path must be absolute, got %q", c.managedConfigPath)
	}
	if raw := strings.TrimSpace(cfg.Get(cfgExpectedPolicy)); raw != "" {
		var p Policy
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return config{}, fmt.Errorf("invalid expected_policy: %w", err)
		}
		c.expected = &p
	}
	if c.scope == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			c.scope = h
		} else {
			c.scope = "codex-managed"
		}
	}
	return c, nil
}

// isAbsPath reports whether a path is absolute on Unix or Windows (a drive-letter or UNC
// root), independent of the runtime OS, so a config authored for a Windows fleet validates
// on a Linux control plane.
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
