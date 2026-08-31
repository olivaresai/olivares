// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models MODEL LIFECYCLE per deployment surface (ANT2-03): retirement is
// PER-PLATFORM, not global. The Anthropic-operated surfaces (first-party API, Claude
// Platform on AWS, Foundry) retire a model on a different date than Bedrock and
// Vertex — verified: Sonnet 4 retires 2026-06-15 first-party vs 2026-09-14 on Vertex.
// A single "retired" flag would give a false migration deadline on half the estate.
// The registry now carries the full published schedule (deprecated-on date, per-
// surface retirement dates, recommended replacement) and a state evaluator
// (LifecycleStateFor) the gather uses to flag deprecated/retired models still IN USE
//. It also models the param DEPRECATION (temperature/top_p/top_k → 400 on
// Opus 4.7+ and on Fable 5 / Mythos 5): per this is Anthropic's
// deprecation, NOT a product bug, so the connector PRE-ADVISES (an informational
// finding + the inference client withholds the params), it does not "fix" anything.
// All dates are DECLARED reference data, AsOf-stamped; no retirement date is
// fabricated for a model/surface whose schedule the authority did not publish
// — absence is absence, and a registry hit with no date is "deprecated",
// never a guessed "retired" (deny-closed).
//
// Authority (verbatim, fetched 2026-06-09): …/about-claude/model-deprecations
// (ANT2-03) + the Claude Fable 5 / Mythos 5 launch notes (2026-06-09).
package claudeapi

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// subjectModelLifecycle is the FindingReport subject for lifecycle/param pre-advice.
const subjectModelLifecycle = "anthropic.model_lifecycle"

// lifecycleAsOf stamps the lifecycle schedule with the date it was recorded.
// Source: model-deprecations.md fetched 2026-06-09 (+ the 2026-06-09 Fable 5 /
// Mythos 5 launch page for the claude-mythos-preview note).
const lifecycleAsOf = "2026-06-09"

// retirementEntry is a declared lifecycle schedule for a model family, matched by id
// prefix. Only surfaces with a VERIFIED date appear in perSurface; a surface absent
// from it has no published retirement date (NOT "never" — unknown, ARCHITECTURE.md).
type retirementEntry struct {
	prefix string
	// deprecatedOn is the date the provider deprecated the family (ISO-8601) on the
	// Anthropic-operated surfaces — model-deprecations.md publishes ONE deprecation
	// date there (Bedrock/Vertex run their own schedules). Empty means "date not
	// published in the verified capture", never a guess.
	deprecatedOn string
	// perSurface maps surface -> ISO-8601 retirement date. An empty date means
	// "retirement announced, date not published" (e.g. claude-mythos-preview).
	perSurface map[model.Gateway]string
	// replacement is the recommended replacement model-deprecations.md PUBLISHES for
	// the family (carried onto ModelRetirement.ReplacementRef / LifecycleInfo). Empty
	// when the verified capture names none.
	replacement string
	// current is the current same-family model id. It is used ONLY to SUPPRESS a
	// self-referential schedule (so the current model does not inherit its family's
	// retirement via the prefix match).
	current string
	// toConfirm lists surfaces where the family is VERIFIED to be deprecated/served
	// but whose retirement date the operating authority has NOT published. It is
	// DESCRIPTIVE only — LifecycleReference materializes it as explicit
	// "to-confirm" rows (empty date) so the matrix never renders absence as "never
	// retires" — and it NEVER feeds the deny-closed evaluators (RetirementsFor /
	// LifecycleStateFor stay date-driven: a surface absent from perSurface is an
	// honest unknown, ARCHITECTURE.md). Provenance rule: each entry restates verified
	// knowledge already recorded in this file's comments, and where the authority
	// says only "Bedrock" it means BOTH modeled bedrock gateways (bedrock-legacy +
	// bedrock-mantle) — the deprecations page does not distinguish the two surfaces
	// we model.
	toConfirm []model.Gateway
	// exempt marks a carve-out entry: a LONGER prefix that wins the longest-prefix
	// match purely to shield a still-active id from a broader family entry (e.g.
	// claude-sonnet-4-5 inside the claude-sonnet-4 family). An exempt hit means "no
	// lifecycle schedule", never a date.
	exempt bool
}

