// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// authoring.go is the POLICY-WRITE surface for OpenHands: it generates a config.toml
// fragment from governance rules that the operator distributes to the OpenHands install.
// The generated config pins sandbox type, model, iteration limits, OTEL endpoint, and
// MCP server allowlist — the governance-relevant subset of config.toml.
//
// This is ADVISORY governance: OpenHands has no managed-settings tier. The generated
// config is a recommendation file that the operator deploys; the user can override it
// with environment variables or a local config.toml. The connector documents this
// limitation honestly.
package openhands

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// Policy is the governance-authored intent for an OpenHands install: the desired
// configuration posture expressed in clean, typed form. It is the input to Render.
type Policy struct {
	// SandboxType pins the sandbox type (docker/e2b/remote). Empty = not governed.
	SandboxType string
	// Model pins the LLM model. Empty = not governed.
	Model string
	// Provider pins the LLM provider. Empty = not governed.
	Provider string
	// MaxIterations caps the iteration limit. 0 = not governed.
	MaxIterations int64
	// OTELEndpoint sets the OTEL exporter endpoint. Empty = not governed.
	OTELEndpoint string
	// MCPServers is the MCP server allowlist: name → URL. Empty = not governed.
	MCPServers map[string]string
	// DeniedPlugins lists action plugins to deny. Empty = not governed.
	DeniedPlugins []string
}

// Render produces a config.toml fragment from the governance-authored Policy. The output
// is valid TOML containing only the governed sections. Empty policy = empty output.
func Render(p Policy) ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("# Managed by Olivares AI control plane — do not edit manually.\n")
	buf.WriteString("# Override with environment variables if needed (env wins over TOML).\n\n")

	hasLLM := p.Model != "" || p.Provider != ""
	if hasLLM {
		buf.WriteString("[llm]\n")
		if p.Model != "" {
			fmt.Fprintf(&buf, "model = %q\n", p.Model)
		}
		if p.Provider != "" {
			fmt.Fprintf(&buf, "provider = %q\n", p.Provider)
		}
		buf.WriteByte('\n')
	}

	if p.SandboxType != "" {
		buf.WriteString("[sandbox]\n")
		fmt.Fprintf(&buf, "sandbox_type = %q\n\n", p.SandboxType)
	}

	hasCore := p.MaxIterations > 0 || p.OTELEndpoint != ""
	if hasCore {
		buf.WriteString("[core]\n")
		if p.MaxIterations > 0 {
			fmt.Fprintf(&buf, "max_iterations = %d\n", p.MaxIterations)
		}
		if p.OTELEndpoint != "" {
			fmt.Fprintf(&buf, "otel_exporter_otlp_endpoint = %q\n", p.OTELEndpoint)
		}
		buf.WriteByte('\n')
	}

	if len(p.MCPServers) > 0 {
		buf.WriteString("[mcp]\n[mcp.servers]\n")
		names := make([]string, 0, len(p.MCPServers))
		for n := range p.MCPServers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			safeName := name
			if strings.ContainsAny(name, " .\"") {
				safeName = fmt.Sprintf("%q", name)
			}
			fmt.Fprintf(&buf, "[mcp.servers.%s]\nurl = %q\n", safeName, p.MCPServers[name])
		}
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}
