// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.managed-settings"

const version = "0.1.0"

// Configuration keys.
const (
	cfgConfigPath     = "config_path"
	cfgScope          = "scope"
	cfgExpectedPolicy = "expected_policy"
	cfgDropinDir      = "dropin_dir"
)

// Default managed-settings.json locations by OS (verified 2026-06-05). The
// operator names the exact path for the host being verified; these are documented
// so the right path is obvious.
//
//	macOS    /Library/Application Support/ClaudeCode/managed-settings.json
//	Linux    /etc/claude-code/managed-settings.json
//	Windows  C:\Program Files\ClaudeCode\managed-settings.json
//
// ⭐ EL ALCANCE DE ESTE FICHERO YA NO ES SÓLO CLAUDE CODE, y conviene saberlo antes de razonar
//
//	sobre su radio de acción. Verificado el 2026-08-19 en el repositorio público de xAI, en el
//	commit anclado en `connectors/grok/session/hook.go`:
//	`crates/codegen/xai-grok-config/src/paths.rs:8-11,33-38` declara
//	`CLAUDE_MANAGED_SETTINGS_PATH` con ESTA misma ruta en Linux —y la de macOS— y lo lee «for
//	settings compat». ⇒ **Grok Build honra el managed-settings.json de Claude Code.**
//
//	Quien gobierna Claude por esta vía está gobernando también ese agente. El hecho se EMITE
//	desde `connectors/grok` —es quien reclama esa superficie, y duplicarlo aquí lo reportaría
//	dos veces—; esto es la referencia cruzada para quien llegue a esta constante.
const defaultLinuxPath = "/etc/claude-code/managed-settings.json"

// config is the resolved connector configuration.
type config struct {
	configPath string
	scope      string
	// dropinDir is the managed-settings.d/ drop-in directory whose *.json fragments are
	// deep-merged onto the base file to form the host's EFFECTIVE managed policy.
	// It defaults to the conventional sibling of configPath; an operator may override it
	// for a non-standard layout. An absent directory is a no-op (the common case).
	dropinDir string
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
		Title:       "Claude Code managed-settings.json (govern + verify)",
		Description: "Reads the live managed-settings.json on a host, emits the managed permission grants as PERMITTED policy edges, and reports drift against the governance-authored policy (CLA-05).",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgConfigPath, Type: sdk.FieldString, Default: defaultLinuxPath, Description: "absolute path to the host's live managed-settings.json (macOS: /Library/Application Support/ClaudeCode/…, Linux: /etc/claude-code/…, Windows: C:\\Program Files\\ClaudeCode\\…)"},
			{Key: cfgScope, Type: sdk.FieldString, Default: "", Description: "attribution scope ref for the managed policy (a host id / org-distribution name); defaults to the OS hostname"},
			{Key: cfgExpectedPolicy, Type: sdk.FieldString, Default: "", Description: "OPTIONAL governance-authored intent as a Policy JSON object; when set, the connector reports drift (PERMITTED-policy vs OBSERVED-config). When empty it is observe-only (inventory + absence)"},
			{Key: cfgDropinDir, Type: sdk.FieldString, Default: "", Description: "OPTIONAL override for the managed-settings.d/ drop-in directory whose *.json fragments are deep-merged onto config_path. Defaults to the conventional sibling of config_path (e.g. /etc/claude-code/managed-settings.d); an absent directory is a no-op"},
		},
	}
}

// loadConfig resolves and validates the settings. config_path is required and must
// be absolute (a relative path is unpredictable in a service context). A malformed
// expected_policy fails LOUD here — never silently downgrading to observe-only,
// which would hide drift the operator asked to detect.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		configPath: strings.TrimSpace(firstNonEmpty(cfg.Get(cfgConfigPath), defaultLinuxPath)),
		scope:      strings.TrimSpace(cfg.Get(cfgScope)),
	}
	if c.configPath == "" {
		return config{}, fmt.Errorf("config_path is required")
	}
	if !isAbsPath(c.configPath) {
		return config{}, fmt.Errorf("config_path must be absolute, got %q", c.configPath)
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
			c.scope = "managed"
		}
	}
	// The drop-in directory defaults to the conventional sibling of the base file
	// (<dir>/managed-settings.d); an operator may override it for a non-standard layout.
	c.dropinDir = strings.TrimSpace(cfg.Get(cfgDropinDir))
	if c.dropinDir == "" {
		c.dropinDir = filepath.Join(filepath.Dir(c.configPath), dropinDirName)
	}
	return c, nil
}

// isAbsPath reports whether a path is absolute on Unix or Windows (a drive-letter
// or UNC root), independent of the runtime OS, so a config authored for a Windows
// fleet validates on a Linux control plane.
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	// C:\ or C:/ drive-letter root.
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
