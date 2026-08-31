// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package geminicli is the Olivares AI connector for Google's Gemini CLI — the
// open-source terminal coding agent (github.com/google-gemini/gemini-cli) — read through
// its LOCAL layered settings.json files. It is the GOVERN half of
// agent-surface parity for Gemini CLI; the OBSERVE half (live token usage, sessions, tool
// calls) rides the existing OpenTelemetry gen_ai.* ingest, because the Gemini CLI
// emits the vendor-neutral gen_ai.* profile (verified) — so no bespoke telemetry parser is
// needed, and this connector governs exactly what OTel cannot: the on-disk enforced policy.
//
// IMPORTANT — this is NOT the Gemini API. The connectors/gemini connector reads the Gemini
// API (generativelanguage.googleapis.com, model catalog + cost). THIS connector reads the
// gemini-cli AGENT's configuration; the two never overlap.
//
// What it does (read-only, local-filesystem, minimal-data — docs/SECURITY-HARDENING.md-3):
//   - reads the settings.json precedence layers (system-defaults < user < workspace <
//     SYSTEM override — VERIFIED merge order) and resolves the EFFECTIVE value of each
//     governable control plus the scope that won it;
//   - emits PERMITTED capability EdgeObservations (Source=config) for each configured MCP
//     server and each allowed tool — the agent's declared, wired reach;
//   - emits posture FindingReports for governance gaps (no admin/system settings; YOLO
//     bypass not disabled; auto-approve edits; telemetry off; prompt logging on; wide tool
//     allowlist; any-MCP-server allowed; auth type not pinned; anonymized usage stats on);
//   - emits one inventory FindingReport summarizing the effective config, and one coverage
//     FindingReport stating whether live activity flows to the control-plane OTel collector
//     (the gen_ai.* ingest) given the telemetry settings;
//   - reports the PRESENCE of the admin Policy Engine .toml files (a real governance
//     signal) — parsing the per-rule TOML contents is a declared follow-up (no TOML
//     dependency is pulled into the connector tree to read a secondary surface).
//
// POLICY ENGINE MODEL (verified against gemini-cli source). The Gemini CLI uses a
// hybrid priority system: 5 named tiers — DEFAULT (1), EXTENSION (2), WORKSPACE (3),
// USER (4), ADMIN (5) — each with numeric priority 0–999 within the tier. The effective
// priority of a rule is tier + (priority/1000). Conflict resolution is highest-priority
// first-match (sorted descending). Safety checkers can still escalate (ALLOW→ASK_USER or
// force DENY). Admin policy files live on disk (e.g. /etc/gemini-cli/policies/*.toml on
// Linux) and are NOT fetched remotely — they are the LOCAL enforcement arm. This is
// distinct from the admin{} settings block below.
//
// SETTINGS PRECEDENCE (lowest → highest): hardcoded defaults, system-defaults.json,
// user ~/.gemini/settings.json, workspace .gemini/settings.json, system override
// /etc/gemini-cli/settings.json, environment variables, CLI arguments. The system
// override tier (ADMIN scope in this connector) is the enforced ceiling: it wins over
// user/workspace settings and is the signal an enterprise uses to lock down the agent.
//
// Honest blind spot (VERIFIED): the gemini-cli admin{} settings block (secureModeEnabled,
// admin.mcp/extensions/skills) is fetched REMOTELY by the CLI and "file-based admin
// settings are ignored" — it is NOT on disk, so a local connector cannot observe what an
// enterprise enforces through that remote channel. The connector documents this rather than
// fabricating coverage. It imports only the SDK, never the engine.
package geminicli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.gemini-cli"

// maxSettingsBytes caps a settings.json read so a corrupt/hostile file cannot exhaust
// memory; a real config is small.
const maxSettingsBytes = 1 << 20

// Default well-known SYSTEM (admin) paths on Linux (VERIFIED getSystemSettingsPath()). The
// user/workspace layers have no safe default from inside the control plane (the agent
// user's HOME differs), so the operator points at them explicitly.
const (
	defaultSystemSettingsPath = "/etc/gemini-cli/settings.json"
	defaultSystemDefaultsPath = "/etc/gemini-cli/system-defaults.json"
	defaultAdminPolicyDir     = "/etc/gemini-cli/policies"
	defaultAgentRef           = "gemini-cli"
)