// anthropicOperated builds the per-surface map for a retirement date published for
// the Anthropic-operated surfaces (direct Claude API, Claude Platform on AWS,
// Foundry) — model-deprecations.md dates apply to exactly those three. Bedrock and
// Vertex set their own schedules and are listed only when verified.
func anthropicOperated(date string) map[model.Gateway]string {
	return map[model.Gateway]string{
		model.GatewayDirect:            date,
		model.GatewayClaudePlatformAWS: date,
		model.GatewayFoundry:           date,
	}
}

// retirementSchedule is the declared, verified-only lifecycle reference
// (model-deprecations.md fetched 2026-06-09). Dates apply to the Anthropic-operated
// surfaces unless a surface-specific date is verified (Sonnet 4 on Vertex); a date
// the authority did not publish is ABSENT (to-confirm), never fabricated.
var retirementSchedule = []retirementEntry{
	// --- Deprecated, retirement scheduled ---
	{
		// Covers the alias claude-opus-4-1 and the dated claude-opus-4-1-20250805.
		// Vertex lists claude-opus-4-1@20250805 as deprecated with NO published date
		// → vertex omitted (absence, not "never").
		prefix:       "claude-opus-4-1",
		deprecatedOn: "2026-06-05",
		perSurface:   anthropicOperated("2026-08-05"),
		replacement:  "claude-opus-4-8",
		toConfirm:    []model.Gateway{model.GatewayVertex},
	},
	{
		// The dated claude-opus-4-20250514. The "-2025" tail keeps the prefix from
		// catching claude-opus-4-8/4-7/4-6/4-5 (they share "claude-opus-4-", so
		// longest-prefix alone cannot separate them). Vertex claude-opus-4@20250514
		// is deprecated with no published date → omitted.
		prefix:       "claude-opus-4-2025",
		deprecatedOn: "2026-04-14",
		perSurface:   anthropicOperated("2026-06-15"),
		replacement:  "claude-opus-4-8",
		toConfirm:    []model.Gateway{model.GatewayVertex},
	},
	{
		// The alias claude-opus-4-0 (same model as claude-opus-4-20250514).
		prefix:       "claude-opus-4-0",
		deprecatedOn: "2026-04-14",
		perSurface:   anthropicOperated("2026-06-15"),
		replacement:  "claude-opus-4-8",
		toConfirm:    []model.Gateway{model.GatewayVertex},
	},
	{
		// Sonnet 4 (claude-sonnet-4-20250514 / alias claude-sonnet-4-0): the ONE
		// entry whose per-surface divergence is verified (first-party 2026-06-15 vs
		// Vertex 2026-09-14). The Bedrock date the authority did not publish is left
		// ABSENT (to-confirm), never fabricated. The still-active 4.5/4.6 ids are
		// shielded by the exempt carve-outs below + the current suppression.
		prefix:       "claude-sonnet-4",
		deprecatedOn: "2026-04-14",
		perSurface: map[model.Gateway]string{
			model.GatewayDirect:            "2026-06-15",
			model.GatewayClaudePlatformAWS: "2026-06-15",
			model.GatewayFoundry:           "2026-06-15",
			model.GatewayVertex:            "2026-09-14",
		},
		replacement: "claude-sonnet-4-6",
		current:     "claude-sonnet-4-6", // suppression only (exact id)
		// "Bedrock" per the authority (no published date) ⇒ both modeled gateways.
		toConfirm: []model.Gateway{model.GatewayBedrockLegacy, model.GatewayBedrockMantle},
	},
	// Carve-outs: claude-sonnet-4-5 / claude-sonnet-4-6 (and their dated forms) are
	// NOT on the deprecations page — these longer prefixes win the longest-prefix
	// match so the claude-sonnet-4 family entry never marks an active id deprecated.
	{prefix: "claude-sonnet-4-5", exempt: true},
	{prefix: "claude-sonnet-4-6", exempt: true},
	{
		// Deprecated 2026-06-09 per the model-deprecations.md note: "will be retired
		// after claude-mythos-5 becomes available" (Mythos 5 GA'd 2026-06-09). The
		// retirement date is unpublished → empty dates (announced, not scheduled).
		prefix:       "claude-mythos-preview",
		deprecatedOn: "2026-06-09",
		perSurface:   anthropicOperated(""),
		replacement:  "claude-mythos-5",
	},

	// --- Retired on the Anthropic-operated surfaces (requests FAIL there) ---
	{
		prefix:       "claude-3-7-sonnet",
		deprecatedOn: "2025-10-28",
		perSurface:   anthropicOperated("2026-02-19"),
		replacement:  "claude-sonnet-4-6",
	},
	{
		// Still served on Bedrock/Vertex (their dates unpublished → omitted).
		prefix:       "claude-3-5-haiku",
		deprecatedOn: "2025-12-19",
		perSurface:   anthropicOperated("2026-02-19"),
		replacement:  "claude-haiku-4-5-20251001",
		toConfirm:    []model.Gateway{model.GatewayBedrockLegacy, model.GatewayBedrockMantle, model.GatewayVertex},
	},
	{
		prefix:       "claude-3-haiku",
		deprecatedOn: "2026-02-19",
		perSurface:   anthropicOperated("2026-04-20"),
		replacement:  "claude-haiku-4-5-20251001",
	},
	{
		prefix:       "claude-3-opus",
		deprecatedOn: "2025-06-30",
		perSurface:   anthropicOperated("2026-01-05"),
		replacement:  "claude-opus-4-8",
	},
	{
		// Covers both dated ids (20241022 and 20240620) — same schedule.
		prefix:       "claude-3-5-sonnet",
		deprecatedOn: "2025-08-13",
		perSurface:   anthropicOperated("2025-10-28"),
		replacement:  "claude-sonnet-4-6",
	},
	{
		prefix:       "claude-3-sonnet",
		deprecatedOn: "2025-01-21",
		perSurface:   anthropicOperated("2025-07-21"),
		replacement:  "claude-sonnet-4-6",
	},
	{
		// claude-2.0 / claude-2.1: retired per the deprecations page, but the
		// deprecation/retirement dates and replacement were NOT in the verified
		// capture (to-confirm) — modeled as a dateless registry hit, which the
		// deny-closed evaluator reports as "deprecated", never a guessed "retired".
		prefix:     "claude-2.",
		perSurface: anthropicOperated(""),
	},
}

