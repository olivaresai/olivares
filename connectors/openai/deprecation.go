// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// deprecation.go implements the Assistants API deprecation-risk detector.
// OpenAI removes the Assistants API on 2026-08-26 — assistants, threads, runs
// and messages; files and vector stores SURVIVE (the Responses file_search tool
// is built on vector stores), so they get no deprecation findings. Threads/runs
// have no list endpoints, so assistants are the only enumerable at-risk objects.
//
// While the API still answers, every active assistant emits a deprecation_risk
// finding whose severity escalates as the sunset approaches, plus one posture
// summary. Once the API is removed upstream, gatherAssistants degrades to an
// Info posture finding: the removal is the EXPECTED sunset, not a permission
// problem, and must never read as a permanent health error.
//
// The migration target is Responses + Conversations. The guidance deliberately
// warns AGAINST migrating to prompt objects: /v1/prompts is itself deprecated
// (sunset 2026-11-30), so pointing operators at it would trade one dying API
// for another.
package openai

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// assistantsSunset is the announced Assistants API removal date (deprecation
// announced 2025-08-26; source: developers.openai.com/api/docs/deprecations).
var assistantsSunset = time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)

const (
	assistantsSunsetDate        = "2026-08-26"
	assistantsMigrationGuidance = "Migrate Assistants API workloads to the OpenAI Responses API plus Conversations API (https://developers.openai.com/api/docs/guides/migrate-to-responses); do not migrate to prompt objects because /v1/prompts is also deprecated and sunsets 2026-11-30."
)

// sunsetSeverity grades the urgency of a sunsetting surface: more than 90 days
// out is low, 31-90 medium, and 30 or fewer — including a passed deadline while
// the API still answers — high.
func sunsetSeverity(now, deadline time.Time) model.Severity {
	daysLeft := sunsetDaysLeft(now, deadline)
	switch {
	case daysLeft > 90:
		return model.SeverityLow
	case daysLeft >= 31:
		return model.SeverityMedium
	default:
		return model.SeverityHigh
	}
}

// assistantDeprecationFinding builds the per-assistant deprecation_risk
// finding. The Title carries the deadline and days left (or that it passed);
// the rule id (openai_assistants_deprecated) and the full migration guidance
// travel in the DetailHash (minimal-data).
func (s *Source) assistantDeprecationFinding(a assistantEntry) model.FindingReport {
	now := s.clock().UTC()
	daysLeft := sunsetDaysLeft(now, assistantsSunset)
	title := fmt.Sprintf("OpenAI assistant %q runs on the deprecated Assistants API (sunset %s, %d day(s) left): migrate to Responses/Conversations",
		truncateName(a.Name, 30), assistantsSunsetDate, daysLeft)
	if now.After(assistantsSunset) {
		title = fmt.Sprintf("OpenAI assistant %q runs on the deprecated Assistants API (sunset %s has passed): migrate to Responses/Conversations",
			truncateName(a.Name, 30), assistantsSunsetDate)
	}

	detail := fmt.Sprintf("rule=openai_assistants_deprecated id=%s model=%s deadline=%s days_left=%d guidance=%s",
		a.ID, a.Model, assistantsSunsetDate, daysLeft, assistantsMigrationGuidance)

	return model.FindingReport{
		Kind:        "deprecation_risk",
		Severity:    sunsetSeverity(now, assistantsSunset),
		SubjectKind: subjectAssistant,
		SubjectRef:  a.ID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  now,
	}
}

// assistantsDeprecationSummary is the one posture finding summarizing how many
// assistants still ride the sunsetting API, at the same escalating severity.
func (s *Source) assistantsDeprecationSummary(count int) model.FindingReport {
	now := s.clock().UTC()
	daysLeft := sunsetDaysLeft(now, assistantsSunset)
	title := fmt.Sprintf("OpenAI Assistants API sunset %s (%d day(s) left): %d assistant(s) still active.",
		assistantsSunsetDate, daysLeft, count)
	if now.After(assistantsSunset) {
		title = fmt.Sprintf("OpenAI Assistants API sunset %s has passed: %d assistant(s) still active.",
			assistantsSunsetDate, count)
	}

	detail := fmt.Sprintf("rule=openai_assistants_deprecated_summary count=%d deadline=%s days_left=%d guidance=%s",
		count, assistantsSunsetDate, daysLeft, assistantsMigrationGuidance)

	return model.FindingReport{
		Kind:        "posture",
		Severity:    sunsetSeverity(now, assistantsSunset),
		SubjectKind: subjectSurface,
		SubjectRef:  "assistants_deprecation",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  now,
	}
}

// emitAssistantsDeprecation emits one deprecation_risk finding per collected
// assistant plus the summary; with no assistants there is nothing at risk and
// it emits nothing.
func (s *Source) emitAssistantsDeprecation(ctx context.Context, sink sdk.Sink, assistants []assistantEntry) error {
	if len(assistants) == 0 {
		return nil
	}
	for _, a := range assistants {
		if err := sink.Emit(ctx, s.assistantDeprecationFinding(a)); err != nil {
			return err
		}
	}
	return sink.Emit(ctx, s.assistantsDeprecationSummary(len(assistants)))
}

// assistantsRemovedFinding is the post-sunset degradation finding: the
// Assistants API answering 403/404/410 on/after the sunset date is the EXPECTED
// upstream removal, so it reports Info — never the medium "permission or
// entitlement" posture — and never fails the gather.
func (s *Source) assistantsRemovedFinding() model.FindingReport {
	now := s.clock().UTC()
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectSurface,
		SubjectRef:  "assistants",
		Title: "OpenAI Assistants API removed upstream (sunset " + assistantsSunsetDate +
			" reached); disable the 'assistants' option or migrate remaining workloads to Responses/Conversations.",
		DetailHash: redact.Hash("rule=openai_assistants_removed surface=assistants path=/v1/assistants deadline=" +
			assistantsSunsetDate + " guidance=" + assistantsMigrationGuidance),
		OccurredAt: now,
	}
}

// sunsetDaysLeft is the whole number of days from now until the deadline
// (negative once the deadline has passed).
func sunsetDaysLeft(now, deadline time.Time) int {
	return int(math.Floor(deadline.Sub(now).Hours() / 24))
}
