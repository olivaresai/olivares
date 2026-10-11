// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

func validProviderDefaultModel(choice *string) (string, error) {
	if choice == nil {
		return "", nil
	}
	value := strings.TrimSpace(*choice)
	if value != "" && !validOpenProviderTerm(value) {
		return "", badRequest("default_model must be a bounded model identifier without control characters")
	}
	return value, nil
}

// 26.10.2 coding preferences, selected only when this record's test lists the
// exact ID. Release notes name this table's choices; saved defaults never follow
// later table changes. Vendor model/selection references:
// developers.openai.com/api/docs/models, platform.claude.com/docs/en/models/overview,
// docs.x.ai/developers/models. Native qualification accompanies the release.
var providerCodingModels = map[string]string{
	ProviderKindAnthropic: "claude-opus-5-5",
	ProviderKindOpenAI:    "gpt-6-astra",
	ProviderKindXAI:       "grok-4.7",
}

// initialProviderModel uses only the successful bound test's usable model IDs.
func initialProviderModel(kind string, models []string) string {
	usable := localModelNames(models)
	usable = slices.DeleteFunc(usable, func(s string) bool { return !validOpenProviderTerm(s) })
	if preferred := providerCodingModels[kind]; slices.Contains(usable, preferred) {
		return preferred
	}
	if len(usable) > 0 && (kind == ProviderKindOllama || kind == ProviderKindOpenAICompatible && len(usable) == 1) {
		return usable[0]
	}
	return ""
}

// resolveProviderDefaultModel fills the existing launch choice before admission
// and persistence. Resume and dispatch replay do not enter this fresh-start path.
func (m *Module) resolveProviderDefaultModel(ctx context.Context, tenant model.TenantID, p *CreateRunParams) error {
	if p.Model != "" || p.ProviderHome == nil || p.ProviderHome.AuthSource != AuthSourceManagedInjection || p.ProviderHome.ProviderRecordRef == "" {
		return nil
	}
	rec, err := m.GetProviderRecord(ctx, tenant, p.ProviderHome.ProviderRecordRef)
	if err != nil {
		return err
	}
	if rec.State != ProviderRecordActive || rec.ProbeState == ProbeRefused || rec.DefaultModel == nil || *rec.DefaultModel == "" {
		return nil
	}
	if !validOpenProviderTerm(*rec.DefaultModel) || rec.ProbeState != ProbeOK || !slices.Contains(localModelNames(rec.Models), *rec.DefaultModel) {
		return &codedRunErr{conflictErr("The saved default model for " + rec.DisplayName +
			" is unavailable: test this provider and choose a listed default in Providers, or choose a model in New session or with --model."),
			"provider_default_model_unavailable"}
	}
	p.Model = *rec.DefaultModel
	if p.ProviderHome.Driver == providerDriverOpenCode {
		// Saved IDs belong to the endpoint. Translate once before admission and
		// the run snapshot; an explicit native choice above is never translated.
		p.Model = openCodeModelValue(BoundProvider{Kind: rec.Kind}, p.Model, true)
	}
	return nil
}
