// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// shadowauth.go is the "shadow auth" detector over the Claude Code
// Analytics feed: a developer whose Claude Code usage bills as customer_type=api
// is running on a personal/API key OUTSIDE the org subscription. That is
// identity + cost drift, not a billing curiosity:
//
//   - identity: the developer's activity is not seat-attributed — forceLoginOrgUUID
//     (the managed login pin, connectors/managedsettings) exists precisely to
//     prevent this posture, and an api row is evidence it is not in force for them;
//   - cost: their spend rides an ungoverned key in usage_report instead of the
//     subscription seat the org provisioned (per-seat utilization undercounts).
//
// The sibling template is connectors/claude-wif detectShadowing (a static API key
// shadowing federation). Severity here is MEDIUM, not High: the feed proves the
// posture exists but not how the key is scoped; the org pin drift (managedsettings,
// High) is the enforcement-side signal.
//
// HONESTY BOUNDARY (VERIFIED 2026-06-10, docs.claude.com/en/api/
// claude-code-analytics-api): the feed "only tracks Claude Code usage on the
// Claude API" — Claude Platform on AWS, Microsoft Foundry, Amazon Bedrock and
// Vertex AI are NOT included. The detector therefore covers Claude-API-served
// usage only; absence of findings is NOT evidence of absence for 3P-provider
// fleets (the OTel plane, connectors/claude, is their observation path).

// findingKindGovernance matches the house kind the wif shadow detector uses.
const findingKindGovernance = "governance"

// shadowAuthFinding builds the per-developer/day shadow-auth finding for an
// api-billed feed record. ok is false when the record carries no actor to
// attribute (an unattributable row signals nothing actionable). The Title names
// the actor and posture only — never usage figures (the detector signals drift,
// not recon); the day and customer_type bind the DetailHash.
func shadowAuthFinding(rec claudeCodeRecord, at time.Time) (model.FindingReport, bool) {
	actor := rec.Actor.ref()
	if actor == "" {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindGovernance,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectClaudeCodeDeveloper,
		SubjectRef:  actor,
		Title:       "Claude Code shadow auth: " + actor + " runs on an API key outside the org subscription",
		DetailHash:  redact.Hash("claude_code_shadow_auth|org=" + rec.OrganizationID + "|actor=" + actor + "|date=" + rec.Date + "|customer_type=" + rec.CustomerType),
		OccurredAt:  at,
	}, true
}
