// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.mcp"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgServers         = "servers"
	cfgConfigPath      = "config_path"
	cfgTimeout         = "timeout"
	cfgRegistryEnabled = "registry_enabled"
	cfgRegistryURL     = "registry_url"
	// registry sync + internal registry + posture scanning.
	cfgRegistrySync    = "registry_sync"    // enumerate owned namespaces in the public registry (yank/unmanaged detection)
	cfgOwnedNamespaces = "owned_namespaces" // reverse-DNS namespaces the org owns (JSON array or comma list)
	cfgInternalServers = "internal_servers" // JSON array of approved internal MCP server entries (name/registry_name/version)
	cfgPostureScan     = "posture_scan"     // run the introspection-time posture scanner (default on)
	// the 2026-07-28 frozen-RC stateless mode is the default.
	cfgNextRevisionPreview = "next_revision_preview" // auto-negotiate the 2026-07-28 frozen-RC stateless mode by default; per-server next_revision is tri-state
	// deprecation-aware posture + catalog federation.
	cfgDeprecationFeed     = "deprecation_feed"     // fetch the official deprecated-features registry each pass (drift detection; rules stay compiled-in)
	cfgDeprecationFeedURL  = "deprecation_feed_url" // deprecated-features registry source (raw MDX); setting it implies deprecation_feed
	cfgFederatedRegistries = "federated_registries" // JSON array of federated MCP registries ({name, url, allowlist}) implementing the /v0.1 OpenAPI
	cfgDockerCatalog       = "docker_catalog"       // consult the Docker MCP Catalog feed for image pin/provenance checks
	cfgDockerCatalogURL    = "docker_catalog_url"   // Docker MCP Catalog feed URL; setting it implies docker_catalog
)

// defaultTimeout bounds the introspection of a single server.
const defaultTimeout = 30 * time.Second

// defaultRegistryURL is the official MCP Registry base URL (AIP-03). It is in
// PREVIEW (minimal-to-no moderation), so the connector treats any result as
// self-verify provenance, never an endorsement (registry.go).
const defaultRegistryURL = "https://registry.modelcontextprotocol.io"

// serverSpec is one MCP server to introspect. It is shaped to accept both an
// inline JSON array (the "servers" setting) and a Claude Code .mcp.json entry
// (the "config_path" setting). Env and Headers may carry secret references; they
// are used only to connect and are never persisted.
type serverSpec struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	// Auth, when present, enables MCP OAuth (IDN-03 Phase 2): the connector obtains a
	// token bound to this server and introspects authenticated. Absent, an
	// OAuth-protected server is only detected (Phase 1).
	Auth *serverAuth `json:"auth"`
	// RegistryName, when set, is the reverse-DNS MCP Registry name the operator
	// asserts this server is published under (e.g. "io.github.acme/widgets"). It
	// lets the registry enrichment resolve provenance confidently; absent, a server
	// that cannot be tied to a verified namespace is flagged as a shadow candidate
	// (AIP-03 / OWASP MCP09). It is never trusted as proof — the registry is
	// consulted to corroborate it (registry.go).
	RegistryName string `json:"registry_name"`
	// NextRevision is a tri-state per-server override for the 2026-07-28
	// frozen-RC stateless mode: nil inherits next_revision_preview
	// and auto-negotiates when that connector flag is true, true forces
	// stateless-only fail-loud behavior, and false forces the 2025-11-25
	// Initialize path.
	NextRevision *bool `json:"next_revision"`
	// UITemplates is the operator's PRE-DECLARED inventory of the MCP App ui://
	// templates this server is sanctioned to expose (SEP-1865). With a
	// non-empty list, an observed template outside it is a HIGH mcp_app finding;
	// with no list, any observed ui:// surface is flagged as ungoverned. The RS
	// PEP enforces the same inventory at render time (apps.go).
	UITemplates []string `json:"ui_templates"`
}