// lifecycleEntryFor resolves a model id to its lifecycle schedule entry by the
// LONGEST matching prefix, applying the current-id and exempt suppressions. ok is
// false when the model has no (effective) schedule — the common case.
func lifecycleEntryFor(modelID string) (retirementEntry, bool) {
	id := strings.TrimSpace(modelID)
	best := -1
	for i, e := range retirementSchedule {
		if strings.HasPrefix(id, e.prefix) {
			if best < 0 || len(e.prefix) > len(retirementSchedule[best].prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return retirementEntry{}, false
	}
	e := retirementSchedule[best]
	// An exempt carve-out, or the current same-family model, is not retiring: no
	// schedule (so claude-sonnet-4-6 never reports its predecessor's sunset).
	if e.exempt || (e.current != "" && id == e.current) {
		return retirementEntry{}, false
	}
	return e, true
}

// RetirementsFor returns the per-surface lifecycle schedule for a model id, matched
// by the longest family prefix. Empty when no schedule is known (the common case: a
// model with no published sunset). Each entry is AsOf-stamped so a migration plan
// can weigh staleness, and carries the deprecation date and the replacement
// model-deprecations.md PUBLISHES as the recommended successor (no longer an
// unmarked inference — the page names one per deprecated family).
func RetirementsFor(modelID string) []modelprovider.ModelRetirement {
	e, ok := lifecycleEntryFor(modelID)
	if !ok {
		return nil
	}
	out := make([]modelprovider.ModelRetirement, 0, len(e.perSurface))
	for surface, date := range e.perSurface {
		out = append(out, modelprovider.ModelRetirement{
			Surface:        surface,
			DeprecatedOn:   e.deprecatedOn,
			RetiresOn:      date,
			ReplacementRef: e.replacement,
			AsOf:           lifecycleAsOf,
		})
	}
	sortRetirements(out)
	return out
}

// sortRetirements orders the schedule by surface for a stable, diff-friendly catalog.
func sortRetirements(rs []modelprovider.ModelRetirement) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].Surface > rs[j].Surface; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}

// LifecycleState is the lifecycle vocabulary of model-deprecations.md
// (Active/Legacy/Deprecated/Retired) collapsed to the three states governance acts
// on: a "legacy" model is not on the deprecations schedule and evaluates as active.
type LifecycleState string

