// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"encoding/json"
	"errors"
)

// Policy is the governance-authored hardening intent for an opencode install.
// Render turns it into an opencode.json fragment suitable for deployment to the
// managed directory (/etc/opencode on Linux, with OS-specific equivalents).
//
// Unlike the Goose/Cline authoring surfaces, opencode's managed layer is an
// actual enforcement layer because opencode merges it after user and project
// config. The caveats remain important: the merge is per-key last-writer-wins,
// not an immutable lock; OPENCODE_PERMISSION can override permission at runtime;
// OPENCODE_TEST_MANAGED_CONFIG_DIR can redirect the managed directory; and a
// remote organization / Console layer is not represented in this local fragment.
type Policy struct {
	// EditAction pins edit/write/patch permission. Empty defaults to "ask".
	// Only "ask" and "deny" are accepted.
	EditAction string
	// BashAction pins shell execution permission. Empty defaults to "ask".
	// Only "ask" and "deny" are accepted.
	BashAction string
	// MCPServers is the governed MCP server allowlist: name -> server config.
	MCPServers map[string]PolicyMCPServer
}

// PolicyMCPServer is one governed opencode MCP server entry.
type PolicyMCPServer struct {
	Type    string   `json:"type,omitempty"`
	Command []string `json:"command,omitempty"`
	URL     string   `json:"url,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
	Timeout *int     `json:"timeout,omitempty"`
	CWD     string   `json:"cwd,omitempty"`
}

type renderedPolicy struct {
	Permission   map[string]string          `json:"permission"`
	MCP          map[string]PolicyMCPServer `json:"mcp,omitempty"`
	Share        string                     `json:"share"`
	Experimental renderedPolicyExperimental `json:"experimental"`
}

type renderedPolicyExperimental struct {
	OpenTelemetry bool `json:"openTelemetry"`
}

// Render produces a managed-dir deployable opencode.json fragment. It always
// pins edit and bash to ask/deny, disables share egress, and enables native OTEL.
func Render(p Policy) ([]byte, error) {
	edit := normalizePolicyAction(p.EditAction)
	if edit == "" {
		edit = "ask"
	}
	bash := normalizePolicyAction(p.BashAction)
	if bash == "" {
		bash = "ask"
	}
	if !validManagedAction(edit) {
		return nil, errors.New("edit action must be ask or deny")
	}
	if !validManagedAction(bash) {
		return nil, errors.New("bash action must be ask or deny")
	}

	doc := renderedPolicy{
		Permission: map[string]string{
			"edit": edit,
			"bash": bash,
		},
		MCP:   p.MCPServers,
		Share: "disabled",
		Experimental: renderedPolicyExperimental{
			OpenTelemetry: true,
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func normalizePolicyAction(action string) string {
	return normalizeAction(action)
}

func validManagedAction(action string) bool {
	return action == "ask" || action == "deny"
}