// config is the resolved connector configuration.
type config struct {
	servers         []serverSpec
	timeout         time.Duration
	registryEnabled bool
	registryURL     string
	// registry sync + internal registry + posture scanning.
	registrySync    bool
	ownedNamespaces []string
	internalServers []internalEntry
	postureScan     bool
	// auto-negotiate the 2026-07-28 frozen-RC stateless mode by default.
	nextRevisionPreview bool
	// deprecation-feed drift detection + catalog federation.
	deprecationFeed     bool
	deprecationFeedURL  string
	federatedRegistries []federatedRegistrySpec
	dockerCatalog       bool
	dockerCatalogURL    string
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "MCP introspection",
		Description: "Introspects MCP servers (stdio + Streamable HTTP); emits capability edges with UNTRUSTED R/RW hints.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgServers, Type: sdk.FieldString, Description: "inline JSON array of MCP server specs to introspect"},
			{Key: cfgConfigPath, Type: sdk.FieldString, Description: "path to a Claude Code .mcp.json whose mcpServers are introspected"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-server introspection timeout"},
			{Key: cfgRegistryEnabled, Type: sdk.FieldBool, Default: "false", Description: "opt in to read-only MCP Registry provenance enrichment (PREVIEW; self-verify)"},
			{Key: cfgRegistryURL, Type: sdk.FieldString, Default: defaultRegistryURL, Description: "MCP Registry base URL (read-only); setting it implies registry_enabled"},
			{Key: cfgRegistrySync, Type: sdk.FieldBool, Default: "false", Description: "enumerate owned namespaces in the public registry to detect yanked/unmanaged publications (supply-chain, MCP04)"},
			{Key: cfgOwnedNamespaces, Type: sdk.FieldString, Description: "reverse-DNS namespaces the org owns (JSON array or comma list); clears internal servers from shadow flagging"},
			{Key: cfgInternalServers, Type: sdk.FieldString, Description: "JSON array of approved internal MCP servers ({name, registry_name, version}) for reconciliation + version-drift detection"},
			{Key: cfgPostureScan, Type: sdk.FieldBool, Default: "true", Description: "scan introspected catalog metadata for tool-poisoning/injection/homoglyph/over-broad-scope and grade posture (OWASP MCP Top-10)"},
			{Key: cfgNextRevisionPreview, Type: sdk.FieldBool, Default: "true", Description: "auto-negotiate the MCP 2026-07-28 frozen-RC stateless mode by default; per-server next_revision true forces RC-only, false forces 2025-11-25 legacy"},
			{Key: cfgDeprecationFeed, Type: sdk.FieldBool, Default: "false", Description: "fetch the official MCP deprecated-features registry each pass to detect rule drift (the compiled deprecation rules never depend on it)"},
			{Key: cfgDeprecationFeedURL, Type: sdk.FieldString, Default: defaultDeprecationFeedURL, Description: "deprecated-features registry source (raw MDX of /specification/draft/deprecated); setting it implies deprecation_feed"},
			{Key: cfgFederatedRegistries, Type: sdk.FieldString, Description: "JSON array of federated MCP registries ({name, url, allowlist}) implementing the pinned /v0.1 registry OpenAPI (GitHub BYO org/enterprise registries, Azure API Center, private subregistries)"},
			{Key: cfgDockerCatalog, Type: sdk.FieldBool, Default: "false", Description: "consult the Docker MCP Catalog feed: image digest-pin drift + Docker-built (signed) vs community (unattested) provenance per server"},
			{Key: cfgDockerCatalogURL, Type: sdk.FieldString, Default: defaultDockerCatalogURL, Description: "Docker MCP Catalog feed URL (catalog.yaml v2); setting it implies docker_catalog"},
		},
	}
}

