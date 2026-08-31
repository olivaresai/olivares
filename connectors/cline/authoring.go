// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// authoring.go is the POLICY-WRITE surface for Cline/Kilo Code: it generates a VSCode
// settings.json fragment from governance rules that the operator distributes to the
// workspace or user settings. The generated config pins auto-approve off, MCP server
// allowlist, tool allowlist, and provider/model — the governance-relevant subset of
// the cline.*/kilocode.* VSCode settings namespace.
//
// This is ADVISORY governance: Cline/Kilo Code has no admin/system settings tier. The
// generated settings are a recommendation file that the operator deploys; the user can
// override VSCode settings. The connector documents this limitation honestly.
package cline

import (
	"encoding/json"
	"sort"
)

// Policy is the governance-authored intent for a Cline/Kilo Code install: the desired
// configuration posture expressed in clean, typed form. It is the input to Render.
type Policy struct {
	// Variant selects the settings namespace: "cline" or "kilocode". Default "cline".
	Variant string
	// Provider pins the API provider. Empty = not governed.
	Provider string
	// Model pins the API model. Empty = not governed.
	Model string
	// DisableAutoApprove forces auto-approve off. False = not governed.
	DisableAutoApprove bool
	// MCPServers is the MCP server allowlist: name → entry. Empty = not governed.
	MCPServers map[string]PolicyMCPServer
	// AllowedTools is the tool allowlist. Empty = not governed.
	AllowedTools []string
}

// PolicyMCPServer is one governed MCP server entry.
type PolicyMCPServer struct {
	Command  string   `json:"command,omitempty"`
	Args     []string `json:"args,omitempty"`
	URL      string   `json:"url,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
}

// Render produces a VSCode settings.json fragment from the governance-authored Policy.
// The output is valid JSON containing only the governed keys under the correct namespace
// prefix (cline.* or kilocode.*). Empty policy = empty JSON object.
func Render(p Policy) ([]byte, error) {
	prefix := p.Variant
	if prefix == "" {
		prefix = variantCline
	}

	out := map[string]any{}

	if p.DisableAutoApprove {
		out[prefix+".autoApproveReadOnly"] = false
		out[prefix+".autoApproveWrite"] = false
		out[prefix+".autoApprove"] = []string{}
	}

	if p.Provider != "" {
		out[prefix+".apiProvider"] = p.Provider
	}
	if p.Model != "" {
		out[prefix+".apiModelId"] = p.Model
	}

	if len(p.MCPServers) > 0 {
		servers := map[string]PolicyMCPServer{}
		names := make([]string, 0, len(p.MCPServers))
		for n := range p.MCPServers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			servers[n] = p.MCPServers[n]
		}
		out[prefix+".mcpServers"] = servers
	}

	if len(p.AllowedTools) > 0 {
		out[prefix+".allowedTools"] = p.AllowedTools
	}

	return json.MarshalIndent(out, "", "  ")
}
