// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudeconfig is the CLA-14 static-config DISCOVERY feeder: a read-first
// source that reads a Claude config tree and emits one EdgeObservation per capability
// the config DECLARES — a subagent, a Skill, a plugin or an output-style — so the
// capability graph shows DECLARED capabilities, not only those observed EXECUTING on
// the bus at runtime (today only MCP edge.observed populates the graph).
//
// Why a sibling package (not connectors/claude): the runtime claude connector carries
// the OTLP/gRPC dependency tree and runs OUT-OF-PROCESS to keep that weight out of the
// core. This feeder is pure filesystem + YAML/JSON parsing, so — like claude-api,
// claude-wif and claude-compliance — it is its own dependency-light Apache package and
// runs IN-PROCESS.
//
// READ-FIRST and MINIMAL-DATA (docs/SECURITY-HARDENING.md, §3): it READS files, it NEVER executes
// anything, and it EMITS structural metadata only — the capability NAME and its
// surface kind. It never emits a subagent's prompt body, a Skill's instructions, a
// plugin's code, a description, a tool argument or any secret; a secret-shaped name is
// dropped. The skill posture scanner (skillscan.go, default on) additionally
// READS each SKILL.md's content to grade it — but its output keeps the same bar:
// sanitized titles + hashed details, never the text itself. The on-disk conventions
// and frontmatter fields below were verified verbatim against code.claude.com
// (sub-agents, skills, plugins, output-styles), jun-2026.
//
// The auth-posture scanner (authposture.go, default on) adds the one host-level
// signal: which CREDENTIAL MODE this host's Claude Code uses (subscription OAuth, a
// setup-token, apiKeyHelper, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_API_KEY or a cloud provider)
// by the documented precedence — so the plane can assert the fleet is governed by the only
// lawful path for a subscription host (in-process OBSERVATION; intermediating a
// subscription is forbidden by Anthropic —). It reads PRESENCE/FORM only (env
// set/unset, the .credentials.json mode bits, the apiKeyHelper key's presence), NEVER a
// credential value. The env-derived modes reflect the FEEDER'S process environment — a host
// fact only under co-deployment; the finding states that observation boundary
// explicitly rather than over-claiming, and the on-disk signals are host-true regardless.
package claudeconfig

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier (the runtime registry key).
const Name = "olivares.connector.claude-config"

// Config keys.
const (
	cfgRoot  = "root"  // the directory to scan (a project root or a .claude directory)
	cfgLabel = "label" // the workspace label stamped as the declaring origin ref
	// SKILL.md posture/provenance scanning (skillscan.go).
	cfgSkillScan         = "skill_scan"         // run the skill posture scanner (default on)
	cfgKnownMarketplaces = "known_marketplaces" // operator allowlist of marketplace names (JSON array or comma list); unset = inventory-only
	// per-skill-name authorization (two-tier: explicit name > marketplace provenance).
	cfgAuthorizedSkills = "authorized_skills" // operator allowlist of authorized skill NAMES (JSON array or comma list); unset = no per-name restriction. A skill not matched by name falls back to marketplace provenance (known_marketplaces); both unset = inventory-only.
	// host credential-mode auth-posture (authposture.go).
	cfgAuthPosture       = "auth_posture"        // observe the host's effective Claude Code credential mode (default on)
	cfgExpectedAuthModes = "expected_auth_modes" // operator allowlist of permitted credential modes (JSON array or comma list); unset = inventory-only
)

// Declared-capability ResourceKinds this feeder emits (the EdgeObservation.
// ResourceKind). The capabilities reactor maps each to a capability kind with
// signal_source=config (modules/capabilities/reactor.go declaredCapabilityKind).
const (
	resSubagent     = "config.subagent"
	resSubagentTool = "config.subagent_tool"
	resSkill        = "config.skill"
	resPlugin       = "config.plugin"
	resOutputStyle  = "config.output_style"
	// (2.1.17x parity): a hook EVENT declared in settings.json — arbitrary
	// command/prompt execution wired into the agent loop, the highest-value
	// declared capability to surface (the 2.1.x hook schema spans command hooks,
	// prompt hooks with continueOnBlock, and the display-only MessageDisplay
	// event added in 2.1.152 — VERIFIED 2026-06-10, docs.claude.com hooks page).
	// The ref is the EVENT NAME only; the command/prompt strings are deliberately
	// never emitted (they can embed secrets/paths — minimal-data, docs/SECURITY-HARDENING.md).
	resHook = "config.hook"
	// a project-declared MCP server (.mcp.json) — the declared surface the
	// 2.1.17x managed allowedMcpServers/deniedMcpServers predicates govern;
	// surfacing it lets the plane diff DECLARED vs ALLOWED. Name only, never the
	// command/args/url/env of the entry.
	resMCPServer = "config.mcp_server"
)