// loadConfig resolves the server list from the inline "servers" JSON and/or the
// ".mcp.json" at config_path, plus the per-server timeout. It fails if neither
// source yields a server (nothing to introspect).
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{timeout: cfg.GetDuration(cfgTimeout, defaultTimeout)}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}

	// AIP-03: registry enrichment is opt-in. Setting a registry_url implies enabled;
	// registry_enabled=true alone uses the official preview registry URL.:
	// registry_sync (enumerate owned namespaces) also implies the registry client.
	c.registryURL = cfg.Get(cfgRegistryURL)
	c.registrySync = cfg.GetBool(cfgRegistrySync, false)
	c.registryEnabled = cfg.GetBool(cfgRegistryEnabled, false) || c.registryURL != "" || c.registrySync
	if c.registryEnabled && c.registryURL == "" {
		c.registryURL = defaultRegistryURL
	}

	// posture scanning is on by default (it needs no network — it analyzes the
	// already-introspected catalog); the internal registry is operator config.
	c.postureScan = cfg.GetBool(cfgPostureScan, true)
	// the 2026-07-28 frozen-RC stateless mode is the default and
	// auto-negotiates down to 2025-11-25 when a server proves it is legacy.
	c.nextRevisionPreview = cfg.GetBool(cfgNextRevisionPreview, true)
	// deprecation-feed drift detection + federation, all opt-in. Setting a
	// URL implies the corresponding flag (the registry_url precedent).
	c.deprecationFeedURL = cfg.Get(cfgDeprecationFeedURL)
	c.deprecationFeed = cfg.GetBool(cfgDeprecationFeed, false) || c.deprecationFeedURL != ""
	if c.deprecationFeed && c.deprecationFeedURL == "" {
		c.deprecationFeedURL = defaultDeprecationFeedURL
	}
	c.dockerCatalogURL = cfg.Get(cfgDockerCatalogURL)
	c.dockerCatalog = cfg.GetBool(cfgDockerCatalog, false) || c.dockerCatalogURL != ""
	if c.dockerCatalog && c.dockerCatalogURL == "" {
		c.dockerCatalogURL = defaultDockerCatalogURL
	}
	federated, err := parseFederatedRegistries(cfg.Get(cfgFederatedRegistries))
	if err != nil {
		return config{}, err
	}
	c.federatedRegistries = federated
	c.ownedNamespaces = parseNamespaceList(cfg.Get(cfgOwnedNamespaces))
	internalServers, err := parseInternalEntries(cfg.Get(cfgInternalServers))
	if err != nil {
		return config{}, err
	}
	c.internalServers = internalServers

	if raw := cfg.Get(cfgServers); raw != "" {
		specs, err := parseInlineServers(raw)
		if err != nil {
			return config{}, err
		}
		c.servers = append(c.servers, specs...)
	}
	if path := cfg.Get(cfgConfigPath); path != "" {
		specs, err := parseMCPConfigFile(path)
		if err != nil {
			return config{}, err
		}
		c.servers = append(c.servers, specs...)
	}
	if len(c.servers) == 0 {
		return config{}, fmt.Errorf("mcp: no servers configured (set %q or %q)", cfgServers, cfgConfigPath)
	}
	for i := range c.servers {
		if c.servers[i].Name == "" {
			return config{}, fmt.Errorf("mcp: server #%d has no name", i)
		}
	}
	return c, nil
}

// parseInlineServers decodes the inline "servers" JSON array.
func parseInlineServers(raw string) ([]serverSpec, error) {
	var specs []serverSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("mcp: parse %q: %w", cfgServers, err)
	}
	return specs, nil
}

// mcpConfigFile is the Claude Code .mcp.json shape: a map of server name to its
// stdio/http spec, where "type" selects the transport (absent ⇒ stdio).
type mcpConfigFile struct {
	MCPServers map[string]struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Auth    *serverAuth       `json:"auth"`
		// NextRevision is the per-server tri-state override (an Olivares
		// extension to the .mcp.json shape; Claude Code ignores it).
		NextRevision *bool `json:"next_revision"`
		// UITemplates is the pre-declared MCP App inventory (an Olivares
		// extension to the .mcp.json shape; Claude Code ignores it).
		UITemplates []string `json:"ui_templates"`
	} `json:"mcpServers"`
}

// parseMCPConfigFile reads a .mcp.json and converts its mcpServers map into specs,
// sorted by name for deterministic ordering.
func parseMCPConfigFile(path string) ([]serverSpec, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-provided config path, read-only
	if err != nil {
		return nil, fmt.Errorf("mcp: read %s: %w", path, err)
	}
	var file mcpConfigFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("mcp: parse %s: %w", path, err)
	}
	names := make([]string, 0, len(file.MCPServers))
	for name := range file.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	specs := make([]serverSpec, 0, len(names))
	for _, name := range names {
		s := file.MCPServers[name]
		specs = append(specs, serverSpec{
			Name:         name,
			Transport:    s.Type,
			Command:      s.Command,
			Args:         s.Args,
			Env:          s.Env,
			URL:          s.URL,
			Headers:      s.Headers,
			Auth:         s.Auth,
			NextRevision: s.NextRevision,
			UITemplates:  s.UITemplates,
		})
	}
	return specs, nil
}