const (
	// LifecycleActive: no registry hit — the model has no published sunset.
	LifecycleActive LifecycleState = "active"
	// LifecycleDeprecated: on the schedule, before the surface's retirement date —
	// or with NO date published for the queried surface (deny-closed: a missing
	// date never escalates to retired).
	LifecycleDeprecated LifecycleState = "deprecated"
	// LifecycleRetired: on/after the surface's retirement date — requests FAIL.
	LifecycleRetired LifecycleState = "retired"
)

// LifecycleInfo is the schedule detail behind a LifecycleStateFor verdict, for the
// queried surface. Empty fields mean "not published" (ARCHITECTURE.md), never zero-values
// invented by the evaluator.
type LifecycleInfo struct {
	// DeprecatedOn is the published deprecation date ("" = not published).
	DeprecatedOn string
	// RetiresOn is the queried surface's retirement date ("" = not published for it).
	RetiresOn string
	// ReplacementRef is the published recommended replacement ("" = none named).
	ReplacementRef string
	// AsOf stamps when the schedule was recorded.
	AsOf string
}

// LifecycleStateFor evaluates the declared lifecycle registry for a model id on one
// deployment surface at a given instant. No registry hit = active. A hit is
// deprecated until the queried surface's retirement date and retired on/after it;
// a hit with no date for the queried surface stays deprecated — deny-closed, the
// evaluator never guesses a retirement the authority did not publish (ARCHITECTURE.md).
func LifecycleStateFor(modelID string, surface model.Gateway, now time.Time) (LifecycleState, LifecycleInfo) {
	e, ok := lifecycleEntryFor(modelID)
	if !ok {
		return LifecycleActive, LifecycleInfo{}
	}
	info := LifecycleInfo{
		DeprecatedOn:   e.deprecatedOn,
		RetiresOn:      e.perSurface[surface],
		ReplacementRef: e.replacement,
		AsOf:           lifecycleAsOf,
	}
	if info.RetiresOn != "" {
		if d, err := time.Parse("2006-01-02", info.RetiresOn); err == nil && !now.Before(d) {
			return LifecycleRetired, info
		}
	}
	return LifecycleDeprecated, info
}

// lifecycleRetirementSoon is the migration-urgency window: a deprecated model whose
// retirement date falls within this horizon grades High instead of Medium (the
// deprecations policy guarantees >=60 days notice; 30 days marks the late half).
const lifecycleRetirementSoon = 30 * 24 * time.Hour

// deprecatedModelFinding builds the ONE deprecated_model_in_use finding for a model
// observed in use whose lifecycle state on the connector's surface is deprecated or
// retired. Severity grades the migration urgency: retired = Critical
// (requests FAIL), deprecated with retirement within lifecycleRetirementSoon = High,
// otherwise Medium (including a hit with no published date — deny-closed). The title
// is short and non-sensitive (model id + dates only); the schedule tuple travels as
// a hash. ok is false for an active model.
func deprecatedModelFinding(modelID string, surface model.Gateway, now time.Time) (model.FindingReport, bool) {
	state, info := LifecycleStateFor(modelID, surface, now)
	if state == LifecycleActive {
		return model.FindingReport{}, false
	}
	sev := model.SeverityMedium
	var title string
	switch {
	case state == LifecycleRetired:
		sev = model.SeverityCritical
		title = "Retired model still in use: " + modelID + " retired " + info.RetiresOn + " on " + string(surface)
	case info.RetiresOn != "":
		title = "Deprecated model in use: " + modelID + " retires " + info.RetiresOn + " on " + string(surface)
		if d, err := time.Parse("2006-01-02", info.RetiresOn); err == nil && !d.After(now.Add(lifecycleRetirementSoon)) {
			sev = model.SeverityHigh
		}
	default:
		title = "Deprecated model in use: " + modelID + " (retirement date unpublished) on " + string(surface)
	}
	if info.ReplacementRef != "" {
		title += " (replacement: " + info.ReplacementRef + ")"
	}
	return model.FindingReport{
		Kind:        "deprecated_model_in_use",
		Severity:    sev,
		SubjectKind: subjectModelLifecycle,
		SubjectRef:  modelID,
		Title:       title,
		DetailHash: redact.Hash(modelID + "|" + string(surface) + "|" + info.DeprecatedOn + "|" +
			info.RetiresOn + "|" + info.ReplacementRef + "|" + string(state)),
		OccurredAt: now,
	}, true
}