// originWorkspace is the EdgeObservation.OriginKind for a config-declared capability:
// the workspace/project scope that declares it.
const originWorkspace = "workspace"

// maxFrontmatterBytes bounds how much of a file is read to extract its frontmatter —
// the frontmatter is at the very top and the body is never needed (minimal-data + a
// guard against a pathologically large file).
const maxFrontmatterBytes = 256 * 1024

// Feeder discovers the capabilities a Claude config tree declares.
type Feeder struct {
	root  string
	label string
	// skillScan (default on) runs the SKILL.md posture/provenance scanner
	// (skillscan.go) over every discovered skill — no network, read-only.
	skillScan bool
	// knownMarketplaces is the operator allowlist of plugin-marketplace NAMES
	// (B1 provenance): nil = no allowlist configured (provenance is
	// inventoried, never judged); non-nil = a marketplace-delivered skill outside
	// the set is a finding.
	knownMarketplaces map[string]struct{}
	// authorizedSkills is the operator allowlist of authorized skill NAMES
	//: nil = no per-name restriction. Resolution is two-tier: explicit
	// name match (highest priority) > marketplace provenance (primary fallback).
	// A skill not matched by either tier when a policy IS configured is a
	// supply-chain finding. Olivares INVENTORIES and SIGNALS; the host runtime
	// (Claude Code) is where any blocking enforcement happens.
	authorizedSkills map[string]struct{}
	now              func() time.Time
	// Host credential-mode auth-posture (authposture.go; default on). It observes the
	// EFFECTIVE Claude Code credential mode of the host this feeder runs on — env presence
	// (process environment) + the config dir's .credentials.json mode / apiKeyHelper — and
	// emits a minimal-data finding (never a credential value). The env/home/goos sources are
	// injectable for tests; nil/"" resolve to the os defaults at call time.
	authPosture       bool
	expectedAuthModes map[string]struct{}
	lookupEnv         func(string) (string, bool)
	homeDir           func() (string, error)
	goos              string
}

// Compile-time proof the feeder satisfies the connector SDK source seam.
var _ sdk.SourceConnector = (*Feeder)(nil)

// New returns a Claude static-config discovery feeder.
func New() *Feeder { return &Feeder{} }

