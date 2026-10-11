// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

type tracingSettingsService struct {
	mu       sync.Mutex
	settings *productSettings
	provider *obstrace.Provider
	version  string
}

func newTracingSettingsService(settings *productSettings, provider *obstrace.Provider, version string) *tracingSettingsService {
	return &tracingSettingsService{settings: settings, provider: provider, version: version}
}

func (s *tracingSettingsService) TracingSettings(ctx context.Context) (api.TracingStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status(ctx)
}

func (s *tracingSettingsService) status(ctx context.Context) (api.TracingStatus, error) {
	doc, _, err := s.settings.load(ctx)
	if err != nil {
		return api.TracingStatus{}, api.ErrTracingUnavailable
	}
	choice := obstrace.DefaultSettings()
	if doc.Tracing != nil {
		choice = *doc.Tracing
	}
	_, overrides := choice.Resolve(s.version)
	if overrides == nil {
		overrides = []string{}
	}
	effective := s.provider.Settings()
	return api.TracingStatus{Settings: choice, Effective: effective, Overrides: overrides}, nil
}

func (s *tracingSettingsService) SaveTracingSettings(ctx context.Context, actor auth.Principal, choice obstrace.Settings) (api.TracingStatus, error) {
	if err := choice.Validate(); err != nil {
		return api.TracingStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, _ := choice.Resolve(s.version)
	next, err := obstrace.New(ctx, cfg)
	if err != nil {
		return api.TracingStatus{}, fmt.Errorf("%w: collector could not be configured", api.ErrTracingUnavailable)
	}
	err = s.settings.updateWithAudit(ctx, actor, "deployment.settings.tracing", map[string]any{"enabled": choice.Enabled, "protocol": choice.Protocol, "sample_ratio": choice.SampleRatio}, func(doc *productSettingsDoc) { doc.Tracing = &choice }, true)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err != nil {
		_ = next.Shutdown(cleanupCtx)
		return api.TracingStatus{}, api.ErrTracingUnavailable
	}
	if err = s.provider.Replace(cleanupCtx, next); err != nil {
		return api.TracingStatus{}, api.ErrTracingUnavailable
	}
	return s.status(ctx)
}

// applyStored runs after the store opens and before listeners accept requests.
func (s *tracingSettingsService) applyStored(ctx context.Context) error {
	doc, _, err := s.settings.load(ctx)
	if err != nil {
		return api.ErrTracingUnavailable
	}
	if doc.Tracing == nil {
		return nil
	}
	if err = doc.Tracing.Validate(); err != nil {
		return api.ErrTracingUnavailable
	}
	cfg, _ := doc.Tracing.Resolve(s.version)
	next, err := obstrace.New(ctx, cfg)
	if err != nil {
		return api.ErrTracingUnavailable
	}
	swapCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.provider.Replace(swapCtx, next)
}
