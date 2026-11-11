// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package documents

import (
	"context"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	conversion "github.com/olivaresai/olivares/core/runtime/documents"
	"github.com/olivaresai/olivares/sdk"
)

// OCRModule controls the optional backend used by the Documents converter.
type OCRModule struct {
	enabled atomic.Bool
	config  conversion.OCRConfig
}

func NewOCR(config ...conversion.OCRConfig) *OCRModule {
	m := &OCRModule{}
	if len(config) > 0 {
		m.config = config[0]
	}
	return m
}
func (*OCRModule) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.ocr", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeModule, Title: "Scanned documents (OCR)", Description: "Read scanned pages and images with a local endpoint and administrator-declared Unlimited-OCR 2 model."}
}
func (m *OCRModule) Init(context.Context, sdk.Host) error { return m.config.Validate() }
func (m *OCRModule) Start(context.Context) error          { m.enabled.Store(true); return nil }
func (m *OCRModule) Stop(context.Context) error           { m.enabled.Store(false); return nil }
func (*OCRModule) APINamespace() string                   { return "ocr" }
func (*OCRModule) APIRoutes(api.RouteRegistrar)           {}
func (*OCRModule) Permissions() []auth.Permission         { return nil }

// Config returns no endpoint while the module is off, preventing document egress.
func (m *OCRModule) Config() conversion.OCRConfig {
	if !m.enabled.Load() {
		return conversion.OCRConfig{}
	}
	return m.config
}
