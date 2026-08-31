// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hermes

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const Name = "olivares.hermes"

const (
	defaultAgentRef = "hermes"
)

type Source struct {
	agentRef      string
	hermesHome    string
	managedDir    string
	configPath    string
	meterProvider string
	now           func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source {
	return &Source{
		agentRef:      defaultAgentRef,
		meterProvider: "anthropic",
	}
}

func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.2.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Hermes (governance)",
		Description: "Governs Hermes Agent v0.18.0-era local installs from $HERMES_HOME/config.yaml (read-only): " +
			"discovers env/default/profile installs, applies managed-scope leaf overrides from /etc/hermes, " +
			"evaluates terminal/channel/skill/security/model/MCP posture, emits config-declared channel/skill/model/MCP edges, " +
			"inventory and Langfuse-plugin coverage. CostType=\"hermes\" for Hermes metering. " +
			"HONEST BLIND SPOTS: no inline PEP hook verified upstream, config-only coverage, and upstream states the OS is the only security boundary against an adversarial LLM.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable origin reference for the default Hermes install. Profile installs are suffixed."},
			{Key: "hermes_home", Type: sdk.FieldString, Default: defaultHermesHome, Description: "Hermes home directory override. Default discovery uses $HERMES_HOME, ~/.hermes and ~/.hermes/profiles/*."},
			{Key: "managed_dir", Type: sdk.FieldString, Default: defaultManagedDir, Description: "Hermes managed-scope directory override. Default discovery uses $HERMES_MANAGED_DIR, then /etc/hermes."},
			{Key: "config_path", Type: sdk.FieldString, Default: defaultConfigPath, Description: "Legacy alias for a specific Hermes config.yaml path. When set, discovery is limited to this file."},
		},
	}
}

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimSpace(cfg.Get("agent_ref")); v != "" {
		s.agentRef = v
	}
	if v := strings.TrimSpace(cfg.Get("hermes_home")); v != "" {
		s.hermesHome = v
	}
	if v := strings.TrimSpace(cfg.Get("managed_dir")); v != "" {
		s.managedDir = v
	}
	if v := strings.TrimSpace(cfg.Get("config_path")); v != "" {
		s.configPath = v
	}
	return nil
}

func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	installs := s.discoverInstalls()
	if len(installs) == 0 {
		return nil
	}
	for _, inst := range installs {
		cfg := s.readInstall(inst)
		if cfg.AgentRef == s.agentRef && strings.TrimSpace(cfg.Model.Provider) != "" {
			s.meterProvider = strings.ToLower(strings.TrimSpace(cfg.Model.Provider))
		}
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
