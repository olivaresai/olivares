// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/compliance"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// claudefileswiring.go is the composition-root adapter that binds the AGPL compliance
// module's FileStoreEraser seam to the Apache claude-api Files connector — the open-core
// boundary: compliance owns the governed DECISION (hold gate, dual-control, receipt) and
// never imports the connector; this root holds the operator's workspace credential and does
// the identity-blind HTTP I/O. It is the Files-store sibling of the providerEraserAdapter.

// claudeFilesConfig is the operator provisioning of the governed Files plane
// (OLIVARES_CLAUDE_FILES_CONFIG, an operator-secret JSON file — the loadClaudeEraserConfig
// pattern). workspace_key is the INFERENCE (workspace) API key the Files API uses (x-api-key)
// — NOT the Admin key. The Files API is available on the Direct, Claude-Platform-on-AWS and
// Microsoft Foundry surfaces (not Bedrock/Vertex); surface defaults to direct.
type claudeFilesConfig struct {
	WorkspaceKey     string `json:"workspace_key"`
	BaseURL          string `json:"base_url"`
	Surface          string `json:"surface"`
	Region           string `json:"region"`
	AnthropicVersion string `json:"anthropic_version"`
}

// loadClaudeFilesConfig reads the optional OLIVARES_CLAUDE_FILES_CONFIG JSON. A missing path
// yields an empty config (the plane stays not-wired, honest); a supplied path must be
// readable and contain valid JSON or startup fails closed.
func loadClaudeFilesConfig(_ *slog.Logger) (claudeFilesConfig, error) {
	path := os.Getenv("OLIVARES_CLAUDE_FILES_CONFIG")
	if path == "" {
		return claudeFilesConfig{}, nil
	}
	var cfg claudeFilesConfig
	if err := loadOperatorJSONConfig("OLIVARES_CLAUDE_FILES_CONFIG", path, &cfg); err != nil {
		return claudeFilesConfig{}, err
	}
	return cfg, nil
}

// fileStoreEraserAdapter implements compliance.FileStoreEraser over the operator-credentialed
// inference client. It is identity-blind (the connector decides nothing); the tenant is
// ignored because one workspace credential fronts one workspace store (the providerEraser
// pattern — per-tenant credential routing is a follow-up).
type fileStoreEraserAdapter struct {
	inf *claudeapi.Inference
	log *slog.Logger
}

// newFileStoreEraserAdapter builds the adapter, or nil when no workspace key is provisioned
// (then the plane stays not-wired and the receipt records the gap honestly).
func newFileStoreEraserAdapter(cfg claudeFilesConfig, log *slog.Logger) *fileStoreEraserAdapter {
	if strings.TrimSpace(cfg.WorkspaceKey) == "" {
		return nil
	}
	surface := sdkmodel.Gateway(firstNonEmpty(strings.TrimSpace(cfg.Surface), string(sdkmodel.GatewayDirect)))
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		if s, ok := claudeapi.SurfaceFor(surface); ok {
			base = strings.ReplaceAll(s.BaseURLPattern, "{region}", strings.TrimSpace(cfg.Region))
		}
	}
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{
		BaseURL: base, APIKey: strings.TrimSpace(cfg.WorkspaceKey),
		AnthropicVersion: strings.TrimSpace(cfg.AnthropicVersion), Gateway: surface,
	})
	return &fileStoreEraserAdapter{inf: inf, log: log}
}

var _ compliance.FileStoreEraser = (*fileStoreEraserAdapter)(nil)

// Wired reports the adapter is configured (it is only constructed with a workspace key).
func (a *fileStoreEraserAdapter) Wired() bool { return a != nil && a.inf != nil }

// ListFiles enumerates the store (paginated by the connector) and maps to minimal-data refs
// — never the filename (it can carry PII), only id / mime / size / created / scope.
func (a *fileStoreEraserAdapter) ListFiles(ctx context.Context, _ model.TenantID, scopeID string) ([]compliance.FileRef, error) {
	files, err := a.inf.ListAllFiles(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	out := make([]compliance.FileRef, 0, len(files))
	for _, f := range files {
		ref := compliance.FileRef{ID: f.ID, MimeType: f.MimeType, SizeBytes: f.SizeBytes, CreatedAt: f.CreatedAt}
		if f.Scope != nil {
			ref.ScopeID = f.Scope.ID
		}
		out = append(out, ref)
	}
	return out, nil
}

// DeleteFile deletes one file and returns the provider's confirmation id.
func (a *fileStoreEraserAdapter) DeleteFile(ctx context.Context, _ model.TenantID, fileID string) (string, error) {
	del, err := a.inf.DeleteFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	return del.ID, nil
}
