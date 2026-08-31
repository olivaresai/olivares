// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the CONTEXT WINDOW per deployment surface (ANT2-01): a model's
// maximum input context is PER-PLATFORM, not global. The six-surface matrix
// (surfaces.go) already models auth/APIs/residency/billing per surface, and the
// family catalog (catalog.go) and the AGPL reference (modules/models/reference.go)
// carry a SINGLE ContextWindow per family — but that single value is silently wrong on
// any surface where the window diverges. The headline, verified divergence: Claude
// Opus 4.8 has a 1M-token window on the Claude API / Amazon Bedrock / Vertex AI /
// Claude Platform on AWS but only 200K on Microsoft Foundry — so the NEWEST Opus is the
// one current model capped on Foundry, while the older Opus 4.7 / 4.6 (and Sonnet 4.6,
// Fable 5) get the full 1M there. A router or governance check that reads the single
// 1M window does not know a 1M request to a Foundry estate will be truncated or
// rejected (stop_reason model_context_window_exceeded) — a "silently wrong" blind
// spot. This file turns that blind spot into an explicit signal: the per-(model,
// surface) overlay, a pre-flight router/governance CHECK, and a declared
// surface_capability_divergence FINDING the gather emits per configured surface.
//
// It mirrors the per-surface lifecycle model (lifecycle.go: RetirementsFor /
// LifecycleStateFor): a declared, AsOf-stamped table matched by longest model-id
// prefix, verified against primary docs, with nothing fabricated for a (model,
// surface) the authority did not state (ARCHITECTURE.md). It REFINES — it does not replace —
// the coarse single-window family catalog: buildModel uses an entry's standard window
// for Model.ContextWindow and attaches the per-surface overlay (SurfaceContextWindowsFor).
//
// Authority (verbatim, fetched 2026-06-15):
//
//	platform.claude.com/docs/en/build-with-claude/context-windows — "Claude Opus 4.8,
//	… Opus 4.7, Opus 4.6, and Sonnet 4.6 have a 1M-token context window on the Claude
//	API, Amazon Bedrock, and Vertex AI. On Microsoft Foundry, Claude Opus 4.8 has a
//	200k-token context window. Other Claude models, including Claude Sonnet 4.5, have a
//	200k-token context window."
//	…/build-with-claude/claude-in-microsoft-foundry — "Claude Fable 5, Claude Opus 4.7,
//	Claude Opus 4.6, and Claude Sonnet 4.6 have a 1M-token context window on Microsoft
//	Foundry. Other Claude models, including Claude Opus 4.8 and Sonnet 4.5, have a 200k-
//	token context window."
package claudeapi

import (
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk/model"
)

// contextWindowAsOf stamps the per-surface context-window overlay with the date it was
// recorded (the primary-doc capture above). Distinct from pricingAsOf / lifecycleAsOf:
// each dimension carries its own verification date so bumping one never fabricates
// provenance for another (ARCHITECTURE.md).
const contextWindowAsOf = "2026-06-15"

// subjectSurfaceCapability is the FindingReport subject for a per-surface capability
// divergence (a model whose context window is smaller on this surface than its standard).
const subjectSurfaceCapability = "anthropic.surface_capability"

// findingSurfaceCapabilityDivergence is the FindingReport.Kind the router/governance
// surfaces when a model's effective window on a surface is below its standard window.
// The session names this kind explicitly.
const findingSurfaceCapabilityDivergence = "surface_capability_divergence"

// contextWindowEntry is a declared per-surface context window for a model family/
// version, matched by longest model-id prefix. standard is the window on the surfaces
// NOT listed in perSurface; perSurface overrides it for the surfaces where the
// authority published a divergent value. A surface absent from perSurface gets
// standard — never a fabricated value (ARCHITECTURE.md).
type contextWindowEntry struct {
	prefix string
	// standard is the model's maximum input context on its standard surfaces (the
	// Claude API / Amazon Bedrock / Vertex AI / Claude Platform on AWS).
	standard int64
	// perSurface overrides standard for the specific surfaces where the window
	// diverges (verified). Currently only Microsoft Foundry caps a current model
	// (Opus 4.8) below its standard window.
	perSurface map[model.Gateway]int64
	// defaultEffort is the model's default effort level when the effort parameter is
	// omitted (VERIFIED 2026-06-27: "high" for all current models — "Setting effort
	// to 'high' produces exactly the same behavior as omitting the effort parameter
	// entirely", platform.claude.com/docs/en/build-with-claude/effort). Empty means
	// the model does not support effort control.
	defaultEffort string
	// effortLevels are the supported effort tiers in API order (VERIFIED 2026-06-27).
	effortLevels []string
}

