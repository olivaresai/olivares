// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
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

// claudeModelAliases are the Claude Code aliases a launch accepts and never hands
// the tool: Claude Code resolves an alias, and picks its own default when no model
// is named, in each process it starts, and one account measured `opus` on
// claude-opus-4-8 in one session and claude-opus-5-5 in the next. The
// launch passes and records the exact id; the tool's `[1m]` suffix is kept.
var claudeModelAliases = map[string]string{
	"default": providerCodingModels[ProviderKindAnthropic],
	"opus":    "claude-opus-5-5",
	"sonnet":  "claude-sonnet-5-5",
	"haiku":   "claude-haiku-5-5",
	"fable":   "claude-fable-5-1",
}

// claudeModelID is the exact id a Claude Code model choice launches on; a term
// that is not an alias is returned as written. Claude Code reads aliases in any
// case, so this does too.
func claudeModelID(model string) string {
	alias, suffix := strings.ToLower(model), ""
	if base, ok := strings.CutSuffix(alias, "[1m]"); ok {
		alias, suffix = base, "[1m]"
	}
	if alias == "" {
		alias = "default"
	}
	if id, ok := claudeModelAliases[alias]; ok {
		return id + suffix
	}
	return model
}

// resolveClaudeModelAlias runs on every launch once its profile is known. A
// provider-bound launch keeps its model: there the endpoint names the models.
func resolveClaudeModelAlias(p *CreateRunParams) {
	if p.ProviderHome == nil || p.ProviderHome.Driver != providerDriverClaude || p.ProviderHome.ProviderRecordRef != "" {
		return
	}
	p.Model = claudeModelID(p.Model)
}

// initialProviderModel uses only the successful bound test's usable model IDs.
// A preset's own default, when the vendor lists it, comes before the kind's.
func initialProviderModel(kind, service string, models []string) string {
	usable := localModelNames(models)
	usable = slices.DeleteFunc(usable, func(s string) bool { return !validOpenProviderTerm(s) })
	if row, ok := modelprovider.LookupPreset(service); ok && slices.Contains(usable, row.Models.Default) {
		return row.Models.Default
	}
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
	if rec.State != ProviderRecordActive || rec.ProbeState == ProbeRefused || rec.DefaultModel == nil {
		return nil
	}
	selected := *rec.DefaultModel
	if selected == "" && rec.ProbeState != ProbeNever {
		if preset, ok := modelprovider.LookupPreset(rec.Service); ok {
			selected = preset.Models.Default
		}
	}
	if selected == "" {
		return nil
	}
	// An unavailable catalog says nothing about a chosen model, just as for an
	// explicit launch choice. A successful list remains authoritative, even empty.
	available := rec.ProbeState == ProbeNever || rec.ProbeState == ProbeUnreachable ||
		rec.ProbeState == ProbeOK && slices.Contains(localModelNames(rec.Models), selected)
	if !validOpenProviderTerm(selected) || !available {
		return &codedRunErr{conflictErr("The saved default model for " + rec.DisplayName +
			" is unavailable: test this provider and choose a listed default in Providers, or choose a model in New session or with --model."),
			"provider_default_model_unavailable"}
	}
	p.Model = selected
	if p.ProviderHome.Driver == providerDriverOpenCode {
		// Saved IDs belong to the endpoint. Translate once before admission and
		// the run snapshot; an explicit native choice above is never translated.
		p.Model = openCodeModelValue(BoundProvider{Kind: rec.Kind}, p.Model, true)
	}
	return nil
}

// providerPresetModelEnv is minted only for a Claude launch on a preset. Saved
// defaults override the main role; background roles keep the vendor's map.
func providerPresetModelEnv(rec ProviderRecord) []EnvVar {
	row, ok := modelprovider.LookupPreset(rec.Service)
	if !ok || row.Kind != ProviderKindAnthropic {
		return nil
	}
	main := row.Models.Default
	if rec.DefaultModel != nil && *rec.DefaultModel != "" {
		main = *rec.DefaultModel
	}
	var env []EnvVar
	for _, item := range []EnvVar{
		{Name: "ANTHROPIC_MODEL", Value: main},
		{Name: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: row.Models.Opus},
		{Name: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: row.Models.Sonnet},
		{Name: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: row.Models.Haiku},
		{Name: "ANTHROPIC_DEFAULT_FABLE_MODEL", Value: row.Models.Fable},
		{Name: "CLAUDE_CODE_SUBAGENT_MODEL", Value: row.Models.Subagent},
	} {
		if item.Value != "" {
			env = append(env, item)
		}
	}
	return env
}
