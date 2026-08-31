// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

import (
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// This file classifies the Anthropic Compliance API Activity Feed event types into
// non-sensitive CATEGORIES (so the ledger/SIEM can group and prioritize hundreds of
// activity types instead of one opaque blob), and it documents — HONESTLY, as the product's
// posture demands — the feed's known coverage GAPS so a buyer is never sold a
// completeness the API does not deliver.
//
// Honesty on the type set (ARCHITECTURE.md, mirrors claude-api's knownBetaHeaders): the
// Activity Feed produces hundreds of distinct activity types, but rather than fabricate
// an exhaustive list to hit a count, this classifies by the documented type-name FAMILIES
// (prefix/substring) plus a verified subset of exact types. It is FORWARD-COMPATIBLE:
// a new type Anthropic adds either matches a known family or falls to CategoryOther
// (an honest "unclassified", never a guessed category). Re-verify the live type set on
// the next build against the Compliance API reference.
//
// Authority: https://platform.claude.com/docs/en/manage-claude/compliance-api and its
// compliance-integration-patterns subpage (feed-consumption / SIEM-correlation /
// retention). The gap facts are corroborated by the General Analysis Compliance-API
// guide (references).

// ActivityCategory is the non-sensitive class of a Compliance activity record.
type ActivityCategory string

const (
	// CategoryChat is conversation/chat lifecycle (claude_chat_*).
	CategoryChat ActivityCategory = "chat"
	// CategoryProject is project lifecycle (claude_project_*).
	CategoryProject ActivityCategory = "project"
	// CategoryAuthn is sign-in/sign-out/session activity.
	CategoryAuthn ActivityCategory = "authn"
	// CategoryMembership is org/workspace membership and role changes (the
	// privilege-relevant family).
	CategoryMembership ActivityCategory = "membership"
	// CategoryCredential is API-key / token lifecycle.
	CategoryCredential ActivityCategory = "credential"
	// CategoryWorkspace is workspace configuration.
	CategoryWorkspace ActivityCategory = "workspace"
	// CategoryAdmin is org-level configuration / policy / settings changes.
	CategoryAdmin ActivityCategory = "admin"
	// CategoryData is data export / file / eDiscovery / deletion activity.
	CategoryData ActivityCategory = "data"
	// CategoryAudit is audit/log-access activity, including Compliance API self-access.
	CategoryAudit ActivityCategory = "audit"
	// CategoryOther is an unclassified type (honest unknown — never a guessed class).
	CategoryOther ActivityCategory = "other"
)

// ActivityClass is the classification of one activity type: its category and whether
// it is SECURITY-RELEVANT (a privilege/credential/data event a SOC prioritizes for
// correlation). Security-relevance is carried for the consuming SIEM/compliance module;
// the emitted evidence stays Info severity (this connector reports evidence, not alerts).
type ActivityClass struct {
	Category         ActivityCategory
	SecurityRelevant bool
}

// classifyRule binds a matcher to a class. Rules are evaluated in order; the first
// match wins, so more specific families precede broader ones.
type classifyRule struct {
	// contains matches when the lower-cased type CONTAINS this substring.
	contains string
	class    ActivityClass
}

// activityRules is the ordered classifier. It keys on documented type-name families
// (substring), not a fabricated exhaustive enum, so it stays correct as the feed grows.
// Security-relevant families: membership/role, credential, data export/delete, and
// authentication (sign-in is the anchor for anomaly correlation).
var activityRules = []classifyRule{
	{"compliance_api_accessed", ActivityClass{CategoryAudit, true}}, // Compliance API self-audit
	{"role", ActivityClass{CategoryMembership, true}},               // *_role_changed / role_assigned
	{"invite", ActivityClass{CategoryMembership, true}},             // user_invited / invite_*
	{"member", ActivityClass{CategoryMembership, true}},             // member_added / removed
	{"api_key", ActivityClass{CategoryCredential, true}},            // api_key_created / deleted
	{"apikey", ActivityClass{CategoryCredential, true}},             // apikey_* (alt spelling)
	{"token", ActivityClass{CategoryCredential, true}},              // token_* (oauth/wif)
	{"export", ActivityClass{CategoryData, true}},                   // data_export* / *_exported
	{"delete", ActivityClass{CategoryData, true}},                   // *_deleted (eDiscovery/RTBF surface)
	{"ediscovery", ActivityClass{CategoryData, true}},               // ediscovery_*
	{"signed_in", ActivityClass{CategoryAuthn, true}},               // user_signed_in
	{"sign_in", ActivityClass{CategoryAuthn, true}},                 // alt
	{"signed_out", ActivityClass{CategoryAuthn, false}},             // user_signed_out
	{"sign_out", ActivityClass{CategoryAuthn, false}},               // alt
	{"login", ActivityClass{CategoryAuthn, true}},                   // login_*
	{"chat", ActivityClass{CategoryChat, false}},                    // claude_chat_*
	{"conversation", ActivityClass{CategoryChat, false}},            // conversation_*
	{"project", ActivityClass{CategoryProject, false}},              // claude_project_*
	{"workspace", ActivityClass{CategoryWorkspace, false}},          // workspace_*
	{"setting", ActivityClass{CategoryAdmin, true}},                 // *_settings_updated (policy posture)
	{"policy", ActivityClass{CategoryAdmin, true}},                  // policy_*
	{"organization", ActivityClass{CategoryAdmin, true}},            // organization_*
	{"org_", ActivityClass{CategoryAdmin, true}},                    // org_*
}

// ClassifyActivity classifies an Activity Feed event type into its non-sensitive
// category and security-relevance. An empty or unrecognized type yields CategoryOther
// (an honest unclassified), never a guessed class. It is exported so the consuming
// compliance/SIEM module can re-derive the same category from the type carried in the
// finding (the connector and the consumer share one taxonomy).
func ClassifyActivity(eventType string) ActivityClass {
	t := strings.ToLower(strings.TrimSpace(eventType))
	if t == "" {
		return ActivityClass{CategoryOther, false}
	}
	for _, r := range activityRules {
		if strings.Contains(t, r.contains) {
			return r.class
		}
	}
	return ActivityClass{CategoryOther, false}
}

// ComplianceGap is one HONESTLY-documented coverage limit of the Activity Feed ingest.
// Owner names the session/path that closes (or owns) the gap, so the gap is tracked,
// not hand-waved.
type ComplianceGap struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Owner  string `json:"owner"`
}

