// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const Name = "olivares.openhands"

const (
	maxConfigBytes    = 1 << 20
	defaultConfigPath = "~/.openhands/config.toml"
	defaultAgentRef   = "openhands"
)

type Source struct {
	agentRef   string
	configPath string
	now        func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source {
	return &Source{
		agentRef:   defaultAgentRef,
		configPath: defaultConfigPath,
	}
}

func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "OpenHands (governance)",
		Description: "Governs the OpenHands AI software engineer from its local config.toml + environment overrides (read-only): " +
			"posture findings on sandbox type, model pinning, credential exposure, telemetry, iteration limits; " +
			"PERMITTED MCP/action edges; coverage finding for OTEL gen_ai.* ingest. CostType=\"openhands\". " +
			"OpenHands has the best OSS OTEL gen_ai.* story — live usage arrives via the ingest.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable origin reference for this OpenHands install's permitted-capability edges."},
			{Key: "config_path", Type: sdk.FieldString, Default: defaultConfigPath, Description: "Path to config.toml (operator-supplied; default is the standard OpenHands location)."},
		},
	}
}

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimSpace(cfg.Get("agent_ref")); v != "" {
		s.agentRef = v
	}
	if v := strings.TrimSpace(cfg.Get("config_path")); v != "" {
		s.configPath = v
	}
	return nil
}

func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	cfg, present, err := s.readConfig()
	if err != nil {
		return err
	}

	if present && cfg.invalid {
		if eerr := sink.Emit(ctx, s.invalidConfigFinding()); eerr != nil {
			return eerr
		}
	}

	cfg = s.applyEnvOverrides(cfg)

	for _, f := range s.postureFindings(cfg) {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	for _, e := range s.permittedEdges(cfg) {
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}
	if err := sink.Emit(ctx, s.inventoryFinding(cfg)); err != nil {
		return err
	}
	return sink.Emit(ctx, s.coverageFinding(cfg))
}

func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Source) readConfig() (config, bool, error) {
	path := expandHome(s.configPath)
	f, err := os.Open(path) //nolint:gosec // operator-supplied config path
	if errors.Is(err, os.ErrNotExist) {
		return config{}, false, nil
	}
	if err != nil {
		return config{}, false, err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes))
	if err != nil {
		return config{}, true, err
	}

	var c config
	if err := toml.Unmarshal(data, &c.raw); err != nil {
		c.invalid = true
		return c, true, nil
	}
	c.present = true
	return c, true, nil
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

var (
	_ model.Observation = model.EdgeObservation{}
	_ model.Observation = model.FindingReport{}
)