// Source is the Gemini CLI governance source connector. It is a BATCH source: Gather reads
// the live config layers once, emits the governance observations, and returns nil; the
// engine re-polls on the operator's schedule (the connector owns no ticker).
type Source struct {
	agentRef string

	systemPath    string
	defaultsPath  string
	userPath      string
	workspacePath string
	adminPolicy   string
	userPolicy    string

	now func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Gemini CLI connector with default configuration.
func New() *Source {
	return &Source{
		agentRef:     defaultAgentRef,
		systemPath:   defaultSystemSettingsPath,
		defaultsPath: defaultSystemDefaultsPath,
		adminPolicy:  defaultAdminPolicyDir,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Gemini CLI (governance)",
		Description: "Governs Google's Gemini CLI agent from its local settings.json layers (read-only): PERMITTED MCP/tool capabilities + posture findings on enforcement gaps. Live usage/cost is observed via the OTel gen_ai.* ingest. Not the Gemini API.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable origin reference for this Gemini CLI install's permitted-capability edges (e.g. a host/fleet name)."},
			{Key: "system_settings_path", Type: sdk.FieldString, Default: defaultSystemSettingsPath, Description: "Admin/system settings.json (the enforced override tier; final say in the merge). Linux default shown."},
			{Key: "system_defaults_path", Type: sdk.FieldString, Default: defaultSystemDefaultsPath, Description: "system-defaults.json (lowest non-builtin precedence)."},
			{Key: "user_settings_path", Type: sdk.FieldString, Description: "The agent user's ~/.gemini/settings.json (no default — the control plane cannot resolve the agent user's HOME)."},
			{Key: "workspace_settings_path", Type: sdk.FieldString, Description: "A project's ./.gemini/settings.json (optional; dropped by the CLI for untrusted folders)."},
			{Key: "admin_policy_dir", Type: sdk.FieldString, Default: defaultAdminPolicyDir, Description: "Admin Policy Engine directory; the connector reports presence of *.toml policy files (rule-content parsing is a declared follow-up)."},
			{Key: "user_policy_dir", Type: sdk.FieldString, Description: "User Policy Engine directory (~/.gemini/policies; optional)."},
		},
	}
}

// Open resolves configuration. A bad path is not validated here (a missing file is a
// finding, not an error); only obviously-empty overrides fall back to the defaults.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimSpace(cfg.Get("agent_ref")); v != "" {
		s.agentRef = v
	}
	if v := strings.TrimSpace(cfg.Get("system_settings_path")); v != "" {
		s.systemPath = v
	}
	if v := strings.TrimSpace(cfg.Get("system_defaults_path")); v != "" {
		s.defaultsPath = v
	}
	s.userPath = strings.TrimSpace(cfg.Get("user_settings_path"))
	s.workspacePath = strings.TrimSpace(cfg.Get("workspace_settings_path"))
	if v := strings.TrimSpace(cfg.Get("admin_policy_dir")); v != "" {
		s.adminPolicy = v
	}
	s.userPolicy = strings.TrimSpace(cfg.Get("user_policy_dir"))
	return nil
}

// Gather reads the configured settings layers, resolves the effective governance posture,
// and emits the observations. A missing settings file is a layer that is absent (and an
// absent SYSTEM layer is itself a finding) — NOT a Gather error; only a genuine,
// possibly-transient read fault (permission denied, I/O) is returned so the engine retries.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	ls, err := s.readLayers()
	if err != nil {
		return err
	}

	// An invalid (present-but-malformed) layer is a posture finding, not a hard failure.
	for _, l := range ls {
		if l.present && l.invalid {
			if eerr := sink.Emit(ctx, s.invalidLayerFinding(l.scope)); eerr != nil {
				return eerr
			}
		}
	}

	for _, f := range s.postureFindings(ls) {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	for _, e := range s.permittedEdges(ls) {
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}
	if err := sink.Emit(ctx, s.inventoryFinding(ls)); err != nil {
		return err
	}
	if err := sink.Emit(ctx, s.coverageFinding(ls)); err != nil {
		return err
	}
	if f, ok := s.policyPresenceFinding(); ok {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources (none held).
func (s *Source) Close(context.Context) error { return nil }

// readLayers reads every configured settings layer. A configured-but-missing file yields a
// present=false layer; a present-but-malformed file yields present=true, invalid=true; a
// real read fault (not not-exist) aborts the run for retry.
func (s *Source) readLayers() (layers, error) {
	specs := []struct {
		scope string
		path  string
	}{
		{scopeSystem, s.systemPath},
		{scopeWorkspace, s.workspacePath},
		{scopeUser, s.userPath},
		{scopeSystemDefaults, s.defaultsPath},
	}
	var ls layers
	for _, sp := range specs {
		if sp.path == "" {
			continue // an unconfigured layer is simply not consulted
		}
		l := layer{scope: sp.scope}
		data, err := readFileCapped(sp.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			l.present = false
		case err != nil:
			return nil, err // transient/permission — let the engine retry
		default:
			l.present = true
			parsed, perr := parseSettings(data)
			if perr != nil {
				l.invalid = true
			} else {
				l.s = parsed
			}
		}
		ls = append(ls, l)
	}
	return ls, nil
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// readFileCapped reads up to maxSettingsBytes, preserving os.ErrNotExist so the caller can
// treat absence as a finding rather than a fault.
func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied settings path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSettingsBytes))
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// compile-time assurance the emitted observation types are the SDK's.
var (
	_ model.Observation = model.EdgeObservation{}
	_ model.Observation = model.FindingReport{}
)
