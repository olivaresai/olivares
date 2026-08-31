// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Content-capture categories, mirroring Claude Code's own OTEL_LOG_* opt-in flags
// (OBS-10). The connector is STRUCTURAL-BY-DEFAULT: it retains durations, model and
// tool names, counts, and sanitized resource references, and NO raw content unless
// the operator consciously opts a category in. This preserves Anthropic's
// default-redacted posture when the control plane re-exports to a SIEM/OCSF lake.
//
//   - user_prompts   ⟷ OTEL_LOG_USER_PROMPTS    — prompt text
//   - tool_details   ⟷ OTEL_LOG_TOOL_DETAILS    — tool input arguments
//   - tool_content   ⟷ OTEL_LOG_TOOL_CONTENT    — full tool input/output bodies
//   - raw_api_bodies ⟷ OTEL_LOG_RAW_API_BODIES  — full Messages API request/response
//
// Extended-thinking content is ALWAYS redacted — there is no flag for it.
// Today the only ingest surface through which raw content can reach the connector
// is the tracing-beta span events (OBS-03), which carry tool content and raw API
// bodies; those are dropped unless the matching category is enabled here. The
// log-event path the connector maps never carries prompt text, tool bodies or API
// bodies — it is sanitized-ref / count / name only by construction.
// Source: https://code.claude.com/docs/en/monitoring-usage (Security and privacy).
const (
	capUserPrompts  = "user_prompts"
	capToolDetails  = "tool_details"
	capToolContent  = "tool_content"
	capRawAPIBodies = "raw_api_bodies"
)

// allCaptureCategories is the closed set of recognized categories, in stable order.
var allCaptureCategories = []string{capUserPrompts, capToolDetails, capToolContent, capRawAPIBodies}

// redactionPolicy is the connector's resolved content-capture posture. The zero
// value is fully structural (everything redacted) — the safe default.
type redactionPolicy struct {
	userPrompts  bool
	toolDetails  bool
	toolContent  bool
	rawAPIBodies bool
}

// parseRedaction builds a policy from a comma-separated allowlist of categories
// (e.g. "tool_content,user_prompts"). Empty or whitespace yields the fully
// structural posture. Unknown tokens are ignored (a future/typo'd category must
// degrade safe, never silently widen capture). Recognized tokens are reported so
// the caller can surface exactly what was honored.
func parseRedaction(list string) (redactionPolicy, []string) {
	var p redactionPolicy
	var honored []string
	for _, raw := range strings.Split(list, ",") {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case capUserPrompts:
			p.userPrompts, honored = true, append(honored, capUserPrompts)
		case capToolDetails:
			p.toolDetails, honored = true, append(honored, capToolDetails)
		case capToolContent:
			p.toolContent, honored = true, append(honored, capToolContent)
		case capRawAPIBodies:
			p.rawAPIBodies, honored = true, append(honored, capRawAPIBodies)
		}
	}
	sort.Strings(honored)
	return p, honored
}

// allows reports whether a content category may be retained under this policy.
func (p redactionPolicy) allows(category string) bool {
	switch category {
	case capUserPrompts:
		return p.userPrompts
	case capToolDetails:
		return p.toolDetails
	case capToolContent:
		return p.toolContent
	case capRawAPIBodies:
		return p.rawAPIBodies
	default:
		return false
	}
}

// summary renders the active posture for the self-audit finding: "structural-only"
// when nothing is opted in, else the sorted list of enabled categories.
func (p redactionPolicy) summary() string {
	var on []string
	for _, c := range allCaptureCategories {
		if p.allows(c) {
			on = append(on, c)
		}
	}
	if len(on) == 0 {
		return "structural-only"
	}
	return "structural+" + strings.Join(on, "+")
}

// selfAuditFinding records the connector's active redaction posture as a
// low-severity self-audit finding at Gather start, so the tamper-evident ledger
// (and any SIEM export) carries proof of exactly what content the connector was
// permitted to retain (OBS-10: "surface the active redaction posture in
// self-audit"). It is emitted once per run.
func (p redactionPolicy) selfAuditFinding(at time.Time) model.FindingReport {
	posture := p.summary()
	return model.FindingReport{
		Kind:        "self_audit",
		Severity:    model.SeverityLow,
		SubjectKind: "connector",
		SubjectRef:  Name,
		Title:       "Claude telemetry content-capture posture: " + posture,
		DetailHash:  redact.Hash("redaction|" + posture),
		OccurredAt:  at,
	}
}
