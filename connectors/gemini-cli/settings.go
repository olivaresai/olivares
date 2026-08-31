// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package geminicli

import (
	"encoding/json"
	"sort"
)

// settings.go holds the gemini-cli settings.json wire shape (the subset this connector
// governs) and the layered-precedence resolver. Only governance-relevant keys are
// declared; unknown keys are ignored (forward-compatible). Pointers/empties mark
// PRESENCE so the resolver can tell "set to false at scope X" from "unset" — the
// difference between an enforced control and an absent one (ARCHITECTURE.md).
//
// VERIFIED (primary source, jun-2026, github.com/google-gemini/gemini-cli
// packages/cli/src/config/settings.ts + schemas): settings are NESTED category objects,
// merged with precedence (last wins for single values): schema-defaults < system-defaults
// < user (~/.gemini/settings.json) < workspace (./.gemini/settings.json) < SYSTEM override
// (/etc/gemini-cli/settings.json). mcpServers and telemetry are TOP-LEVEL keys (NOT nested
// under the mcp/ category). The admin{} block is fetched REMOTELY and is NOT in these files
// (an honest blind spot, reported as a caveat — never read from disk).

// settings is the governance-relevant subset of one settings.json layer.
type settings struct {
	Security   *securityCfg            `json:"security"`
	Tools      *toolsCfg               `json:"tools"`
	MCP        *mcpCfg                 `json:"mcp"`
	MCPServers map[string]mcpServerCfg `json:"mcpServers"` // TOP-LEVEL (verified)
	Telemetry  *telemetryCfg           `json:"telemetry"`  // TOP-LEVEL (verified)
	General    *generalCfg             `json:"general"`
	Privacy    *privacyCfg             `json:"privacy"`
	Context    *contextCfg             `json:"context"`
}

type securityCfg struct {
	DisableYoloMode *bool    `json:"disableYoloMode"`
	ToolSandboxing  *bool    `json:"toolSandboxing"`
	Auth            *authCfg `json:"auth"`
}

type authCfg struct {
	SelectedType string `json:"selectedType"`
	EnforcedType string `json:"enforcedType"`
}

type toolsCfg struct {
	Core    []string `json:"core"`
	Allowed []string `json:"allowed"`
	Exclude []string `json:"exclude"`
}

type mcpCfg struct {
	Allowed  []string `json:"allowed"`
	Excluded []string `json:"excluded"`
}

// mcpServerCfg is one configured MCP server. The connector reads only its address (a
// remote url/httpUrl, or the stdio command name via the map key) for the PERMITTED
// capability edge — never env/secrets.
type mcpServerCfg struct {
	Command string `json:"command"`
	URL     string `json:"url"`
	HTTPURL string `json:"httpUrl"`
}

type telemetryCfg struct {
	Enabled    *bool  `json:"enabled"`
	Target     string `json:"target"`  // "gcp" | "local"
	Outfile    string `json:"outfile"` // local file export path
	LogPrompts *bool  `json:"logPrompts"`
}

type generalCfg struct {
	DefaultApprovalMode string `json:"defaultApprovalMode"` // "default" | "auto_edit" | "plan"
}

type privacyCfg struct {
	UsageStatisticsEnabled *bool `json:"usageStatisticsEnabled"`
}

type contextCfg struct {
	IncludeDirectories []string `json:"includeDirectories"`
}

// parseSettings decodes one layer. An empty body is a zero (valid, empty) layer; a
// malformed body returns the decode error so the caller can report "present but invalid".
func parseSettings(data []byte) (settings, error) {
	var s settings
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return settings{}, err
	}
	return s, nil
}

// layer is one resolved settings file with its scope label and presence/validity.
type layer struct {
	scope   string
	s       settings
	present bool
	invalid bool
}

// scope labels in PRECEDENCE order (low→high). The resolver walks high→low so the
// highest-precedence layer that SETS a key wins (the CLI's "last wins for single values").
const (
	scopeSystemDefaults = "system-defaults"
	scopeUser           = "user"
	scopeWorkspace      = "workspace"
	scopeSystem         = "system" // /etc/gemini-cli/settings.json override — final say
)

// layers is the ordered set, lowest precedence first.
type layers []layer

// highToLow returns the present, valid layers from highest precedence to lowest, so the
// first to set a key wins.
func (ls layers) highToLow() []layer {
	order := map[string]int{scopeSystem: 0, scopeWorkspace: 1, scopeUser: 2, scopeSystemDefaults: 3}
	out := make([]layer, 0, len(ls))
	for _, l := range ls {
		if l.present && !l.invalid {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].scope] < order[out[j].scope] })
	return out
}

// effBool resolves a *bool governance key: the value from the highest-precedence layer
// that sets it, plus the winning scope. set=false means no layer sets it.
func (ls layers) effBool(get func(settings) *bool) (val, set bool, scope string) {
	for _, l := range ls.highToLow() {
		if p := get(l.s); p != nil {
			return *p, true, l.scope
		}
	}
	return false, false, ""
}

// effStr resolves a string key (empty = unset). Returns the value + winning scope.
func (ls layers) effStr(get func(settings) string) (val, scope string) {
	for _, l := range ls.highToLow() {
		if v := get(l.s); v != "" {
			return v, l.scope
		}
	}
	return "", ""
}

// effStrs resolves a []string key (empty/nil = unset). Returns the highest-precedence
// non-empty slice + its scope (single-value precedence, not the CLI's per-key array merge
// — sufficient for "is an allowlist in force, and where").
func (ls layers) effStrs(get func(settings) []string) (val []string, scope string) {
	for _, l := range ls.highToLow() {
		if v := get(l.s); len(v) > 0 {
			return v, l.scope
		}
	}
	return nil, ""
}

// mcpServers unions the configured MCP servers across all PRESENT, VALID layers (highToLow
// excludes a present-but-invalid layer, which must not contribute trusted addresses; the
// CLI itself merges valid layers by name across scopes), returning name→address. Address is
// a remote url/httpUrl, or the stdio command (or the name) when there is no URL.
func (ls layers) mcpServers() map[string]string {
	out := map[string]string{}
	for _, l := range ls.highToLow() { // high→low so a higher scope's address wins on a name clash
		for name, srv := range l.s.MCPServers {
			if _, seen := out[name]; seen {
				continue
			}
			out[name] = firstNonEmpty(srv.HTTPURL, srv.URL, srv.Command, name)
		}
	}
	return out
}

// hasSystemLayer reports whether the SYSTEM override layer is present (the admin-enforced
// tier). Its absence is the headline "ungoverned fleet" posture finding.
func (ls layers) hasSystemLayer() bool {
	for _, l := range ls {
		if l.scope == scopeSystem && l.present && !l.invalid {
			return true
		}
	}
	return false
}