// contextWindowSchedule is the declared, verified-only per-surface context-window
// overlay (primary docs fetched 2026-06-15). Entries are the CURRENT generation; an
// id with no entry falls back to the coarse family window (catalog.go) — the same
// conservative-floor behavior the offline catalog already uses for unlisted ids.
// Listed longest-prefix-first is not required (lookup picks the longest match), but
// the version-specific prefixes (claude-opus-4-8) must out-specify any shorter ones.
// effortAll5 is the full effort level set for models supporting all five tiers.
var effortAll5 = []string{"low", "medium", "high", "xhigh", "max"}

// effortNoXHigh is the effort level set for models that lack xhigh (Opus 4.6, Sonnet 4.6).
var effortNoXHigh = []string{"low", "medium", "high", "max"}

// effortDefault is the default effort level when the effort parameter is omitted
// (VERIFIED 2026-06-27: "Setting effort to 'high' produces exactly the same behavior
// as omitting the effort parameter entirely").
const effortDefault = "high"

var contextWindowSchedule = []contextWindowEntry{
	// Opus 4.8 — THE verified divergence: 1M standard, 200K on Foundry. The newest Opus
	// is the only current model Foundry caps; Opus 4.7/4.6 get the full 1M there.
	// Effort: all 5 levels incl. xhigh (VERIFIED 2026-06-27).
	{prefix: "claude-opus-4-8", standard: 1_000_000, perSurface: map[model.Gateway]int64{model.GatewayFoundry: 200_000}, defaultEffort: effortDefault, effortLevels: effortAll5},
	// Opus 4.7 / 4.6 — 1M on every surface incl. Foundry (no override).
	// Opus 4.7: all 5 effort levels incl. xhigh. Opus 4.6: 4 levels, NO xhigh.
	{prefix: "claude-opus-4-7", standard: 1_000_000, defaultEffort: effortDefault, effortLevels: effortAll5},
	{prefix: "claude-opus-4-6", standard: 1_000_000, defaultEffort: effortDefault, effortLevels: effortNoXHigh},
	// Opus 4.5 — predates the 1M GA (Opus 4.6 / Sonnet 4.6, 2026-03-13): 200K everywhere.
	// Effort supported but only low/medium/high/max (no xhigh, VERIFIED 2026-06-27).
	{prefix: "claude-opus-4-5", standard: 200_000, defaultEffort: effortDefault, effortLevels: effortNoXHigh},
	// Sonnet 5 — 1M standard context, 128K max output; full effort set incl. xhigh.
	// VERIFIED 2026-07-03 against models/overview ("effort" defaults to high on the
	// Claude API and Claude Code).
	{prefix: "claude-sonnet-5", standard: 1_000_000, defaultEffort: effortDefault, effortLevels: effortAll5},
	// Sonnet 4.6 — 1M on every surface incl. Foundry; Sonnet 4.5 — 200K everywhere.
	// Sonnet 4.6: 4 effort levels (no xhigh). Sonnet 4.5: no effort support.
	{prefix: "claude-sonnet-4-6", standard: 1_000_000, defaultEffort: effortDefault, effortLevels: effortNoXHigh},
	{prefix: "claude-sonnet-4-5", standard: 200_000},
	// Haiku 4.5 — 200K everywhere (its standard IS 200K, so no surface caps it).
	// No effort parameter support (VERIFIED 2026-06-27: not listed in the effort docs).
	{prefix: "claude-haiku", standard: 200_000},
	// Fable 5 — 1M on every surface (the Foundry doc names Fable 5 in its 1M list; GA on
	// API/Bedrock/Vertex). All 5 effort levels incl. xhigh (VERIFIED 2026-06-27).
	{prefix: "claude-fable", standard: 1_000_000, defaultEffort: effortDefault, effortLevels: effortAll5},
	// Mythos 5 — VERIFIED 2026-07-03 against models/overview: "shares Claude Fable
	// 5's specs and pricing" (1M context / 128K output, full effort set). It remains
	// Glasswing-gated; access is governed in modules/models, not hidden here. Only the
	// STANDARD window is asserted (backed by the sharing statement); no perSurface rows
	// are emitted — Mythos access is account-team-mediated (Anthropic/AWS/Google Cloud)
	// and the authority publishes no Foundry availability or per-surface window for it.
	{prefix: "claude-mythos-5", standard: 1_000_000, defaultEffort: effortDefault, effortLevels: effortAll5},
}

