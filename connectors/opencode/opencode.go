// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const Name = "olivares.opencode"

const (
	maxConfigBytes  = 1 << 20
	defaultAgentRef = "opencode"
	costType        = "opencode"
)

type Source struct {
	agentRef    string
	globalPath  string
	projectPath string
	now         func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source {
	return &Source{agentRef: defaultAgentRef}
}

func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "opencode (governance)",
		Description: "Governs SST opencode from local opencode.json/opencode.jsonc JSONC layers (read-only): " +
			"posture findings on managed admin config, permission defaults, MCP, credential exposure, share egress, autoupdate, loop-on-deny, and OTEL; " +
			"PERMITTED MCP/tool/custom-agent edges. CostType=\"opencode\". Native OTEL, when enabled, feeds gen_ai.* ingest out of band via OTEL_*.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable origin reference for this opencode install's permitted-capability edges."},
			{Key: "global_config_path", Type: sdk.FieldString, Description: "User/global opencode.json, opencode.jsonc, or legacy config.json path supplied by the operator."},
			{Key: "project_config_path", Type: sdk.FieldString, Description: "Project opencode.json or opencode.jsonc path supplied by the operator."},
		},
	}
}

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimSpace(cfg.Get("agent_ref")); v != "" {
		s.agentRef = v
	}
	s.globalPath = strings.TrimSpace(cfg.Get("global_config_path"))
	s.projectPath = strings.TrimSpace(cfg.Get("project_config_path"))
	return nil
}

func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	ls, effective, err := s.readLayers()
	if err != nil {
		return err
	}

	for _, l := range ls {
		if l.present && l.invalid {
			if err := sink.Emit(ctx, s.invalidConfigFinding(l.scope)); err != nil {
				return err
			}
		}
	}

	for _, f := range s.postureFindings(effective) {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	for _, e := range s.permittedEdges(effective) {
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}
	if err := sink.Emit(ctx, s.inventoryFinding(effective)); err != nil {
		return err
	}
	return sink.Emit(ctx, s.coverageFinding(effective))
}

func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Source) readLayers() ([]configLayer, config, error) {
	specs := []struct {
		scope string
		path  string
	}{
		{scopeGlobal, s.globalPath},
		{scopeProject, s.projectPath},
	}

	var layers []configLayer
	var effective config
	for _, sp := range specs {
		if sp.path == "" {
			continue
		}
		l := configLayer{scope: sp.scope, path: sp.path}
		data, err := readFileCapped(sp.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			l.present = false
		case err != nil:
			return nil, config{}, err
		default:
			l.present = true
			parsed, perr := parseConfig(data)
			if perr != nil {
				l.invalid = true
			} else {
				parsed.present = true
				l.cfg = parsed
				effective = effective.merge(parsed)
				effective.present = true
			}
		}
		layers = append(layers, l)
	}
	return layers, effective, nil
}

func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied opencode config path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxConfigBytes))
}

func parseConfig(data []byte) (config, error) {
	cleaned := stripJSONC(data)
	var c config
	if err := json.Unmarshal(cleaned, &c); err != nil {
		return config{}, err
	}
	c.foldDeprecatedMode()
	return c, nil
}

var (
	_ model.Observation = model.EdgeObservation{}
	_ model.Observation = model.FindingReport{}
)
