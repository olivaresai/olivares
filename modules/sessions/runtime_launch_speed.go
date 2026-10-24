// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
)

func runFastMode(rec model.Record) *bool {
	if rec[colRunFastMode] == nil {
		return nil
	}
	value := rec.Bool(colRunFastMode)
	return &value
}

// Speed choices are checked after profile resolution on create and resume, before
// governance or persistence. A native tool must never silently discard a choice.
func (m *Module) validateLaunchSpeed(ctx context.Context, tenant model.TenantID, p *CreateRunParams) error {
	if p.FastMode == nil && p.ServiceTier == "" {
		return nil
	}
	if p.ProviderHome == nil {
		return badRequest("speed options require a provider profile")
	}
	driver := launchDriverKey(*p)
	if p.FastMode != nil && driver != providerDriverClaude {
		return badRequest("fast_mode is available only for Claude Code")
	}
	if p.ServiceTier != "" && driver != providerDriverCodex {
		return badRequest("service_tier is available only for Codex")
	}
	if p.ServiceTier != "" && !validToolCatalogModelID(p.ServiceTier) {
		return badRequest("invalid service_tier")
	}
	if p.FastMode == nil || !*p.FastMode {
		return nil
	}
	// Claude's fast availability probe ignores ANTHROPIC_BASE_URL. A bound
	// provider cannot authorize that request to Anthropic's separate endpoint.
	if p.ProviderHome != nil && p.ProviderHome.ProviderRecordRef != "" {
		return badRequest("Fast mode cannot be enabled for a provider-bound session")
	}
	catalog, err := m.ProfileModelCatalog(ctx, tenant, p.ProviderProfileRef, false)
	if err != nil {
		return err
	}
	if catalog.State != "fresh" {
		return &runErr{http.StatusServiceUnavailable, "Fast mode support could not be checked; refresh the model catalog or leave Fast mode at the tool default"}
	}
	selected := p.Model
	if selected == "" {
		selected = catalog.DefaultModel
	}
	for _, md := range catalog.Models {
		// A session stores the exact id its catalog selector launched on (dev#2786):
		// its resume finds the same entry.
		if (md.ID == selected || claudeModelID(md.ID) == selected) && md.SupportsFastMode != nil && *md.SupportsFastMode {
			// Pin the exact selector checked above instead of inheriting a different
			// configured model that Fast mode could replace without governance.
			p.Model = selected
			return nil
		}
	}
	return badRequest("choose a model that reports Fast mode support before enabling Fast mode")
}
