// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cline

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

const Name = "olivares.cline"

const (
	maxSettingsBytes = 1 << 20
	defaultAgentRef  = "cline"
	variantCline     = "cline"
	variantKiloCode  = "kilocode"
)

type Source struct {
	agentRef      string
	variant       string
	userPath      string
	workspacePath string
	now           func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source {
	return &Source{
		agentRef: defaultAgentRef,
		variant:  variantCline,
	}
}

func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Cline / Kilo Code (governance)",
		Description: "Governs the Cline (or Kilo Code) VSCode extension from its settings.json layers (read-only): " +
			"posture findings on auto-approve, MCP allowlist, credential exposure, model pinning, custom instructions; " +
			"PERMITTED MCP/tool edges. CostType=\"cline\". Variant config selects cline.*/kilocode.* namespace. " +
			"Cline has no native OTEL — honest blind spot; observability requires a wrapper/proxy.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable origin reference for this Cline install's permitted-capability edges."},
			{Key: "variant", Type: sdk.FieldString, Default: variantCline, Description: "Settings namespace: 'cline' (default) or 'kilocode' (Kilo Code fork)."},
			{Key: "user_settings_path", Type: sdk.FieldString, Description: "User-level VSCode settings.json (no default — the control plane cannot resolve the user's HOME)."},
			{Key: "workspace_settings_path", Type: sdk.FieldString, Description: "Workspace-level .vscode/settings.json (optional)."},
		},
	}
}

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimSpace(cfg.Get("agent_ref")); v != "" {
		s.agentRef = v
	}
	if v := strings.TrimSpace(cfg.Get("variant")); v != "" {
		s.variant = v
	}
	s.userPath = strings.TrimSpace(cfg.Get("user_settings_path"))
	s.workspacePath = strings.TrimSpace(cfg.Get("workspace_settings_path"))
	return nil
}

func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	ls, err := s.readLayers()
	if err != nil {
		return err
	}

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
	return sink.Emit(ctx, s.coverageFinding())
}

func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Source) prefix() string {
	if s.variant == variantKiloCode {
		return "kilocode."
	}
	return "cline."
}

func (s *Source) readLayers() (layers, error) {
	specs := []struct {
		scope string
		path  string
	}{
		{scopeWorkspace, s.workspacePath},
		{scopeUser, s.userPath},
	}
	var ls layers
	for _, sp := range specs {
		if sp.path == "" {
			continue
		}
		l := layer{scope: sp.scope}
		data, err := readFileCapped(sp.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			l.present = false
		case err != nil:
			return nil, err
		default:
			l.present = true
			parsed, perr := parseSettings(data, s.prefix())
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

func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied settings path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSettingsBytes))
}

// parseSettings extracts namespace-prefixed keys from a flat VSCode settings.json.
func parseSettings(data []byte, prefix string) (clineSettings, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return clineSettings{}, err
	}
	var s clineSettings
	for key, val := range raw {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		suffix := key[len(prefix):]
		switch suffix {
		case "autoApprove":
			_ = json.Unmarshal(val, &s.AutoApprove)
		case "autoApproveReadOnly":
			var b bool
			if json.Unmarshal(val, &b) == nil {
				s.AutoApproveReadOnly = &b
			}
		case "autoApproveWrite":
			var b bool
			if json.Unmarshal(val, &b) == nil {
				s.AutoApproveWrite = &b
			}
		case "apiProvider":
			_ = json.Unmarshal(val, &s.APIProvider)
		case "apiModelId":
			_ = json.Unmarshal(val, &s.APIModelID)
		case "apiKey":
			_ = json.Unmarshal(val, &s.APIKey)
		case "customInstructions":
			_ = json.Unmarshal(val, &s.CustomInstructions)
		case "mcpServers":
			_ = json.Unmarshal(val, &s.MCPServers)
		case "allowedTools":
			_ = json.Unmarshal(val, &s.AllowedTools)
		}
	}
	return s, nil
}

var (
	_ model.Observation = model.EdgeObservation{}
	_ model.Observation = model.FindingReport{}
)
