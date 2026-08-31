// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file exports CREDENTIAL-LESS reference accessors over the package's static
// declared data: the deployment-surface AsOf stamp (surfaces.go), the full
// model-lifecycle schedule (lifecycle.go retirementSchedule) and the sampling-param
// deprecation descriptor. They exist because CatalogProvider.Snapshot cannot serve
// the platforms matrix: Snapshot carries models/keys/workspaces, while the
// surface matrix lives in AllSurfaces() and the lifecycle registry had no exported
// enumerator (RetirementsFor is per-id). Everything here is DECLARED, AsOf-stamped
// reference — read-only, deterministic, no network, no credential — so a module
// route can present it verbatim (ARCHITECTURE.md present-never-recompute) without ever
// fabricating a date the authority did not publish.
package claudeapi

import (
	"sort"
	"strings"

	"github.com/olivaresai/olivares/sdk/model"
)

// SurfacesAsOf returns the date the deployment-surface matrix (AllSurfaces) was
// recorded — the AsOf stamp every Surface row carries.
func SurfacesAsOf() string { return surfacesAsOf }

// LifecycleAsOf returns the date the lifecycle schedule (LifecycleReference) was
// recorded against the model-deprecations authority.
func LifecycleAsOf() string { return lifecycleAsOf }

// SurfaceDate is one verified per-surface retirement deadline of a lifecycle family.
// An empty RetiresOn means "retirement announced, date not published" (e.g. the
// claude-mythos-preview rows) — never a guess.
type SurfaceDate struct {
	// Surface is the deployment surface the deadline applies to.
	Surface model.Gateway
	// RetiresOn is the ISO-8601 retirement date, or "" when announced without a date.
	RetiresOn string
}

// LifecycleFamily is one declared lifecycle schedule entry, materialized for the
// reference consumers (the lifecycle matrix). It is descriptive: the deny-closed
// evaluators (RetirementsFor / LifecycleStateFor) stay the enforcement truth.
type LifecycleFamily struct {
	// ModelID is the family's registered id PREFIX (the registry matcher), verbatim:
	// "claude-opus-4-2025" covers the dated claude-opus-4-20250514, "claude-2." covers
	// claude-2.0/2.1. It is an identifier of the schedule entry, not always a callable id.
	ModelID string
	// DisplayName is a deterministic human label derived from the prefix (humanizeFamily).
	DisplayName string
	// DeprecatedOn is the published deprecation date on the Anthropic-operated
	// surfaces ("" = not published in the verified capture).
	DeprecatedOn string
	// Replacement is the recommended successor model-deprecations.md publishes for
	// the family ("" = none named).
	Replacement string
	// AsOf stamps when the schedule was recorded.
	AsOf string
	// PerSurface lists the surfaces with a VERIFIED schedule, sorted by surface.
	PerSurface []SurfaceDate
	// ToConfirm lists surfaces verified to run their own (unpublished) schedule for
	// the family — rendered as explicit "date not published / to-confirm" rows so
	// absence is never presented as "never retires". See retirementEntry.toConfirm
	// for the provenance rule. Sorted by surface; empty for most families.
	ToConfirm []model.Gateway
}

// LifecycleReference materializes the declared lifecycle schedule
// (retirementSchedule) in registry order, skipping the exempt carve-out entries —
// an exempt hit means "no schedule", so presenting one as a family would fabricate
// a lifecycle for a still-active id. Each call returns fresh slices: callers may
// sort/mutate without corrupting the registry.
func LifecycleReference() []LifecycleFamily {
	out := make([]LifecycleFamily, 0, len(retirementSchedule))
	for _, e := range retirementSchedule {
		if e.exempt {
			continue
		}
		f := LifecycleFamily{
			ModelID:      e.prefix,
			DisplayName:  humanizeFamily(e.prefix),
			DeprecatedOn: e.deprecatedOn,
			Replacement:  e.replacement,
			AsOf:         lifecycleAsOf,
			PerSurface:   make([]SurfaceDate, 0, len(e.perSurface)),
		}
		for surface, date := range e.perSurface {
			f.PerSurface = append(f.PerSurface, SurfaceDate{Surface: surface, RetiresOn: date})
		}
		sort.Slice(f.PerSurface, func(i, j int) bool { return f.PerSurface[i].Surface < f.PerSurface[j].Surface })
		if len(e.toConfirm) > 0 {
			f.ToConfirm = append([]model.Gateway(nil), e.toConfirm...)
			sort.Slice(f.ToConfirm, func(i, j int) bool { return f.ToConfirm[i] < f.ToConfirm[j] })
		}
		out = append(out, f)
	}
	return out
}

// humanizeFamily derives a deterministic human label from a registry prefix:
// "claude-sonnet-4" → "Claude Sonnet 4", "claude-3-5-haiku" → "Claude 3.5 Haiku",
// "claude-2." → "Claude 2", "claude-mythos-preview" → "Claude Mythos (preview)".
// Rules (a LABEL transform, not model knowledge): name tokens are title-cased in
// place; consecutive numeric tokens join with "."; a numeric token of 4+ digits is a
// dated-id year/date pin and is dropped ("claude-opus-4-2025" → "Claude Opus 4");
// a trailing "-0" minor is Anthropic's same-model alias marker and is dropped
// ("claude-opus-4-0" ≡ claude-opus-4-20250514 → also "Claude Opus 4" — honest: the
// two entries name the SAME model, distinguished by ModelID).
func humanizeFamily(prefix string) string {
	p := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(prefix), "."), "-")
	p = strings.TrimPrefix(p, "claude-")
	var (
		parts   []string
		version []string
	)
	flush := func() {
		if len(version) == 0 {
			return
		}
		if len(version) > 1 && version[len(version)-1] == "0" {
			version = version[:len(version)-1] // alias "-0" marker, not a minor version
		}
		parts = append(parts, strings.Join(version, "."))
		version = nil
	}
	for _, tok := range strings.Split(p, "-") {
		switch {
		case tok == "":
		case isDigits(tok):
			if len(tok) >= 4 {
				continue // dated-id year/date pin (e.g. "2025"), not a version
			}
			version = append(version, tok)
		case tok == "preview":
			flush()
			parts = append(parts, "(preview)")
		default:
			flush()
			parts = append(parts, strings.ToUpper(tok[:1])+tok[1:])
		}
	}
	flush()
	return "Claude " + strings.Join(parts, " ")
}

// isDigits reports whether s is non-empty and all ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ParamDeprecationRef is the declared sampling-param deprecation descriptor
// (ANT2-03): the affected generations reject non-default temperature/top_p/top_k
// with a 400. The enforcement truth is RejectsSamplingParams (per-id); this is the
// presentational summary the reference consumers render. It is Anthropic's
// deprecation, not a product bug — informational pre-advice.
type ParamDeprecationRef struct {
	// Params are the rejected sampling parameters.
	Params []string
	// Affected names the affected generations, matching the finding title vocabulary
	// (samplingDeprecationFinding): "Opus 4.7+, Fable/Mythos 5".
	Affected string
	// HTTPStatus is the status the provider returns for a non-default value.
	HTTPStatus int
	// AsOf stamps when the deprecation was recorded (the lifecycle capture).
	AsOf string
}

// ParamDeprecationReference returns the declared sampling-param deprecation
// descriptor. Fresh slice per call.
func ParamDeprecationReference() ParamDeprecationRef {
	return ParamDeprecationRef{
		Params:     []string{"temperature", "top_p", "top_k"},
		Affected:   "Opus 4.7+, Fable/Mythos 5",
		HTTPStatus: 400,
		AsOf:       lifecycleAsOf,
	}
}