// Descriptor returns the connector's self-description and declared configuration.
func (f *Feeder) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude config discovery",
		Description: "Discovers the subagents, Skills, plugins and output-styles a Claude config tree DECLARES (read-only, metadata only) so the capability graph distinguishes declared capabilities from those observed executing. With skill_scan on (default) it also grades each SKILL.md's posture (spec conformance, allowed-tools breadth, injection/hidden-Unicode/secret shapes, load-time execution, marketplace provenance, fleet authorization) as minimal-data findings. Each subagent's allowed-tools list is emitted as distinct capability-layer edges.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgRoot, Type: sdk.FieldString, Description: "Directory to scan for Claude config (a project root, or a .claude directory). READ-ONLY: nothing is executed; only capability names are read."},
			{Key: cfgLabel, Type: sdk.FieldString, Description: "Workspace label stamped as the declaring origin (defaults to the root directory name). The filesystem path is never emitted."},
			{Key: cfgSkillScan, Type: sdk.FieldBool, Default: "true", Description: "scan each discovered SKILL.md's content for posture (agentskills.io conformance, allowed-tools breadth, injection/hidden-Unicode/secrets, load-time execution) — findings are sanitized titles + hashed details, never skill text"},
			{Key: cfgKnownMarketplaces, Type: sdk.FieldString, Description: "operator allowlist of plugin-marketplace NAMES (JSON array or comma list); a marketplace-delivered skill outside the set is a finding. Unset = provenance is inventoried only"},
			{Key: cfgAuthorizedSkills, Type: sdk.FieldString, Description: "operator allowlist of authorized skill NAMES (JSON array or comma list); a skill not matched by name falls back to marketplace provenance (known_marketplaces); both unset = inventory-only. Olivares inventories and signals; enforcement is on the host (Claude Code)"},
			{Key: cfgAuthPosture, Type: sdk.FieldBool, Default: "true", Description: "observe the EFFECTIVE Claude Code credential mode in use on this host (subscription/oauth-token/api-key-helper/auth-token/api-key/cloud-provider) by the documented precedence — from the feeder's process environment + the resolved config dir, host-accurate under co-deployment. PRESENCE/FORM only, never a credential value; honors CLAUDE_CONFIG_DIR"},
			{Key: cfgExpectedAuthModes, Type: sdk.FieldString, Description: "operator allowlist of permitted credential modes (JSON array or comma list: subscription|oauth_token|api_key_helper|auth_token|api_key|cloud_provider); an effective mode outside the set drifts Medium. Unset = inventory-only (Info)"},
		},
	}
}

// Open validates the configured root and resolves the workspace label. It never
// reaches the filesystem beyond a stat — discovery happens in Gather.
func (f *Feeder) Open(_ context.Context, cfg sdk.Config) error {
	f.root = strings.TrimSpace(cfg.Get(cfgRoot))
	if f.root == "" {
		return fmt.Errorf("claude-config: %q is required (the directory to scan)", cfgRoot)
	}
	f.label = strings.TrimSpace(cfg.Get(cfgLabel))
	if f.label == "" {
		f.label = filepath.Base(filepath.Clean(f.root))
	}
	f.skillScan = cfg.GetBool(cfgSkillScan, true)
	f.knownMarketplaces = parseNameList(cfg.Get(cfgKnownMarketplaces))
	f.authorizedSkills = parseNameList(cfg.Get(cfgAuthorizedSkills))
	f.authPosture = cfg.GetBool(cfgAuthPosture, true)
	f.expectedAuthModes = parseNameList(cfg.Get(cfgExpectedAuthModes))
	for m := range f.expectedAuthModes {
		if !knownAuthMode(m) {
			return fmt.Errorf("claude-config: %q lists unknown credential mode %q (expected: cloud_provider|auth_token|api_key|api_key_helper|oauth_token|subscription)", cfgExpectedAuthModes, m)
		}
	}
	return nil
}

// parseNameList parses a JSON array or comma list of names into a set. An empty
// input yields nil — "no allowlist configured", distinct from an empty allowlist
// (which an operator expresses as "[]" → an empty non-nil set = nothing known). A
// MALFORMED JSON array (e.g. a missing bracket) also yields nil rather than a non-nil
// EMPTY set: a parse error is "no allowlist configured / typo", never a silent deny-all
// allowlist that would flag every host/skill — the honest, deny-OPEN posture for an
// unparseable expectation (a deliberate "[]" still yields the empty non-nil lockdown set).
func parseNameList(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var names []string
	if strings.HasPrefix(raw, "[") {
		if yaml.Unmarshal([]byte(raw), &names) != nil {
			return nil
		}
	} else {
		names = strings.Split(raw, ",")
	}
	out := map[string]struct{}{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = struct{}{}
		}
	}
	return out
}

// Close releases resources (none).
func (f *Feeder) Close(context.Context) error { return nil }

// clock is the feeder's time source (injectable for tests).
func (f *Feeder) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

