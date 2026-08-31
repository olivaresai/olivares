// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cline

// settings.go holds the Cline/Kilo Code VSCode settings wire shape and the layered
// resolver. Only governance-relevant keys under the cline.* or kilocode.* namespace are
// declared; all other VSCode settings are ignored.
//
// VERIFIED (primary source, jun-2026, github.com/cline/cline src/shared/):
// Cline stores configuration in VSCode settings under the "cline." namespace.
// Workspace settings override user settings (standard VSCode precedence).

// clineSettings is the governance-relevant subset of one VSCode settings layer.
type clineSettings struct {
	AutoApprove         []string                  // list of auto-approved operations
	AutoApproveReadOnly *bool                     // auto-approve read operations
	AutoApproveWrite    *bool                     // auto-approve write operations
	APIProvider         string                    // provider name
	APIModelID          string                    // model identifier
	APIKey              string                    // API key (credential exposure if set)
	CustomInstructions  string                    // custom system prompt
	MCPServers          map[string]mcpServerEntry // configured MCP servers
	AllowedTools        []string                  // tool allowlist
}

// mcpServerEntry is one configured MCP server in Cline settings.
type mcpServerEntry struct {
	Command  string   `json:"command"`
	Args     []string `json:"args"`
	URL      string   `json:"url"`
	Disabled bool     `json:"disabled"`
}

// layer is one resolved settings file with its scope label.
type layer struct {
	scope   string
	s       clineSettings
	present bool
	invalid bool
}

const (
	scopeUser      = "user"
	scopeWorkspace = "workspace"
)

type layers []layer

// effAutoApprove returns whether any auto-approve setting is active.
func (ls layers) hasAutoApprove() bool {
	for _, l := range ls {
		if !l.present || l.invalid {
			continue
		}
		if len(l.s.AutoApprove) > 0 {
			return true
		}
		if l.s.AutoApproveReadOnly != nil && *l.s.AutoApproveReadOnly {
			return true
		}
		if l.s.AutoApproveWrite != nil && *l.s.AutoApproveWrite {
			return true
		}
	}
	return false
}

// effProvider returns the highest-precedence provider.
func (ls layers) effProvider() string {
	for _, l := range ls.workspaceFirst() {
		if l.s.APIProvider != "" {
			return l.s.APIProvider
		}
	}
	return ""
}

// effModel returns the highest-precedence model.
func (ls layers) effModel() string {
	for _, l := range ls.workspaceFirst() {
		if l.s.APIModelID != "" {
			return l.s.APIModelID
		}
	}
	return ""
}

// hasAPIKeyInSettings reports whether an API key is set in settings (credential exposure).
func (ls layers) hasAPIKeyInSettings() bool {
	for _, l := range ls {
		if l.present && !l.invalid && l.s.APIKey != "" {
			return true
		}
	}
	return false
}

// hasCustomInstructions reports whether custom instructions are set.
func (ls layers) hasCustomInstructions() bool {
	for _, l := range ls {
		if l.present && !l.invalid && l.s.CustomInstructions != "" {
			return true
		}
	}
	return false
}

// mcpServers returns all enabled MCP servers across layers.
func (ls layers) mcpServers() map[string]string {
	out := map[string]string{}
	for _, l := range ls.workspaceFirst() {
		for name, srv := range l.s.MCPServers {
			if srv.Disabled {
				continue
			}
			if _, seen := out[name]; seen {
				continue
			}
			ref := name
			if srv.URL != "" {
				ref = srv.URL
			} else if srv.Command != "" {
				ref = srv.Command
			}
			out[name] = ref
		}
	}
	return out
}

// allowedTools returns the de-duplicated tool allowlist.
func (ls layers) allowedTools() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range ls.workspaceFirst() {
		for _, t := range l.s.AllowedTools {
			if t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// workspaceFirst returns present, valid layers workspace-first (highest precedence).
func (ls layers) workspaceFirst() []layer {
	var ws, us []layer
	for _, l := range ls {
		if !l.present || l.invalid {
			continue
		}
		if l.scope == scopeWorkspace {
			ws = append(ws, l)
		} else {
			us = append(us, l)
		}
	}
	return append(ws, us...)
}