// modelRecorder wraps the gather sink and records the DISTINCT model id of every
// CostSample that passes through it — the usage rows, billed cost rows and Claude
// Code model_breakdown spans all emit CostSamples — so the lifecycle check after the
// pulls covers exactly the models observed IN USE this gather. It records and
// forwards; it never mutates an observation.
type modelRecorder struct {
	sink sdk.Sink
	seen map[string]struct{}
}

// newModelRecorder wraps sink with model-id recording.
func newModelRecorder(sink sdk.Sink) *modelRecorder {
	return &modelRecorder{sink: sink, seen: map[string]struct{}{}}
}

// Emit records a CostSample's model ref and forwards the observation unchanged.
func (r *modelRecorder) Emit(ctx context.Context, o model.Observation) error {
	if cs, ok := o.(model.CostSample); ok && cs.ModelRef != "" {
		r.seen[cs.ModelRef] = struct{}{}
	}
	return r.sink.Emit(ctx, o)
}

// models returns the recorded ids SORTED, so finding emission is deterministic.
func (r *modelRecorder) models() []string {
	out := make([]string, 0, len(r.seen))
	for id := range r.seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// emitLifecycleFindings emits one deprecated_model_in_use finding per observed model
// whose lifecycle state on the connector's configured surface is deprecated or
// retired. modelIDs must be the recorder's sorted set (deterministic order).
func (s *Source) emitLifecycleFindings(ctx context.Context, sink sdk.Sink, modelIDs []string) error {
	at := s.clock().UTC()
	surface := s.surface().Gateway
	for _, id := range modelIDs {
		f, ok := deprecatedModelFinding(id, surface, at)
		if !ok {
			continue
		}
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// RejectsSamplingParams reports whether a model rejects non-default temperature/
// top_p/top_k with a 400 (ANT2-03 param-deprecation): Opus 4.7 and later, plus
// Fable 5 and Mythos 5 (their 2026-06-09 launch page states sampling params are
// rejected exactly like Opus 4.7+; claude-mythos-preview is NOT listed there and
// stays false — fail-closed, only verified ids opt in). For Opus it parses the
// minor version; a non-Opus or pre-4.7 id returns false. This is the signal the
// inference client uses to WITHHOLD those params (pre-advice), and the catalog
// uses to emit the informational deprecation finding — it is Anthropic's
// deprecation, not a product bug, so it is surfaced, never silently
// worked around.
func RejectsSamplingParams(modelID string) bool {
	id := strings.TrimSpace(modelID)
	if strings.HasPrefix(id, "claude-fable-5") || strings.HasPrefix(id, "claude-mythos-5") {
		return true
	}
	const p = "claude-opus-"
	rest, ok := strings.CutPrefix(id, p)
	if !ok {
		return false
	}
	// rest looks like "4-7", "4-8", "5-0", possibly with a trailing date suffix.
	parts := strings.SplitN(rest, "-", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major > 4 {
		return true // Opus 5+
	}
	return major == 4 && minor >= 7 // Opus 4.7+
}

// samplingDeprecationFinding emits an informational PRE-ADVICE finding when the
// declared catalog contains models that reject non-default temperature/top_p/top_k
// (Opus 4.7+, Fable 5, Mythos 5) — so an operator migrating a model id is warned
// BEFORE the 400, not after (ANT2-03). It is Anthropic's deprecation, not a product
// bug: the finding informs, it does not claim a fix. It is independent
// of any credential (declared knowledge), so it is emitted even in offline mode. ok
// is false when no declared model is affected.
func samplingDeprecationFinding(at time.Time) (model.FindingReport, bool) {
	var affected []string
	for _, d := range declaredModelIDs {
		if RejectsSamplingParams(d.id) {
			affected = append(affected, d.id)
		}
	}
	if len(affected) == 0 {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        "deprecation",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectModelLifecycle,
		SubjectRef:  "sampling_params",
		Title:       "Models reject non-default temperature/top_p/top_k (Opus 4.7+, Fable/Mythos 5) — withhold these params",
		DetailHash:  redact.Hash("opus-4.7+/fable-5/mythos-5 reject temperature/top_p/top_k with 400; affected=" + strings.Join(affected, ",") + " (ANT2-03; Anthropic deprecation, not a product bug)"),
		OccurredAt:  at,
	}, true
}