// Gather walks the config tree once (a batch source: it returns nil when drained) and
// emits a declared-capability edge per surface. It is IDEMPOTENT across re-polls: the
// capabilities reactor upserts by (origin, capability), so re-emitting the same config
// merges (bumps last-seen/occurrence), never duplicates.
func (f *Feeder) Gather(ctx context.Context, sink sdk.Sink) error {
	if info, err := os.Stat(f.root); err != nil || !info.IsDir() {
		return fmt.Errorf("claude-config: root %q is not a readable directory", f.label)
	}
	at := f.clock().UTC()

	// The standard .claude surfaces. The operator may point root at a project (scan
	// root/.claude/...) or directly at a .claude directory (scan root/...).
	for _, base := range f.claudeBases() {
		if err := f.scanSurfaces(ctx, sink, base, at); err != nil {
			return err
		}
	}
	// Project-declared MCP servers (.mcp.json at the scan root).
	if err := f.emitMCPServers(ctx, sink, at); err != nil {
		return err
	}
	// Plugins + marketplaces (anywhere under root), and each plugin's bundled surfaces.
	if err := f.emitPlugins(ctx, sink, at); err != nil {
		return err
	}
	// the host's EFFECTIVE Claude Code credential mode (authposture.go), emitted once
	// per Gather. Idempotent across re-polls (the finding reactor upserts by DetailHash).
	if f.authPosture {
		return f.emitAuthPosture(ctx, sink, at)
	}
	return nil
}

// claudeBases resolves the base directory(ies) holding the standard config subdirs.
func (f *Feeder) claudeBases() []string {
	var bases []string
	if dot := filepath.Join(f.root, ".claude"); isDir(dot) {
		bases = append(bases, dot)
	}
	if filepath.Base(filepath.Clean(f.root)) == ".claude" {
		bases = append(bases, f.root)
	}
	if len(bases) == 0 {
		// Neither a project-with-.claude nor a .claude dir: scan the root directly (a
		// bare config directory, e.g. a managed-settings tree).
		bases = append(bases, f.root)
	}
	return bases
}

// scanSurfaces emits the four standard surfaces found directly under one base dir.
func (f *Feeder) scanSurfaces(ctx context.Context, sink sdk.Sink, base string, at time.Time) error {
	if err := f.emitSubagents(ctx, sink, filepath.Join(base, "agents"), at); err != nil {
		return err
	}
	if err := f.emitSkills(ctx, sink, filepath.Join(base, "skills"), skillProvenance{}, at); err != nil {
		return err
	}
	// Legacy custom commands are merged into Skills (verified vs code.claude.com): a
	// flat .claude/commands/*.md declares a /command, i.e. a skill.
	if err := f.emitFlatMarkdown(ctx, sink, filepath.Join(base, "commands"), resSkill, at); err != nil {
		return err
	}
	// Hooks declared in the base's settings files.
	if err := f.emitSettingsHooks(ctx, sink, base, at); err != nil {
		return err
	}
	return f.emitOutputStyles(ctx, sink, filepath.Join(base, "output-styles"), at)
}

