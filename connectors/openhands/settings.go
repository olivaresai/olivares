// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"os"
	"strconv"
	"strings"
)

// config holds the governance-relevant subset of OpenHands config.toml. The raw TOML map
// is used for flexible access to nested keys; only governance-relevant keys are inspected.
// Unknown keys are ignored (forward-compatible).
//
// VERIFIED (primary source, jun-2026, github.com/All-Hands-AI/OpenHands
// openhands/core/config/): config.toml uses TOML tables [core], [llm], [sandbox], etc.
// Environment variables override every TOML key with the pattern OPENHANDS_* / LLM_* /
// SANDBOX_*. The environment layer wins unconditionally.
type config struct {
	raw     map[string]any
	present bool
	invalid bool

	// Environment overrides (applied after TOML parsing).
	envModel       string
	envProvider    string
	envAPIKey      bool // true if LLM_API_KEY / OPENAI_API_KEY set (presence, not value)
	envSandboxType string
	envMaxIter     string
}

// getString reads a dotted key ("llm.model") from the raw TOML map.
func (c config) getString(key string) string {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 || c.raw == nil {
		return ""
	}
	section, ok := c.raw[parts[0]]
	if !ok {
		return ""
	}
	m, ok := section.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := m[parts[1]]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// getInt reads a dotted integer key from the raw TOML map.
func (c config) getInt(key string) (int64, bool) {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 || c.raw == nil {
		return 0, false
	}
	section, ok := c.raw[parts[0]]
	if !ok {
		return 0, false
	}
	m, ok := section.(map[string]any)
	if !ok {
		return 0, false
	}
	v, ok := m[parts[1]]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// getBool reads a dotted boolean key from the raw TOML map.
func (c config) getBool(key string) (bool, bool) {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 || c.raw == nil {
		return false, false
	}
	section, ok := c.raw[parts[0]]
	if !ok {
		return false, false
	}
	m, ok := section.(map[string]any)
	if !ok {
		return false, false
	}
	v, ok := m[parts[1]]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// getStringSlice reads a dotted key as a string slice from the raw TOML map.
func (c config) getStringSlice(key string) []string {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 || c.raw == nil {
		return nil
	}
	section, ok := c.raw[parts[0]]
	if !ok {
		return nil
	}
	m, ok := section.(map[string]any)
	if !ok {
		return nil
	}
	v, ok := m[parts[1]]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// getSubMap reads a dotted key as a sub-map from the raw TOML map.
func (c config) getSubMap(key string) map[string]any {
	if c.raw == nil {
		return nil
	}
	section, ok := c.raw[key]
	if !ok {
		return nil
	}
	m, ok := section.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// effectiveModel returns the resolved model: env override > TOML.
func (c config) effectiveModel() string {
	if c.envModel != "" {
		return c.envModel
	}
	return c.getString("llm.model")
}

// effectiveSandboxType returns the resolved sandbox type: env override > TOML.
func (c config) effectiveSandboxType() string {
	if c.envSandboxType != "" {
		return c.envSandboxType
	}
	return c.getString("sandbox.sandbox_type")
}

// hasAPIKeyInConfig reports whether an API key appears in the TOML config file (a
// credential exposure finding — the key should be in env vars or a secret store).
func (c config) hasAPIKeyInConfig() bool {
	return c.getString("llm.api_key") != ""
}

// hasAPIKeyInEnv reports whether LLM_API_KEY or OPENAI_API_KEY is set.
func (c config) hasAPIKeyInEnv() bool {
	return c.envAPIKey
}

// effectiveMaxIterations returns the resolved max_iterations: env override > TOML.
func (c config) effectiveMaxIterations() (int64, bool) {
	if c.envMaxIter != "" {
		n, err := strconv.ParseInt(c.envMaxIter, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return c.getInt("core.max_iterations")
}

// mcpServers returns the configured MCP servers from the [mcp] TOML section.
func (c config) mcpServers() map[string]string {
	mcp := c.getSubMap("mcp")
	if len(mcp) == 0 {
		return nil
	}
	servers, ok := mcp["servers"]
	if !ok {
		return nil
	}
	srvMap, ok := servers.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(srvMap))
	for name, v := range srvMap {
		ref := name
		if m, ok := v.(map[string]any); ok {
			if url, ok := m["url"].(string); ok && url != "" {
				ref = url
			} else if cmd, ok := m["command"].(string); ok && cmd != "" {
				ref = cmd
			}
		}
		out[name] = ref
	}
	return out
}

// actionPlugins returns configured action plugins.
func (c config) actionPlugins() []string {
	return c.getStringSlice("core.plugins")
}

// otelEnabled reports whether OTEL export is configured.
func (c config) otelEnabled() bool {
	ep := c.getString("core.otel_exporter_otlp_endpoint")
	if ep != "" {
		return true
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

// otelEndpoint returns the configured OTEL endpoint (for coverage assessment).
func (c config) otelEndpoint() string {
	ep := c.getString("core.otel_exporter_otlp_endpoint")
	if ep == "" {
		ep = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	return ep
}

// applyEnvOverrides reads governance-relevant environment variables and overlays them.
func (s *Source) applyEnvOverrides(c config) config {
	if v := os.Getenv("LLM_MODEL"); v != "" {
		c.envModel = v
	}
	if v := os.Getenv("LLM_PROVIDER"); v != "" {
		c.envProvider = v
	}
	_, keySet := os.LookupEnv("LLM_API_KEY")
	_, oaiSet := os.LookupEnv("OPENAI_API_KEY")
	c.envAPIKey = keySet || oaiSet
	if v := os.Getenv("SANDBOX_TYPE"); v != "" {
		c.envSandboxType = v
	}
	if v := os.Getenv("MAX_ITERATIONS"); v != "" {
		c.envMaxIter = v
	}
	return c
}

// agentsMDPresent checks for AGENTS.md at the workspace root.
func agentsMDPresent(configPath string) bool {
	dir := dirOf(configPath)
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir + "/AGENTS.md")
	return err == nil && !info.IsDir()
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}
