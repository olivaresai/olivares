// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package goose

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const Name = "olivares.goose"

const (
	maxConfigBytes    = 1 << 20
	defaultConfigPath = "~/.config/goose/profiles.yaml"
	defaultAgentRef   = "goose"
)

type Source struct {
	agentRef   string
	configPath string
	profile    string
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
		Title:      "Goose (governance)",
		Description: "Governs Block's Goose AI coding agent from its local profiles.yaml + environment overrides (read-only): " +
			"posture findings on admin settings, provider pinning, extension governance, telemetry, tool approval; " +
			"PERMITTED extension/tool edges. CostType=\"goose\". " +
			"Goose has limited native OTEL — documented as honest blind spot.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable origin reference for this Goose install's permitted-capability edges."},
			{Key: "config_path", Type: sdk.FieldString, Default: defaultConfigPath, Description: "Path to profiles.yaml (operator-supplied)."},
			{Key: "profile", Type: sdk.FieldString, Description: "Active profile name (default: reads GOOSE_PROFILE env or 'default')."},
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
	s.profile = strings.TrimSpace(cfg.Get("profile"))
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

func (s *Source) readConfig() (profileConfig, bool, error) {
	path := expandHome(s.configPath)
	f, err := os.Open(path) //nolint:gosec // operator-supplied config path
	if errors.Is(err, os.ErrNotExist) {
		return profileConfig{}, false, nil
	}
	if err != nil {
		return profileConfig{}, false, err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes))
	if err != nil {
		return profileConfig{}, true, err
	}

	var profiles profilesFile
	if err := yaml.Unmarshal(data, &profiles); err != nil {
		return profileConfig{invalid: true}, true, nil
	}

	activeProfile := s.activeProfileName()
	pc, ok := profiles[activeProfile]
	if !ok {
		return profileConfig{present: true, profileName: activeProfile}, true, nil
	}
	pc.present = true
	pc.profileName = activeProfile
	return pc, true, nil
}

func (s *Source) activeProfileName() string {
	if s.profile != "" {
		return s.profile
	}
	if v := os.Getenv("GOOSE_PROFILE"); v != "" {
		return v
	}
	return "default"
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
