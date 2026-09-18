// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// ManagedPolicy is the governance input for Grok Build requirements.toml.
//
// Can-enforce (when distributed as root-owned /etc/grok/requirements.toml, which
// Grok clamps as the highest layer): sandbox profile, MCP server allowlist.
//
// Can-only-observe: ~/.grok/disabled-hooks (a user can disable a managed hook
// by name; requirements.toml does not clamp that file). Probe --version is not
// authentication. Compatibility with Claude Code managed-settings.json is
// observed when configured, not authored here.
type ManagedPolicy struct {
	SandboxProfile    string    `json:"sandbox_profile"`
	AllowedMCPServers *[]string `json:"allowed_mcp_servers"`
}

func RenderRequirements(p ManagedPolicy) ([]byte, error) {
	if err := validateManagedPolicy(p); err != nil {
		return nil, err
	}
	m := map[string]any{}
	if s := strings.TrimSpace(p.SandboxProfile); s != "" {
		m["sandbox"] = map[string]any{"profile": s}
	}
	if p.AllowedMCPServers != nil {
		servers := map[string]any{}
		for _, name := range sortedCopy(*p.AllowedMCPServers) {
			if name == "" {
				continue
			}
			servers[name] = map[string]any{}
		}
		m["mcp_servers"] = servers
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("the policy authors no requirements (nothing to render)")
	}
	return toml.Marshal(m)
}

func validateManagedPolicy(p ManagedPolicy) error {
	if s := strings.TrimSpace(p.SandboxProfile); s != "" {
		if _, ok := perfiles[s]; !ok {
			return fmt.Errorf("sandbox_profile %q is not one of %s", s, strings.Join(perfilesConocidos(), ", "))
		}
	}
	if p.AllowedMCPServers != nil {
		seen := map[string]struct{}{}
		for _, name := range *p.AllowedMCPServers {
			n := strings.TrimSpace(name)
			if n == "" {
				return fmt.Errorf("allowed_mcp_servers contains an empty name")
			}
			if _, dup := seen[n]; dup {
				return fmt.Errorf("allowed_mcp_servers contains duplicate %q", n)
			}
			seen[n] = struct{}{}
		}
	}
	return nil
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