// emitSettingsHooks emits one config.hook edge per hook EVENT declared with at
// least one entry in the base's settings.json / settings.local.json. The
// event-name KEY is the capability — the 2.1.x event set grows (MessageDisplay
// landed in 2.1.152), so no event allowlist is hardcoded. The hook entries
// themselves (commands, prompts, matchers) are NEVER read beyond presence: a
// command string can embed secrets/paths (minimal-data, docs/SECURITY-HARDENING.md). Both files
// may declare the same event; the capabilities reactor upserts by
// (origin, capability), so the double emission merges.
func (f *Feeder) emitSettingsHooks(ctx context.Context, sink sdk.Sink, base string, at time.Time) error {
	for _, file := range []string{"settings.json", "settings.local.json"} {
		path := filepath.Join(base, file)
		if !isFile(path) {
			continue
		}
		var settings struct {
			Hooks map[string][]any `yaml:"hooks"`
		}
		if !unmarshalBounded(path, &settings) {
			continue // tolerate a malformed settings file; never abort discovery
		}
		for event, entries := range settings.Hooks {
			if len(entries) == 0 {
				continue // an empty matcher list declares nothing
			}
			if err := f.emit(ctx, sink, resHook, event, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// emitMCPServers emits one config.mcp_server edge per server NAME declared in the
// scan root's .mcp.json. Only the name keys of the mcpServers object are
// read — never an entry's command/args/url/env (they routinely embed credentials).
func (f *Feeder) emitMCPServers(ctx context.Context, sink sdk.Sink, at time.Time) error {
	path := filepath.Join(f.root, ".mcp.json")
	if !isFile(path) {
		return nil
	}
	var manifest struct {
		MCPServers map[string]any `yaml:"mcpServers"`
	}
	if !unmarshalBounded(path, &manifest) {
		return nil
	}
	for name := range manifest.MCPServers {
		if err := f.emit(ctx, sink, resMCPServer, name, at); err != nil {
			return err
		}
	}
	return nil
}

// emitSubagents walks `agents/` recursively (verified: subfolders are allowed; identity
// is the frontmatter `name`, with the filename as a defensive fallback) and emits one
// config.subagent edge per markdown file.: also emits config.subagent_tool edges
// for each tool the agent's frontmatter declares in `allowed-tools`, making the
// subagent a DISTINCT capability layer (its own tool-list, not fused with the parent).
func (f *Feeder) emitSubagents(ctx context.Context, sink sdk.Sink, dir string, at time.Time) error {
	if !isDir(dir) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate an unreadable entry; never abort discovery on one file
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !isMarkdown(d.Name()) {
			return nil
		}
		name, tools := frontmatterNameAndTools(path)
		if name == "" {
			name = stem(d.Name())
		}
		if err := f.emit(ctx, sink, resSubagent, name, at); err != nil {
			return err
		}
		for _, tool := range tools {
			if err := f.emitSubagentTool(ctx, sink, name, tool, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// emitSubagentTool publishes one declared subagent-tool edge, namespaced as
// "agent:tool" so the access-map treats it as a DISTINCT layer under the agent.
func (f *Feeder) emitSubagentTool(ctx context.Context, sink sdk.Sink, agentName, toolName string, at time.Time) error {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || redact.ContainsSecret(toolName) {
		return nil
	}
	ref := agentName + ":" + toolName
	return sink.Emit(ctx, model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    f.label,
		ResourceKind: resSubagentTool,
		ResourceRef:  ref,
		Mode:         model.ModeUnknown,
		Source:       model.SignalConfig,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	})
}

// emitSkills emits one config.skill edge per `<skills>/<name>/SKILL.md`. The skill's
// identity is the DIRECTORY name (the command name; the frontmatter `name` is display
// only — verified vs code.claude.com). With skill_scan on it also runs the
// posture/provenance scanner over each skill (skillscan.go) — the edge declares the
// skill exists; the findings grade what it carries.
func (f *Feeder) emitSkills(ctx context.Context, sink sdk.Sink, dir string, prov skillProvenance, at time.Time) error {
	if !isDir(dir) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if isFile(filepath.Join(dir, e.Name(), "SKILL.md")) {
			if err := f.emit(ctx, sink, resSkill, e.Name(), at); err != nil {
				return err
			}
			if f.skillScan {
				if err := f.scanSkillDir(ctx, sink, filepath.Join(dir, e.Name()), e.Name(), prov, at); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// emitOutputStyles emits one config.output_style edge per `<output-styles>/*.md`. The
// identity is the frontmatter `name`, with the filename as a fallback (verified vs
// code.claude.com).
func (f *Feeder) emitOutputStyles(ctx context.Context, sink sdk.Sink, dir string, at time.Time) error {
	if !isDir(dir) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !isMarkdown(e.Name()) {
			continue
		}
		name := frontmatterName(filepath.Join(dir, e.Name()))
		if name == "" {
			name = stem(e.Name())
		}
		if err := f.emit(ctx, sink, resOutputStyle, name, at); err != nil {
			return err
		}
	}
	return nil
}

// emitFlatMarkdown emits one edge per flat `<dir>/*.md` file, identity = filename stem.
func (f *Feeder) emitFlatMarkdown(ctx context.Context, sink sdk.Sink, dir, resourceKind string, at time.Time) error {
	if !isDir(dir) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !isMarkdown(e.Name()) {
			continue
		}
		if err := f.emit(ctx, sink, resourceKind, stem(e.Name()), at); err != nil {
			return err
		}
	}
	return nil
}

// emitPlugins walks the tree for `.claude-plugin/plugin.json` (a plugin) and
// `.claude-plugin/marketplace.json` (a marketplace catalog), emitting one config.plugin
// edge per plugin, plus each plugin's bundled subagents/Skills/commands/output-styles.
// The walk COLLECTS first and emits after, so a plugin listed in a marketplace catalog
// carries that marketplace as its skills' PROVENANCE (B1) regardless of the
// order the walk found the two manifests in.
func (f *Feeder) emitPlugins(ctx context.Context, sink sdk.Sink, at time.Time) error {
	type pluginRef struct{ name, root string }
	var plugins []pluginRef
	var catalogPlugins []string
	// marketplaceOf maps a plugin NAME to the marketplace catalog that lists it.
	marketplaceOf := map[string]string{}

	walkErr := filepath.WalkDir(f.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != ".claude-plugin" {
			return nil
		}
		switch d.Name() {
		case "plugin.json":
			if name := jsonName(path); name != "" {
				// The plugin root is the parent of the .claude-plugin directory.
				plugins = append(plugins, pluginRef{name: name, root: filepath.Dir(filepath.Dir(path))})
			}
		case "marketplace.json":
			mk := jsonName(path)
			if mk == "" {
				// A catalog with no name still provides provenance — identified by
				// its directory (the marketplace checkout root).
				mk = filepath.Base(filepath.Dir(filepath.Dir(path)))
			}
			for _, name := range marketplacePluginNames(path) {
				catalogPlugins = append(catalogPlugins, name)
				if _, dup := marketplaceOf[name]; !dup {
					marketplaceOf[name] = mk
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	for _, name := range catalogPlugins {
		if err := f.emit(ctx, sink, resPlugin, name, at); err != nil {
			return err
		}
	}
	for _, p := range plugins {
		if err := f.emit(ctx, sink, resPlugin, p.name, at); err != nil {
			return err
		}
		prov := skillProvenance{plugin: p.name, marketplace: marketplaceOf[p.name]}
		if err := f.scanPluginSurfaces(ctx, sink, p.root, prov, at); err != nil {
			return err
		}
	}
	return nil
}

// scanPluginSurfaces emits a plugin's bundled subagents/Skills/commands/output-styles
// (verified plugin layout: these live at the plugin ROOT, never inside .claude-plugin).
func (f *Feeder) scanPluginSurfaces(ctx context.Context, sink sdk.Sink, pluginRoot string, prov skillProvenance, at time.Time) error {
	if err := f.emitSubagents(ctx, sink, filepath.Join(pluginRoot, "agents"), at); err != nil {
		return err
	}
	if err := f.emitSkills(ctx, sink, filepath.Join(pluginRoot, "skills"), prov, at); err != nil {
		return err
	}
	if err := f.emitFlatMarkdown(ctx, sink, filepath.Join(pluginRoot, "commands"), resSkill, at); err != nil {
		return err
	}
	return f.emitOutputStyles(ctx, sink, filepath.Join(pluginRoot, "output-styles"), at)
}

// emit publishes one declared-capability edge. The capability name is the only payload
// (structural metadata); a blank or secret-shaped name is dropped (minimal-data — never
// surface a secret a misauthored config leaked into a name).
func (f *Feeder) emit(ctx context.Context, sink sdk.Sink, resourceKind, name string, at time.Time) error {
	name = strings.TrimSpace(name)
	if name == "" || redact.ContainsSecret(name) {
		return nil
	}
	return sink.Emit(ctx, model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    f.label,
		ResourceKind: resourceKind,
		ResourceRef:  name,
		Mode:         model.ModeUnknown, // a declaration, not an access
		Source:       model.SignalConfig,
		Confidence:   model.ConfidenceAttributed, // reading the config IS the attribution
		ObservedAt:   at,
	})
}

// --- frontmatter / manifest parsing (metadata only) --------------------------------

// frontmatterName extracts the YAML frontmatter `name` from a markdown file, or "".
// It reads only a bounded prefix (the frontmatter is at the top; the body is never
// needed) and never returns or logs any other field.
func frontmatterName(path string) string {
	block, ok := readFrontmatter(path)
	if !ok {
		return ""
	}
	var fm struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(block, &fm); err != nil {
		return ""
	}
	return strings.TrimSpace(fm.Name)
}

// frontmatterNameAndTools extracts the `name` and `allowed-tools` from a markdown
// frontmatter. `allowed-tools` is a space-separated string (agentskills.io spec) or
// a YAML list (some agent definitions use this form). Only metadata is read — never
// the body, prompt, or any other field (minimal-data, docs/SECURITY-HARDENING.md).
func frontmatterNameAndTools(path string) (name string, tools []string) {
	block, ok := readFrontmatter(path)
	if !ok {
		return "", nil
	}
	var fm struct {
		Name         string `yaml:"name"`
		AllowedTools any    `yaml:"allowed-tools"`
	}
	if err := yaml.Unmarshal(block, &fm); err != nil {
		return "", nil
	}
	name = strings.TrimSpace(fm.Name)
	switch v := fm.AllowedTools.(type) {
	case string:
		for _, t := range strings.Fields(v) {
			tools = append(tools, t)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				if t := strings.TrimSpace(s); t != "" {
					tools = append(tools, t)
				}
			}
		}
	}
	return name, tools
}

// readFrontmatter returns the bytes between the opening and closing `---` fences of a
// markdown file's leading frontmatter block, or ok=false when there is none.
func readFrontmatter(path string) ([]byte, bool) {
	fh, err := os.Open(path) //nolint:gosec // operator-provided config path, read-only
	if err != nil {
		return nil, false
	}
	defer func() { _ = fh.Close() }()
	buf, err := io.ReadAll(io.LimitReader(fh, maxFrontmatterBytes))
	if err != nil {
		return nil, false
	}
	s := strings.TrimPrefix(string(buf), "\ufeff") // tolerate a UTF-8 BOM
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return nil, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[i], "\r")) == "---" {
			return []byte(strings.Join(lines[1:i], "\n")), true
		}
	}
	return nil, false
}

// jsonName reads a plugin.json manifest and returns its `name`, or "".
func jsonName(path string) string {
	var manifest struct {
		Name string `yaml:"name"`
	}
	if !unmarshalBounded(path, &manifest) {
		return ""
	}
	return strings.TrimSpace(manifest.Name)
}

// marketplacePluginNames reads a marketplace.json catalog and returns the names of the
// plugins it lists. An entry with no name is skipped.
func marketplacePluginNames(path string) []string {
	var catalog struct {
		Plugins []struct {
			Name string `yaml:"name"`
		} `yaml:"plugins"`
	}
	if !unmarshalBounded(path, &catalog) {
		return nil
	}
	out := make([]string, 0, len(catalog.Plugins))
	for _, p := range catalog.Plugins {
		if n := strings.TrimSpace(p.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// unmarshalBounded decodes a bounded prefix of a JSON file into v (yaml.v3 parses the
// JSON subset, so one parser covers frontmatter and manifests). It reads at most
// maxFrontmatterBytes — a manifest is small; this guards against a pathological file.
func unmarshalBounded(path string, v any) bool {
	fh, err := os.Open(path) //nolint:gosec // operator-provided config path, read-only
	if err != nil {
		return false
	}
	defer func() { _ = fh.Close() }()
	buf, err := io.ReadAll(io.LimitReader(fh, maxFrontmatterBytes))
	if err != nil {
		return false
	}
	return yaml.Unmarshal(buf, v) == nil
}

// --- small filesystem helpers ------------------------------------------------------

func isDir(path string) bool  { info, err := os.Stat(path); return err == nil && info.IsDir() }
func isFile(path string) bool { info, err := os.Stat(path); return err == nil && !info.IsDir() }

func isMarkdown(name string) bool { return strings.EqualFold(filepath.Ext(name), ".md") }

func stem(name string) string { return strings.TrimSuffix(name, filepath.Ext(name)) }

// skipDir reports whether a directory should not be descended into during the plugin
// walk: heavy/irrelevant trees that never hold Claude config. .claude and
// .claude-plugin are NOT skipped (they are exactly what we look for).
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".venv", "__pycache__", ".next", ".cache", ".idea":
		return true
	}
	return false
}