// complianceGaps is the verified gap inventory (AsOf 2026-06-08). These are the limits
// the product STATES rather than papering over — the difference between an honest
// evidence connector and an over-promise.
var complianceGaps = []ComplianceGap{
	{
		ID:     "enterprise-gated",
		Title:  "Compliance API is Enterprise-gated",
		Detail: "the /v1/compliance/* endpoints require a Claude Enterprise plan; without it this connector degrades honestly (offline → no activity evidence emitted, never fabricated).",
		Owner:  "Anthropic plan gating (documented, not closable here)",
	},
	{
		ID:     "zdr-content-excluded",
		Title:  "ZDR excludes code-exec / MCP / Files content",
		Detail: "under Zero-Data-Retention the feed does NOT capture code-execution, MCP tool, or Files content; activity records still flow but their content is out of retention — the data-plane R/RW signal must come from the OTEL/hooks path, not this feed.",
		Owner:  "Anthropic ZDR design (documented limitation)",
	},
	{
		ID:     "cowork-not-captured",
		Title:  "Claude Cowork activity is not captured",
		Detail: "the Activity Feed covers Claude.ai/Claude Code organizational activity; Cowork has its own OTEL/Analytics governance surface — tracked separately, not merged here.",
		Owner:  "(Cowork governance/observability)",
	},
	{
		ID:     "no-eu-only-routing",
		Title:  "No EU-only routing for the feed",
		Detail: "the Compliance API has no EU-only residency/routing option; data-residency is governed at the workspace geo / inference_geo level (claude-api ANT2-06/17), not by this feed.",
		Owner:  "Anthropic residency design (documented limitation)",
	},
	{
		ID:     "content-via-governed-seam",
		Title:  "Content + RTBF DELETE are a SEPARATE governed surface, not the activity feed",
		Detail: "the Activity Feed ingest stays GET-only/minimal-data. Adds (content.go), on a DISTINCT Compliance Access Key: content ENUMERATION (references only — the connector never routes raw customer content through the ledger) and the permanent, irreversible RTBF eDiscovery DELETE as a DUAL-CONTROL (two-person) + HITL + deny-closed actuator whose delete:compliance_user_data key and bridge are wired at the composition root — never an automatic poll. The org DIRECTORY (orgs/users/roles/groups) is ingested (directory.go), including the SCIM-provisioning signal the Admin API cannot see.",
		Owner:  "(content.go dual-control RTBF eraser + enumeration; directory.go)",
	},
}

// ComplianceGaps returns the documented coverage gaps (a copy, so a caller cannot
// mutate package state). It is the honest companion to the activity evidence: what the
// feed does NOT cover, and who owns each gap.
func ComplianceGaps() []ComplianceGap {
	return append([]ComplianceGap(nil), complianceGaps...)
}

// coverageFinding emits a single posture finding per online Gather that records the
// documented coverage gaps to the ledger (Info — this is documentation, not an alert).
// The detail is a one-way hash of the gap ids + the org ref, so the evidence is
// tamper-evident without surfacing anything sensitive (there is nothing sensitive here,
// but the connector keeps a uniform minimal-data shape).
func (s *Source) coverageFinding(at time.Time) model.FindingReport {
	ids := make([]string, 0, len(complianceGaps))
	for _, g := range complianceGaps {
		ids = append(ids, g.ID)
	}
	return model.FindingReport{
		Kind:        findingKindCoverage,
		Severity:    model.SeverityInfo,
		SubjectKind: "claude_compliance",
		SubjectRef:  s.orgRef,
		Title:       "Claude Compliance ingest covers the event-log stream; " + strconv.Itoa(len(ids)) + " known gaps documented (ZDR/Cowork/EU-routing; content+RTBF via the governed seam)",
		DetailHash:  redact.Hash(s.orgRef + "|gaps|" + strings.Join(ids, ",")),
		OccurredAt:  at,
	}
}
