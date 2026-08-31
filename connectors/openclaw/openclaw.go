// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openclaw

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const Name = "olivares.openclaw"

const (
	defaultAgentRef = "openclaw"
)

type Source struct {
	agentRef   string
	stateDir   string
	configPath string
	// systemdRoots overrides the directories scanned for OpenClaw service units
	// (test seam). Empty uses the standard system + per-user unit directories.
	systemdRoots []string
	// skillScan carries the signed deny-list + approved baseline + allowlist the
	// skill supply-chain scanner enforces. Zero value = posture-only scanning.
	skillScan skillScanPolicy
	now       func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source {
	return &Source{
		agentRef: defaultAgentRef,
	}
}

func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.3.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "OpenClaw (governance)",
		Description: "Governs OpenClaw v2026.6.11-era local installs from ~/.openclaw/openclaw.json JSON5 (read-only): " +
			"discovers env/default/legacy/profile installs (plus a systemd service signal for always-on hosts), " +
			"resolves confined $include files and ${VAR} references, " +
			"evaluates gateway/channel/tool/sandbox/skill/plugin/model/MCP posture per agent, emits channel/skill/model/MCP edges, " +
			"inventory and config-only diagnostics coverage. CostType=\"openclaw\" for OpenClaw metering. " +
			"HONEST BLIND SPOTS: no inline PEP hook verified upstream, config-only coverage, and upstream assumes a single trusted operator.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable origin reference for the default OpenClaw install. Profile installs are suffixed."},
			{Key: "state_dir", Type: sdk.FieldString, Default: defaultStateDir, Description: "OpenClaw state directory override. Default discovery uses $OPENCLAW_HOME/$OPENCLAW_STATE_DIR, ~/.openclaw, legacy ~/.clawdbot and ~/.openclaw-* profiles."},
			{Key: "config_path", Type: sdk.FieldString, Default: defaultConfigPath, Description: "Optional operator-supplied openclaw.json JSON5 path. When set, discovery is limited to this file."},
			{Key: "skill_denylist_path", Type: sdk.FieldString, Description: "Optional path to a signed threatfeed rule-pack (ClawHavoc-style deny-list IOCs / blocked MCP / attack patterns). Its detached signature is <path>.sig unless skill_denylist_sig is set. Deny-closed: a configured-but-unverifiable pack is reported loudly, never treated as clean."},
			{Key: "skill_denylist_sig", Type: sdk.FieldString, Description: "Optional signature path for the deny-list rule-pack (default: <skill_denylist_path>.sig)."},
			{Key: "skill_denylist_keys", Type: sdk.FieldString, Description: "Comma-separated base64 Ed25519 trusted publisher keys for the deny-list rule-pack (required to enable the deny-list)."},
			{Key: "skill_baseline_path", Type: sdk.FieldString, Description: "Optional path to a JSON {skill-name: approved-sha256} map. A skill whose current content digest differs from its approved digest is reported as drift (changed after approval)."},
			{Key: "authorized_skills", Type: sdk.FieldString, Description: "Optional comma-separated allowlist of authorized skill names. A discovered skill outside the list is reported as unauthorized. Baseline and allowlist are authored under governance dual-control; the connector consumes them read-only."},
		},
	}
}

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimSpace(cfg.Get("agent_ref")); v != "" {
		s.agentRef = v
	}
	if v := strings.TrimSpace(cfg.Get("state_dir")); v != "" {
		s.stateDir = v
	}
	if v := strings.TrimSpace(cfg.Get("config_path")); v != "" {
		s.configPath = v
	}
	s.skillScan = buildSkillScanPolicy(cfg)
	return nil
}

func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	installs := s.discoverInstalls()
	if len(installs) == 0 {
		return nil
	}
	for _, inst := range installs {
		cfg := s.readInstall(inst)
		findings := s.findings(cfg)
		sort.SliceStable(findings, func(i, j int) bool {
			return findingSortKey(findings[i]) < findingSortKey(findings[j])
		})
		for _, f := range findings {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
		edges := s.permittedEdges(cfg)
		sort.SliceStable(edges, func(i, j int) bool {
			if edges[i].ResourceKind == edges[j].ResourceKind {
				if edges[i].ResourceRef == edges[j].ResourceRef {
					return edges[i].OriginRef < edges[j].OriginRef
				}
				return edges[i].ResourceRef < edges[j].ResourceRef
			}
			return edges[i].ResourceKind < edges[j].ResourceKind
		})
		for _, e := range edges {
			if err := sink.Emit(ctx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

var (
	_ model.Observation = model.EdgeObservation{}
	_ model.Observation = model.FindingReport{}
)
