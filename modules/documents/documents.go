// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package documents enables local, offline document conversion for sessions.
package documents

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	conversion "github.com/olivaresai/olivares/core/runtime/documents"
	"github.com/olivaresai/olivares/sdk"
)

type Module struct {
	enabled atomic.Bool
	log     *slog.Logger
}

func New() *Module { return &Module{} }
func (*Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.documents", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeModule, Title: "Documents", Description: "Convert documents to Markdown locally with MarkItDown, without network access."}
}
func (m *Module) Init(_ context.Context, host sdk.Host) error { m.log = host.Logger(); return nil }
func (m *Module) Start(context.Context) error                 { m.enabled.Store(true); return nil }
func (m *Module) Stop(context.Context) error                  { m.enabled.Store(false); return nil }
func (*Module) APINamespace() string                          { return "documents" }
func (*Module) APIRoutes(api.RouteRegistrar)                  {}
func (*Module) Permissions() []auth.Permission                { return nil }
func (m *Module) Markdown(ctx context.Context, name string, data []byte) ([]byte, error) {
	if !m.enabled.Load() {
		return nil, errors.New("document conversion is disabled in Settings > Modules")
	}
	started := time.Now()
	md, err := conversion.Markdown(ctx, name, data)
	if m.log != nil {
		result := "converted"
		if err != nil {
			result = "refused"
		}
		m.log.Info("document conversion", "format", filepath.Ext(name), "input_bytes", len(data), "output_bytes", len(md), "duration", time.Since(started), "result", result)
	}
	return md, err
}