// contextWindowEntryFor resolves a model id to its overlay entry by the LONGEST
// matching prefix. ok is false when the id has no entry (the caller falls back to the
// coarse family window, never an invented one).
func contextWindowEntryFor(modelID string) (contextWindowEntry, bool) {
	id := strings.TrimSpace(modelID)
	best := -1
	for i, e := range contextWindowSchedule {
		if strings.HasPrefix(id, e.prefix) {
			if best < 0 || len(e.prefix) > len(contextWindowSchedule[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return contextWindowEntry{}, false
	}
	return contextWindowSchedule[best], true
}

// StandardContextWindow returns a model's STANDARD maximum input context (the window
// on the Claude API / Bedrock / Vertex / Claude Platform on AWS), matched by longest
// family prefix. ok is false when the id has no declared entry (the coarse family
// window applies). It is the precise, surface-aware successor to the single family
// ContextWindow for the current Claude generation.
func StandardContextWindow(modelID string) (int64, bool) {
	e, ok := contextWindowEntryFor(modelID)
	if !ok {
		return 0, false
	}
	return e.standard, true
}

// SurfaceContextWindow returns a model's EFFECTIVE maximum input context on one
// deployment surface: the per-surface override when one is published, else the
// standard window. ok is false when the id has no declared entry (an honest unknown —
// the caller falls back to the coarse family window and must not assume the standard
// applies). This is the value a router/governance check compares a request against.
func SurfaceContextWindow(surface model.Gateway, modelID string) (int64, bool) {
	e, ok := contextWindowEntryFor(modelID)
	if !ok {
		return 0, false
	}
	if w, has := e.perSurface[surface]; has {
		return w, true
	}
	return e.standard, true
}

// SurfaceContextWindowsFor returns the per-surface context-window overlay for a model
// id — one entry per modeled surface, each carrying that surface's EFFECTIVE window —
// but ONLY for a model whose window actually DIVERGES across surfaces (i.e. it has a
// per-surface override). A model that is uniform across surfaces returns nil: its
// single Model.ContextWindow already tells the whole story, so attaching a redundant
// six-row overlay to every model would be noise. Carried on modelprovider.Model so the
// catalog/governance views and any surface-aware router see the full picture (ANT2-01).
func SurfaceContextWindowsFor(modelID string) []modelprovider.SurfaceContextWindow {
	e, ok := contextWindowEntryFor(modelID)
	if !ok || len(e.perSurface) == 0 {
		return nil // unlisted, or uniform across surfaces — no per-surface divergence
	}
	surfaces := AllSurfaces()
	out := make([]modelprovider.SurfaceContextWindow, 0, len(surfaces))
	for _, sf := range surfaces {
		w := e.standard
		if override, has := e.perSurface[sf.Gateway]; has {
			w = override
		}
		out = append(out, modelprovider.SurfaceContextWindow{
			Surface:       sf.Gateway,
			ContextWindow: w,
			AsOf:          contextWindowAsOf,
		})
	}
	return out
}

// SurfaceContextVerdict is the outcome of the pre-flight context-window check for a
// (surface, model, request) triple. It NEVER decides to deny on its own: the caller's
// policy does (the default is flag-by-default, deny opt-in). Inspect Exceeds to
// flag/deny a request, and Capped to surface the structural divergence.
type SurfaceContextVerdict struct {
	// Surface and ModelRef echo the inputs.
	Surface  model.Gateway
	ModelRef string
	// Requested is the request's context size in tokens (0 when only the structural
	// divergence is being evaluated, not a concrete request).
	Requested int64
	// Effective is the surface's maximum input context for the model (0 when Known is
	// false — the model has no declared per-surface entry).
	Effective int64
	// Standard is the model's standard window across non-capped surfaces (0 when
	// unknown).
	Standard int64
	// Known reports whether the model has a declared per-surface overlay entry. When
	// false, Exceeds is false (an unknown window is never asserted to be exceeded — the
	// same open behavior the router's MinContextWindow filter uses, ARCHITECTURE.md).
	Known bool
	// Capped is true when Effective < Standard: this surface caps the model below its
	// standard window (a structural blind spot worth flagging even without a request).
	Capped bool
	// Exceeds is true when Requested > Effective and Known: the request will be
	// truncated or rejected at this surface. This is the flag/deny signal.
	Exceeds bool
}

// CheckContextWindowForSurface is the pre-flight router/governance CHECK: given
// the destination surface, model id and the request's context size, it reports whether
// the request fits the surface's effective window. It performs NO inference and NEVER
// denies — it returns the verdict a router or the inference client
// consumes to flag-by-default and deny only when a policy opts in. requestedTokens <= 0
// evaluates only the structural divergence (Capped), not a concrete request.
func CheckContextWindowForSurface(surface model.Gateway, modelID string, requestedTokens int64) SurfaceContextVerdict {
	v := SurfaceContextVerdict{Surface: surface, ModelRef: modelID, Requested: requestedTokens}
	eff, ok := SurfaceContextWindow(surface, modelID)
	if !ok {
		return v // unknown model: honest unknown, no assertion
	}
	std, _ := StandardContextWindow(modelID)
	v.Known = true
	v.Effective = eff
	v.Standard = std
	v.Capped = eff < std
	v.Exceeds = requestedTokens > 0 && requestedTokens > eff
	return v
}

// Finding builds the request-time surface_capability_divergence finding for a verdict
// whose request EXCEEDS the surface window (emit it; the gather emits the
// structural posture finding below). It is High severity — a concrete request WILL be
// truncated or rejected. ok is false when the request fits (nothing to report). The
// detail (model/surface/sizes) travels as a hash; the title is short and non-sensitive.
func (v SurfaceContextVerdict) Finding(now time.Time) (model.FindingReport, bool) {
	if !v.Exceeds {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingSurfaceCapabilityDivergence,
		Severity:    model.SeverityHigh,
		SubjectKind: subjectSurfaceCapability,
		SubjectRef:  v.ModelRef,
		Title: "Request context exceeds surface window: " + v.ModelRef + " on " + string(v.Surface) +
			" caps at " + tokenLabel(v.Effective) + " (request " + tokenLabel(v.Requested) + ", standard " + tokenLabel(v.Standard) + ")",
		DetailHash: redact.Hash(v.ModelRef + "|" + string(v.Surface) + "|requested=" + strconv.FormatInt(v.Requested, 10) +
			"|effective=" + strconv.FormatInt(v.Effective, 10) + "|standard=" + strconv.FormatInt(v.Standard, 10)),
		OccurredAt: now,
	}, true
}

// surfaceCapabilityDivergenceFinding builds the ONE declared posture finding when the
// connector's configured surface caps a current model's context window below its
// standard window (e.g. Foundry caps Opus 4.8 at 200K vs its 1M standard). It is
// DECLARED knowledge — like samplingDeprecationFinding it is emitted on a credentialed
// run independent of the Admin API, so a Microsoft Foundry estate (which exposes NO
// Admin/Usage API and short-circuits the rest of the gather) is still warned that its
// 1M assumption is silently wrong. ok is false on a surface that caps nothing (every
// non-Foundry surface today). Medium severity: a structural blind spot to surface, not
// a per-request failure (that is the High request-time finding above) — flag by
// default, deny opt-in per policy.
func (s *Source) surfaceCapabilityDivergenceFinding(at time.Time) (model.FindingReport, bool) {
	surface := s.surface().Gateway
	var capped []string
	for _, e := range contextWindowSchedule {
		if w, has := e.perSurface[surface]; has && w < e.standard {
			capped = append(capped, e.prefix+" ("+tokenLabel(w)+" vs "+tokenLabel(e.standard)+" standard)")
		}
	}
	if len(capped) == 0 {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingSurfaceCapabilityDivergence,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectSurfaceCapability,
		SubjectRef:  string(surface),
		Title: "Surface " + s.surface().DisplayName + " caps context window below standard for: " +
			strings.Join(capped, ", ") + " — requests beyond the cap truncate or reject (model_context_window_exceeded)",
		DetailHash: redact.Hash("surface=" + string(surface) + "|capped=" + strings.Join(capped, ",") + "|asof=" + contextWindowAsOf),
		OccurredAt: at,
	}, true
}

// DefaultEffortFor returns a model's default effort level (the level that applies
// when the effort parameter is omitted) and its supported effort tiers. ok is false
// when the id has no declared entry (the model does not support effort control).
func DefaultEffortFor(modelID string) (defaultEffort string, levels []string, ok bool) {
	e, found := contextWindowEntryFor(modelID)
	if !found || e.defaultEffort == "" {
		return "", nil, false
	}
	return e.defaultEffort, append([]string(nil), e.effortLevels...), true
}

// --- 300k-output beta (output-300k-2026-03-24) -----------------------------------
//
// Authority (VERIFIED 2026-06-27, platform.claude.com/docs/en/build-with-claude/
// extended-thinking + /batch-processing):
//   - Supported models: Opus 4.8, Opus 4.7, Opus 4.6, Sonnet 4.6.
//   - Sonnet 5 added by its own 2026-07-03 model-overview verification; its
//     SurfaceMaxOutput rows carry that row-local AsOf rather than bumping the
//     2026-06-27 stamp for the older entries.
//   - Max output: 300,000 tokens.
//   - Supported surface: Message Batches API ONLY (NOT standard Messages, NOT
//     Bedrock, NOT Vertex). Requires the beta header output-300k-2026-03-24.
//   - On the Claude API: surfaces direct + claude-platform-aws (both have
//     Batches: true in surfaces.go). Bedrock-mantle and bedrock-legacy also have
//     Batches: true, but the beta header is an Anthropic API feature and NOT
//     available on Bedrock/Vertex per the docs.
//
// This is modeled as declared data, NOT a context-window divergence (it is an
// output cap, not an input cap). Carried as SurfaceMaxOutput on the Model.

const (
	outputBetaAsOf = "2026-06-27"
	outputBetaName = "output-300k-2026-03-24"
	outputBetaMax  = 300_000
)

type outputBetaModel struct {
	prefix string
	asOf   string
}

// outputBetaModels are the model prefixes that support the 300k output beta.
var outputBetaModels = []outputBetaModel{
	{prefix: "claude-opus-4-8", asOf: outputBetaAsOf},
	{prefix: "claude-opus-4-7", asOf: outputBetaAsOf},
	{prefix: "claude-opus-4-6", asOf: outputBetaAsOf},
	{prefix: "claude-sonnet-5", asOf: "2026-07-03"},
	{prefix: "claude-sonnet-4-6", asOf: outputBetaAsOf},
}

// outputBetaSurfaces are the surfaces where the 300k output beta applies
// (Anthropic API surfaces with Batches support that accept beta headers).
var outputBetaSurfaces = []model.Gateway{
	model.GatewayDirect,
	model.GatewayClaudePlatformAWS,
}

// SurfaceMaxOutputsFor returns the per-surface max-output overlay for a model id,
// modeling the 300k-output beta. Returns nil for models that do not support the beta
// (the standard MaxOutputTokens applies on every surface). Only surfaces where the
// beta is available get the override; other surfaces keep the standard limit.
func SurfaceMaxOutputsFor(modelID string) []modelprovider.SurfaceMaxOutput {
	id := strings.TrimSpace(modelID)
	asOf := ""
	for _, m := range outputBetaModels {
		if strings.HasPrefix(id, m.prefix) {
			asOf = m.asOf
			break
		}
	}
	if asOf == "" {
		return nil
	}
	out := make([]modelprovider.SurfaceMaxOutput, 0, len(outputBetaSurfaces))
	for _, gw := range outputBetaSurfaces {
		out = append(out, modelprovider.SurfaceMaxOutput{
			Surface:         gw,
			MaxOutputTokens: outputBetaMax,
			Beta:            outputBetaName,
			AsOf:            asOf,
		})
	}
	return out
}

// tokenLabel renders a token count as a short human label (200000 -> "200K",
// 1000000 -> "1M"), for non-sensitive finding titles. Non-round counts fall back to
// the exact integer.
func tokenLabel(n int64) string {
	switch {
	case n != 0 && n%1_000_000 == 0:
		return strconv.FormatInt(n/1_000_000, 10) + "M"
	case n != 0 && n%1_000 == 0:
		return strconv.FormatInt(n/1_000, 10) + "K"
	default:
		return strconv.FormatInt(n, 10)
	}
}
